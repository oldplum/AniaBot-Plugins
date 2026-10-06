// Package music 是插件市场的音乐点歌插件：基于 GD音乐台(music.gdstudio.xyz) 开放 API
// 搜索与点播歌曲，支持多音源、歌词查询，QQ 平台以自定义音乐卡片发送；
// 支持内联按钮的平台（如 Telegram）列表附带序号点播与翻页按钮，点击即用。
package music

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
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
	"github.com/spf13/viper"
)

const (
	// creditLine 出处注明（API 使用要求），附在搜索列表与文本链接末尾。
	creditLine = "via GD音乐台 (music.gdstudio.xyz)"
	// jumpURL 音乐卡片点击跳转页；API 未提供单曲页，跳转 GD音乐台主页。
	jumpURL = "https://music.gdstudio.xyz/"
	// lyricFileMaxChars 歌词超过该字符数时改发 .lrc 文件（QQ 平台）。
	lyricFileMaxChars = 3800
)

// MusicPlugin 音乐点歌插件：搜索 → 序号点播 → 音乐卡片/链接 + 歌词。
// 支持按钮翻页的平台（Telegram 等，断言 bot.Interactive 探测）列表附带
// 翻页按钮，点击就地翻页，无需重复输入指令。
type MusicPlugin struct {
	plugin.Meta
	cfg musicConfig

	client *gdMusicClient

	mu       sync.Mutex
	sessions map[string]*searchSession      // 选歌会话：key = 用户|场景
	byMsg    map[message.QID]*searchSession // 列表消息 ID → 会话（按钮点击路由）
	cooldown map[string]time.Time           // 个人搜索冷却
	inflight map[string]time.Time           // 进行中的点播（防连点/多人同点重复发送）
	limiter  *rateLimiter                   // 全局 API 频率保护（官方 50 次/5 分钟）
}

// searchSession 一次搜索的候选缓存（单页），供序号点歌/查歌词与翻页。
// 按钮翻页时以新对象整体替换（copy-on-write），避免与点播/歌词的并发读取
// 产生数据竞争，调用方不应原地修改字段。
type searchSession struct {
	key       string // 会话键（用户|场景），翻页替换时定位 sessions 表项
	keyword   string
	source    string // 会话创建时的音源，点播/歌词/翻页按它请求，避免改配置后 id 对不上
	page      int    // 当前页码，从 1 起
	tracks    []*track
	listMsg   message.QID // 按钮模式下列表消息 ID（翻页时就地编辑；文本模式为空）
	isGroup   bool
	chat      message.QID // 群聊为群 ID，私聊为发送者 ID（编辑失败补发用）
	createdAt time.Time
}

// NewPlugin 构造函数（plugin.json 的 entry.constructor 指向这里）。
func NewPlugin() *MusicPlugin {
	p := &MusicPlugin{
		sessions: make(map[string]*searchSession),
		byMsg:    make(map[message.QID]*searchSession),
		cooldown: make(map[string]time.Time),
		inflight: make(map[string]time.Time),
	}
	p.Name = "音乐点歌"
	p.HelpWords = "at 我发送 /点歌 关键词 搜歌，支持按钮的平台点序号直接下载、按钮翻页，/点歌 歌词 序号 看歌词"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.4.1"
	p.Order = plugin.LevelNormal
	return p
}

// OnUnload 卸载钩子：被插件市场卸载时清空内存中的选歌会话、按钮索引、
// 冷却与点播标记；Bot 退出/重启（UnloadShutdown）无需处理——这些状态
// 本就在内存、随进程结束释放。
func (p *MusicPlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	if reason != plugin.UnloadUninstall {
		return nil
	}
	p.mu.Lock()
	p.sessions = make(map[string]*searchSession)
	p.byMsg = make(map[message.QID]*searchSession)
	p.cooldown = make(map[string]time.Time)
	p.inflight = make(map[string]time.Time)
	p.mu.Unlock()
	p.Logger.Info("已清理点歌会话缓存（卸载）")
	return nil
}

// Start 初始化：配置兜底、构建 API 客户端与全局限流器。
func (p *MusicPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.sessions == nil {
		p.sessions = make(map[string]*searchSession)
	}
	if p.byMsg == nil {
		p.byMsg = make(map[message.QID]*searchSession)
	}
	if p.cooldown == nil {
		p.cooldown = make(map[string]time.Time)
	}
	if p.inflight == nil {
		p.inflight = make(map[string]time.Time)
	}
	if strings.TrimSpace(p.cfg.APIBase) == "" {
		p.cfg.APIBase = defaultAPIBase
	}
	if !validSources[p.cfg.Source] {
		if p.cfg.Source != "" {
			p.Logger.Warn("配置的音源无效，回退为 netease", "source", p.cfg.Source)
		}
		p.cfg.Source = "netease"
	}
	if !validBitrates[p.cfg.Bitrate] {
		if p.cfg.Bitrate != "" {
			p.Logger.Warn("配置的音质无效，回退为 320", "bitrate", p.cfg.Bitrate)
		}
		p.cfg.Bitrate = "320"
	}
	if p.cfg.SendMode != "file" && p.cfg.SendMode != "card" && p.cfg.SendMode != "text" {
		p.cfg.SendMode = "file"
	}
	if p.cfg.MaxSizeMB < 0 {
		p.cfg.MaxSizeMB = 20
	}
	if p.cfg.DownloadTimeoutSec < 30 {
		p.cfg.DownloadTimeoutSec = 180
	}
	if p.cfg.DownloadTimeoutSec > 600 {
		p.cfg.DownloadTimeoutSec = 600
	}
	if p.cfg.SearchCount < 1 {
		p.cfg.SearchCount = 10
	}
	if p.cfg.SearchCount > 30 {
		p.cfg.SearchCount = 30
	}
	if p.cfg.SessionMin < 1 {
		p.cfg.SessionMin = 10
	}
	if p.cfg.CooldownSec < 0 {
		p.cfg.CooldownSec = 0
	}
	if p.cfg.RateLimit5Min < 1 {
		p.cfg.RateLimit5Min = 40
	}
	p.client = newGDClient(p.cfg.APIBase, p.RestyClient)
	p.limiter = newRateLimiter(5*time.Minute, p.cfg.RateLimit5Min)
	p.Logger.Info("音乐点歌插件已初始化",
		"source", p.cfg.Source,
		"bitrate", p.cfg.Bitrate,
		"send_mode", p.cfg.SendMode,
		"max_size_mb", p.cfg.MaxSizeMB,
		"rate_limit_5min", p.cfg.RateLimit5Min,
	)
	return nil
}

// isMusicCmd 是否点歌命令（含中文别名）。
func isMusicCmd(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "music", "点歌", "音乐":
		return true
	default:
		return false
	}
}

// OnGroupMsg 群聊消息事件：必须 @机器人。
func (p *MusicPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if !cmd.Mention || !isMusicCmd(cmd.Name) {
		return true, nil
	}
	p.handleMusic(ctx, b, cmd, msg, true)
	return false, nil
}

// OnFriendMsg 私聊消息事件：无需 @。
func (p *MusicPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	if !isMusicCmd(cmd.Name) {
		return true, nil
	}
	p.handleMusic(ctx, b, cmd, msg, false)
	return false, nil
}

// handleMusic /点歌 主流程：按子命令分发。
func (p *MusicPlugin) handleMusic(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message, isGroup bool) {
	act := parseMusicArgs(cmd.Args)
	switch act.kind {
	case "help":
		p.replyText(b, msg, isGroup, helpText(p.cfg.SessionMin))
	case "search":
		if !p.passSearchLimits(b, msg, isGroup) {
			return
		}
		p.doSearchPage(ctx, b, msg, isGroup, p.cfg.Source, act.keyword, 1)
	case "next":
		if !p.passAPIQuota(b, msg, isGroup) {
			return
		}
		p.flipPage(ctx, b, msg, isGroup, 1)
	case "prev":
		if !p.passAPIQuota(b, msg, isGroup) {
			return
		}
		p.flipPage(ctx, b, msg, isGroup, -1)
	case "page":
		if act.index <= 0 {
			p.replyText(b, msg, isGroup, "用法：/点歌 页 页码，先搜索拿到列表哦")
			return
		}
		if !p.passAPIQuota(b, msg, isGroup) {
			return
		}
		p.jumpPage(ctx, b, msg, isGroup, act.index)
	case "pick":
		p.doPick(ctx, b, msg, isGroup, act.index)
	case "lyric":
		if act.index <= 0 {
			p.replyText(b, msg, isGroup, "用法：/点歌 歌词 序号，先搜索拿到列表哦")
			return
		}
		if !p.passAPIQuota(b, msg, isGroup) {
			return
		}
		p.doLyric(ctx, b, msg, isGroup, act.index)
	}
}

// flipPage 下一页/上一页：沿当前会话的关键词与音源翻页。
func (p *MusicPlugin) flipPage(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, delta int) {
	sess, ok := p.currentSession(msg)
	if !ok {
		p.replyText(b, msg, isGroup, "没有有效的搜索结果，先发 /点歌 关键词 搜索一下吧")
		return
	}
	page := sess.page + delta
	if page < 1 {
		p.replyText(b, msg, isGroup, "已经是第一页了")
		return
	}
	p.doSearchPage(ctx, b, msg, isGroup, sess.source, sess.keyword, page)
}

// jumpPage 跳到指定页：沿当前会话的关键词与音源。
func (p *MusicPlugin) jumpPage(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, page int) {
	sess, ok := p.currentSession(msg)
	if !ok {
		p.replyText(b, msg, isGroup, "没有有效的搜索结果，先发 /点歌 关键词 搜索一下吧")
		return
	}
	if page == sess.page {
		p.replyText(b, msg, isGroup, fmt.Sprintf("已经在第 %d 页了", page))
		return
	}
	p.doSearchPage(ctx, b, msg, isGroup, sess.source, sess.keyword, page)
}

// musicAction 解析后的子命令：kind 为 help/search/pick/lyric/next/prev/page。
type musicAction struct {
	kind    string
	keyword string
	index   int // pick/lyric/page 用，1 起
}

// parseMusicArgs 解析参数：无参/help → 帮助；纯数字或 选 N → 点播；
// 歌词 N → 查歌词；下一页/上一页/页 N → 翻页；其余整体作为搜索关键词。
func parseMusicArgs(args []string) musicAction {
	if len(args) == 0 {
		return musicAction{kind: "help"}
	}
	first := strings.TrimSpace(args[0])
	rest := args[1:]
	switch first {
	case "help", "帮助", "-h", "--help":
		return musicAction{kind: "help"}
	case "歌词", "词", "lyric":
		if n, ok := parseIndex(rest); ok {
			return musicAction{kind: "lyric", index: n}
		}
		return musicAction{kind: "lyric"}
	case "下一页", "下页", "next":
		return musicAction{kind: "next"}
	case "上一页", "上页", "prev":
		return musicAction{kind: "prev"}
	case "页", "page":
		if n, ok := parseIndex(rest); ok {
			return musicAction{kind: "page", index: n}
		}
		return musicAction{kind: "page"}
	case "选", "播放", "点", "play":
		if n, ok := parseIndex(rest); ok {
			return musicAction{kind: "pick", index: n}
		}
		kw := strings.TrimSpace(strings.Join(rest, " "))
		if kw == "" {
			return musicAction{kind: "help"}
		}
		return musicAction{kind: "search", keyword: kw}
	}
	if n, err := strconv.Atoi(first); err == nil {
		return musicAction{kind: "pick", index: n}
	}
	keyword := strings.TrimSpace(strings.Join(args, " "))
	if keyword == "" {
		return musicAction{kind: "help"}
	}
	return musicAction{kind: "search", keyword: keyword}
}

// parseIndex 解析序号参数：恰好一个正整数才合法。
func parseIndex(args []string) (int, bool) {
	if len(args) != 1 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// passSearchLimits 搜索前置检查：个人冷却 + 全局 API 配额；不通过时已回复提示。
func (p *MusicPlugin) passSearchLimits(b bot.Bot, msg message.Message, isGroup bool) bool {
	if p.cfg.CooldownSec > 0 {
		if ok, wait := p.takeCooldown(msg.Sender.UserId.String()); !ok {
			p.replyText(b, msg, isGroup, fmt.Sprintf("手速太快啦，冷却 %s 后再来搜歌", humanDur(wait)))
			return false
		}
	}
	return p.passAPIQuota(b, msg, isGroup)
}

// passAPIQuota 全局 API 配额检查（点播/歌词也各消耗 API 次数）。
func (p *MusicPlugin) passAPIQuota(b bot.Bot, msg message.Message, isGroup bool) bool {
	if ok, wait := p.limiter.allow(); !ok {
		p.replyText(b, msg, isGroup, fmt.Sprintf("点歌配额用完了（每 5 分钟限 %d 次），约 %s 后再试", p.cfg.RateLimit5Min, humanDur(wait)))
		return false
	}
	return true
}

// takeCooldown 消费一次搜索冷却，返回 (放行与否, 需等待时长)。
func (p *MusicPlugin) takeCooldown(key string) (bool, time.Duration) {
	now := time.Now()
	window := time.Duration(p.cfg.CooldownSec) * time.Second
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cooldown) > 4096 { // 防止长期运行后无限膨胀
		for k, t := range p.cooldown {
			if now.Sub(t) > window {
				delete(p.cooldown, k)
			}
		}
	}
	if last, ok := p.cooldown[key]; ok {
		if wait := window - now.Sub(last); wait > 0 {
			return false, wait
		}
	}
	p.cooldown[key] = now
	return true, 0
}

// inflightMaxAge 点播进行中标记的最长保留：大于最大下载超时（600 秒），
// 投递协程异常终止后标记也能自然过期，不会把点播永久卡死。
const inflightMaxAge = 15 * time.Minute

// pickInflightKey 点播去重键：会话（群/私聊）+ 音源 + 曲目 ID。
// 同一会话同一首歌同时只投递一次，不同会话/不同曲目互不影响。
func pickInflightKey(chat message.QID, source string, t *track) string {
	return chat.String() + "|pick|" + source + "|" + t.ID.String()
}

// tryAcquirePick 标记一次点播进入投递中；同键已有任务在途返回 false，
// 用于防按钮连点/重复指令导致的重复下载与重复发送。
func (p *MusicPlugin) tryAcquirePick(key string) bool {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inflight) > 1024 { // 防长期运行膨胀：顺手清理过期标记
		for k, t0 := range p.inflight {
			if now.Sub(t0) > inflightMaxAge {
				delete(p.inflight, k)
			}
		}
	}
	if _, busy := p.inflight[key]; busy {
		return false
	}
	p.inflight[key] = now
	return true
}

// releasePick 释放点播进行中标记（投递结束后调用）。
func (p *MusicPlugin) releasePick(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inflight, key)
}

// doSearchPage 按页搜索并回复候选列表，同时记入选歌会话；空页时保留原会话。
// 支持按钮的平台列表附翻页按钮（点击就地翻页），其余提示文本指令翻页。
func (p *MusicPlugin) doSearchPage(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, source, keyword string, page int) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tracks, err := p.client.searchSongs(sctx, source, keyword, p.cfg.SearchCount, page)
	if err != nil {
		p.Logger.Warn("点歌搜索失败", "error", err, "keyword", keyword, "page", page, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, "搜索失败了："+err.Error())
		return
	}
	if len(tracks) == 0 {
		if page > 1 {
			p.replyText(b, msg, isGroup, fmt.Sprintf("第 %d 页没有更多结果了，回复 /点歌 上一页 回看", page))
			return
		}
		p.replyText(b, msg, isGroup, fmt.Sprintf("没找到 %q 相关的歌曲，换个关键词试试？", keyword))
		return
	}
	sess := p.storeSession(msg, source, keyword, page, tracks)
	if p.keyboardSupported(b) {
		if msgId := p.sendList(b, sess, msg.MessageId); msgId != "" {
			p.Logger.Info("点歌搜索完成", "keyword", keyword, "source", source, "page", page, "results", len(tracks), "user", msg.Sender.UserId, "is_group", isGroup, "mode", "button")
			return
		}
		// 按钮列表发送失败 → 降级为纯文本
	}
	p.replyText(b, msg, isGroup, p.buildListText(sess, false))
	p.Logger.Info("点歌搜索完成", "keyword", keyword, "source", source, "page", page, "results", len(tracks), "user", msg.Sender.UserId, "is_group", isGroup, "mode", "text")
}

// keyboardSupported 事件来源平台是否支持内联按钮；不支持时退化为文本指令交互。
func (p *MusicPlugin) keyboardSupported(b bot.Bot) bool {
	iv, ok := b.(bot.Interactive)
	return ok && iv.SupportsKeyboard()
}

// buildListText 组装列表文案：interactive 提示点序号直接下载与按钮翻页，
// 文本模式提示指令用法。
func (p *MusicPlugin) buildListText(sess *searchSession, interactive bool) string {
	hasButtons := sess.page > 1 || len(sess.tracks) >= p.cfg.SearchCount
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎵 为你找到 %q 的候选（第 %d 页）", sess.keyword, sess.page)
	if interactive {
		sb.WriteString("，点下方序号直接下载")
		if hasButtons {
			sb.WriteString("、◀️▶️ 翻页")
		}
		fmt.Fprintf(&sb, "，歌词：回复 /点歌 歌词 序号（%d 分钟内有效）：\n", p.cfg.SessionMin)
	} else {
		fmt.Fprintf(&sb, "，回复 /点歌 序号 下载、/点歌 歌词 序号 看歌词（%d 分钟内有效）：\n", p.cfg.SessionMin)
	}
	for i, t := range sess.tracks {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, trackLine(t))
	}
	if !interactive && len(sess.tracks) >= p.cfg.SearchCount { // 整页结果大概率还有下一页
		sb.WriteString("回复 /点歌 下一页 看更多\n")
	}
	sb.WriteString(creditLine)
	return sb.String()
}

// pickColumns 序号点播按钮的每行个数。
const pickColumns = 5

// keyboardRows 列表键盘：序号点播按钮（每行 5 个，点击直接下载发送）+ 翻页行。
// 回调数据经 Meta.CallbackData 打包插件前缀，框架按前缀路由回本插件。
func (p *MusicPlugin) keyboardRows(sess *searchSession) [][]message.InlineButton {
	var rows [][]message.InlineButton
	for start := 0; start < len(sess.tracks); start += pickColumns {
		end := min(start+pickColumns, len(sess.tracks))
		row := make([]message.InlineButton, 0, end-start)
		for i := start; i < end; i++ {
			row = append(row, msgchain.Button(strconv.Itoa(i+1), p.CallbackData("pick:"+strconv.Itoa(i+1))))
		}
		rows = append(rows, row)
	}
	if row := p.paginationRow(sess); len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// paginationRow 翻页按钮行：有上一页/下一页才出现对应按钮；回调数据为
// 目标页码。
func (p *MusicPlugin) paginationRow(sess *searchSession) []message.InlineButton {
	var row []message.InlineButton
	if sess.page > 1 {
		row = append(row, msgchain.Button("◀️ 上一页", p.CallbackData("pg:"+strconv.Itoa(sess.page-1))))
	}
	if len(sess.tracks) >= p.cfg.SearchCount {
		row = append(row, msgchain.Button("▶️ 下一页", p.CallbackData("pg:"+strconv.Itoa(sess.page+1))))
	}
	return row
}

// sendList 发送带翻页按钮的列表消息（群聊回复原指令），列表消息 ID 经
// bindListMsg 记入会话供按钮点击路由与就地编辑；返回消息 ID，发送失败返回空。
func (p *MusicPlugin) sendList(b bot.Bot, sess *searchSession, replyTo message.QID) message.QID {
	text := p.buildListText(sess, true)
	rows := p.keyboardRows(sess)
	var msgId message.QID
	var ok bool
	if sess.isGroup {
		gb := msgchain.Builder().Group()
		if replyTo != "" {
			gb = gb.Reply(replyTo)
		}
		gb = gb.Text(text)
		if len(rows) > 0 {
			gb = gb.Keyboard(rows...)
		}
		msgId, ok = b.SendGroupMsg(sess.chat, gb.Build())
	} else {
		fb := msgchain.Builder().Friend().Text(text)
		if len(rows) > 0 {
			fb = fb.Keyboard(rows...)
		}
		msgId, ok = b.SendFriendMsg(sess.chat, fb.Build())
	}
	if !ok {
		p.Logger.Warn("按钮列表发送失败", "chat", sess.chat, "is_group", sess.isGroup)
		return ""
	}
	p.bindListMsg(sess, msgId)
	return msgId
}

// storeSession 保存选歌会话（返回会话指针，供发送列表后 bindListMsg 关联
// 列表消息），顺手清理过期会话与失效列表消息的按钮索引。
func (p *MusicPlugin) storeSession(msg message.Message, source, keyword string, page int, tracks []*track) *searchSession {
	exp := time.Duration(p.cfg.SessionMin) * time.Minute
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, s := range p.sessions {
		if now.Sub(s.createdAt) > exp {
			p.removeLocked(k, s)
		}
	}
	key := sessionKey(msg)
	if old, ok := p.sessions[key]; ok {
		p.removeLocked(key, old) // 同用户重新搜索：旧列表消息的按钮索引一并移除
	}
	sess := &searchSession{
		key:       key,
		keyword:   keyword,
		source:    source,
		page:      page,
		tracks:    tracks,
		isGroup:   msg.GroupId != "",
		chat:      msg.GroupId,
		createdAt: now,
	}
	if sess.chat == "" {
		sess.chat = msg.Sender.UserId
	}
	p.sessions[key] = sess
	return sess
}

// updateSessionPage 按钮翻页后以新会话对象原子替换 sessions/按钮索引表项
// （copy-on-write，见 searchSession 注释），返回新对象。
func (p *MusicPlugin) updateSessionPage(sess *searchSession, page int, tracks []*track) *searchSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	updated := *sess
	updated.page = page
	updated.tracks = tracks
	updated.createdAt = time.Now()
	p.sessions[updated.key] = &updated
	if updated.listMsg != "" {
		p.byMsg[updated.listMsg] = &updated // 点击索引跟随新对象
	}
	return &updated
}

// removeLocked 删除会话并移除其列表消息的按钮索引（需持有 p.mu）。
func (p *MusicPlugin) removeLocked(key string, s *searchSession) {
	delete(p.sessions, key)
	if s.listMsg != "" && p.byMsg[s.listMsg] == s {
		delete(p.byMsg, s.listMsg)
	}
}

// bindListMsg 记录列表消息 ID：翻页就地编辑 + 按钮点击路由索引。
func (p *MusicPlugin) bindListMsg(sess *searchSession, msgId message.QID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sess.listMsg != "" && p.byMsg[sess.listMsg] == sess {
		delete(p.byMsg, sess.listMsg) // 会话已换新列表消息，旧按钮作废
	}
	sess.listMsg = msgId
	p.byMsg[msgId] = sess
}

// sessionByMsg 按列表消息 ID 取会话（按钮点击路由），过期或不存在返回 nil。
func (p *MusicPlugin) sessionByMsg(msgId message.QID) *searchSession {
	exp := time.Duration(p.cfg.SessionMin) * time.Minute
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.byMsg[msgId]
	if !ok || time.Since(s.createdAt) > exp {
		return nil
	}
	return s
}

// OnInteraction 内联按钮点击（plugin.InteractionHandler）：载荷 "pg:<页码>"
// 为翻页、"pick:<序号>" 为点播。群内任何人可点（列表卡片共享）；API 配额
// 与文本指令共用全局限流，超限时以应答文本提示。
func (p *MusicPlugin) OnInteraction(ctx context.Context, b bot.Bot, ev *message.InteractionEvent) error {
	if !p.cfg.Enable {
		return nil
	}
	sess := p.sessionByMsg(ev.MessageId)
	if sess == nil {
		ev.AnswerText = "选歌会话已过期，请重新搜索"
		return nil
	}
	if page, ok := parsePagePayload(ev.Data); ok {
		p.onPageInteraction(ctx, b, ev, sess, page)
		return nil
	}
	if index, ok := parsePickPayload(ev.Data); ok {
		p.onPickInteraction(b, ev, sess, index)
		return nil
	}
	return nil
}

// onPageInteraction 翻页：沿会话关键词与音源搜索，就地编辑列表消息
// （编辑失败降级为补发新列表）。
func (p *MusicPlugin) onPageInteraction(ctx context.Context, b bot.Bot, ev *message.InteractionEvent, sess *searchSession, page int) {
	if page == sess.page {
		ev.AnswerText = fmt.Sprintf("已经在第 %d 页了", page)
		return
	}
	if ok, wait := p.limiter.allow(); !ok {
		ev.AnswerText = fmt.Sprintf("点歌配额用完了（每 5 分钟限 %d 次），约 %s 后再试", p.cfg.RateLimit5Min, humanDur(wait))
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tracks, err := p.client.searchSongs(sctx, sess.source, sess.keyword, p.cfg.SearchCount, page)
	if err != nil {
		p.Logger.Warn("按钮翻页搜索失败", "error", err, "keyword", sess.keyword, "page", page)
		ev.AnswerText = "搜索失败了：" + err.Error()
		return
	}
	if len(tracks) == 0 {
		ev.AnswerText = fmt.Sprintf("第 %d 页没有更多结果了", page)
		return
	}
	sess = p.updateSessionPage(sess, page, tracks)
	listMsg, isGroup := sess.listMsg, sess.isGroup
	ev.AnswerText = fmt.Sprintf("已翻到第 %d 页", page)
	if !p.editListPage(b, listMsg, isGroup, sess) {
		// 就地编辑失败（消息过旧/平台限制等）→ 补发一条带按钮的新列表
		p.sendList(b, sess, "")
	}
	p.Logger.Info("按钮翻页完成", "keyword", sess.keyword, "page", page, "user", ev.UserId, "is_group", isGroup)
}

// onPickInteraction 序号点播：先快速应答（toast 提示，回调应答必须及时返回，
// 长下载放 OnInteraction 里会拖到应答失效），下载投递在后台协程进行。
// 去重放在配额消费之前，连点不白烧配额；toast 转瞬即逝且仅点击者可见，
// 后台会再往会话发一条「正在发送」提示，让群里有明确动静、其他人也不再重复点。
func (p *MusicPlugin) onPickInteraction(b bot.Bot, ev *message.InteractionEvent, sess *searchSession, index int) {
	if index < 1 || index > len(sess.tracks) {
		ev.AnswerText = fmt.Sprintf("序号超出范围（1~%d），列表可能已更新，看最新列表再选吧", len(sess.tracks))
		return
	}
	// 快照后即与会话对象解耦（copy-on-write：会话可能被并发翻页替换）
	s := *sess
	t := s.tracks[index-1]
	key := pickInflightKey(s.chat, s.source, t)
	if !p.tryAcquirePick(key) {
		ev.AnswerText = fmt.Sprintf("《%s》正在发送中，请稍候，不要重复点击哦", truncate(t.Name, 30))
		return
	}
	if ok, wait := p.limiter.allow(); !ok {
		p.releasePick(key)
		ev.AnswerText = fmt.Sprintf("点歌配额用完了（每 5 分钟限 %d 次），约 %s 后再试", p.cfg.RateLimit5Min, humanDur(wait))
		return
	}
	ev.AnswerText = fmt.Sprintf("🎵 正在获取《%s》，稍等…", truncate(t.Name, 30))
	b.Go("music-pick", func() {
		defer p.releasePick(key)
		p.sendPlain(b, s.isGroup, s.chat, fmt.Sprintf("🎵 正在发送《%s》，请稍候…", truncate(t.Name, 30)))
		p.deliverPick(context.Background(), b, s.isGroup, s.chat, s.source, t, func(text string) {
			p.sendPlain(b, s.isGroup, s.chat, text)
		}, ev.UserId)
	})
}

// parsePagePayload 解析翻页按钮回调载荷 "pg:<页码>"。
func parsePagePayload(data string) (int, bool) {
	return parseNumPayload(data, "pg:")
}

// parsePickPayload 解析点播按钮回调载荷 "pick:<序号>"。
func parsePickPayload(data string) (int, bool) {
	return parseNumPayload(data, "pick:")
}

// parseNumPayload 解析 "<前缀><正整数>" 形式的按钮回调载荷。
func parseNumPayload(data, prefix string) (int, bool) {
	payload, ok := strings.CutPrefix(data, prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(payload)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// editListPage 就地编辑列表消息的文案与翻页按钮；不支持编辑或编辑失败
// 返回 false 由调用方降级。
func (p *MusicPlugin) editListPage(b bot.Bot, msgId message.QID, isGroup bool, sess *searchSession) bool {
	ed, ok := b.(bot.MsgEditor)
	if !ok || msgId == "" {
		return false
	}
	text := p.buildListText(sess, true)
	rows := p.keyboardRows(sess)
	if isGroup {
		gb := msgchain.Builder().Group().Text(text)
		if len(rows) > 0 {
			gb = gb.Keyboard(rows...)
		}
		return ed.EditGroupMsg(msgId, gb.Build())
	}
	fb := msgchain.Builder().Friend().Text(text)
	if len(rows) > 0 {
		fb = fb.Keyboard(rows...)
	}
	return ed.EditFriendMsg(msgId, fb.Build())
}

// currentSession 取当前会话，过期或不存在返回 false。
func (p *MusicPlugin) currentSession(msg message.Message) (*searchSession, bool) {
	exp := time.Duration(p.cfg.SessionMin) * time.Minute
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[sessionKey(msg)]
	if !ok || time.Since(s.createdAt) > exp {
		return nil, false
	}
	return s, true
}

// sessionKey 会话键：用户 + 场景（群聊按群隔离，私聊统一）。
func sessionKey(msg message.Message) string {
	if msg.GroupId == "" {
		return msg.Sender.UserId.String() + "|f"
	}
	return msg.Sender.UserId.String() + "|g:" + msg.GroupId.String()
}

// doPick 点播（文本指令）：默认下载音频以文件发送；card 模式发音乐卡片
// （失败降级为文件）；text 模式只发播放链接。先回一条「正在发送」提示——
// 大文件下载动辄十几秒，没动静容易被当成没反应而反复发指令。
func (p *MusicPlugin) doPick(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, index int) {
	sess, ok := p.currentSession(msg)
	if !ok {
		p.replyText(b, msg, isGroup, fmt.Sprintf("没有有效的选歌列表，先发 /点歌 关键词 搜索一下吧"))
		return
	}
	t := sessTrack(sess, index)
	if t == nil {
		p.replyText(b, msg, isGroup, fmt.Sprintf("序号超出范围（1~%d），看下列表再选吧", len(sess.tracks)))
		return
	}
	key := pickInflightKey(pickChat(msg), sess.source, t)
	if !p.tryAcquirePick(key) {
		p.replyText(b, msg, isGroup, fmt.Sprintf("《%s》正在发送中，请稍候，不要重复点播哦", truncate(t.Name, 30)))
		return
	}
	defer p.releasePick(key)
	if !p.passAPIQuota(b, msg, isGroup) {
		return
	}
	p.replyText(b, msg, isGroup, fmt.Sprintf("🎵 正在发送《%s》，请稍候…", truncate(t.Name, 30)))
	p.deliverPick(ctx, b, isGroup, pickChat(msg), sess.source, t, func(text string) {
		p.replyText(b, msg, isGroup, text)
	}, msg.Sender.UserId)
}

// sessTrack 取列表中的序号曲目；越界返回 nil（deliverPick 会反馈提示）。
func sessTrack(sess *searchSession, index int) *track {
	if index < 1 || index > len(sess.tracks) {
		return nil
	}
	return sess.tracks[index-1]
}

// pickChat 点播/歌词的发送目标：群聊为群 ID，私聊为发送者。
func pickChat(msg message.Message) message.QID {
	if msg.GroupId != "" {
		return msg.GroupId
	}
	return msg.Sender.UserId
}

// deliverPick 点播投递（文本指令与序号按钮共用）：取播放链接后按 SendMode
// 发卡片/文件/直链，各环节失败自动降级，保证用户总能拿到可用的结果。
// reply 承接全部文本反馈（指令路径为 @ 引用回复，按钮路径为会话直发）；
// user 仅用于日志标识发起者。
func (p *MusicPlugin) deliverPick(ctx context.Context, b bot.Bot, isGroup bool, chat message.QID, source string, t *track, reply func(string), user message.QID) {
	if t == nil {
		reply("序号超出范围，看下列表再选吧")
		return
	}
	sctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.DownloadTimeoutSec+30)*time.Second)
	defer cancel()
	res, err := p.client.songURL(sctx, source, t.ID.String(), p.cfg.Bitrate)
	if err != nil {
		p.Logger.Warn("获取歌曲播放链接失败", "error", err, "track", t.Name, "id", t.ID.String(), "user", user)
		reply("获取播放链接失败：" + err.Error())
		return
	}
	audio := res.URL.String()
	size := parseSizeBytes(res.Size.String())

	title := t.Name
	subtitle := t.artistLine()
	if t.Album != "" {
		if subtitle == "" {
			subtitle = t.Album
		} else {
			subtitle += " - " + t.Album
		}
	}

	switch p.cfg.SendMode {
	case "text":
		p.sendSongLink(b, isGroup, chat, title, subtitle, audio)
		p.Logger.Info("点歌完成", "track", title, "user", user, "is_group", isGroup, "mode", "text")
		return
	case "card":
		cover := p.fetchCover(sctx, source, t)
		if p.sendCard(b, isGroup, chat, title, subtitle, audio, cover) {
			p.Logger.Info("点歌完成", "track", title, "user", user, "is_group", isGroup, "mode", "card")
			return
		}
		// 卡片发送失败 → 降级为下载发文件
	}

	// 文件路径（file 模式，或 card 模式降级）：超过上限直接给链接并说明原因。
	limit := int64(p.cfg.MaxSizeMB) * 1024 * 1024
	if limit > 0 && size > limit {
		reply(fmt.Sprintf("这首 %.1fMB，超过下载上限 %dMB（无损音质更大，可调大「下载上限」或降低音质），给你直链：\n🔗 %s",
			float64(size)/1024/1024, p.cfg.MaxSizeMB, audio))
		return
	}
	data, ctype, err := p.client.download(sctx, audio)
	if err != nil {
		p.Logger.Warn("音频下载失败，降级为链接", "error", err, "track", title, "user", user)
		reply("下载失败了：" + err.Error() + "，给你直链：\n🔗 " + audio)
		return
	}
	if limit > 0 && int64(len(data)) > limit {
		reply(fmt.Sprintf("这首 %.1fMB，超过下载上限 %dMB（可调大「下载上限」或降低音质），给你直链：\n🔗 %s",
			float64(len(data))/1024/1024, p.cfg.MaxSizeMB, audio))
		return
	}
	name := buildAudioName(t, ctype, res.BR.String())
	if p.sendAudioFile(b, isGroup, chat, name, data) {
		p.Logger.Info("点歌完成", "track", title, "user", user, "is_group", isGroup, "mode", "file",
			"size_mb", fmt.Sprintf("%.1f", float64(len(data))/1024/1024))
		return
	}
	// 文件发送失败 → 最后兜底给链接
	reply("文件发送失败，给你直链：\n🔗 " + audio)
}

// buildAudioName 组装发送文件名：歌名 - 歌手.ext。
func buildAudioName(t *track, contentType, br string) string {
	name := t.Name
	if a := t.artistLine(); a != "" {
		name += " - " + a
	}
	return sanitizeFilename(name) + audioExt(contentType, br)
}

// fetchCover best-effort 获取封面（卡片模式用），失败不影响点播。
func (p *MusicPlugin) fetchCover(ctx context.Context, source string, t *track) string {
	pid := strings.TrimSpace(t.PicID.String())
	if pid == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := p.client.coverURL(cctx, source, pid)
	if err != nil {
		p.Logger.Warn("获取封面失败，忽略", "error", err, "track", t.Name)
		return ""
	}
	return u
}

// doLyric 查歌词：默认去时间轴，过长时 QQ 发 .lrc 文件，其余发截断文本。
func (p *MusicPlugin) doLyric(ctx context.Context, b bot.Bot, msg message.Message, isGroup bool, index int) {
	sess, ok := p.currentSession(msg)
	if !ok {
		p.replyText(b, msg, isGroup, "没有有效的选歌列表，先发 /点歌 关键词 搜索一下吧")
		return
	}
	if index < 1 || index > len(sess.tracks) {
		p.replyText(b, msg, isGroup, fmt.Sprintf("序号超出范围（1~%d），看下列表再选吧", len(sess.tracks)))
		return
	}
	t := sess.tracks[index-1]
	lid := strings.TrimSpace(t.LyricID.String())
	if lid == "" {
		lid = t.ID.String()
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := p.client.lyric(sctx, sess.source, lid)
	if err != nil {
		p.Logger.Warn("获取歌词失败", "error", err, "track", t.Name, "id", lid, "user", msg.Sender.UserId)
		p.replyText(b, msg, isGroup, "获取歌词失败："+err.Error())
		return
	}
	text := strings.TrimSpace(res.Lyric)
	if text == "" {
		p.replyText(b, msg, isGroup, fmt.Sprintf("%s 没有找到歌词", t.Name))
		return
	}
	if p.cfg.LyricStripTimestamps {
		text = stripTimestamps(text)
	}
	if tr := strings.TrimSpace(res.TLyric); tr != "" {
		if p.cfg.LyricStripTimestamps {
			tr = stripTimestamps(tr)
		}
		text = strings.TrimSpace(text) + "\n————翻译————\n" + strings.TrimSpace(tr)
	}
	if len([]rune(text)) <= lyricFileMaxChars {
		p.replyText(b, msg, isGroup, "🎼 "+t.Name+" 歌词：\n"+text)
		return
	}
	if msg.Platform == "qq" && p.sendLyricFile(b, isGroup, pickChat(msg), t, text) {
		return
	}
	p.replyText(b, msg, isGroup, fmt.Sprintf("🎼 %s 歌词（过长截断）：\n%s", t.Name, truncate(text, 1800)))
}

// sendLyricFile 歌词过长时以 .lrc 文件发送，发送失败返回 false 由调用方降级。
func (p *MusicPlugin) sendLyricFile(b bot.Bot, isGroup bool, chat message.QID, t *track, text string) bool {
	name := t.Name + ".lrc"
	if a := t.artistLine(); a != "" {
		name = t.Name + " - " + a + ".lrc"
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	var ok bool
	if isGroup {
		_, ok = b.SendGroupMsg(chat, msgchain.Builder().Group().FileBase64(name, b64).Build())
	} else {
		_, ok = b.SendFriendMsg(chat, msgchain.Builder().Friend().FileBase64(name, b64).Build())
	}
	if !ok {
		p.Logger.Warn("歌词文件发送失败", "track", t.Name, "chat", chat, "is_group", isGroup)
	}
	return ok
}

// sendCard 发 OneBot v11 自定义音乐卡片，发送失败返回 false 由调用方降级。
func (p *MusicPlugin) sendCard(b bot.Bot, isGroup bool, chat message.QID, title, subtitle, audio, cover string) bool {
	seg := musicCardSeg(title, subtitle, jumpURL, audio, cover)
	var ok bool
	if isGroup {
		_, ok = b.SendGroupMsg(chat, msgchain.Builder().Group().Raw(seg).Build())
	} else {
		_, ok = b.SendFriendMsg(chat, msgchain.Builder().Friend().Raw(seg).Build())
	}
	if !ok {
		p.Logger.Warn("音乐卡片发送失败，降级", "track", title, "chat", chat, "is_group", isGroup)
	}
	return ok
}

// sendAudioFile 把下载好的音频以文件发送（base64 直传），失败返回 false 由调用方兜底。
func (p *MusicPlugin) sendAudioFile(b bot.Bot, isGroup bool, chat message.QID, name string, data []byte) bool {
	b64 := base64.StdEncoding.EncodeToString(data)
	var ok bool
	if isGroup {
		_, ok = b.SendGroupMsg(chat, msgchain.Builder().Group().FileBase64(name, b64).Build())
	} else {
		_, ok = b.SendFriendMsg(chat, msgchain.Builder().Friend().FileBase64(name, b64).Build())
	}
	if !ok {
		p.Logger.Warn("音频文件发送失败", "name", name, "size", len(data), "chat", chat, "is_group", isGroup)
	}
	return ok
}

// sendPlain 向会话直发文本（按钮路径的反馈：无 @、无引用）。
func (p *MusicPlugin) sendPlain(b bot.Bot, isGroup bool, chat message.QID, text string) {
	if isGroup {
		if _, ok := b.SendGroupMsg(chat, msgchain.Builder().Group().Text(text).Build()); !ok {
			p.Logger.Warn("群聊消息发送失败", "chat", chat)
		}
		return
	}
	if _, ok := b.SendFriendMsg(chat, msgchain.Builder().Friend().Text(text).Build()); !ok {
		p.Logger.Warn("私聊消息发送失败", "chat", chat)
	}
}

// musicCardSeg 构造 OneBot v11 自定义音乐卡片段（type=custom）。
func musicCardSeg(title, content, jump, audio, cover string) message.OB11Segment {
	data := map[string]any{
		"type":    "custom",
		"url":     jump,
		"audio":   audio,
		"title":   title,
		"content": content,
	}
	if cover != "" {
		data["image"] = cover
	}
	return message.OB11Segment{Type: "music", Data: data}
}

// sendSongLink 文本降级：歌名 + 歌手 + 播放直链。
func (p *MusicPlugin) sendSongLink(b bot.Bot, isGroup bool, chat message.QID, title, subtitle, audio string) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎵 %s\n", title)
	if subtitle != "" {
		fmt.Fprintf(&sb, "%s\n", subtitle)
	}
	fmt.Fprintf(&sb, "🔗 %s\n%s", audio, creditLine)
	p.sendPlain(b, isGroup, chat, sb.String())
}

// replyText 回复文本（群聊带 @）。
func (p *MusicPlugin) replyText(b bot.Bot, msg message.Message, isGroup bool, text string) {
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

// helpText /点歌 help 的回复。
func helpText(sessionMin int) string {
	return fmt.Sprintf(`🎵 音乐点歌
/点歌 关键词      搜索歌曲（歌名/歌手/专辑）
/点歌 下一页      看下一页结果
/点歌 上一页      回看上一页
/点歌 页 页码     跳到指定页
/点歌 序号        下载列表中的歌曲并发送文件（%d 分钟内有效）
/点歌 选 序号     同上
/点歌 歌词 序号   查看歌词
/点歌 help        查看本帮助
支持按钮的平台可直接点序号下载、◀️▶️ 翻页，无需输指令
%s，仅供个人学习，请勿商用`, sessionMin, creditLine)
}

// filenameBadRe 文件名中的非法字符（Windows/常见聊天平台均不友好）。
var filenameBadRe = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]`)

// sanitizeFilename 清理文件名非法字符并限制长度。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(filenameBadRe.ReplaceAllString(strings.TrimSpace(name), ""))
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	if name == "" {
		name = "未命名"
	}
	return name
}

// audioExt 按响应 Content-Type 与音质推断文件扩展名。
func audioExt(contentType, br string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "flac"):
		return ".flac"
	case strings.Contains(ct, "mp4") || strings.Contains(ct, "m4a"):
		return ".m4a"
	case strings.Contains(ct, "ogg"):
		return ".ogg"
	case strings.Contains(ct, "wav"):
		return ".wav"
	case strings.Contains(ct, "mpeg") || strings.Contains(ct, "mp3"):
		return ".mp3"
	}
	if n, err := strconv.Atoi(strings.TrimSpace(br)); err == nil && n >= 740 {
		return ".flac" // 740/999 为无损，容器一般是 flac
	}
	return ".mp3"
}

// parseSizeBytes 解析 API 返回的文件大小（字节），解析失败按 0 处理。
func parseSizeBytes(s string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n < 0 {
		return 0
	}
	return int64(n)
}

// trackLine 列表里的一行：歌名 - 歌手 [专辑]，缺省部分跳过。
func trackLine(t *track) string {
	line := t.Name
	if a := t.artistLine(); a != "" {
		line += " - " + a
	}
	if t.Album != "" {
		line += " [" + t.Album + "]"
	}
	return line
}

// humanDur 人类可读的等待时长提示。
func humanDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds())+1)
	}
	return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60+1)
}

// lrcTimeRe LRC 时间轴标记，如 [01:23.45]、[1:2.3]、[00:12:55]。
var lrcTimeRe = regexp.MustCompile(`\[\d{1,3}:\d{1,2}(?:[.:]\d{1,3})?\]`)

// lrcMetaRe LRC 头部信息标记，如 [ti:歌名]、[ar:歌手]、[offset:500]。
var lrcMetaRe = regexp.MustCompile(`^\[[a-zA-Z]{1,8}:.*\]\s*`)

// stripTimestamps 去掉 LRC 时间轴与头部信息标记，并压缩空行。
func stripTimestamps(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(lrcTimeRe.ReplaceAllString(line, ""))
		line = strings.TrimSpace(lrcMetaRe.ReplaceAllString(line, ""))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// rateLimiter 滑动窗口限流器：保护官方 API 的频率配额（50 次/5 分钟）。
type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   []time.Time
}

func newRateLimiter(window time.Duration, limit int) *rateLimiter {
	return &rateLimiter{window: window, limit: limit}
}

// allow 尝试消费一次配额，返回 (放行与否, 需等待时长)。
func (r *rateLimiter) allow() (bool, time.Duration) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	valid := r.hits[:0]
	for _, t := range r.hits {
		if now.Sub(t) < r.window {
			valid = append(valid, t)
		}
	}
	r.hits = valid
	if len(r.hits) >= r.limit {
		return false, r.window - now.Sub(r.hits[0])
	}
	r.hits = append(r.hits, now)
	return true, 0
}
