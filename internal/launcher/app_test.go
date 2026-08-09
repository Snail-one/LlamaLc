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
	for _, args := range [][]string{{"run", "api", "--help"}, {"update"}, {"update", "llama", "--help"}} {
		out.Reset()
		err.Reset()
		if code := Main(args, strings.NewReader(""), &out, &err, llama.OSExecutor{}); code != 0 {
			t.Fatalf("args=%v code=%d", args, code)
		}
		if called {
			t.Fatalf("help detected layout: %v", args)
		}
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
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := MainWithLayout([]string{"key", "show"}, l, strings.NewReader(""), &out, &stderr, llama.OSExecutor{})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("legacy content changed: %v", err)
	}
	if _, err := os.Stat(l.ConfigFile); !os.IsNotExist(err) {
		t.Fatalf("key command unexpectedly initialized config: %v", err)
	}
	if _, err := os.Stat(l.APIKeyFile); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("legacy path was reported: %s", stderr.String())
	}
}
