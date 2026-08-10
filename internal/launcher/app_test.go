package launcher

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/update"
)

func TestUnknownAndVersionHaveNoFilesystemSideEffects(t *testing.T) {
	old := detectLayout
	defer func() { detectLayout = old }()
	called := false
	detectLayout = func() (layout.Layout, error) { called = true; return layout.Layout{}, errors.New("should not run") }
	var out, err bytes.Buffer
	if code := Main([]string{"serve"}, strings.NewReader(""), &out, &err, llama.OSExecutor{}); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if called {
		t.Fatal("unknown command detected layout")
	}
	err.Reset()
	if code := Main([]string{"version"}, strings.NewReader(""), &out, &err, llama.OSExecutor{}); code != 0 {
		t.Fatal(code)
	}
	if called {
		t.Fatal("version detected layout")
	}
	for _, args := range [][]string{{"run", "api", "--help"}, {"update"}, {"update", "llama", "--help"}} {
		out.Reset()
		err.Reset()
		if code := Main(args, strings.NewReader(""), &out, &err, llama.OSExecutor{}); code != 0 {
			t.Fatalf("args=%v code=%d", args, code)
		}
		if called {
			t.Fatalf("help detected layout: %v", args)
		}
	}
}

func TestFreshInitializationOnlyUsesV1Layout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	legacy := filepath.Join(root, "data", "llama.cpp")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(legacy, "keep")
	if err := os.WriteFile(sentinel, []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := MainWithLayout([]string{"key", "show"}, l, strings.NewReader(""), &out, &stderr, llama.OSExecutor{})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("legacy content changed: %v", err)
	}
	if _, err := os.Stat(l.ConfigFile); !os.IsNotExist(err) {
		t.Fatalf("key command unexpectedly initialized config: %v", err)
	}
	if _, err := os.Stat(l.APIKeyFile); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("legacy path was reported: %s", stderr.String())
	}
}

func TestOperationalInitializationReportsOnlyNewModelDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, err := layout.New(root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err = initializeOperational(l, &output); err != nil {
		t.Fatal(err)
	}
	first := output.String()
	modelDirectories := []string{l.GenerationModels, l.EmbeddingModels, l.RerankModels, l.MMProjModels}
	if count := strings.Count(first, "已创建目录:"); count != len(modelDirectories) {
		t.Fatalf("created directory messages=%d, output=%s", count, first)
	}
	for _, directory := range modelDirectories {
		if !strings.Contains(first, "已创建目录: "+directory) {
			t.Errorf("missing model directory %s in %s", directory, first)
		}
	}
	for _, want := range []string{
		"已自动生成 128 位 API key 并保存到: " + l.APIKeyFile,
		"API key 文件: " + l.APIKeyFile,
		"已生成配置: " + l.ConfigFile,
	} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q in %s", want, first)
		}
	}
	if directories, key, keyFile, configFile := strings.Index(first, "已创建目录:"), strings.Index(first, "已自动生成"), strings.Index(first, "API key 文件:"), strings.Index(first, "已生成配置:"); !(directories < key && key < keyFile && keyFile < configFile) {
		t.Fatalf("unexpected initialization message order: %s", first)
	}

	output.Reset()
	if _, err = initializeOperational(l, &output); err != nil {
		t.Fatal(err)
	}
	second := output.String()
	if strings.Contains(second, "已创建目录:") || strings.Contains(second, "已自动生成") || strings.Contains(second, "已生成配置:") {
		t.Fatalf("existing resources reported as newly created: %s", second)
	}
	if !strings.Contains(second, "API key 文件: "+l.APIKeyFile) {
		t.Fatalf("API key path not reported: %s", second)
	}
}

func TestReportActiveRuntimeShowsProbeFileAndVersion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test fixture is a Linux shell script")
	}
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, err := layout.New(root, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDirectory := filepath.Join(l.LlamaRuntimeDir, "cpu", "b10333")
	if err = os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(runtimeDirectory, "llama-server")
	for _, executable := range []string{server, filepath.Join(runtimeDirectory, "llama-cli")} {
		content := "#!/bin/sh\nprintf 'version: 10333 (08659901c)\\nbuilt with cc\\n'\n"
		if err = os.WriteFile(executable, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := update.State{Schema: update.StateSchema, LlamaTag: "b10333", Backend: "cpu", ActiveRuntime: "runtime/llama.cpp/cpu/b10333", Assets: []update.InstalledAsset{{Name: "runtime.tar.gz", SHA256: strings.Repeat("a", 64)}}}
	if err = update.SaveState(l, state); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	version, err := reportActiveRuntime(l, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"实际探测文件: " + server, "已识别 llama.cpp: version: 10333 (08659901c)"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in %s", want, output.String())
		}
	}
	if version != "b10333 / cpu — version: 10333 (08659901c)" {
		t.Fatalf("version=%q", version)
	}
}
