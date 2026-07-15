//go:build windows

package launcher

import (
	"net/http"
	"testing"
)

func TestSelectWindowsProxy(t *testing.T) {
	if got := selectWindowsProxy("http=127.0.0.1:8080;https=127.0.0.1:8443", "https"); got != "127.0.0.1:8443" {
		t.Fatalf("HTTPS proxy = %q", got)
	}
	if got := selectWindowsProxy("127.0.0.1:7890", "https"); got != "127.0.0.1:7890" {
		t.Fatalf("shared proxy = %q", got)
	}
}

func TestWindowsProxyBypass(t *testing.T) {
	for _, host := range []string{"localhost", "api.example.com", "127.0.0.1"} {
		if !windowsProxyBypass(host, "<local>;*.example.com;127.0.0.0/8") {
			t.Fatalf("host %q did not bypass proxy", host)
		}
	}
	if windowsProxyBypass("github.com", "<local>;*.example.com") {
		t.Fatal("github.com unexpectedly bypassed proxy")
	}
}

func TestProxyFromWindowsSpec(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "https://github.com/file", nil)
	proxyURL, err := proxyFromWindowsSpec("http=proxy.local:8080;https=proxy.local:8443", "localhost", request)
	if err != nil || proxyURL.String() != "http://proxy.local:8443" {
		t.Fatalf("proxy URL = %v, err=%v", proxyURL, err)
	}
}
