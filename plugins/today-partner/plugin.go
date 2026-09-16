// Package todaypartner 今日对象插件：QQ 群娱乐玩法。
//
//   - /今日老婆、/今日老公、/今日对象：从群成员里随机抽一个今日对象，
//     每日抽取次数可配置（1-5）；次数大于 1 时，后面的抽取会带上之前抽到的一起展示；
//     双向配对：若今天已有人先抽到自己，则直接分配最早抽到自己的那位，不做随机。
//   - /强娶 @群成员：直接指定今日对象，有时间冷却，冷却时长可配置。
//
// 候选成员经 bot.QQ 的 GetGroupMemberList（NapCat get_group_member_list）获取全量群成员。
// 抽取与冷却状态写入持久化存储（命名空间 today-partner），重启不丢，跨天自动重置。
package todaypartner

import (
	"context"
	"fmt"
	"sort"
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

// todayPartnerConfig 插件配置。
type todayPartnerConfig struct {
	Enable               bool `cfg:"plugin.today-partner.enable" label:"启用今日对象" group:"今日对象" default:"true" help:"关闭后不响应任何命令"`
	DailyDraws           int  `cfg:"plugin.today-partner.daily_draws" label:"每日抽取次数" group:"今日对象" default:"1" help:"每人每天可抽取的次数（1-5），大于 1 时后面的抽取会带上之前抽到的一起展示"`
	MarryCooldownMinutes int  `cfg:"plugin.today-partner.marry_cooldown_minutes" label:"强娶冷却(分钟)" group:"今日对象" default:"60" help:"同一人两次强娶的最短间隔，0 为不限制"`
}

// marryState 强娶冷却记录。
type marryState struct {
	Last int64 `json:"last"` // 上次强娶的 Unix 秒级时间戳
}

// TodayPartnerPlugin 插件定义。
type TodayPartnerPlugin struct {
	plugin.Meta
	cfg   todayPartnerConfig
	store storage.PersistentStorage // Clone("today-partner") 后的命名空间
	mu    sync.Mutex                // 保护抽取/冷却状态的读改写
}

// NewPlugin 构造函数。
func NewPlugin() *TodayPartnerPlugin {
	p := &TodayPartnerPlugin{}
	p.Name = "今日对象"
	p.HelpWords = "群内 @我 /今日对象、/今日老婆、/今日老公 从群成员里抽今日对象（每日次数可配），/强娶 @群成员 直接指定（有冷却）"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup
	p.Author = "jeanhua"
	p.Version = "1.2.0"
	p.Order = plugin.LevelNormal
	p.Platforms = []string{"qq"}
	return p
}

// ConfigSchema 声明配置结构体。
func (p *TodayPartnerPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化。
func (p *TodayPartnerPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("today-partner")
	}
	p.Logger.Info("今日对象插件初始化",
		"enable", p.cfg.Enable,
		"daily_draws", clampDraws(p.cfg.DailyDraws),
		"marry_cooldown_minutes", p.cfg.MarryCooldownMinutes,
	)
	return nil
}

// OnGroupMsg 群聊消息事件（本插件只支持群聊，私聊不响应）。
func (p *TodayPartnerPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	switch cmd.Name {
	case "今日老婆", "今日老公", "今日对象":
		p.cmdDraw(ctx, b, cmd.Name, msg)
	case "强娶":
		p.cmdMarry(ctx, b, msg)
	default:
		return true, nil
	}
	return false, nil
}

// ---------- /今日老婆 | /今日老公 | /今日对象 ----------

// cmdDraw 随机抽取今日对象。
func (p *TodayPartnerPlugin) cmdDraw(ctx context.Context, b bot.Bot, cmdName string, msg message.Message) {
	if p.store == nil {
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "存储当前不可用，无法抽取")
		return
	}
	kind := kindOf(cmdName)
	maxDraws := clampDraws(p.cfg.DailyDraws)
	today := time.Now().Format("2006-01-02")
	key := drawKey(msg.GroupId, msg.Sender.UserId)

	p.mu.Lock()
	defer p.mu.Unlock()

	var st drawState
	p.store.Get(ctx, key, &st)
	st.ensureToday(today)

	// 次数用完：不再抽取，把今日对象再展示一遍
	if st.remaining(maxDraws) == 0 {
		p.sendDrawResult(b, msg.GroupId, kind, st.Partners, 0, "今天的抽取次数已经用完啦，明天再来吧～")
		return
	}

	drawn := st.drawnSet()

	// 双向配对：今天若已有人先抽到自己，直接把那位"有缘人"分配给自己，不做随机
	if pt, ok := p.findMutualPartner(ctx, b, msg.GroupId, msg.Sender.UserId, today, drawn); ok {
		st.Used++
		st.Partners = append(st.Partners, pt)
		if !p.store.Set(ctx, key, &st) {
			p.replyText(b, msg.GroupId, msg.Sender.UserId, "保存抽取数据失败，请稍后再试")
			return
		}
		p.sendDrawResult(b, msg.GroupId, kind, st.Partners, st.remaining(maxDraws),
			fmt.Sprintf("💕【%s】先抽到了你，月老把你们绑在一起啦～", pt.Name))
		return
	}

	candidates := p.collectCandidates(b, msg, drawn)
	if len(candidates) == 0 {
		if len(drawn) > 0 {
			p.replyText(b, msg.GroupId, msg.Sender.UserId, "群里暂时没有新的合适人选啦，明天再来抽吧～")
		} else {
			p.replyText(b, msg.GroupId, msg.Sender.UserId, "还没抽到人，先让大家在群里冒个泡吧～")
		}
		return
	}

	picked, ok := pickCandidate(candidates)
	if !ok {
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "还没抽到人，先让大家在群里冒个泡吧～")
		return
	}

	picked.At = time.Now().Unix()
	st.Used++
	st.Partners = append(st.Partners, picked)
	if !p.store.Set(ctx, key, &st) {
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "保存抽取数据失败，请稍后再试")
		return
	}
	p.sendDrawResult(b, msg.GroupId, kind, st.Partners, st.remaining(maxDraws), "")
}

// collectCandidates 获取可抽取的群成员列表。
func (p *TodayPartnerPlugin) collectCandidates(b bot.Bot, msg message.Message, drawn map[string]bool) []partner {
	qb, ok := b.(bot.QQ)
	if !ok {
		return nil
	}
	members, ok := qb.GetGroupMemberList(msg.GroupId, false)
	if !ok || members == nil {
		return nil
	}
	return filterCandidates(*members, msg.SelfId, msg.Sender.UserId, drawn)
}

// findMutualPartner 遍历群内所有人的抽取状态，找最早抽到 sender 的人（见 pickMutual），
// 并查出其展示名。只统计今天的状态，跨天旧状态不参与配对。
func (p *TodayPartnerPlugin) findMutualPartner(ctx context.Context, b bot.Bot, group, sender message.QID, today string, drawn map[string]bool) (partner, bool) {
	keys, err := p.store.Keys(ctx, drawKeyPrefix(group))
	if err != nil || len(keys) == 0 {
		return partner{}, false
	}
	sort.Strings(keys)
	states := make([]memberState, 0, len(keys))
	for _, k := range keys {
		var s drawState
		if !p.store.Get(ctx, k, &s) || s.Date != today {
			continue
		}
		states = append(states, memberState{Owner: ownerOfDrawKey(k), State: s})
	}
	qq, at, ok := pickMutual(sender.TrimQQPrefix(), states, drawn)
	if !ok {
		return partner{}, false
	}
	return partner{QQ: qq, Name: p.resolveName(b, group, message.FromString(qq)), At: at}, true
}

// sendDrawResult 发送抽取结果：历次对象一起展示，每人一段昵称 + 头像。
func (p *TodayPartnerPlugin) sendDrawResult(b bot.Bot, group message.QID, kind partnerKind, partners []partner, remaining int, prefix string) {
	c := msgchain.Builder().Group()
	if prefix != "" {
		c.Text(prefix + "\n")
	}
	c.Text(fmt.Sprintf("你的%s是：\n", kind.Label))
	for _, pt := range partners {
		c.Text(fmt.Sprintf("【%s】\n", pt.Name))
		c.ImageUrl(avatarURL(pt.QQ))
	}
	c.Text(fmt.Sprintf("请好好对待%s哦💗~\n剩余抽取次数: %d次", kind.Pronoun, remaining))
	if _, ok := b.SendGroupMsg(group, c.Build()); !ok {
		p.Logger.Warn("今日对象抽取结果发送失败", "group", group)
	}
}

// ---------- /强娶 @群成员 ----------

// cmdMarry 强娶指定群友。
func (p *TodayPartnerPlugin) cmdMarry(ctx context.Context, b bot.Bot, msg message.Message) {
	if p.store == nil {
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "存储当前不可用，无法强娶")
		return
	}
	target, status := marryTarget(msg.Message, msg.SelfId, msg.Sender.UserId)
	switch status {
	case 0:
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "用法：@我 /强娶 @群成员")
		return
	case 1:
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "哼，机器人可不能被强娶哦～")
		return
	}

	key := marryKey(msg.GroupId, msg.Sender.UserId)
	now := time.Now().Unix()

	p.mu.Lock()
	defer p.mu.Unlock()

	var mc marryState
	p.store.Get(ctx, key, &mc)
	if wait := marryCooldownSec(mc.Last, now, p.cfg.MarryCooldownMinutes); wait > 0 {
		p.replyText(b, msg.GroupId, msg.Sender.UserId,
			fmt.Sprintf("刚刚才强娶过哦，心急吃不了热豆腐～大约 %s 后再来吧", humanWait(wait)))
		return
	}

	name := p.resolveName(b, msg.GroupId, target)
	mc.Last = now
	if !p.store.Set(ctx, key, &mc) {
		p.replyText(b, msg.GroupId, msg.Sender.UserId, "保存强娶数据失败，请稍后再试")
		return
	}

	c := msgchain.Builder().Group()
	c.Text(fmt.Sprintf("你今天强娶了【%s】哦❤️~\n请对她好一点哦~。\n", name))
	c.ImageUrl(avatarURL(target.TrimQQPrefix()))
	if _, ok := b.SendGroupMsg(msg.GroupId, c.Build()); !ok {
		p.Logger.Warn("强娶结果发送失败", "group", msg.GroupId)
	}
}

// resolveName 查询目标群友的展示名：群名片优先，回退昵称，再回退 QQ 号。
func (p *TodayPartnerPlugin) resolveName(b bot.Bot, group, user message.QID) string {
	if qb, ok := b.(bot.QQ); ok {
		if info, ok := qb.GetGroupUserInfo(group, user); ok && info != nil {
			if info.Card != "" {
				return info.Card
			}
			if info.Nickname != "" {
				return info.Nickname
			}
		}
	}
	return "QQ" + user.TrimQQPrefix()
}

// ---------- 小工具 ----------

// replyText 短文本回复：群聊开头 @ 用户，失败记日志。
func (p *TodayPartnerPlugin) replyText(b bot.Bot, group, user message.QID, text string) {
	c := msgchain.Builder().Group()
	c.Mention(user)
	c.Text(" ")
	c.Text(text)
	if _, ok := b.SendGroupMsg(group, c.Build()); !ok {
		p.Logger.Warn("今日对象回复发送失败", "group", group)
	}
}

// drawKeyPrefix 某个群全部抽取状态的存储键前缀（枚举群内状态用）。
func drawKeyPrefix(group message.QID) string {
	return "d:" + group.TrimQQPrefix() + ":"
}

// drawKey 抽取状态的存储键（群 + 用户维度，当日状态覆盖写，跨天自动重置）。
func drawKey(group, user message.QID) string {
	return drawKeyPrefix(group) + user.TrimQQPrefix()
}

// marryKey 强娶冷却的存储键。
func marryKey(group, user message.QID) string {
	return "m:" + group.TrimQQPrefix() + ":" + user.TrimQQPrefix()
}

// humanWait 把剩余秒数格式化成可读的中文时长。
func humanWait(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	case sec < 3600:
		return fmt.Sprintf("%d 分钟", (sec+59)/60)
	default:
		return fmt.Sprintf("%d 小时", (sec+3599)/3600)
	}
}
