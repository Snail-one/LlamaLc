package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanModelsRecursiveFilteredStable(t *testing.T) {
	root := t.TempDir()
	touchFile(t, filepath.Join(root, "nested", "z.GGML"))
	touchFile(t, filepath.Join(root, "B.bin"))
	touchFile(t, filepath.Join(root, "a.gguf"))
	touchFile(t, filepath.Join(root, "ignore.txt"))
	models, err := ScanModels(root, GenerationModel, generationExtensions)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(models))
	for i, model := range models {
		got[i] = model.ID
	}
	if strings.Join(got, ",") != "a.gguf,B.bin,z.GGML" {
		t.Fatalf("unexpected sorted models: %v", got)
	}

	routerModels, err := ScanModels(root, GenerationModel, ggufExtension)
	if err != nil {
		t.Fatal(err)
	}
	if len(routerModels) != 1 || routerModels[0].ID != "a.gguf" {
		t.Fatalf("router extension filter failed: %#v", routerModels)
	}
}

func TestFindMatchingMmproj(t *testing.T) {
	model := ModelFile{ID: "Qwen2-VL-7B-Q4_K_M.gguf"}
	projectors := []ModelFile{
		{ID: "mmproj-Other-F16.gguf", Path: "other"},
		{ID: "mmproj-Qwen2-VL-7B-F16.gguf", Path: "matched"},
	}
	got := FindMatchingMmproj(model, projectors)
	if got == nil || got.Path != "matched" {
		t.Fatalf("unexpected match: %#v", got)
	}
	if got := FindMatchingMmproj(ModelFile{ID: "Llama.gguf"}, projectors); got != nil {
		t.Fatalf("unexpected unrelated match: %#v", got)
	}
}

func TestResolveModelDetectsDuplicateNestedNames(t *testing.T) {
	root := t.TempDir()
	touchFile(t, filepath.Join(root, "one", "same.gguf"))
	touchFile(t, filepath.Join(root, "two", "same.gguf"))
	if _, err := ResolveModel(root, "same.gguf", GenerationModel, generationExtensions); err == nil {
		t.Fatal("ambiguous basename was accepted")
	}
}

func TestResolveModelExplicitRelativePathUsesLauncherRoot(t *testing.T) {
	launcherRoot := t.TempDir()
	searchRoot := filepath.Join(launcherRoot, "models")
	path := filepath.Join(launcherRoot, "outside", "custom.gguf")
	touchFile(t, path)
	model, err := ResolveModelAt(searchRoot, launcherRoot, filepath.Join("outside", "custom.gguf"), GenerationModel, generationExtensions)
	if err != nil {
		t.Fatal(err)
	}
	if model.Path != path {
		t.Fatalf("explicit relative path resolved to %q, want %q", model.Path, path)
	}
}

func TestResolveModelBareFilenameFallsBackToLauncherRoot(t *testing.T) {
	launcherRoot := t.TempDir()
	searchRoot := filepath.Join(launcherRoot, "models")
	path := filepath.Join(launcherRoot, "custom.gguf")
	touchFile(t, path)

	model, err := ResolveModelAt(searchRoot, launcherRoot, "custom.gguf", GenerationModel, generationExtensions)
	if err != nil {
		t.Fatal(err)
	}
	if model.Path != path {
		t.Fatalf("bare root-relative path resolved to %q, want %q", model.Path, path)
	}
}

func TestResolveModelBareFilenamePrefersSearchDirectory(t *testing.T) {
	launcherRoot := t.TempDir()
	searchRoot := filepath.Join(launcherRoot, "models")
	rootModel := filepath.Join(launcherRoot, "same.gguf")
	searchedModel := filepath.Join(searchRoot, "nested", "same.gguf")
	touchFile(t, rootModel)
	touchFile(t, searchedModel)

	model, err := ResolveModelAt(searchRoot, launcherRoot, "same.gguf", GenerationModel, generationExtensions)
	if err != nil {
		t.Fatal(err)
	}
	if model.Path != searchedModel {
		t.Fatalf("bare model ID resolved to %q, want categorized model %q", model.Path, searchedModel)
	}
}
