package videoparse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// videoInfo 一次成功解析的结果：直链 + 展示用元信息。
type videoInfo struct {
	Platform  string
	Title     string
	Author    string
	Duration  float64 // 秒，0 表示未知
	SizeBytes int64   // 字节，<=0 表示未知
	MediaURL  string  // 视频直链
	Referer   string  // 下载直链时需要携带的 Referer
}

// 下载与访问分享页用的 UA：B 站 CDN 对空 UA 直接 403，快手分享页只对移动端 UA
// 返回完整数据。
const (
	downloadUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 AniaBot-videoparse/1.0"
	mobileUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) " +
		"AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
)

// apiMinInterval 两次解析接口调用的最小间隔，保护免费接口不被瞬时打爆。
const apiMinInterval = 3 * time.Second

// httpClient 返回解析请求使用的客户端：优先框架注入的共享客户端（统一代理与超时），
// 否则惰性创建一个带超时的兜底客户端（保证任何调用路径都不会因客户端缺失而 panic）。
func (p *VideoParsePlugin) httpClient() *resty.Client {
	if p.RestyClient != nil {
		return p.RestyClient
	}
	p.clientOnce.Do(func() {
		if p.client == nil {
			p.client = resty.New().SetTimeout(30 * time.Second)
		}
	})
	return p.client
}

// resolve 解析单个链接。
// 默认策略是解析接口优先（三平台统一走 DownCats，行为一致）；接口不可用时，
// B 站与快手退化为本地解析（官方接口 / 分享页），抖音没有本地取流方案，直接返回接口错误。
// 配置 prefer_local 后改为本地优先：B 站/快手不消耗接口额度，把额度留给抖音。
func (p *VideoParsePlugin) resolve(ctx context.Context, link detectedLink, shareText string) (*videoInfo, error) {
	if p.cfg.PreferLocal {
		return p.resolveLocalFirst(ctx, link, shareText)
	}
	if p.apiDisabled {
		return p.resolveLocal(ctx, link)
	}
	info, apiErr := p.extractViaAPI(ctx, link, shareText)
	if apiErr == nil {
		return info, nil
	}
	// 接口明确表示「该链接不支持解析」（图集等）：不再做本地兜底，直接返回原因
	var unsupported *unsupportedError
	if errors.As(apiErr, &unsupported) {
		return nil, apiErr
	}

	local, localErr := p.resolveLocal(ctx, link)
	if localErr == nil {
		p.Logger.Warn("解析接口不可用，已改用本地解析",
			"platform", link.Platform, "api_err", apiErr)
		return local, nil
	}
	if link.Platform == platformDouyin {
		// 抖音只有接口一条路，保留接口错误里更有价值的提示（如额度用尽）
		return nil, apiErr
	}
	return nil, fmt.Errorf("%v（本地解析也不可用：%v）", apiErr, localErr)
}

// resolveLocalFirst 本地优先策略：B 站/快手先本地解析，失败再走解析接口；
// 抖音没有本地方案，直接走接口。
func (p *VideoParsePlugin) resolveLocalFirst(ctx context.Context, link detectedLink, shareText string) (*videoInfo, error) {
	if link.Platform == platformDouyin {
		if p.apiDisabled {
			return nil, fmt.Errorf("抖音解析未启用（可配置 plugin.videoparse.api_url）")
		}
		return p.extractViaAPI(ctx, link, shareText)
	}
	local, localErr := p.resolveLocal(ctx, link)
	if localErr == nil {
		return local, nil
	}
	if p.apiDisabled {
		return nil, localErr
	}
	info, apiErr := p.extractViaAPI(ctx, link, shareText)
	if apiErr == nil {
		p.Logger.Debug("本地解析失败，已改用解析接口", "platform", link.Platform, "local_err", localErr)
		return info, nil
	}
	return nil, fmt.Errorf("%v（解析接口也不可用：%v）", localErr, apiErr)
}

// resolveLocal 本地解析（不依赖第三方解析接口）：B 站走官方接口，快手读分享页。
func (p *VideoParsePlugin) resolveLocal(ctx context.Context, link detectedLink) (*videoInfo, error) {
	switch link.Platform {
	case platformBilibili:
		return p.resolveBilibili(ctx, link.URL)
	case platformKuaishou:
		return p.resolveKuaishou(ctx, link.URL)
	case platformDouyin:
		return nil, fmt.Errorf("抖音链接需要解析接口支持")
	}
	return nil, fmt.Errorf("暂不支持解析该链接（支持 %s）", supportedPlatformsHint)
}

// waitAPISlot 限流：两次解析接口调用之间至少间隔 p.apiInterval。
func (p *VideoParsePlugin) waitAPISlot(ctx context.Context) error {
	interval := p.apiInterval
	p.apiMu.Lock()
	wait := interval - time.Since(p.apiLast)
	if wait > 0 {
		p.apiMu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		p.apiMu.Lock()
	}
	p.apiLast = time.Now()
	p.apiMu.Unlock()
	return nil
}

// ---- 哔哩哔哩：官方公开接口（接口不可用时的本地兜底） ----

// biliViewResp B 站 web-interface/view 接口响应（只取用到的字段）。
type biliViewResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Bvid     string `json:"bvid"`
		Cid      int64  `json:"cid"`
		Title    string `json:"title"`
		Duration int    `json:"duration"`
		Owner    struct {
			Name string `json:"name"`
		} `json:"owner"`
	} `json:"data"`
}

// biliPlayResp B 站 player/playurl 接口响应（html5 平台返回整体 mp4，无需合流）。
type biliPlayResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Durl []struct {
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"durl"`
	} `json:"data"`
}

// resolveBilibili 用 B 站官方接口解析：b23.tv 短链先还原成标准视频页，
// 再 view 取 cid 与标题、playurl(platform=html5) 取整段 mp4 直链。
func (p *VideoParsePlugin) resolveBilibili(ctx context.Context, rawURL string) (*videoInfo, error) {
	pageURL := rawURL
	if u, err := url.Parse(rawURL); err == nil && strings.ToLower(u.Hostname()) == "b23.tv" {
		final, err := p.finalURL(ctx, rawURL)
		if err != nil {
			return nil, fmt.Errorf("展开 B 站短链失败：%w", err)
		}
		pageURL = final
	}
	id, ok := bilibiliVideoID(pageURL)
	if !ok {
		return nil, fmt.Errorf("无法从链接中识别 B 站视频号")
	}

	headers := map[string]string{
		"User-Agent": downloadUserAgent,
		"Referer":    "https://www.bilibili.com/",
	}
	viewQuery := url.Values{}
	if strings.HasPrefix(id, "av") {
		viewQuery.Set("aid", strings.TrimPrefix(id, "av"))
	} else {
		viewQuery.Set("bvid", id)
	}
	var view biliViewResp
	if err := p.getJSON(ctx, "https://api.bilibili.com/x/web-interface/view?"+viewQuery.Encode(), headers, &view); err != nil {
		return nil, fmt.Errorf("B 站视频信息获取失败：%w", err)
	}
	if view.Code != 0 {
		return nil, fmt.Errorf("B 站接口返回错误：%s", biliMessage(view.Code, view.Message))
	}
	if view.Data.Cid == 0 {
		return nil, fmt.Errorf("B 站视频信息不完整（cid 为空）")
	}

	playQuery := url.Values{}
	playQuery.Set("bvid", view.Data.Bvid)
	playQuery.Set("cid", fmt.Sprint(view.Data.Cid))
	playQuery.Set("qn", "64")          // 720P：未登录状态下可用
	playQuery.Set("fnval", "1")        // durl 形式的整段 mp4
	playQuery.Set("platform", "html5") // html5 返回单文件，免音视频合流
	playQuery.Set("high_quality", "1")
	var play biliPlayResp
	if err := p.getJSON(ctx, "https://api.bilibili.com/x/player/playurl?"+playQuery.Encode(), headers, &play); err != nil {
		return nil, fmt.Errorf("B 站视频地址获取失败：%w", err)
	}
	if play.Code != 0 {
		return nil, fmt.Errorf("B 站接口返回错误：%s", biliMessage(play.Code, play.Message))
	}
	if len(play.Data.Durl) == 0 || play.Data.Durl[0].URL == "" {
		return nil, fmt.Errorf("B 站未返回可下载地址（付费或番剧视频暂不支持）")
	}

	return &videoInfo{
		Platform:  platformBilibili,
		Title:     view.Data.Title,
		Author:    view.Data.Owner.Name,
		Duration:  float64(view.Data.Duration),
		SizeBytes: play.Data.Durl[0].Size,
		MediaURL:  play.Data.Durl[0].URL,
		Referer:   "https://www.bilibili.com/",
	}, nil
}

// biliMessage B 站错误码展示：接口给出非空 message 时优先用它。
func biliMessage(code int, message string) string {
	if strings.TrimSpace(message) != "" {
		return message
	}
	return fmt.Sprintf("错误码 %d", code)
}

// ---- 快手：分享页原生解析（接口不可用时的本地兜底） ----

// resolveKuaishou 解析快手分享链接：快手在移动端分享页把作品数据以
// window.INIT_STATE 内联下发，其中 mainMvUrls 就是无水印直链。
func (p *VideoParsePlugin) resolveKuaishou(ctx context.Context, rawURL string) (*videoInfo, error) {
	var lastErr error
	for _, candidate := range kuaishouCandidates(rawURL) {
		html, finalURL, err := p.getPage(ctx, candidate, map[string]string{
			"User-Agent":      mobileUserAgent,
			"Accept-Language": "zh-CN,zh;q=0.9",
		})
		if err != nil {
			lastErr = err
			continue
		}
		info, err := parseKuaishouPage(html)
		if err != nil {
			lastErr = err
			continue
		}
		info.Platform = platformKuaishou
		info.Referer = "https://www.kuaishou.com/"
		p.Logger.Debug("快手分享页解析成功", "final_url", finalURL)
		return info, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未找到可用的快手页面")
	}
	return nil, fmt.Errorf("快手解析失败：%w", lastErr)
}

// kuaishouCandidates 生成快手页面抓取候选：带作品 ID 的链接改写为移动端分享页，
// 短链保持原样（由其自身 302 到移动端页）。
func kuaishouCandidates(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return []string{rawURL}
	}
	id := kuaishouPhotoID(u.Path)
	if id == "" {
		return []string{rawURL}
	}
	candidates := []string{"https://v.m.chenzhongtech.com/fw/photo/" + id}
	if !strings.Contains(rawURL, "v.m.chenzhongtech.com") {
		candidates = append(candidates, rawURL)
	}
	return candidates
}

// kuaishouMainMvKey 快手分享页里主视频直链所在的键。
const kuaishouMainMvKey = `"mainMvUrls"`

// parseKuaishouPage 从分享页 HTML 里提取作品信息。纯函数，便于单测。
func parseKuaishouPage(html string) (*videoInfo, error) {
	state, ok := extractINITState(html)
	if !ok {
		return nil, fmt.Errorf("页面中未找到快手作品数据（可能是图集或链接已失效）")
	}
	media, ok := firstURLAfterKey(state, kuaishouMainMvKey)
	if !ok || !strings.Contains(media, ".mp4") {
		return nil, fmt.Errorf("页面数据里没有可直接发送的视频直链（该作品可能是图集）")
	}
	info := &videoInfo{MediaURL: media}
	if title, ok := findJSONString(state, "caption"); ok {
		info.Title = title
	}
	if author, ok := findJSONString(state, "userName"); ok {
		info.Author = author
	}
	return info, nil
}

// ---- 通用 HTTP 与文本辅助 ----

// getJSON 发起 GET 并反序列化 JSON 响应体，非 2xx 直接报错。
func (p *VideoParsePlugin) getJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	resp, err := p.httpClient().R().
		SetContext(ctx).
		SetHeaders(headers).
		SetHeader("Accept", "application/json").
		Get(rawURL)
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("HTTP %s：%s", resp.Status(), truncateText(resp.String(), 120))
	}
	if err := json.Unmarshal(resp.Body(), out); err != nil {
		return fmt.Errorf("响应不是合法 JSON：%w", err)
	}
	return nil
}

// getPage 抓取网页并返回 HTML 与最终地址（跟随 302 后的地址）。
func (p *VideoParsePlugin) getPage(ctx context.Context, rawURL string, headers map[string]string) (string, string, error) {
	resp, err := p.httpClient().R().
		SetContext(ctx).
		SetHeaders(headers).
		Get(rawURL)
	if err != nil {
		return "", "", err
	}
	if resp.IsError() {
		return "", "", fmt.Errorf("HTTP %s", resp.Status())
	}
	final := rawURL
	if rr := resp.RawResponse; rr != nil && rr.Request != nil && rr.Request.URL != nil {
		final = rr.Request.URL.String()
	}
	return resp.String(), final, nil
}

// finalURL 展开短链：用桌面 UA 请求并返回重定向终点地址。
func (p *VideoParsePlugin) finalURL(ctx context.Context, rawURL string) (string, error) {
	_, final, err := p.getPage(ctx, rawURL, map[string]string{"User-Agent": downloadUserAgent})
	return final, err
}

// truncateText 截断过长文本（错误信息展示用，按字符计）。
func truncateText(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

// extractINITState 提取页面中 `window.INIT_STATE = {...}` 的完整 JSON 对象文本。
func extractINITState(html string) (string, bool) {
	idx := strings.Index(html, "window.INIT_STATE")
	if idx < 0 {
		return "", false
	}
	start := strings.IndexByte(html[idx:], '{')
	if start < 0 {
		return "", false
	}
	obj, _, ok := matchJSONObject(html, idx+start)
	return obj, ok
}

// matchJSONObject 从 s[start]（必须是 '{'）起做括号配对，返回完整的 JSON 对象文本与
// 结束位置。逐字符跟踪字符串与转义状态，避免值中的花括号扰乱配对。
func matchJSONObject(s string, start int) (string, int, bool) {
	if start >= len(s) || s[start] != '{' {
		return "", start, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], i + 1, true
			}
		}
	}
	return "", start, false
}

// findJSONString 在 JSON 文本中定位 `"key":"value"` 并返回解码后的值。
func findJSONString(text, key string) (string, bool) {
	marker := `"` + key + `"`
	for offset := 0; ; {
		idx := strings.Index(text[offset:], marker)
		if idx < 0 {
			return "", false
		}
		pos := offset + idx + len(marker)
		offset = pos
		rest := strings.TrimLeft(text[pos:], " \t\r\n")
		if !strings.HasPrefix(rest, ":") {
			continue // 是别的字段名的一部分或位于字符串内，继续找下一个
		}
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
		if !strings.HasPrefix(rest, `"`) {
			continue // 该键的值不是字符串
		}
		raw, ok := scanJSONStringLiteral(rest)
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			continue
		}
		return value, true
	}
}

// firstURLAfterKey 在 JSON 文本中定位键后出现的第一个字符串型 http(s) url 值
// （适配 mainMvUrls 这类 `"key":[{"cdn":"...","url":"https://..."}]` 结构）。
func firstURLAfterKey(text, key string) (string, bool) {
	idx := strings.Index(text, key)
	if idx < 0 {
		return "", false
	}
	rest := text[idx+len(key):]
	for {
		u, ok := findJSONString(rest, "url")
		if !ok {
			return "", false
		}
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			return u, true
		}
		next := strings.Index(rest, `"url"`)
		if next < 0 {
			return "", false
		}
		rest = rest[next+len(`"url"`):]
	}
}

// scanJSONStringLiteral 从以 '"' 开头的文本里取一个完整 JSON 字符串字面量（含引号）。
func scanJSONStringLiteral(s string) (string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", false
	}
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			return s[:i+1], true
		}
	}
	return "", false
}
