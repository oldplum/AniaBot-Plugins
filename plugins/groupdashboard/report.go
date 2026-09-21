package groupdashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// digestSystemPrompt 看板分析的系统提示词（内置，不暴露为配置项）：
// 要求模型只输出符合 digestReport 结构的 JSON，本地负责统计真实数据并渲染看板。
const digestSystemPrompt = `你是一名群聊数据分析师兼群刊主编。请阅读群聊记录，输出一份 JSON 格式的群聊日常分析报告。
只输出 JSON 本身，禁止输出任何解释、markdown 代码块标记或其他文字。
JSON 结构如下（所有字段必填，内容用中文，nickname 必须逐字取自聊天记录中的昵称）：
{
  "topics": [
    {
      "title": "话题标题（10 字内）",
      "summary": "话题描述（80 字内，概括讨论内容与各方观点）",
      "quotes": [
        {"nickname": "发言者昵称", "text": "代表性发言（尽量原文摘录，30 字内）"}
      ],
      "tags": ["标签1", "标签2"]
    }
  ],
  "members": [
    {
      "nickname": "昵称",
      "title": "人设称号（6 字内，如「气氛担当」「技术百科」）",
      "impression": "画像描述（50 字内，概括发言风格与性格特点）",
      "tags": ["标签1", "标签2"]
    }
  ],
  "quotes": [
    {"nickname": "发言者昵称", "text": "金句原文（40 字内）", "comment": "编辑点评（40 字内，幽默风趣）"}
  ],
  "atmosphere": {
    "summary": "群聊整体氛围概括（60 字内）",
    "dimensions": [
      {"name": "维度名（活跃度/话题集中度/互动性/欢乐值等）", "score": 85, "comment": "简短点评（20 字内）"}
    ],
    "advice": "给群友的一句话建议（30 字内）"
  },
  "editor_comment": "群刊编辑寄语（60 字内，温暖有趣）"
}
数量要求：topics 3~5 个按热度排序；members 4~8 位最活跃或最有特点的群友；quotes 3~6 条；dimensions 恰好 4 个（score 为 0-100 整数）。
风格要求：语气轻松幽默但不过度玩梗，引用尽量用原文，不要虚构没出现过的昵称。`

// reportQuote 话题里引用的一条发言。
type reportQuote struct {
	Nickname string `json:"nickname"`
	Text     string `json:"text"`
}

// reportTopic 话题焦点。
type reportTopic struct {
	Title   string        `json:"title"`
	Summary string        `json:"summary"`
	Quotes  []reportQuote `json:"quotes"`
	Tags    []string      `json:"tags"`
}

// reportMember 群友画像。
type reportMember struct {
	Nickname   string   `json:"nickname"`
	Title      string   `json:"title"`
	Impression string   `json:"impression"`
	Tags       []string `json:"tags"`
}

// reportGoldenQuote 今日金句（附编辑点评）。
type reportGoldenQuote struct {
	Nickname string `json:"nickname"`
	Text     string `json:"text"`
	Comment  string `json:"comment"`
}

// reportDimension 氛围维度评分。
type reportDimension struct {
	Name    string `json:"name"`
	Score   int    `json:"score"`
	Comment string `json:"comment"`
}

// reportAtmosphere 群聊氛围报告。
type reportAtmosphere struct {
	Summary    string            `json:"summary"`
	Dimensions []reportDimension `json:"dimensions"`
	Advice     string            `json:"advice"`
}

// digestReport AI 输出的看板分析报告（JSON 反序列化目标）。
type digestReport struct {
	Topics        []reportTopic      `json:"topics"`
	Members       []reportMember     `json:"members"`
	Quotes        []reportGoldenQuote `json:"quotes"`
	Atmosphere    reportAtmosphere   `json:"atmosphere"`
	EditorComment string             `json:"editor_comment"`
}

// generateDigest 用 AI 根据消息快照生成结构化看板报告。
func (p *GroupDashboardPlugin) generateDigest(ctx context.Context, groupName string, msgs []digestMessage) (*digestReport, error) {
	if p.chat == nil {
		return nil, errors.New("AI 对话未配置，无法生成看板")
	}

	stats := buildStats(msgs)
	var sb strings.Builder
	sb.WriteString("请根据以下群聊记录生成本期群聊分析报告。\n")
	if groupName != "" {
		sb.WriteString("群名：" + groupName + "。\n")
	}
	fmt.Fprintf(&sb, "统计时段：%s。\n", stats.RangeText())
	fmt.Fprintf(&sb, "共 %d 条消息、%d 位参与者、约 %d 字（按时间顺序）：\n\n", stats.Total, stats.Participants, stats.Chars)
	shown := 0
	for i, m := range msgs {
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "%d. [%s] %s：%s\n", i+1, m.Time.Format("01-02 15:04"), m.Nickname, text)
		shown++
	}
	if shown == 0 {
		return nil, errors.New("没有可用的群聊消息文本")
	}
	sb.WriteString("\n请直接输出符合要求的 JSON 报告，不要输出其他内容。")

	// 复用同一个 ChatBot，生成前清空历史，保证每期看板互不串上下文
	if err := p.chat.ClearHistory(ctx); err != nil {
		return nil, fmt.Errorf("重置 AI 会话失败: %w", err)
	}
	// 只传空参数：不设输出上限与采样参数，跟随模型 API 默认
	opts := aichat.ChatOptions{}
	resp, _, err := p.chat.Chat(ctx, sb.String(), llmtool.CallBackFuncs{}, opts)
	if err != nil {
		return nil, fmt.Errorf("AI 生成看板失败: %w", err)
	}
	report, perr := parseReportJSON(resp)
	if perr == nil {
		return report, nil
	}
	// 首次输出不是合法 JSON：借同一会话追问一次，模型能看到自己上一条回答
	p.Logger.Warn("看板 AI 输出不是合法 JSON，重试一次", "error", perr.Error())
	retry, _, rerr := p.chat.Chat(ctx, "上面的输出不是合法 JSON。请重新只输出符合要求的 JSON，不要任何多余文字。",
		llmtool.CallBackFuncs{}, opts)
	if rerr != nil {
		return nil, fmt.Errorf("AI 生成看板失败: %w", rerr)
	}
	report, perr = parseReportJSON(retry)
	if perr != nil {
		p.Logger.Error("看板 AI 输出无法解析为 JSON", "raw_head", truncateRunes(strings.TrimSpace(resp), 500))
		return nil, errors.New("AI 输出无法解析为 JSON 报告")
	}
	return report, nil
}

// parseReportJSON 从模型输出中提取并解析看板报告 JSON。
// 容忍模型输出 markdown 代码块围栏或前后夹杂的说明文字。
func parseReportJSON(resp string) (*digestReport, error) {
	raw := extractJSON(resp)
	if raw == "" {
		return nil, errors.New("输出中未找到 JSON 对象")
	}
	var report digestReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	if len(report.Topics) == 0 && len(report.Members) == 0 && len(report.Quotes) == 0 {
		return nil, errors.New("报告内容为空")
	}
	return &report, nil
}

// extractJSON 截取文本中第一个 '{' 到最后一个 '}' 之间的内容（先去掉代码围栏）。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```JSON")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

// deliver 按配置的发送形式把看板发到群：看板图片或 Markdown 文本文件。
func (p *GroupDashboardPlugin) deliver(ctx context.Context, b bot.Bot, gid message.QID, report *digestReport, msgs []digestMessage, groupName string) error {
	stats := buildStats(msgs)
	if strings.EqualFold(p.cfg.SendMode, "image") {
		return p.sendDashboardImage(ctx, b, gid, report, stats, groupName)
	}
	return p.sendMarkdownFile(b, gid, renderReportMarkdown(report, stats, groupName))
}

// sendMarkdownFile 把看板正文作为 Markdown 文件发送。
func (p *GroupDashboardPlugin) sendMarkdownFile(b bot.Bot, gid message.QID, md string) error {
	name := fmt.Sprintf("群聊看板-%s.md", time.Now().Format("20060102-150405"))
	b64 := base64.StdEncoding.EncodeToString([]byte(md))
	chain := msgchain.Builder().Group().Text("📰 本期群聊看板已生成～").FileBase64(name, b64)
	if _, ok := b.SendGroupMsg(gid, chain.Build()); !ok {
		return errors.New("发送看板 Markdown 文件失败")
	}
	return nil
}

// sendDashboardImage 渲染看板 HTML，调用本地 md2img-api 的 /render-html 截图后发送。
func (p *GroupDashboardPlugin) sendDashboardImage(ctx context.Context, b bot.Bot, gid message.QID, report *digestReport, stats *groupStats, groupName string) error {
	if p.RestyClient == nil {
		return errors.New("HTTP 客户端不可用，无法调用 md2img 服务")
	}
	baseURL := strings.TrimSpace(p.cfg.MD2ImgURL)
	if baseURL == "" {
		return errors.New("未配置 md2img 服务地址（plugin.groupdashboard.md2img_url）")
	}
	renderURL := strings.TrimRight(baseURL, "/") + "/render-html"

	html, err := renderDashboardHTML(report, stats, groupName, p.cfg.Style)
	if err != nil {
		return fmt.Errorf("渲染看板失败: %w", err)
	}

	resp, err := p.RestyClient.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]any{
			"html":     html,
			"width":    dashboardWidth,
			"scale":    2,
			"fullPage": true,
		}).
		Post(renderURL)
	if err != nil {
		return fmt.Errorf("调用 md2img 服务失败: %w", err)
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("md2img 服务返回异常状态码 %d", resp.StatusCode())
	}
	body := resp.Body()
	if len(body) == 0 {
		return errors.New("md2img 服务返回空内容")
	}

	b64 := base64.StdEncoding.EncodeToString(body)
	chain := msgchain.Builder().Group().Text("📰 本期群聊看板已生成～").ImageBase64(b64)
	if _, ok := b.SendGroupMsg(gid, chain.Build()); !ok {
		return errors.New("发送看板图片失败")
	}
	return nil
}

// notifyError 记录失败日志并简短告知群成员。
func (p *GroupDashboardPlugin) notifyError(b bot.Bot, gid message.QID, err error) {
	p.Logger.Error("生成看板失败", "group", gid.String(), "error", err.Error())
	chain := msgchain.Builder().Group().Text("群聊看板生成失败：" + err.Error())
	b.SendGroupMsg(gid, chain.Build())
}
