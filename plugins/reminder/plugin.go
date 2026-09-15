// Package reminder 提醒事项插件：在群聊或私聊里设定一次性/循环提醒，
// 到点后机器人主动推送。支持中文时间描述（30分钟后、每天8:30、每周一 18:00、
// 明天 9点、09-15 10:00 等），数据持久化保存，重启不丢。
package reminder

import (
	"context"
	"fmt"
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

// reminderConfig 插件配置。
type reminderConfig struct {
	Enable               bool `cfg:"plugin.reminder.enable" label:"启用提醒事项" group:"提醒事项" default:"true" help:"关闭后不响应提醒命令且停止到点推送"`
	CheckIntervalSeconds int  `cfg:"plugin.reminder.check_interval_seconds" label:"检查间隔(秒)" group:"提醒事项" default:"30" help:"到点检查频率，提醒最多延迟该秒数送达"`
	MaxPerChat           int  `cfg:"plugin.reminder.max_per_chat" label:"单会话提醒上限" group:"提醒事项" default:"30" help:"每个群聊/私聊同时进行的提醒数量上限"`
	MaxContentLen        int  `cfg:"plugin.reminder.max_content_length" label:"内容长度上限" group:"提醒事项" default:"100" help:"提醒内容的最大字符数"`
}

// Reminder 一条提醒的持久化数据。
type Reminder struct {
	ID        string `json:"id"`         // 唯一 ID：r + 纳秒时间戳
	ChatID    string `json:"chat_id"`    // 会话标识：g:<群ID> / u:<用户ID>
	UserID    string `json:"user_id"`    // 创建者 QID（到点时 @）
	Nickname  string `json:"nickname"`   // 创建者昵称（列表展示用）
	Content   string `json:"content"`    // 提醒内容
	NextAt    int64  `json:"next_at"`    // 下次触发时间（Unix 秒）
	Repeat    string `json:"repeat"`     // 重复规则："" / daily / weekly:0~6
	CreatedAt int64  `json:"created_at"` // 创建时间（Unix 秒）
}

// ReminderPlugin 插件定义。
type ReminderPlugin struct {
	plugin.Meta
	cfg    reminderConfig
	store  storage.PersistentStorage // Clone("reminder") 后的数据命名空间
	mu     sync.Mutex                // 保护 items 与存储写
	items  map[string]*Reminder      // 内存索引：ID → 提醒
	cancel context.CancelFunc        // 停止扫描 goroutine
}

// NewPlugin 构造函数。
func NewPlugin() *ReminderPlugin {
	p := &ReminderPlugin{}
	p.Name = "提醒事项"
	p.HelpWords = "@我 /提醒 30分钟后 喝水 设定提醒，/提醒 每天8:30 早安 循环提醒，/提醒列表 查看，/删除提醒 <序号> 取消"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 声明配置结构体。
func (p *ReminderPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化：打开存储命名空间，加载已有提醒到内存。
func (p *ReminderPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("reminder")
		p.reload(ctx)
	}
	p.Logger.Info("提醒事项插件初始化",
		"enable", p.cfg.Enable,
		"check_interval_seconds", p.cfg.CheckIntervalSeconds,
		"max_per_chat", p.cfg.MaxPerChat,
		"loaded", len(p.items),
	)
	return nil
}

// reload 从持久化存储加载全部提醒。
func (p *ReminderPlugin) reload(ctx context.Context) {
	keys, err := p.store.Keys(ctx, "r:")
	if err != nil {
		p.Logger.Error("加载提醒数据失败", "error", err)
		return
	}
	items := make(map[string]*Reminder, len(keys))
	for _, k := range keys {
		var r Reminder
		if p.store.Get(ctx, k, &r) && r.ID != "" {
			items[r.ID] = &r
		}
	}
	p.mu.Lock()
	p.items = items
	p.mu.Unlock()
}

// Awake 启动到点扫描 goroutine。
func (p *ReminderPlugin) Awake(ctx context.Context, b bot.Bot) error {
	if !p.cfg.Enable {
		p.Logger.Info("提醒事项插件已在配置中禁用")
		return nil
	}
	if p.store == nil {
		p.Logger.Warn("持久化存储不可用，提醒事项功能停用")
		return nil
	}
	scanCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.scanLoop(scanCtx, b, p.checkInterval())
	return nil
}

// scanLoop 周期扫描到点的提醒。
func (p *ReminderPlugin) scanLoop(ctx context.Context, b bot.Bot, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.fireDue(b)
		}
	}
}

// fireDue 推送所有到点的提醒。
func (p *ReminderPlugin) fireDue(b bot.Bot) {
	now := time.Now().Unix()

	due := p.takeDue(now)
	for _, r := range due {
		func() {
			// 单条推送异常不中断本轮扫描
			defer func() {
				if e := recover(); e != nil {
					p.Logger.Error("推送提醒时 panic", "id", r.ID, "panic", e)
				}
			}()
			p.sendReminder(b, r)
		}()
		p.afterFire(r, now)
	}
}

// takeDue 取出到点提醒的快照并从内存移除（一次性）；循环提醒由 afterFire 重新写入。
func (p *ReminderPlugin) takeDue(now int64) []*Reminder {
	p.mu.Lock()
	defer p.mu.Unlock()
	var due []*Reminder
	for id, r := range p.items {
		if r.NextAt <= now {
			due = append(due, r)
			delete(p.items, id)
		}
	}
	return due
}

// afterFire 推送后处理：循环提醒推进到下一次并持久化，一次性提醒删除存储。
func (p *ReminderPlugin) afterFire(r *Reminder, now int64) {
	if r.Repeat != repeatNone {
		next, ok := nextRepeat(r.NextAt, time.Unix(now, 0), r.Repeat)
		if !ok {
			p.Logger.Warn("循环提醒长期未运行已停用", "id", r.ID, "repeat", r.Repeat)
			p.mu.Lock()
			p.store.Del(context.Background(), "r:"+r.ID)
			p.mu.Unlock()
			return
		}
		r.NextAt = next
		p.mu.Lock()
		p.items[r.ID] = r
		p.store.Set(context.Background(), "r:"+r.ID, r)
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	p.store.Del(context.Background(), "r:"+r.ID)
	p.mu.Unlock()
}

// sendReminder 推送一条提醒到会话。
func (p *ReminderPlugin) sendReminder(b bot.Bot, r *Reminder) {
	if strings.HasPrefix(r.ChatID, "g:") {
		c := msgchain.Builder().Group()
		c.Mention(message.FromString(r.UserID))
		text := " ⏰ 提醒：" + r.Content
		if r.Repeat != repeatNone {
			text += "（" + repeatText(r.Repeat) + "提醒）"
		}
		c.Text(text)
		if _, ok := b.SendGroupMsg(message.FromString(r.ChatID[2:]), c.Build()); !ok {
			p.Logger.Warn("提醒推送失败", "id", r.ID, "chat", r.ChatID)
		}
		return
	}
	c := msgchain.Builder().Friend()
	c.Text("⏰ 提醒：" + r.Content)
	if _, ok := b.SendFriendMsg(message.FromString(r.UserID), c.Build()); !ok {
		p.Logger.Warn("提醒推送失败", "id", r.ID, "chat", r.ChatID)
	}
}

// OnGroupMsg 群聊消息事件。
func (p *ReminderPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	switch cmd.Name {
	case "提醒", "提醒我", "remind":
		p.cmdSet(ctx, b, "g:"+msg.GroupId.String(), cmd.Args, msg)
	case "提醒列表", "我的提醒", "reminds":
		p.cmdList(ctx, b, "g:"+msg.GroupId.String())
	case "删除提醒", "删提醒", "取消提醒":
		p.cmdDel(ctx, b, "g:"+msg.GroupId.String(), cmd.Args)
	default:
		return true, nil
	}
	return false, nil
}

// OnFriendMsg 私聊消息事件（无需 @）。
func (p *ReminderPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	switch cmd.Name {
	case "提醒", "提醒我", "remind":
		p.cmdSet(ctx, b, "u:"+msg.Sender.UserId.String(), cmd.Args, msg)
	case "提醒列表", "我的提醒", "reminds":
		p.cmdList(ctx, b, "u:"+msg.Sender.UserId.String())
	case "删除提醒", "删提醒", "取消提醒":
		p.cmdDel(ctx, b, "u:"+msg.Sender.UserId.String(), cmd.Args)
	default:
		return true, nil
	}
	return false, nil
}

// chatReminders 某会话进行中的提醒，按触发时间排序（列表序号与此一致）。
func (p *ReminderPlugin) chatReminders(chat string) []*Reminder {
	p.mu.Lock()
	defer p.mu.Unlock()
	var rs []*Reminder
	for _, r := range p.items {
		if r.ChatID == chat {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].NextAt < rs[j].NextAt })
	return rs
}

// cmdSet 设定提醒。
func (p *ReminderPlugin) cmdSet(ctx context.Context, b bot.Bot, chat string, args []string, msg message.Message) {
	if p.store == nil {
		p.reply(b, chat, "", "存储当前不可用，无法设置提醒")
		return
	}
	input := strings.TrimSpace(strings.Join(args, " "))
	if input == "" {
		p.reply(b, chat, "", reminderHelp)
		return
	}

	pt, content, err := parseReminderTime(input, time.Now())
	if err != nil {
		p.reply(b, chat, "", err.Error())
		return
	}
	content = strings.TrimSpace(content)
	if content == "" {
		p.reply(b, chat, "", "缺少提醒内容，例如：/提醒 30分钟后 喝水")
		return
	}
	if p.cfg.MaxContentLen > 0 {
		r := []rune(content)
		if len(r) > p.cfg.MaxContentLen {
			content = string(r[:p.cfg.MaxContentLen])
		}
	}

	// 数量上限
	if p.cfg.MaxPerChat > 0 && len(p.chatReminders(chat)) >= p.cfg.MaxPerChat {
		p.reply(b, chat, "", fmt.Sprintf("本会话进行中的提醒已达上限（%d 个），请先清理", p.cfg.MaxPerChat))
		return
	}

	r := &Reminder{
		ID:        "r" + strconv.FormatInt(time.Now().UnixNano(), 36),
		ChatID:    chat,
		UserID:    msg.Sender.UserId.String(),
		Nickname:  msg.Sender.Card,
		Content:   content,
		NextAt:    pt.At.Unix(),
		Repeat:    pt.Repeat,
		CreatedAt: time.Now().Unix(),
	}
	if r.Nickname == "" {
		r.Nickname = msg.Sender.Nickname
	}

	p.mu.Lock()
	ok := p.store.Set(ctx, "r:"+r.ID, r)
	if ok {
		p.items[r.ID] = r
	}
	p.mu.Unlock()
	if !ok {
		p.reply(b, chat, "", "保存提醒失败，请稍后再试")
		return
	}

	p.reply(b, chat, msg.Sender.UserId, fmt.Sprintf("⏰ 已设定提醒：%s\n时间：%s 周%s%s",
		content, pt.At.Format("2006-01-02 15:04"), weekdayCN(pt.At), repeatSuffix(pt.Repeat)))
}

// cmdList 列出会话进行中的提醒。
func (p *ReminderPlugin) cmdList(ctx context.Context, b bot.Bot, chat string) {
	rs := p.chatReminders(chat)
	if len(rs) == 0 {
		p.reply(b, chat, "", "当前没有进行中的提醒，用 /提醒 <时间> <内容> 添加")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "⏰ 本会话进行中的提醒（%d 个）：\n", len(rs))
	for i, r := range rs {
		fmt.Fprintf(&sb, "%d. %s %s%s\n", i+1,
			time.Unix(r.NextAt, 0).Format("01-02 15:04"),
			repeatSuffix(r.Repeat), r.Content)
	}
	p.reply(b, chat, "", sb.String())
}

// cmdDel 按列表序号删除提醒。
func (p *ReminderPlugin) cmdDel(ctx context.Context, b bot.Bot, chat string, args []string) {
	n, err := strconv.Atoi(strings.TrimSpace(strings.Join(args, " ")))
	if err != nil || n < 1 {
		p.reply(b, chat, "", "用法：/删除提醒 <序号>，序号可用 /提醒列表 查看")
		return
	}
	rs := p.chatReminders(chat)
	if n > len(rs) {
		p.reply(b, chat, "", "序号超出范围，用 /提醒列表 查看列表")
		return
	}
	target := rs[n-1]
	p.mu.Lock()
	delete(p.items, target.ID)
	ok := p.store.Del(ctx, "r:"+target.ID)
	p.mu.Unlock()
	if !ok {
		p.reply(b, chat, "", "删除失败，请稍后再试")
		return
	}
	p.reply(b, chat, "", "已删除提醒："+target.Content)
}

// ---------- 小工具 ----------

const reminderHelp = "提醒用法：/提醒 <时间> <内容>\n" +
	"时间支持：30分钟后 / 半小时后 / 每天8:30 / 每周一 18:00 / 明天 9点 / 09-15 10:00 / 18:00\n" +
	"例：/提醒 2小时后 开会、/提醒 每天8:30 早安"

// reply 向会话回复文本；mentionUser 非空时在群聊开头 @ 该用户。
func (p *ReminderPlugin) reply(b bot.Bot, chat string, mentionUser message.QID, text string) {
	if strings.HasPrefix(chat, "g:") {
		c := msgchain.Builder().Group()
		if mentionUser != "" {
			c.Mention(mentionUser)
			c.Text(" ")
		}
		c.Text(text)
		if _, ok := b.SendGroupMsg(message.FromString(chat[2:]), c.Build()); !ok {
			p.Logger.Warn("提醒回复发送失败", "chat", chat)
		}
		return
	}
	c := msgchain.Builder().Friend()
	c.Text(text)
	if _, ok := b.SendFriendMsg(message.FromString(chat[2:]), c.Build()); !ok {
		p.Logger.Warn("提醒回复发送失败", "chat", chat)
	}
}

// checkInterval 检查间隔，钳制在 10 秒～1 小时。
func (p *ReminderPlugin) checkInterval() time.Duration {
	s := p.cfg.CheckIntervalSeconds
	if s < 10 {
		s = 10
	}
	if s > 3600 {
		s = 3600
	}
	return time.Duration(s) * time.Second
}

// repeatSuffix 循环规则的后缀描述。
func repeatSuffix(repeat string) string {
	if repeat == repeatNone {
		return ""
	}
	return "（" + repeatText(repeat) + "）"
}

// weekdayCN 星期的中文表示（pt.At.Format 的 "周一" 在部分平台不可靠，手动映射）。
func weekdayCN(t time.Time) string {
	return [...]string{"日", "一", "二", "三", "四", "五", "六"}[t.Weekday()]
}
