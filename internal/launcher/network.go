package launcher

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

func validateNetworkExposure(host string, extra []string) (string, bool, error) {
	effectiveHost := lastArgumentValue(host, extra, "--host")
	if isLocalOnlyHost(effectiveHost) {
		return effectiveHost, false, nil
	}
	if !hasServerAuthentication(extra) {
		return effectiveHost, true, fmt.Errorf("拒绝在非本机地址 %q 上无认证启动；请使用 -- 后的 --api-key-file FILE，或设置 LLAMA_API_KEY", effectiveHost)
	}
	return effectiveHost, true, nil
}

func serviceURL(host string, port int, path string) string {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.HasSuffix(strings.ToLower(host), ".sock") {
		return host + path
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
}

func isLocalOnlyHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".sock") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasServerAuthentication(args []string) bool {
	if strings.TrimSpace(os.Getenv("LLAMA_API_KEY")) != "" || strings.TrimSpace(os.Getenv("LLAMA_ARG_API_KEY_FILE")) != "" {
		return true
	}
	return lastArgumentValue("", args, "--api-key") != "" || lastArgumentValue("", args, "--api-key-file") != ""
}

func lastArgumentValue(initial string, args []string, name string) string {
	value := initial
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == name && index+1 < len(args) {
			value = strings.TrimSpace(args[index+1])
			index++
			continue
		}
		if strings.HasPrefix(argument, name+"=") {
			value = strings.TrimSpace(strings.TrimPrefix(argument, name+"="))
		}
	}
	return value
}
