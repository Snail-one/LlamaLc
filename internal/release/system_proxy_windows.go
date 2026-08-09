//go:build windows

package release

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const (
	winHTTPAccessTypeNoProxy   = 1
	winHTTPAutoProxyAutoDetect = 1
	winHTTPAutoProxyConfigURL  = 2
	winHTTPAutoDetectTypeDHCP  = 1
	winHTTPAutoDetectTypeDNSA  = 2
)

var (
	winHTTPDLL              = syscall.NewLazyDLL("winhttp.dll")
	winHTTPGetIEProxyConfig = winHTTPDLL.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	winHTTPOpen             = winHTTPDLL.NewProc("WinHttpOpen")
	winHTTPCloseHandle      = winHTTPDLL.NewProc("WinHttpCloseHandle")
	winHTTPGetProxyForURL   = winHTTPDLL.NewProc("WinHttpGetProxyForUrl")
	globalFreeProxy         = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalFree")
)

type currentUserProxy struct {
	AutoDetect                        int32
	AutoConfigURL, Proxy, ProxyBypass uintptr
}
type autoProxyOptions struct {
	Flags, AutoDetectFlags  uint32
	AutoConfigURL, Reserved uintptr
	ReservedDWORD           uint32
	AutoLogonIfChallenged   int32
}
type proxyInfo struct {
	AccessType         uint32
	Proxy, ProxyBypass uintptr
}
type windowsProxySettings struct {
	autoDetect             bool
	autoURL, proxy, bypass string
}

func newSystemProxyResolver() func(*http.Request) (*url.URL, error) {
	settings := readWindowsProxySettings()
	type result struct {
		proxy *url.URL
		err   error
	}
	cache := map[string]result{}
	var mu sync.Mutex
	return func(req *http.Request) (*url.URL, error) {
		if hasEnvironmentProxy(req.URL.Scheme) {
			return http.ProxyFromEnvironment(req)
		}
		key := req.URL.String()
		mu.Lock()
		cached, ok := cache[key]
		mu.Unlock()
		if ok {
			return cached.proxy, cached.err
		}
		if settings.autoDetect || settings.autoURL != "" {
			if proxy, resolved := resolveWindowsAutoProxy(key, settings); resolved {
				mu.Lock()
				cache[key] = result{proxy: proxy}
				mu.Unlock()
				return proxy, nil
			}
		}
		proxy, err := proxyFromWindowsSpec(settings.proxy, settings.bypass, req)
		mu.Lock()
		cache[key] = result{proxy: proxy, err: err}
		mu.Unlock()
		return proxy, err
	}
}
func hasEnvironmentProxy(scheme string) bool {
	names := []string{"HTTP_PROXY", "http_proxy"}
	if strings.EqualFold(scheme, "https") {
		names = []string{"HTTPS_PROXY", "https_proxy"}
	}
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}
func readWindowsProxySettings() windowsProxySettings {
	var cfg currentUserProxy
	result, _, _ := winHTTPGetIEProxyConfig.Call(uintptr(unsafe.Pointer(&cfg)))
	if result == 0 {
		return windowsProxySettings{}
	}
	settings := windowsProxySettings{autoDetect: cfg.AutoDetect != 0, autoURL: utf16String(cfg.AutoConfigURL), proxy: utf16String(cfg.Proxy), bypass: utf16String(cfg.ProxyBypass)}
	for _, pointer := range []uintptr{cfg.AutoConfigURL, cfg.Proxy, cfg.ProxyBypass} {
		if pointer != 0 {
			globalFreeProxy.Call(pointer)
		}
	}
	return settings
}
func resolveWindowsAutoProxy(target string, settings windowsProxySettings) (*url.URL, bool) {
	agent, err := syscall.UTF16PtrFromString("LlamaLc-update-client")
	if err != nil {
		return nil, false
	}
	session, _, _ := winHTTPOpen.Call(uintptr(unsafe.Pointer(agent)), winHTTPAccessTypeNoProxy, 0, 0, 0)
	if session == 0 {
		return nil, false
	}
	defer winHTTPCloseHandle.Call(session)
	options := autoProxyOptions{AutoLogonIfChallenged: 1}
	var autoURL *uint16
	if settings.autoURL != "" {
		autoURL, err = syscall.UTF16PtrFromString(settings.autoURL)
		if err == nil {
			options.Flags |= winHTTPAutoProxyConfigURL
			options.AutoConfigURL = uintptr(unsafe.Pointer(autoURL))
		}
	}
	if settings.autoDetect {
		options.Flags |= winHTTPAutoProxyAutoDetect
		options.AutoDetectFlags = winHTTPAutoDetectTypeDHCP | winHTTPAutoDetectTypeDNSA
	}
	if options.Flags == 0 {
		return nil, false
	}
	targetPointer, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return nil, false
	}
	var info proxyInfo
	result, _, _ := winHTTPGetProxyForURL.Call(session, uintptr(unsafe.Pointer(targetPointer)), uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return nil, false
	}
	spec, bypass := utf16String(info.Proxy), utf16String(info.ProxyBypass)
	for _, pointer := range []uintptr{info.Proxy, info.ProxyBypass} {
		if pointer != 0 {
			globalFreeProxy.Call(pointer)
		}
	}
	if spec == "" {
		return nil, true
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	proxy, err := proxyFromWindowsSpec(spec, bypass, req)
	return proxy, err == nil
}
func proxyFromWindowsSpec(spec, bypass string, req *http.Request) (*url.URL, error) {
	if noProxy := firstEnvironment("NO_PROXY", "no_proxy"); noProxy != "" {
		bypass += ";" + noProxy
	}
	if windowsProxyBypass(req.URL.Hostname(), bypass) {
		return nil, nil
	}
	selected := selectWindowsProxy(spec, req.URL.Scheme)
	if selected == "" || strings.EqualFold(selected, "DIRECT") {
		return nil, nil
	}
	scheme := "http"
	upper := strings.ToUpper(selected)
	for _, prefix := range []struct{ token, scheme string }{{"PROXY ", "http"}, {"HTTPS ", "https"}, {"SOCKS5 ", "socks5"}, {"SOCKS ", "socks5"}} {
		if strings.HasPrefix(upper, prefix.token) {
			selected = strings.TrimSpace(selected[len(prefix.token):])
			scheme = prefix.scheme
			break
		}
	}
	if !strings.Contains(selected, "://") {
		selected = scheme + "://" + selected
	}
	return url.Parse(selected)
}
func selectWindowsProxy(spec, scheme string) string {
	fallback := ""
	for _, field := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == ',' }) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, value, found := strings.Cut(field, "=")
		if found {
			if strings.EqualFold(strings.TrimSpace(key), scheme) {
				return strings.TrimSpace(value)
			}
			continue
		}
		if fallback == "" {
			fallback = field
		}
	}
	return fallback
}
func windowsProxyBypass(host, spec string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, field := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\t' }) {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		if field == "*" || (field == "<local>" && !strings.Contains(host, ".")) {
			return true
		}
		if _, network, err := net.ParseCIDR(field); err == nil {
			if address := net.ParseIP(host); address != nil && network.Contains(address) {
				return true
			}
			continue
		}
		if h, _, err := net.SplitHostPort(field); err == nil {
			field = h
		}
		if strings.HasPrefix(field, ".") && strings.HasSuffix(host, field) {
			return true
		}
		if matched, _ := path.Match(field, host); matched || host == field {
			return true
		}
	}
	return false
}
func firstEnvironment(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
func utf16String(pointer uintptr) string {
	if pointer == 0 {
		return ""
	}
	values := make([]uint16, 0, 64)
	for i := uintptr(0); i < 32768; i++ {
		value := *(*uint16)(unsafe.Pointer(pointer + i*2))
		if value == 0 {
			break
		}
		values = append(values, value)
	}
	return syscall.UTF16ToString(values)
}
