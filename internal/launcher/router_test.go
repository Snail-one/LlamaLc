package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouterConflictAcrossKinds(t *testing.T) {
	models := []ModelFile{
		{ID: "same.gguf", Path: `C:\models\same.gguf`, Kind: GenerationModel},
		{ID: "SAME.GGUF", Path: `C:\embeddings\SAME.GGUF`, Kind: EmbeddingModel},
	}
	err := CheckModelIDConflicts(models)
	if err == nil || !strings.Contains(err.Error(), "generation") || !strings.Contains(err.Error(), "embedding") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
}

func TestRenderRouterPresetAllKinds(t *testing.T) {
	models := []ModelFile{
		{ID: "chat.gguf", Path: `C:\models\chat.gguf`, Kind: GenerationModel},
		{ID: "embed.gguf", Path: `C:\embeddings\embed.gguf`, Kind: EmbeddingModel},
		{ID: "rank.gguf", Path: `C:\rerank\rank.gguf`, Kind: RerankModel},
	}
	projectors := []ModelFile{{ID: "mmproj-chat-F16.gguf", Path: `C:\mmproj\mmproj-chat-F16.gguf`}}
	content := RenderRouterPreset(models, projectors, PresetOptions{GPULayers: "auto", Pooling: "mean", UBatchSize: 8192})
	for _, expected := range []string{
		"version = 1", "[chat.gguf]", `mmproj = C:\mmproj\mmproj-chat-F16.gguf`,
		"[embed.gguf]", "embedding = true", "pooling = mean", "ubatch-size = 8192",
		"[rank.gguf]", "reranking = true",
	} {
		if !strings.Contains(content, expected) {
			t.Errorf("preset missing %q:\n%s", expected, content)
		}
	}
}

func TestWriteRouterPresetRejectsOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router-models.ini")
	if err := WriteRouterPreset(path, "first", false); err != nil {
		t.Fatal(err)
	}
	if err := WriteRouterPreset(path, "second", false); err == nil {
		t.Fatal("manual preset was silently overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("existing content changed: %q", data)
	}
}

func TestPrepareRouterManualTakesPriority(t *testing.T) {
	root := t.TempDir()
	paths := ResolvedPaths{
		Models: filepath.Join(root, "models"), Embeddings: filepath.Join(root, "embeddings"),
		Rerank: filepath.Join(root, "rerank"), Mmproj: filepath.Join(root, "mmproj"),
		RouterManual: filepath.Join(root, "bin", "router-models.ini"),
		RouterAuto:   filepath.Join(root, "bin", "router-models.auto.ini"),
	}
	touchFile(t, filepath.Join(paths.Models, "chat.gguf"))
	touchFile(t, paths.RouterManual)
	preset, manual, _, err := PrepareRouter(paths, PresetOptions{GPULayers: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if !manual || preset != paths.RouterManual {
		t.Fatalf("manual preset did not win: %q, %v", preset, manual)
	}
	if _, err := os.Stat(paths.RouterAuto); err != nil {
		t.Fatalf("auto preset was not refreshed: %v", err)
	}
}
