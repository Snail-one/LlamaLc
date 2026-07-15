package launcher

import (
	"bufio"
	"bytes"
	"encoding/json"
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

func TestPrepareAPIKeyKeepsOrResetsPersistedKey(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Server.APIKey = strings.Repeat("e", MinAPIKeyLength)
	if err := WriteDefaultConfig(path, config); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	reader, err := prepareAPIKey(&config, path, false, strings.NewReader("\nchild input\n"), out)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.APIKey != strings.Repeat("e", MinAPIKeyLength) {
		t.Fatalf("default answer unexpectedly reset key: %q", config.Server.APIKey)
	}
	if !strings.Contains(out.String(), path) || !strings.Contains(out.String(), "server.api_key") {
		t.Fatalf("API key location was not displayed: %q", out.String())
	}
	remaining, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil || remaining != "child input\n" {
		t.Fatalf("buffered child input was lost: %q, %v", remaining, err)
	}

	oldKey := config.Server.APIKey
	resetOut := &bytes.Buffer{}
	if _, err := prepareAPIKey(&config, path, false, strings.NewReader("y\n"), resetOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resetOut.String(), path) || !strings.Contains(resetOut.String(), "server.api_key") {
		t.Fatalf("reset API key location was not displayed: %q", resetOut.String())
	}
	if config.Server.APIKey == oldKey || len(config.Server.APIKey) != GeneratedAPIKeyLength {
		t.Fatalf("key was not reset: length=%d", len(config.Server.APIKey))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Server.APIKey != config.Server.APIKey {
		t.Fatal("reset API key was not persisted")
	}
}

func TestWriteAPIKeyFileUsesPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", MinAPIKeyLength)
	if err := WriteAPIKeyFile(root, path, key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != key+"\n" {
		t.Fatalf("API key file content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("API key file permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestWriteAPIKeyFileRejectsSymlink(t *testing.T) {
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
	if err := WriteAPIKeyFile(root, path, strings.Repeat("a", MinAPIKeyLength)); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("API key symlink was accepted: %v", err)
	}
	data, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged\n" {
		t.Fatalf("symlink target was modified: %q", data)
	}
}

func TestPrepareAPIKeyDisplaysLocationAfterGeneration(t *testing.T) {
	config := DefaultConfig()
	path := filepath.Join(t.TempDir(), "config", "launcher.json")
	out := &bytes.Buffer{}

	if _, err := prepareAPIKey(&config, path, true, strings.NewReader(""), out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), path) || !strings.Contains(out.String(), "server.api_key") {
		t.Fatalf("generated API key location was not displayed: %q", out.String())
	}
}

func TestReadStartupYesNoDefaultsToNo(t *testing.T) {
	out := &bytes.Buffer{}
	answer, err := readStartupYesNo(bufio.NewReader(strings.NewReader("\n")), out, "reset", false)
	if err != nil || answer {
		t.Fatalf("default answer = %v, err=%v", answer, err)
	}
	if !strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("default was not displayed: %q", out.String())
	}
}
