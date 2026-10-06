package pixiv

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

// PixivPlugin Pixiv 全功能插件：配置 refresh_token 登录后支持搜索、排行榜、
// 推荐、作品/画师查询与相关作品，内置分级(R18)过滤、个人限流与群放行名单。
type PixivPlugin struct {
	plugin.Meta
	cfg     pixivConfig
	session pixivSession
	http    *resty.Client // Start 里按代理配置构建，API 与图片下载共用

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
func NewPlugin() *PixivPlugin {
	p := &PixivPlugin{users: make(map[string]*userBucket)}
	p.Name = "Pixiv"
	p.HelpWords = "群里@我 /pixiv 搜索 关键词 搜插画，/pixiv 排行 看榜单，/pixiv help 看全部玩法"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.4"
	p.Order = plugin.LevelNormal
	return p
}

// OnUnload 卸载钩子：被插件市场卸载时清空登录会话与用户频率缓存；
// Bot 退出/重启（UnloadShutdown）无需处理——这些状态本就在内存、
// 随进程结束释放，重启后按 refresh_token 重新登录。
func (p *PixivPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	if reason != plugin.UnloadUninstall {
		return nil
	}
	p.session.mu.Lock()
	p.session.accessToken = ""
	p.session.expiresAt = time.Time{}
	p.session.userID = ""
	p.session.userName = ""
	p.session.mu.Unlock()
	p.mu.Lock()
	p.users = make(map[string]*userBucket)
	p.mu.Unlock()
	p.Logger.Info("已清理登录会话与频率缓存（卸载）")
	return nil
}

// Start 初始化：参数兜底、正则/代理预检、构建 HTTP 客户端并异步预热登录。
func (p *PixivPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.users == nil {
		p.users = make(map[string]*userBucket)
	}
	if p.cfg.ListSize < 1 {
		p.cfg.ListSize = 1
	}
	if p.cfg.ListSize > 10 {
		p.cfg.ListSize = 10
	}
	if p.cfg.PreviewCount < 0 {
		p.cfg.PreviewCount = 0
	}
	if p.cfg.PreviewCount > 3 {
		p.cfg.PreviewCount = 3
	}
	if p.cfg.CooldownSec < 0 {
		p.cfg.CooldownSec = 0
	}
	if p.cfg.DailyLimit < 0 {
		p.cfg.DailyLimit = 0
	}
	if p.cfg.ContentType != "safe" && p.cfg.ContentType != "mixed" && p.cfg.ContentType != "r18" {
		p.cfg.ContentType = "safe"
	}
	for _, bad := range invalidPatterns(append(append([]string{}, p.cfg.AllowGroups...), p.cfg.AllowFriends...)) {
		p.Logger.Warn("放行名单正则无效，启动后该条不会命中任何会话", "pattern", bad)
	}
	if bad := strings.TrimSpace(p.cfg.Proxy); bad != "" {
		if err := validateProxy(bad); err != nil {
			p.Logger.Warn("代理配置无效，将忽略代理直连", "proxy", bad, "error", err)
			p.cfg.Proxy = ""
		}
	}
	p.http = newPixivHTTPClient(p.RestyClient, p.cfg.Proxy)

	if strings.TrimSpace(p.cfg.RefreshToken) == "" {
		p.Logger.Info("未配置 Pixiv refresh_token，登录后功能不可用（面板→配置管理→Pixiv）")
	} else {
		// 异步预热登录，不阻塞启动；失败不致命，首次使用时会再试。
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if _, err := p.ensureToken(c); err != nil {
				p.Logger.Warn("Pixiv 登录失败，请检查 refresh_token 与代理配置", "error", err)
				return
			}
			p.Logger.Info("Pixiv 登录成功", "user", p.session.userName)
		}()
	}
	p.Logger.Info("Pixiv 插件已初始化",
		"content_type", p.cfg.ContentType,
		"list_size", p.cfg.ListSize,
		"preview_count", p.cfg.PreviewCount,
		"proxy_set", strings.TrimSpace(p.cfg.Proxy) != "",
	)
	return nil
}

// Awake 启动完成事件：如实汇报登录配置状态。
func (p *PixivPlugin) Awake(ctx context.Context, b bot.Bot) error {
	if !p.cfg.Enable {
		p.Logger.Info("Pixiv 插件已在配置中禁用")
		return nil
	}
	p.Logger.Info("Pixiv 插件已就绪",
		"login_configured", strings.TrimSpace(p.cfg.RefreshToken) != "",
		"proxy_set", strings.TrimSpace(p.cfg.Proxy) != "")
	return nil
}

// isPixivCmd 是否 Pixiv 命令（含中文别名）。
func isPixivCmd(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "pixiv", "p站":
		return true
	default:
		return false
	}
}

// OnGroupMsg 群聊消息事件：必须 @机器人。
func (p *PixivPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if !cmd.Mention || !isPixivCmd(cmd.Name) {
		return true, nil
	}
	p.handlePixiv(ctx, b, cmd.Args, msg, true)
	return false, nil
}

// OnFriendMsg 私聊消息事件：无需 @。
func (p *PixivPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if !isPixivCmd(cmd.Name) {
		return true, nil
	}
	p.handlePixiv(ctx, b, cmd.Args, msg, false)
	return false, nil
}

func (p *PixivPlugin) isAdmin(msg message.Message) bool {
	return p.SystemConfig.AdminId != "" && msg.Sender.UserId == p.SystemConfig.AdminId
}

// handlePixiv /pixiv 主流程：子命令分发，真实功能统一走 登录校验→放行→限流。
func (p *PixivPlugin) handlePixiv(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	sub := ""
	rest := args
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
		rest = args[1:]
	}
	switch sub {
	case "", "help", "h", "帮助", "?", "？":
		p.replyText(b, msg, isGroup, pixivHelpText)
		return
	case "status", "st", "状态":
		p.replyText(b, msg, isGroup, p.statusText(ctx, msg, isGroup))
		return
	}

	// 以下是真实功能：先登录校验，再放行与限流。
	if strings.TrimSpace(p.cfg.RefreshToken) == "" {
		p.replyText(b, msg, isGroup, pixivNotLoginText)
		return
	}
	admin := p.isAdmin(msg)
	bypass := admin && p.cfg.AdminBypass
	if !bypass {
		allowed := false
		if isGroup {
			allowed = matchAllowlist(p.cfg.AllowGroups, msg.GroupId)
		} else {
			allowed = matchAllowlist(p.cfg.AllowFriends, msg.Sender.UserId)
		}
		if !allowed {
			p.Logger.Info("非放行会话请求 Pixiv 功能", "is_group", isGroup, "group", msg.GroupId, "user", msg.Sender.UserId)
			if p.cfg.SilentDeny {
				return
			}
			p.replyText(b, msg, isGroup, pixivDenyText)
			return
		}
	}
	if !bypass {
		if ok, kind, remain, _ := p.takeQuota(msg.Sender.UserId); !ok {
			if kind == "cooldown" {
				p.replyText(b, msg, isGroup, fmt.Sprintf("冲太快啦，休息 %d 秒再来~", int(remain.Seconds())+1))
			} else {
				p.replyText(b, msg, isGroup, fmt.Sprintf("今天的 %d 次额度用完啦，明天再来吧（管理员可旁路）", p.cfg.DailyLimit))
			}
			return
		}
	}

	switch sub {
	case "search", "s", "搜索", "搜":
		p.handleSearch(ctx, b, rest, msg, isGroup)
	case "illust", "图", "作品", "查看":
		p.handleIllust(ctx, b, rest, msg, isGroup)
	case "rank", "排行", "排行榜":
		p.handleRank(ctx, b, rest, msg, isGroup)
	case "recommend", "rec", "推荐":
		p.handleRecommend(ctx, b, msg, isGroup)
	case "user", "画师", "作者":
		p.handleUser(ctx, b, rest, msg, isGroup)
	case "related", "相似", "相关":
		p.handleRelated(ctx, b, rest, msg, isGroup)
	default:
		// 未知子命令按搜索关键词处理：/pixiv 少女前线 == /pixiv 搜索 少女前线
		p.handleSearch(ctx, b, args, msg, isGroup)
	}
}

// handleSearch 关键词搜索插画。
func (p *PixivPlugin) handleSearch(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	keyword, page := parseKeywordAndPage(args)
	if keyword == "" {
		p.replyText(b, msg, isGroup, "用法：/pixiv 搜索 <关键词> [页码]，例如 /pixiv 搜索 少女前线 2")
		return
	}
	items, err := p.collectIllusts(ctx, p.cfg.ListSize, (page-1)*30, func(offset int) ([]illust, int, error) {
		return p.apiSearchIllust(ctx, keyword, offset)
	})
	if err != nil {
		p.Logger.Warn("Pixiv 搜索失败", "error", err, "keyword", keyword, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pixivAPIErrText(err))
		return
	}
	if len(items) == 0 {
		p.replyText(b, msg, isGroup, fmt.Sprintf("「%s」在当前分级（%s）下没搜到结果，换个关键词或页码试试", keyword, contentTypeName(p.cfg.ContentType)))
		return
	}
	p.Logger.Info("Pixiv 搜索完成", "keyword", keyword, "page", page, "items", len(items), "user", msg.Sender.UserId)
	p.sendListResult(ctx, b, msg, isGroup,
		fmt.Sprintf("🔍 搜索「%s」（第 %d 页）", truncate(keyword, 20), page), items,
		"💡 /pixiv 图 <ID> 查看大图；/pixiv 搜索 <关键词> <页码> 翻页")
}

// handleIllust 查看作品详情并发图，多图作品可指定页码。
func (p *PixivPlugin) handleIllust(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	id, page := parseIDAndPage(args)
	if id <= 0 {
		p.replyText(b, msg, isGroup, "用法：/pixiv 图 <作品ID> [页码]，例如 /pixiv 图 12345 2（ID 见作品页链接 artworks/ 后的数字）")
		return
	}
	it, err := p.apiIllustDetail(ctx, id)
	if err != nil {
		p.Logger.Warn("Pixiv 作品详情获取失败", "error", err, "pid", id, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pixivAPIErrText(err))
		return
	}
	if it.ID == 0 || !it.visible() {
		p.replyText(b, msg, isGroup, fmt.Sprintf("没找到作品 %d：可能已删除、不可见或 ID 有误", id))
		return
	}
	if page < 1 {
		page = 1
	}
	if it.PageCount > 0 && page > it.PageCount {
		p.replyText(b, msg, isGroup, fmt.Sprintf("该作品共 %d 页（1~%d），可以 /pixiv 图 %d <页码> 翻页", it.PageCount, it.PageCount, it.ID))
		return
	}
	res := p.downloadImages(ctx, [][]string{it.pageCandidates(page - 1)})[0]
	text := renderIllustCard(&it, page)
	p.Logger.Info("Pixiv 查看作品", "pid", id, "page", page, "user", msg.Sender.UserId)
	if res.b64 == "" {
		p.Logger.Warn("Pixiv 作品图下载失败", "pid", id, "error", res.err)
		p.replyText(b, msg, isGroup, text+"\n⚠️ 图片下载失败："+shortDownloadErr(res.err)+"（可点上方链接直达作品页）")
		return
	}
	p.sendImageMessage(b, msg, isGroup, text, res.b64)
}

// handleRank 插画排行榜。
func (p *PixivPlugin) handleRank(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	mode, wantR18 := parseRankArgs(args)
	apiMode, title, denyMsg := resolveRankMode(p.cfg.ContentType, mode, wantR18)
	if denyMsg != "" {
		p.replyText(b, msg, isGroup, denyMsg)
		return
	}
	items, err := p.collectIllusts(ctx, p.cfg.ListSize, 0, func(offset int) ([]illust, int, error) {
		return p.apiRankingIllust(ctx, apiMode, offset)
	})
	if err != nil {
		p.Logger.Warn("Pixiv 排行获取失败", "error", err, "mode", apiMode, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pixivAPIErrText(err))
		return
	}
	if len(items) == 0 {
		p.replyText(b, msg, isGroup, "排行榜暂时没拉到数据（当前分级下可能为空），稍后再试")
		return
	}
	p.Logger.Info("Pixiv 排行完成", "mode", apiMode, "items", len(items), "user", msg.Sender.UserId)
	p.sendListResult(ctx, b, msg, isGroup, title, items, "💡 /pixiv 图 <ID> 查看大图")
}

// handleRecommend 为登录账号个性化推荐。
func (p *PixivPlugin) handleRecommend(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool) {
	items, err := p.collectIllusts(ctx, p.cfg.ListSize, 0, func(offset int) ([]illust, int, error) {
		return p.apiRecommendedIllust(ctx, offset)
	})
	if err != nil {
		p.Logger.Warn("Pixiv 推荐获取失败", "error", err, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pixivAPIErrText(err))
		return
	}
	if len(items) == 0 {
		p.replyText(b, msg, isGroup, "推荐暂时拿不到数据，稍后再试")
		return
	}
	p.sendListResult(ctx, b, msg, isGroup, "✨ 为你推荐", items, "💡 /pixiv 图 <ID> 查看大图")
}

// handleUser 画师近期插画作品。
func (p *PixivPlugin) handleUser(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	userID, page := parseIDAndPage(args)
	if userID <= 0 {
		p.replyText(b, msg, isGroup, "用法：/pixiv 画师 <用户ID> [页码]，例如 /pixiv 画师 678（用户 ID 见画师主页链接 users/ 后的数字）")
		return
	}
	title := fmt.Sprintf("🎨 画师近期插画（第 %d 页）", page)
	if page == 1 {
		if d, err := p.apiUserDetail(ctx, strconv.FormatInt(userID, 10)); err == nil {
			if head := renderUserHeader(&d); head != "" {
				title = head + "\n" + title
			}
		} else {
			p.Logger.Warn("画师信息获取失败（继续列作品）", "user_id", userID, "error", err)
		}
	}
	items, err := p.collectIllusts(ctx, p.cfg.ListSize, (page-1)*30, func(offset int) ([]illust, int, error) {
		return p.apiUserIllusts(ctx, strconv.FormatInt(userID, 10), offset)
	})
	if err != nil {
		p.Logger.Warn("Pixiv 画师作品获取失败", "error", err, "user_id", userID, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pixivAPIErrText(err))
		return
	}
	if len(items) == 0 {
		p.replyText(b, msg, isGroup, fmt.Sprintf("画师 %d 没有找到作品：可能不存在、没有插画或当前分级下为空", userID))
		return
	}
	p.Logger.Info("Pixiv 画师作品完成", "user_id", userID, "page", page, "items", len(items), "user", msg.Sender.UserId)
	p.sendListResult(ctx, b, msg, isGroup, title, items,
		"💡 /pixiv 图 <ID> 查看大图；/pixiv 画师 <用户ID> <页码> 翻页")
}

// handleRelated 相似作品。
func (p *PixivPlugin) handleRelated(ctx context.Context, b bot.Bot, args []string, msg message.Message, isGroup bool) {
	id, _ := parseIDAndPage(args)
	if id <= 0 {
		p.replyText(b, msg, isGroup, "用法：/pixiv 相关 <作品ID>，例如 /pixiv 相关 12345")
		return
	}
	items, err := p.collectIllusts(ctx, p.cfg.ListSize, 0, func(offset int) ([]illust, int, error) {
		return p.apiRelatedIllust(ctx, id, offset)
	})
	if err != nil {
		p.Logger.Warn("Pixiv 相关作品获取失败", "error", err, "pid", id, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, pixivAPIErrText(err))
		return
	}
	if len(items) == 0 {
		p.replyText(b, msg, isGroup, fmt.Sprintf("没找到与作品 %d 相关的内容：作品可能已删除，稍后再试", id))
		return
	}
	p.sendListResult(ctx, b, msg, isGroup, fmt.Sprintf("🔗 相关作品（基于 ID:%d）", id), items, "💡 /pixiv 图 <ID> 查看大图")
}

// sendListResult 发送列表结果：文字列表 + 前 N 个作品的预览图。
func (p *PixivPlugin) sendListResult(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, title string, items []illust, hint string) {
	text := renderIllustList(title, items, hint)
	n := p.cfg.PreviewCount
	if n > len(items) {
		n = len(items)
	}
	if n <= 0 {
		p.replyText(b, msg, isGroup, text)
		return
	}
	cands := make([][]string, n)
	for i := 0; i < n; i++ {
		cands[i] = items[i].previewCandidates()
	}
	results := p.downloadImages(ctx, cands)
	if results[0].b64 == "" {
		p.Logger.Warn("Pixiv 列表预览下载失败", "error", shortDownloadErr(results[0].err))
		text += "\n⚠️ 预览图下载失败：" + shortDownloadErr(results[0].err)
	} else {
		p.sendImageMessage(b, msg, isGroup, text, results[0].b64)
	}
	// 其余预览逐张发送（首条已带列表文本，只补图）。
	for i := 1; i < len(results); i++ {
		if results[i].b64 == "" {
			continue
		}
		caption := fmt.Sprintf("预览 %d/%d：%s", i+1, len(results), truncate(items[i].Title, 30))
		p.sendImageMessage(b, msg, isGroup, caption, results[i].b64)
	}
}

// sendImageMessage 发送 文本+图片（群聊首行 @发送者）。
func (p *PixivPlugin) sendImageMessage(b bot.Bot, msg message.Message, isGroup bool, text, b64 string) {
	if isGroup {
		c := msgchain.Builder().Group().Mention(msg.Sender.UserId).Text("\n" + text).ImageBase64(b64).Build()
		if _, ok := b.SendGroupMsg(msg.GroupId, c); !ok {
			p.Logger.Warn("Pixiv 图片消息发送失败（群聊）", "group", msg.GroupId)
		}
		return
	}
	c := msgchain.Builder().Friend().Text(text).ImageBase64(b64).Build()
	if _, ok := b.SendFriendMsg(msg.Sender.UserId, c); !ok {
		p.Logger.Warn("Pixiv 图片消息发送失败（私聊）", "user", msg.Sender.UserId)
	}
}

// replyText 回复文本（群聊带 @）。
func (p *PixivPlugin) replyText(b bot.Bot, msg message.Message, isGroup bool, text string) {
	if isGroup {
		c := msgchain.Builder().Group().Mention(msg.Sender.UserId).Text("\n" + text).Build()
		if _, ok := b.SendGroupMsg(msg.GroupId, c); !ok {
			p.Logger.Warn("Pixiv 群聊回复发送失败", "group", msg.GroupId)
		}
		return
	}
	c := msgchain.Builder().Friend().Text(text).Build()
	if _, ok := b.SendFriendMsg(msg.Sender.UserId, c); !ok {
		p.Logger.Warn("Pixiv 私聊回复发送失败", "user", msg.Sender.UserId)
	}
}

// statusText /pixiv 状态：登录信息 + 分级 + 放行/额度。
func (p *PixivPlugin) statusText(ctx context.Context, msg message.Message, isGroup bool) string {
	var sb strings.Builder
	sb.WriteString("📊 Pixiv 插件状态\n")
	if strings.TrimSpace(p.cfg.RefreshToken) == "" {
		sb.WriteString("登录：❌ 未配置 refresh_token（面板→配置管理→Pixiv）\n")
	} else {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if _, err := p.ensureToken(sctx); err != nil {
			fmt.Fprintf(&sb, "登录：❌ %s\n", truncate(err.Error(), 60))
		} else {
			fmt.Fprintf(&sb, "登录：✅ %s（ID:%s）\n", p.session.userName, p.session.userID)
			fmt.Fprintf(&sb, "Token 剩余：%d 分钟\n", int(time.Until(p.session.expiresAt).Minutes()))
		}
	}
	fmt.Fprintf(&sb, "内容分级：%s\n", contentTypeName(p.cfg.ContentType))
	admin := p.isAdmin(msg)
	bypass := admin && p.cfg.AdminBypass
	allowed, reason := false, ""
	if bypass {
		allowed, reason = true, "管理员旁路"
	} else if isGroup {
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
	if allowed {
		fmt.Fprintf(&sb, "使用权限：✅（%s）\n", reason)
	} else {
		fmt.Fprintf(&sb, "使用权限：❌（%s）\n", reason)
	}
	if !bypass {
		cd, left := p.quotaView(msg.Sender.UserId)
		if p.cfg.DailyLimit > 0 {
			fmt.Fprintf(&sb, "今日剩余：%d/%d\n", left, p.cfg.DailyLimit)
		} else {
			sb.WriteString("今日剩余：不限量\n")
		}
		if cd > 0 {
			fmt.Fprintf(&sb, "冷却剩余：%ds\n", int(cd.Seconds())+1)
		}
	}
	sb.WriteString("用法：@我 /pixiv 搜索 关键词，/pixiv help 看全部")
	return sb.String()
}

// takeQuota 消费一次请求额度，返回 (放行与否, 受限种类, 冷却剩余, 每日剩余)。
func (p *PixivPlugin) takeQuota(uid message.QID) (bool, string, time.Duration, int) {
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
func (p *PixivPlugin) quotaView(uid message.QID) (cd time.Duration, dailyLeft int) {
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

// ---------- 参数解析 ----------

// pageRe 页码/数字参数。
var pageRe = regexp.MustCompile(`^\d{1,3}$`)

// parseKeywordAndPage 解析搜索参数：末尾的独立数字视为页码（1~100），其余是关键词。
func parseKeywordAndPage(args []string) (string, int) {
	page := 1
	toks := append([]string{}, args...)
	if n := len(toks); n > 1 && pageRe.MatchString(strings.TrimSpace(toks[n-1])) {
		if v, err := strconv.Atoi(strings.TrimSpace(toks[n-1])); err == nil && v >= 1 && v <= 100 {
			page = v
			toks = toks[:n-1]
		}
	}
	return strings.TrimSpace(strings.Join(toks, " ")), page
}

// parseIDAndPage 解析 <数字ID> [页码]，不合法返回 (0, 0)。
func parseIDAndPage(args []string) (int64, int) {
	if len(args) == 0 {
		return 0, 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil || id <= 0 {
		return 0, 0
	}
	page := 1
	if len(args) > 1 {
		if v, err := strconv.Atoi(strings.TrimSpace(args[1])); err == nil && v >= 1 && v <= 1000 {
			page = v
		}
	}
	return id, page
}

// parseRankArgs 解析排行参数：日/周/月（默认日榜）+ 可选 r18 意图。
func parseRankArgs(args []string) (mode string, wantR18 bool) {
	mode = "day"
	for _, a := range args {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "日", "day", "daily", "今天", "日榜":
			mode = "day"
		case "周", "week", "weekly", "周榜":
			mode = "week"
		case "月", "month", "monthly", "月榜":
			mode = "month"
		case "r18", "r-18", "🔞":
			wantR18 = true
		}
	}
	return mode, wantR18
}

// resolveRankMode 结合分级配置与用户意图算出实际 API mode。
// safe 下要 R18 直接拒绝；r18 分级下自动升级为 R18 榜；月榜没有 R18 版本。
func resolveRankMode(contentType, mode string, wantR18 bool) (apiMode, title, denyMsg string) {
	if contentType == "safe" && wantR18 {
		return "", "", "当前内容分级是 safe（仅全年龄），R18 榜单不可用；如需开启请联系管理员调整「内容分级」"
	}
	r18 := wantR18 || contentType == "r18"
	switch mode {
	case "week":
		if r18 {
			return "week_r18", "🔞 插画排行 · 周榜 R18", ""
		}
		return "week", "📊 插画排行 · 周榜", ""
	case "month":
		if r18 {
			return "", "", "月榜没有 R18 版本，试试 /pixiv 排行 日 r18 或 周 r18"
		}
		return "month", "📊 插画排行 · 月榜", ""
	default: // day
		if r18 {
			return "day_r18", "🔞 插画排行 · 日榜 R18", ""
		}
		return "day", "📊 插画排行 · 日榜", ""
	}
}
