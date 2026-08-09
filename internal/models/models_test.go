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
	if !strings.Contains(string(data), "[A.gguf]") {
		t.Fatalf("preset=%s", data)
	}
}

func TestRouterPresetRestoresAllV015ModelKindsAndMMProj(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	for directory, name := range map[string]string{
		l.GenerationModels: "Qwen-VL.gguf",
		l.EmbeddingModels:  "embed.gguf",
		l.RerankModels:     "rerank.gguf",
		l.MMProjModels:     "Qwen-VL-mmproj.gguf",
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, projectors, err := CollectRouterModels(l)
	if err != nil {
		t.Fatal(err)
	}
	options := PresetOptions{GPULayers: "auto", ContextSize: 8192, Pooling: "last", BatchSize: 8192, UBatchSize: 4096, Manual: true}
	if err = WriteRouterPresetWithOptions(l, l.RouterPreset, files, projectors, options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(l.RouterPreset)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Qwen-VL.gguf]", "mmproj = ", "[embed.gguf]", "embedding = true", "pooling = last", "batch-size = 8192", "[rerank.gguf]", "reranking = true", "n-gpu-layers = auto", "ctx-size = 8192"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
}

func TestRouterRejectsDuplicateIDsAcrossDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	for _, directory := range []string{l.GenerationModels, l.EmbeddingModels} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "same.gguf"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := CollectRouterModels(l); err == nil || !strings.Contains(err.Error(), "同名冲突") {
		t.Fatalf("duplicate IDs accepted: %v", err)
	}
}
