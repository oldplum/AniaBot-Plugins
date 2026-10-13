package videoparse

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 下载相关常量。
const (
	// downloadTimeout 单个视频下载超时（后台协程任务，不受 5 分钟事件超时约束）。
	downloadTimeout = 3 * time.Minute
	// tempFilePrefix 临时文件前缀，便于清理任务识别本插件产生的文件。
	tempFilePrefix = "videoparse-"
	// tempFileTTL 临时文件最大保留时长，兜底清理异常退出遗留的文件。
	tempFileTTL = 2 * time.Hour
	// minResolveSize 小于该体积（1KB）的响应当作错误页而非视频。
	minResolveSize = 1024
)

// downloadResult 一次媒体下载的结果。
type downloadResult struct {
	path string
	size int64
}

// downloadMedia 把解析出的视频直链下载到插件临时目录。
// 超过体积上限（含 Content-Length 预检与流式计数双重判断）时中止并删除半成品。
func (p *VideoParsePlugin) downloadMedia(ctx context.Context, info *videoInfo) (*downloadResult, error) {
	maxBytes := int64(p.cfg.MaxSizeMB) << 20
	if maxBytes <= 0 {
		maxBytes = 200 << 20
	}
	dctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	headers := map[string]string{"User-Agent": downloadUserAgent}
	if info.Referer != "" {
		headers["Referer"] = info.Referer
	}
	resp, err := p.httpClient().R().
		SetContext(dctx).
		SetHeaders(headers).
		SetDoNotParseResponse(true).
		Get(info.MediaURL)
	if err != nil {
		return nil, fmt.Errorf("下载视频失败：%w", err)
	}
	body := resp.RawBody()
	if body == nil {
		return nil, fmt.Errorf("下载视频失败：响应体为空")
	}
	defer body.Close()

	if resp.IsError() {
		return nil, fmt.Errorf("下载视频失败：HTTP %s", resp.Status())
	}
	if length := resp.RawResponse.ContentLength; length > maxBytes {
		return nil, fmt.Errorf("视频体积 %s 超过上限 %dMB", humanSize(length), p.cfg.MaxSizeMB)
	}

	path, file, err := p.createTempFile()
	if err != nil {
		return nil, err
	}
	written, err := io.Copy(file, io.LimitReader(body, maxBytes+1))
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("下载视频失败：%w", err)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("写入临时文件失败：%w", closeErr)
	}
	if written > maxBytes {
		_ = os.Remove(path)
		return nil, fmt.Errorf("视频体积超过上限 %dMB", p.cfg.MaxSizeMB)
	}
	if written < minResolveSize {
		_ = os.Remove(path)
		return nil, fmt.Errorf("下载到的内容过小（%d 字节），可能不是有效视频", written)
	}
	return &downloadResult{path: path, size: written}, nil
}

// createTempFile 在插件临时目录下创建一个空文件，返回路径与句柄（句柄由调用方关闭）。
func (p *VideoParsePlugin) createTempFile() (string, *os.File, error) {
	dir := p.tempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("创建临时目录失败：%w", err)
	}
	file, err := os.CreateTemp(dir, tempFilePrefix+"*.mp4")
	if err != nil {
		return "", nil, fmt.Errorf("创建临时文件失败：%w", err)
	}
	return file.Name(), file, nil
}

// tempDir 插件临时目录：data/videoparse/tmp（相对 Bot 工作目录，与框架数据目录同级）。
// 测试可通过 tempDirOverride 指向 t.TempDir()，避免污染仓库。
func (p *VideoParsePlugin) tempDir() string {
	if p.tempDirOverride != "" {
		return p.tempDirOverride
	}
	return filepath.Join("data", "videoparse", "tmp")
}

// cleanupTempFiles 删除超过 TTL 的遗留临时文件；启动时与定时任务各调用一次。
func (p *VideoParsePlugin) cleanupTempFiles() {
	dir := p.tempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), tempFilePrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < tempFileTTL {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err == nil {
			p.Logger.Debug("已清理遗留临时视频文件", "name", entry.Name())
		}
	}
}

// removeTempFile 发送完成后删除临时文件。
func (p *VideoParsePlugin) removeTempFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		p.Logger.Debug("临时视频文件删除失败", "path", path, "err", err)
	}
}

// humanSize 把字节数格式化为易读文本。
func humanSize(n int64) string {
	if n <= 0 {
		return "未知"
	}
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(n)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1f%s", math.Round(value*10)/10, units[unit])
}

// humanDuration 把秒数格式化为 mm:ss / h:mm:ss。
func humanDuration(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	total := int(seconds + 0.5)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
