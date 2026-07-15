package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateAPIKeyUsesConfiguredLength(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != GeneratedAPIKeyLength {
		t.Fatalf("API key length = %d, want %d", len(key), GeneratedAPIKeyLength)
	}
	if strings.ContainsAny(key, "+/=, \t\r\n") {
		t.Fatalf("API key is not URL-safe: %q", key)
	}
	if err := ValidateAPIKey(key); err != nil {
		t.Fatalf("generated API key is invalid: %v", err)
	}
}

func TestValidateAPIKeyRejectsUnsupportedValues(t *testing.T) {
	for _, value := range []string{
		"",
		strings.Repeat("a", MaxAPIKeyLength+1),
		strings.Repeat("a", MinAPIKeyLength-1),
		strings.Repeat("a", MinAPIKeyLength) + ",",
		strings.Repeat("a", MinAPIKeyLength) + " ",
		",",
		"contains\nnewline",
		"包含非 ASCII",
	} {
		if err := ValidateAPIKey(value); err == nil {
			t.Fatalf("invalid API key was accepted: %q", value)
		}
	}
}

func TestEnsureAPIKeyKeepsExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	oldKey := strings.Repeat("e", MinAPIKeyLength)
	if err := WriteAPIKeyFile(root, path, oldKey); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := ensureAPIKey(root, path, out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("existing key caused startup output: %q", out.String())
	}
	saved, exists, err := ReadAPIKeyFile(root, path)
	if err != nil || !exists || saved != oldKey {
		t.Fatalf("existing key file=%q exists=%v err=%v", saved, exists, err)
	}
}

func TestEnsureAPIKeyGeneratesMissingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ensureAPIKey(root, path, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), path) {
		t.Fatalf("generated key location missing from output=%q", out)
	}
	saved, exists, err := ReadAPIKeyFile(root, path)
	if err != nil || !exists || len(saved) != GeneratedAPIKeyLength {
		t.Fatalf("generated key file=%q exists=%v err=%v", saved, exists, err)
	}
}

func TestResetAPIKeyReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	oldKey := strings.Repeat("e", MinAPIKeyLength)
	if err := WriteAPIKeyFile(root, path, oldKey); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := resetAPIKey(root, path, out); err != nil {
		t.Fatal(err)
	}
	newKey, exists, err := ReadAPIKeyFile(root, path)
	if err != nil || !exists || newKey == oldKey || len(newKey) != GeneratedAPIKeyLength {
		t.Fatalf("reset key file=%q exists=%v err=%v", newKey, exists, err)
	}
	if !strings.Contains(out.String(), "已重置") || !strings.Contains(out.String(), path) {
		t.Fatalf("reset output missing result: %q", out.String())
	}
}

func TestWriteAndReadAPIKeyFileUsesPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", MinAPIKeyLength)
	if err := WriteAPIKeyFile(root, path, key); err != nil {
		t.Fatal(err)
	}
	got, exists, err := ReadAPIKeyFile(root, path)
	if err != nil || !exists || got != key {
		t.Fatalf("read key=%q exists=%v err=%v", got, exists, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("API key file permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestAPIKeyFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ConfigDirectoryName)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-key")
	if err := os.WriteFile(external, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, DefaultAPIKeyName)
	if err := os.Symlink(external, path); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, _, err := ReadAPIKeyFile(root, path); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("API key symlink was accepted for reading: %v", err)
	}
	if err := WriteAPIKeyFile(root, path, strings.Repeat("a", MinAPIKeyLength)); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("API key symlink was accepted for writing: %v", err)
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != "unchanged\n" {
		t.Fatalf("symlink target was modified: %q err=%v", data, err)
	}
}
