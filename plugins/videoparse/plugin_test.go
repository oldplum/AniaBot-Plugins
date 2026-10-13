package videoparse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

func TestClaimDeduplicatesWithinWindow(t *testing.T) {
	p := newTestPlugin(t)
	links := []detectedLink{{Platform: platformDouyin, URL: "https://v.douyin.com/abc123/"}}

	first := p.claim("qq:100", links)
	if len(first) != 1 {
		t.Fatalf("首次应认领成功，实际 %d 条", len(first))
	}
	// 同一会话、同一链接：去重窗口内不再认领
	if second := p.claim("qq:100", links); len(second) != 0 {
		t.Errorf("重复链接应被去重，实际 %d 条", len(second))
	}
	// 会话内连发不同链接：仍受冷却限制（避免连发刷屏）
	other := []detectedLink{{Platform: platformBilibili, URL: "https://www.bilibili.com/video/BV1PWpn6oEmK/"}}
	if got := p.claim("qq:100", other); len(got) != 0 {
		t.Errorf("冷却窗口内不应连发第二个视频，实际 %d 条", len(got))
	}
	// 另一个会话发布同一链接：各群互不影响，应正常解析
	if got := p.claim("qq:200", links); len(got) != 1 {
		t.Errorf("其他会话应能独立解析同一链接，实际 %d 条", len(got))
	}
}

func TestClaimSkipsInflight(t *testing.T) {
	p := newTestPlugin(t)
	link := detectedLink{Platform: platformBilibili, URL: "https://www.bilibili.com/video/BV1PWpn6oEmK/"}
	key := "qq:100|" + dedupeKey(link.Platform, link.URL)

	p.mu.Lock()
	p.inflight[key] = struct{}{}
	delete(p.cooldown, "qq:100") // 确保拦下它的是 inflight 而不是冷却
	p.mu.Unlock()

	if got := p.claim("qq:100", []detectedLink{link}); len(got) != 0 {
		t.Errorf("处理中的链接不应被重复认领，实际 %d 条", len(got))
	}
}

func TestClaimRespectsClosedGate(t *testing.T) {
	p := newTestPlugin(t)
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	if got := p.claim("qq:100", []detectedLink{{Platform: platformDouyin, URL: "https://v.douyin.com/a/"}}); len(got) != 0 {
		t.Errorf("卸载后不应认领新任务，实际 %d 条", len(got))
	}
}

func TestSweepStateExpiresEntries(t *testing.T) {
	p := newTestPlugin(t)
	stale := time.Now().Add(-sameLinkTTL - time.Minute)
	p.mu.Lock()
	p.seen["qq:1|k1"] = stale
	p.seen["qq:1|k2"] = time.Now()
	p.cooldown["qq:1"] = time.Now().Add(-time.Second)
	p.cooldown["qq:2"] = time.Now().Add(time.Minute)
	p.mu.Unlock()

	p.sweepState()

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.seen["qq:1|k1"]; ok {
		t.Error("过期的去重记录应被清理")
	}
	if _, ok := p.seen["qq:1|k2"]; !ok {
		t.Error("未过期的去重记录不应被清理")
	}
	if _, ok := p.cooldown["qq:1"]; ok {
		t.Error("过期的冷却记录应被清理")
	}
	if _, ok := p.cooldown["qq:2"]; !ok {
		t.Error("未过期的冷却记录不应被清理")
	}
}

func TestOnUnloadUninstallClearsState(t *testing.T) {
	p := newTestPlugin(t)
	if err := os.MkdirAll(p.tempDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.seen["a"] = time.Now()
	p.inflight["a"] = struct{}{}
	p.cooldown["a"] = time.Now()
	p.mu.Unlock()

	if err := p.OnUnload(context.Background(), plugin.UnloadUninstall); err != nil {
		t.Fatalf("卸载返回错误：%v", err)
	}
	if !p.isClosed() {
		t.Error("卸载后闸门应关闭")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) != 0 || len(p.inflight) != 0 || len(p.cooldown) != 0 {
		t.Error("卸载后内存状态应清空")
	}
}

func TestIsParseCommand(t *testing.T) {
	p := newTestPlugin(t)
	cases := []struct {
		name    string
		cmd     string
		mention bool
		group   bool
		want    bool
	}{
		{"群聊 at + /解析", cmdParse, true, true, true},
		{"群聊 at + /parse", cmdParseEN, true, true, true},
		{"群聊 at + 中文别名", cmdParseAlt, true, true, true},
		{"群聊未 at", cmdParse, false, true, false},
		{"私聊无需 at", cmdParse, false, false, true},
		{"其它命令", "点歌", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := command.Command{Name: tc.cmd, Mention: tc.mention}
			if got := p.isParseCommand(cmd, tc.group); got != tc.want {
				t.Errorf("isParseCommand = %v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestTitleLine(t *testing.T) {
	p := newTestPlugin(t)
	info := &videoInfo{
		Platform: platformDouyin,
		Title:    "雪豹原来还有这么多我们不知道的秘密",
		Author:   "动物之星频道",
		Duration: 596.434,
	}
	want := "【抖音】雪豹原来还有这么多我们不知道的秘密 - 动物之星频道（9:56）"
	if got := p.titleLine(info); got != want {
		t.Errorf("titleLine = %q，期望 %q", got, want)
	}

	p.cfg.SendTitle = false
	if got := p.titleLine(info); got != "" {
		t.Errorf("关闭标题后应返回空串，得到 %q", got)
	}

	p.cfg.SendTitle = true
	if got := p.titleLine(&videoInfo{Platform: platformKuaishou}); got != "" {
		t.Errorf("无标题时应返回空串，得到 %q", got)
	}
}

func TestTitleLineWithoutAuthor(t *testing.T) {
	p := newTestPlugin(t)
	got := p.titleLine(&videoInfo{Platform: platformBilibili, Title: "标题"})
	if got != "【哔哩哔哩】标题" {
		t.Errorf("无作者时不应出现分隔符：%q", got)
	}
}

func TestChatID(t *testing.T) {
	p := newTestPlugin(t)
	msg := message.Message{
		GroupId: message.FromString("123456"),
		Sender:  message.MessageSender{UserId: message.FromString("654321")},
	}
	if got := p.chatID(msg, true); got != "qq:123456" {
		t.Errorf("群聊会话 ID = %q", got)
	}
	if got := p.chatID(msg, false); got != "qq:654321" {
		t.Errorf("私聊会话 ID = %q", got)
	}
}

func TestKuaishouCandidates(t *testing.T) {
	got := kuaishouCandidates("https://www.kuaishou.com/short-video/3xmvunesqmup5i6")
	if len(got) != 2 {
		t.Fatalf("应生成移动端页与原链接两个候选，实际 %+v", got)
	}
	if got[0] != "https://v.m.chenzhongtech.com/fw/photo/3xmvunesqmup5i6" {
		t.Errorf("首个候选应为移动端分享页：%q", got[0])
	}
	short := kuaishouCandidates("https://v.kuaishou.com/K2cussRN")
	if len(short) != 1 || short[0] != "https://v.kuaishou.com/K2cussRN" {
		t.Errorf("短链候选错误：%+v", short)
	}
}

func TestStartDisablesAPIWhenConfiguredOff(t *testing.T) {
	p := NewPlugin()
	p.Logger = testLogger(t)
	p.cfg.APIURL = "off"

	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start 报错：%v", err)
	}
	if !p.apiDisabled {
		t.Error("api_url=off 时应关闭接口调用")
	}
	if p.cfg.APIURL != "" {
		t.Errorf("关闭后接口地址应清空，实际 %q", p.cfg.APIURL)
	}

	// 关闭接口后：抖音无本地兜底，应明确报错
	if _, err := p.resolve(context.Background(), detectedLink{Platform: platformDouyin, URL: "https://v.douyin.com/a/"}, ""); err == nil {
		t.Error("接口关闭时抖音解析应报错")
	}
}

func TestStartDefaultsAPIURL(t *testing.T) {
	p := NewPlugin()
	p.Logger = testLogger(t)
	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start 报错：%v", err)
	}
	if p.cfg.APIURL != defaultAPIURL {
		t.Errorf("默认接口地址 = %q，期望 %q", p.cfg.APIURL, defaultAPIURL)
	}
	if p.ctx == nil || p.sem == nil {
		t.Error("Start 应初始化上下文与信号量")
	}
}

func TestWaitAPISlotTimesOutWithContext(t *testing.T) {
	p := newTestPlugin(t)
	p.apiInterval = time.Minute
	p.apiMu.Lock()
	p.apiLast = time.Now()
	p.apiMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.waitAPISlot(ctx); err == nil {
		t.Error("上下文超时时应返回错误而不是无限等待")
	}
}
