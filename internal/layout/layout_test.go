package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentLayoutIgnoresLegacyPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, err := New(root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.ActiveTestPath(), filepath.Join(root, "runtime", "llama.cpp", "cpu", "b123"); got != want {
		t.Fatalf("path=%s want %s", got, want)
	}
	if err = os.MkdirAll(filepath.Join(root, "data", "llama.cpp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(l.ModelsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldModel := filepath.Join(l.ModelsDir, "old.gguf")
	if err = os.WriteFile(oldModel, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l.GenerationModels != filepath.Join(root, "models", "llm") {
		t.Fatalf("llm directory=%s", l.GenerationModels)
	}
	if _, err = os.Stat(filepath.Join(root, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("detection modified new layout: %v", err)
	}
}
func (l Layout) ActiveTestPath() string { return filepath.Join(l.LlamaRuntimeDir, "cpu", "b123") }

func TestExecutableMustBeUnderNamedRoot(t *testing.T) {
	_, err := FromExecutable(filepath.Join(t.TempDir(), "bin", "llamalc"), "linux")
	if err == nil {
		t.Fatal("expected root-name error")
	}
	root := filepath.Join(t.TempDir(), "LlamaLc")
	if err = os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "bin", "llamalc")
	if err = os.WriteFile(exe, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = FromExecutable(exe, "linux"); err != nil {
		t.Fatal(err)
	}
}
