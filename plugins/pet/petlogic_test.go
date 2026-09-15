package pet

import "testing"

func TestClamp(t *testing.T) {
	cases := map[int]int{-5: 0, 0: 0, 55: 55, 100: 100, 150: 100}
	for in, want := range cases {
		if got := clamp(in); got != want {
			t.Errorf("clamp(%d) = %d, 期望 %d", in, got, want)
		}
	}
}

func TestApplyDecay(t *testing.T) {
	pc := petState{Hunger: 80, Happy: 80, UpdatedAt: 0}
	// UpdatedAt 未初始化时只设基准时间，不衰减
	applyDecay(&pc, 1000, 4, 3)
	if pc.Hunger != 80 || pc.Happy != 80 || pc.UpdatedAt != 1000 {
		t.Fatalf("首次结算不应衰减： %+v", pc)
	}
	// 过去 1.5 小时：hunger -6（4*1.5=6），happy -4.5 → -5（四舍五入）
	applyDecay(&pc, 1000+5400, 4, 3)
	if pc.Hunger != 74 {
		t.Errorf("hunger 期望 74，得到 %d", pc.Hunger)
	}
	if pc.Happy != 75 {
		t.Errorf("happy 期望 75，得到 %d", pc.Happy)
	}
	// 时间倒流不应产生任何变化
	applyDecay(&pc, 1000, 4, 3)
	if pc.Hunger != 74 || pc.Happy != 75 {
		t.Errorf("时间倒流不应衰减： %+v", pc)
	}
	// 长时间衰减触底为 0
	applyDecay(&pc, 1000+5400+100*3600, 4, 3)
	if pc.Hunger != 0 || pc.Happy != 0 {
		t.Errorf("长时间衰减应触底 0： %+v", pc)
	}
}

func TestAddExpLevelUp(t *testing.T) {
	pc := petState{Level: 1, Exp: 0}
	if ups := addExp(&pc, 59); len(ups) != 0 || pc.Exp != 59 {
		t.Fatalf("59 点不应升级： %+v ups=%v", pc, ups)
	}
	ups := addExp(&pc, 1) // 恰好 60 升 Lv.2
	if len(ups) != 1 || ups[0] != 2 || pc.Level != 2 || pc.Exp != 0 {
		t.Fatalf("60 点应升 Lv.2： %+v ups=%v", pc, ups)
	}
	ups = addExp(&pc, 200) // 连续跳级：Lv.2 需 90，200 → 升 Lv.3 后剩 110 → 再升 Lv.4 剩 50-... 逐级结算
	if pc.Level < 3 {
		t.Fatalf("大量成长值应连续升级： %+v", pc)
	}
	if len(ups) == 0 {
		t.Fatalf("应返回升级等级列表")
	}
}

func TestExpNeededMonotonic(t *testing.T) {
	prev := 0
	for lv := 1; lv <= 50; lv++ {
		need := expNeeded(lv)
		if need <= prev {
			t.Fatalf("expNeeded(%d)=%d 未随等级递增", lv, need)
		}
		prev = need
	}
}

func TestMoodText(t *testing.T) {
	cases := []struct {
		hunger, happy int
		want          string
	}{
		{5, 5, "又饿又无聊，蔫得不行了…"},
		{5, 80, "饿得瘪瘪的，快喂点吃的吧"},
		{80, 5, "无聊透顶，快陪它玩玩吧"},
		{80, 80, "吃饱喝足，心情美美哒"},
		{40, 40, "平静地趴着发呆"},
	}
	for _, c := range cases {
		got, face := moodText(c.hunger, c.happy)
		if got != c.want {
			t.Errorf("moodText(%d, %d) = %q, 期望 %q", c.hunger, c.happy, got, c.want)
		}
		if face == "" {
			t.Errorf("状态表情不应为空")
		}
	}
}

func TestBar(t *testing.T) {
	cases := map[int]string{
		0:   "░░░░░░░░",
		50:  "████░░░░",
		100: "████████",
		-3:  "░░░░░░░░",
		250: "████████",
	}
	for v, want := range cases {
		if got := bar(v, 8); got != want {
			t.Errorf("bar(%d, 8) = %q, 期望 %q", v, got, want)
		}
	}
}

func TestFeedResult(t *testing.T) {
	full := petState{Hunger: 96}
	if _, refuse := feedResult(&full, 25); refuse == "" {
		t.Error("太饱时应返回拒绝文案")
	}
	pc := petState{Hunger: 90}
	add, refuse := feedResult(&pc, 25)
	if refuse != "" || add != 10 {
		t.Errorf("90+25 应只加 10：add=%d refuse=%q", add, refuse)
	}
	pc.Hunger = 50
	add, refuse = feedResult(&pc, 25)
	if refuse != "" || add != 25 {
		t.Errorf("正常喂食 add 期望 25：add=%d refuse=%q", add, refuse)
	}
}

func TestStatusTextContainsKeyInfo(t *testing.T) {
	pc := petState{Name: "糯米", Emoji: "🐱", Species: "小猫", Level: 3, Exp: 10, Hunger: 60, Happy: 40, UpdatedAt: 0}
	text := statusText(&pc, 4, 3, 1000)
	for _, want := range []string{"糯米", "小猫", "Lv.3", "饱食度", "心情"} {
		if !contains(text, want) {
			t.Errorf("状态卡缺少 %q：%q", want, text)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("一二三四", 4); got != "一二三四" {
		t.Errorf("不超长不应截断：%q", got)
	}
	if got := truncateRunes("一二三四五", 4); got != "一二三四…" {
		t.Errorf("超长应截断补省略号：%q", got)
	}
}

// contains 简易子串检查（避免每个用例都引 strings）。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
