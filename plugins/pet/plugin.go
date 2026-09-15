// Package pet 电子宠物插件：领养专属宠物并照顾它长大。
//
//   - /领养 <名字>：随机领养一只（猫/狗/兔/仓鼠/狐狸…）；
//   - /宠物：查看状态卡（饱食度、心情、成长值）；
//   - /喂食、/逗宠：互动（有冷却），互动涨成长值，满了就升级；
//   - /改名、/告别：改名 / 送走宠物。
//
// 饱食度与心情随时间自动衰减（按结算时间差计算，无需后台任务），数据持久化重启不丢。
package pet

import (
	"context"
	"fmt"
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

const petPrefix = "p:" // 宠物存档的存储键前缀

// petConfig 插件配置。
type petConfig struct {
	Enable            bool    `cfg:"plugin.pet.enable" label:"启用电子宠物" group:"电子宠物" default:"true" help:"关闭后不响应任何宠物命令"`
	FeedHunger        int     `cfg:"plugin.pet.feed_hunger" label:"喂食恢复饱食度" group:"电子宠物" default:"25"`
	FeedHappy         int     `cfg:"plugin.pet.feed_happy" label:"喂食增加心情" group:"电子宠物" default:"3"`
	FeedExp           int     `cfg:"plugin.pet.feed_exp" label:"喂食成长值" group:"电子宠物" default:"6"`
	PlayHappy         int     `cfg:"plugin.pet.play_happy" label:"逗宠增加心情" group:"电子宠物" default:"20"`
	PlayHunger        int     `cfg:"plugin.pet.play_hunger" label:"逗宠消耗饱食度" group:"电子宠物" default:"3"`
	PlayExp           int     `cfg:"plugin.pet.play_exp" label:"逗宠成长值" group:"电子宠物" default:"8"`
	HungerDecayPerH   float64 `cfg:"plugin.pet.hunger_decay_per_hour" label:"饱食度衰减/小时" group:"电子宠物" default:"4" help:"饱食度每小时自然下降的数值"`
	HappyDecayPerH    float64 `cfg:"plugin.pet.happy_decay_per_hour" label:"心情衰减/小时" group:"电子宠物" default:"3" help:"心情每小时自然下降的数值"`
	CooldownMinutes   int     `cfg:"plugin.pet.cooldown_minutes" label:"互动冷却(分钟)" group:"电子宠物" default:"10" help:"喂食/逗宠各自的最短间隔，0 为不限制"`
	MaxNameLen        int     `cfg:"plugin.pet.max_name_length" label:"名字长度上限" group:"电子宠物" default:"12"`
}

// PetPlugin 插件定义。
type PetPlugin struct {
	plugin.Meta
	cfg   petConfig
	store storage.PersistentStorage // Clone("pet") 后的命名空间
	mu    sync.Mutex                // 保护存档读改写
}

// NewPlugin 构造函数。
func NewPlugin() *PetPlugin {
	p := &PetPlugin{}
	p.Name = "电子宠物"
	p.HelpWords = "@我 /领养 <名字> 领养一只电子宠物，/宠物 看状态，/喂食 和 /逗宠 照顾它，/改名 <新名字>、/告别 确认 送走它"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 声明配置结构体。
func (p *PetPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化。
func (p *PetPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("pet")
	}
	p.Logger.Info("电子宠物插件初始化",
		"enable", p.cfg.Enable,
		"cooldown_minutes", p.cfg.CooldownMinutes,
	)
	return nil
}

// OnGroupMsg 群聊消息事件。
func (p *PetPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	return p.handle(ctx, b, true, msg.GroupId, cmd, msg)
}

// OnFriendMsg 私聊消息事件（无需 @）。
func (p *PetPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable {
		return true, nil
	}
	return p.handle(ctx, b, false, msg.Sender.UserId, cmd, msg)
}

// handle 命令分发。
func (p *PetPlugin) handle(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, cmd command.Command, msg message.Message) (bool, error) {
	switch strings.ToLower(cmd.Name) {
	case "领养", "adopt":
		p.cmdAdopt(ctx, b, inGroup, chat, cmd.Args, msg)
	case "宠物", "我的宠物", "pet":
		p.cmdStatus(ctx, b, inGroup, chat, msg)
	case "喂食", "喂":
		p.cmdFeed(ctx, b, inGroup, chat, msg)
	case "逗宠", "玩耍", "玩":
		p.cmdPlay(ctx, b, inGroup, chat, msg)
	case "改名":
		p.cmdRename(ctx, b, inGroup, chat, cmd.Args, msg)
	case "告别", "送走":
		p.cmdGoodbye(ctx, b, inGroup, chat, cmd.Args, msg)
	default:
		return true, nil
	}
	return false, nil
}

// loadPet 读取宠物存档并结算衰减。
func (p *PetPlugin) loadPet(ctx context.Context, qid string) (petState, bool) {
	var pc petState
	if p.store == nil {
		return pc, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.store.Get(ctx, petPrefix+qid, &pc) {
		return pc, false
	}
	applyDecay(&pc, time.Now().Unix(), p.cfg.HungerDecayPerH, p.cfg.HappyDecayPerH)
	return pc, true
}

// savePet 保存宠物存档。
func (p *PetPlugin) savePet(ctx context.Context, qid string, pc *petState) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.store.Set(ctx, petPrefix+qid, pc)
}

// cmdAdopt /领养 <名字>。
func (p *PetPlugin) cmdAdopt(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, args []string, msg message.Message) {
	if p.store == nil {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "存储当前不可用，无法领养")
		return
	}
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "给它取个名字吧：/领养 <名字>，例如 /领养 糯米")
		return
	}
	name = truncateRunes(name, p.maxNameLen())

	if _, ok := p.loadPet(ctx, msg.Sender.UserId.String()); ok {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你已经有宠物啦，一心不能二用哦～（/宠物 查看，/告别 确认 送走它）")
		return
	}
	emoji, species := randomSpecies()
	pc := petState{
		Name:      name,
		Emoji:     emoji,
		Species:   species,
		Level:     1,
		Hunger:    70,
		Happy:     70,
		UpdatedAt: time.Now().Unix(),
	}
	if !p.savePet(ctx, msg.Sender.UserId.String(), &pc) {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "保存宠物数据失败，请稍后再试")
		return
	}
	p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf(
		"🎉 领养成功！%s%s「%s」来到你身边啦～\n发送 /宠物 查看状态，用 /喂食 和 /逗宠 照顾它吧！",
		emoji, species, name))
}

// cmdStatus /宠物：查看状态。
func (p *PetPlugin) cmdStatus(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, msg message.Message) {
	pc, ok := p.loadPet(ctx, msg.Sender.UserId.String())
	if !ok {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你还没有宠物，@我 /领养 <名字> 领一只吧！")
		return
	}
	p.reply(b, inGroup, chat, msg.Sender.UserId, statusText(&pc, p.cfg.HungerDecayPerH, p.cfg.HappyDecayPerH, time.Now().Unix()))
}

// cmdFeed /喂食。
func (p *PetPlugin) cmdFeed(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, msg message.Message) {
	pc, ok := p.loadPet(ctx, msg.Sender.UserId.String())
	if !ok {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你还没有宠物，@我 /领养 <名字> 领一只吧！")
		return
	}
	if cd, wait := p.cooldownWait(pc.LastFeed); wait > 0 {
		p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf("%s%s 才刚吃过，还不饿～（约 %d 分钟后再喂）", pc.Emoji, pc.Name, cd))
		return
	}
	add, refuse := feedResult(&pc, p.cfg.FeedHunger)
	if refuse != "" {
		p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf("%s%s %s", pc.Emoji, pc.Name, refuse))
		return
	}
	now := time.Now().Unix()
	pc.Hunger += add
	pc.Happy = clamp(pc.Happy + p.cfg.FeedHappy)
	pc.LastFeed = now
	pc.FedCount++
	text := fmt.Sprintf("%s%s 大口大口吃得好香～饱食度 +%d（%d%%）", pc.Emoji, pc.Name, add, pc.Hunger)
	ups := addExp(&pc, p.cfg.FeedExp)
	text += levelUpText(ups)
	pc.UpdatedAt = now
	p.savePet(ctx, msg.Sender.UserId.String(), &pc)
	p.reply(b, inGroup, chat, msg.Sender.UserId, text)
}

// cmdPlay /逗宠。
func (p *PetPlugin) cmdPlay(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, msg message.Message) {
	pc, ok := p.loadPet(ctx, msg.Sender.UserId.String())
	if !ok {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你还没有宠物，@我 /领养 <名字> 领一只吧！")
		return
	}
	if cd, wait := p.cooldownWait(pc.LastPlay); wait > 0 {
		p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf("%s%s 玩累了正在休息～（约 %d 分钟后再来逗它）", pc.Emoji, pc.Name, cd))
		return
	}
	now := time.Now().Unix()
	pc.Happy = clamp(pc.Happy + p.cfg.PlayHappy)
	pc.Hunger = clamp(pc.Hunger - p.cfg.PlayHunger)
	pc.LastPlay = now
	pc.PlayedCount++
	text := fmt.Sprintf("%s%s 撒欢地玩了半天，心情 +%d（%d%%）", pc.Emoji, pc.Name, p.cfg.PlayHappy, pc.Happy)
	if event := maybePlayEvent(); event != "" {
		pc.Happy = clamp(pc.Happy + 5)
		text += "\n✨ " + event + "（心情额外 +5）"
	}
	ups := addExp(&pc, p.cfg.PlayExp)
	text += levelUpText(ups)
	pc.UpdatedAt = now
	p.savePet(ctx, msg.Sender.UserId.String(), &pc)
	p.reply(b, inGroup, chat, msg.Sender.UserId, text)
}

// cmdRename /改名 <新名字>。
func (p *PetPlugin) cmdRename(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, args []string, msg message.Message) {
	pc, ok := p.loadPet(ctx, msg.Sender.UserId.String())
	if !ok {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你还没有宠物，@我 /领养 <名字> 领一只吧！")
		return
	}
	newName := strings.TrimSpace(strings.Join(args, " "))
	if newName == "" {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "用法：/改名 <新名字>")
		return
	}
	newName = truncateRunes(newName, p.maxNameLen())
	old := pc.Name
	pc.Name = newName
	if !p.savePet(ctx, msg.Sender.UserId.String(), &pc) {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "改名失败，请稍后再试")
		return
	}
	p.reply(b, inGroup, chat, msg.Sender.UserId, fmt.Sprintf("「%s」改名为「%s」啦～", old, newName))
}

// cmdGoodbye /告别 确认：送走宠物。
func (p *PetPlugin) cmdGoodbye(ctx context.Context, b bot.Bot, inGroup bool, chat message.QID, args []string, msg message.Message) {
	pc, ok := p.loadPet(ctx, msg.Sender.UserId.String())
	if !ok {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "你还没有宠物，无需告别～")
		return
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) != "确认" {
		p.reply(b, inGroup, chat, msg.Sender.UserId,
			fmt.Sprintf("要和 %s%s「%s」说再见吗？确定的话发送 /告别 确认", pc.Emoji, pc.Species, pc.Name))
		return
	}
	p.mu.Lock()
	okDel := p.store.Del(ctx, petPrefix+msg.Sender.UserId.String())
	p.mu.Unlock()
	if !okDel {
		p.reply(b, inGroup, chat, msg.Sender.UserId, "操作失败，请稍后再试")
		return
	}
	p.reply(b, inGroup, chat, msg.Sender.UserId,
		fmt.Sprintf("%s%s「%s」挥挥小爪子走了，祝它遇到更好的主人…（随时可以重新 /领养）", pc.Emoji, pc.Species, pc.Name))
}

// ---------- 小工具 ----------

// cooldownWait 互动冷却：返回（还需等待的分钟数, 剩余秒数）；冷却关闭时恒为 0。
func (p *PetPlugin) cooldownWait(last int64) (int, int64) {
	if p.cfg.CooldownMinutes <= 0 || last <= 0 {
		return 0, 0
	}
	waitSec := int64(p.cfg.CooldownMinutes)*60 - (time.Now().Unix() - last)
	if waitSec <= 0 {
		return 0, 0
	}
	mins := int(waitSec/60) + 1
	return mins, waitSec
}

// maxNameLen 名字长度上限（含防御性默认值）。
func (p *PetPlugin) maxNameLen() int {
	if p.cfg.MaxNameLen <= 0 {
		return 12
	}
	return p.cfg.MaxNameLen
}

// levelUpText 升级提示文案（无升级返回空串）。
func levelUpText(ups []int) string {
	if len(ups) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ups))
	for _, lv := range ups {
		parts = append(parts, "Lv."+strconv.Itoa(lv))
	}
	return fmt.Sprintf("\n🎊 恭喜！升级到 %s！", strings.Join(parts, " → "))
}

// reply 向会话回复：群聊 chat 为群 ID 且开头 @ 用户，私聊 chat 为用户 ID。
func (p *PetPlugin) reply(b bot.Bot, inGroup bool, chat message.QID, user message.QID, text string) {
	if inGroup {
		c := msgchain.Builder().Group()
		c.Mention(user)
		c.Text(" ")
		c.Text(text)
		if _, ok := b.SendGroupMsg(chat, c.Build()); !ok {
			p.Logger.Warn("宠物回复发送失败", "group", chat)
		}
		return
	}
	c := msgchain.Builder().Friend()
	c.Text(text)
	if _, ok := b.SendFriendMsg(chat, c.Build()); !ok {
		p.Logger.Warn("宠物回复发送失败", "user", chat)
	}
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
