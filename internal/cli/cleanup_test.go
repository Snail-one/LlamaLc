package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
)

func TestCleanupMenuShowsDetailsAndRequiresItemConfirmation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	legacy := filepath.Join(root, "data", "llama.cpp")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	a := App{Layout: l, In: strings.NewReader("2\n1\n0\n2\n3\ny\n0\n"), Out: &out, Err: &stderr}
	if err := a.runCleanupMenu(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"旧版布局", "需手动确认", "查看目录内容", "keep.txt", "即将永久删除完整路径", "已删除"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy still exists: %v", err)
	}
}
