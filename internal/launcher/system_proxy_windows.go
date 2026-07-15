//go:build windows

package launcher

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
	winHTTPAutoProxyAutoDetect = 0x00000001
	winHTTPAutoProxyConfigURL  = 0x00000002
	winHTTPAutoDetectTypeDHCP  = 0x00000001
	winHTTPAutoDetectTypeDNSA  = 0x00000002
)

var (
	winHTTPDLL              = syscall.NewLazyDLL("winhttp.dll")
	winHTTPGetIEProxyConfig = winHTTPDLL.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	winHTTPOpen             = winHTTPDLL.NewProc("WinHttpOpen")
	winHTTPCloseHandle      = winHTTPDLL.NewProc("WinHttpCloseHandle")
	winHTTPGetProxyForURL   = winHTTPDLL.NewProc("WinHttpGetProxyForUrl")
	kernel32Proxy           = syscall.NewLazyDLL("kernel32.dll")
	globalFreeProxy         = kernel32Proxy.NewProc("GlobalFree")
)

type winHTTPCurrentUserProxyConfig struct {
	AutoDetect    int32
	AutoConfigURL uintptr
	Proxy         uintptr
	ProxyBypass   uintptr
}

type winHTTPAutoProxyOptions struct {
	Flags                 uint32
	AutoDetectFlags       uint32
	AutoConfigURL         uintptr
	Reserved              uintptr
	ReservedDWORD         uint32
	AutoLogonIfChallenged int32
}

type winHTTPProxyInfo struct {
	AccessType  uint32
	Proxy       uintptr
	ProxyBypass uintptr
}

type windowsProxySettings struct {
	autoDetect  bool
	autoURL     string
	proxy       string
	proxyBypass string
}

func newSystemProxyResolver() func(*http.Request) (*url.URL, error) {
	settings := readWindowsProxySettings()
	type cachedResult struct {
		proxy *url.URL
		err   error
	}
	var cacheMu sync.Mutex
	cache := make(map[string]cachedResult)
	return func(request *http.Request) (*url.URL, error) {
		// Explicit process environment always wins. ProxyFromEnvironment also
		// applies NO_PROXY using Go's complete host/IP/CIDR matching rules.
		if hasEnvironmentProxy(request.URL.Scheme) {
			return http.ProxyFromEnvironment(request)
		}
		cacheKey := request.URL.String()
		cacheMu.Lock()
		cached, exists := cache[cacheKey]
		cacheMu.Unlock()
		if exists {
			return cached.proxy, cached.err
		}
		var proxyURL *url.URL
		var proxyErr error
		if settings.autoDetect || settings.autoURL != "" {
			if resolvedProxy, resolved := resolveWindowsAutoProxy(request.URL.String(), settings); resolved {
				proxyURL = resolvedProxy
				cacheMu.Lock()
				cache[cacheKey] = cachedResult{proxy: proxyURL}
				cacheMu.Unlock()
				return proxyURL, nil
			}
		}
		proxyURL, proxyErr = proxyFromWindowsSpec(settings.proxy, settings.proxyBypass, request)
		cacheMu.Lock()
		cache[cacheKey] = cachedResult{proxy: proxyURL, err: proxyErr}
		cacheMu.Unlock()
		return proxyURL, proxyErr
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
	var config winHTTPCurrentUserProxyConfig
	result, _, _ := winHTTPGetIEProxyConfig.Call(uintptr(unsafe.Pointer(&config)))
	if result == 0 {
		return windowsProxySettings{}
	}
	settings := windowsProxySettings{
		autoDetect:  config.AutoDetect != 0,
		autoURL:     utf16PointerString(config.AutoConfigURL),
		proxy:       utf16PointerString(config.Proxy),
		proxyBypass: utf16PointerString(config.ProxyBypass),
	}
	for _, pointer := range []uintptr{config.AutoConfigURL, config.Proxy, config.ProxyBypass} {
		if pointer != 0 {
			_, _, _ = globalFreeProxy.Call(pointer)
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

	options := winHTTPAutoProxyOptions{AutoLogonIfChallenged: 1}
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
	var info winHTTPProxyInfo
	result, _, _ := winHTTPGetProxyForURL.Call(
		session,
		uintptr(unsafe.Pointer(targetPointer)),
		uintptr(unsafe.Pointer(&options)),
		uintptr(unsafe.Pointer(&info)),
	)
	if result == 0 {
		return nil, false
	}
	proxySpec := utf16PointerString(info.Proxy)
	bypassSpec := utf16PointerString(info.ProxyBypass)
	for _, pointer := range []uintptr{info.Proxy, info.ProxyBypass} {
		if pointer != 0 {
			_, _, _ = globalFreeProxy.Call(pointer)
		}
	}
	if proxySpec == "" {
		return nil, true
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	proxyURL, err := proxyFromWindowsSpec(proxySpec, bypassSpec, request)
	if err != nil {
		return nil, false
	}
	return proxyURL, true
}

func proxyFromWindowsSpec(proxySpec, bypassSpec string, request *http.Request) (*url.URL, error) {
	combinedBypass := bypassSpec
	if noProxy := strings.TrimSpace(firstEnvironment("NO_PROXY", "no_proxy")); noProxy != "" {
		combinedBypass += ";" + noProxy
	}
	if windowsProxyBypass(request.URL.Hostname(), combinedBypass) {
		return nil, nil
	}
	selected := selectWindowsProxy(proxySpec, request.URL.Scheme)
	if selected == "" || strings.EqualFold(selected, "DIRECT") {
		return nil, nil
	}
	proxyScheme := "http"
	upper := strings.ToUpper(selected)
	for _, prefix := range []struct{ token, scheme string }{{"PROXY ", "http"}, {"HTTPS ", "https"}, {"SOCKS5 ", "socks5"}, {"SOCKS ", "socks5"}} {
		if strings.HasPrefix(upper, prefix.token) {
			selected = strings.TrimSpace(selected[len(prefix.token):])
			proxyScheme = prefix.scheme
			break
		}
	}
	if !strings.Contains(selected, "://") {
		selected = proxyScheme + "://" + selected
	}
	return url.Parse(selected)
}

func selectWindowsProxy(spec, scheme string) string {
	var fallback string
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
		if bypassHost, _, err := net.SplitHostPort(field); err == nil {
			field = bypassHost
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

func utf16PointerString(pointer uintptr) string {
	if pointer == 0 {
		return ""
	}
	values := make([]uint16, 0, 64)
	for index := uintptr(0); index < 32768; index++ {
		value := *(*uint16)(unsafe.Pointer(pointer + index*2))
		if value == 0 {
			break
		}
		values = append(values, value)
	}
	return syscall.UTF16ToString(values)
}
