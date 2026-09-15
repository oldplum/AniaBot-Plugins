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
