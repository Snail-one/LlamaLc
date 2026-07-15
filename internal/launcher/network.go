package launcher

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func validateNetworkExposure(host string, args []string, managedAPIKeyFile string) (string, bool, error) {
	effectiveHost := lastArgumentValue(host, args, "--host")
	if isLocalOnlyHost(effectiveHost) {
		return effectiveHost, false, nil
	}
	if !hasArgumentValue(args, "--api-key-file", managedAPIKeyFile) {
		return effectiveHost, true, fmt.Errorf("拒绝在非本机地址 %q 上无托管认证启动", effectiveHost)
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

func hasArgumentValue(args []string, name, expected string) bool {
	for index := 0; index < len(args); index++ {
		if args[index] == name && index+1 < len(args) {
			if args[index+1] == expected {
				return true
			}
			index++
			continue
		}
		if strings.HasPrefix(args[index], name+"=") && strings.TrimPrefix(args[index], name+"=") == expected {
			return true
		}
	}
	return false
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
