package videoparse

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestClassifyLink(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"抖音短链", "https://v.douyin.com/eximqs9eRIY/", platformDouyin},
		{"抖音短链无斜杠", "https://v.douyin.com/eximqs9eRIY", platformDouyin},
		{"抖音视频页", "https://www.douyin.com/video/7126745726494821640", platformDouyin},
		{"抖音图文页", "https://www.douyin.com/note/7126745726494821640", platformDouyin},
		{"快手短链", "https://v.kuaishou.com/K2cussRN", platformKuaishou},
		{"快手作品页", "https://www.kuaishou.com/short-video/3xmvunesqmup5i6", platformKuaishou},
		{"快手分享页", "https://v.m.chenzhongtech.com/fw/photo/3xmvunesqmup5i6", platformKuaishou},
		{"B站BV号", "https://www.bilibili.com/video/BV1PWpn6oEmK/?share_source=copy_web", platformBilibili},
		{"B站短链", "https://b23.tv/abcdefg", platformBilibili},
		{"B站av号", "https://www.bilibili.com/video/av170001", platformBilibili},
		{"B站主页不支持", "https://space.bilibili.com/123456", ""},
		{"B站直播间不支持", "https://live.bilibili.com/12345", ""},
		{"抖音主页不支持", "https://www.douyin.com/user/MS4wLjABAAAA", ""},
		{"快手主页不支持", "https://www.kuaishou.com/profile/3xmvunesqmup5i6", ""},
		{"其它站点不支持", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", ""},
		{"缺主机名不支持", "https:///video/BV1PWpn6oEmK", ""},
		{"非法链接不支持", "not-a-url", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLink(tc.url); got != tc.want {
				t.Errorf("classifyLink(%q) = %q, 期望 %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestDetectLinksFromShareText(t *testing.T) {
	// 真实分享文案：链接前后带 emoji、全角空格与中文说明
	text := "3.02 :2pm V@l.cn wfo:/ 07/20 雪豹原来还有这么多我们不知道的秘密！ " +
		"https://v.douyin.com/eximqs9eRIY/ 复制此链接，打开Dou音搜索，直接观看视频！"
	links := detectLinks(text)
	if len(links) != 1 {
		t.Fatalf("期望识别 1 条链接，实际 %d 条：%+v", len(links), links)
	}
	if links[0].Platform != platformDouyin {
		t.Errorf("平台判定错误：%q", links[0].Platform)
	}
	if links[0].URL != "https://v.douyin.com/eximqs9eRIY/" {
		t.Errorf("链接还原错误：%q", links[0].URL)
	}
}

func TestDetectLinksTrailingPunctuation(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"看看这个 https://v.douyin.com/abc123。", "https://v.douyin.com/abc123"},
		{"分享 https://b23.tv/xyz），很不错", "https://b23.tv/xyz"},
		{"链接 https://v.kuaishou.com/K2cussRN, 谢谢", "https://v.kuaishou.com/K2cussRN"},
		{"<https://www.bilibili.com/video/BV1PWpn6oEmK/>", "https://www.bilibili.com/video/BV1PWpn6oEmK/"},
	}
	for _, tc := range cases {
		links := detectLinks(tc.text)
		if len(links) != 1 {
			t.Fatalf("文本 %q 期望 1 条链接，实际 %d 条", tc.text, len(links))
		}
		if links[0].URL != tc.want {
			t.Errorf("文本 %q 尾部分隔符未裁净：得到 %q，期望 %q", tc.text, links[0].URL, tc.want)
		}
	}
}

func TestDetectLinksDeduplicatesSameVideo(t *testing.T) {
	// 同一视频的两种 B 站分享链接（BV 号相同，追踪参数不同）只保留一条
	text := "https://www.bilibili.com/video/BV1PWpn6oEmK/?share_source=copy_web&vd_source=abc " +
		"https://www.bilibili.com/video/BV1PWpn6oEmK?spm_id_from=333.999"
	links := detectLinks(text)
	if len(links) != 1 {
		t.Fatalf("同一视频应去重为 1 条，实际 %d 条：%+v", len(links), links)
	}
}

func TestDetectLinksSkipsCQCodeAndUnsupported(t *testing.T) {
	cases := []string{
		"[CQ:image,file=http://example.com/a.jpg,url=https://v.douyin.com/eximqs9eRIY/]",
		"https://www.youtube.com/watch?v=abc",
		"普通聊天，没有任何链接",
		"",
	}
	for _, text := range cases {
		if links := detectLinks(stripCQCode(text)); len(links) != 0 {
			t.Errorf("文本 %q 不应识别出链接，实际 %+v", text, links)
		}
	}
}

func TestDetectLinksMixedPlatforms(t *testing.T) {
	text := "抖音 https://v.douyin.com/eximqs9eRIY/ 快手 https://v.kuaishou.com/K2cussRN " +
		"B站 https://www.bilibili.com/video/BV1PWpn6oEmK/"
	links := detectLinks(text)
	if len(links) != 3 {
		t.Fatalf("期望 3 条链接，实际 %d 条：%+v", len(links), links)
	}
	got := []string{links[0].Platform, links[1].Platform, links[2].Platform}
	want := []string{platformDouyin, platformKuaishou, platformBilibili}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条平台 = %q，期望 %q", i+1, got[i], want[i])
		}
	}
}

func TestBilibiliVideoID(t *testing.T) {
	cases := []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://www.bilibili.com/video/BV1PWpn6oEmK/?share_source=copy_web", "BV1PWpn6oEmK", true},
		{"https://www.bilibili.com/video/av170001", "av170001", true},
		{"https://b23.tv/abc123", "b23:abc123", true},
		{"https://space.bilibili.com/123", "", false},
	}
	for _, tc := range cases {
		got, ok := bilibiliVideoID(tc.url)
		if ok != tc.ok || got != tc.want {
			t.Errorf("bilibiliVideoID(%q) = (%q,%v)，期望 (%q,%v)", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}

func TestKuaishouPhotoID(t *testing.T) {
	cases := map[string]string{
		"/short-video/3xmvunesqmup5i6":   "3xmvunesqmup5i6",
		"/fw/photo/3xmvunesqmup5i6":      "3xmvunesqmup5i6",
		"/fw/long-video/3xh9xf45h97htrk": "3xh9xf45h97htrk",
		"/K2cussRN":                      "",
	}
	for path, want := range cases {
		if got := kuaishouPhotoID(path); got != want {
			t.Errorf("kuaishouPhotoID(%q) = %q，期望 %q", path, got, want)
		}
	}
}

func TestShortCode(t *testing.T) {
	cases := map[string]string{
		"/eximqs9eRIY":  "eximqs9eRIY",
		"/eximqs9eRIY/": "eximqs9eRIY",
		"/abcdefg":      "abcdefg",
		"/a/b":          "",
		"/index.html":   "",
		"/中文":           "",
		"/":             "",
	}
	for path, want := range cases {
		if got := shortCode(path); got != want {
			t.Errorf("shortCode(%q) = %q，期望 %q", path, got, want)
		}
	}
}

func TestNormalizeGroupIDs(t *testing.T) {
	set := normalizeGroupIDs([]string{"123456", "qq:789", "tg:-100999", "  ", ""})
	for _, want := range []string{"qq:123456", "qq:789", "tg:-100999"} {
		if _, ok := set[want]; !ok {
			t.Errorf("生效群集合缺少 %q：%+v", want, set)
		}
	}
	if len(set) != 3 {
		t.Errorf("空项不应进入集合，实际 %+v", set)
	}
}

// testLogger 测试用 logger：丢弃输出。
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestPlugin 构造完成基本初始化的插件实例（不启动框架、不联网）。
func newTestPlugin(t *testing.T) *VideoParsePlugin {
	t.Helper()
	p := NewPlugin()
	p.Logger = testLogger(t)
	p.ctx = context.Background()
	p.sem = make(chan struct{}, parseConcurrency)
	p.seen = make(map[string]time.Time)
	p.inflight = make(map[string]struct{})
	p.cooldown = make(map[string]time.Time)
	p.cfg.Enable = true
	p.cfg.AutoParse = true
	p.cfg.MaxSizeMB = 200
	p.cfg.SendTitle = true
	p.cfg.APIURL = defaultAPIURL
	p.apiInterval = 0 // 测试不节流
	p.tempDirOverride = t.TempDir()
	return p
}
