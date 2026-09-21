package groupdashboard

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"strings"
	"time"
	"unicode/utf8"
)

// dashboardWidth 看板视口宽度（竖向长图，交给 md2img 截取整页）。
const dashboardWidth = 800

// 看板风格标识（plugin.groupdashboard.style 配置值）。
const (
	styleMint     = "mint"     // 薄荷看板风（默认）
	styleMagazine = "magazine" // 杂志海报风
	styleDark     = "dark"     // 暗夜霓虹风
)

// normalizeStyle 归一化风格配置值，未知或留空回退默认薄荷风。
func normalizeStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case styleMagazine:
		return styleMagazine
	case styleDark:
		return styleDark
	default:
		return styleMint
	}
}

// groupStats 由本地消息缓冲统计出的真实数据（不依赖 AI，保证数字可信）。
type groupStats struct {
	Total        int               // 消息总量
	Participants int               // 参与人数
	Chars        int               // 字符总数
	Hours        [24]int           // 24 小时活动分布
	MaxHour      int               // 单小时最大消息数（柱高归一化用）
	StartTime    time.Time         // 最早一条消息时间
	EndTime      time.Time         // 最晚一条消息时间
	NickCounts   map[string]int    // 昵称 -> 发言条数
	AvatarByNick map[string]string // 昵称 -> 头像 URL（仅 QQ 有）
}

// buildStats 从消息快照统计看板数据。
func buildStats(msgs []digestMessage) *groupStats {
	s := &groupStats{
		NickCounts:   make(map[string]int),
		AvatarByNick: make(map[string]string),
	}
	participants := make(map[string]struct{})
	for _, m := range msgs {
		s.Total++
		s.Chars += utf8.RuneCountInString(m.Text)
		s.Hours[m.Time.Hour()]++
		nick := strings.TrimSpace(m.Nickname)
		s.NickCounts[nick]++
		key := m.UserID
		if key == "" {
			key = "nick:" + nick
		}
		participants[key] = struct{}{}
		if url := qqAvatarURL(m.UserID); url != "" && s.AvatarByNick[nick] == "" {
			s.AvatarByNick[nick] = url
		}
		if s.StartTime.IsZero() || m.Time.Before(s.StartTime) {
			s.StartTime = m.Time
		}
		if m.Time.After(s.EndTime) {
			s.EndTime = m.Time
		}
	}
	for _, c := range s.Hours {
		if c > s.MaxHour {
			s.MaxHour = c
		}
	}
	s.Participants = len(participants)
	return s
}

// RangeText 统计时段文案：同一天只显示时分，跨天带日期。
func (s *groupStats) RangeText() string {
	if s.StartTime.IsZero() {
		return "暂无消息"
	}
	if s.StartTime.Format("2006-01-02") == s.EndTime.Format("2006-01-02") {
		return s.StartTime.Format("15:04") + " - " + s.EndTime.Format("15:04")
	}
	return s.StartTime.Format("01-02 15:04") + " ~ " + s.EndTime.Format("01-02 15:04")
}

// DateText 统计日期文案（横幅右侧）。
func (s *groupStats) DateText() string {
	if s.StartTime.IsZero() {
		return ""
	}
	start := s.StartTime.Format("2006/01/02")
	if start == s.EndTime.Format("2006/01/02") {
		return start
	}
	return start + " - " + s.EndTime.Format("01/02")
}

// qqAvatarURL QQ 号对应的官方头像地址；非 QQ 用户（其他平台/无 ID）返回空。
func qqAvatarURL(userID string) string {
	if !strings.HasPrefix(userID, "qq:") {
		return ""
	}
	num := strings.TrimPrefix(userID, "qq:")
	if num == "" {
		return ""
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return "https://q1.qlogo.cn/g?nk=" + num + "&s=100"
}

// avatarPalette 头像兜底底色（按昵称哈希取色，无头像时显示首字圆标）。
var avatarPalette = []string{"#5fc9a8", "#f2a379", "#8fbdf0", "#c9a3e8", "#e8c97e", "#8fd0d8", "#f096a8", "#a8d896"}

// avatarColor 按昵称稳定取一个兜底色。
func avatarColor(nick string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(nick))
	return avatarPalette[h.Sum32()%uint32(len(avatarPalette))]
}

// avatarView 头像展示视图：有 URL 用图片（加载失败回退首字圆标），否则纯圆标。
type avatarView struct {
	URL     string
	Initial string
	Color   string
}

func newAvatarView(nick, url string) avatarView {
	v := avatarView{URL: url, Color: avatarColor(nick)}
	if r := []rune(nick); len(r) > 0 {
		v.Initial = string(r[0])
	}
	return v
}

// hourView 24 小时活动柱状图单列。
type hourView struct {
	Label   string
	Count   int
	Height  string // 百分比高度
	Peak    bool   // 是否为峰值列
	ShowVal bool   // 是否显示数值（非零列）
}

// topicView 话题焦点卡片视图。
type topicView struct {
	Num     string
	Title   string
	Summary string
	Quotes  []quoteChipView
	Tags    []string
}

// quoteChipView 话题内引用发言的小条目。
type quoteChipView struct {
	Avatar avatarView
	Nick   string
	Text   string
}

// memberView 群友画像卡片视图。
type memberView struct {
	Avatar     avatarView
	Nick       string
	Count      string // 本期发言条数（本地真实统计）
	Title      string
	Impression string
	Tags       []string
}

// quotePairView 今日金句：成员发言气泡 + 编辑点评气泡。
type quotePairView struct {
	Avatar  avatarView
	Nick    string
	Text    string
	Comment string
}

// dimView 氛围维度评分条视图。
type dimView struct {
	Name    string
	Score   int
	Width   string // 评分条宽度百分比
	Comment string
}

// dashboardView 看板 HTML 模板的完整视图数据。
type dashboardView struct {
	CSS           template.CSS // 按风格注入的样式表
	GroupName     string
	GenTime       string
	RangeText     string
	DateText      string
	StatTotal     int
	StatMembers   int
	StatTopics    int
	StatChars     int
	Hours         []hourView
	Topics        []topicView
	Members       []memberView
	Quotes        []quotePairView
	HasAtmos      bool
	AtmosSummary  string
	AtmosDims     []dimView
	AtmosAdvice   string
	EditorComment string
}

// buildDashboardView 组装看板视图：本地统计提供数字与头像，AI 报告提供内容。
func buildDashboardView(report *digestReport, stats *groupStats, groupName, style string, genTime time.Time) *dashboardView {
	v := &dashboardView{
		CSS:           styleCSS(normalizeStyle(style)),
		GroupName:     groupName,
		GenTime:       genTime.Format("2006/01/02 15:04"),
		RangeText:     stats.RangeText(),
		DateText:      stats.DateText(),
		StatTotal:     stats.Total,
		StatMembers:   stats.Participants,
		StatTopics:    len(report.Topics),
		StatChars:     stats.Chars,
		EditorComment: strings.TrimSpace(report.EditorComment),
	}

	for h, c := range stats.Hours {
		hv := hourView{Label: fmt.Sprintf("%02d", h), Count: c, ShowVal: c > 0}
		if stats.MaxHour > 0 && c > 0 {
			pct := float64(c) / float64(stats.MaxHour) * 100
			if pct < 6 {
				pct = 6
			}
			hv.Height = fmt.Sprintf("%.1f%%", pct)
		}
		if c > 0 && c == stats.MaxHour {
			hv.Peak = true
		}
		if hv.Height == "" {
			hv.Height = "4%"
		}
		v.Hours = append(v.Hours, hv)
	}

	for i, t := range report.Topics {
		tv := topicView{Num: fmt.Sprintf("%02d", i+1), Title: t.Title, Summary: t.Summary, Tags: t.Tags}
		for _, q := range t.Quotes {
			tv.Quotes = append(tv.Quotes, quoteChipView{
				Avatar: newAvatarView(q.Nickname, stats.AvatarByNick[strings.TrimSpace(q.Nickname)]),
				Nick:   q.Nickname,
				Text:   q.Text,
			})
		}
		v.Topics = append(v.Topics, tv)
	}

	for _, m := range report.Members {
		nick := strings.TrimSpace(m.Nickname)
		mv := memberView{
			Avatar:     newAvatarView(m.Nickname, stats.AvatarByNick[nick]),
			Nick:       m.Nickname,
			Count:      fmt.Sprintf("%d 条", stats.NickCounts[nick]),
			Title:      m.Title,
			Impression: m.Impression,
			Tags:       m.Tags,
		}
		v.Members = append(v.Members, mv)
	}

	for _, q := range report.Quotes {
		v.Quotes = append(v.Quotes, quotePairView{
			Avatar:  newAvatarView(q.Nickname, stats.AvatarByNick[strings.TrimSpace(q.Nickname)]),
			Nick:    q.Nickname,
			Text:    q.Text,
			Comment: q.Comment,
		})
	}

	if a := report.Atmosphere; a.Summary != "" || len(a.Dimensions) > 0 || a.Advice != "" {
		v.HasAtmos = true
		v.AtmosSummary = a.Summary
		v.AtmosAdvice = a.Advice
		for _, d := range a.Dimensions {
			score := d.Score
			if score < 0 {
				score = 0
			}
			if score > 100 {
				score = 100
			}
			v.AtmosDims = append(v.AtmosDims, dimView{
				Name:    d.Name,
				Score:   score,
				Width:   fmt.Sprintf("%d%%", score),
				Comment: d.Comment,
			})
		}
	}
	return v
}

// baseCSS 各风格共用的版式（只管布局尺寸，不涉及配色）。
const baseCSS = `
* { margin:0; padding:0; box-sizing:border-box; }
.wrap { width:800px; margin:0 auto; padding:30px 26px 22px; }
.head { display:flex; justify-content:space-between; align-items:flex-end; }
.stats { display:flex; gap:12px; margin:18px 0; }
.stat { flex:1; padding:14px 8px 12px; text-align:center; }
.stat-num { font-size:22px; font-weight:800; }
.stat-label { font-size:11px; margin-top:3px; }
.banner { padding:16px 24px; display:flex; justify-content:space-between; align-items:center; }
.banner-label { font-size:11px; margin-bottom:4px; }
.banner-time { font-size:25px; font-weight:800; letter-spacing:1px; }
.banner-date { font-size:12px; text-align:right; }
.sec { display:flex; align-items:center; margin:24px 0 12px; }
.sec-bar { width:4px; height:16px; border-radius:2px; margin-right:8px; }
.sec-title { font-size:16px; font-weight:800; }
.card { border-radius:14px; padding:18px; }
.chart { display:flex; align-items:flex-end; gap:5px; height:130px; }
.chart-col { flex:1; display:flex; flex-direction:column; align-items:center; justify-content:flex-end; gap:4px; height:100%; }
.chart-val { font-size:9px; font-weight:700; line-height:1; }
.chart-bar { width:72%; border-radius:4px 4px 2px 2px; }
.chart-x { display:flex; gap:5px; margin-top:8px; }
.chart-x span { flex:1; text-align:center; font-size:9px; }
.topic { border-radius:14px; padding:18px; margin-bottom:13px; }
.topic-head { display:flex; align-items:baseline; gap:10px; }
.topic-num { font-size:17px; font-weight:800; }
.topic-title { font-size:16px; font-weight:800; }
.topic-summary { font-size:13px; line-height:1.8; margin:8px 0 4px; }
.tq { border-radius:10px; padding:8px 12px; font-size:12px; display:flex; gap:8px; align-items:flex-start; margin-top:8px; line-height:1.6; }
.tq .av-wrap, .tq .av-fb, .tq .av { width:20px; height:20px; }
.tq .av-fb { font-size:10px; }
.tq-nick { font-weight:700; flex-shrink:0; }
.tags { display:flex; flex-wrap:wrap; gap:8px; margin-top:10px; }
.tag { font-size:11px; padding:3px 11px; border-radius:999px; }
.av-wrap { position:relative; width:42px; height:42px; flex-shrink:0; }
.av { position:absolute; left:0; top:0; width:42px; height:42px; border-radius:50%; object-fit:cover; }
.av-fb { width:42px; height:42px; border-radius:50%; display:flex; align-items:center; justify-content:center; color:#fff; font-weight:700; font-size:16px; overflow:hidden; }
.members { display:grid; grid-template-columns:1fr 1fr; gap:12px; }
.member { border-radius:14px; padding:16px; }
.member-head { display:flex; align-items:center; gap:10px; }
.member-name { font-size:14px; font-weight:800; }
.member-title { font-size:11px; margin-top:2px; }
.member-badge { font-size:10px; padding:2px 8px; margin-left:auto; flex-shrink:0; border-radius:999px; color:#fff; }
.member-imp { font-size:12px; line-height:1.7; margin:10px 0 2px; }
.quote-item { display:flex; gap:10px; margin-bottom:12px; }
.quote-bubble { padding:10px 14px; max-width:86%; border-radius:4px 14px 14px 14px; }
.quote-nick { font-size:11px; margin-bottom:4px; }
.quote-text { font-size:13px; line-height:1.7; }
.quote-cmt { display:flex; justify-content:flex-end; margin:-4px 0 16px; }
.quote-cmt-bubble { padding:8px 13px; font-size:12px; max-width:76%; line-height:1.7; border-radius:14px 4px 14px 14px; color:#fff; }
.dim { margin-bottom:12px; }
.dim-head { display:flex; justify-content:space-between; align-items:baseline; font-size:12px; margin-bottom:5px; }
.dim-name { font-weight:700; }
.dim-cmt { font-size:11px; margin-left:8px; }
.dim-score { font-weight:800; }
.dim-track { height:8px; border-radius:999px; overflow:hidden; }
.dim-fill { height:100%; border-radius:999px; }
.atmos-summary { font-size:13px; line-height:1.8; margin-bottom:14px; }
.editor-card { border-radius:14px; padding:16px 18px; margin-top:6px; }
.editor-title { font-size:13px; font-weight:800; margin-bottom:6px; }
.editor-body { font-size:13px; line-height:1.8; opacity:.96; }
.empty { font-size:12px; text-align:center; padding:14px 0; }
.foot { text-align:center; font-size:10px; margin-top:20px; line-height:1.8; }
`

// stylePalettes 各风格的配色与质感（在共用版式上叠加）。
var stylePalettes = map[string]string{
	// 薄荷看板风：奶油底 + 白卡片 + 薄荷绿主色、珊瑚色点缀（默认）
	styleMint: `
body { background:#f2f1ea; color:#253b35; font-family:"PingFang SC","Microsoft YaHei","Noto Sans SC","Helvetica Neue",Arial,sans-serif; -webkit-font-smoothing:antialiased; }
.head-title { font-size:26px; font-weight:800; letter-spacing:1px; }
.head-sub { color:#98a29b; }
.head-group { font-size:12px; color:#2aa285; margin-top:4px; }
.stat, .card, .topic, .member, .quote-bubble { background:#fff; box-shadow:0 2px 10px rgba(37,59,53,.05); }
.stat-label { color:#98a29b; }
.banner { background:linear-gradient(120deg,#43cfa9,#27a084); border-radius:16px; color:#fff; box-shadow:0 4px 14px rgba(42,162,133,.25); }
.banner-label { opacity:.85; }
.banner-date { opacity:.9; }
.sec-bar { background:#3ec9a7; }
.chart-val { color:#f0784e; }
.chart-bar { background:#bfe6da; }
.chart-bar.peak { background:linear-gradient(180deg,#ffa37e,#f0784e); }
.chart-x span { color:#a8b0aa; }
.topic-summary { color:#5c6b64; }
.tq { background:#f4f6f1; color:#42534c; }
.tq-nick { color:#2aa285; }
.tag { color:#2aa285; background:#e6f7f1; }
.member-title { color:#f0784e; }
.member-badge { background:#3ec9a7; }
.member-imp { color:#5c6b64; }
.quote-nick { color:#98a29b; }
.quote-cmt-bubble { background:linear-gradient(120deg,#43cfa9,#27a084); }
.dim-cmt { color:#98a29b; }
.dim-score { color:#2aa285; }
.dim-track { background:#eef1ea; }
.dim-fill { background:linear-gradient(90deg,#3ec9a7,#8ce4c6); }
.atmos-summary { color:#5c6b64; }
.editor-card { background:linear-gradient(120deg,#43cfa9,#27a084); color:#fff; box-shadow:0 4px 14px rgba(42,162,133,.25); }
.empty, .foot { color:#a8b0aa; }
`,
	// 杂志海报风：纸感底 + 衬线字 + 黑红撞色、直角与粗细线
	styleMagazine: `
body { background:#f6f1e7; color:#191714; font-family:"Noto Serif SC","Source Han Serif SC","Songti SC",SimSun,Georgia,serif; }
.wrap { padding:34px 30px 24px; }
.head { border-top:4px solid #191714; border-bottom:1px solid #191714; padding:16px 0 12px; }
.head-title { font-size:32px; font-weight:900; letter-spacing:6px; }
.head-sub { color:#6b6459; }
.head-group { font-size:13px; color:#c8382e; margin-top:6px; font-weight:700; letter-spacing:2px; }
.stats { gap:0; border:1px solid #191714; border-right:none; }
.stat { border-right:1px solid #191714; }
.stat-num { font-size:26px; font-weight:900; }
.stat-label { letter-spacing:2px; color:#6b6459; }
.banner { background:#191714; color:#f6f1e7; border-radius:0; }
.banner-label { letter-spacing:4px; opacity:.7; }
.banner-time { font-weight:900; letter-spacing:2px; }
.banner-date { opacity:.75; }
.sec { border-bottom:2px solid #191714; padding-bottom:6px; }
.sec-bar { display:none; }
.sec-title { font-size:19px; font-weight:900; letter-spacing:3px; }
.card, .member { background:#fdfaf3; border:1px solid #191714; border-radius:0; box-shadow:none; }
.topic { background:transparent; border-radius:0; box-shadow:none; padding:16px 4px; border-bottom:1px solid #d9cdb4; }
.topic-num { color:#c8382e; font-size:22px; font-style:italic; }
.topic-title { letter-spacing:1px; }
.topic-summary { color:#4a443b; }
.tq { background:transparent; border-radius:0; border-left:3px solid #c8382e; color:#191714; }
.tq-nick { color:#c8382e; }
.tag { color:#191714; background:transparent; border:1px solid #191714; border-radius:0; }
.member-badge { background:#c8382e; border-radius:0; }
.member-title { color:#c8382e; }
.member-imp { color:#4a443b; }
.quote-bubble { background:#fdfaf3; border:1px solid #191714; border-radius:0; box-shadow:4px 4px 0 rgba(25,23,20,.12); }
.quote-nick { color:#6b6459; }
.quote-cmt-bubble { background:#191714; border-radius:0; }
.chart-val { color:#191714; }
.chart-bar { background:#d9cdb4; border-radius:0; }
.chart-bar.peak { background:#c8382e; }
.chart-x span { color:#8a8375; }
.dim-score { color:#c8382e; }
.dim-track { background:#e6ddca; border-radius:0; }
.dim-fill { background:#c8382e; border-radius:0; }
.dim-cmt { color:#8a8375; }
.atmos-summary { color:#4a443b; }
.editor-card { background:#c8382e; color:#fff; border-radius:0; box-shadow:6px 6px 0 rgba(25,23,20,.15); }
.empty, .foot { color:#8a8375; }
.foot { border-top:1px solid #191714; padding-top:10px; margin-top:24px; }
`,
	// 暗夜霓虹风：深色底 + 霓虹青蓝渐变、发光点缀
	styleDark: `
body { background:#0e1613; color:#d9e6e0; font-family:"PingFang SC","Microsoft YaHei","Noto Sans SC","Helvetica Neue",Arial,sans-serif; -webkit-font-smoothing:antialiased; }
.head-title { font-size:26px; font-weight:800; letter-spacing:1px; color:#eafff7; }
.head-sub { color:#5f7a70; }
.head-group { font-size:12px; color:#37e0b0; margin-top:4px; }
.stat, .card, .topic, .member, .quote-bubble { background:#152420; border:1px solid #1f3a31; box-shadow:none; }
.stat-label { color:#5f7a70; }
.stat-num { color:#37e0b0; }
.banner { background:linear-gradient(120deg,#123f33,#25355c); border:1px solid #24493c; border-radius:14px; color:#eafff7; box-shadow:0 0 24px rgba(55,224,176,.15); }
.banner-label { opacity:.8; }
.banner-date { opacity:.85; }
.sec-bar { background:#37e0b0; }
.sec-title { color:#eafff7; }
.chart-val { color:#ffb37e; }
.chart-bar { background:#245443; }
.chart-bar.peak { background:linear-gradient(180deg,#ffb37e,#ff7e4e); }
.chart-x span { color:#4d6a5f; }
.topic-num { color:#37e0b0; }
.topic-title { color:#eafff7; }
.topic-summary { color:#a8c3b8; }
.tq { background:#101c18; color:#c4d9d0; }
.tq-nick { color:#37e0b0; }
.tag { color:#37e0b0; background:rgba(55,224,176,.08); border:1px solid #2a5a49; }
.member-title { color:#ffb37e; }
.member-badge { background:#225545; color:#7ff0cd; }
.member-imp { color:#a8c3b8; }
.quote-nick { color:#5f7a70; }
.quote-text { color:#d9e6e0; }
.quote-cmt-bubble { background:linear-gradient(120deg,#134636,#25355c); }
.dim-cmt { color:#5f7a70; }
.dim-score { color:#37e0b0; }
.dim-track { background:#1c2f28; }
.dim-fill { background:linear-gradient(90deg,#37e0b0,#5aa8ff); }
.atmos-summary { color:#a8c3b8; }
.editor-card { background:linear-gradient(120deg,#134636,#25355c); color:#eafff7; box-shadow:0 0 24px rgba(55,224,176,.12); }
.empty, .foot { color:#4d6a5f; }
`,
}

// styleCSS 按风格组装完整样式表（共用版式 + 风格调色板）。
func styleCSS(style string) template.CSS {
	return template.CSS(baseCSS + stylePalettes[style])
}

// dashboardTpl 看板 HTML 模板：纯内联样式无外部资源，风格通过 .CSS 注入。
var dashboardTpl = template.Must(template.New("dashboard").Parse(`{{define "av"}}{{if .URL}}<div class="av-wrap"><div class="av-fb" style="background:{{.Color}}">{{.Initial}}</div><img class="av" src="{{.URL}}" alt="" onerror="this.remove()"></div>{{else}}<div class="av-fb" style="background:{{.Color}}">{{.Initial}}</div>{{end}}{{end}}
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<style>{{.CSS}}</style>
</head>
<body>
<div class="wrap">
  <div class="head">
    <div>
      <div class="head-title">群聊日常分析看板</div>
      {{if .GroupName}}<div class="head-group">{{.GroupName}}</div>{{end}}
    </div>
    <div class="head-sub">生成时间 · {{.GenTime}}</div>
  </div>

  <div class="stats">
    <div class="stat"><div class="stat-num">{{.StatTotal}}</div><div class="stat-label">消息总量</div></div>
    <div class="stat"><div class="stat-num">{{.StatMembers}}</div><div class="stat-label">参与人数</div></div>
    <div class="stat"><div class="stat-num">{{.StatTopics}}</div><div class="stat-label">热门话题</div></div>
    <div class="stat"><div class="stat-num">{{.StatChars}}</div><div class="stat-label">字符总数</div></div>
  </div>

  <div class="banner">
    <div>
      <div class="banner-label">统计时段</div>
      <div class="banner-time">{{.RangeText}}</div>
    </div>
    <div class="banner-date">{{.DateText}}</div>
  </div>

  <div class="sec"><div class="sec-bar"></div><div class="sec-title">24 小时活动</div></div>
  <div class="card">
    <div class="chart">
      {{range .Hours}}<div class="chart-col">{{if .ShowVal}}<div class="chart-val">{{.Count}}</div>{{end}}<div class="chart-bar{{if .Peak}} peak{{end}}" style="height:{{.Height}}"></div></div>{{end}}
    </div>
    <div class="chart-x">{{range .Hours}}<span>{{.Label}}</span>{{end}}</div>
  </div>

  <div class="sec"><div class="sec-bar"></div><div class="sec-title">话题焦点</div></div>
  {{if .Topics}}{{range .Topics}}
  <div class="topic">
    <div class="topic-head"><span class="topic-num">#{{.Num}}</span><span class="topic-title">{{.Title}}</span></div>
    {{if .Summary}}<div class="topic-summary">{{.Summary}}</div>{{end}}
    {{range .Quotes}}<div class="tq">{{template "av" .Avatar}}<div><span class="tq-nick">{{.Nick}}：</span>{{.Text}}</div></div>{{end}}
    {{if .Tags}}<div class="tags">{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</div>{{end}}
  </div>
  {{end}}{{else}}<div class="card empty">本期暂无话题提炼</div>{{end}}

  <div class="sec"><div class="sec-bar"></div><div class="sec-title">群友画像</div></div>
  {{if .Members}}<div class="members">{{range .Members}}
  <div class="member">
    <div class="member-head">{{template "av" .Avatar}}<div><div class="member-name">{{.Nick}}</div>{{if .Title}}<div class="member-title">{{.Title}}</div>{{end}}</div><span class="member-badge">{{.Count}}</span></div>
    {{if .Impression}}<div class="member-imp">{{.Impression}}</div>{{end}}
    {{if .Tags}}<div class="tags">{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</div>{{end}}
  </div>
  {{end}}</div>{{else}}<div class="card empty">本期暂无群友画像</div>{{end}}

  <div class="sec"><div class="sec-bar"></div><div class="sec-title">今日金句</div></div>
  {{if .Quotes}}{{range .Quotes}}
  <div class="quote-item">{{template "av" .Avatar}}<div class="quote-bubble"><div class="quote-nick">{{.Nick}}</div><div class="quote-text">{{.Text}}</div></div></div>
  {{if .Comment}}<div class="quote-cmt"><div class="quote-cmt-bubble">💬 {{.Comment}}</div></div>{{end}}
  {{end}}{{else}}<div class="card empty">本期暂无金句</div>{{end}}

  {{if .HasAtmos}}
  <div class="sec"><div class="sec-bar"></div><div class="sec-title">群聊氛围报告</div></div>
  <div class="card">
    {{if .AtmosSummary}}<div class="atmos-summary">{{.AtmosSummary}}</div>{{end}}
    {{range .AtmosDims}}
    <div class="dim">
      <div class="dim-head"><span><span class="dim-name">{{.Name}}</span>{{if .Comment}}<span class="dim-cmt">{{.Comment}}</span>{{end}}</span><span class="dim-score">{{.Score}}</span></div>
      <div class="dim-track"><div class="dim-fill" style="width:{{.Width}}"></div></div>
    </div>
    {{end}}
    {{if .AtmosAdvice}}<div class="atmos-summary">💡 {{.AtmosAdvice}}</div>{{end}}
  </div>
  {{end}}

  {{if .EditorComment}}
  <div class="editor-card">
    <div class="editor-title">✏️ 编辑寄语</div>
    <div class="editor-body">{{.EditorComment}}</div>
  </div>
  {{end}}

  <div class="foot">Generated by AniaBot · 群聊看板插件 · {{.GenTime}}<br>由 AI 分析生成，内容仅供娱乐参考</div>
</div>
</body>
</html>
`))

// renderDashboardHTML 按指定风格渲染看板 HTML。
func renderDashboardHTML(report *digestReport, stats *groupStats, groupName, style string) (string, error) {
	var sb strings.Builder
	if err := dashboardTpl.Execute(&sb, buildDashboardView(report, stats, groupName, style, time.Now())); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// renderReportMarkdown 渲染纯 Markdown 版看板（md 发送模式 / 无 md2img 服务时的降级）。
func renderReportMarkdown(report *digestReport, stats *groupStats, groupName string) string {
	var sb strings.Builder
	sb.WriteString("# 群聊日常分析看板\n\n")
	var meta []string
	if groupName != "" {
		meta = append(meta, "群名："+groupName)
	}
	meta = append(meta, "统计时段："+stats.RangeText())
	fmt.Fprintf(&sb, "> %s · 消息 %d 条 / %d 人 / %d 字\n\n",
		strings.Join(meta, " · "), stats.Total, stats.Participants, stats.Chars)

	var acts []string
	for h, c := range stats.Hours {
		if c > 0 {
			acts = append(acts, fmt.Sprintf("%02d时 %d 条", h, c))
		}
	}
	if len(acts) > 0 {
		fmt.Fprintf(&sb, "## 24 小时活动\n\n%s\n\n", strings.Join(acts, " · "))
	}

	if len(report.Topics) > 0 {
		sb.WriteString("## 话题焦点\n\n")
		for i, t := range report.Topics {
			fmt.Fprintf(&sb, "### #%02d %s\n\n", i+1, t.Title)
			if t.Summary != "" {
				fmt.Fprintf(&sb, "%s\n\n", t.Summary)
			}
			for _, q := range t.Quotes {
				fmt.Fprintf(&sb, "- **%s**：%s\n", q.Nickname, q.Text)
			}
			if len(t.Tags) > 0 {
				fmt.Fprintf(&sb, "\n标签：%s\n", strings.Join(t.Tags, " / "))
			}
			sb.WriteString("\n")
		}
	}

	if len(report.Members) > 0 {
		sb.WriteString("## 群友画像\n\n")
		for _, m := range report.Members {
			count := stats.NickCounts[strings.TrimSpace(m.Nickname)]
			if m.Title != "" {
				fmt.Fprintf(&sb, "### %s（%s）· %d 条\n\n", m.Nickname, m.Title, count)
			} else {
				fmt.Fprintf(&sb, "### %s · %d 条\n\n", m.Nickname, count)
			}
			if m.Impression != "" {
				fmt.Fprintf(&sb, "%s\n\n", m.Impression)
			}
			if len(m.Tags) > 0 {
				fmt.Fprintf(&sb, "标签：%s\n\n", strings.Join(m.Tags, " / "))
			}
		}
	}

	if len(report.Quotes) > 0 {
		sb.WriteString("## 今日金句\n\n")
		for _, q := range report.Quotes {
			fmt.Fprintf(&sb, "- 「%s」 —— %s\n", q.Text, q.Nickname)
			if q.Comment != "" {
				fmt.Fprintf(&sb, "  > %s\n", q.Comment)
			}
		}
		sb.WriteString("\n")
	}

	if a := report.Atmosphere; a.Summary != "" || len(a.Dimensions) > 0 {
		sb.WriteString("## 群聊氛围报告\n\n")
		if a.Summary != "" {
			fmt.Fprintf(&sb, "%s\n\n", a.Summary)
		}
		for _, d := range a.Dimensions {
			score := d.Score
			if score < 0 {
				score = 0
			}
			if score > 100 {
				score = 100
			}
			fmt.Fprintf(&sb, "- **%s %d/100**：%s\n", d.Name, score, d.Comment)
		}
		if a.Advice != "" {
			fmt.Fprintf(&sb, "\n💡 %s\n", a.Advice)
		}
		sb.WriteString("\n")
	}

	if report.EditorComment != "" {
		fmt.Fprintf(&sb, "## 编辑寄语\n\n%s\n\n", report.EditorComment)
	}
	fmt.Fprintf(&sb, "---\n\n*Generated by AniaBot · 群聊看板插件 · %s · AI 生成内容仅供娱乐参考*\n", time.Now().Format("2006/01/02 15:04"))
	return strings.TrimSpace(sb.String())
}
