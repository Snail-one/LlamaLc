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
	if config.Server.ContextSize != 0 || config.Server.Threads != -1 || config.Server.BatchSize != 2048 ||
		config.Server.UBatchSize != 512 || config.Server.FlashAttention != "auto" || config.Server.Parallel != -1 {
		t.Fatalf("unexpected official runtime defaults: %#v", config.Server)
	}
	if config.Embedding.Pooling != "last" || config.Embedding.BatchSize != 8192 || config.Embedding.UBatchSize != 8192 || config.Embedding.Normalize != 2 {
		t.Fatalf("unexpected embedding defaults: %#v", config.Embedding)
	}
	if config.Router.ModelsMax != 1 || !config.Router.Autoload {
		t.Fatalf("unexpected router defaults: %#v", config.Router)
	}
}

func TestLoadConfigMergesWithDefaults(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"server":{"port":31000},"embedding":{"pooling":"mean"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, _, needsCreate, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if needsCreate {
		t.Fatal("existing config reported as missing")
	}
	if config.Server.Port != 31000 || config.Server.Host != "127.0.0.1" || config.Embedding.Pooling != "mean" || config.Embedding.BatchSize != 8192 || config.Embedding.UBatchSize != 8192 {
		t.Fatalf("config precedence/merge failed: %#v", config)
	}
}

func TestLoadEmptyPoolingUsesDefault(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"embedding":{"pooling":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, _, _, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Embedding.Pooling != "last" || config.Embedding.BatchSize != 8192 || config.Embedding.UBatchSize != 8192 {
		t.Fatalf("legacy embedding defaults were not migrated: %#v", config.Embedding)
	}
}

func TestLoadDefaultConfigWithoutWriting(t *testing.T) {
	root := t.TempDir()
	config, path, needsCreate, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !needsCreate || config.Server.Port != 29856 {
		t.Fatalf("unexpected result: needsCreate=%v config=%#v", needsCreate, config)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config load unexpectedly wrote a file: %v", err)
	}
	if path != DefaultConfigPath(root) {
		t.Fatalf("default config path = %q, want %q", path, DefaultConfigPath(root))
	}
}

func TestDefaultConfigIgnoresOldRootConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "launcher.json"), []byte(`{"server":{"port":70000}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, path, needsCreate, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !needsCreate || path != DefaultConfigPath(root) || config.Server.Port != 29856 {
		t.Fatalf("old root config affected new layout: needsCreate=%v path=%q config=%#v", needsCreate, path, config)
	}
}

func TestEnsureRuntimeDirectoriesValidatesBeforeCreating(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolveFixedPaths(root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Models, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRuntimeDirectories(root); err == nil {
		t.Fatal("models file was accepted as a directory")
	}
	for _, path := range []string{paths.Embeddings, paths.Rerank, paths.Mmproj, filepath.Join(root, ConfigDirectoryName)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("directory was created before validation completed: %s (err=%v)", path, err)
		}
	}
}

func TestResolveFixedPathsByPlatform(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	windows, err := ResolveFixedPaths(root, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if windows.Server != filepath.Join(root, "llama-server.exe") || windows.CLI != filepath.Join(root, "llama-cli.exe") || windows.CLIFallback != filepath.Join(root, "llama.exe") {
		t.Fatalf("unexpected Windows paths: %#v", windows)
	}
	linux, err := ResolveFixedPaths(root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if linux.Server != filepath.Join(root, "llama-server") || linux.CLI != filepath.Join(root, "llama-cli") || linux.CLIFallback != filepath.Join(root, "llama") {
		t.Fatalf("unexpected Linux paths: %#v", linux)
	}
	if _, err := ResolveFixedPaths(root, "darwin"); err == nil {
		t.Fatal("unsupported platform was accepted")
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
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"server":{"port":70000}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadConfig(root); err == nil {
		t.Fatal("invalid port was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"embedding":{"pooling":"bogus"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadConfig(root); err == nil {
		t.Fatal("invalid pooling was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"embedding":{"ubatch_size":-1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadConfig(root); err == nil {
		t.Fatal("invalid ubatch-size was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"server":{"flash_attention":"fast"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadConfig(root); err == nil {
		t.Fatal("invalid flash-attn was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"embedding":{"batch_size":2048,"ubatch_size":8192}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadConfig(root); err == nil {
		t.Fatal("embedding batch-size smaller than ubatch-size was accepted")
	}
}

func TestConfigRejectsRemovedPathsSection(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"paths":{"models":"elsewhere"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadConfig(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("removed paths section was accepted: %v", err)
	}
}

func TestWriteDefaultConfigIsExclusive(t *testing.T) {
	root := t.TempDir()
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefaultConfig(path, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefaultConfig(path, DefaultConfig()); err == nil {
		t.Fatal("existing config was overwritten")
	}
}
