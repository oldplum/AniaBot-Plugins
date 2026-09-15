// Package checkin 运势文案：按签到基础积分在区间中的位置给出运势等级与一句话点评。
package checkin

import (
	"fmt"
	"math/rand"
)

// fortuneText 依据基础积分 roll 在 [lo,hi] 中的位置返回（运势等级, 点评）。
func fortuneText(base, lo, hi int) (string, string) {
	ratio := 0.0
	if hi > lo {
		ratio = float64(base-lo) / float64(hi-lo)
	}
	switch {
	case ratio >= 0.9:
		return "大吉", pickOne("今天运气爆棚，做什么都顺！", "出门捡钱的节奏，快去买张彩票！")
	case ratio >= 0.75:
		return "吉", pickOne("运气不错，摸鱼都不会被抓。", "宜摸鱼、宜开黑、宜炫饭。")
	case ratio >= 0.5:
		return "中吉", pickOne("平平淡淡才是真。", "不好不坏，稳中向好。")
	case ratio >= 0.25:
		return "小吉", pickOne("小有惊喜，继续攒人品。", "再接再厉，明天爆发！")
	default:
		return "平", pickOne("稳住，我们能赢。", "运气守恒定律：现在省的运气以后都会还的。")
	}
}

// pickOne 随机取一条文案。
func pickOne(cands ...string) string {
	if len(cands) == 0 {
		return ""
	}
	if len(cands) == 1 {
		return cands[0]
	}
	return cands[rand.Intn(len(cands))]
}

// streakTitle 连签天数的俏皮称号。
func streakTitle(streak int) string {
	switch {
	case streak >= 100:
		return "百天钉子户"
	case streak >= 30:
		return "月度钉子户"
	case streak >= 14:
		return "风雨无阻"
	case streak >= 7:
		return "一周全勤"
	case streak >= 3:
		return "小有所成"
	default:
		return fmt.Sprintf("%d 天连签", streak)
	}
}
