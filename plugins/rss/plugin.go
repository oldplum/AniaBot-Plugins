// Package rss RSS 订阅推送插件。
//
// 用户把 RSS/Atom 源订阅到某个群聊或私聊，插件后台定时轮询，
// 发现新条目后推送标题、摘要与链接到对应会话。支持 RSS 2.0 与
// Atom 两种格式；订阅数据保存在持久化存储中，重启不丢。
//
// 命令（群聊需 @ 机器人，私聊无需）：/rss add|list|del|now。
package rss

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/spf13/viper"
)

const (
	seenCap       = 200  // 每个订阅保留的最近条目 ID 数量（环形，超出淘汰最旧的）
	maxFetchItems = 50   // 单次拉取最多解析的条目数
	maxTextRunes  = 2000 // 单条推送消息的最大字符数，防止超出平台消息长度限制
)

// rssConfig 插件配置（面板表单自动生成，Start 前由框架填充）。
type rssConfig struct {
	Enable          bool `cfg:"plugin.rss.enable" label:"启用订阅推送" group:"RSS订阅" default:"true" help:"关闭后不响应订阅命令且停止后台轮询"`
	PollMinutes     int  `cfg:"plugin.rss.poll_minutes" label:"轮询间隔(分钟)" group:"RSS订阅" default:"10" help:"检查订阅源更新的间隔，最小 5 分钟"`
	MaxFeeds        int  `cfg:"plugin.rss.max_feeds" label:"单会话订阅上限" group:"RSS订阅" default:"10" help:"每个群聊/私聊最多可添加的订阅数量"`
	MaxItemsPerPush int  `cfg:"plugin.rss.max_items_per_push" label:"单次推送条数上限" group:"RSS订阅" default:"5" help:"一次检查中每个源最多推送的条目数，超出的以省略提示代替"`
	SummaryLen      int  `cfg:"plugin.rss.summary_length" label:"摘要长度" group:"RSS订阅" default:"100" help:"推送时附带的正文摘要最大字符数，0 表示不显示摘要"`
	MaxFails        int  `cfg:"plugin.rss.max_fails" label:"失效自动移除阈值" group:"RSS订阅" default:"20" help:"订阅源连续拉取失败达到该次数后自动移除并在会话中通知"`
}

// feedSub 一条订阅的持久化数据。
type feedSub struct {
	URL         string    `json:"url"`         // 订阅源地址
	Title       string    `json:"title"`       // 源标题（首次成功拉取时填充）
	Seen        []string  `json:"seen"`        // 最近已推送条目的 ID（新 ID 头插，容量 seenCap）
	Initialized bool      `json:"initialized"` // 首次拉取是否已建立基线（基线期的旧条目不推送）
	FailCount   int       `json:"fail_count"`  // 连续拉取失败次数
	AddedAt     time.Time `json:"added_at"`    // 添加时间（列表排序用）
	AddedBy     string    `json:"added_by"`    // 添加者 QID
}

// feedItem 一次拉取解析出的条目。
type feedItem struct {
	ID      string // 条目唯一标识：guid / link / 标题哈希
	Title   string
	Link    string
	Summary string
}

// RSSPlugin 插件定义：嵌入 plugin.Meta 获得默认实现。
type RSSPlugin struct {
	plugin.Meta
	cfg    rssConfig
	store  storage.PersistentStorage // Clone("rss") 后的订阅数据命名空间
	mu     sync.Mutex                // 保护订阅条目的读改写（命令处理与后台轮询并发）
	pollMu sync.Mutex                // 防止多轮轮询并发执行导致重复推送
	cancel context.CancelFunc        // 停止后台轮询 goroutine
}

// NewPlugin 构造函数（plugin.json 的 entry.constructor 指向这里）。
func NewPlugin() *RSSPlugin {
	p := &RSSPlugin{}
	p.Name = "RSS订阅"
	p.HelpWords = "@我 /rss add <地址> 订阅 RSS/Atom 源、/rss list 查看、/rss del <序号> 删除、/rss now 立即检查更新"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 声明配置结构体（面板表单 + 默认值 + Start 前自动填充）。
func (p *RSSPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化：开订阅数据命名空间并打印配置摘要。
func (p *RSSPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("rss")
	}
	p.Logger.Info("RSS 订阅插件初始化",
		"enable", p.cfg.Enable,
		"poll_minutes", p.cfg.PollMinutes,
		"max_feeds", p.cfg.MaxFeeds,
		"max_items_per_push", p.cfg.MaxItemsPerPush,
		"summary_length", p.cfg.SummaryLen,
	)
	return nil
}

// Awake 启动后台轮询 goroutine（需要 bot 发送能力，故放在 Awake）。
func (p *RSSPlugin) Awake(ctx context.Context, b bot.Bot) error {
	if !p.cfg.Enable {
		p.Logger.Info("RSS 订阅推送已在配置中禁用")
		return nil
	}
	if p.PersistentStorage == nil {
		p.Logger.Warn("持久化存储不可用，RSS 订阅功能停用")
		return nil
	}
	p.store = p.PersistentStorage.Clone("rss")

	// 参照 eew：后台 goroutine 用独立可取消 context，避免依赖框架 ctx 生命周期
	pollCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	interval := p.pollInterval()
	go p.pollLoop(pollCtx, b, interval)

	p.Logger.Info("RSS 订阅推送已启动", "interval", interval.String())
	return nil
}

// OnGroupMsg 群聊消息事件：/rss 子命令分发。
func (p *RSSPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	switch normalizeCmd(cmd.Name) {
	case "rss", "订阅", "subscribe":
	default:
		return true, nil
	}
	p.dispatch(ctx, b, "g:"+msg.GroupId.String(), cmd.Args, msg)
	return false, nil
}

// OnFriendMsg 私聊消息事件：私聊无需 @。
func (p *RSSPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	switch normalizeCmd(cmd.Name) {
	case "rss", "订阅", "subscribe":
	default:
		return true, nil
	}
	p.dispatch(ctx, b, "u:"+msg.Sender.UserId.String(), cmd.Args, msg)
	return false, nil
}

// ---------- 命令分发 ----------

// dispatch 解析 /rss 子命令并执行。
func (p *RSSPlugin) dispatch(ctx context.Context, b bot.Bot, chat string, args []string, msg message.Message) {
	if p.store == nil {
		p.reply(b, chat, "订阅存储当前不可用，请联系管理员检查存储配置")
		return
	}
	sub := ""
	if len(args) > 0 {
		sub = normalizeCmd(args[0])
	}
	rest := []string{}
	if len(args) > 1 {
		rest = args[1:]
	}

	switch sub {
	case "":
		p.reply(b, chat, rssHelp)
	case "add", "添加", "订阅源":
		p.cmdAdd(ctx, b, chat, strings.Join(rest, " "), msg.Sender.UserId)
	case "list", "列表", "查看":
		p.cmdList(ctx, b, chat)
	case "del", "remove", "rm", "删除", "退订":
		p.cmdDel(ctx, b, chat, strings.Join(rest, " "))
	case "now", "check", "检查", "更新":
		go func() {
			// 与后台轮询互斥：正在轮询时跳过本次手动检查，避免重复推送
			if !p.pollMu.TryLock() {
				p.Logger.Info("手动检查跳过：后台轮询进行中", "chat", chat)
				return
			}
			defer p.pollMu.Unlock()
			p.pollChats(context.Background(), b, []string{chat})
		}()
		p.reply(b, chat, "正在检查订阅更新，稍等片刻…")
	default:
		p.reply(b, chat, "未知子命令，"+rssHelp)
	}
}

const rssHelp = "RSS 订阅用法：\n" +
	"/rss add <地址> — 订阅一个 RSS/Atom 源\n" +
	"/rss list — 查看本会话的订阅列表\n" +
	"/rss del <序号> — 退订指定订阅\n" +
	"/rss now — 立即检查一次更新"

// cmdAdd 添加订阅：立即拉取一次建立基线，之后只推送新条目。
func (p *RSSPlugin) cmdAdd(ctx context.Context, b bot.Bot, chat, rawURL string, user message.QID) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.reply(b, chat, "地址格式不对，请提供 http(s):// 开头的 RSS/Atom 订阅地址")
		return
	}

	p.mu.Lock()
	keys, kerr := p.store.Keys(ctx, "f:"+chat+":")
	if kerr != nil {
		p.mu.Unlock()
		p.Logger.Error("读取订阅列表失败", "chat", chat, "error", kerr)
		p.reply(b, chat, "读取订阅列表失败，请稍后再试")
		return
	}
	if p.cfg.MaxFeeds > 0 && len(keys) >= p.cfg.MaxFeeds {
		p.mu.Unlock()
		p.reply(b, chat, fmt.Sprintf("订阅数已达上限（%d 个），请先退订一些再添加", p.cfg.MaxFeeds))
		return
	}
	key := subKey(chat, rawURL)
	for _, k := range keys {
		if k == key {
			p.mu.Unlock()
			p.reply(b, chat, "这个地址已经订阅过啦")
			return
		}
	}
	p.mu.Unlock()

	// 首次拉取：拿到标题并把现有条目设为基线（避免把历史文章全部推出来）
	sub := &feedSub{URL: rawURL, AddedAt: time.Now(), AddedBy: user.String()}
	items, title, ferr := p.fetchFeed(rawURL)
	if ferr == nil {
		sub.Title = title
		sub.Seen = itemIDs(items)
		sub.Initialized = true
	} else {
		p.Logger.Warn("订阅源首次拉取失败，稍后由轮询重试", "url", rawURL, "error", ferr)
	}

	p.mu.Lock()
	if !p.store.Set(ctx, key, sub) {
		p.mu.Unlock()
		p.reply(b, chat, "保存订阅失败，请稍后再试")
		return
	}
	p.mu.Unlock()

	if ferr != nil {
		p.reply(b, chat, fmt.Sprintf("已添加订阅 %s\n⚠️ 首次拉取失败，将自动重试，成功后开始推送", rawURL))
		return
	}
	p.reply(b, chat, fmt.Sprintf("✅ 已订阅「%s」，当前共 %d 条历史记录，之后的新文章会自动推送到这里", title, len(items)))
}

// cmdList 列出本会话的订阅。
func (p *RSSPlugin) cmdList(ctx context.Context, b bot.Bot, chat string) {
	subs := p.listChatSubs(ctx, chat)
	if len(subs) == 0 {
		p.reply(b, chat, "还没有订阅，用 /rss add <地址> 添加一个吧")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 本会话共 %d 个订阅：\n", len(subs)))
	for i, s := range subs {
		title := s.Title
		if title == "" {
			title = s.URL
		}
		state := ""
		if s.FailCount > 0 {
			state = fmt.Sprintf("（连续失败 %d 次）", s.FailCount)
		}
		fmt.Fprintf(&sb, "%d. %s%s\n", i+1, title, state)
	}
	p.reply(b, chat, sb.String())
}

// cmdDel 按列表序号退订。
func (p *RSSPlugin) cmdDel(ctx context.Context, b bot.Bot, chat, arg string) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n < 1 {
		p.reply(b, chat, "用法：/rss del <序号>，序号可用 /rss list 查看")
		return
	}
	subs := p.listChatSubs(ctx, chat)
	if n > len(subs) {
		p.reply(b, chat, "序号超出范围，用 /rss list 查看列表")
		return
	}
	target := subs[n-1]
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.store.Del(ctx, subKey(chat, target.URL)) {
		p.reply(b, chat, "删除失败，请稍后再试")
		return
	}
	title := target.Title
	if title == "" {
		title = target.URL
	}
	p.reply(b, chat, "已退订「"+title+"」")
}

// listChatSubs 读取某会话的全部订阅并按添加时间排序。
func (p *RSSPlugin) listChatSubs(ctx context.Context, chat string) []feedSub {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys, err := p.store.Keys(ctx, "f:"+chat+":")
	if err != nil {
		p.Logger.Error("读取订阅列表失败", "chat", chat, "error", err)
		return nil
	}
	subs := make([]feedSub, 0, len(keys))
	for _, k := range keys {
		var s feedSub
		if p.store.Get(ctx, k, &s) {
			subs = append(subs, s)
		}
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].AddedAt.Before(subs[j].AddedAt) })
	return subs
}

// ---------- 后台轮询 ----------

// pollLoop 后台轮询主循环：启动 30 秒后先检查一轮，之后按固定间隔轮询。
func (p *RSSPlugin) pollLoop(ctx context.Context, b bot.Bot, interval time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	p.pollAll(b)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAll(b)
		}
	}
}

// pollAll 轮询全部会话的订阅。
func (p *RSSPlugin) pollAll(b bot.Bot) {
	// 轮询本体不可重入：后台轮询与手动检查共用该锁
	p.pollMu.Lock()
	defer p.pollMu.Unlock()

	p.mu.Lock()
	keys, err := p.store.Keys(context.Background(), "f:")
	p.mu.Unlock()
	if err != nil {
		p.Logger.Error("轮询订阅失败：读取键列表出错", "error", err)
		return
	}
	p.pollKeys(context.Background(), b, keys)
}

// pollChats 只轮询指定会话（/rss now 手动检查用，调用方已持有 pollMu）。
func (p *RSSPlugin) pollChats(ctx context.Context, b bot.Bot, chats []string) {
	var keys []string
	for _, chat := range chats {
		p.mu.Lock()
		ks, err := p.store.Keys(ctx, "f:"+chat+":")
		p.mu.Unlock()
		if err != nil {
			p.Logger.Error("手动检查订阅失败", "chat", chat, "error", err)
			continue
		}
		keys = append(keys, ks...)
	}
	p.pollKeys(ctx, b, keys)
}

// pollKeys 逐个拉取并推送新条目。网络请求不持锁，只在读写订阅数据时加锁。
func (p *RSSPlugin) pollKeys(ctx context.Context, b bot.Bot, keys []string) {
	for _, key := range keys {
		select {
		case <-ctx.Done():
			return
		default:
		}

		p.mu.Lock()
		var sub feedSub
		if !p.store.Get(ctx, key, &sub) {
			p.mu.Unlock()
			continue
		}
		p.mu.Unlock()

		items, title, err := p.fetchFeed(sub.URL)
		if err != nil {
			p.mu.Lock()
			sub.FailCount++
			reached := p.cfg.MaxFails > 0 && sub.FailCount >= p.cfg.MaxFails
			if reached {
				p.store.Del(ctx, key)
			} else {
				p.store.Set(ctx, key, &sub)
			}
			p.mu.Unlock()
			p.Logger.Warn("订阅源拉取失败", "url", sub.URL, "fail_count", sub.FailCount, "error", err)
			if reached {
				t := sub.Title
				if t == "" {
					t = sub.URL
				}
				p.reply(b, keyChat(key), fmt.Sprintf("订阅「%s」连续 %d 次拉取失败，已自动移除", t, sub.FailCount))
			}
			continue
		}

		p.mu.Lock()
		sub.FailCount = 0
		if sub.Title == "" && title != "" {
			sub.Title = title
		}
		ids := itemIDs(items)
		if !sub.Initialized {
			// 首次成功拉取：只建立基线不推送
			sub.Seen = ids
			sub.Initialized = true
			p.store.Set(ctx, key, &sub)
			p.mu.Unlock()
			continue
		}
		fresh := unseenItems(sub.Seen, items)
		sub.Seen = prependSeen(sub.Seen, ids)
		saved := p.store.Set(ctx, key, &sub)
		p.mu.Unlock()

		if !saved || len(fresh) == 0 {
			continue
		}
		p.pushItems(b, keyChat(key), sub.Title, fresh)
	}
}

// pushItems 把新条目推送到会话。
func (p *RSSPlugin) pushItems(b bot.Bot, chat string, feedTitle string, items []feedItem) {
	limit := p.cfg.MaxItemsPerPush
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}

	name := feedTitle
	if name == "" {
		name = "订阅源"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📰 「%s」更新 %d 条\n", name, len(items))
	for i, it := range items[:limit] {
		fmt.Fprintf(&sb, "\n%d. %s\n", i+1, it.Title)
		if p.cfg.SummaryLen > 0 && it.Summary != "" {
			sb.WriteString(truncateRunes(it.Summary, p.cfg.SummaryLen) + "\n")
		}
		if it.Link != "" {
			sb.WriteString(it.Link + "\n")
		}
		if sb.Len() > maxTextRunes {
			break
		}
	}
	if limit < len(items) {
		fmt.Fprintf(&sb, "\n…还有 %d 条未展示，可发送 /rss now 查看", len(items)-limit)
	}
	p.reply(b, chat, sb.String())
}

// ---------- 小工具 ----------

// reply 向会话发送一条纯文本消息。
func (p *RSSPlugin) reply(b bot.Bot, chat, text string) {
	if strings.HasPrefix(chat, "g:") {
		c := msgchain.Builder().Group()
		c.Text(text)
		if _, ok := b.SendGroupMsg(message.FromString(chat[2:]), c.Build()); !ok {
			p.Logger.Warn("订阅消息发送失败", "chat", chat)
		}
		return
	}
	c := msgchain.Builder().Friend()
	c.Text(text)
	if _, ok := b.SendFriendMsg(message.FromString(chat[2:]), c.Build()); !ok {
		p.Logger.Warn("订阅消息发送失败", "chat", chat)
	}
}

// subKey 订阅的存储键：会话 + 地址哈希。
func subKey(chat, feedURL string) string {
	sum := md5.Sum([]byte(feedURL))
	return "f:" + chat + ":" + hex.EncodeToString(sum[:])[:12]
}

// keyChat 从存储键还原会话标识（f:<chat>:<hash>）。
func keyChat(key string) string {
	rest := strings.TrimPrefix(key, "f:")
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// normalizeCmd 归一化命令名（小写、去空白）。
func normalizeCmd(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// pollInterval 轮询间隔，钳制在 5 分钟～24 小时。
func (p *RSSPlugin) pollInterval() time.Duration {
	m := p.cfg.PollMinutes
	if m < 5 {
		m = 5
	}
	if m > 1440 {
		m = 1440
	}
	return time.Duration(m) * time.Minute
}

// itemIDs 提取条目 ID 列表（上限 maxFetchItems）。
func itemIDs(items []feedItem) []string {
	ids := make([]string, 0, len(items))
	for i, it := range items {
		if i >= maxFetchItems {
			break
		}
		if it.ID != "" {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// unseenItems 返回 items 中 ID 不在 seen 集合里的条目（保持源内顺序）。
func unseenItems(seen []string, items []feedItem) []feedItem {
	set := make(map[string]struct{}, len(seen))
	for _, id := range seen {
		set[id] = struct{}{}
	}
	var fresh []feedItem
	for i, it := range items {
		if i >= maxFetchItems {
			break
		}
		if it.ID == "" {
			continue
		}
		if _, ok := set[it.ID]; !ok {
			fresh = append(fresh, it)
		}
	}
	return fresh
}

// prependSeen 把最新 ID 头插进已见列表并裁剪到容量上限。
func prependSeen(seen []string, ids []string) []string {
	out := make([]string, 0, len(seen)+len(ids))
	for i := len(ids) - 1; i >= 0; i-- { // 反转头插，保证 ids 中最新（最前）的排在 seen 最前
		out = append(out, ids[i])
	}
	out = append(out, seen...)
	if len(out) > seenCap {
		out = out[:seenCap]
	}
	return out
}

// truncateRunes 按字符数截断，超长补省略号。
func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
