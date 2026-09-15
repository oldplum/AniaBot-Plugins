package reminder

import (
	"testing"
	"time"
)

// now 固定基准时间：2026-09-11 12:00:00（周五）。
func now() time.Time {
	return time.Date(2026, time.September, 11, 12, 0, 0, 0, time.Local)
}

func at(y, mo, d, h, mi int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.Local)
}

func TestParseRelative(t *testing.T) {
	cases := []struct {
		in      string
		wantAt  time.Time
		wantTxt string
	}{
		{"30分钟后 喝水", now().Add(30 * time.Minute), "喝水"},
		{"2小时后 开会", now().Add(2 * time.Hour), "开会"},
		{"90秒后 测试", now().Add(90 * time.Second), "测试"},
		{"半小时后 收快递", now().Add(30 * time.Minute), "收快递"},
		{"3天后 交报告", now().AddDate(0, 0, 3), "交报告"},
		{"1周后 复查", now().AddDate(0, 0, 7), "复查"},
		{"10 分钟后 带空格", now().Add(10 * time.Minute), "带空格"},
	}
	for _, c := range cases {
		pt, txt, err := parseReminderTime(c.in, now())
		if err != nil {
			t.Fatalf("%q 解析失败: %v", c.in, err)
		}
		if !pt.At.Equal(c.wantAt) {
			t.Errorf("%q: 时间 = %v, 期望 %v", c.in, pt.At, c.wantAt)
		}
		if pt.Repeat != repeatNone {
			t.Errorf("%q: 不应有重复规则, got %q", c.in, pt.Repeat)
		}
		if txt != c.wantTxt {
			t.Errorf("%q: 内容 = %q, 期望 %q", c.in, txt, c.wantTxt)
		}
	}
}

func TestParseRecurring(t *testing.T) {
	cases := []struct {
		in      string
		wantAt  time.Time
		repeat  string
		wantTxt string
	}{
		// 周五 12:00 之后最近的 8:30 是次日
		{"每天8:30 早安", at(2026, 9, 12, 8, 30), repeatDaily, "早安"},
		{"每天 08:30 早安", at(2026, 9, 12, 8, 30), repeatDaily, "早安"},
		{"每天8点30 早安", at(2026, 9, 12, 8, 30), repeatDaily, "早安"},
		{"每天 早安", at(2026, 9, 12, 9, 0), repeatDaily, "早安"}, // 缺省时刻默认 09:00
		{"每周一 18:00 例会", at(2026, 9, 14, 18, 0), repeatWeekly + "1", "例会"},
		{"每周日 9点 教会", at(2026, 9, 13, 9, 0), repeatWeekly + "0", "教会"},
	}
	for _, c := range cases {
		pt, txt, err := parseReminderTime(c.in, now())
		if err != nil {
			t.Fatalf("%q 解析失败: %v", c.in, err)
		}
		if !pt.At.Equal(c.wantAt) {
			t.Errorf("%q: 时间 = %v, 期望 %v", c.in, pt.At, c.wantAt)
		}
		if pt.Repeat != c.repeat {
			t.Errorf("%q: 重复规则 = %q, 期望 %q", c.in, pt.Repeat, c.repeat)
		}
		if txt != c.wantTxt {
			t.Errorf("%q: 内容 = %q, 期望 %q", c.in, txt, c.wantTxt)
		}
	}
}

func TestParseOneShot(t *testing.T) {
	cases := []struct {
		in     string
		wantAt time.Time
	}{
		{"明天 9点 交报告", at(2026, 9, 12, 9, 0)},
		{"后天 14:30 复诊", at(2026, 9, 13, 14, 30)},
		{"今天 18:00 下班", at(2026, 9, 11, 18, 0)},
		{"18:00 下班", at(2026, 9, 11, 18, 0)},
		{"8:30 跑步", at(2026, 9, 12, 8, 30)}, // 今天已过 → 明天
		{"12点 午休", at(2026, 9, 12, 12, 0)},  // 恰好等于当前时刻 → 明天
		{"09-15 10:00 体检", at(2026, 9, 15, 10, 0)},
		{"2026-12-25 08:00 圣诞", at(2026, 12, 25, 8, 0)},
		{"12月25日 8点 圣诞", at(2026, 12, 25, 8, 0)},
		{"周三 18:00 健身", at(2026, 9, 16, 18, 0)}, // 周五之后最近的周三
	}
	for _, c := range cases {
		pt, _, err := parseReminderTime(c.in, now())
		if err != nil {
			t.Fatalf("%q 解析失败: %v", c.in, err)
		}
		if pt.Repeat != repeatNone {
			t.Errorf("%q: 不应有重复规则, got %q", c.in, pt.Repeat)
		}
		if !pt.At.Equal(c.wantAt) {
			t.Errorf("%q: 时间 = %v, 期望 %v", c.in, pt.At, c.wantAt)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"",           // 空
		"喝水",         // 无时间
		"0分钟后 喝水",    // 数量非法
		"25:00 喝水",   // 时刻非法
		"13月1日 8点 x", // 月份非法
		"分钟后 喝水",     // 缺数量
	}
	for _, in := range cases {
		if _, _, err := parseReminderTime(in, now()); err == nil {
			t.Errorf("%q 应解析失败", in)
		}
	}
	// 只有时间没有内容：解析成功但内容为空，由插件层拦截
	_, txt, err := parseReminderTime("30分钟后", now())
	if err != nil || txt != "" {
		t.Errorf("仅有时间的输入应解析成功且内容为空, got %q, %v", txt, err)
	}
}

func TestNextRepeat(t *testing.T) {
	base := at(2026, 9, 10, 8, 0).Unix()
	n := at(2026, 9, 13, 12, 0)

	// 每天：从 09-10 08:00 推进到 09-13 之后的 09-14 08:00
	got, ok := nextRepeat(base, n, repeatDaily)
	if !ok || !time.Unix(got, 0).Equal(at(2026, 9, 14, 8, 0)) {
		t.Errorf("daily 推进错误: %v %v", got, ok)
	}

	// 每周：从 09-01 周二推进到 09-13 之后的下一个周二 09-15
	got, ok = nextRepeat(at(2026, 9, 1, 9, 0).Unix(), n, repeatWeekly+"2")
	if !ok || !time.Unix(got, 0).Equal(at(2026, 9, 15, 9, 0)) {
		t.Errorf("weekly 推进错误: %v %v", got, ok)
	}

	// 未知规则：失败
	if _, ok := nextRepeat(base, n, "x"); ok {
		t.Error("未知重复规则应返回 false")
	}
}

func TestRepeatText(t *testing.T) {
	if repeatText(repeatDaily) != "每天" {
		t.Error("daily 文案错误")
	}
	if repeatText(repeatWeekly+"3") != "每周三" {
		t.Error("weekly 文案错误")
	}
	if repeatText(repeatNone) != "" {
		t.Error("一次性提醒不应有文案")
	}
}
