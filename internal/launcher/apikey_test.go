package launcher

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	config.Server.APIKey = "existing-key"
	if err := WriteDefaultConfig(path, config); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	reader, err := prepareAPIKey(&config, path, false, strings.NewReader("\nchild input\n"), out)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.APIKey != "existing-key" {
		t.Fatalf("default answer unexpectedly reset key: %q", config.Server.APIKey)
	}
	remaining, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil || remaining != "child input\n" {
		t.Fatalf("buffered child input was lost: %q, %v", remaining, err)
	}

	oldKey := config.Server.APIKey
	if _, err := prepareAPIKey(&config, path, false, strings.NewReader("y\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
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
