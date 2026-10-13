package videoparse

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// liveInt 读取整型环境变量，未设置或非法时用默认值。
func liveInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return def
}

// TestLiveResolve 对真实分享链接做端到端解析验证（需要外网）。
// 默认跳过，本地验证时显式开启：
//
//	VIDEOPARSE_LIVE=1 VIDEOPARSE_LIVE_LINKS="<链接1>,<链接2>" go test ./plugins/videoparse/ -run TestLiveResolve -v
//
// 追加 VIDEOPARSE_LIVE_DOWNLOAD=1 验证下载整链路（可用 VIDEOPARSE_LIVE_MAXMB 放宽体积上限）。
func TestLiveResolve(t *testing.T) {
	if os.Getenv("VIDEOPARSE_LIVE") != "1" {
		t.Skip("未设置 VIDEOPARSE_LIVE=1，跳过联网验证")
	}
	raw := strings.TrimSpace(os.Getenv("VIDEOPARSE_LIVE_LINKS"))
	if raw == "" {
		t.Fatal("VIDEOPARSE_LIVE_LINKS 未设置")
	}

	p := newTestPlugin(t)
	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start 失败：%v", err)
	}

	for _, shareText := range splitLiveLinks(raw) {
		shareText := shareText
		t.Run(shareText, func(t *testing.T) {
			detected := detectLinks(shareText)
			if len(detected) != 1 {
				t.Fatalf("未能识别为受支持链接：%q", shareText)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			info, err := p.resolve(ctx, detected[0], shareText)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			t.Logf("platform=%s title=%q author=%q size=%s duration=%s",
				info.Platform, info.Title, info.Author, humanSize(info.SizeBytes), humanDuration(info.Duration))
			if info.MediaURL == "" {
				t.Error("未返回视频直链")
			}

			if os.Getenv("VIDEOPARSE_LIVE_DOWNLOAD") != "1" {
				return
			}
			p.cfg.MaxSizeMB = liveInt("VIDEOPARSE_LIVE_MAXMB", 200)
			media, err := p.downloadMedia(ctx, info)
			if err != nil {
				t.Fatalf("下载失败：%v", err)
			}
			defer p.removeTempFile(media.path)
			t.Logf("已下载 %s 到 %s", humanSize(media.size), media.path)
			if media.size <= 0 {
				t.Error("下载结果为空")
			}
		})
	}
}

// splitLiveLinks 按 "||" 切分多条分享文案（文案本身可能含逗号，故不用逗号分隔）。
func splitLiveLinks(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, "||") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
