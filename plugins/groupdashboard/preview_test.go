package groupdashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGeneratePreview 本地预览用：PREVIEW=1 go test ./plugins/groupdashboard -run TestGeneratePreview
// 会在仓库根 tmp_preview/ 下生成三种风格的看板 HTML（含模拟数据），供人工检查渲染效果。
func TestGeneratePreview(t *testing.T) {
	if os.Getenv("PREVIEW") == "" {
		t.Skip("设置 PREVIEW=1 后生成本地预览 HTML")
	}
	users := []struct{ id, nick string }{
		{"qq:2207739460", "汤圆"},
		{"qq:123456789", "阿泽"},
		{"qq:987654321", "Miko"},
		{"qq:555000111", "芝士蛋糕"},
		{"qq:77889900", "摸鱼大师"},
		{"", "潜水怪"},
	}
	texts := []string{
		"晚上吃什么，纠结了半小时", "火锅！必须火锅", "天冷了只想吃热乎的", "我投烧烤一票",
		"这家店的辣椒酱绝了", "新模型发布了吧，实测比上代聪明", "写代码写到头秃，谁来救我",
		"刚跑完 10 公里，累瘫", "周末有人去看展吗", "今天的月亮好圆", "CPU 都干冒烟了",
		"这个 bug 抓了一下午，结果是少了个分号", "群主什么时候更新插件", "笑死，我家猫把键盘踩乱了",
	}
	var msgs []digestMessage
	hourDist := map[int]int{19: 12, 20: 25, 21: 38, 22: 30, 23: 15}
	for h, n := range hourDist {
		for i := 0; i < n; i++ {
			u := users[(h*7+i*3)%len(users)]
			msgs = append(msgs, digestMessage{
				Time:     time.Date(2026, 9, 18, h, (i*7)%60, 0, 0, time.Local),
				UserID:   u.id,
				Nickname: u.nick,
				Text:     texts[(h*5+i)%len(texts)],
			})
		}
	}
	report := &digestReport{
		Topics: []reportTopic{
			{
				Title:   "晚饭吃什么",
				Summary: "入秋后的第一顿晚饭引发了火锅派与烧烤派的正面对决，最终辣椒酱成为最大赢家。",
				Quotes: []reportQuote{
					{Nickname: "汤圆", Text: "火锅！必须火锅"},
					{Nickname: "阿泽", Text: "我投烧烤一票"},
				},
				Tags: []string{"美食", "日常", "入秋"},
			},
			{
				Title:   "新模型实测",
				Summary: "大家讨论了新发布模型的实测体验，写代码与抓 bug 场景被反复点名。",
				Quotes: []reportQuote{
					{Nickname: "摸鱼大师", Text: "实测比上代聪明"},
					{Nickname: "阿泽", Text: "抓了一下午的 bug 是少了个分号"},
				},
				Tags: []string{"AI", "编程"},
			},
			{
				Title:   "周末去哪",
				Summary: "看展、跑步、撸猫，群友的周末计划高度重合又完全不同。",
				Quotes:  []reportQuote{{Nickname: "芝士蛋糕", Text: "周末有人去看展吗"}},
				Tags:    []string{"周末", "生活"},
			},
		},
		Members: []reportMember{
			{Nickname: "汤圆", Title: "气氛担当", Impression: "群里最活跃的声音，火锅事业头号代言人", Tags: []string{"干饭", "活跃"}},
			{Nickname: "阿泽", Title: "技术百科", Impression: "从模型评测到分号 debug 无所不知", Tags: []string{"编程", "实测"}},
			{Nickname: "摸鱼大师", Title: "冲浪先锋", Impression: "新事物第一现场的常客，潜水时间最长", Tags: []string{"资讯"}},
			{Nickname: "芝士蛋糕", Title: "生活美学家", Impression: "看展跑步两不误，把日子过成诗", Tags: []string{"文艺", "运动"}},
			{Nickname: "潜水怪", Title: "幕后观众", Impression: "常年潜水，但每条消息都有点赞", Tags: []string{"潜水"}},
		},
		Quotes: []reportGoldenQuote{
			{Nickname: "阿泽", Text: "这个 bug 抓了一下午，结果是少了个分号", Comment: "分号虽小， kills 最狠"},
			{Nickname: "汤圆", Text: "天冷了只想吃热乎的", Comment: "人间真实，胃是第一个入秋的器官"},
			{Nickname: "摸鱼大师", Text: "CPU 都干冒烟了", Comment: "建议群友和人一起降降温"},
		},
		Atmosphere: reportAtmosphere{
			Summary: "今晚群聊以美食开场、技术收尾，整体轻松欢乐，争论激烈但气氛友好。",
			Dimensions: []reportDimension{
				{Name: "活跃度", Score: 92, Comment: "话题一个接一个"},
				{Name: "话题集中度", Score: 76, Comment: "吃与代码齐飞"},
				{Name: "互动性", Score: 85, Comment: "接梗速度极快"},
				{Name: "欢乐值", Score: 95, Comment: "笑点密集预警"},
			},
			Advice: "多喝水，少熬夜，火锅要趁热",
		},
		EditorComment: "今晚的群像火锅沸腾：一半人在干饭，一半人在 debug，而分号为这一切画上了句号。明天见！",
	}
	stats := buildStats(msgs)
	for _, style := range []string{styleMint, styleMagazine, styleDark} {
		html, err := renderDashboardHTML(report, stats, "AniaBot 交流群", style)
		if err != nil {
			t.Fatalf("渲染 %s 失败: %v", style, err)
		}
		dir := filepath.Join("..", "..", "tmp_preview")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(dir, "preview-"+style+".html")
		if err := os.WriteFile(name, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("written %s", name)
	}
}
