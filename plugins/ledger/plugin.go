// Package ledger 群记账本插件：为每个群聊维护一本共享账本（私聊则为个人账本），
// 支持记一笔、月度汇总、流水明细、按流水号删除与清账。
// 数据按「会话 × 月份」分键持久化，重启不丢。
package ledger

import (
	"context"
	"fmt"
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

// ledgerConfig 插件配置。
type ledgerConfig struct {
	Enable         bool `cfg:"plugin.ledger.enable" label:"启用记账本" group:"群记账本" default:"true" help:"关闭后不响应任何记账命令"`
	MaxRecords     int  `cfg:"plugin.ledger.max_records" label:"单月记录上限" group:"群记账本" default:"500" help:"每个会话每月最多保存的记录条数"`
	MaxNoteLen     int  `cfg:"plugin.ledger.max_note_length" label:"事项长度上限" group:"群记账本" default:"30" help:"记账事项的最大字符数"`
	MaxAmountYuan  int  `cfg:"plugin.ledger.max_amount_yuan" label:"单笔金额上限(元)" group:"群记账本" default:"1000000" help:"单笔记账金额的绝对值上限"`
	FlowListSize   int  `cfg:"plugin.ledger.flow_list_size" label:"流水展示条数" group:"群记账本" default:"10" help:"/流水 命令展示的最近记录条数"`
	SummaryRecords int  `cfg:"plugin.ledger.summary_records" label:"账单附带明细条数" group:"群记账本" default:"5" help:"/账单 汇总末尾附带的最近记录条数"`
}

// record 一条账目记录。
type record struct {
	Seq    int64  `json:"seq"`    // 月内流水号（删除不回收）
	Amount int64  `json:"amount"` // 分；正=收入 负=支出
	Note   string `json:"note"`   // 事项
	UserID string `json:"user_id"`
	Name   string `json:"name"` // 记账人昵称
	Time   int64  `json:"time"` // 记账时间（Unix 秒）
}

// book 一个月的账本数据。
type book struct {
	NextSeq int64    `json:"next_seq"`
	Records []record `json:"records"`
}

// LedgerPlugin 插件定义。
type LedgerPlugin struct {
	plugin.Meta
	cfg   ledgerConfig
	store storage.PersistentStorage // Clone("ledger") 后的数据命名空间
	mu    sync.Mutex                // 保护账本的读改写
}

// NewPlugin 构造函数。
func NewPlugin() *LedgerPlugin {
	p := &LedgerPlugin{}
	p.Name = "群记账本"
	p.HelpWords = "@我 /记账 25.5 午饭 记支出、/记账 +3000 工资 记收入，/账单 看本月汇总，/流水 看明细，/删账 <流水号> 删除，/清账 清空本月"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 声明配置结构体。
func (p *LedgerPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化。
func (p *LedgerPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("ledger")
	}
	p.Logger.Info("群记账本插件初始化",
		"enable", p.cfg.Enable,
		"max_records", p.cfg.MaxRecords,
		"max_amount_yuan", p.cfg.MaxAmountYuan,
	)
	return nil
}

// OnGroupMsg 群聊消息事件：群共享账本。
func (p *LedgerPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	return p.handle(ctx, b, "g:"+msg.GroupId.String(), cmd, msg)
}

// OnFriendMsg 私聊消息事件：个人账本（无需 @）。
func (p *LedgerPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	return p.handle(ctx, b, "u:"+msg.Sender.UserId.String(), cmd, msg)
}

// handle 命令分发。
func (p *LedgerPlugin) handle(ctx context.Context, b bot.Bot, chat string, cmd command.Command, msg message.Message) (bool, error) {
	switch strings.ToLower(cmd.Name) {
	case "记账", "记一笔", "记":
		p.cmdAdd(ctx, b, chat, cmd.Args, msg)
	case "账单", "月账单", "汇总":
		p.cmdSummary(ctx, b, chat)
	case "流水", "明细":
		p.cmdFlow(ctx, b, chat)
	case "删账", "删除记账", "删记录":
		p.cmdDel(ctx, b, chat, cmd.Args, msg)
	case "清账", "清空账单", "清空本月":
		p.cmdClear(ctx, b, chat, msg)
	default:
		return true, nil
	}
	return false, nil
}

// monthKey 当前月份的存储键。
func monthKey(chat string, now time.Time) string {
	return "b:" + chat + ":" + now.Format("2006-01")
}

// loadBook 读取本月账本（不存在时返回空账本）。
func (p *LedgerPlugin) loadBook(ctx context.Context, key string) book {
	var bk book
	p.mu.Lock()
	ok := p.store.Get(ctx, key, &bk)
	p.mu.Unlock()
	if !ok || bk.NextSeq <= 0 {
		bk.NextSeq = 1
	}
	return bk
}

// saveBook 保存账本。
func (p *LedgerPlugin) saveBook(ctx context.Context, key string, bk *book) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.store.Set(ctx, key, bk)
}

// cmdAdd 记一笔：/记账 <金额> <事项…>。
func (p *LedgerPlugin) cmdAdd(ctx context.Context, b bot.Bot, chat string, args []string, msg message.Message) {
	if p.store == nil {
		p.reply(b, chat, msg.Sender.UserId, "存储当前不可用，无法记账")
		return
	}
	if len(args) == 0 {
		p.reply(b, chat, msg.Sender.UserId, "用法：/记账 <金额> <事项>\n例：/记账 25.5 午饭（支出）、/记账 +3000 工资（收入）")
		return
	}
	cents, income, err := parseAmount(args[0])
	if err != nil {
		p.reply(b, chat, msg.Sender.UserId, err.Error())
		return
	}
	if p.cfg.MaxAmountYuan > 0 && abs64(cents) > int64(p.cfg.MaxAmountYuan)*100 {
		p.reply(b, chat, msg.Sender.UserId, fmt.Sprintf("单笔金额不能超过 %d 元", p.cfg.MaxAmountYuan))
		return
	}
	if len(args) < 2 {
		p.reply(b, chat, msg.Sender.UserId, "缺少事项，例如：/记账 25.5 午饭")
		return
	}
	note := truncateRunes(strings.TrimSpace(strings.Join(args[1:], " ")), p.cfg.MaxNoteLen)
	if note == "" {
		p.reply(b, chat, msg.Sender.UserId, "缺少事项，例如：/记账 25.5 午饭")
		return
	}

	key := monthKey(chat, time.Now())
	bk := p.loadBook(ctx, key)
	if p.cfg.MaxRecords > 0 && len(bk.Records) >= p.cfg.MaxRecords {
		p.reply(b, chat, msg.Sender.UserId, "本月记录数已达上限，无法再记")
		return
	}

	rec := record{
		Seq:    bk.NextSeq,
		Amount: cents,
		Note:   note,
		UserID: msg.Sender.UserId.String(),
		Name:   displayName(msg.Sender),
		Time:   time.Now().Unix(),
	}
	bk.NextSeq++
	bk.Records = append(bk.Records, rec)
	if !p.saveBook(ctx, key, &bk) {
		p.reply(b, chat, msg.Sender.UserId, "保存账目失败，请稍后再试")
		return
	}

	kind := "支出"
	if income {
		kind = "收入"
	}
	p.reply(b, chat, msg.Sender.UserId, fmt.Sprintf("📝 已记%s %s 元：%s（流水号 %d）",
		kind, formatCents(abs64(cents)), note, rec.Seq))
}

// cmdSummary 本月汇总：收入/支出/结余 + 最近几条。
func (p *LedgerPlugin) cmdSummary(ctx context.Context, b bot.Bot, chat string) {
	if p.store == nil {
		p.reply(b, chat, "", "存储当前不可用")
		return
	}
	bk := p.loadBook(ctx, monthKey(chat, time.Now()))
	income, expense, count := summarize(bk.Records)

	var sb strings.Builder
	if count == 0 {
		p.reply(b, chat, "", "本月还没有记账，用 /记账 <金额> <事项> 记第一笔吧")
		return
	}
	fmt.Fprintf(&sb, "📊 %s 账单（共 %d 笔）\n", time.Now().Format("2006年01月"), count)
	fmt.Fprintf(&sb, "收入：+%s 元\n支出：-%s 元\n结余：%s 元\n",
		formatCents(income), formatCents(expense), formatCents(income-expense))

	// 末尾附带最近几条
	n := p.cfg.SummaryRecords
	if n > len(bk.Records) {
		n = len(bk.Records)
	}
	if n > 0 {
		sb.WriteString("\n最近记录：")
		for i := len(bk.Records) - n; i < len(bk.Records); i++ {
			r := bk.Records[i]
			fmt.Fprintf(&sb, "\n%d. %s %s %s", r.Seq, time.Unix(r.Time, 0).Format("01-02"), signedAmount(r.Amount), r.Note)
		}
	}
	p.reply(b, chat, "", sb.String())
}

// cmdFlow 最近流水明细。
func (p *LedgerPlugin) cmdFlow(ctx context.Context, b bot.Bot, chat string) {
	if p.store == nil {
		p.reply(b, chat, "", "存储当前不可用")
		return
	}
	bk := p.loadBook(ctx, monthKey(chat, time.Now()))
	if len(bk.Records) == 0 {
		p.reply(b, chat, "", "本月还没有记账")
		return
	}
	n := p.cfg.FlowListSize
	if n <= 0 || n > len(bk.Records) {
		n = len(bk.Records)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📜 %s 流水（最近 %d 条）：\n", time.Now().Format("2006年01月"), n)
	for i := len(bk.Records) - n; i < len(bk.Records); i++ {
		r := bk.Records[i]
		fmt.Fprintf(&sb, "%d. %s %s %s（%s）\n",
			r.Seq, time.Unix(r.Time, 0).Format("01-02 15:04"), signedAmount(r.Amount), r.Note, r.Name)
	}
	p.reply(b, chat, "", sb.String())
}

// cmdDel 按流水号删除：仅记账本人或管理员可删。
func (p *LedgerPlugin) cmdDel(ctx context.Context, b bot.Bot, chat string, args []string, msg message.Message) {
	if p.store == nil {
		p.reply(b, chat, "", "存储当前不可用")
		return
	}
	seq, err := parseSeq(args)
	if err != nil {
		p.reply(b, chat, "", "用法：/删账 <流水号>，流水号可用 /流水 查看")
		return
	}
	key := monthKey(chat, time.Now())
	bk := p.loadBook(ctx, key)
	idx := -1
	for i := range bk.Records {
		if bk.Records[i].Seq == seq {
			idx = i
			break
		}
	}
	if idx < 0 {
		p.reply(b, chat, "", fmt.Sprintf("本月没有流水号 %d 的记录", seq))
		return
	}
	if bk.Records[idx].UserID != msg.Sender.UserId.String() && msg.Sender.UserId != p.SystemConfig.AdminId {
		p.reply(b, chat, msg.Sender.UserId, "只能删除自己记的账（管理员可删除任意记录）")
		return
	}
	removed := bk.Records[idx]
	bk.Records = append(bk.Records[:idx], bk.Records[idx+1:]...)
	if !p.saveBook(ctx, key, &bk) {
		p.reply(b, chat, "", "删除失败，请稍后再试")
		return
	}
	p.reply(b, chat, msg.Sender.UserId, fmt.Sprintf("已删除：%s %s（流水号 %d）", signedAmount(removed.Amount), removed.Note, removed.Seq))
}

// cmdClear 清空本月账本：群聊仅管理员，私聊即本人。
func (p *LedgerPlugin) cmdClear(ctx context.Context, b bot.Bot, chat string, msg message.Message) {
	if p.store == nil {
		p.reply(b, chat, "", "存储当前不可用")
		return
	}
	if strings.HasPrefix(chat, "g:") && msg.Sender.UserId != p.SystemConfig.AdminId {
		p.reply(b, chat, msg.Sender.UserId, "清空本月账本需要管理员权限")
		return
	}
	key := monthKey(chat, time.Now())
	bk := p.loadBook(ctx, key)
	count := len(bk.Records)
	if count == 0 {
		p.reply(b, chat, "", "本月还没有账目")
		return
	}
	bk.Records = nil
	if !p.saveBook(ctx, key, &bk) {
		p.reply(b, chat, "", "清账失败，请稍后再试")
		return
	}
	p.reply(b, chat, "", fmt.Sprintf("🧹 已清空本月 %d 条账目（流水号继续顺延）", count))
}

// ---------- 小工具 ----------

// reply 向会话回复；mentionUser 非空时在群聊开头 @。
func (p *LedgerPlugin) reply(b bot.Bot, chat string, mentionUser message.QID, text string) {
	if strings.HasPrefix(chat, "g:") {
		c := msgchain.Builder().Group()
		if mentionUser != "" {
			c.Mention(mentionUser)
			c.Text(" ")
		}
		c.Text(text)
		if _, ok := b.SendGroupMsg(message.FromString(chat[2:]), c.Build()); !ok {
			p.Logger.Warn("记账回复发送失败", "chat", chat)
		}
		return
	}
	c := msgchain.Builder().Friend()
	c.Text(text)
	if _, ok := b.SendFriendMsg(message.FromString(chat[2:]), c.Build()); !ok {
		p.Logger.Warn("记账回复发送失败", "chat", chat)
	}
}

// parseSeq 解析流水号参数。
func parseSeq(args []string) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("缺少流水号")
	}
	var seq int64
	if _, err := fmt.Sscanf(strings.TrimSpace(args[0]), "%d", &seq); err != nil || seq < 1 {
		return 0, fmt.Errorf("流水号非法")
	}
	return seq, nil
}

// summarize 汇总：返回（收入分, 支出分, 笔数）。
func summarize(records []record) (income, expense int64, count int) {
	for _, r := range records {
		if r.Amount >= 0 {
			income += r.Amount
		} else {
			expense += -r.Amount
		}
	}
	return income, expense, len(records)
}

// signedAmount 带符号金额展示：收入 +xx.xx，支出 -xx.xx。
func signedAmount(cents int64) string {
	if cents >= 0 {
		return "+" + formatCents(cents)
	}
	return formatCents(cents)
}

// displayName 取群名片，空则回退昵称。
func displayName(sender message.MessageSender) string {
	if sender.Card != "" {
		return sender.Card
	}
	return sender.Nickname
}

// abs64 绝对值。
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// truncateRunes 按字符数截断，超长补省略号。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
