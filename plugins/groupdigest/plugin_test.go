package groupdigest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
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
	for i := 0; i < 99; i++ {
		st.add(digestMessage{Text: "x"}, 200)
	}
	if got := st.tryTrigger(100, 0, time.Now()); got != nil {
		t.Fatalf("未达阈值不应触发")
	}
	st.add(digestMessage{Text: "第100条"}, 200)
	got := st.tryTrigger(100, 0, time.Now())
	if len(got) != 100 {
		t.Fatalf("达到阈值应领取 100 条，实际 %d", len(got))
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
	now := time.Date(2026, 9, 1, 10, 30, 0, 0, time.Local)
	ps := persistedState{
		Count: 42,
		Messages: []digestMessage{
			{Time: now, Nickname: "张三", Text: "今天天气不错"},
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
	if !got.Messages[0].Time.Equal(now) || got.Messages[0].Nickname != "张三" {
		t.Errorf("消息字段往返不一致: %+v", got.Messages[0])
	}
	if !got.LastGen.Equal(ps.LastGen) {
		t.Errorf("最近生成时间往返不一致: %v", got.LastGen)
	}
}

func TestGroupStateRestore(t *testing.T) {
	store := newFakePersistent()
	store.SetString(context.Background(), "g:qq:1", `{"count":7,"messages":[{"time":"2026-09-01T10:00:00+08:00","nickname":"王五","text":"测试"}],"last_gen":"2026-09-01T09:00:00+08:00"}`)

	st := &groupState{}
	st.ensureLoaded(store, "qq:1")
	if st.count != 7 || len(st.messages) != 1 || st.messages[0].Nickname != "王五" {
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
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("你好世界", 4); got != "你好世界" {
		t.Errorf("未超长不应截断: %q", got)
	}
	if got := truncateRunes("你好世界", 2); got != "你好…" {
		t.Errorf("超长应截断并加省略号: %q", got)
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

// TestOnUnload 卸载钩子：shutdown（退出/重启）保留缓存，uninstall（市场卸载）
// 清空内存与持久化缓存，且清理后不再写回。
func TestOnUnload(t *testing.T) {
	p := NewPlugin()
	p.cfg.Enable = true
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	store := newFakePersistent()
	p.store = store
	ctx := context.Background()

	st := &groupState{count: 3, messages: []digestMessage{{Nickname: "张三", Text: "你好"}}}
	p.states.Store("qq:100", st)
	if !store.SetString(ctx, "g:qq:100", `{"count":3}`) {
		t.Fatal("准备持久化缓存失败")
	}

	// shutdown：不清理
	if err := p.OnUnload(ctx, plugin.UnloadShutdown); err != nil {
		t.Fatalf("shutdown 卸载不应报错: %v", err)
	}
	if !store.Has(ctx, "g:qq:100") {
		t.Fatal("shutdown 不应清理持久化缓存")
	}

	// uninstall：清理内存与持久层
	if err := p.OnUnload(ctx, plugin.UnloadUninstall); err != nil {
		t.Fatalf("uninstall 卸载不应报错: %v", err)
	}
	if store.Has(ctx, "g:qq:100") {
		t.Fatal("uninstall 应清理持久化缓存")
	}
	if _, ok := p.states.Load("qq:100"); ok {
		t.Fatal("uninstall 应清空内存缓存")
	}

	// 清理后事件不再收集、也不写回
	got, err := p.OnGroupMsg(ctx, nil, command.Command{}, message.Message{GroupId: message.FromString("100")})
	if err != nil || !got {
		t.Fatalf("卸载后消息事件应放行: got=%v err=%v", got, err)
	}
	p.persistState("qq:100", st)
	if store.Has(ctx, "g:qq:100") {
		t.Fatal("清理后不应再写回缓存")
	}
}
