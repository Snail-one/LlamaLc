package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/config"
	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/update"
)

func TestUsageContainsCompleteV1Tree(t *testing.T) {
	var out, err bytes.Buffer
	a := App{Config: config.Default(), In: strings.NewReader(""), Out: &out, Err: &err}
	if code := a.Run([]string{"help"}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"run api", "run embedding", "run rerank", "run router", "run chat", "config router generate", "config key show|reset", "update check", "update llama", "update launcher", "maintenance cleanup", "version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}

type exitExecutor struct{ code int }

func (executor exitExecutor) Execute(llama.Command, io.Reader, io.Writer, io.Writer) (int, error) {
	return executor.code, nil
}

func TestRunPropagatesLlamaExitCodeAndFlagErrorsUseCodeTwo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	for _, directory := range l.Directories() {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	model := filepath.Join(l.GenerationModels, "model.gguf")
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory := filepath.Join(l.LlamaRuntimeDir, "cpu", "b123")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"llama-server", "llama-cli"} {
		if err := os.WriteFile(filepath.Join(runtimeDirectory, name), []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := update.State{Schema: update.StateSchema, LlamaTag: "b123", Backend: "cpu", ActiveRuntime: "runtime/llama.cpp/cpu/b123", Assets: []update.InstalledAsset{{Name: "runtime.tar.gz", SHA256: strings.Repeat("a", 64)}}}
	if err := update.SaveState(l, state); err != nil {
		t.Fatal(err)
	}
	var output, errorOutput bytes.Buffer
	a := App{Layout: l, Config: config.Default(), In: strings.NewReader(""), Out: &output, Err: &errorOutput, Executor: exitExecutor{code: 7}, GOOS: "linux"}
	if code := a.Run([]string{"run", "api", "--model", model}); code != 7 {
		t.Fatalf("exit code=%d stderr=%s", code, errorOutput.String())
	}
	errorOutput.Reset()
	if code := a.Run([]string{"run", "api", "--not-a-flag"}); code != 2 {
		t.Fatalf("syntax code=%d stderr=%s", code, errorOutput.String())
	}
}
func TestHelpAndRemovedAliases(t *testing.T) {
	for _, args := range [][]string{{"run", "api", "--help"}, {"update", "llama", "--help"}, {"maintenance", "cleanup", "--help"}} {
		var out, err bytes.Buffer
		a := App{Config: config.Default(), Out: &out, Err: &err}
		if code := a.Run(args); code != 0 || !strings.Contains(out.String(), "用法:") {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out.String(), err.String())
		}
	}
	for _, old := range []string{"serve", "install", "router-config"} {
		var out, err bytes.Buffer
		a := App{Out: &out, Err: &err}
		if code := a.Run([]string{old}); code != 2 {
			t.Fatalf("%s code=%d", old, code)
		}
	}
}
