package setu

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

// SetuPlugin Pixiv 涩图插件：群聊 @机器人或私聊发送 /setu 开车。
// 图片索引走 Mabbs 的 pixiv-index（jsDelivr CDN），index.json 只在启动与
// 定时刷新时全量拉取并缓存在内存里，每次请求只按需拉取命中的详情文件。
type SetuPlugin struct {
	plugin.Meta
	cfg setuConfig

	index       indexCache
	loadMu      sync.Mutex
	indexCtx    context.Context
	indexCancel context.CancelFunc

	mu    sync.Mutex
	users map[string]*userBucket
}

// userBucket 单用户的频率状态。
type userBucket struct {
	last  time.Time
	day   string
	count int
}

// NewPlugin 构造函数（plugin.json 的 entry.constructor 指向这里）。
func NewPlugin() *SetuPlugin {
	p := &SetuPlugin{users: make(map[string]*userBucket)}
	p.Name = "Pixiv涩图"
	p.HelpWords = "群里@我 /setu 来一张涩图，/setu 白丝 按 tag 搜，/setu help 看玩法"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.1.3"
	p.Order = plugin.LevelNormal
	return p
}

// Start 初始化：参数兜底、提醒配错的正则、异步预加载索引并启动定时刷新。
func (p *SetuPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.users == nil {
		p.users = make(map[string]*userBucket)
	}
	if p.cfg.MaxCount < 1 {
		p.cfg.MaxCount = 1
	}
	if p.cfg.MaxCount > 5 {
		p.cfg.MaxCount = 5
	}
	if p.cfg.CooldownSec < 0 {
		p.cfg.CooldownSec = 0
	}
	if p.cfg.DailyLimit < 0 {
		p.cfg.DailyLimit = 0
	}
	if p.cfg.MaxSearchTries <= 0 {
		p.cfg.MaxSearchTries = 15
	}
	if p.cfg.R18Mode != "0" && p.cfg.R18Mode != "1" && p.cfg.R18Mode != "2" {
		p.cfg.R18Mode = "1"
	}
	if strings.TrimSpace(p.cfg.IndexURL) == "" {
		p.cfg.IndexURL = defaultIndexURL
	}
	if strings.TrimSpace(p.cfg.DataBase) == "" {
		p.cfg.DataBase = defaultDataBase
	}
	for _, bad := range invalidPatterns(append(append([]string{}, p.cfg.AllowGroups...), p.cfg.AllowFriends...)) {
		p.Logger.Warn("放行名单正则无效，启动后该条不会命中任何会话", "pattern", bad)
	}
	p.indexCtx, p.indexCancel = context.WithCancel(context.Background())
	// 索引 5 万条约 1MB，异步预加载不阻塞启动；加载完之前来的请求走 ensureIndex 兜底同步拉一次。
	go func() {
		fctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := p.reloadIndex(fctx); err != nil {
			p.Logger.Warn("涩图索引初次加载失败，首次使用时再试", "error", err)
			return
		}
		p.Logger.Info("涩图索引加载完成", "size", p.index.size())
	}()
	go p.refreshLoop(p.indexCtx)
	p.Logger.Info("Pixiv涩图插件已初始化",
		"r18_mode", p.cfg.R18Mode,
		"allow_groups", len(p.cfg.AllowGroups),
		"allow_friends", len(p.cfg.AllowFriends),
		"cooldown_sec", p.cfg.CooldownSec,
		"daily_limit", p.cfg.DailyLimit,
	)
	return nil
}

// Awake 启动完成事件：汇报索引状态（仍在加载也如实说）。
func (p *SetuPlugin) Awake(ctx context.Context, b bot.Bot) error {
	if !p.cfg.Enable {
		p.Logger.Info("涩图插件已在配置中禁用")
		return nil
	}
	if n := p.index.size(); n > 0 {
		p.Logger.Info("涩图插件已就绪", "index_size", n)
	} else {
		p.Logger.Info("涩图插件已就绪，索引仍在后台加载中，首个请求会触发同步加载")
	}
	return nil
}

// OnUnload 卸载钩子：取消索引定时刷新 goroutine，并清空内存中的索引与
// 用户频率缓存（纯内存数据，无需区分卸载原因）。
func (p *SetuPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	if p.indexCancel != nil {
		p.indexCancel()
		p.indexCancel = nil
	}
	p.index.mu.Lock()
	p.index.files = nil
	p.index.updatedAt = time.Time{}
	p.index.mu.Unlock()
	p.mu.Lock()
	p.users = make(map[string]*userBucket)
	p.mu.Unlock()
	p.Logger.Info("已停止涩图索引刷新并清空缓存（卸载）", "reason", reason)
	return nil
}

// isSetuCmd 是否涩图命令（含中文别名）。
func isSetuCmd(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "setu", "涩图", "色图":
		return true
	default:
		return false
	}
}

// OnGroupMsg 群聊消息事件：必须 @机器人。
func (p *SetuPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if !cmd.Mention || !isSetuCmd(cmd.Name) {
		return true, nil
	}
	p.handleSetu(ctx, b, cmd.Args, msg, true)
	return false, nil
}

// OnFriendMsg 私聊消息事件：无需 @。
func (p *SetuPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if !isSetuCmd(cmd.Name) {
		return true, nil
	}
	p.handleSetu(ctx, b, cmd.Args, msg, false)
	return false, nil
}

func (p *SetuPlugin) isAdmin(msg message.Message) bool {
	return p.SystemConfig.AdminId != "" && msg.Sender.UserId == p.SystemConfig.AdminId
}

// handleSetu /setu 主流程：子命令 → 放行 → 限流 → 搜图 → 发送。
func (p *SetuPlugin) handleSetu(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	req := parseSetuArgs(args, p.cfg.MaxCount)
	nick := displayName(msg)
	admin := p.isAdmin(msg)
	bypass := admin && p.cfg.AdminBypass

	if req.isHelp {
		p.replyText(b, msg, isGroup, helpTextClean)
		return
	}
	if req.isStatus {
		p.replyText(b, msg, isGroup, p.statusText(msg, isGroup, bypass))
		return
	}
	if req.isRefresh {
		if !admin {
			p.replyText(b, msg, isGroup, "只有管理员能刷新索引缓存")
			return
		}
		p.replyText(b, msg, isGroup, "收到，正在刷新索引（5 万条左右，约几秒）……")
		rctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := p.reloadIndex(rctx); err != nil {
			p.Logger.Warn("管理员手动刷新索引失败", "error", err)
			p.replyText(b, msg, isGroup, "索引刷新失败："+err.Error())
			return
		}
		p.Logger.Info("管理员手动刷新索引完成", "size", p.index.size(), "admin", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, fmt.Sprintf("索引刷新完成，共 %d 条，好车多多~", p.index.size()))
		return
	}

	// 放行检查（help/status/refresh 之外的真实开车才拦）。
	if !bypass {
		allowed := false
		if isGroup {
			allowed = matchAllowlist(p.cfg.AllowGroups, msg.GroupId)
		} else {
			allowed = matchAllowlist(p.cfg.AllowFriends, msg.Sender.UserId)
		}
		if !allowed {
			p.Logger.Info("非放行会话请求涩图", "is_group", isGroup, "group", msg.GroupId, "user", msg.Sender.UserId)
			if p.cfg.SilentDeny {
				return
			}
			p.replyText(b, msg, isGroup, pickRand(denyMessages))
			return
		}
	}

	// 频率限制。
	if !bypass {
		if ok, kind, remain, left := p.takeQuota(msg.Sender.UserId); !ok {
			switch kind {
			case "cooldown":
				p.replyText(b, msg, isGroup, fmt.Sprintf(pickRand(cooldownMessages), int(remain.Seconds())+1))
			case "daily":
				p.replyText(b, msg, isGroup, fmt.Sprintf(pickRand(dailyMessages), p.cfg.DailyLimit, p.cfg.DailyLimit))
			default:
				_ = left
			}
			return
		}
	}

	// R18 意图。
	r18, sealed := resolveR18(p.cfg.R18Mode, req)
	if sealed {
		p.replyText(b, msg, isGroup, pickRand(r18SealedMessages))
		return
	}

	tags := req.tags
	if len(tags) == 0 && len(p.cfg.DefaultTags) > 0 {
		tags = append([]string{}, p.cfg.DefaultTags...)
	}

	found, tried, err := p.searchSetu(ctx, setuFilter{tags: tags, r18: r18}, req.count)
	if err != nil {
		p.Logger.Warn("涩图搜索失败", "error", err, "tags", tags, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pickRand(errorMessages))
		return
	}
	if len(found) == 0 {
		want := strings.Join(tags, " ")
		if want == "" {
			want = "随机"
		}
		p.replyText(b, msg, isGroup, fmt.Sprintf(pickRand(emptyMessages), tried, want))
		return
	}
	p.Logger.Info("涩图开车", "user", msg.Sender.UserId, "is_group", isGroup, "tags", tags, "count", len(found), "tried", tried)
	p.sendSetu(ctx, b, msg, isGroup, nick, found)
}

// takeQuota 消费一次请求额度，返回 (放行与否, 受限种类, 冷却剩余, 每日剩余)。
func (p *SetuPlugin) takeQuota(uid message.QID) (bool, string, time.Duration, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	bk, ok := p.users[uid.String()]
	if !ok {
		bk = &userBucket{}
		p.users[uid.String()] = bk
	}
	if bk.day != today {
		bk.day = today
		bk.count = 0
	}
	if p.cfg.DailyLimit > 0 && bk.count >= p.cfg.DailyLimit {
		return false, "daily", 0, 0
	}
	if p.cfg.CooldownSec > 0 && !bk.last.IsZero() {
		if d := time.Since(bk.last); d < time.Duration(p.cfg.CooldownSec)*time.Second {
			left := 0
			if p.cfg.DailyLimit > 0 {
				left = p.cfg.DailyLimit - bk.count
			}
			return false, "cooldown", time.Duration(p.cfg.CooldownSec)*time.Second - d, left
		}
	}
	bk.last = time.Now()
	bk.count++
	left := 0
	if p.cfg.DailyLimit > 0 {
		left = p.cfg.DailyLimit - bk.count
	}
	return true, "", 0, left
}

// quotaView 查看剩余额度（不消费），供 status 用。
func (p *SetuPlugin) quotaView(uid message.QID) (cd time.Duration, dailyLeft int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dailyLeft = p.cfg.DailyLimit
	if p.cfg.DailyLimit > 0 {
		used := 0
		if bk, ok := p.users[uid.String()]; ok && bk.day == time.Now().Format("2006-01-02") {
			used = bk.count
			if p.cfg.CooldownSec > 0 && !bk.last.IsZero() {
				if d := time.Since(bk.last); d < time.Duration(p.cfg.CooldownSec)*time.Second {
					cd = time.Duration(p.cfg.CooldownSec)*time.Second - d
				}
			}
		}
		dailyLeft = p.cfg.DailyLimit - used
	}
	return cd, dailyLeft
}

// statusText /setu status 的回复。
func (p *SetuPlugin) statusText(msg message.Message, isGroup bool, bypass bool) string {
	var b strings.Builder
	b.WriteString("📊 涩图状态\n")
	allowed := bypass
	reason := "管理员旁路"
	if !bypass {
		if isGroup {
			allowed = matchAllowlist(p.cfg.AllowGroups, msg.GroupId)
			reason = "本群在放行名单"
			if !allowed {
				reason = "本群不在放行名单"
			}
		} else {
			allowed = matchAllowlist(p.cfg.AllowFriends, msg.Sender.UserId)
			reason = "你在放行名单"
			if !allowed {
				reason = "你不在放行名单"
			}
		}
	}
	if allowed {
		fmt.Fprintf(&b, "放行：✅（%s）\n", reason)
	} else {
		fmt.Fprintf(&b, "放行：❌（%s）\n", reason)
	}
	modeName := map[string]string{"0": "仅全年龄", "1": "仅 R18", "2": "混合"}[p.cfg.R18Mode]
	if modeName == "" {
		modeName = p.cfg.R18Mode
	}
	fmt.Fprintf(&b, "R18 模式：%s\n", modeName)
	if n := p.index.size(); n > 0 {
		_, updated := p.index.snapshot()
		fmt.Fprintf(&b, "索引：%d 条（%s更新）\n", n, humanAgo(updated))
	} else {
		b.WriteString("索引：加载中（稍后再试，或让管理员 /setu refresh）\n")
	}
	cd, left := p.quotaView(msg.Sender.UserId)
	if bypass {
		b.WriteString("额度：管理员旁路，无限制\n")
	} else {
		if p.cfg.DailyLimit > 0 {
			fmt.Fprintf(&b, "今日剩余：%d/%d\n", left, p.cfg.DailyLimit)
		} else {
			b.WriteString("今日剩余：不限量\n")
		}
		if cd > 0 {
			fmt.Fprintf(&b, "冷却剩余：%ds\n", int(cd.Seconds())+1)
		}
	}
	b.WriteString("用法：@我 /setu [数量] [tag...]，/setu help 看玩法")
	return b.String()
}

// humanAgo “x小时前更新”这种相对时间。
func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "未知时间"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return t.Format("01-02 15:04") + " "
	}
}

// replyText 回复文本（群聊带 @）。
func (p *SetuPlugin) replyText(b bot.Bot, msg message.Message, isGroup bool, text string) {
	if isGroup {
		c := msgchain.Builder().Group().Mention(msg.Sender.UserId).Text("\n" + text).Build()
		if _, ok := b.SendGroupMsg(msg.GroupId, c); !ok {
			p.Logger.Warn("群聊回复发送失败", "group", msg.GroupId)
		}
		return
	}
	c := msgchain.Builder().Friend().Text(text).Build()
	if _, ok := b.SendFriendMsg(msg.Sender.UserId, c); !ok {
		p.Logger.Warn("私聊回复发送失败", "user", msg.Sender.UserId)
	}
}

// sendSetu 发送涩图：先把原图下载到本地再转 base64 发送（不再直发 URL，
// 避免 NapCat 侧拉图床 403/超时导致无提示失败），逐张发送、逐张报错。
func (p *SetuPlugin) sendSetu(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, nick string, metas []*PixivMeta) {
	if len(metas) == 0 {
		return
	}
	results := p.downloadSetuImages(ctx, metas)
	var okList, dlFailed []setuDownload
	for _, r := range results {
		if r.err != nil || r.b64 == "" {
			if r.err == nil {
				r.err = fmt.Errorf("图片数据为空")
			}
			dlFailed = append(dlFailed, r)
			continue
		}
		okList = append(okList, r)
	}
	// 全军覆没：必须明确告诉用户为什么，并给作品页链接兜底。
	if len(okList) == 0 {
		p.Logger.Warn("涩图全部下载失败", "count", len(metas), "user", msg.Sender.UserId, "reason", shortDownloadErr(dlFailed[0].err))
		p.replyText(b, msg, isGroup, allDownloadFailText(dlFailed))
		return
	}
	if len(dlFailed) > 0 {
		p.Logger.Warn("涩图部分下载失败", "ok", len(okList), "failed", len(dlFailed), "user", msg.Sender.UserId, "reason", shortDownloadErr(dlFailed[0].err))
	}
	footer := pickRand(footers)
	total := len(okList)
	var sendFailed []setuDownload
	for i, r := range okList {
		first, last := i == 0, i == total-1
		text := captionLine(i+1, total, r.meta)
		if first {
			text = summonLine(nick, total) + "\n" + text
		}
		if last {
			if footer != "" {
				text += "\n" + footer
			}
			if len(dlFailed) > 0 {
				text += "\n" + downloadFailSummary(dlFailed)
			}
		}
		var sent bool
		if isGroup {
			c := msgchain.Builder().Group()
			if first {
				c.Mention(msg.Sender.UserId).Text("\n" + text)
			} else {
				c.Text(text)
			}
			c.ImageBase64(r.b64)
			_, sent = b.SendGroupMsg(msg.GroupId, c.Build())
		} else {
			c := msgchain.Builder().Friend()
			c.Text(text)
			c.ImageBase64(r.b64)
			_, sent = b.SendFriendMsg(msg.Sender.UserId, c.Build())
		}
		if !sent {
			p.Logger.Warn("涩图单张发送失败", "pid", r.meta.Pid, "size", r.size, "user", msg.Sender.UserId, "is_group", isGroup)
			sendFailed = append(sendFailed, r)
		}
	}
	// 图已下载但发不出去：同样要明确提示 + 给作品页链接，而不是只记日志。
	if len(sendFailed) > 0 {
		p.Logger.Warn("涩图部分发送失败", "sent", total-len(sendFailed), "failed", len(sendFailed), "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, sendFailSummary(sendFailed))
		return
	}
	p.Logger.Info("涩图开车完成", "user", msg.Sender.UserId, "is_group", isGroup, "sent", total, "dl_failed", len(dlFailed))
}

// 确保 regexp 被使用（Start 里做正则预检）。
var _ = regexp.Compile
