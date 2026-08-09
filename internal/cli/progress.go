package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Snail-one/LlamaLc/internal/release"
)

// NewDownloadReporter renders structured release events for both CLI and TUI.
// Terminals get an in-place bar; redirected output gets stable 10% checkpoints.
func NewDownloadReporter(output io.Writer) func(release.DownloadEvent) {
	reporter := &downloadReporter{output: output, terminal: terminalWriter(output), lastBucket: -1}
	return reporter.report
}

type downloadReporter struct {
	mu                   sync.Mutex
	output               io.Writer
	terminal, activeLine bool
	lastBucket           int
	lastLog              time.Time
}

func (r *downloadReporter) report(event release.DownloadEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch event.Phase {
	case release.DownloadStart:
		r.finishLine()
		r.lastBucket = -1
		r.lastLog = time.Time{}
		fmt.Fprintf(r.output, "\n准备下载\n  文件: %s\n  地址: %s\n", terminalText(event.Asset), terminalText(event.URL))
		if event.EffectiveURL != "" && event.EffectiveURL != event.URL {
			fmt.Fprintf(r.output, "  实际地址: %s\n", terminalText(event.EffectiveURL))
		}
		switch event.Route {
		case release.RouteProxy:
			fmt.Fprintf(r.output, "  访问方式: 系统/环境代理 %s\n", terminalText(event.Proxy))
		case release.RouteURLPrefix:
			fmt.Fprintf(r.output, "  访问方式: 下载代理前缀 %s\n", terminalText(event.Proxy))
		default:
			fmt.Fprintln(r.output, "  访问方式: 直接连接")
		}
		if event.Total > 0 {
			fmt.Fprintf(r.output, "  大小: %s\n", humanBytes(event.Total))
		}
	case release.DownloadFallback:
		r.finishLine()
		fmt.Fprintf(r.output, "  代理访问失败: %s\n  正在尝试直接连接原始 GitHub 地址...\n", terminalText(event.Detail))
	case release.DownloadProgress:
		r.progress(event)
	case release.DownloadComplete:
		r.finishLine()
		fmt.Fprintf(r.output, "下载完成\n  文件: %s\n  大小: %s\n  SHA-256: %s（校验通过）\n  平均速度: %s/s\n", terminalText(event.Asset), humanBytes(event.Downloaded), terminalText(event.SHA256), humanBytes(int64(event.SpeedBytesPerSecond)))
	}
}

func (r *downloadReporter) progress(event release.DownloadEvent) {
	percent := -1
	if event.Total > 0 {
		percent = int(event.Downloaded * 100 / event.Total)
		if percent > 100 {
			percent = 100
		}
	}
	line := ""
	if percent >= 0 {
		filled := percent * 30 / 100
		bar := strings.Repeat("=", filled)
		if filled < 30 {
			bar += ">" + strings.Repeat("-", 29-filled)
		}
		line = fmt.Sprintf("下载 %s [%s] %3d%% %s/%s %s/s", terminalText(event.Asset), bar, percent, humanBytes(event.Downloaded), humanBytes(event.Total), humanBytes(int64(event.SpeedBytesPerSecond)))
	} else {
		line = fmt.Sprintf("下载 %s %s %s/s", terminalText(event.Asset), humanBytes(event.Downloaded), humanBytes(int64(event.SpeedBytesPerSecond)))
	}
	if r.terminal {
		fmt.Fprintf(r.output, "\r%-110s", line)
		r.activeLine = true
		return
	}
	now := time.Now()
	if percent >= 0 {
		bucket := percent / 10
		if bucket == r.lastBucket && percent < 100 {
			return
		}
		r.lastBucket = bucket
	} else if !r.lastLog.IsZero() && now.Sub(r.lastLog) < 5*time.Second {
		return
	}
	r.lastLog = now
	fmt.Fprintln(r.output, line)
}

func (r *downloadReporter) finishLine() {
	if r.activeLine {
		fmt.Fprintln(r.output)
		r.activeLine = false
	}
}
func terminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
func humanBytes(value int64) string {
	if value < 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.2f %s", size, units[unit])
}
func terminalText(value string) string {
	var b strings.Builder
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			b.WriteRune('�')
		} else {
			b.WriteRune(character)
		}
	}
	return b.String()
}
