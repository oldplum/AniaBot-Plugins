package videoparse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// defaultAPIURL 默认解析接口：DownCats（蓝猫素材助手）的免费提取接口。
// 该接口接收整段分享文案，返回作品直链，支持抖音/快手/B站/小红书/TikTok。
// 注意：免费额度按 IP 每日限额（被限时接口会返回 ERR_EXTRACT_LIMIT_MAX），
// 登录官网可提升额度；也可将本地址换成自建的兼容服务（见 README）。
const defaultAPIURL = "https://www.downcats.com/v1/extract/free/video"

// apiCallTimeout 单次接口调用超时：解析服务偶发较慢，但不能拖住整个任务太久。
const apiCallTimeout = 45 * time.Second

// apiMaxTextRunes 送给接口的文案长度上限，避免超长消息被服务端拒绝。
const apiMaxTextRunes = 500

// apiResp DownCats 提取接口响应。
type apiResp struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Success bool   `json:"success"`
	Data    *struct {
		Name         string   `json:"name"` // 平台标识，如 DOU_YIN / BILI_BILI
		Video        string   `json:"video"`
		LowRate      string   `json:"lowRate"`
		Music        string   `json:"music"`
		Image        []string `json:"image"`
		Text         string   `json:"text"`
		ResourceType string   `json:"resourceType"` // V=视频，图集为图片类资源
	} `json:"data"`
}

// errAPIQuota 免费额度用尽的哨兵错误：调用方据此给出更友好的提示，
// 并允许浏览器解析类平台继续走本地兜底。
var errAPIQuota = errors.New("解析接口今日免费额度已用完")

// unsupportedError 表示「链接本身不支持解析」（图集等），不参与本地兜底重试。
type unsupportedError struct{ msg string }

func (e *unsupportedError) Error() string { return e.msg }

// extractViaAPI 调用解析接口取作品直链。
// shareText 为整段分享文案（接口从文案中提取链接；抖音短链只给链接时解析不出，
// 需要分享文案或还原后的长链，故按需补全）。
func (p *VideoParsePlugin) extractViaAPI(ctx context.Context, link detectedLink, shareText string) (*videoInfo, error) {
	if p.apiDisabled {
		return nil, fmt.Errorf("解析接口已关闭")
	}
	text := p.buildAPIText(ctx, link, shareText)
	if err := p.waitAPISlot(ctx); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()

	resp, err := p.httpClient().R().
		SetContext(callCtx).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("User-Agent", downloadUserAgent).
		SetBody(map[string]string{"text": text, "locale": "zh"}).
		Post(p.cfg.APIURL)
	if err != nil {
		return nil, fmt.Errorf("解析接口请求失败：%w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("解析接口返回 HTTP %s", resp.Status())
	}
	var parsed apiResp
	if err := json.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, fmt.Errorf("解析接口响应不是合法 JSON：%w", err)
	}
	return p.apiResultToInfo(link, &parsed)
}

// apiResultToInfo 把接口响应转换为统一的视频信息，并细分失败原因。
func (p *VideoParsePlugin) apiResultToInfo(link detectedLink, resp *apiResp) (*videoInfo, error) {
	if strings.EqualFold(resp.Code, "OK") && resp.Data != nil {
		data := resp.Data
		media := strings.TrimSpace(data.Video)
		if media == "" {
			media = strings.TrimSpace(data.LowRate) // 高清直链缺失时退而用低码率
		}
		if media != "" {
			return &videoInfo{
				Platform: link.Platform,
				Title:    strings.TrimSpace(data.Text),
				MediaURL: media,
				Referer:  apiReferer(link.Platform),
			}, nil
		}
		if len(data.Image) > 0 {
			return nil, &unsupportedError{msg: fmt.Sprintf("该作品是图集（%d 张图片），暂不支持解析", len(data.Image))}
		}
		return nil, fmt.Errorf("解析接口未返回可下载地址")
	}

	msg := strings.TrimSpace(resp.Message)
	switch strings.ToUpper(strings.TrimSpace(resp.Code)) {
	case "OK":
		// 接口报成功但没带作品数据：多见于作品已删除或需要完整分享文案
		return nil, fmt.Errorf("解析接口未返回该作品的数据（%s，可尝试发送完整分享文案）", fallbackText(msg, "无数据"))
	case "ERR_EXTRACT_LIMIT_MAX":
		if msg == "" {
			msg = "今日提取已达上限"
		}
		return nil, fmt.Errorf("%w（%s，可稍后重试或登录官网提升额度，详见插件说明）", errAPIQuota, msg)
	case "ERR_EXTRACT_LINK_NOT_EXIST":
		return nil, fmt.Errorf("解析接口未在文案中找到可提取的链接")
	case "ERR_NOT_SUPPORT_STATION", "ERR_SUPPORT_STATION":
		return nil, &unsupportedError{msg: "解析接口暂不支持该站点"}
	case "ERR_EXTRACT_FAIL":
		return nil, fmt.Errorf("解析接口提取失败（%s）", fallbackText(msg, "作品可能已删除或需要登录"))
	default:
		if msg == "" {
			msg = "未知错误"
		}
		return nil, fmt.Errorf("解析接口返回 %s：%s", resp.Code, msg)
	}
}

// buildAPIText 组装送给接口的文案。接口从整段文本里找链接，实测：
//   - 带分享文案（标题/「复制此链接…」等）时抖音短链可正常解析；
//   - 单发裸短链时接口可能取不到内容，此时先把短链还原成长链再送。
//
// 多链接消息只送本条链接，避免接口解析到别的链接去。
func (p *VideoParsePlugin) buildAPIText(ctx context.Context, link detectedLink, shareText string) string {
	trimmed := strings.TrimSpace(shareText)
	if len(detectLinks(trimmed)) == 1 && hasTextBeyondLink(trimmed, link.URL) {
		return truncateRunes(trimmed, apiMaxTextRunes)
	}
	if link.Platform == platformDouyin && strings.EqualFold(hostOf(link.URL), "v.douyin.com") {
		if long, err := p.finalURL(ctx, link.URL); err == nil && long != link.URL {
			return long
		}
	}
	return link.URL
}

// hasTextBeyondLink 判断文案里除了该链接之外是否还有实质文字（标题、平台提示语等）。
// 只剩标点（如「……」、表情符号）时视为没有文案，调用方据此决定是否需要补全链接。
func hasTextBeyondLink(text, linkURL string) bool {
	rest := strings.Replace(text, linkURL, " ", 1)
	rest = strings.TrimFunc(rest, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	return rest != ""
}

// hostOf 取链接主机名（小写）；解析失败返回空串。
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// apiReferer 下载接口返回直链时的 Referer（部分 CDN 校验来路）。
func apiReferer(platform string) string {
	switch platform {
	case platformDouyin:
		return "https://www.douyin.com/"
	case platformKuaishou:
		return "https://www.kuaishou.com/"
	case platformBilibili:
		return "https://www.bilibili.com/"
	}
	return ""
}

// fallbackText 主值为空时返回备选文案。
func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// truncateRunes 按字符数截断（中英文混排下比字节截断更符合直觉）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
