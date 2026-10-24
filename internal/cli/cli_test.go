package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type channelCheckSource struct{ channel string }

func (s *channelCheckSource) Latest(context.Context, string) (release.GitHubRelease, error) {
	return release.GitHubRelease{}, errors.New("channel selection bypassed")
}

func (s *channelCheckSource) LatestLlama(_ context.Context, channel string) (release.GitHubRelease, error) {
	s.channel = channel
	return release.GitHubRelease{Tag: "b123", ReleaseTag: "v0.7.0"}, nil
}

func (*channelCheckSource) Download(context.Context, release.Asset, string) error { return nil }

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

func TestLlamaInstallConfirmationDefaultsToYes(t *testing.T) {
	var output, stderr bytes.Buffer
	app := App{In: strings.NewReader("\n"), Out: &output, Err: &stderr, Interactive: true}
	if err := app.confirmAction("即将下载并安装或更新 llama.cpp，是否继续", false, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "即将下载并安装或更新 llama.cpp，是否继续 [Y/n]:") {
		t.Fatalf("output=%q", output.String())
	}

	output.Reset()
	app.In = strings.NewReader("n\n")
	if err := app.confirmAction("即将下载并安装或更新 llama.cpp，是否继续", false, true); !errors.Is(err, errCancelled) {
		t.Fatalf("err=%v", err)
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

func TestUpdateCheckChannelsAndJSON(t *testing.T) {
	for _, args := range [][]string{
		{"update", "check", "llama", "--json"},
		{"update", "check", "llama", "--channel", "stable", "--json"},
		{"update", "check", "--channel=stable", "--json", "llama"},
	} {
		l, _ := layout.New(filepath.Join(t.TempDir(), "LlamaLc"), "linux")
		source := &channelCheckSource{}
		var output, stderr bytes.Buffer
		app := App{Layout: l, Out: &output, Err: &stderr, Updates: update.NewManager(l, source)}
		if code := app.Run(args); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		var results []update.CheckResult
		if err := json.Unmarshal(output.Bytes(), &results); err != nil {
			t.Fatalf("invalid JSON output: %s: %v", output.String(), err)
		}
		want := release.ChannelStable
		if len(args) == 4 {
			want = release.ChannelPreview
		}
		if source.channel != want || len(results) != 1 || results[0].Channel != want {
			t.Fatalf("channel=%q results=%+v", source.channel, results)
		}
	}
}

func TestUpdateRejectsInvalidChannels(t *testing.T) {
	for _, args := range [][]string{
		{"update", "check", "llama", "--channel", "invalid"},
		{"update", "check", "llama", "--channel"},
		{"update", "check", "llama", "--channel=stable", "--channel=preview"},
		{"update", "check", "launcher", "--channel=stable"},
		{"update", "llama", "--channel", "invalid", "--yes"},
		{"update", "all", "--channel", "invalid", "--yes"},
	} {
		l, _ := layout.New(filepath.Join(t.TempDir(), "LlamaLc"), "linux")
		var output, stderr bytes.Buffer
		app := App{Layout: l, Out: &output, Err: &stderr, Updates: update.NewManager(l, &channelCheckSource{})}
		if code := app.Run(args); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestInteractiveChannelSelectionDefaultsToPreview(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"\n", release.ChannelPreview}, {"2\n", release.ChannelStable}, {"9\n1\n", release.ChannelPreview}} {
		var output, stderr bytes.Buffer
		app := App{Interactive: true, In: strings.NewReader(tc.input + "next\n"), Out: &output, Err: &stderr}
		options := update.LlamaOptions{}
		if err := app.chooseLlamaChannel(&options, false); err != nil || options.Channel != tc.want {
			t.Fatalf("input=%q options=%+v err=%v", tc.input, options, err)
		}
		remaining, _ := io.ReadAll(app.In)
		if string(remaining) != "next\n" {
			t.Fatalf("lost buffered input: %q", remaining)
		}
	}
	var output, stderr bytes.Buffer
	app := App{Interactive: true, In: strings.NewReader("q\n"), Out: &output, Err: &stderr}
	if err := app.chooseLlamaChannel(&update.LlamaOptions{}, false); !errors.Is(err, errBack) {
		t.Fatalf("back=%v", err)
	}
	output.Reset()
	if err := app.chooseLlamaChannel(&update.LlamaOptions{}, true); err != nil || output.Len() != 0 {
		t.Fatalf("--yes prompted: %s err=%v", output.String(), err)
	}
}

func TestLauncherUpdateDelegatesOptionsAndPreservesOutcome(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		result                     CommandOutcome
		wantHandoff, wantCancelled bool
	}{
		{"install", CommandOutcome{Handoff: true}, true, false},
		{"cancel", CommandOutcome{Cancelled: true}, false, true},
		{"current", CommandOutcome{Success: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := layout.New(filepath.Join(t.TempDir(), "LlamaLc"), "linux")
			var out, stderr bytes.Buffer
			// A self-update must never consult this llama-only release source.
			source := &channelCheckSource{}
			app := App{Layout: l, Out: &out, Err: &stderr, Updates: update.NewManager(l, source)}
			called := false
			app.LauncherUpdate = func(_ context.Context, options update.LauncherOptions, yes bool) (CommandOutcome, error) {
				called = true
				if options.Version != "v2.0.0" || !options.Reinstall || !options.AllowDowngrade || !yes {
					t.Fatalf("options=%+v yes=%v", options, yes)
				}
				return tc.result, nil
			}
			result := app.RunWithResult([]string{"update", "launcher", "--version", "v2.0.0", "--reinstall", "--allow-downgrade", "--yes"})
			if !called || result.Code != 0 || result.Handoff != tc.wantHandoff || result.Cancelled != tc.wantCancelled {
				t.Fatalf("called=%v result=%+v stderr=%s", called, result, &stderr)
			}
		})
	}
}
