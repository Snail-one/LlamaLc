package llama

import (
	"strings"
	"testing"
)

func TestBuildAndExposure(t *testing.T) {
	rt := Runtime{Server: "llama-server", CLI: "llama-cli"}
	o := Options{Model: "m.gguf", Host: "127.0.0.1", Port: 29856, GPULayers: "auto", Threads: -1, BatchSize: 2048, UBatchSize: 512, FlashAttention: "auto", Parallel: -1, APIKeyFile: "key"}
	c, err := Build(API, rt, "/x", o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c.Args, " "), "--api-key-file key") {
		t.Fatalf("args=%v", c.Args)
	}
	if err = ValidateExposure("0.0.0.0", "key", []string{"--api-key-file", "other"}); err == nil {
		t.Fatal("accepted overridden key")
	}
	if err = ValidateExposure("0.0.0.0", "key", []string{"--api-key-file", "key"}); err != nil {
		t.Fatal(err)
	}
}
