// Package pet 电子宠物养成逻辑：均为纯函数，便于单元测试。
package pet

import (
	"fmt"
	"math/rand"
	"strings"
)

// speciesPool 可领养的物种池（表情 + 物种名）。
var speciesPool = []struct {
	Emoji   string
	Species string
}{
	{"🐱", "小猫"},
	{"🐶", "小狗"},
	{"🐰", "小兔"},
	{"🐹", "仓鼠"},
	{"🦊", "小狐狸"},
	{"🐼", "小熊猫"},
	{"🦁", "小狮子"},
	{"🦔", "小刺猬"},
}

// randomSpecies 随机挑一只物种。
func randomSpecies() (emoji, species string) {
	s := speciesPool[rand.Intn(len(speciesPool))]
	return s.Emoji, s.Species
}

// petState 宠物存档（JSON 持久化）。
type petState struct {
	Name      string `json:"name"`
	Emoji     string `json:"emoji"`
	Species   string `json:"species"`
	Level     int    `json:"level"`       // 等级，从 1 开始
	Exp       int    `json:"exp"`         // 当前等级内成长值
	Hunger    int    `json:"hunger"`      // 饱食度 0~100
	Happy     int    `json:"happy"`       // 心情 0~100
	UpdatedAt int64  `json:"updated_at"`  // 上次结算衰减的时间
	LastFeed  int64  `json:"last_feed"`   // 上次喂食时间
	LastPlay  int64  `json:"last_play"`   // 上次逗宠时间
	FedCount  int    `json:"fed_count"`   // 累计喂食次数
	PlayedCount int  `json:"played_count"` // 累计逗宠次数
}

// clamp 把值限制在 [0, 100]。
func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// applyDecay 按经过的小时数结算饱食度/心情衰减（纯函数，直接改传入结构体）。
func applyDecay(pc *petState, now int64, hungerPerHour, happyPerHour float64) {
	if pc.UpdatedAt <= 0 {
		pc.UpdatedAt = now
		return
	}
	hours := float64(now-pc.UpdatedAt) / 3600
	if hours <= 0 {
		return
	}
	pc.Hunger = clamp(pc.Hunger - int(hours*hungerPerHour+0.5))
	pc.Happy = clamp(pc.Happy - int(hours*happyPerHour+0.5))
	pc.UpdatedAt = now
}

// expNeeded 升到下一级所需成长值。
func expNeeded(level int) int {
	return 60 + (level-1)*30
}

// addExp 增加成长值并结算升级，返回升到的等级列表（可能连续升级）。
func addExp(pc *petState, n int) []int {
	var ups []int
	pc.Exp += n
	for pc.Level <= 0 {
		pc.Level = 1
	}
	for pc.Exp >= expNeeded(pc.Level) {
		pc.Exp -= expNeeded(pc.Level)
		pc.Level++
		ups = append(ups, pc.Level)
	}
	return ups
}

// moodText 依据饱食度/心情返回（状态描述, 状态表情）。
func moodText(hunger, happy int) (string, string) {
	switch {
	case hunger < 15 && happy < 15:
		return "又饿又无聊，蔫得不行了…", "🥺"
	case hunger < 15:
		return "饿得瘪瘪的，快喂点吃的吧", "🍽️"
	case happy < 15:
		return "无聊透顶，快陪它玩玩吧", "💤"
	case hunger >= 60 && happy >= 60:
		return "吃饱喝足，心情美美哒", "😊"
	default:
		return "平静地趴着发呆", "😌"
	}
}

// bar 渲染进度条：bar(60, 10) → "██████░░░░"。
func bar(v, width int) string {
	if width <= 0 {
		return ""
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	filled := v * width / 100
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// statusText 渲染宠物状态卡。
func statusText(pc *petState, hungerPerHour, happyPerHour float64, now int64) string {
	tmp := *pc // 不改动原数据，仅用于展示
	applyDecay(&tmp, now, hungerPerHour, happyPerHour)
	mood, face := moodText(tmp.Hunger, tmp.Happy)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s（%s）Lv.%d %s\n", tmp.Emoji, tmp.Name, tmp.Species, tmp.Level, face)
	fmt.Fprintf(&sb, "饱食度 [%s] %d%%\n", bar(tmp.Hunger, 8), tmp.Hunger)
	fmt.Fprintf(&sb, "心情   [%s] %d%%\n", bar(tmp.Happy, 8), tmp.Happy)
	fmt.Fprintf(&sb, "状态：%s\n", mood)
	fmt.Fprintf(&sb, "成长值 %d/%d（累计喂食 %d 次、逗宠 %d 次）", tmp.Exp, expNeeded(tmp.Level), tmp.FedCount, tmp.PlayedCount)
	return sb.String()
}

// playEvents 逗宠时的随机趣味事件（15% 概率触发）。
var playEvents = []string{
	"追着自己的尾巴转了三圈，把自己转晕了",
	"突然开始满地跑酷，撞翻了水碗",
	"对着窗外的小鸟看了半天，入了神",
	"在你怀里打起了小呼噜，特别满足",
	"翻了个身露出肚皮，示意你使劲rua",
	"叼来一双拖鞋邀你一起玩耍",
}

// maybePlayEvent 15% 概率返回一条随机事件文案，否则返回空串。
func maybePlayEvent() string {
	if rand.Intn(100) < 15 {
		return playEvents[rand.Intn(len(playEvents))]
	}
	return ""
}

// feedResult 计算喂食效果：返回（实际增加的饱食度, 拒绝原因）。
// 拒绝原因非空表示太饱吃不下。
func feedResult(pc *petState, gain int) (int, string) {
	if pc.Hunger >= 95 {
		return 0, "肚子圆滚滚的，一口都吃不下啦…"
	}
	add := gain
	if pc.Hunger+add > 100 {
		add = 100 - pc.Hunger
	}
	return add, ""
}
