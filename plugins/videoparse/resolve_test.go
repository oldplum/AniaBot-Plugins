package videoparse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 从真实快手分享页抓取并精简出的 INIT_STATE 片段：保留 mainMvUrls/caption/userName 结构与
// 一个容易干扰括号配对的转义字符串（p2spCode）。
const kuaishouFixture = `<html><script>window.INIT_STATE = {"tusjoh.0":` +
	`{"result":1},"tusjoh.1":{"photo":{"caption":"一口气看完《不可饶恕》，超大尺度反差电影 #电影解说","userName":"啊路观影",` +
	`"duration":685366,"videoId":"2141edd43a6f3f0f","p2spCode":"{\"fRsn\":0,\"fixOpt\":-1}","mainMvUrls":` +
	`[{"cdn":"txmov2.a.kwimgs.com","url":"https://txmov2.a.kwimgs.com/upic/2024/10/15/22/video.mp4?clientCacheKey=3xmvunesqmup5i6_b.mp4&tt=b"},` +
	`{"cdn":"tymov2.a.kwimgs.com","url":"https://tymov2.a.kwimgs.com/upic/2024/10/15/22/video.mp4?tt=b"}]}}}` +
	`</script><script>window.__USE_SSR__ = true</script></html>`

func TestExtractINITState(t *testing.T) {
	state, ok := extractINITState(kuaishouFixture)
	if !ok {
		t.Fatal("未能提取 INIT_STATE")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(state), &parsed); err != nil {
		t.Fatalf("提取出的 INIT_STATE 不是合法 JSON：%v", err)
	}
	if len(parsed) == 0 {
		t.Error("提取结果为空对象")
	}
}

func TestExtractINITStateMissing(t *testing.T) {
	if _, ok := extractINITState("<html><body>普通页面</body></html>"); ok {
		t.Error("页面中无 INIT_STATE 时不应返回成功")
	}
}

func TestParseKuaishouPage(t *testing.T) {
	info, err := parseKuaishouPage(kuaishouFixture)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	wantURL := "https://txmov2.a.kwimgs.com/upic/2024/10/15/22/video.mp4?clientCacheKey=3xmvunesqmup5i6_b.mp4&tt=b"
	if info.MediaURL != wantURL {
		t.Errorf("直链取错：得到 %q，期望 %q", info.MediaURL, wantURL)
	}
	if info.Title != "一口气看完《不可饶恕》，超大尺度反差电影 #电影解说" {
		t.Errorf("标题解析错误：%q", info.Title)
	}
	if info.Author != "啊路观影" {
		t.Errorf("作者解析错误：%q", info.Author)
	}
}

func TestParseKuaishouPageAtlas(t *testing.T) {
	// 图集作品：INIT_STATE 存在但没有 mainMvUrls
	html := `<script>window.INIT_STATE = {"tusjoh.0":{"photo":{"caption":"图集","atlas":["https://x/a.jpg"]}}}</script>`
	if _, err := parseKuaishouPage(html); err == nil {
		t.Error("图集作品应返回错误提示")
	}
}

func TestParseKuaishouPageNoState(t *testing.T) {
	if _, err := parseKuaishouPage("<html></html>"); err == nil {
		t.Error("缺少 INIT_STATE 时应返回错误")
	}
}

func TestFindJSONString(t *testing.T) {
	text := `{"caption":"标题含\"引号\"","userName":"作者","nested":{"caption":"内层"}}`
	got, ok := findJSONString(text, "caption")
	if !ok || got != `标题含"引号"` {
		t.Errorf("caption = (%q,%v)", got, ok)
	}
	if got, ok := findJSONString(text, "userName"); !ok || got != "作者" {
		t.Errorf("userName = (%q,%v)", got, ok)
	}
	if _, ok := findJSONString(text, "missing"); ok {
		t.Error("不存在的键不应匹配")
	}
	// 值是数字时不应误报为字符串
	if _, ok := findJSONString(`{"duration":685366}`, "duration"); ok {
		t.Error("非字符串值不应被当成字符串返回")
	}
}

func TestFindJSONStringIgnoresKeySuffix(t *testing.T) {
	text := `{"mycaption":"错的","caption":"对的"}`
	if got, ok := findJSONString(text, "caption"); !ok || got != "对的" {
		t.Errorf("键名后缀干扰：得到 (%q,%v)", got, ok)
	}
}

func TestFirstURLAfterKey(t *testing.T) {
	text := `{"mainMvUrls":[{"cdn":"a","url":"https://a/video.mp4"},{"url":"https://b/video.mp4"}],"other":1}`
	got, ok := firstURLAfterKey(text, kuaishouMainMvKey)
	if !ok || got != "https://a/video.mp4" {
		t.Errorf("firstURLAfterKey = (%q,%v)", got, ok)
	}
	if _, ok := firstURLAfterKey(text, `"noSuchKey"`); ok {
		t.Error("不存在的键不应返回值")
	}
	// 非 http 值的 url 字段应被跳过
	skip := `{"mainMvUrls":[{"url":"ftp://x"},{"url":"https://good/v.mp4"}]}`
	if got, ok := firstURLAfterKey(skip, kuaishouMainMvKey); !ok || got != "https://good/v.mp4" {
		t.Errorf("非 http 值未跳过：得到 (%q,%v)", got, ok)
	}
}

// ---- 解析接口响应处理 ----

func TestAPIResultToInfoSuccess(t *testing.T) {
	p := newTestPlugin(t)
	resp := &apiResp{Code: "OK", Message: "成功", Success: true}
	resp.Data = &struct {
		Name         string   `json:"name"`
		Video        string   `json:"video"`
		LowRate      string   `json:"lowRate"`
		Music        string   `json:"music"`
		Image        []string `json:"image"`
		Text         string   `json:"text"`
		ResourceType string   `json:"resourceType"`
	}{Name: "BILI_BILI", Video: "https://upos.example/video.mp4", Text: "标题文本"}

	info, err := p.apiResultToInfo(detectedLink{Platform: platformBilibili, URL: "u"}, resp)
	if err != nil {
		t.Fatalf("解析接口成功响应应被转换：%v", err)
	}
	if info.MediaURL != "https://upos.example/video.mp4" || info.Title != "标题文本" {
		t.Errorf("转换结果错误：%+v", info)
	}
	if info.Platform != platformBilibili {
		t.Errorf("平台应沿用识别结果：%q", info.Platform)
	}
}

func TestAPIResultToInfoFallsBackToLowRate(t *testing.T) {
	p := newTestPlugin(t)
	resp := &apiResp{Code: "OK", Success: true}
	resp.Data = &struct {
		Name         string   `json:"name"`
		Video        string   `json:"video"`
		LowRate      string   `json:"lowRate"`
		Music        string   `json:"music"`
		Image        []string `json:"image"`
		Text         string   `json:"text"`
		ResourceType string   `json:"resourceType"`
	}{LowRate: "https://example/low.mp4"}

	info, err := p.apiResultToInfo(detectedLink{Platform: platformDouyin}, resp)
	if err != nil || info.MediaURL != "https://example/low.mp4" {
		t.Errorf("高清缺失时应回退低码率：info=%+v err=%v", info, err)
	}
}

func TestAPIResultToInfoAtlas(t *testing.T) {
	p := newTestPlugin(t)
	resp := &apiResp{Code: "OK", Success: true}
	resp.Data = &struct {
		Name         string   `json:"name"`
		Video        string   `json:"video"`
		LowRate      string   `json:"lowRate"`
		Music        string   `json:"music"`
		Image        []string `json:"image"`
		Text         string   `json:"text"`
		ResourceType string   `json:"resourceType"`
	}{Image: []string{"https://a/1.jpg", "https://a/2.jpg"}, ResourceType: "P"}

	_, err := p.apiResultToInfo(detectedLink{Platform: platformDouyin}, resp)
	if err == nil {
		t.Fatal("图集应返回错误")
	}
	var unsupported *unsupportedError
	if !errors.As(err, &unsupported) {
		t.Errorf("图集应标记为不支持解析（不触发本地兜底），实际错误类型 %T", err)
	}
	if !strings.Contains(err.Error(), "2 张图片") {
		t.Errorf("提示应包含图片数量：%v", err)
	}
}

func TestAPIResultToInfoMessages(t *testing.T) {
	p := newTestPlugin(t)
	cases := []struct {
		code      string
		message   string
		wantQuota bool
		wantInMsg string
	}{
		{"ERR_EXTRACT_LIMIT_MAX", "提取已达上限", true, "额度"},
		{"ERR_EXTRACT_LINK_NOT_EXIST", "未发现需提取链接", false, "未在文案中找到"},
		{"ERR_EXTRACT_FAIL", "提取失败", false, "提取失败"},
		{"ERR_UNKNOWN", "服务异常", false, "服务异常"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			resp := &apiResp{Code: tc.code, Message: tc.message}
			_, err := p.apiResultToInfo(detectedLink{Platform: platformDouyin}, resp)
			if err == nil {
				t.Fatalf("错误码 %s 应返回错误", tc.code)
			}
			isQuota := errors.Is(err, errAPIQuota)
			if isQuota != tc.wantQuota {
				t.Errorf("配额判定 = %v，期望 %v（err=%v）", isQuota, tc.wantQuota, err)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Errorf("错误文案 %q 应包含 %q", err.Error(), tc.wantInMsg)
			}
		})
	}
}

func TestBuildAPIText(t *testing.T) {
	p := newTestPlugin(t)
	linkURL := "https://www.bilibili.com/video/BV1PWpn6oEmK/"
	link := detectedLink{Platform: platformBilibili, URL: linkURL}

	// 带分享文案：整段文案原样送出（接口兼容文案形式，且对抖音短链必需）
	share := "【标题】 " + linkURL + " 复制此链接打开B站"
	if got := p.buildAPIText(context.Background(), link, share); got != share {
		t.Errorf("带文案时应传原文，得到 %q", got)
	}
	// 只有链接本身：送出链接
	if got := p.buildAPIText(context.Background(), link, linkURL); got != linkURL {
		t.Errorf("纯链接应传链接，得到 %q", got)
	}
	// 空文案：送出链接
	if got := p.buildAPIText(context.Background(), link, "   "); got != linkURL {
		t.Errorf("空文案应退回链接，得到 %q", got)
	}
	// 多链接消息：只传本条链接，避免接口解析到别的链接
	multi := "https://v.douyin.com/a/ 和 " + linkURL
	if got := p.buildAPIText(context.Background(), link, multi); got != linkURL {
		t.Errorf("多链接应只传本条链接，得到 %q", got)
	}
	// 超长文案按字符截断
	long := strings.Repeat("字", 600) + " " + linkURL
	if got := p.buildAPIText(context.Background(), link, long); len([]rune(got)) != apiMaxTextRunes {
		t.Errorf("超长文案应截断到 %d 字符，实际 %d", apiMaxTextRunes, len([]rune(got)))
	}
}

func TestHasTextBeyondLink(t *testing.T) {
	cases := []struct {
		text string
		link string
		want bool
	}{
		{"【标题】 https://v.douyin.com/a/ 复制此链接", "https://v.douyin.com/a/", true},
		{"https://v.douyin.com/a/", "https://v.douyin.com/a/", false},
		{"  https://v.douyin.com/a/  ", "https://v.douyin.com/a/", false},
		{"https://v.douyin.com/a/ ……", "https://v.douyin.com/a/", false},
		{"@某人 https://v.douyin.com/a/", "https://v.douyin.com/a/", true},
	}
	for _, tc := range cases {
		if got := hasTextBeyondLink(tc.text, tc.link); got != tc.want {
			t.Errorf("hasTextBeyondLink(%q) = %v，期望 %v", tc.text, got, tc.want)
		}
	}
}

func TestHostOf(t *testing.T) {
	if got := hostOf("https://V.DouYin.COM/abc"); got != "v.douyin.com" {
		t.Errorf("主机名未转小写：%q", got)
	}
	if got := hostOf("not-a-url"); got != "" {
		t.Errorf("非法链接应返回空串：%q", got)
	}
}

// ---- 工具函数 ----

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:                  "未知",
		-1:                 "未知",
		512:                "512B",
		1024:               "1.0KB",
		718490:             "701.7KB",
		25 * 1024 * 1024:   "25.0MB",
		1536 * 1024 * 1024: "1.5GB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q，期望 %q", in, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[float64]string{
		0:     "",
		-3:    "",
		7.569: "0:08",
		190:   "3:10",
		3661:  "1:01:01",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%v) = %q，期望 %q", in, got, want)
		}
	}
}

func TestTruncateText(t *testing.T) {
	if got := truncateText("  hello  ", 10); got != "hello" {
		t.Errorf("未去空白：%q", got)
	}
	if got := truncateText("一二三四五六", 3); got != "一二三…" {
		t.Errorf("截断结果 %q", got)
	}
	if got := truncateText("abc", 10); got != "abc" {
		t.Errorf("短文不应被截断：%q", got)
	}
}

func TestTempDirUsesOverrideAndCleanup(t *testing.T) {
	p := newTestPlugin(t)

	// 未设置覆盖时应落在 data/videoparse/tmp
	raw := &VideoParsePlugin{}
	if got := raw.tempDir(); got != filepath.Join("data", "videoparse", "tmp") {
		t.Errorf("默认临时目录 = %q", got)
	}
	if p.tempDir() != p.tempDirOverride {
		t.Errorf("覆盖目录未生效：%q", p.tempDir())
	}

	// 清理只删除本插件前缀的过期文件
	dir := p.tempDir()
	stale := filepath.Join(dir, tempFilePrefix+"stale.mp4")
	fresh := filepath.Join(dir, tempFilePrefix+"fresh.mp4")
	other := filepath.Join(dir, "keep.txt")
	for _, name := range []string{stale, fresh, other} {
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-tempFileTTL - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	p.cleanupTempFiles()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("过期临时文件应被删除")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("未过期临时文件不应被删除")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("非本插件文件不应被删除")
	}

	p.removeTempFile(fresh)
	p.removeTempFile("") // 空路径不应 panic
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Error("removeTempFile 应删除文件")
	}
}

func TestAPIResultToInfoNullData(t *testing.T) {
	p := newTestPlugin(t)
	// 接口报成功但没有作品数据（实测快手短链偶发）：应给出可读提示而非"返回 OK：成功"
	resp := &apiResp{Code: "OK", Message: "成功", Success: true, Data: nil}
	_, err := p.apiResultToInfo(detectedLink{Platform: platformKuaishou}, resp)
	if err == nil {
		t.Fatal("无数据应返回错误")
	}
	if !strings.Contains(err.Error(), "未返回该作品的数据") {
		t.Errorf("错误文案不清晰：%v", err)
	}
	// 非配额错误，应允许本地兜底（不属于 unsupportedError）
	var unsupported *unsupportedError
	if errors.As(err, &unsupported) {
		t.Error("无数据不应被标记为「不支持解析」，否则快手无法触发本地兜底")
	}
}
