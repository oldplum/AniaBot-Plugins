// timeparse.go 提醒时间描述的解析：把「30分钟后」「每天8:30」「每周一 18:00」
// 「明天 9点」「12-25 10:00」等中文表达解析为触发时间。
// 纯函数实现，便于表驱动测试。
package reminder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repeat 重复规则取值。
const (
	repeatNone   = ""
	repeatDaily  = "daily"
	repeatWeekly = "weekly:" // 后缀 0~6，0=周日
)

// parsedTime 解析结果：触发时间 + 重复规则。
type parsedTime struct {
	At     time.Time
	Repeat string
}

var (
	reDaily  = regexp.MustCompile(`^(每天|每日|天天)\s*`)
	reWeekly = regexp.MustCompile(`^(每)?(?:周|星期|礼拜)([一二三四五六日天])\s*`)
	reRel    = regexp.MustCompile(`^(\d+)\s*(?:个)?\s*(秒|分钟|分|小时|时|天|日|周|星期)\s*后\s*`)
	reHalf   = regexp.MustCompile(`^半小时\s*后?\s*`)
	reDay    = regexp.MustCompile(`^(今天|今日|明天|明日|后天)\s*`)
	reDate   = regexp.MustCompile(`^(?:(\d{4})\s*[-/年]\s*)?(\d{1,2})\s*[-/月]\s*(\d{1,2})\s*日?\s*`)
	reClock  = regexp.MustCompile(`^(\d{1,2})\s*[点时:：]\s*(?:(半)|(\d{1,2})\s*分?)?\s*`)
)

// 无时间部分时的默认时刻（早上 9 点）。
const defaultHour, defaultMinute = 9, 0

// 相对时间的最大跨度：10 年。
const maxRelativeSeconds = 10 * 365 * 24 * 3600

// parseReminderTime 从 input 开头解析时间描述，返回触发时间与剩余内容。
// 支持的格式（时间部分可省略，省略时默认 09:00；相对时间必须完整）：
//
//	相对：30分钟后 / 2小时后 / 半小时后 / 3天后 / 1周后
//	每天：每天8:30 / 每天 8点30 / 每日晚安（默认 09:00）
//	每周：每周一 18:00 / 每周日 9点
//	一次性：明天 9点 / 后天 14:30 / 周三 18:00 / 09-15 10:00 / 2026-12-25 08:00 / 18:00
func parseReminderTime(input string, now time.Time) (parsedTime, string, error) {
	s := strings.TrimSpace(input)

	// 每天 …
	if m := reDaily.FindString(s); m != "" {
		s = strings.TrimPrefix(s, m)
		h, min, rest, err := parseClockOpt(s, defaultHour, defaultMinute)
		if err != nil {
			return parsedTime{}, "", err
		}
		s = rest
		return parsedTime{At: nextDaily(now, h, min), Repeat: repeatDaily}, s, nil
	}

	// （每）周X …
	if m := reWeekly.FindStringSubmatch(s); m != nil {
		every := m[1] == "每"
		wd, ok := cnWeekday(m[2])
		if !ok {
			return parsedTime{}, "", fmt.Errorf("不认识的星期：%s", m[2])
		}
		s = strings.TrimPrefix(s, m[0])
		h, min, rest, err := parseClockOpt(s, defaultHour, defaultMinute)
		if err != nil {
			return parsedTime{}, "", err
		}
		s = rest
		if every {
			return parsedTime{At: nextWeekly(now, wd, h, min), Repeat: repeatWeekly + strconv.Itoa(int(wd))}, s, nil
		}
		return parsedTime{At: nextWeekly(now, wd, h, min)}, s, nil
	}

	// 半小时后
	if m := reHalf.FindString(s); m != "" {
		s = strings.TrimPrefix(s, m)
		return parsedTime{At: now.Add(30 * time.Minute)}, s, nil
	}

	// N秒/分钟/小时/天/周 后
	if m := reRel.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n <= 0 {
			return parsedTime{}, "", fmt.Errorf("时间数量应是正整数：%s", m[1])
		}
		var unit time.Duration
		switch m[2] {
		case "秒":
			unit = time.Second
		case "分钟", "分":
			unit = time.Minute
		case "小时", "时":
			unit = time.Hour
		case "天", "日":
			unit = 24 * time.Hour
		case "周", "星期":
			unit = 7 * 24 * time.Hour
		}
		total := time.Duration(n) * unit
		if total > maxRelativeSeconds*time.Second {
			return parsedTime{}, "", fmt.Errorf("提醒时间太远了（最多 10 年）")
		}
		s = strings.TrimPrefix(s, m[0])
		return parsedTime{At: now.Add(total)}, s, nil
	}

	// 今天/明天/后天 …
	if m := reDay.FindStringSubmatch(s); m != nil {
		offset := map[string]int{"今天": 0, "今日": 0, "明天": 1, "明日": 1, "后天": 2}[m[1]]
		s = strings.TrimPrefix(s, m[0])
		h, min, rest, err := parseClockOpt(s, defaultHour, defaultMinute)
		if err != nil {
			return parsedTime{}, "", err
		}
		s = rest
		return parsedTime{At: atClock(now.AddDate(0, 0, offset), h, min)}, s, nil
	}

	// 日期 [时刻]
	if m := reDate.FindStringSubmatch(s); m != nil {
		year := now.Year()
		if m[1] != "" {
			y, err := strconv.Atoi(m[1])
			if err != nil {
				return parsedTime{}, "", fmt.Errorf("年份不合法：%s", m[1])
			}
			year = y
		}
		mo, err1 := strconv.Atoi(m[2])
		d, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil || mo < 1 || mo > 12 || d < 1 || d > 31 {
			return parsedTime{}, "", fmt.Errorf("日期不合法：%s", m[0])
		}
		s = strings.TrimPrefix(s, m[0])
		h, min, rest, err := parseClockOpt(s, defaultHour, defaultMinute)
		if err != nil {
			return parsedTime{}, "", err
		}
		s = rest
		t := time.Date(year, time.Month(mo), d, h, min, 0, 0, now.Location())
		return parsedTime{At: t}, s, nil
	}

	// 裸时刻：今天或明天的 HH:MM
	if m := reClock.FindStringSubmatch(s); m != nil {
		h, min, ok := clockParts(m)
		if !ok {
			return parsedTime{}, "", fmt.Errorf("时刻不合法：%s", m[0])
		}
		s = strings.TrimPrefix(s, m[0])
		return parsedTime{At: nextClockFrom(now, h, min)}, s, nil
	}

	return parsedTime{}, "", fmt.Errorf("没能识别时间，支持：30分钟后 / 每天8:30 / 每周一 18:00 / 明天 9点 / 09-15 10:00 / 18:00")
}

// parseClockOpt 解析可选时刻；无时刻时用传入默认值。
func parseClockOpt(s string, defH, defM int) (h, m int, rest string, err error) {
	c := strings.TrimLeft(s, " ")
	if cm := reClock.FindStringSubmatch(c); cm != nil {
		hh, mm, ok := clockParts(cm)
		if !ok {
			return 0, 0, "", fmt.Errorf("时刻不合法：%s", cm[0])
		}
		return hh, mm, strings.TrimPrefix(c, cm[0]), nil
	}
	return defH, defM, s, nil
}

// clockParts 从 reClock 的子匹配提取时/分并校验。
func clockParts(m []string) (h, min int, ok bool) {
	h, err := strconv.Atoi(m[1])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	switch {
	case m[2] == "半":
		min = 30
	case m[3] != "":
		min, err = strconv.Atoi(m[3])
		if err != nil || min < 0 || min > 59 {
			return 0, 0, false
		}
	default:
		min = 0
	}
	return h, min, true
}

// cnWeekday 中文星期字符 → time.Weekday。
func cnWeekday(c string) (time.Weekday, bool) {
	switch c {
	case "一":
		return time.Monday, true
	case "二":
		return time.Tuesday, true
	case "三":
		return time.Wednesday, true
	case "四":
		return time.Thursday, true
	case "五":
		return time.Friday, true
	case "六":
		return time.Saturday, true
	case "日", "天":
		return time.Sunday, true
	}
	return time.Sunday, false
}

// atClock 把 t 当天的时刻设为 h:m。
func atClock(t time.Time, h, m int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location())
}

// nextClockFrom 今天 h:m，已过则明天。
func nextClockFrom(now time.Time, h, m int) time.Time {
	t := atClock(now, h, m)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// nextDaily 下一个每天的 h:m（严格晚于 now）。
func nextDaily(now time.Time, h, m int) time.Time {
	return nextClockFrom(now, h, m)
}

// nextWeekly 下一个周 wd 的 h:m（严格晚于 now）。
func nextWeekly(now time.Time, wd time.Weekday, h, m int) time.Time {
	diff := int(wd) - int(now.Weekday())
	if diff < 0 {
		diff += 7
	}
	t := atClock(now.AddDate(0, 0, diff), h, m)
	if !t.After(now) {
		t = t.AddDate(0, 0, 7)
	}
	return t
}

// nextRepeat 计算循环提醒的下一次触发时间：从上次触发时间逐周期推进，
// 直到严格晚于 now。落后超过 400 个周期（防止停机数月后爆发补发）时放弃。
func nextRepeat(prevAt int64, now time.Time, repeat string) (int64, bool) {
	t := time.Unix(prevAt, 0)
	for i := 0; i < 400; i++ {
		switch {
		case repeat == repeatDaily:
			t = t.AddDate(0, 0, 1)
		case strings.HasPrefix(repeat, repeatWeekly):
			t = t.AddDate(0, 0, 7)
		default:
			return 0, false
		}
		if t.After(now) {
			return t.Unix(), true
		}
	}
	return 0, false
}

// repeatText 重复规则的可读描述。
func repeatText(repeat string) string {
	switch repeat {
	case repeatDaily:
		return "每天"
	case repeatWeekly + "1":
		return "每周一"
	case repeatWeekly + "2":
		return "每周二"
	case repeatWeekly + "3":
		return "每周三"
	case repeatWeekly + "4":
		return "每周四"
	case repeatWeekly + "5":
		return "每周五"
	case repeatWeekly + "6":
		return "每周六"
	case repeatWeekly + "0":
		return "每周日"
	}
	return ""
}
