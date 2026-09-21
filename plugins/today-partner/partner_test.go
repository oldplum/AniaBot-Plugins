package todaypartner

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// seg 快速构造消息段。
func seg(typ string, data map[string]any) message.OB11Segment {
	return message.OB11Segment{Type: typ, Data: data}
}

// member 快速构造群成员。
func member(uid message.QID, card, nick string) message.GroupUserInfo {
	return message.GroupUserInfo{UserID: uid, Card: card, Nickname: nick}
}

func TestClampDraws(t *testing.T) {
	cases := []struct{ in, want int }{
		{-1, 1}, {0, 1}, {1, 1}, {3, 3}, {5, 5}, {6, 5}, {100, 5},
	}
	for _, c := range cases {
		if got := clampDraws(c.in); got != c.want {
			t.Errorf("clampDraws(%d) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

func TestDrawStateEnsureTodayAndRemaining(t *testing.T) {
	var st drawState
	st.ensureToday("2026-09-14")
	if st.Date != "2026-09-14" || st.Used != 0 || len(st.Partners) != 0 {
		t.Fatalf("空状态 ensureToday 后应清零: %+v", st)
	}

	st.Partners = []partner{{QQ: "1", Name: "a"}}
	st.Used = 2
	// 跨天重置：Used/Partners 清空
	st.ensureToday("2026-09-15")
	if st.Used != 0 || len(st.Partners) != 0 {
		t.Fatalf("跨天应重置: %+v", st)
	}

	// 剩余次数随 Used 递减，最低 0
	st.Used = 3
	if got := st.remaining(5); got != 2 {
		t.Errorf("remaining = %d, 期望 2", got)
	}
	st.Used = 5
	if got := st.remaining(5); got != 0 {
		t.Errorf("用完时 remaining = %d, 期望 0", got)
	}
	st.Used = 7
	if got := st.remaining(5); got != 0 {
		t.Errorf("超抽时 remaining = %d, 期望 0", got)
	}
}

func TestFilterCandidates(t *testing.T) {
	self := message.FromUint64(100)
	sender := message.FromUint64(200)
	robot := member(message.FromUint64(9), "", "小冰")
	robot.IsRobot = true
	members := []message.GroupUserInfo{
		member(message.FromUint64(200), "发送者", "小发送"), // 自己 → 剔除
		member(self, "", "机器人"),                       // 机器人 → 剔除
		robot,                                         // 机器人账号 → 剔除
		member(message.FromUint64(1), "群名片A", "昵称A"), // 群名片优先
		member(message.FromUint64(2), "", "昵称B"),     // 无名片用昵称
		member(message.FromUint64(3), "", ""),        // 全空回退 QQ 号
		member(message.FromUint64(4), "", "已抽到"),     // 今日已抽到 → 剔除
	}
	drawn := map[string]bool{"4": true}
	got := filterCandidates(members, self, sender, drawn)
	want := []partner{
		{QQ: "1", Name: "群名片A"},
		{QQ: "2", Name: "昵称B"},
		{QQ: "3", Name: "QQ3"},
	}
	if len(got) != len(want) {
		t.Fatalf("候选 = %+v, 期望 %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("候选[%d] = %+v, 期望 %+v", i, got[i], want[i])
		}
	}
}

func TestFilterCandidatesExcludesEmptyQQ(t *testing.T) {
	self := message.FromUint64(100)
	got := filterCandidates([]message.GroupUserInfo{member(message.QID(""), "", "无名")}, self, message.QID(""), nil)
	if len(got) != 0 {
		t.Errorf("空 QQ 号应被剔除, 实际 %+v", got)
	}
}

func TestPickCandidate(t *testing.T) {
	if _, ok := pickCandidate(nil); ok {
		t.Error("空候选应返回 false")
	}
	candidates := []partner{{QQ: "1"}, {QQ: "2"}, {QQ: "3"}}
	got, ok := pickCandidate(candidates)
	if !ok {
		t.Fatal("非空候选应返回 true")
	}
	found := false
	for _, c := range candidates {
		if c == got {
			found = true
		}
	}
	if !found {
		t.Errorf("抽中结果 %+v 不在候选里", got)
	}
}

func TestMentionTargets(t *testing.T) {
	segs := []message.OB11Segment{
		seg("text", map[string]any{"text": "/强娶 "}),
		seg("at", map[string]any{"qq": "all"}),       // @全体 → 忽略
		seg("image", map[string]any{"url": "x.png"}), // 非 at 段
		seg("at", map[string]any{"qq": "qq:111"}),    // 有效目标
		seg("at", map[string]any{"qq": "qq:222"}),
	}
	got := mentionTargets(segs)
	if len(got) != 2 || got[0] != message.QID("qq:111") || got[1] != message.QID("qq:222") {
		t.Errorf("mentionTargets = %v, 期望 [qq:111 qq:222]", got)
	}
}

func TestMarryTarget(t *testing.T) {
	self := message.FromUint64(100)
	sender := message.FromUint64(200)

	cases := []struct {
		name     string
		segs     []message.OB11Segment
		wantQQ   string
		wantCode int
	}{
		{"没有任何@", []message.OB11Segment{seg("text", map[string]any{"text": "/强娶"})}, "", 0},
		{"只@全体", []message.OB11Segment{seg("at", map[string]any{"qq": "all"})}, "", 0},
		{"只@自己", []message.OB11Segment{seg("at", map[string]any{"qq": "qq:200"})}, "", 0},
		{"只@机器人", []message.OB11Segment{seg("at", map[string]any{"qq": "qq:100"})}, "", 1},
		{"机器人+目标", []message.OB11Segment{
			seg("at", map[string]any{"qq": "qq:100"}),
			seg("text", map[string]any{"text": " "}),
			seg("at", map[string]any{"qq": "qq:111"}),
		}, "qq:111", 2},
		{"多个目标取第一个", []message.OB11Segment{
			seg("at", map[string]any{"qq": "qq:333"}),
			seg("at", map[string]any{"qq": "qq:444"}),
		}, "qq:333", 2},
		{"自己不算目标", []message.OB11Segment{
			seg("at", map[string]any{"qq": "qq:200"}),
			seg("at", map[string]any{"qq": "qq:555"}),
		}, "qq:555", 2},
	}
	for _, c := range cases {
		got, code := marryTarget(c.segs, self, sender)
		if code != c.wantCode || (code == 2 && got != message.QID(c.wantQQ)) {
			t.Errorf("%s: marryTarget = (%q, %d), 期望 (%q, %d)", c.name, got, code, c.wantQQ, c.wantCode)
		}
	}
}

func TestAvatarURL(t *testing.T) {
	const want = "https://q1.qlogo.cn/g?b=qq&nk=123456&s=640"
	if got := avatarURL("123456"); got != want {
		t.Errorf("avatarURL = %s, 期望 %s", got, want)
	}
}

func TestMarryCooldownSec(t *testing.T) {
	const now = int64(1000000)
	cases := []struct {
		name        string
		last        int64
		cooldownMin int
		want        int64
	}{
		{"从未娶过", 0, 60, 0},
		{"不限制", now - 10, 0, 0},
		{"冷却已过", now - 3600, 60, 0},
		{"冷却中", now - 1800, 60, 1800},
		{"刚娶完", now - 5, 60, 60*60 - 5},
		{"时间倒流", now + 100, 60, 0},
	}
	for _, c := range cases {
		if got := marryCooldownSec(c.last, now, c.cooldownMin); got != c.want {
			t.Errorf("%s: marryCooldownSec = %d, 期望 %d", c.name, got, c.want)
		}
	}
}

func TestKindOf(t *testing.T) {
	cases := []struct {
		cmd      string
		wantKind partnerKind
	}{
		{"今日老婆", partnerKind{Label: "今日老婆", Pronoun: "她"}},
		{"今日老公", partnerKind{Label: "今日老公", Pronoun: "他"}},
		{"今日对象", partnerKind{Label: "今日对象", Pronoun: "TA"}},
		{"随便", partnerKind{Label: "今日老婆", Pronoun: "她"}},
	}
	for _, c := range cases {
		if got := kindOf(c.cmd); got != c.wantKind {
			t.Errorf("kindOf(%q) = %+v, 期望 %+v", c.cmd, got, c.wantKind)
		}
	}
}

func TestPickMutual(t *testing.T) {
	// 基础状态：1 抽到 2（At=200），3 也抽到 2（At=100，更早），2 自己抽到 4，5 抽到 6
	base := []memberState{
		{Owner: "1", State: drawState{Date: "2026-09-16", Partners: []partner{{QQ: "2", Name: "乙", At: 200}}}},
		{Owner: "2", State: drawState{Date: "2026-09-16", Partners: []partner{{QQ: "4", Name: "丁", At: 50}}}},
		{Owner: "3", State: drawState{Date: "2026-09-16", Partners: []partner{{QQ: "2", Name: "乙", At: 100}}}},
		{Owner: "5", State: drawState{Date: "2026-09-16", Partners: []partner{{QQ: "6", Name: "己", At: 300}}}},
	}

	// 2 抽取：1 和 3 都抽到过 2，取最早抽到他的 3（At=100）
	if qq, at, ok := pickMutual("2", base, nil); !ok || qq != "3" || at != 100 {
		t.Errorf("多人抽到时应取最早的: got (%q, %d, %v), 期望 (3, 100, true)", qq, at, ok)
	}

	// 6 抽取：只有 5 抽到过 6
	if qq, _, ok := pickMutual("6", base, nil); !ok || qq != "5" {
		t.Errorf("单一配对应命中: got (%q, %v), 期望 5", qq, ok)
	}

	// 4 抽取：2 抽到过 4（At=50）
	if qq, at, ok := pickMutual("4", base, nil); !ok || qq != "2" || at != 50 {
		t.Errorf("应命中抽到 4 的 2: got (%q, %d, %v)", qq, at, ok)
	}

	// 7 抽取：没人抽到过 7
	if qq, _, ok := pickMutual("7", base, nil); ok {
		t.Errorf("无人抽到时应返回 false: got %q", qq)
	}

	// 已抽到的人不再配对：2 已抽了 4，排除 2 后 4 无人可配
	if _, _, ok := pickMutual("4", base, map[string]bool{"2": true}); ok {
		t.Error("排除已配对对象后应返回 false")
	}

	// 排除最早的一位后取剩下的：2 抽取时排除 3，应轮到 1（At=200）
	if qq, at, ok := pickMutual("2", base, map[string]bool{"3": true}); !ok || qq != "1" || at != 200 {
		t.Errorf("排除 3 后应轮到 1: got (%q, %d, %v)", qq, at, ok)
	}

	// 时间相同（旧数据 At=0）取键序靠前者：states 须已按键排序
	tie := []memberState{
		{Owner: "7", State: drawState{Date: "2026-09-16", Partners: []partner{{QQ: "8"}}}},
		{Owner: "9", State: drawState{Date: "2026-09-16", Partners: []partner{{QQ: "8"}}}},
	}
	if qq, _, ok := pickMutual("8", tie, nil); !ok || qq != "7" {
		t.Errorf("时间相同时应取键序靠前的 7: got (%q, %v)", qq, ok)
	}
}

func TestOwnerOfDrawKey(t *testing.T) {
	cases := []struct{ key, want string }{
		{"d:888:123", "123"},
		{"d:888:", ""},
		{"nocolon", ""},
	}
	for _, c := range cases {
		if got := ownerOfDrawKey(c.key); got != c.want {
			t.Errorf("ownerOfDrawKey(%q) = %q, 期望 %q", c.key, got, c.want)
		}
	}
}

func TestDrawKeyPrefix(t *testing.T) {
	if got, want := drawKeyPrefix(message.FromUint64(888)), "d:888:"; got != want {
		t.Errorf("drawKeyPrefix = %s, 期望 %s", got, want)
	}
}

func TestDrawAndMarryKeys(t *testing.T) {
	group := message.FromUint64(888)
	user := message.FromUint64(123)
	if got, want := drawKey(group, user), "d:888:123"; got != want {
		t.Errorf("drawKey = %s, 期望 %s", got, want)
	}
	if got, want := marryKey(group, user), "m:888:123"; got != want {
		t.Errorf("marryKey = %s, 期望 %s", got, want)
	}
}

func TestHumanWait(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{30, "30 秒"},
		{60, "1 分钟"},
		{119, "2 分钟"},
		{3600, "1 小时"},
		{7200, "2 小时"},
	}
	for _, c := range cases {
		if got := humanWait(c.sec); got != c.want {
			t.Errorf("humanWait(%d) = %s, 期望 %s", c.sec, got, c.want)
		}
	}
}

func TestDrawnSet(t *testing.T) {
	st := drawState{Partners: []partner{{QQ: "1"}, {QQ: "2"}}}
	set := st.drawnSet()
	if !set["1"] || !set["2"] || set["3"] {
		t.Errorf("drawnSet = %v, 期望含 1、2 不含 3", set)
	}
}
