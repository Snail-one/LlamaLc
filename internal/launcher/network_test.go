package launcher

import (
	"strings"
	"testing"
)

func TestValidateNetworkExposure(t *testing.T) {
	t.Setenv("LLAMA_API_KEY", "")
	t.Setenv("LLAMA_ARG_API_KEY_FILE", "")
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		if _, remote, err := validateNetworkExposure(host, nil); err != nil || remote {
			t.Fatalf("local host %q was rejected: remote=%v err=%v", host, remote, err)
		}
	}
	if _, _, err := validateNetworkExposure("0.0.0.0", nil); err == nil || !strings.Contains(err.Error(), "无认证") {
		t.Fatalf("unauthenticated remote listener was accepted: %v", err)
	}
	effective, remote, err := validateNetworkExposure("127.0.0.1", []string{"--host", "0.0.0.0", "--api-key-file", "keys.txt"})
	if err != nil || !remote || effective != "0.0.0.0" {
		t.Fatalf("authenticated forwarded host failed: effective=%q remote=%v err=%v", effective, remote, err)
	}
}
