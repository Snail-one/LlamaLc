package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Snail-one/LlamaLc/internal/release"
)

func TestDownloadReporterRestoresV015Details(t *testing.T) {
	var output bytes.Buffer
	report := NewDownloadReporter(&output)
	report(release.DownloadEvent{Phase: release.DownloadStart, Asset: "runtime.zip", URL: "https://github.com/example/runtime.zip", Route: release.RouteProxy, Proxy: "http://127.0.0.1:7890", Total: 2048})
	report(release.DownloadEvent{Phase: release.DownloadProgress, Asset: "runtime.zip", Downloaded: 1024, Total: 2048, SpeedBytesPerSecond: 512})
	report(release.DownloadEvent{Phase: release.DownloadFallback, Asset: "runtime.zip", Detail: "502 Bad Gateway"})
	report(release.DownloadEvent{Phase: release.DownloadProgress, Asset: "runtime.zip", Downloaded: 2048, Total: 2048, SpeedBytesPerSecond: 1024})
	report(release.DownloadEvent{Phase: release.DownloadComplete, Asset: "runtime.zip", Downloaded: 2048, Total: 2048, SpeedBytesPerSecond: 1024, Elapsed: 2 * time.Second, SHA256: strings.Repeat("a", 64)})

	for _, want := range []string{"准备下载", "https://github.com/example/runtime.zip", "系统/环境代理 http://127.0.0.1:7890", "50%", "1.00 KiB/2.00 KiB", "代理访问失败", "直接连接原始 GitHub", "[==============================] 100%", "SHA-256:", "校验通过", "平均速度"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %s", want, output.String())
		}
	}
}
