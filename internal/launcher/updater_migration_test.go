package launcher

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyUpdaterToLlamaup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(bin, "llama-updater.exe")
	current := filepath.Join(bin, "llamaup.exe")
	if err := os.WriteFile(legacy, []byte("updater"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	if err := migrateLegacyUpdater(root, "windows", output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy updater remained: %v", err)
	}
	content, err := os.ReadFile(current)
	if err != nil || string(content) != "updater" {
		t.Fatalf("llamaup content=%q err=%v", content, err)
	}
	if !strings.Contains(output.String(), current) {
		t.Fatalf("migration output missing target: %q", output)
	}
	if err := migrateLegacyUpdater(root, "windows", output); err != nil {
		t.Fatalf("migration was not idempotent: %v", err)
	}
}

func TestMigrateLegacyUpdaterDoesNotReplaceExistingLlamaup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(bin, "llama-updater")
	current := filepath.Join(bin, "llamaup")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyUpdater(root, "linux", nil); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{legacy: "legacy", current: "current"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("%s=%q err=%v", path, content, err)
		}
	}
}
