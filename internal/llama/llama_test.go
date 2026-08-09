package llama

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestLocateRequiresUniqueServerAndCLI(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "llama-server"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Locate(directory, "linux"); err == nil || !strings.Contains(err.Error(), "llama-cli") {
		t.Fatalf("err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "llama-cli"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nested", "llama-cli"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Locate(directory, "linux"); err == nil || !strings.Contains(err.Error(), "实际 2 个") {
		t.Fatalf("err=%v", err)
	}
}

func TestVersionProbeOutputCap(t *testing.T) {
	var output cappedBuffer
	data := bytes.Repeat([]byte("x"), (1<<20)+1)
	if _, err := output.Write(data); err == nil {
		t.Fatal("accepted oversized version output")
	}
}
