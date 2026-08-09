package launcher

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
)

func TestUnknownAndVersionHaveNoFilesystemSideEffects(t *testing.T) {
	old := detectLayout
	defer func() { detectLayout = old }()
	called := false
	detectLayout = func() (layout.Layout, error) { called = true; return layout.Layout{}, errors.New("should not run") }
	var out, err bytes.Buffer
	if code := Main([]string{"serve"}, strings.NewReader(""), &out, &err, llama.OSExecutor{}); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if called {
		t.Fatal("unknown command detected layout")
	}
	err.Reset()
	if code := Main([]string{"version"}, strings.NewReader(""), &out, &err, llama.OSExecutor{}); code != 0 {
		t.Fatal(code)
	}
	if called {
		t.Fatal("version detected layout")
	}
}

func TestFreshInitializationOnlyUsesV1Layout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	legacy := filepath.Join(root, "data", "llama.cpp")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(legacy, "keep")
	if err := os.WriteFile(sentinel, []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := MainWithLayout([]string{"config", "key", "show"}, l, strings.NewReader(""), &out, &stderr, llama.OSExecutor{})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("legacy content changed: %v", err)
	}
	if _, err := os.Stat(l.ConfigFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.APIKeyFile); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), legacy) {
		t.Fatalf("missing legacy notice: %s", stderr.String())
	}
}
