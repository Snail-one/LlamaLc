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
	content, err := RenderRouterPreset(models, projectors, PresetOptions{GPULayers: "auto", Pooling: "mean", BatchSize: 8192, UBatchSize: 8192})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"version = 1", "[chat.gguf]", `mmproj = C:\mmproj\mmproj-chat-F16.gguf`,
		"[embed.gguf]", "embedding = true", "pooling = mean", "batch-size = 8192", "ubatch-size = 8192",
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

func TestRenderManualRouterPresetCanDisableMmprojAutoMatch(t *testing.T) {
	models := []ModelFile{{ID: "chat.gguf", Path: `C:\models\chat.gguf`, Kind: GenerationModel}}
	projectors := []ModelFile{{ID: "mmproj-chat-F16.gguf", Path: `C:\mmproj\mmproj-chat-F16.gguf`}}
	content, err := RenderRouterPreset(models, projectors, PresetOptions{Manual: true, DisableMmprojAuto: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, `mmproj = C:\mmproj\mmproj-chat-F16.gguf`) {
		t.Fatalf("disabled mmproj auto-match still selected a projector:\n%s", content)
	}
	if !strings.Contains(content, `; mmproj = C:\path\to\mmproj.gguf`) {
		t.Fatalf("manual mmproj placeholder missing:\n%s", content)
	}
}

func TestPrepareRouterManualTakesPriority(t *testing.T) {
	root := t.TempDir()
	paths := ResolvedPaths{
		Models: filepath.Join(root, "models"), Embeddings: filepath.Join(root, "embeddings"),
		Rerank: filepath.Join(root, "rerank"), Mmproj: filepath.Join(root, "mmproj"),
		RouterManual: filepath.Join(root, "config", "router-models.ini"),
		RouterAuto:   filepath.Join(root, "config", "router-models.auto.ini"),
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

func TestRouterPresetRejectsInjectedFilename(t *testing.T) {
	models := []ModelFile{{ID: "safe.gguf]\napi-key = stolen", Path: filepath.Join(t.TempDir(), "safe.gguf"), Kind: GenerationModel}}
	if _, err := RenderRouterPreset(models, nil, PresetOptions{}); err == nil {
		t.Fatalf("injected model ID was accepted: %v", err)
	}
}

func TestWriteRouterPresetRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "important.txt")
	if err := os.WriteFile(target, []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "router-models.auto.ini")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if err := WriteRouterPreset(path, "replacement", true); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("Router symlink was accepted: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "important" {
		t.Fatalf("symlink target was modified: %q", content)
	}
}

func TestWriteRouterPresetAtomicallyReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "router-models.auto.ini")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteRouterPreset(path, "new", true); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("preset was not replaced: %q", content)
	}
	matches, err := filepath.Glob(filepath.Join(configDir, ".router-models.auto.ini.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files remain: %v, %v", matches, err)
	}
}
