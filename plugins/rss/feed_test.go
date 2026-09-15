package rss

import (
	"strconv"
	"testing"
)

const sampleRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>科技周刊</title>
    <item>
      <title>第一篇 &lt;重点&gt; 文章</title>
      <link>https://example.com/a1</link>
      <guid>guid-1</guid>
      <pubDate>Mon, 01 Sep 2026 08:00:00 +0800</pubDate>
      <description>&lt;p&gt;这是 &quot;摘要&quot; 一&lt;b&gt;段&lt;/b&gt;HTML</description>
    </item>
    <item>
      <title>第二篇文章</title>
      <link>https://example.com/a2</link>
      <description>摘要二</description>
    </item>
  </channel>
</rss>`

const sampleAtom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom 示例</title>
  <entry>
    <title>条目甲</title>
    <id>urn:uuid:aaa</id>
    <updated>2026-09-01T08:00:00Z</updated>
    <summary>摘要甲</summary>
    <link rel="self" href="https://example.com/self"/>
    <link rel="alternate" href="https://example.com/e1"/>
  </entry>
  <entry>
    <title>条目乙</title>
    <id>urn:uuid:bbb</id>
    <link href="https://example.com/e2"/>
  </entry>
</feed>`

func TestParseRSS2(t *testing.T) {
	items, title, err := parseFeed([]byte(sampleRSS))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if title != "科技周刊" {
		t.Errorf("标题 = %q, 期望 科技周刊", title)
	}
	if len(items) != 2 {
		t.Fatalf("条目数 = %d, 期望 2", len(items))
	}
	if items[0].ID != "guid-1" {
		t.Errorf("ID = %q, 期望 guid-1", items[0].ID)
	}
	// 标题中的字面 <重点> 是转义文本，剥标签阶段无法与真实标签区分，会被一并去除
	if items[0].Title != "第一篇 文章" {
		t.Errorf("标题清理错误: %q", items[0].Title)
	}
	if items[0].Summary != `这是 "摘要" 一段HTML` {
		t.Errorf("摘要清理错误: %q", items[0].Summary)
	}
	if items[1].ID != "https://example.com/a2" {
		t.Errorf("无 guid 时应回退到 link, got %q", items[1].ID)
	}
}

func TestParseAtom(t *testing.T) {
	items, title, err := parseFeed([]byte(sampleAtom))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if title != "Atom 示例" {
		t.Errorf("标题 = %q", title)
	}
	if len(items) != 2 {
		t.Fatalf("条目数 = %d", len(items))
	}
	if items[0].Link != "https://example.com/e1" {
		t.Errorf("应选择 alternate 链接, got %q", items[0].Link)
	}
	if items[1].Link != "https://example.com/e2" {
		t.Errorf("无 rel 时应取第一个链接, got %q", items[1].Link)
	}
	if items[1].Summary != "" {
		t.Errorf("无摘要应为空, got %q", items[1].Summary)
	}
}

func TestParseFeedInvalid(t *testing.T) {
	if _, _, err := parseFeed([]byte("不是 xml")); err == nil {
		t.Error("非法 XML 应返回错误")
	}
}

func TestUnseenItems(t *testing.T) {
	seen := []string{"a", "b"}
	items := []feedItem{
		{ID: "b"}, {ID: "c"}, {ID: "a"}, {ID: "d"},
	}
	fresh := unseenItems(seen, items)
	if len(fresh) != 2 || fresh[0].ID != "c" || fresh[1].ID != "d" {
		t.Errorf("unseenItems 结果错误: %+v", fresh)
	}
}

func TestPrependSeen(t *testing.T) {
	// 新 ID 应排在最前，超出容量时淘汰最旧的
	got := prependSeen([]string{"s1", "s2"}, []string{"n1", "n2"})
	want := []string{"n2", "n1", "s1", "s2"}
	if len(got) != len(want) {
		t.Fatalf("长度 = %d, 期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("结果 = %v, 期望 %v", got, want)
		}
	}

	// 容量裁剪：循环写入超过 seenCap 条，长度不超过上限
	seen := []string{}
	for i := 0; i < seenCap+50; i++ {
		seen = prependSeen(seen, []string{string(rune('a'+i%26)) + strconv.Itoa(i)})
	}
	if len(seen) != seenCap {
		t.Errorf("容量裁剪失败: %d", len(seen))
	}
}

func TestSubKeyAndKeyChat(t *testing.T) {
	k1 := subKey("g:qq:123", "https://a.example/rss")
	k2 := subKey("g:qq:123", "https://b.example/rss")
	if k1 == k2 {
		t.Error("不同地址的键不应相同")
	}
	if got := keyChat(k1); got != "g:qq:123" {
		t.Errorf("keyChat = %q, 期望 g:qq:123", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("你好世界", 10); got != "你好世界" {
		t.Errorf("不超长不应截断: %q", got)
	}
	if got := truncateRunes("你好世界", 2); got != "你好…" {
		t.Errorf("截断错误: %q", got)
	}
}
