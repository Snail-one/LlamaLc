//go:build !windows

package release

import (
	"net/http"
	"net/url"
)

func newSystemProxyResolver() func(*http.Request) (*url.URL, error) { return http.ProxyFromEnvironment }
