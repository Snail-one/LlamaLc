package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/config"
	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/release"
	"github.com/Snail-one/LlamaLc/internal/update"
)

type checkSource struct{}

func (checkSource) Latest(_ context.Context, _ string) (release.GitHubRelease, error) {
	return release.GitHubRelease{Tag: "b123"}, nil
}
func (checkSource) Download(context.Context, release.Asset, string) error { return nil }

func TestUsageContainsOnlyPublicCommandTree(t *testing.T) {
	var out, err bytes.Buffer
	a := App{Config: config.Default(), In: strings.NewReader(""), Out: &out, Err: &err}
	if code := a.Run([]string{"help"}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"run api", "run embedding", "run rerank", "run router", "run chat", "router generate", "key show", "key reset", "update check", "update llama", "update launcher", "cleanup", "version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, removed := range []string{"config router", "config key", "maintenance cleanup"} {
		if strings.Contains(out.String(), removed) {
			t.Errorf("removed alias exposed: %q", removed)
		}
	}
}

type exitExecutor struct{ code int }

func (executor exitExecutor) Execute(llama.Command, io.Reader, io.Writer, io.Writer) (int, error) {
	return executor.code, nil
}

type captureExecutor struct {
	calls   int
	command llama.Command
}

func (executor *captureExecutor) Execute(command llama.Command, _ io.Reader, _, _ io.Writer) (int, error) {
	executor.calls++
	executor.command = command
	return 0, nil
}

func runnableApp(t *testing.T, interactive bool, input string) (*App, *captureExecutor, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
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
	state := update.State{Schema: 1, LlamaTag: "b123", Backend: "cpu", ActiveRuntime: "runtime/llama.cpp/cpu/b123", Assets: []update.InstalledAsset{{Name: "runtime.tar.gz", SHA256: strings.Repeat("a", 64)}}}
	if err := update.SaveState(l, state); err != nil {
		t.Fatal(err)
	}
	executor, output, errorOutput := &captureExecutor{}, &bytes.Buffer{}, &bytes.Buffer{}
	app := &App{Layout: l, Config: config.Default(), In: strings.NewReader(input), Out: output, Err: errorOutput, Executor: executor, GOOS: "linux", Interactive: interactive}
	return app, executor, output, errorOutput
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
	for _, args := range [][]string{{"run", "api", "--help"}, {"update", "llama", "--help"}, {"cleanup", "--help"}} {
		var out, err bytes.Buffer
		a := App{Config: config.Default(), Out: &out, Err: &err}
		if code := a.Run(args); code != 0 || !strings.Contains(out.String(), "用法:") {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out.String(), err.String())
		}
	}
	for _, old := range []string{"serve", "install", "router-config", "config", "maintenance"} {
		var out, err bytes.Buffer
		a := App{Out: &out, Err: &err}
		if code := a.Run([]string{old}); code != 2 {
			t.Fatalf("%s code=%d", old, code)
		}
	}
	var out, err bytes.Buffer
	if code := (&App{Out: &out, Err: &err}).Run([]string{"config", "--help"}); code != 2 {
		t.Fatalf("removed help alias code=%d", code)
	}
}

func TestRunModeFlagsAndExtraSeparator(t *testing.T) {
	for _, args := range [][]string{{"run", "router", "--model", "x.gguf"}, {"run", "chat", "--host", "127.0.0.1"}, {"run", "api", "--pooling", "last"}, {"run", "api", "--model", "model.gguf", "--jinja"}} {
		app, executor, _, _ := runnableApp(t, false, "")
		if code := app.Run(args); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
		if executor.calls != 0 {
			t.Fatalf("invalid args executed: %v", args)
		}
	}
	app, executor, _, stderr := runnableApp(t, false, "")
	if code := app.Run([]string{"run", "api", "--model", "model.gguf", "--", "--jinja"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if got := executor.command.Args[len(executor.command.Args)-1]; got != "--jinja" {
		t.Fatalf("extra=%q", got)
	}
}

func TestInteractiveRunConfirmsFinalCommand(t *testing.T) {
	app, executor, output, _ := runnableApp(t, true, "n\n")
	if code := app.Run([]string{"run", "api", "--model", "model.gguf"}); code != 0 {
		t.Fatal(code)
	}
	if executor.calls != 0 {
		t.Fatal("cancelled command executed")
	}
	if !strings.Contains(output.String(), "最终命令") || !strings.Contains(output.String(), "确认启动以上命令") {
		t.Fatalf("output=%s", output)
	}
}

func TestBareUpdateIsHelpAndKeyResetRequiresYesNonInteractive(t *testing.T) {
	var output, stderr bytes.Buffer
	app := App{Out: &output, Err: &stderr, In: strings.NewReader("")}
	if code := app.Run([]string{"update"}); code != 0 || !strings.Contains(output.String(), "update check") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	app.Layout, output, stderr = l, bytes.Buffer{}, bytes.Buffer{}
	app.Out, app.Err = &output, &stderr
	if code := app.Run([]string{"key", "reset"}); code != 1 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(l.APIKeyFile); !os.IsNotExist(err) {
		t.Fatalf("key was written: %v", err)
	}
}

func TestUpdateCheckJSONAcceptsFlagAfterTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	manager := update.NewManager(l, checkSource{})
	var output, stderr bytes.Buffer
	app := App{Layout: l, In: strings.NewReader(""), Out: &output, Err: &stderr, Updates: manager}
	if code := app.Run([]string{"update", "check", "llama", "--json"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"component":"llama"`, `"installed":""`, `"latest":"b123"`, `"available":true`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %s in %s", want, output.String())
		}
	}
}
