// Package games 群小游戏插件：群聊多人互动游戏合集。
//
//   - 猜数字：全群轮流猜机器人想好的 1~N 的整数，每次猜测自动缩小范围；
//   - 24 点：出题保证有解，成员用四则运算抢答，表达式精确验算（分数运算），
//     胜场持久化可查排行榜；
//   - 选一个：随机帮群友做选择。
package games

import (
	"context"
	"fmt"
	"math/rand"
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
	guessDefaultMax = 100 // 猜数字默认范围上限
	game24Target    = 24  // 24 点目标值
	topListSize     = 5   // 排行榜展示人数
	maxPickOptions  = 20  // 随机选择的最大选项数
	maxPickOptLen   = 30  // 单个选项最大字符数
)

// gamesConfig 插件配置。
type gamesConfig struct {
	Enable               bool `cfg:"plugin.games.enable" label:"启用小游戏" group:"群小游戏" default:"true" help:"总开关，关闭后不响应任何游戏命令"`
	EnableGuess          bool `cfg:"plugin.games.enable_guess" label:"启用猜数字" group:"群小游戏" default:"true"`
	Enable24             bool `cfg:"plugin.games.enable_24" label:"启用24点" group:"群小游戏" default:"true"`
	EnablePick           bool `cfg:"plugin.games.enable_pick" label:"启用随机选择" group:"群小游戏" default:"true"`
	GuessMax             int  `cfg:"plugin.games.guess_max" label:"猜数字范围上限" group:"群小游戏" default:"100" help:"目标数字范围 1~该值"`
	GuessTimeoutMinutes  int  `cfg:"plugin.games.guess_timeout_minutes" label:"猜数字超时(分钟)" group:"群小游戏" default:"10" help:"超时后该局自动作废"`
	Game24TimeoutMinutes int  `cfg:"plugin.games.game24_timeout_minutes" label:"24点超时(分钟)" group:"群小游戏" default:"15" help:"超时后该局自动作废"`
	NumberMax24          int  `cfg:"plugin.games.number_max_24" label:"24点数字范围" group:"群小游戏" default:"13" help:"出题数字取 1~该值（13 即扑克牌 A~K）"`
}

// guessGame 猜数字对局状态。
type guessGame struct {
	Target      int    `json:"target"`
	Lo          int    `json:"lo"` // 当前仍可能的最小值
	Hi          int    `json:"hi"` // 当前仍可能的最大值
	Attempts    int    `json:"attempts"`
	Starter     string `json:"starter"`      // 发起者 QID
	StarterName string `json:"starter_name"` // 发起者昵称
	StartedAt   int64  `json:"started_at"`
}

// game24State 24 点对局状态。
type game24State struct {
	Nums      []int  `json:"nums"`
	Starter   string `json:"starter"`
	Attempts  int    `json:"attempts"`
	StartedAt int64  `json:"started_at"`
}

// scoreEntry 24 点胜场记录。
type scoreEntry struct {
	Name string `json:"name"`
	Wins int    `json:"wins"`
}

// GamesPlugin 插件定义。
type GamesPlugin struct {
	plugin.Meta
	cfg   gamesConfig
	store storage.PersistentStorage // Clone("games") 后的命名空间（24 点胜场）
	mu    sync.Mutex
	guess map[string]*guessGame   // 群ID → 猜数字对局（内存态，重启作废）
	g24   map[string]*game24State // 群ID → 24 点对局（内存态）
}

// NewPlugin 构造函数。
func NewPlugin() *GamesPlugin {
	p := &GamesPlugin{
		guess: make(map[string]*guessGame),
		g24:   make(map[string]*game24State),
	}
	p.Name = "群小游戏"
	p.HelpWords = "群内 @我 /猜数字 开始猜数游戏（/猜 50），/24 出一道 24 点（/24 (5-3)*8+8 抢答，/24 榜 看胜场榜），/选 a b c 随机帮你选"
	p.AdminOnly = false
	p.ShowFor = plugininfo.ShowForGroup
	p.Author = "jeanhua"
	p.Version = "1.0.0"
	p.Order = plugin.LevelNormal
	return p
}

// ConfigSchema 声明配置结构体。
func (p *GamesPlugin) ConfigSchema() any { return &p.cfg }

// Start 初始化。
func (p *GamesPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	if p.PersistentStorage != nil {
		p.store = p.PersistentStorage.Clone("games")
	}
	p.Logger.Info("群小游戏插件初始化",
		"enable", p.cfg.Enable,
		"enable_guess", p.cfg.EnableGuess,
		"enable_24", p.cfg.Enable24,
		"enable_pick", p.cfg.EnablePick,
		"guess_max", p.cfg.GuessMax,
	)
	return nil
}

// OnGroupMsg 群聊消息事件。
func (p *GamesPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if !p.cfg.Enable || !cmd.Mention {
		return true, nil
	}
	gid := msg.GroupId.String()

	switch name := strings.ToLower(cmd.Name); name {
	case "猜数字", "guess":
		if !p.cfg.EnableGuess {
			return true, nil
		}
		p.cmdGuess(b, gid, cmd.Args, msg)
	case "猜":
		if !p.cfg.EnableGuess {
			return true, nil
		}
		p.cmdMakeGuess(b, gid, cmd.Args, msg)
	case "24", "24点", "game24":
		if !p.cfg.Enable24 {
			return true, nil
		}
		p.cmdGame24(ctx, b, msg.GroupId, gid, cmd.Args, msg)
	case "选", "选择", "挑一个":
		if !p.cfg.EnablePick {
			return true, nil
		}
		p.cmdPick(b, cmd.Args, msg)
	default:
		return true, nil
	}
	return false, nil
}

// ---------- 猜数字 ----------

// cmdGuess /猜数字：开始/查看/结束对局。
func (p *GamesPlugin) cmdGuess(b bot.Bot, gid string, args []string, msg message.Message) {
	if len(args) > 0 && (args[0] == "结束" || args[0] == "放弃") {
		p.endGuess(b, msg.GroupId, gid, msg)
		return
	}
	g := p.activeGuess(gid)
	if g == nil {
		p.startGuess(b, gid, msg)
		return
	}
	p.reply(b, msg.GroupId, fmt.Sprintf("🎯 进行中：范围 %d~%d，已猜 %d 次（%s 发起，发送 /猜 <数字> 继续猜）",
		g.Lo, g.Hi, g.Attempts, g.StarterName))
}

// startGuess 开一局猜数字。
func (p *GamesPlugin) startGuess(b bot.Bot, gid string, msg message.Message) {
	maxN := guessDefaultMax
	if p.cfg.GuessMax > 1 {
		maxN = p.cfg.GuessMax
	}
	g := &guessGame{
		Target:      1 + randIntn(maxN),
		Lo:          1,
		Hi:          maxN,
		Starter:     msg.Sender.UserId.String(),
		StarterName: displayName(msg.Sender),
		StartedAt:   time.Now().Unix(),
	}
	p.mu.Lock()
	p.guess[gid] = g
	p.mu.Unlock()
	p.reply(b, msg.GroupId, fmt.Sprintf("🎯 猜数字开始！我想好了一个 1~%d 的整数，@我发送 /猜 <数字> 来猜（/猜数字 结束 可提前揭晓）", maxN))
}

// endGuess 提前结束当前局（发起者或管理员）。
func (p *GamesPlugin) endGuess(b bot.Bot, group message.QID, gid string, msg message.Message) {
	p.mu.Lock()
	g, ok := p.guess[gid]
	if ok && g.Starter != msg.Sender.UserId.String() && msg.Sender.UserId != p.SystemConfig.AdminId {
		p.mu.Unlock()
		p.reply(b, group, "只有发起者或管理员可以提前结束")
		return
	}
	if ok {
		delete(p.guess, gid)
	}
	p.mu.Unlock()
	if !ok {
		p.reply(b, group, "当前没有进行中的猜数字，@我发送 /猜数字 开始一局")
		return
	}
	p.reply(b, group, fmt.Sprintf("🎯 游戏结束，答案是 %d", g.Target))
}

// cmdMakeGuess /猜 <数字>：进行一次猜测。
func (p *GamesPlugin) cmdMakeGuess(b bot.Bot, gid string, args []string, msg message.Message) {
	n, err := strconv.Atoi(strings.TrimSpace(strings.Join(args, "")))
	if err != nil {
		p.reply(b, msg.GroupId, "用法：/猜 <数字>")
		return
	}

	p.mu.Lock()
	g, ok := p.guess[gid]
	if ok && p.expired(g.StartedAt, p.cfg.GuessTimeoutMinutes) {
		delete(p.guess, gid)
		ok = false
	}
	if !ok {
		p.mu.Unlock()
		p.reply(b, msg.GroupId, "当前没有进行中的猜数字，@我发送 /猜数字 开始一局")
		return
	}

	g.Attempts++
	if n == g.Target {
		attempts := g.Attempts
		delete(p.guess, gid)
		p.mu.Unlock()
		c := msgchain.Builder().Group()
		c.Mention(msg.Sender.UserId)
		c.Text(fmt.Sprintf(" 🎉 答对了！答案就是 %d，全群共猜了 %d 次", n, attempts))
		b.SendGroupMsg(msg.GroupId, c.Build())
		return
	}

	// 收缩可能范围：范围端点只在猜测值「有用」时才更新
	hintLo, hintHi := g.Lo, g.Hi
	if n < g.Target {
		if n >= g.Lo {
			g.Lo = n + 1
		}
	} else {
		if n <= g.Hi {
			g.Hi = n - 1
		}
	}
	hint := "小了"
	if n > g.Target {
		hint = "大了"
	}
	outside := n < hintLo || n > hintHi
	p.mu.Unlock()

	if outside {
		p.reply(b, msg.GroupId, fmt.Sprintf("%d %s（这个数早已排除，当前范围 %d~%d）", n, hint, hintLo, hintHi))
		return
	}
	p.reply(b, msg.GroupId, fmt.Sprintf("%d %s！范围缩小到 %d~%d", n, hint, hintLo, hintHi))
}

// activeGuess 取有效对局（超时视为不存在并清理）。
func (p *GamesPlugin) activeGuess(gid string) *guessGame {
	p.mu.Lock()
	defer p.mu.Unlock()
	g, ok := p.guess[gid]
	if !ok {
		return nil
	}
	if p.expired(g.StartedAt, p.cfg.GuessTimeoutMinutes) {
		delete(p.guess, gid)
		return nil
	}
	return g
}

// ---------- 24 点 ----------

// cmdGame24 /24：开始、抢答、看榜、放弃。
func (p *GamesPlugin) cmdGame24(ctx context.Context, b bot.Bot, group message.QID, gid string, args []string, msg message.Message) {
	sub := strings.TrimSpace(strings.Join(args, " "))

	if sub == "" {
		if st := p.active24(gid); st != nil {
			p.reply(b, group, fmt.Sprintf("🎴 当前题目：%s，@我发送 /24 <表达式> 抢答", joinInts(st.Nums, " ")))
			return
		}
		p.start24(b, gid, msg)
		return
	}

	switch sub {
	case "榜", "排行", "排行榜":
		p.show24Board(ctx, b, group)
		return
	case "答案", "提示", "跳过", "放弃", "投降":
		p.reveal24(b, group, gid)
		return
	}

	// 抢答
	p.mu.Lock()
	st, ok := p.g24[gid]
	if ok && p.expired(st.StartedAt, p.cfg.Game24TimeoutMinutes) {
		delete(p.g24, gid)
		ok = false
	}
	if !ok {
		p.mu.Unlock()
		p.reply(b, group, "当前没有进行中的 24 点，@我发送 /24 出题")
		return
	}
	st.Attempts++

	v, used, err := eval24(sub)
	if err != nil {
		p.mu.Unlock()
		p.reply(b, group, "表达式有问题："+err.Error())
		return
	}
	if !sameMultiset(used, st.Nums) {
		p.mu.Unlock()
		p.reply(b, group, fmt.Sprintf("必须且只能使用这 4 个数字各一次：%s（你用了 %s）",
			joinInts(st.Nums, " "), joinInts(used, " ")))
		return
	}
	if !v.isInt(game24Target) {
		p.mu.Unlock()
		p.reply(b, group, fmt.Sprintf("%s = %s，结果不是 24，再想想", normalizeExpr(sub), v.String()))
		return
	}

	// 答对：记录胜场并结束
	attempts := st.Attempts
	delete(p.g24, gid)
	p.mu.Unlock()
	p.addWin24(ctx, gid, msg.Sender.UserId, displayName(msg.Sender))

	c := msgchain.Builder().Group()
	c.Mention(msg.Sender.UserId)
	c.Text(fmt.Sprintf(" 🎉 漂亮！%s = 24，本局第 %d 次尝试", normalizeExpr(sub), attempts))
	b.SendGroupMsg(group, c.Build())
}

// start24 出题。
func (p *GamesPlugin) start24(b bot.Bot, gid string, msg message.Message) {
	maxN := p.cfg.NumberMax24
	if maxN < 1 {
		maxN = 13
	}
	nums := gen24(maxN)
	st := &game24State{
		Nums:      nums,
		Starter:   msg.Sender.UserId.String(),
		StartedAt: time.Now().Unix(),
	}
	p.mu.Lock()
	p.g24[gid] = st
	p.mu.Unlock()
	p.reply(b, msg.GroupId, fmt.Sprintf("🎴 24 点来啦！用 %s 和 + - * / ( ) 算出 24，每个数字恰好用一次\n@我发送 /24 <表达式> 抢答；/24 答案 揭晓；/24 榜 看胜场榜", joinInts(nums, " ")))
}

// reveal24 揭晓答案并结束。
func (p *GamesPlugin) reveal24(b bot.Bot, group message.QID, gid string) {
	p.mu.Lock()
	st, ok := p.g24[gid]
	if ok {
		delete(p.g24, gid)
	}
	p.mu.Unlock()
	if !ok {
		p.reply(b, group, "当前没有进行中的 24 点，@我发送 /24 出题")
		return
	}
	text := "🎴 本局结束，题目是 " + joinInts(st.Nums, " ")
	if ans, ok := solve24(st.Nums); ok {
		text += "，参考答案：" + ans + " = 24"
	}
	p.reply(b, group, text)
}

// active24 取有效 24 点对局。
func (p *GamesPlugin) active24(gid string) *game24State {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.g24[gid]
	if !ok {
		return nil
	}
	if p.expired(st.StartedAt, p.cfg.Game24TimeoutMinutes) {
		delete(p.g24, gid)
		return nil
	}
	return st
}

// score24 读取某群的胜场表。
func (p *GamesPlugin) score24(ctx context.Context, gid string) map[string]scoreEntry {
	scores := make(map[string]scoreEntry)
	if p.store == nil {
		return scores
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store.Get(ctx, "w:"+gid, &scores)
	return scores
}

// addWin24 记录一次获胜。
func (p *GamesPlugin) addWin24(ctx context.Context, gid string, user message.QID, name string) {
	if p.store == nil {
		return
	}
	scores := p.score24(ctx, gid)
	e := scores[user.String()]
	e.Wins++
	if name != "" {
		e.Name = name
	}
	scores[user.String()] = e
	p.mu.Lock()
	p.store.Set(ctx, "w:"+gid, scores)
	p.mu.Unlock()
}

// show24Board 展示胜场榜。
func (p *GamesPlugin) show24Board(ctx context.Context, b bot.Bot, group message.QID) {
	scores := p.score24(ctx, group.String())
	if len(scores) == 0 {
		p.reply(b, group, "还没有人赢过 24 点，快来抢答拿第一！")
		return
	}
	type row struct {
		name string
		wins int
	}
	rows := make([]row, 0, len(scores))
	for _, e := range scores {
		rows = append(rows, row{name: e.Name, wins: e.Wins})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].wins > rows[j].wins })
	if len(rows) > topListSize {
		rows = rows[:topListSize]
	}
	var sb strings.Builder
	sb.WriteString("🏆 24 点胜场榜：\n")
	for i, r := range rows {
		name := r.name
		if name == "" {
			name = "神秘玩家"
		}
		fmt.Fprintf(&sb, "%d. %s — %d 胜\n", i+1, name, r.wins)
	}
	p.reply(b, group, sb.String())
}

// ---------- 随机选择 ----------

// pickSeparators 随机选择时过滤掉的分隔词与标点。
var pickSeparators = map[string]struct{}{
	"或": {}, "还是": {}, "呢": {}, "啊": {},
	",": {}, "，": {}, "、": {}, "?": {}, "？": {},
}

// cmdPick /选 a b c：随机选一个。
func (p *GamesPlugin) cmdPick(b bot.Bot, args []string, msg message.Message) {
	var opts []string
	for _, a := range args {
		a = strings.TrimSpace(a)
		if _, sep := pickSeparators[a]; sep || a == "" {
			continue
		}
		opts = append(opts, truncateRunes(a, maxPickOptLen))
	}
	if len(opts) < 2 {
		p.reply(b, msg.GroupId, "至少给我两个选项呀，例如：/选 吃饭 吃面 叫外卖")
		return
	}
	if len(opts) > maxPickOptions {
		opts = opts[:maxPickOptions]
	}
	pick := opts[randIntn(len(opts))]
	c := msgchain.Builder().Group()
	c.Mention(msg.Sender.UserId)
	c.Text(" 🎲 我选【" + pick + "】！")
	b.SendGroupMsg(msg.GroupId, c.Build())
}

// ---------- 小工具 ----------

// reply 发送纯文本群消息。
func (p *GamesPlugin) reply(b bot.Bot, group message.QID, text string) {
	c := msgchain.Builder().Group()
	c.Text(text)
	if _, ok := b.SendGroupMsg(group, c.Build()); !ok {
		p.Logger.Warn("游戏消息发送失败", "group", group)
	}
}

// expired 对局是否超时（timeoutMinutes <= 0 表示不限时）。
func (p *GamesPlugin) expired(startedAt int64, timeoutMinutes int) bool {
	if timeoutMinutes <= 0 {
		return false
	}
	return time.Now().Unix()-startedAt > int64(timeoutMinutes)*60
}

// displayName 取群名片，空则回退昵称。
func displayName(sender message.MessageSender) string {
	if sender.Card != "" {
		return sender.Card
	}
	return sender.Nickname
}

// joinInts 整数列表转字符串。
func joinInts(nums []int, sep string) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, sep)
}

// randIntn [0, n) 随机数。
func randIntn(n int) int {
	return rand.Intn(n)
}

// truncateRunes 按字符数截断，超长补省略号。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
