// Package videoparse 是插件市场的群聊视频解析插件：群成员发送抖音、快手、哔哩哔哩的
// 视频分享链接后，插件自动解析出无水印原视频、下载到本地并回发到群里；
// 也支持 `@机器人 /解析 <链接>` 手动触发与私聊使用。
package videoparse

import (
	"context"
	"os"
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

// 命令名（/解析 与英文别名均可）。
const (
	cmdParse    = "解析"
	cmdParseEN  = "parse"
	cmdParseAlt = "视频解析"
)

// 运行期常量。
const (
	// sameLinkTTL 同一链接在同一会话内的去重窗口：窗口内重复出现的链接不再解析，
	// 避免同一视频被多人转发时刷屏。
	sameLinkTTL = 10 * time.Minute
	// chatCooldown 同会话两次发送视频的最小间隔，抑制连发刷屏。
	chatCooldown = 5 * time.Second
	// parseConcurrency 全局并发解析（含下载）上限，避免同时下载大量视频打爆带宽。
	parseConcurrency = 3
	// parseTimeout 单个链接从解析到发送的总超时。
	parseTimeout = 4 * time.Minute
	// sweepInterval 去重/冷却表与遗留文件的清理间隔。
	sweepInterval = 10 * time.Minute
)

// VideoParsePlugin 视频解析插件定义：嵌入 plugin.Meta 获得全部默认实现，只覆盖用到的部分。
type VideoParsePlugin struct {
	plugin.Meta
	cfg videoParseConfig

	// client 兜底 HTTP 客户端：框架未注入 RestyClient 时惰性创建（见 httpClient）。
	client     *resty.Client
	clientOnce sync.Once

	// apiDisabled 解析接口被显式关闭（api_url 配置为 off/none/disabled）。
	apiDisabled bool
	// apiInterval 解析接口调用的最小间隔（测试可调为 0）。
	apiInterval time.Duration

	// groupSet 生效群集合（为空表示全部群生效）。
	groupSet map[string]struct{}
	// ctx 插件生命周期上下文，用于派生后台解析任务（避免随事件 ctx 被取消）。
	ctx context.Context
	// tempDirOverride 测试用：覆盖临时文件目录，避免污染仓库工作目录。
	tempDirOverride string

	// sem 全局并发解析信号量。
	sem chan struct{}

	// apiMu/apiLast 解析接口的调用节流。
	apiMu   sync.Mutex
	apiLast time.Time

	mu       sync.Mutex
	seen     map[string]time.Time // 链接去重表：会话+链接键 → 首次出现时间
	inflight map[string]struct{}  // 正在解析的链接
	cooldown map[string]time.Time // 会话发送冷却：会话 ID → 下次可发送时间
	closed   bool                 // 卸载闸门：清理后不再接受新任务
}

// NewPlugin 构造函数（plugin.json 的 entry.constructor 指向这里）。
func NewPlugin() *VideoParsePlugin {
	p := &VideoParsePlugin{
		seen:        make(map[string]time.Time),
		inflight:    make(map[string]struct{}),
		cooldown:    make(map[string]time.Time),
		apiInterval: apiMinInterval,
	}
	p.Name = "视频解析"
	p.HelpWords = "发送抖音/快手/B站视频分享链接自动解析原视频，也可 at 我发送 /解析 <链接> 手动触发"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 返回配置结构体指针（框架反射注册字段并渲染面板表单）。
func (p *VideoParsePlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化：配置兜底、生效群集合与后台清理。
func (p *VideoParsePlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	p.ctx = ctx
	switch strings.ToLower(strings.TrimSpace(p.cfg.APIURL)) {
	case "", "default":
		p.cfg.APIURL = defaultAPIURL
	case "off", "none", "disabled":
		p.apiDisabled = true
		p.cfg.APIURL = ""
	}
	if p.cfg.MaxSizeMB <= 0 {
		p.cfg.MaxSizeMB = 200
	}
	p.groupSet = normalizeGroupIDs(p.cfg.GroupIDs)
	p.sem = make(chan struct{}, parseConcurrency)
	p.cleanupTempFiles()

	// 定时清理去重表与遗留临时文件（随插件 ctx 退出）
	go p.backgroundLoop(ctx)
	p.Logger.Info("视频解析插件已启动",
		"auto_parse", p.cfg.AutoParse,
		"groups", len(p.groupSet),
		"max_size_mb", p.cfg.MaxSizeMB,
		"api", p.cfg.APIURL)
	return nil
}

// backgroundLoop 周期清理去重表/冷却表与遗留临时文件，直到插件上下文结束。
func (p *VideoParsePlugin) backgroundLoop(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepState()
			p.cleanupTempFiles()
		}
	}
}

// sweepState 清理过期的去重与冷却记录，避免长期运行后内存增长。
func (p *VideoParsePlugin) sweepState() {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, at := range p.seen {
		if now.Sub(at) > sameLinkTTL {
			delete(p.seen, key)
		}
	}
	for key, until := range p.cooldown {
		if now.After(until) {
			delete(p.cooldown, key)
		}
	}
}

// OnUnload 卸载钩子：清空内存状态并关闭闸门，阻止清理后仍有后台任务回写。
// 临时文件在彻底卸载时删除；Bot 重启（UnloadShutdown）保留目录继续使用。
func (p *VideoParsePlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	p.mu.Lock()
	p.closed = true
	p.seen = make(map[string]time.Time)
	p.inflight = make(map[string]struct{})
	p.cooldown = make(map[string]time.Time)
	p.mu.Unlock()
	if reason == plugin.UnloadUninstall {
		if err := os.RemoveAll(p.tempDir()); err != nil {
			p.Logger.Warn("清理临时目录失败", "err", err)
		}
		p.Logger.Info("已清理视频解析插件数据（卸载）")
	}
	return nil
}

// OnGroupMsg 群聊事件：自动识别分享链接，或响应 /解析 命令。
func (p *VideoParsePlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	return p.handle(ctx, b, cmd, msg, true)
}

// OnFriendMsg 私聊事件：逻辑同群聊，只是不需要 at 机器人。
func (p *VideoParsePlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	return p.handle(ctx, b, cmd, msg, false)
}

// handle 群聊/私聊共用的事件处理：命中命令或自动解析时返回 false（截断后续插件）。
func (p *VideoParsePlugin) handle(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message, isGroup bool) (bool, error) {
	if !p.cfg.Enable || p.isClosed() {
		return true, nil
	}
	if isGroup && len(p.groupSet) > 0 {
		if _, ok := p.groupSet[msg.GroupId.String()]; !ok {
			return true, nil
		}
	}
	if msg.Sender.UserId == msg.SelfId {
		return true, nil
	}
	// 只处理含链接的消息，其余直接放行
	text := stripCQCode(msg.RawMessage)
	if strings.TrimSpace(text) == "" {
		return true, nil
	}

	manual := p.isParseCommand(cmd, isGroup)
	links := detectLinks(text)
	if len(links) == 0 {
		if manual {
			// 手动命令但没带链接：提示用法并截断
			p.replyUsage(b, msg, isGroup)
			return false, nil
		}
		return true, nil
	}
	if !manual && !p.cfg.AutoParse {
		return true, nil
	}

	chat := p.chatID(msg, isGroup)
	accepted := p.claim(chat, links)
	if len(accepted) == 0 {
		// 全部链接都在处理中/冷却中：命令模式给个提示，自动模式静默放行
		if manual {
			p.replyText(b, msg, isGroup, "这个链接刚刚已经在解析啦，请稍等～")
			return false, nil
		}
		return true, nil
	}
	p.Logger.Info("开始解析视频分享链接",
		"platform", accepted[0].Platform,
		"count", len(accepted),
		"group", isGroup,
		"chat", chat)
	// 后台解析：不阻塞事件循环（下载大视频可能耗时数十秒）
	go p.processLinks(chat, accepted, text, b, msg, isGroup)
	return false, nil
}

// claim 认领本次要解析的链接：过滤掉窗口内出现过的重复链接与正在处理中的链接。
func (p *VideoParsePlugin) claim(chat string, links []detectedLink) []detectedLink {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	out := make([]detectedLink, 0, len(links))
	for _, link := range links {
		key := chat + "|" + dedupeKey(link.Platform, link.URL)
		if at, ok := p.seen[key]; ok && now.Sub(at) < sameLinkTTL {
			continue
		}
		if _, ok := p.inflight[key]; ok {
			continue
		}
		if until, ok := p.cooldown[chat]; ok && now.Before(until) {
			continue
		}
		p.seen[key] = now
		p.inflight[key] = struct{}{}
		p.cooldown[chat] = now.Add(chatCooldown)
		out = append(out, link)
	}
	return out
}

// processLinks 后台逐个解析并发送：解析 → 下载 → 发送 → 清理临时文件。
func (p *VideoParsePlugin) processLinks(chat string, links []detectedLink, shareText string, b bot.Bot, msg message.Message, isGroup bool) {
	defer func() {
		p.mu.Lock()
		for _, link := range links {
			delete(p.inflight, chat+"|"+dedupeKey(link.Platform, link.URL))
		}
		p.mu.Unlock()
	}()
	for _, link := range links {
		select {
		case p.sem <- struct{}{}:
		case <-p.ctx.Done():
			return
		}
		func() {
			defer func() { <-p.sem }()
			p.processOne(link, shareText, b, msg, isGroup)
		}()
	}
}

// processOne 处理单个链接：解析、下载、发送，任何一步失败都回一句可读的错误提示。
func (p *VideoParsePlugin) processOne(link detectedLink, shareText string, b bot.Bot, msg message.Message, isGroup bool) {
	if p.isClosed() {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, parseTimeout)
	defer cancel()

	start := time.Now()
	info, err := p.resolve(ctx, link, shareText)
	if err != nil {
		p.Logger.Warn("解析视频失败", "platform", link.Platform, "url", link.URL, "err", err)
		p.replyText(b, msg, isGroup, "解析失败："+err.Error())
		return
	}
	p.Logger.Info("解析成功，开始下载",
		"platform", link.Platform,
		"title", truncateText(info.Title, 40),
		"size", humanSize(info.SizeBytes))

	media, err := p.downloadMedia(ctx, info)
	if err != nil {
		p.Logger.Warn("下载视频失败", "platform", link.Platform, "err", err)
		p.replyText(b, msg, isGroup, "解析成功但下载视频失败："+err.Error())
		return
	}
	defer p.removeTempFile(media.path)

	if p.isClosed() {
		return
	}
	if !p.sendVideo(b, msg, isGroup, info, media.path) {
		p.Logger.Warn("视频消息发送失败", "platform", link.Platform, "title", truncateText(info.Title, 40))
		return
	}
	p.Logger.Info("视频发送完成",
		"platform", link.Platform,
		"size", humanSize(media.size),
		"cost", time.Since(start).Round(time.Millisecond).String())
}

// sendVideo 组装并发送「一行标题信息 + 视频」的消息。
func (p *VideoParsePlugin) sendVideo(b bot.Bot, msg message.Message, isGroup bool, info *videoInfo, path string) bool {
	title := p.titleLine(info)
	if isGroup {
		c := msgchain.Builder().Group()
		if title != "" {
			c.Text(title)
		}
		c.Reply(msg.MessageId).VideoLocal(path)
		_, ok := b.SendGroupMsg(msg.GroupId, c.Build())
		return ok
	}
	c := msgchain.Builder().Friend()
	if title != "" {
		c.Text(title)
	}
	c.Reply(msg.MessageId).VideoLocal(path)
	_, ok := b.SendFriendMsg(msg.Sender.UserId, c.Build())
	return ok
}

// titleLine 生成标题行「【平台】标题 - 作者（时长）」；无标题或未开启时返回空串。
func (p *VideoParsePlugin) titleLine(info *videoInfo) string {
	if !p.cfg.SendTitle {
		return ""
	}
	title := truncateText(info.Title, 60)
	if title == "" {
		return ""
	}
	line := "【" + platformNames[info.Platform] + "】" + title
	if author := strings.TrimSpace(info.Author); author != "" {
		line += " - " + author
	}
	if duration := humanDuration(info.Duration); duration != "" {
		line += "（" + duration + "）"
	}
	return line
}

// isParseCommand 判断是否为本插件的手动解析命令（群聊须同时 at 机器人）。
func (p *VideoParsePlugin) isParseCommand(cmd command.Command, isGroup bool) bool {
	if isGroup && !cmd.Mention {
		return false
	}
	switch cmd.Name {
	case cmdParse, cmdParseEN, cmdParseAlt:
		return true
	}
	return false
}

// chatID 会话标识：群聊用群 ID，私聊用对方 ID（去重与冷却的作用域）。
func (p *VideoParsePlugin) chatID(msg message.Message, isGroup bool) string {
	if isGroup {
		return msg.GroupId.String()
	}
	return msg.Sender.UserId.String()
}

// replyUsage 提示命令用法。
func (p *VideoParsePlugin) replyUsage(b bot.Bot, msg message.Message, isGroup bool) {
	p.replyText(b, msg, isGroup, "用法：发送视频分享链接即可自动解析，或使用 /解析 <链接>。支持 "+supportedPlatformsHint+"。")
}

// replyText 回复一句纯文本（失败仅记日志，不再打扰用户）。
func (p *VideoParsePlugin) replyText(b bot.Bot, msg message.Message, isGroup bool, text string) {
	var ok bool
	if isGroup {
		c := msgchain.Builder().Group()
		c.Text(text).Reply(msg.MessageId)
		_, ok = b.SendGroupMsg(msg.GroupId, c.Build())
	} else {
		c := msgchain.Builder().Friend()
		c.Text(text).Reply(msg.MessageId)
		_, ok = b.SendFriendMsg(msg.Sender.UserId, c.Build())
	}
	if !ok {
		p.Logger.Debug("文本回复发送失败", "text", truncateText(text, 40))
	}
}

// isClosed 读取卸载闸门。
func (p *VideoParsePlugin) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
