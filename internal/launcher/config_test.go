package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()
	if config.Server.Host != "127.0.0.1" || config.Server.Port != 29856 {
		t.Fatalf("unexpected server defaults: %#v", config.Server)
	}
	if config.Server.GPULayers != "auto" || config.Server.UI {
		t.Fatalf("unexpected GPU/UI defaults: %#v", config.Server)
	}
	if config.Router.ModelsMax != 1 || !config.Router.Autoload {
		t.Fatalf("unexpected router defaults: %#v", config.Router)
	}
}

func TestLoadConfigMergesWithDefaults(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom.json")
	if err := os.WriteFile(path, []byte(`{"server":{"port":31000},"embedding":{"pooling":"mean"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, _, created, err := LoadOrCreateConfig(root, "custom.json")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing config reported as created")
	}
	if config.Server.Port != 31000 || config.Server.Host != "127.0.0.1" || config.Embedding.Pooling != "mean" {
		t.Fatalf("config precedence/merge failed: %#v", config)
	}
}

func TestLoadOrCreateDefaultConfig(t *testing.T) {
	root := t.TempDir()
	config, path, created, err := LoadOrCreateConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || config.Server.Port != 29856 {
		t.Fatalf("unexpected result: created=%v config=%#v", created, config)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
}

func TestResolveWindowsPath(t *testing.T) {
	input := `D:\models\Qwen 3.gguf`
	if got := ResolvePath(`/launcher`, input); got != filepath.Clean(input) {
		t.Fatalf("Windows absolute path was joined to root: %q", got)
	}
	if got := ResolvePath(`/launcher`, `models\x.gguf`); got != filepath.Clean(filepath.Join(`/launcher`, `models\x.gguf`)) {
		t.Fatalf("relative path mismatch: %q", got)
	}
}

func TestInvalidConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, DefaultConfigName)
	if err := os.WriteFile(path, []byte(`{"server":{"port":70000}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadOrCreateConfig(root, ""); err == nil {
		t.Fatal("invalid port was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"embedding":{"pooling":"bogus"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadOrCreateConfig(root, ""); err == nil {
		t.Fatal("invalid pooling was accepted")
	}
}
