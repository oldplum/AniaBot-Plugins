package eew

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
	"github.com/spf13/viper"
)

type EEWPlugin struct {
	plugin.Meta
	cfg eewConfig

	mu            sync.Mutex
	cancelConn    context.CancelFunc
	runningSource string
	runningMode   string // "websocket" 或 "http_polling"
	connected     bool
	lastMsgTime   time.Time

	// 记录已推送报数 EventID_ReportNum -> bool
	pushedReports sync.Map
}

func NewPlugin() *EEWPlugin {
	return &EEWPlugin{
		Meta: plugin.Meta{
			Name:      "地震预警插件",
			HelpWords: "地震预警实时推送及防灾查询。发送 /eew 或 /地震 获取最新地震目录，发送 /weather 或 /天气 获取全国气象排行，发送 /eew status 查看状态",
			Order:     plugin.LevelNormal,
			ShowFor:   plugininfo.ShowForGroup | plugininfo.ShowForFriend,
			Author:    "oldplum",
			Version:   "1.3.1",
		},
	}
}

func (p *EEWPlugin) Start(ctx context.Context, cfg *viper.Viper) error {
	p.Logger.Info("地震预警插件初始化",
		"enable", p.cfg.Enable,
		"source", p.cfg.Source,
		"connection_type", p.cfg.ConnectionType,
		"poll_interval", p.cfg.PollInterval,
		"weather_cron_enable", p.cfg.WeatherCronEnable,
		"weather_cron", p.cfg.WeatherCron,
		"min_magnitude", p.cfg.MinMagnitude,
		"min_intensity", p.cfg.MinIntensity,
		"push_strategy", p.cfg.PushStrategy,
		"focus_mode", p.cfg.FocusMode,
		"location_enable", p.cfg.LocationEnable,
		"location_name", p.cfg.LocationName,
		"location_lat", p.cfg.LocationLat,
		"location_lng", p.cfg.LocationLng,
		"min_local_intensity", p.cfg.MinLocalIntensity,
		"groups", p.cfg.Groups,
		"friends", p.cfg.Friends,
	)
	return nil
}

func (p *EEWPlugin) StartCron(ctx context.Context, bot bot.Bot, c plugin.CronManager) error {
	if !p.cfg.Enable || !p.cfg.WeatherCronEnable {
		return nil
	}

	cronExpr := strings.TrimSpace(p.cfg.WeatherCron)
	if cronExpr == "" {
		p.Logger.Warn("已开启气象定时播报，但 Cron 表达式为空")
		return nil
	}

	_, err := c.AddFunc(cronExpr, func() {
		p.Logger.Info("触发定时气象实况播报任务")
		p.broadcastWeatherCron(bot)
	})
	if err != nil {
		p.Logger.Error("注册气象定时播报 Cron 任务失败", "cron", cronExpr, "error", err)
		return err
	}

	p.Logger.Info("已成功注册气象定时播报 Cron 任务", "cron", cronExpr)
	return nil
}

func (p *EEWPlugin) broadcastWeatherCron(b bot.Bot) {
	urlStr := "https://api.wolfx.jp/weather_rank.json"
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://wolfx.jp")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.Logger.Error("定时气象播报获取数据失败", "error", err)
		return
	}
	defer resp.Body.Close()

	var rawMap map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rawMap); err != nil {
		p.Logger.Error("定时气象播报解析数据失败", "error", err)
		return
	}

	var latestKey string
	for k := range rawMap {
		if k != "md5" && k > latestKey {
			latestKey = k
		}
	}

	if latestKey == "" {
		return
	}

	var hourRank WeatherHourRank
	if err := json.Unmarshal(rawMap[latestKey], &hourRank); err != nil {
		return
	}

	timeStr := latestKey
	if len(latestKey) == 12 {
		timeStr = fmt.Sprintf("%s-%s-%s %s:%s",
			latestKey[0:4], latestKey[4:6], latestKey[6:8], latestKey[8:10], latestKey[10:12])
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🌤️【全国气象实况准点播报 (%s)】\n", timeStr))
	sb.WriteString("--------------------------------\n")

	if len(hourRank.TempRank) > 0 {
		sb.WriteString("🔥【最高气温 Top 5】\n")
		n := 5
		if len(hourRank.TempRank) < n {
			n = len(hourRank.TempRank)
		}
		for i := 0; i < n; i++ {
			item := hourRank.TempRank[i]
			sb.WriteString(fmt.Sprintf("%d. %s %s: %s\n", i+1, item.Province, item.City, item.Value))
		}
		sb.WriteString("\n")
	}

	if len(hourRank.RainRank) > 0 {
		sb.WriteString("🌧️【最大降水 Top 5】\n")
		n := 5
		if len(hourRank.RainRank) < n {
			n = len(hourRank.RainRank)
		}
		for i := 0; i < n; i++ {
			item := hourRank.RainRank[i]
			sb.WriteString(fmt.Sprintf("%d. %s %s: %s\n", i+1, item.Province, item.City, item.Value))
		}
		sb.WriteString("\n")
	}

	if len(hourRank.WindSRank) > 0 {
		sb.WriteString("💨【最大风速 Top 5】\n")
		n := 5
		if len(hourRank.WindSRank) < n {
			n = len(hourRank.WindSRank)
		}
		for i := 0; i < n; i++ {
			item := hourRank.WindSRank[i]
			sb.WriteString(fmt.Sprintf("%d. %s %s: %s\n", i+1, item.Province, item.City, item.Value))
		}
	}

	text := strings.TrimSpace(sb.String())

	// 气象播报的目标群与好友严格独立控制，不隐式复用基础设置，防止误发
	groups := p.cfg.WeatherCronGroups
	friends := p.cfg.WeatherCronFriends

	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		gid := message.FromString(g)
		builder := msgchain.Builder().Group()
		builder.Text(text)
		if _, ok := b.SendGroupMsg(gid, builder.Build()); ok {
			p.Logger.Info("定时气象实况成功播报至群聊", "group", gid)
		}
	}

	for _, f := range friends {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		fid := message.FromString(f)
		builder := msgchain.Builder().Friend()
		builder.Text(text)
		if _, ok := b.SendFriendMsg(fid, builder.Build()); ok {
			p.Logger.Info("定时气象实况成功播报至好友", "friend", fid)
		}
	}
}

func (p *EEWPlugin) Awake(ctx context.Context, bot bot.Bot) error {
	if !p.cfg.Enable {
		p.Logger.Info("地震预警插件已在配置中禁用")
		return nil
	}

	connCtx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelConn = cancel
	p.runningSource = p.cfg.Source
	p.mu.Unlock()

	go p.runConnManager(connCtx, bot)
	go p.watchConfigLoop(connCtx, bot)

	return nil
}

func (p *EEWPlugin) watchConfigLoop(ctx context.Context, bot bot.Bot) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			currentSource := p.cfg.Source
			runningSource := p.runningSource
			enabled := p.cfg.Enable
			p.mu.Unlock()

			if enabled && currentSource != runningSource {
				p.Logger.Info("检测到预警数据源变更，重新启动连接", "old", runningSource, "new", currentSource)
				p.restartConn(ctx, bot)
			}
		}
	}
}

func (p *EEWPlugin) restartConn(ctx context.Context, bot bot.Bot) {
	p.mu.Lock()
	if p.cancelConn != nil {
		p.cancelConn()
	}
	connCtx, cancel := context.WithCancel(context.Background())
	p.cancelConn = cancel
	p.runningSource = p.cfg.Source
	p.mu.Unlock()

	go p.runConnManager(connCtx, bot)
}

func (p *EEWPlugin) getWSURL(source string) string {
	switch strings.ToLower(source) {
	case "sc_eew":
		return "wss://ws-api.wolfx.jp/sc_eew"
	case "cenc_eew":
		return "wss://ws-api.wolfx.jp/cenc_eew"
	case "jma_eew":
		return "wss://ws-api.wolfx.jp/jma_eew"
	case "fj_eew":
		return "wss://ws-api.wolfx.jp/fj_eew"
	case "cq_eew":
		return "wss://ws-api.wolfx.jp/cq_eew"
	case "all_eew":
		return "wss://ws-api.wolfx.jp/all_eew"
	default:
		return "wss://ws-api.wolfx.jp/sc_eew"
	}
}

func (p *EEWPlugin) getHTTPURLs(source string) []string {
	switch strings.ToLower(source) {
	case "sc_eew":
		return []string{"https://api.wolfx.jp/sc_eew.json"}
	case "cenc_eew":
		return []string{"https://api.wolfx.jp/cenc_eew.json"}
	case "jma_eew":
		return []string{"https://api.wolfx.jp/jma_eew.json"}
	case "fj_eew":
		return []string{"https://api.wolfx.jp/fj_eew.json"}
	case "cq_eew":
		return []string{"https://api.wolfx.jp/cq_eew.json"}
	case "all_eew":
		return []string{
			"https://api.wolfx.jp/sc_eew.json",
			"https://api.wolfx.jp/cenc_eew.json",
			"https://api.wolfx.jp/jma_eew.json",
			"https://api.wolfx.jp/fj_eew.json",
			"https://api.wolfx.jp/cq_eew.json",
		}
	default:
		return []string{"https://api.wolfx.jp/sc_eew.json"}
	}
}

func (p *EEWPlugin) runConnManager(ctx context.Context, bot bot.Bot) {
	connType := strings.ToLower(p.cfg.ConnectionType)
	if connType == "http_polling" {
		p.Logger.Info("配置为 HTTP 轮询模式，启动高频轮询任务")
		p.runHTTPPollingLoop(ctx, bot)
		return
	}

	wsSuccess := p.runWSLoop(ctx, bot)

	if !wsSuccess && (connType == "auto" || connType == "") {
		select {
		case <-ctx.Done():
			return
		default:
		}
		p.Logger.Warn("WebSocket 被 Cloudflare 拦截 (403)，自动无缝降级切换至 HTTP 高频轮询模式保底")
		p.runHTTPPollingLoop(ctx, bot)
	}
}

func (p *EEWPlugin) runWSLoop(ctx context.Context, bot bot.Bot) bool {
	backoff := time.Second * 2
	maxBackoff := time.Minute
	forbiddenCount := 0

	for {
		select {
		case <-ctx.Done():
			p.setConnected(false, "")
			return true
		default:
		}

		p.mu.Lock()
		source := p.cfg.Source
		p.mu.Unlock()

		urlStr := p.getWSURL(source)
		p.Logger.Info("正在连接 Wolfx 地震预警 WebSocket", "url", urlStr)

		dialer := websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: 15 * time.Second,
		}

		header := make(http.Header)
		header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
		header.Set("Origin", "https://wolfx.jp")
		header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

		conn, resp, err := dialer.DialContext(ctx, urlStr, header)
		if err != nil {
			is403 := false
			if resp != nil {
				if resp.StatusCode == http.StatusForbidden {
					is403 = true
				}
				p.Logger.Error("连接 Wolfx WebSocket 失败", "url", urlStr, "status", resp.Status, "status_code", resp.StatusCode, "error", err)
			} else {
				p.Logger.Error("连接 Wolfx WebSocket 失败", "url", urlStr, "error", err)
			}
			p.setConnected(false, "")

			if is403 {
				forbiddenCount++
				if forbiddenCount >= 2 {
					return false
				}
			}

			select {
			case <-ctx.Done():
				return true
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		}

		p.Logger.Info("Wolfx 地震预警 WebSocket 连接成功", "url", urlStr)
		p.setConnected(true, "websocket")
		backoff = time.Second * 2
		forbiddenCount = 0

		err = p.readLoop(ctx, bot, conn)
		conn.Close()
		p.setConnected(false, "")

		if ctx.Err() != nil {
			return true
		}

		p.Logger.Warn("Wolfx WebSocket 连接断开，准备重连", "error", err)
		select {
		case <-ctx.Done():
			return true
		case <-time.After(backoff):
		}
	}
}

func (p *EEWPlugin) setConnected(connected bool, mode string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = connected
	p.runningMode = mode
}

func (p *EEWPlugin) readLoop(ctx context.Context, bot bot.Bot, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		p.mu.Lock()
		p.lastMsgTime = time.Now()
		p.mu.Unlock()

		var event EEWEvent
		if err := json.Unmarshal(data, &event); err != nil {
			p.Logger.Debug("解析 WS 消息跳过", "raw", string(data), "error", err)
			continue
		}

		if event.Type == "heartbeat" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			continue
		}

		if event.Type == "pong" {
			continue
		}

		p.processEEWEvent(bot, event)
	}
}

func (p *EEWPlugin) runHTTPPollingLoop(ctx context.Context, bot bot.Bot) {
	intervalSec := p.cfg.PollInterval
	if intervalSec <= 0 {
		intervalSec = 2
	}
	p.Logger.Info("启动 HTTP 预警高频轮询任务", "interval_sec", intervalSec, "source", p.cfg.Source)

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	p.setConnected(true, "http_polling")

	for {
		select {
		case <-ctx.Done():
			p.setConnected(false, "")
			return
		case <-ticker.C:
			p.fetchHTTPOnce(bot)
		}
	}
}

func (p *EEWPlugin) fetchHTTPOnce(bot bot.Bot) {
	p.mu.Lock()
	source := p.cfg.Source
	p.mu.Unlock()

	urls := p.getHTTPURLs(source)
	for _, urlStr := range urls {
		req, err := http.NewRequest("GET", urlStr, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
		req.Header.Set("Origin", "https://wolfx.jp")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			p.Logger.Debug("HTTP 轮询拉取失败", "url", urlStr, "error", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			p.Logger.Debug("HTTP 轮询状态码非 200", "url", urlStr, "status", resp.Status)
			continue
		}

		var event EEWEvent
		err = json.NewDecoder(resp.Body).Decode(&event)
		resp.Body.Close()
		if err != nil {
			p.Logger.Debug("解析 HTTP 预警 JSON 失败", "url", urlStr, "error", err)
			continue
		}

		p.mu.Lock()
		p.lastMsgTime = time.Now()
		p.mu.Unlock()

		p.processEEWEvent(bot, event)
	}
}

func parseTimeMin(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ":") {
		var h, m int
		if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err == nil {
			if h == 24 {
				h = 0
			}
			return (h%24)*60 + m, true
		}
	} else {
		if h, err := strconv.Atoi(s); err == nil {
			if h == 24 {
				h = 0
			}
			return (h%24)*60, true
		}
	}
	return 0, false
}

// isQuietTime 检查当前时刻是否在免打扰时间段内 (如 23:00 ~ 07:00 或 00:00 ~ 07:00)
func isQuietTime(now time.Time, startStr, endStr string) bool {
	startMin, ok1 := parseTimeMin(startStr)
	endMin, ok2 := parseTimeMin(endStr)
	if !ok1 || !ok2 {
		return false
	}
	nowMin := now.Hour()*60 + now.Minute()

	if startMin < endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	return nowMin >= startMin || nowMin < endMin
}

func (p *EEWPlugin) processEEWEvent(bot bot.Bot, event EEWEvent) {
	mag := event.GetMagnitude()
	loc := event.GetLocation()

	if event.EventID == "" && mag == 0 {
		return
	}

	reportNum := event.ReportNum
	if reportNum <= 0 {
		reportNum = 1
	}

	// 0. 历史旧预警过滤 (发报/发震时间超过设定的历史时限分钟数，如 10 分钟)
	if p.cfg.MaxAgeMinutes > 0 {
		evtTime := event.GetEventTime()
		if evtTime.IsZero() {
			p.Logger.Warn("无法解析预警事件时间，静默忽略旧数据", "event_id", event.EventID)
			return
		}
		if time.Since(evtTime) > time.Duration(p.cfg.MaxAgeMinutes)*time.Minute {
			p.Logger.Info("预警发报时间超过历史时限，静默忽略旧数据",
				"event_id", event.EventID,
				"event_time", evtTime.Format("2006-01-02 15:04:05"),
				"max_age_minutes", p.cfg.MaxAgeMinutes,
			)
			return
		}
	}

	// 1. 去重检查：防止 HTTP 高频轮询每 2 秒重复评估同一个事件报数刷屏日志
	dedupKey := fmt.Sprintf("%s_%d", event.EventID, reportNum)
	if _, loaded := p.pushedReports.LoadOrStore(dedupKey, true); loaded {
		return
	}

	// 1. 震级过滤
	if mag > 0 && mag < p.cfg.MinMagnitude {
		p.Logger.Info("预警震级低于阈值，已过滤", "magnitude", mag, "min_magnitude", p.cfg.MinMagnitude)
		return
	}

	// 2. 烈度过滤
	if p.cfg.MinIntensity > 0 {
		intensityNum := event.GetIntensityNum()
		if intensityNum > 0 && intensityNum < p.cfg.MinIntensity {
			p.Logger.Info("预警烈度低于门槛，已过滤", "intensity", intensityNum, "min_intensity", p.cfg.MinIntensity)
			return
		}
	}

	// 3. 地区/关切关键词过滤
	if strings.ToLower(p.cfg.FocusMode) == "keyword_only" && len(p.cfg.FocusKeywords) > 0 {
		matched := false
		for _, kw := range p.cfg.FocusKeywords {
			kw = strings.TrimSpace(kw)
			if kw != "" && strings.Contains(loc, kw) {
				matched = true
				break
			}
		}
		if !matched {
			p.Logger.Info("震中不包含关注地区关键词，已过滤", "location", loc, "keywords", p.cfg.FocusKeywords)
			return
		}
	}

	// 4. 夜间免打扰过滤
	if p.cfg.QuietHoursEnable {
		if isQuietTime(time.Now(), p.cfg.QuietHoursStart, p.cfg.QuietHoursEnd) {
			if mag < p.cfg.QuietMinMagnitude {
				p.Logger.Info("处于夜间免打扰时段且震级未达强震门槛，已静音过滤", "magnitude", mag, "quiet_threshold", p.cfg.QuietMinMagnitude)
				return
			}
		}
	}

	// 5. 本地烈度门槛过滤
	var localDist float64
	var localIntensity float64
	hasLocalCalc := p.cfg.LocationEnable && (p.cfg.LocationLat != 0 || p.cfg.LocationLng != 0) && (event.Latitude != 0 || event.Longitude != 0)
	if hasLocalCalc {
		localDist = CalcDistance(p.cfg.LocationLat, p.cfg.LocationLng, event.Latitude, event.Longitude)
		depth := 10.0
		if event.Depth != nil && *event.Depth > 0 {
			depth = *event.Depth
		}
		localIntensity = CalcLocalIntensity(mag, localDist, depth)
		if p.cfg.MinLocalIntensity > 0 && localIntensity < p.cfg.MinLocalIntensity {
			p.Logger.Info("地震预警本地预估烈度低于设定门槛，已过滤", "local_intensity", localIntensity, "min_local_intensity", p.cfg.MinLocalIntensity)
			return
		}
	}

	// 6. 推送策略过滤
	p.Logger.Info("收到符合条件的新 EEW 预警数据，准备广播",
		"event_id", event.EventID,
		"report_num", reportNum,
		"location", loc,
		"magnitude", mag,
		"is_final", event.IsFinal,
		"is_cancel", event.IsCancel,
	)

	strategy := strings.ToLower(p.cfg.PushStrategy)
	switch strategy {
	case "first_only":
		if reportNum > 1 {
			p.Logger.Info("推送策略为仅首报，非首报跳过", "report_num", reportNum)
			return
		}
	case "first_and_final":
		if reportNum > 1 && !event.IsFinal {
			p.Logger.Info("推送策略为首尾报，中间报数跳过", "report_num", reportNum)
			return
		}
	case "all":
		// 全量推送
	default:
		if reportNum > 1 && !event.IsFinal {
			return
		}
	}

	// 7. 构造个性化消息卡片
	header := p.cfg.CustomHeader
	if header == "" {
		header = "🚨【地震预警】🚨"
	}

	var sb strings.Builder
	if event.IsCancel {
		sb.WriteString("⚠️【地震预警 - 取消报】⚠️\n")
	} else if event.IsFinal {
		sb.WriteString(fmt.Sprintf("%s (最终报)\n", header))
	} else {
		sb.WriteString(fmt.Sprintf("%s (第 %d 报)\n", header, reportNum))
	}
	sb.WriteString("--------------------------------\n")
	if event.Title != "" {
		sb.WriteString(fmt.Sprintf("发报说明: %s\n", event.Title))
	}
	sb.WriteString(fmt.Sprintf("震中位置: %s\n", loc))
	if p.cfg.ShowCoordinates && (event.Latitude != 0 || event.Longitude != 0) {
		sb.WriteString(fmt.Sprintf("震中坐标: %s\n", event.GetCoordinateStr()))
	}
	if mag > 0 {
		sb.WriteString(fmt.Sprintf("预估震级: M %.1f\n", mag))
	}
	if p.cfg.ShowDepth {
		sb.WriteString(fmt.Sprintf("震源深度: %s\n", event.GetDepthStr()))
	}
	if p.cfg.ShowIntensity && event.MaxIntensity != nil {
		sb.WriteString(fmt.Sprintf("震中烈度: %s\n", event.GetMaxIntensityStr()))
	}
	if p.cfg.ShowTime {
		if event.OriginTime != "" {
			sb.WriteString(fmt.Sprintf("发震时间: %s\n", event.OriginTime))
		}
		if event.ReportTime != "" {
			sb.WriteString(fmt.Sprintf("发报时间: %s\n", event.ReportTime))
		}
	}
	if hasLocalCalc {
		locName := strings.TrimSpace(p.cfg.LocationName)
		if locName == "" {
			locName = "本地"
		}
		sb.WriteString("--------------------------------\n")
		sb.WriteString(fmt.Sprintf("📍【本地测算 (%s)】\n", locName))
		sb.WriteString(fmt.Sprintf("震中距离: %.1f km\n", localDist))
		sb.WriteString(fmt.Sprintf("预估烈度: %.1f 度 (%s)\n", localIntensity, GetIntensityDesc(localIntensity)))
	}
	if p.cfg.ShowEventID && event.EventID != "" {
		sb.WriteString(fmt.Sprintf("事件ID: %s\n", event.EventID))
	}
	sb.WriteString("--------------------------------\n")
	sb.WriteString("注意安全，请勿惊慌\n")

	msgText := strings.TrimSpace(sb.String())

	// 是否需要强震 @全体成员
	shouldAtAll := p.cfg.AtAll && mag >= p.cfg.AtAllMinMagnitude

	p.broadcast(bot, msgText, shouldAtAll)
}

func (p *EEWPlugin) broadcast(b bot.Bot, text string, atAll bool) {
	for _, group := range p.cfg.Groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		gid := message.FromString(group)
		builder := msgchain.Builder().Group()
		if atAll {
			builder.Mention(message.FromString("all")).Text("\n")
		}
		builder.Text(text)
		if _, ok := b.SendGroupMsg(gid, builder.Build()); ok {
			p.Logger.Info("地震预警成功推送至群聊", "group", gid, "at_all", atAll)
		} else {
			p.Logger.Error("地震预警推送至群聊失败", "group", gid)
		}
	}

	for _, friend := range p.cfg.Friends {
		friend = strings.TrimSpace(friend)
		if friend == "" {
			continue
		}
		fid := message.FromString(friend)
		builder := msgchain.Builder().Friend()
		builder.Text(text)
		if _, ok := b.SendFriendMsg(fid, builder.Build()); ok {
			p.Logger.Info("地震预警成功推送至好友", "friend", fid)
		} else {
			p.Logger.Error("地震预警推送至好友失败", "friend", fid)
		}
	}
}

func (p *EEWPlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if cmd.Name == "eew" || cmd.Name == "地震" {
		if len(cmd.Args) > 0 && cmd.Args[0] == "status" {
			p.replyStatus(b, msg.GroupId, true)
			return false, nil
		}
		p.replyEQList(b, msg.GroupId, true)
		return false, nil
	}

	if cmd.Name == "weather" || cmd.Name == "天气" {
		p.replyWeatherRank(b, msg.GroupId, true)
		return false, nil
	}

	return true, nil
}

func (p *EEWPlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if cmd.Name == "eew" || cmd.Name == "地震" {
		if len(cmd.Args) > 0 && cmd.Args[0] == "status" {
			p.replyStatus(b, msg.Sender.UserId, false)
			return false, nil
		}
		p.replyEQList(b, msg.Sender.UserId, false)
		return false, nil
	}

	if cmd.Name == "weather" || cmd.Name == "天气" {
		p.replyWeatherRank(b, msg.Sender.UserId, false)
		return false, nil
	}

	return true, nil
}

func (p *EEWPlugin) replyStatus(b bot.Bot, target message.QID, isGroup bool) {
	p.mu.Lock()
	connStr := "未连接"
	if p.connected {
		if p.runningMode == "websocket" {
			connStr = "已连接 (WebSocket 毫秒推送)"
		} else if p.runningMode == "http_polling" {
			connStr = fmt.Sprintf("已连接 (HTTP 高频轮询 %ds)", p.cfg.PollInterval)
		} else {
			connStr = "已连接"
		}
	}
	src := p.cfg.Source
	lastMsg := "无"
	if !p.lastMsgTime.IsZero() {
		lastMsg = p.lastMsgTime.Format("15:04:05")
	}
	p.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("📡【地震预警监控运行状态】\n")
	sb.WriteString("--------------------------------\n")
	sb.WriteString(fmt.Sprintf("运行状态: %s\n", connStr))
	sb.WriteString(fmt.Sprintf("订阅源: %s\n", src))
	sb.WriteString(fmt.Sprintf("气象定时播报: %v (%s)\n", p.cfg.WeatherCronEnable, p.cfg.WeatherCron))
	sb.WriteString(fmt.Sprintf("最小震级: M %.1f\n", p.cfg.MinMagnitude))
	if p.cfg.MinIntensity > 0 {
		sb.WriteString(fmt.Sprintf("最小烈度: %d 度\n", p.cfg.MinIntensity))
	}
	sb.WriteString(fmt.Sprintf("地区过滤: %s\n", p.cfg.FocusMode))
	if len(p.cfg.FocusKeywords) > 0 {
		sb.WriteString(fmt.Sprintf("关注地名: %s\n", strings.Join(p.cfg.FocusKeywords, ", ")))
	}
	sb.WriteString(fmt.Sprintf("推送策略: %s\n", p.cfg.PushStrategy))
	if p.cfg.QuietHoursEnable {
		sb.WriteString(fmt.Sprintf("夜间静音: %s ~ %s (放行 M≥%.1f)\n", p.cfg.QuietHoursStart, p.cfg.QuietHoursEnd, p.cfg.QuietMinMagnitude))
	}
	if p.cfg.LocationEnable && (p.cfg.LocationLat != 0 || p.cfg.LocationLng != 0) {
		locName := strings.TrimSpace(p.cfg.LocationName)
		if locName == "" {
			locName = "本地"
		}
		sb.WriteString(fmt.Sprintf("本地测算: %s (%.4f, %.4f)\n", locName, p.cfg.LocationLat, p.cfg.LocationLng))
		if p.cfg.MinLocalIntensity > 0 {
			sb.WriteString(fmt.Sprintf("本地烈度门槛: ≥ %.1f 度\n", p.cfg.MinLocalIntensity))
		}
	}
	sb.WriteString(fmt.Sprintf("最近心跳/数据: %s\n", lastMsg))
	groupCount := 0
	for _, g := range p.cfg.Groups {
		if strings.TrimSpace(g) != "" {
			groupCount++
		}
	}
	friendCount := 0
	for _, f := range p.cfg.Friends {
		if strings.TrimSpace(f) != "" {
			friendCount++
		}
	}
	sb.WriteString(fmt.Sprintf("目标群组数: %d 个\n", groupCount))
	sb.WriteString(fmt.Sprintf("目标好友数: %d 个", friendCount))

	p.sendMsg(b, target, isGroup, sb.String())
}

func (p *EEWPlugin) replyEQList(b bot.Bot, target message.QID, isGroup bool) {
	urlStr := "https://api.wolfx.jp/cenc_eqlist.json"
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		p.sendMsg(b, target, isGroup, "创建请求失败")
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://wolfx.jp")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.sendMsg(b, target, isGroup, "获取中国地震台网数据失败，请稍后再试")
		return
	}
	defer resp.Body.Close()

	var rawMap map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rawMap); err != nil {
		p.sendMsg(b, target, isGroup, "解析地震数据失败")
		return
	}

	var items []CENCEQItem
	for k, v := range rawMap {
		if strings.HasPrefix(k, "No") {
			var item CENCEQItem
			if err := json.Unmarshal(v, &item); err == nil {
				items = append(items, item)
			}
		}
	}

	if len(items) == 0 {
		p.sendMsg(b, target, isGroup, "暂未查询到中国地震台网最新速报")
		return
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Time > items[j].Time
	})

	count := 5
	if len(items) < count {
		count = len(items)
	}

	var sb strings.Builder
	sb.WriteString("🌐【中国地震台网 - 最新地震速报】\n")
	sb.WriteString("--------------------------------\n")
	for i := 0; i < count; i++ {
		item := items[i]
		loc := item.Location
		if loc == "" {
			loc = item.PlaceName
		}
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, loc))
		if p.cfg.ShowCoordinates && item.GetCoordinateStr() != "不明" {
			sb.WriteString(fmt.Sprintf("   震中坐标: %s\n", item.GetCoordinateStr()))
		}
		sb.WriteString(fmt.Sprintf("   发震时间: %s\n", item.Time))
		intensityStr := ""
		if item.GetIntensityStr() != "不明" {
			intensityStr = fmt.Sprintf(" | 震中烈度: %s", item.GetIntensityStr())
		}
		sb.WriteString(fmt.Sprintf("   震级: %s 级 | 深度: %s%s\n", item.Magnitude, item.GetDepthStr(), intensityStr))
		if p.cfg.LocationEnable && (p.cfg.LocationLat != 0 || p.cfg.LocationLng != 0) {
			lat, err1 := strconv.ParseFloat(item.Latitude, 64)
			lng, err2 := strconv.ParseFloat(item.Longitude, 64)
			if err1 == nil && err2 == nil && (lat != 0 || lng != 0) {
				dist := CalcDistance(p.cfg.LocationLat, p.cfg.LocationLng, lat, lng)
				mag, _ := strconv.ParseFloat(item.Magnitude, 64)
				depth := 10.0
				if d, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSuffix(item.Depth, "km"), "KM"), 64); err == nil && d > 0 {
					depth = d
				}
				localIntensity := CalcLocalIntensity(mag, dist, depth)
				sb.WriteString(fmt.Sprintf("   本地测算: 距震中 %.1f km | 预估烈度: %.1f 度 (%s)\n", dist, localIntensity, GetIntensityDesc(localIntensity)))
			}
		}
		if i < count-1 {
			sb.WriteString("\n")
		}
	}

	p.sendMsg(b, target, isGroup, sb.String())
}

func (p *EEWPlugin) replyWeatherRank(b bot.Bot, target message.QID, isGroup bool) {
	urlStr := "https://api.wolfx.jp/weather_rank.json"
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		p.sendMsg(b, target, isGroup, "创建请求失败")
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://wolfx.jp")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.sendMsg(b, target, isGroup, "获取气象实况数据失败，请稍后再试")
		return
	}
	defer resp.Body.Close()

	var rawMap map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rawMap); err != nil {
		p.sendMsg(b, target, isGroup, "解析气象数据失败")
		return
	}

	var latestKey string
	for k := range rawMap {
		if k != "md5" && k > latestKey {
			latestKey = k
		}
	}

	if latestKey == "" {
		p.sendMsg(b, target, isGroup, "暂无最新气象实况数据")
		return
	}

	var hourRank WeatherHourRank
	if err := json.Unmarshal(rawMap[latestKey], &hourRank); err != nil {
		p.sendMsg(b, target, isGroup, "解析气象实况排行失败")
		return
	}

	timeStr := latestKey
	if len(latestKey) == 12 {
		timeStr = fmt.Sprintf("%s-%s-%s %s:%s",
			latestKey[0:4], latestKey[4:6], latestKey[6:8], latestKey[8:10], latestKey[10:12])
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🌤️【全国气象实况排行榜 (%s)】\n", timeStr))
	sb.WriteString("--------------------------------\n")

	if len(hourRank.TempRank) > 0 {
		sb.WriteString("🔥【最高气温 Top 5】\n")
		n := 5
		if len(hourRank.TempRank) < n {
			n = len(hourRank.TempRank)
		}
		for i := 0; i < n; i++ {
			item := hourRank.TempRank[i]
			sb.WriteString(fmt.Sprintf("%d. %s %s: %s\n", i+1, item.Province, item.City, item.Value))
		}
		sb.WriteString("\n")
	}

	if len(hourRank.RainRank) > 0 {
		sb.WriteString("🌧️【最大降水 Top 5】\n")
		n := 5
		if len(hourRank.RainRank) < n {
			n = len(hourRank.RainRank)
		}
		for i := 0; i < n; i++ {
			item := hourRank.RainRank[i]
			sb.WriteString(fmt.Sprintf("%d. %s %s: %s\n", i+1, item.Province, item.City, item.Value))
		}
		sb.WriteString("\n")
	}

	if len(hourRank.WindSRank) > 0 {
		sb.WriteString("💨【最大风速 Top 5】\n")
		n := 5
		if len(hourRank.WindSRank) < n {
			n = len(hourRank.WindSRank)
		}
		for i := 0; i < n; i++ {
			item := hourRank.WindSRank[i]
			sb.WriteString(fmt.Sprintf("%d. %s %s: %s\n", i+1, item.Province, item.City, item.Value))
		}
	}

	p.sendMsg(b, target, isGroup, strings.TrimSpace(sb.String()))
}

func (p *EEWPlugin) sendMsg(b bot.Bot, target message.QID, isGroup bool, text string) {
	if isGroup {
		builder := msgchain.Builder().Group()
		builder.Text(text)
		b.SendGroupMsg(target, builder.Build())
	} else {
		builder := msgchain.Builder().Friend()
		builder.Text(text)
		b.SendFriendMsg(target, builder.Build())
	}
}
