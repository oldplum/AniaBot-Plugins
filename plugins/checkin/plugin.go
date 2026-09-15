// Package checkin 签到打卡插件：每日签到赚积分。
//
//   - /签到：随机积分 + 今日运势，连续签到有额外加成；
//   - /积分：查询自己的积分、连签天数与当前排名；
//   - /积分排行：全服积分排行榜。
//
// 数据写入持久化存储（命名空间 checkin），重启不丢；积分全局按用户累计，跨群共享。
package checkin

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
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

const userPrefix = "u:" // 用户记录的存储键前缀

// checkinConfig 插件配置。
type checkinConfig struct {
	Enable      bool `cfg:"plugin.checkin.enable" label:"启用签到" group:"签到打卡" default:"true" help:"关闭后不响应任何签到命令"`
	MinPoints   int  `cfg:"plugin.checkin.min_points" label:"单次最少积分" group:"签到打卡" default:"10" help:"签到随机积分的下限"`
	MaxPoints   int  `cfg:"plugin.checkin.max_points" label:"单次最多积分" group:"签到打卡" default:"50" help:"签到随机积分的上限"`
	StreakBonus int  `cfg:"plugin.checkin.streak_bonus" label:"连签奖励系数" group:"签到打卡" default:"5" help:"连续签到每满 3 天额外 +该值 积分"`
	BonusCap    int  `cfg:"plugin.checkin.bonus_cap" label:"连签奖励上限" group:"签到打卡" default:"30" help:"连签额外奖励的积分上限"`
	TopListSize int  `cfg:"plugin.checkin.top_list_size" label:"排行榜人数" group:"签到打卡" default:"10" help:"/积分排行 展示的人数"`
}

// userRecord 一个用户的签到数据。
type userRecord struct {
	Points    int    `json:"points"`     // 当前积分
	Streak    int    `json:"streak"`     // 连续签到天数
	LastDate  string `json:"last_date"`  // 最近一次签到日期（2006-01-02）
	TotalDays int    `json:"total_days"` // 累计签到天数
	Name      string `json:"name"`       // 最近一次签到时的展示名
}

// CheckinPlugin 插件定义。
type CheckinPlugin struct {
	plugin.Meta
	cfg   checkinConfig
	store storage.PersistentStorage // Clone("checkin") 后的命名空间
}

// NewPlugin 构造函数。
func NewPlugin() *CheckinPlugin {
	p := &CheckinPlugin{}
	p.Name = "签到打卡"
	p.HelpWords = "@我 /签到 每日签到赚积分（连续签到有加成），/积分 查询自己的积分与排名，/积分排行 看排行榜"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 声明配置结构体。
func (p *CheckinPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化。
func (p *CheckinPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("checkin")
	}
	p.Logger.Info("签到打卡插件初始化",
		"enable", p.cfg.Enable,
		"points_range", fmt.Sprintf("%d~%d", p.cfg.MinPoints, p.cfg.MaxPoints),
		"streak_bonus", p.cfg.StreakBonus,
	)
	return nil
}

// OnGroupMsg 群聊消息事件。
func (p *CheckinPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	return p.handle(ctx, b, true, cmd, msg)
}

// OnFriendMsg 私聊消息事件（无需 @）。
func (p *CheckinPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	return p.handle(ctx, b, false, cmd, msg)
}

// handle 命令分发。
func (p *CheckinPlugin) handle(ctx context.Context, b bot.Bot, inGroup bool, cmd command.Command, msg message.Message) (bool, error) {
	// chat：群聊为群 ID，私聊为用户 ID，回复时统一发往这里
	chat := msg.Sender.UserId
	if inGroup {
		chat = msg.GroupId
	}
	switch strings.ToLower(cmd.Name) {
	case "签到", "打卡", "checkin":
		p.cmdCheckin(ctx, b, inGroup, chat, msg)
	case "积分", "我的积分":
		p.cmdPoints(ctx, b, inGroup, chat, msg)
	case "积分排行", "签到排行", "积分榜", "签到榜":
		p.cmdRank(ctx, b, inGroup, chat, msg)
	default:
		return true, nil
	}
	return false, nil
}

// cmdCheckin /签到：执行签到。
func (p *CheckinPlugin) cmdCheckin(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, msg message.Message) {
	if p.store == nil {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "存储当前不可用，无法签到")
		return
	}
	qid := msg.Sender.UserId.String()
	today := time.Now().Format("2006-01-02")
	rec := p.loadUser(ctx, qid)
	if rec.LastDate == today {
		p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf(
			"今天已经签到过啦～当前 %d 积分（连签 %d 天），明天再来吧！", rec.Points, rec.Streak))
		return
	}

	lo, hi := p.cfg.MinPoints, p.cfg.MaxPoints
	if lo < 0 {
		lo = 10
	}
	if hi < lo {
		hi = lo
	}
	base := lo + rand.Intn(hi-lo+1)

	// 连签判定：上次签到是昨天则连签 +1，否则重新计数
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if rec.LastDate == yesterday {
		rec.Streak++
	} else {
		rec.Streak = 1
	}
	rec.TotalDays++
	rec.LastDate = today
	rec.Name = displayName(msg.Sender)

	bonus := 0
	if p.cfg.StreakBonus > 0 && rec.Streak >= 3 {
		bonus = rec.Streak / 3 * p.cfg.StreakBonus
		if p.cfg.BonusCap > 0 && bonus > p.cfg.BonusCap {
			bonus = p.cfg.BonusCap
		}
	}
	rec.Points += base + bonus

	if !p.store.Set(ctx, userPrefix+qid, rec) {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "保存签到数据失败，请稍后再试")
		return
	}

	fortune, flavor := fortuneText(base, lo, hi)
	text := fmt.Sprintf("📅 签到成功！获得 %d 积分", base+bonus)
	if bonus > 0 {
		text += fmt.Sprintf("（基础 %d + 连签奖励 %d）", base, bonus)
	}
	text += fmt.Sprintf("\n🔥 连续签到 %d 天（%s），累计 %d 天", rec.Streak, streakTitle(rec.Streak), rec.TotalDays)
	text += fmt.Sprintf("\n🍀 今日运势：%s｜%s", fortune, flavor)
	p.reply(b, inGroup, chat, msg.Sender.UserId, text)
}

// cmdPoints /积分：查询自己的积分与排名。
func (p *CheckinPlugin) cmdPoints(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, msg message.Message) {
	if p.store == nil {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "存储当前不可用")
		return
	}
	rec := p.loadUser(ctx, msg.Sender.UserId.String())
	if rec.TotalDays == 0 {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你还没有签到记录，发送 /签到 开启签到之旅吧！")
		return
	}
	rank, _ := p.rankOf(ctx, rec.Points)
	p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf(
		"💰 当前积分：%d\n🔥 连签 %d 天｜累计签到 %d 天\n📈 当前排名：第 %d 名",
		rec.Points, rec.Streak, rec.TotalDays, rank))
}

// cmdRank /积分排行：积分排行榜。
func (p *CheckinPlugin) cmdRank(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, msg message.Message) {
	if p.store == nil {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "存储当前不可用")
		return
	}
	records, err := p.allUsers(ctx)
	if err != nil {
		p.Logger.Warn("读取签到排行榜失败", "err", err)
		p.reply(b, inGroup, chat, msg.Sender.UserId, "读取排行榜失败，请稍后再试")
		return
	}
	if len(records) == 0 {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "还没有人签到，快来抢占第一名！")
		return
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Points != records[j].Points {
			return records[i].Points > records[j].Points
		}
		return records[i].Streak > records[j].Streak
	})
	top := p.cfg.TopListSize
	if top <= 0 || top > len(records) {
		top = len(records)
	}
	records = records[:top]

	var sb strings.Builder
	sb.WriteString("🏆 积分排行榜：\n")
	medals := []string{"🥇", "🥈", "🥉"}
	for i, r := range records {
		name := r.Name
		if name == "" {
			name = "神秘旅人"
		}
		prefix := fmt.Sprintf("%d. ", i+1)
		if i < len(medals) {
			prefix = medals[i] + " "
		}
		fmt.Fprintf(&sb, "%s%s — %d 分（连签 %d 天）\n", prefix, name, r.Points, r.Streak)
	}
	p.reply(b, inGroup, chat, msg.Sender.UserId, strings.TrimRight(sb.String(), "\n"))
}

// ---------- 存取与工具 ----------

// displayName 取展示名：群名片优先，为空回退昵称。
func displayName(sender message.MessageSender) string {
	if sender.Card != "" {
		return sender.Card
	}
	return sender.Nickname
}

// loadUser 读取用户记录（不存在时返回零值记录）。
func (p *CheckinPlugin) loadUser(ctx context.Context, qid string) userRecord {
	var rec userRecord
	if p.store != nil {
		p.store.Get(ctx, userPrefix+qid, &rec)
	}
	return rec
}

// rankOf 计算积分为 points 的用户的全服排名（同分按名次并列靠前计）。
func (p *CheckinPlugin) rankOf(ctx context.Context, points int) (int, error) {
	records, err := p.allUsers(ctx)
	if err != nil {
		return 0, err
	}
	rank := 1
	for _, r := range records {
		if r.Points > points {
			rank++
		}
	}
	return rank, nil
}

// allUsers 读取全部用户记录。
func (p *CheckinPlugin) allUsers(ctx context.Context) ([]userRecord, error) {
	if p.store == nil {
		return nil, nil
	}
	keys, err := p.store.Keys(ctx, userPrefix)
	if err != nil {
		return nil, err
	}
	records := make([]userRecord, 0, len(keys))
	for _, k := range keys {
		var rec userRecord
		if p.store.Get(ctx, k, &rec) && rec.TotalDays > 0 {
			records = append(records, rec)
		}
	}
	return records, nil
}

// reply 向会话回复：群聊 chat 为群 ID 且开头 @ 用户，私聊 chat 为用户 ID。
func (p *CheckinPlugin) reply(b bot.Bot, inGroup bool, chat message.QID, user message.QID, text string) {
	if inGroup {
		c := msgchain.Builder().Group()
		c.Mention(user)
		c.Text(" ")
		c.Text(text)
		if _, ok := b.SendGroupMsg(chat, c.Build()); !ok {
			p.Logger.Warn("签到回复发送失败", "group", chat)
		}
		return
	}
	c := msgchain.Builder().Friend()
	c.Text(text)
	if _, ok := b.SendFriendMsg(chat, c.Build()); !ok {
		p.Logger.Warn("签到回复发送失败", "user", chat)
	}
}
