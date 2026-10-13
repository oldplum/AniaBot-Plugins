package videoparse

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// 支持的平台标识（与配置、日志、提示语共用）。
const (
	platformDouyin   = "douyin"
	platformKuaishou = "kuaishou"
	platformBilibili = "bilibili"
)

// platformNames 平台中文名，用于用户可见文案。
var platformNames = map[string]string{
	platformDouyin:   "抖音",
	platformKuaishou: "快手",
	platformBilibili: "哔哩哔哩",
}

// supportedPlatformsHint 提示语里的支持平台列表。
const supportedPlatformsHint = "抖音、快手、哔哩哔哩"

// linkPattern 从消息文本里抓取 http(s) 链接。排除空白、引号、中英文标点与全角空格
// （分享文本常以全角空格或中文标点紧邻链接，如「https://v.douyin.com/xxx/ 复制此链接」）。
var linkPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>,;、，。！？；：）)】〕》」\x{3000}]+`)

// trailingJunk 链接尾部常见的误配标点，检测后统一裁掉。
const trailingJunk = `.,;:!?)]}>"'、，。！？；：）】〕》」`

// bvPattern 匹配 B 站视频号（BV 号大小写敏感，不能忽略大小写）。
var bvPattern = regexp.MustCompile(`[Bb][Vv][0-9A-Za-z]{10}`)

// avPattern 匹配 B 站旧版 av 号。
var avPattern = regexp.MustCompile(`(?i)\bav(\d+)\b`)

// trackingParams 分享链接中只用于统计追踪的查询参数，参与去重键时剔除。
var trackingParams = map[string]struct{}{
	"vd_source": {}, "share_source": {}, "share_medium": {}, "share_plat": {},
	"share_tag": {}, "share_token": {}, "spm_id_from": {}, "from_spmid": {},
	"unique_k": {}, "timestamp": {}, "buvid": {}, "up_id": {}, "tab": {},
}

// detectedLink 从消息中识别到的一条待解析视频链接。
type detectedLink struct {
	Platform string
	URL      string
}

// classifyLink 判断链接是否为本插件支持的视频分享链接；是则返回平台标识，否则返回空串。
// 平台判断基于域名，页面类型再按路径收紧（避免把用户主页、直播间当视频解析）。
func classifyLink(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	path := u.Path
	switch {
	case host == "douyin.com" || strings.HasSuffix(host, ".douyin.com"):
		// 支持 v.douyin.com/<code> 短链、/video/<id>、/share/video/<id>、/note/<id>
		if host == "v.douyin.com" && shortCode(path) != "" {
			return platformDouyin
		}
		if strings.Contains(path, "/video/") || strings.Contains(path, "/note/") {
			return platformDouyin
		}
		return ""
	case host == "kuaishou.com" || strings.HasSuffix(host, ".kuaishou.com") ||
		strings.HasSuffix(host, ".chenzhongtech.com"):
		// 支持 v.kuaishou.com/<code> 短链、/short-video/<id>、/fw/photo|long-video/<id>
		if shortCode(path) != "" || kuaishouPhotoID(path) != "" || strings.Contains(path, "/short-video/") {
			return platformKuaishou
		}
		return ""
	case host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com") || host == "b23.tv":
		if host == "b23.tv" && shortCode(path) != "" {
			return platformBilibili
		}
		if _, ok := bilibiliVideoID(rawURL); ok {
			return platformBilibili
		}
		return ""
	}
	return ""
}

// detectLinks 从一段文本里提取全部受支持的视频链接（保持出现顺序，同一视频只留一次）。
func detectLinks(text string) []detectedLink {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	matches := linkPattern.FindAllString(text, -1)
	out := make([]detectedLink, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, raw := range matches {
		raw = strings.TrimRight(strings.TrimSpace(raw), trailingJunk)
		platform := classifyLink(raw)
		if platform == "" {
			continue
		}
		key := dedupeKey(platform, raw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, detectedLink{Platform: platform, URL: raw})
	}
	return out
}

// dedupeKey 计算链接的去重键：B 站按视频号归一（同一视频的各种分享链接视为同一键），
// 其余平台按「主机 + 路径 + 必要查询参数」归一。
func dedupeKey(platform, rawURL string) string {
	if platform == platformBilibili {
		if id, ok := bilibiliVideoID(rawURL); ok {
			return platform + ":" + id
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return platform + ":" + rawURL
	}
	q := u.Query()
	for key := range trackingParams {
		q.Del(key)
	}
	query := q.Encode()
	key := platform + ":" + strings.ToLower(u.Hostname()) + strings.TrimRight(u.Path, "/")
	if query != "" {
		key += "?" + query
	}
	return key
}

// bilibiliVideoID 从 B 站链接中提取视频标识：BV 号原样返回，av 号转成 "av<数字>"，
// b23.tv 短链返回 "b23:<code>"；不是视频页时第二个返回值为 false。
func bilibiliVideoID(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "b23.tv" {
		if code := shortCode(u.Path); code != "" {
			return "b23:" + code, true
		}
		return "", false
	}
	if bv := bvPattern.FindString(u.Path); bv != "" && strings.Contains(u.Path, "/video/") {
		return bv, true
	}
	if m := avPattern.FindStringSubmatch(u.Path); m != nil && strings.Contains(u.Path, "/video/") {
		return "av" + m[1], true
	}
	return "", false
}

// kuaishouPhotoID 从快手链接路径中提取作品 ID（/short-video/<id>、/fw/photo/<id>、
// /fw/long-video/<id>）。
func kuaishouPhotoID(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segs {
		if (seg == "photo" || seg == "long-video" || seg == "short-video") && i+1 < len(segs) {
			return strings.TrimSpace(segs[i+1])
		}
	}
	return ""
}

// shortCode 取路径中的短链码（形如 /eximqs9eRIY 的单段路径）。
func shortCode(path string) string {
	code := strings.Trim(path, "/")
	if code == "" || strings.Contains(code, "/") {
		return ""
	}
	for _, r := range code {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-') {
			return ""
		}
	}
	return code
}

// cqCodePattern 匹配 OneBot CQ 码段（[CQ:image,url=...] 等），避免把 CQ 参数里的 URL
// 当成分享链接。
var cqCodePattern = regexp.MustCompile(`\[CQ:[^\]]*\]`)

// stripCQCode 去掉文本中的 CQ 码段。
func stripCQCode(text string) string {
	if !strings.Contains(text, "[CQ:") {
		return text
	}
	return cqCodePattern.ReplaceAllString(text, " ")
}

// normalizeGroupIDs 规范化生效群列表：QQ 纯数字统一补 qq: 前缀，其余平台 ID 原样保留。
func normalizeGroupIDs(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		set[message.FromString(id).String()] = struct{}{}
	}
	return set
}
