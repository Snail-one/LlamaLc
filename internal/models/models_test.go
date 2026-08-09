package models

import (
	"github.com/Snail-one/LlamaLc/internal/layout"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanResolveAndRouterPreset(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(l.GenerationModels, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z.gguf", "A.gguf", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(l.GenerationModels, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Scan(l, Generation)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].ID != "A.gguf" {
		t.Fatalf("files=%v", files)
	}
	if _, err = Resolve(l, Generation, "a.GGUF"); err != nil {
		t.Fatal(err)
	}
	if err = WriteRouterPreset(l, l.RouterPreset, files); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(l.RouterPreset)
	if !strings.Contains(string(data), "[A]") {
		t.Fatalf("preset=%s", data)
	}
}
