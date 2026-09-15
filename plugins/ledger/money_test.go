package ledger

import "testing"

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in         string
		wantCents  int64
		wantIncome bool
		wantErr    bool
	}{
		{"25", -2500, false, false},     // 默认支出
		{"25.5", -2550, false, false},   // 一位小数
		{"25.50", -2550, false, false},  // 两位小数
		{"0.01", -1, false, false},      // 最小金额
		{"+3000", 300000, true, false},  // 收入
		{"+0.5", 50, true, false},       // 收入小数
		{"-12.75", -1275, false, false}, // 显式支出
		{" 100 ", -10000, false, false}, // 前后空白
		{"", 0, false, true},            // 空
		{"abc", 0, false, true},         // 非数字
		{"25.555", 0, false, true},      // 超过两位小数
		{"1.2.3", 0, false, true},       // 多个小数点
		{"12345678901", 0, false, true}, // 整数部分超长（11 位）
		{"++5", 0, false, true},         // 重复符号
		{"+25.5", 2550, true, false},    // 收入带小数
	}
	for _, c := range cases {
		cents, income, err := parseAmount(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q 应解析失败", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q 解析失败: %v", c.in, err)
			continue
		}
		if cents != c.wantCents {
			t.Errorf("%q: 金额 = %d 分, 期望 %d 分", c.in, cents, c.wantCents)
		}
		if income != c.wantIncome {
			t.Errorf("%q: 收支 = %v, 期望 %v", c.in, income, c.wantIncome)
		}
	}
}

func TestFormatCents(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0.00"},
		{2500, "25.00"},
		{255, "2.55"},
		{5, "0.05"},
		{-2500, "-25.00"},
		{-5, "-0.05"},
	}
	for _, c := range cases {
		if got := formatCents(c.in); got != c.want {
			t.Errorf("formatCents(%d) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestSignedAmount(t *testing.T) {
	if got := signedAmount(2550); got != "+25.50" {
		t.Errorf("收入展示错误: %q", got)
	}
	if got := signedAmount(-2550); got != "-25.50" {
		t.Errorf("支出展示错误: %q", got)
	}
}

func TestSummarize(t *testing.T) {
	records := []record{
		{Amount: -2500},
		{Amount: -1000},
		{Amount: 300000},
		{Amount: 50},
	}
	income, expense, count := summarize(records)
	if income != 300050 {
		t.Errorf("收入 = %d, 期望 300050", income)
	}
	if expense != 3500 {
		t.Errorf("支出 = %d, 期望 3500", expense)
	}
	if count != 4 {
		t.Errorf("笔数 = %d, 期望 4", count)
	}
}

func TestParseSeq(t *testing.T) {
	if seq, err := parseSeq([]string{"3"}); err != nil || seq != 3 {
		t.Errorf("parseSeq(3) = %d, %v", seq, err)
	}
	if seq, err := parseSeq([]string{" 12 "}); err != nil || seq != 12 {
		t.Errorf("parseSeq 带空白解析错误: %d, %v", seq, err)
	}
	for _, bad := range [][]string{nil, {"0"}, {"-1"}, {"abc"}} {
		if _, err := parseSeq(bad); err == nil {
			t.Errorf("parseSeq(%v) 应失败", bad)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("你好世界", 3); got != "你好世…" {
		t.Errorf("截断错误: %q", got)
	}
	if got := truncateRunes("你好", 10); got != "你好" {
		t.Errorf("不超长不应截断: %q", got)
	}
	if got := truncateRunes("你好", 0); got != "你好" {
		t.Errorf("上限为 0 时不截断: %q", got)
	}
}
