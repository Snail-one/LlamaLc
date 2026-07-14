package launcher

import (
	"os"
	"path/filepath"
	"strings"
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
	if config.Embedding.Pooling != "last" || config.Embedding.UBatchSize != 8192 {
		t.Fatalf("unexpected embedding defaults: %#v", config.Embedding)
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
	if config.Server.Port != 31000 || config.Server.Host != "127.0.0.1" || config.Embedding.Pooling != "mean" || config.Embedding.UBatchSize != 8192 {
		t.Fatalf("config precedence/merge failed: %#v", config)
	}
}

func TestLoadLegacyEmptyPoolingUsesNewDefaults(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, DefaultConfigName)
	if err := os.WriteFile(path, []byte(`{"embedding":{"pooling":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, _, _, err := LoadOrCreateConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.Embedding.Pooling != "last" || config.Embedding.UBatchSize != 8192 {
		t.Fatalf("legacy embedding defaults were not migrated: %#v", config.Embedding)
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

func TestLauncherRootFromBinDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	executable := filepath.Join(root, "bin", "llama-launcher.exe")
	got, err := launcherRootFromExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("launcher root = %q, want %q", got, root)
	}

	standaloneDir := filepath.Join(root, "tools")
	standalone := filepath.Join(standaloneDir, "llama-launcher.exe")
	if _, err := launcherRootFromExecutable(standalone); err == nil || !strings.Contains(err.Error(), "必须放在 bin 目录下") {
		t.Fatalf("standalone launcher should be rejected, got %v", err)
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
	if err := os.WriteFile(path, []byte(`{"embedding":{"ubatch_size":-1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadOrCreateConfig(root, ""); err == nil {
		t.Fatal("invalid ubatch-size was accepted")
	}
}
