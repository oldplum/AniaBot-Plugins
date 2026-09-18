package groupdashboard

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/storage"
)

func TestNormalizeGroupIDs(t *testing.T) {
	set := normalizeGroupIDs([]string{"123456", "qq:789", " fs:oc_abc ", "", "tg:-100123"})
	for _, want := range []string{"qq:123456", "qq:789", "fs:oc_abc", "tg:-100123"} {
		if _, ok := set[want]; !ok {
			t.Errorf("缺少群 ID %s（实际 %v）", want, set)
		}
	}
	if len(set) != 4 {
		t.Errorf("期望 4 个群，实际 %d", len(set))
	}
}

func TestGroupStateTrigger(t *testing.T) {
	st := &groupState{}
	for i := 0; i < 119; i++ {
		st.add(digestMessage{Text: "x"}, 200)
	}
	if got := st.tryTrigger(120, 0, time.Now()); got != nil {
		t.Fatalf("未达阈值不应触发")
	}
	st.add(digestMessage{Text: "第120条"}, 200)
	got := st.tryTrigger(120, 0, time.Now())
	if len(got) != 120 {
		t.Fatalf("达到阈值应领取 120 条，实际 %d", len(got))
	}
	if st.count != 0 {
		t.Errorf("触发后计数应重置，实际 %d", st.count)
	}
	// 生成中不重复触发
	if got := st.tryTrigger(1, 0, time.Now()); got != nil {
		t.Errorf("生成中不应重复触发")
	}
	st.finish(time.Now())
	// 冷却期内不触发
	st.add(digestMessage{Text: "y"}, 200)
	if got := st.tryTrigger(1, 10*time.Minute, time.Now()); got != nil {
		t.Errorf("冷却期内不应触发")
	}
	// 冷却结束后触发
	if got := st.tryTrigger(1, 10*time.Minute, time.Now().Add(11*time.Minute)); len(got) != 1 {
		t.Errorf("冷却结束后应触发并领取 1 条，实际 %d", len(got))
	}
}

func TestRenderMessageText(t *testing.T) {
	msg := message.Message{
		Sender: message.MessageSender{UserId: message.FromString("10001")},
		Message: []message.OB11Segment{
			{Type: message.SegmentText, Data: map[string]any{"text": "你好"}},
			{Type: message.SegmentImage, Data: map[string]any{"file": "base64://aGk=", "url": "base64://aGk="}},
			{Type: message.SegmentMention, Data: map[string]any{"qq": "all"}},
		},
	}
	got := renderMessageText(msg)
	if got != "你好[图片][at:全体成员]" {
		t.Errorf("渲染结果不符: %q", got)
	}
}

func TestPersistedStateRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 30, 0, 0, time.Local)
	ps := persistedState{
		Count: 42,
		Messages: []digestMessage{
			{Time: now, UserID: "qq:10001", Nickname: "张三", Text: "今天天气不错"},
			{Time: now.Add(time.Minute), Nickname: "李四", Text: "晚上一起吃饭？"},
		},
		LastGen: now.Add(-time.Hour),
	}
	data, err := json.Marshal(&ps)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got persistedState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if got.Count != ps.Count || len(got.Messages) != len(ps.Messages) {
		t.Fatalf("往返不一致: %+v", got)
	}
	if got.Messages[0].UserID != "qq:10001" || got.Messages[0].Nickname != "张三" {
		t.Errorf("消息字段往返不一致: %+v", got.Messages[0])
	}
	if !got.LastGen.Equal(ps.LastGen) {
		t.Errorf("最近生成时间往返不一致: %v", got.LastGen)
	}
}

func TestGroupStateRestore(t *testing.T) {
	store := newFakePersistent()
	store.SetString(context.Background(), "g:qq:1", `{"count":7,"messages":[{"time":"2026-09-18T10:00:00+08:00","user_id":"qq:10002","nickname":"王五","text":"测试"}],"last_gen":"2026-09-18T09:00:00+08:00"}`)

	st := &groupState{}
	st.ensureLoaded(store, "qq:1")
	if st.count != 7 || len(st.messages) != 1 || st.messages[0].Nickname != "王五" || st.messages[0].UserID != "qq:10002" {
		t.Fatalf("从持久层恢复失败: %+v", st)
	}
	if st.lastGen.IsZero() {
		t.Errorf("最近生成时间未恢复")
	}
	// 重复调用不应重复加载（幂等）
	st.count = 99
	st.ensureLoaded(store, "qq:1")
	if st.count != 99 {
		t.Errorf("ensureLoaded 应只执行一次，计数被覆盖: %d", st.count)
	}
	// 兼容没有 user_id 的旧数据
	store.SetString(context.Background(), "g:qq:2", `{"count":1,"messages":[{"time":"2026-09-18T10:00:00+08:00","nickname":"老数据","text":"无ID"}]}`)
	st2 := &groupState{}
	st2.ensureLoaded(store, "qq:2")
	if len(st2.messages) != 1 || st2.messages[0].UserID != "" {
		t.Errorf("旧数据（无 user_id）应能恢复: %+v", st2.messages)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("你好世界", 4); got != "你好世界" {
		t.Errorf("未超长不应截断: %q", got)
	}
	if got := truncateRunes("你好世界", 2); got != "你好…" {
		t.Errorf("超长应截断并加省略号: %q", got)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"纯 JSON", `{"a":1}`, `{"a":1}`},
		{"带围栏", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"围栏无语言", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"前后夹说明", "好的，以下是报告：\n{\"a\":{\"b\":2}}\n希望对你有帮助", `{"a":{"b":2}}`},
		{"没有 JSON", "抱歉我不会", ""},
	}
	for _, c := range cases {
		if got := extractJSON(c.in); got != c.want {
			t.Errorf("%s: extractJSON(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestParseReportJSON(t *testing.T) {
	valid := `{"topics":[{"title":"晚饭","summary":"吃什么","quotes":[{"nickname":"张三","text":"吃火锅"}],"tags":["美食"]}],
		"members":[{"nickname":"张三","title":"干饭王","impression":"顿顿不落","tags":["吃"]}],
		"quotes":[{"nickname":"张三","text":"干饭不积极思想有问题","comment":"精辟"}],
		"atmosphere":{"summary":"欢乐","dimensions":[{"name":"活跃度","score":88,"comment":"很高"}],"advice":"多喝水"},
		"editor_comment":"明天见"}`
	report, err := parseReportJSON(valid)
	if err != nil {
		t.Fatalf("合法报告解析失败: %v", err)
	}
	if len(report.Topics) != 1 || report.Topics[0].Title != "晚饭" || report.Atmosphere.Dimensions[0].Score != 88 {
		t.Errorf("报告字段不符: %+v", report)
	}
	// 带围栏与说明文字也能解析
	if _, err := parseReportJSON("```json\n" + valid + "\n```"); err != nil {
		t.Errorf("围栏 JSON 解析失败: %v", err)
	}
	// 空报告应报错
	if _, err := parseReportJSON(`{"topics":[],"members":[],"quotes":[]}`); err == nil {
		t.Errorf("空报告不应解析成功")
	}
	if _, err := parseReportJSON("不是 JSON"); err == nil {
		t.Errorf("非法输出不应解析成功")
	}
}

func TestBuildStats(t *testing.T) {
	base := time.Date(2026, 9, 18, 21, 0, 0, 0, time.Local)
	msgs := []digestMessage{
		{Time: base, UserID: "qq:10001", Nickname: "张三", Text: "晚上好"},
		{Time: base.Add(30 * time.Minute), UserID: "qq:10001", Nickname: "张三", Text: "吃饭没"},
		{Time: base.Add(40 * time.Minute), UserID: "qq:10002", Nickname: "李四", Text: "吃了"},
		{Time: base.Add(50 * time.Minute), Nickname: "匿名", Text: "潜水围观"}, // 无 UserID
	}
	s := buildStats(msgs)
	if s.Total != 4 {
		t.Errorf("消息总量不符: %d", s.Total)
	}
	if s.Participants != 3 {
		t.Errorf("参与人数应为 3（两条同人不重复计），实际 %d", s.Participants)
	}
	if s.Chars != 3+3+2+4 {
		t.Errorf("字符总数不符: %d", s.Chars)
	}
	if s.Hours[21] != 4 {
		t.Errorf("21 点消息数不符: %d", s.Hours[21])
	}
	if s.MaxHour != 4 {
		t.Errorf("峰值不符: %d", s.MaxHour)
	}
	if s.NickCounts["张三"] != 2 {
		t.Errorf("张三条数不符: %d", s.NickCounts["张三"])
	}
	if s.AvatarByNick["张三"] != "https://q1.qlogo.cn/g?nk=10001&s=100" {
		t.Errorf("张三头像不符: %q", s.AvatarByNick["张三"])
	}
	if _, ok := s.AvatarByNick["匿名"]; ok {
		t.Errorf("无 ID 用户不应有头像 URL")
	}
	if s.RangeText() != "21:00 - 21:50" {
		t.Errorf("同日时段文案不符: %q", s.RangeText())
	}
	// 跨天时段文案
	s2 := buildStats([]digestMessage{
		{Time: base, Nickname: "a", Text: "1"},
		{Time: base.Add(26 * time.Hour), Nickname: "a", Text: "2"},
	})
	if s2.RangeText() != "09-18 21:00 ~ 09-19 23:00" {
		t.Errorf("跨天时段文案不符: %q", s2.RangeText())
	}
}

func TestQQAvatarURL(t *testing.T) {
	if got := qqAvatarURL("qq:12345"); got != "https://q1.qlogo.cn/g?nk=12345&s=100" {
		t.Errorf("QQ 头像不符: %q", got)
	}
	for _, uid := range []string{"", "fs:oc_x", "tg:123", "qo:ABC", "qq:", "qq:12ab"} {
		if got := qqAvatarURL(uid); got != "" {
			t.Errorf("%q 不应有头像 URL，实际 %q", uid, got)
		}
	}
}

func TestNormalizeStyle(t *testing.T) {
	for in, want := range map[string]string{
		"":         styleMint,
		"mint":     styleMint,
		" MINT ":   styleMint,
		"magazine": styleMagazine,
		"Dark":     styleDark,
		"unknown":  styleMint,
	} {
		if got := normalizeStyle(in); got != want {
			t.Errorf("normalizeStyle(%q) = %q, want %q", in, got, want)
		}
	}
}

func sampleReport() *digestReport {
	return &digestReport{
		Topics: []reportTopic{{
			Title:   "晚饭吃什么",
			Summary: "围绕火锅与烧烤展开激烈讨论",
			Quotes:  []reportQuote{{Nickname: "张三", Text: "吃火锅"}},
			Tags:    []string{"美食", "日常"},
		}},
		Members: []reportMember{{
			Nickname:   "张三",
			Title:      "干饭王",
			Impression: "顿顿不落",
			Tags:       []string{"吃"},
		}},
		Quotes: []reportGoldenQuote{{
			Nickname: "张三",
			Text:     "干饭不积极思想有问题",
			Comment:  "精辟",
		}},
		Atmosphere: reportAtmosphere{
			Summary:    "整体欢乐",
			Dimensions: []reportDimension{{Name: "活跃度", Score: 88, Comment: "很高"}, {Name: "欢乐值", Score: 120, Comment: "爆表"}},
			Advice:     "多喝水",
		},
		EditorComment: "明天见",
	}
}

func sampleStats() *groupStats {
	base := time.Date(2026, 9, 18, 21, 0, 0, 0, time.Local)
	return buildStats([]digestMessage{
		{Time: base, UserID: "qq:10001", Nickname: "张三", Text: "晚上好"},
		{Time: base.Add(30 * time.Minute), UserID: "qq:10002", Nickname: "李四", Text: "吃了"},
	})
}

func TestRenderDashboardHTMLStyles(t *testing.T) {
	for _, style := range []string{styleMint, styleMagazine, styleDark} {
		html, err := renderDashboardHTML(sampleReport(), sampleStats(), "测试群", style)
		if err != nil {
			t.Fatalf("渲染 %s 风格失败: %v", style, err)
		}
		for _, want := range []string{"群聊日常分析看板", "24 小时活动", "话题焦点", "群友画像", "今日金句", "群聊氛围报告", "编辑寄语", "测试群", "干饭王", "1 条"} {
			if !strings.Contains(html, want) {
				t.Errorf("%s 风格缺少内容 %q", style, want)
			}
		}
	}
	// 各风格样式表确实不同
	mint, _ := renderDashboardHTML(sampleReport(), sampleStats(), "", styleMint)
	mag, _ := renderDashboardHTML(sampleReport(), sampleStats(), "", styleMagazine)
	dark, _ := renderDashboardHTML(sampleReport(), sampleStats(), "", styleDark)
	if mint == mag || mag == dark || mint == dark {
		t.Errorf("三种风格渲染结果不应完全相同")
	}
	if !strings.Contains(mag, "Noto Serif SC") || !strings.Contains(dark, "#0e1613") {
		t.Errorf("杂志/暗夜风格样式未注入")
	}
}

func TestRenderDashboardHTMLEscapes(t *testing.T) {
	report := sampleReport()
	report.Topics[0].Title = `<script>alert("x")</script>`
	html, err := renderDashboardHTML(report, sampleStats(), "", styleMint)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(html, "<script>alert") {
		t.Errorf("话题标题未被转义，存在注入风险")
	}
}

func TestRenderReportMarkdown(t *testing.T) {
	md := renderReportMarkdown(sampleReport(), sampleStats(), "测试群")
	for _, want := range []string{"# 群聊日常分析看板", "测试群", "话题焦点", "#01 晚饭吃什么", "群友画像", "干饭王", "今日金句", "干饭不积极思想有问题", "群聊氛围报告", "活跃度 88/100", "编辑寄语"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 缺少内容 %q", want)
		}
	}
	// 评分越界应被钳制到 100
	if !strings.Contains(md, "欢乐值 100/100") {
		t.Errorf("越界评分未钳制: %s", md)
	}
}

// fakePersistent 测试用内存持久化存储（模拟 storage.PersistentStorage）。
type fakePersistent struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakePersistent() *fakePersistent {
	return &fakePersistent{m: map[string]string{}}
}

func (f *fakePersistent) GetString(_ context.Context, key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	return v, ok
}

func (f *fakePersistent) SetString(_ context.Context, key, val string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = val
	return true
}

func (f *fakePersistent) Get(ctx context.Context, key string, out any) bool {
	v, ok := f.GetString(ctx, key)
	if !ok {
		return false
	}
	return json.Unmarshal([]byte(v), out) == nil
}

func (f *fakePersistent) Set(ctx context.Context, key string, val any) bool {
	data, err := json.Marshal(val)
	if err != nil {
		return false
	}
	return f.SetString(ctx, key, string(data))
}

func (f *fakePersistent) Has(ctx context.Context, key string) bool {
	_, ok := f.GetString(ctx, key)
	return ok
}

func (f *fakePersistent) Del(ctx context.Context, key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
	return true
}

func (f *fakePersistent) Keys(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.m {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (f *fakePersistent) Clear(_ context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m = map[string]string{}
	return true
}

func (f *fakePersistent) Clone(_ string) storage.PersistentStorage {
	// 测试用：共享同一份数据
	return f
}
