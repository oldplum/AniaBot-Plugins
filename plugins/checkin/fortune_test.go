package checkin

import "testing"

func TestFortuneTextLevels(t *testing.T) {
	// base=lo 应为最低档，base=hi 应为最高档
	if f, _ := fortuneText(10, 10, 50); f != "平" {
		t.Errorf("base=lo 期望 平，得到 %s", f)
	}
	if f, _ := fortuneText(50, 10, 50); f != "大吉" {
		t.Errorf("base=hi 期望 大吉，得到 %s", f)
	}
	if f, _ := fortuneText(10+35, 10, 50); f != "吉" {
		t.Errorf("base=45 期望 吉，得到 %s", f)
	}
	if f, _ := fortuneText(10+20, 10, 50); f != "中吉" {
		t.Errorf("base=30 期望 中吉，得到 %s", f)
	}
	if f, _ := fortuneText(10+10, 10, 50); f != "小吉" {
		t.Errorf("base=20 期望 小吉，得到 %s", f)
	}
}

func TestFortuneTextDegenerateRange(t *testing.T) {
	// hi==lo 时 ratio 恒为 0，应落在最低档且不 panic
	for base := 0; base < 5; base++ {
		if f, _ := fortuneText(base, 1, 1); f != "平" {
			t.Errorf("hi==lo 期望 平，得到 %s", f)
		}
	}
}

func TestFortuneTextFlavorNotEmpty(t *testing.T) {
	for _, base := range []int{10, 20, 30, 40, 50} {
		_, flavor := fortuneText(base, 10, 50)
		if flavor == "" {
			t.Errorf("base=%d 点评文案不应为空", base)
		}
	}
}

func TestStreakTitle(t *testing.T) {
	cases := []struct {
		streak int
		want   string
	}{
		{1, "1 天连签"},
		{3, "小有所成"},
		{7, "一周全勤"},
		{14, "风雨无阻"},
		{30, "月度钉子户"},
		{100, "百天钉子户"},
		{365, "百天钉子户"},
	}
	for _, c := range cases {
		if got := streakTitle(c.streak); got != c.want {
			t.Errorf("streakTitle(%d) = %q, 期望 %q", c.streak, got, c.want)
		}
	}
}
