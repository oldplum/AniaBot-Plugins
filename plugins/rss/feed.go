// feed.go 订阅源的拉取与解析：支持 RSS 2.0 与 Atom 两种格式。
package rss

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"regexp"
	"strings"
)

// rawFeed RSS 2.0 与 Atom 的联合解析结构：字段按格式各自匹配，匹配不到为零值。
type rawFeed struct {
	Channel struct {
		Title string    `xml:"title"`
		Items []rawItem `xml:"item"` // RSS 2.0 条目
	} `xml:"channel"`
	Title   string    `xml:"title"` // Atom feed 标题
	Entries []rawAtom `xml:"entry"` // Atom 条目
}

// rawItem RSS 2.0 <item>。
type rawItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

// rawAtom Atom <entry>。
type rawAtom struct {
	Title   string    `xml:"title"`
	ID      string    `xml:"id"`
	Updated string    `xml:"updated"`
	Summary string    `xml:"summary"`
	Content string    `xml:"content"`
	Links   []rawLink `xml:"link"`
}

// rawLink Atom <link>：正文链接取 rel 为空或 alternate 的那个。
type rawLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// fetchFeed 拉取并解析订阅源，返回条目列表与源标题。
func (p *RSSPlugin) fetchFeed(feedURL string) ([]feedItem, string, error) {
	resp, err := p.RestyClient.R().
		SetHeader("User-Agent", "AniaBot-RSS/1.0 (+https://github.com/jeanhua/AniaBot)").
		SetHeader("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, */*").
		Get(feedURL)
	if err != nil {
		return nil, "", fmt.Errorf("请求失败: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode())
	}
	return parseFeed([]byte(resp.String()))
}

// parseFeed 解析 XML 正文，自动识别 RSS 2.0 与 Atom。
func parseFeed(data []byte) ([]feedItem, string, error) {
	var raw rawFeed
	// xml 会忽略非 UTF-8 声明兼容性差的场景：解析失败时明确报错，由上层计失败次数
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, "", fmt.Errorf("XML 解析失败: %w", err)
	}

	title := strings.TrimSpace(raw.Channel.Title)
	if title == "" {
		title = strings.TrimSpace(raw.Title)
	}

	var items []feedItem
	for _, it := range raw.Channel.Items {
		id := strings.TrimSpace(it.GUID)
		if id == "" {
			id = strings.TrimSpace(it.Link)
		}
		if id == "" {
			// 无 guid 也无 link：用标题哈希兜底，保证去重有键
			id = hashID(it.Title + it.PubDate)
		}
		items = append(items, feedItem{
			ID:      id,
			Title:   cleanText(it.Title),
			Link:    strings.TrimSpace(it.Link),
			Summary: cleanText(it.Description),
		})
	}
	for _, e := range raw.Entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = hashID(e.Title + e.Updated)
		}
		items = append(items, feedItem{
			ID:      id,
			Title:   cleanText(e.Title),
			Link:    atomLink(e.Links),
			Summary: cleanText(firstNonEmpty(e.Summary, e.Content)),
		})
	}
	if len(items) > maxFetchItems {
		items = items[:maxFetchItems]
	}
	return items, title, nil
}

// atomLink 从 Atom entry 的 link 列表中选出正文链接。
func atomLink(links []rawLink) string {
	fallback := ""
	for _, l := range links {
		switch l.Rel {
		case "", "alternate":
			return strings.TrimSpace(l.Href)
		}
		if fallback == "" {
			fallback = strings.TrimSpace(l.Href)
		}
	}
	return fallback
}

var tagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// cleanText 清理条目标题/摘要：去 HTML 标签、反转义实体、压缩空白。
// 标签直接删除而非替换为空格，避免中文里内联标签（如 <b>）把词语拆散。
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// hashID 兜底条目 ID：标题+时间的 SHA-256 前 16 位。
func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
