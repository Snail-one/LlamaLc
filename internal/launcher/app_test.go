package launcher

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

type fakeExecutor struct {
	commands []Command
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	code     int
	err      error
}

type fakeInstallationProbe struct {
	commands []Command
	timeouts []time.Duration
	output   string
	err      error
}

func (f *fakeInstallationProbe) Probe(command Command, timeout time.Duration) (string, error) {
	f.commands = append(f.commands, command)
	f.timeouts = append(f.timeouts, timeout)
	if f.output == "" && f.err == nil {
		return "version: 9999 (test)\nbuilt with Go test for linux/amd64\n", nil
	}
	return f.output, f.err
}

func runTestMain(args []string, stdin io.Reader, stdout, stderr io.Writer, executor Executor, probe InstallationProbe) int {
	return mainWithProbe(args, stdin, stdout, stderr, executor, probe, "windows")
}

func menuInput(lines ...string) *bytes.Buffer {
	return bytes.NewBufferString(strings.Join(lines, "\n") + "\n")
}

func withoutManagedAuthentication(args []string) []string {
	filtered := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "--api-key-file" && index+1 < len(args) {
			index++
			continue
		}
		filtered = append(filtered, args[index])
	}
	return filtered
}

func mockExecutableInBin(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "bin", "llama-launcher.exe")
	touchFile(t, path)
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })
}

func (f *fakeExecutor) Execute(command Command, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	f.commands = append(f.commands, command)
	f.stdin, f.stdout, f.stderr = stdin, stdout, stderr
	return f.code, f.err
}

func TestMenuSeparatesLlamaAndLauncherUpdates(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root:     filepath.Join(t.TempDir(), "llama.cpp"),
		Config:   DefaultConfig(),
		Stdin:    menuInput("8", "n", "", "q"),
		Stdout:   stdout,
		Stderr:   stderr,
		Executor: &fakeExecutor{},
		Updater:  &UpdateManager{},
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, stderr)
	}
	for _, want := range []string{"7. 检查并更新 llama.cpp", "8. 检查并更新启动器", "9. 重置 API key", "将联网检查并更新启动器，是否继续"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("menu output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout.String(), "检查并更新启动器与 llama.cpp") {
		t.Fatalf("combined update option still present:\n%s", stdout)
	}
}

func TestMainFlagOverridesConfigAndForwardsStreams(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	touchFile(t, filepath.Join(root, "models", "chat.gguf"))
	config := `{"server":{"host":"config-host","port":30001,"n_gpu_layers":"0"}}`
	if err := os.MkdirAll(filepath.Dir(DefaultConfigPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DefaultConfigPath(root), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	in := bytes.NewBufferString("stdin")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	fake := &fakeExecutor{code: 23}
	code := runTestMain([]string{"serve", "--model", "chat.gguf", "--port", "30002", "--", "--threads", "8"}, in, out, errOut, fake, &fakeInstallationProbe{})
	if code != 23 || len(fake.commands) != 1 {
		t.Fatalf("unexpected result: code=%d calls=%d stderr=%s", code, len(fake.commands), errOut.String())
	}
	keyFile := lastArgumentValue("", fake.commands[0].Args, "--api-key-file")
	if keyFile != filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName) {
		t.Fatalf("managed API key file = %q", keyFile)
	}
	keyData, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	generatedKey := strings.TrimSpace(string(keyData))
	if len(generatedKey) != GeneratedAPIKeyLength {
		t.Fatalf("generated API key length = %d, want %d", len(generatedKey), GeneratedAPIKeyLength)
	}
	if lastArgumentValue("", fake.commands[0].Args, "--api-key") != "" || strings.Contains(strings.Join(fake.commands[0].Args, "\x00"), generatedKey) {
		t.Fatal("managed API key leaked into child process arguments")
	}
	_, configPath, _, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "api_key") || strings.Contains(string(configData), generatedKey) {
		t.Fatal("generated API key leaked into launcher.json")
	}
	wantTail := []string{"--host", "config-host", "--port", "30002", "--no-ui", "--threads", "8"}
	args := withoutManagedAuthentication(fake.commands[0].Args)
	if !reflect.DeepEqual(args[len(args)-len(wantTail):], wantTail) {
		t.Fatalf("flags/config/extra precedence failed: %#v", args)
	}
	if fake.stdin != in || fake.stdout != out || fake.stderr != errOut {
		t.Fatal("standard streams were not connected to executor")
	}
	if !strings.Contains(errOut.String(), "/models、/v1/models") || !strings.Contains(errOut.String(), "不受 API key 保护") {
		t.Fatalf("remote public endpoint warning was not displayed: %q", errOut.String())
	}
}

func TestMenuCancellationDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	touchFile(t, filepath.Join(root, "models", "chat.gguf"))
	in := menuInput(
		"1", "1", // mode and model
		"",                         // other mmproj
		"", "", "", "", "", "", "", // runtime defaults
		"", "", "", // network defaults, including disabled UI
		"",  // custom arguments
		"n", // cancel after final command preview
		"",  // pause
		"q",
	)
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	fake := &fakeExecutor{}
	code := runTestMain(nil, in, out, errOut, fake, &fakeInstallationProbe{})
	if code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	if len(fake.commands) != 0 {
		t.Fatalf("cancelled launch executed process: %#v", fake.commands)
	}
	if !strings.Contains(out.String(), buildversion.Version) {
		t.Fatalf("menu header does not contain version %q: %s", buildversion.Version, out.String())
	}
	if !strings.Contains(out.String(), "llama.cpp: version: 9999 (test)") {
		t.Fatalf("menu header does not contain detected llama.cpp version: %s", out.String())
	}
}

func TestEmbeddingMenuUsesDefaultsAndForwardsCustomArguments(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	touchFile(t, filepath.Join(root, "embeddings", "embed.gguf"))
	in := menuInput(
		"2", "1", // mode and model
		"", "", "", "", "", "", "", // runtime defaults
		"", "", // pooling and normalization defaults
		"", "", "", // network defaults, including disabled UI
		`--threads 8 --log-prefix "hello world"`,
		"y", "", "q", // confirm, pause, exit
	)
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	fake := &fakeExecutor{}
	code := runTestMain(nil, in, out, errOut, fake, &fakeInstallationProbe{})
	if code != 0 || len(fake.commands) != 1 {
		t.Fatalf("unexpected menu result: code=%d calls=%d stderr=%s", code, len(fake.commands), errOut.String())
	}
	wantTail := []string{
		"--embedding", "--pooling", "last", "--ubatch-size", "8192",
		"--embd-normalize", "2",
		"--host", "127.0.0.1", "--port", "29856", "--no-ui",
		"--threads", "8", "--log-prefix", "hello world",
	}
	args := withoutManagedAuthentication(fake.commands[0].Args)
	if !reflect.DeepEqual(args[len(args)-len(wantTail):], wantTail) {
		t.Fatalf("embedding defaults/custom arguments mismatch:\n got: %#v\nwant tail: %#v", args, wantTail)
	}
	previewAt := strings.Index(out.String(), "最终命令:")
	confirmAt := strings.Index(out.String(), "确认使用以上参数启动")
	if previewAt < 0 || confirmAt < 0 || previewAt > confirmAt {
		t.Fatalf("final command was not displayed before confirmation:\n%s", out.String())
	}
}

func TestVersionCommandsOnlyPrintVersion(t *testing.T) {
	for _, versionFlag := range []string{"-v", "--version", "version"} {
		t.Run(versionFlag, func(t *testing.T) {
			oldVersion, oldCommit, oldBuildDate := buildversion.Version, buildversion.Commit, buildversion.BuildDate
			buildversion.Version = "v9.8.7-test"
			buildversion.Commit = "abc1234"
			buildversion.BuildDate = "2026-07-14T12:00:00Z"
			t.Cleanup(func() {
				buildversion.Version, buildversion.Commit, buildversion.BuildDate = oldVersion, oldCommit, oldBuildDate
			})

			out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
			fake := &fakeExecutor{}
			code := Main([]string{versionFlag}, bytes.NewBuffer(nil), out, errOut, fake)
			if code != 0 {
				t.Fatalf("version returned %d: %s", code, errOut.String())
			}
			want := "Version:   v9.8.7-test\nCommit:    abc1234\nBuildDate: 2026-07-14T12:00:00Z\n"
			if out.String() != want {
				t.Fatalf("unexpected version output: %q", out.String())
			}
			if errOut.Len() != 0 || len(fake.commands) != 0 {
				t.Fatalf("version inspection had side effects: stderr=%q commands=%#v", errOut.String(), fake.commands)
			}
		})
	}
}

func TestMainRejectsExecutableOutsideBin(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "llama-launcher.exe")
	touchFile(t, path)
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	code := runTestMain(nil, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{}, &fakeInstallationProbe{})
	if code != 1 || !strings.Contains(errOut.String(), "必须放在 bin 目录下") {
		t.Fatalf("outside-bin executable was not rejected: code=%d stderr=%q", code, errOut.String())
	}
	if _, err := os.Stat(DefaultConfigPath(root)); !os.IsNotExist(err) {
		t.Fatalf("location validation should happen before config creation, stat error: %v", err)
	}
	for _, directory := range []string{"config", "models", "embeddings", "rerank", "mmproj"} {
		if _, err := os.Stat(filepath.Join(root, directory)); !os.IsNotExist(err) {
			t.Fatalf("location validation should happen before creating %s, stat error: %v", directory, err)
		}
	}

}

func TestMainCreatesRuntimeLayoutAfterLocationValidation(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	probe := &fakeInstallationProbe{}
	code := runTestMain(nil, menuInput("q"), out, errOut, &fakeExecutor{}, probe)
	if code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	for _, directory := range []string{"config", "models", "embeddings", "rerank", "mmproj"} {
		info, err := os.Stat(filepath.Join(root, directory))
		if err != nil || !info.IsDir() {
			t.Fatalf("runtime directory %s was not created: info=%v err=%v", directory, info, err)
		}
	}
	if _, err := os.Stat(DefaultConfigPath(root)); err != nil {
		t.Fatalf("config/launcher.json was not created: %v", err)
	}
	if !strings.Contains(out.String(), "实际探测文件: "+filepath.Join(root, "llama-server.exe")) {
		t.Fatalf("probe path was not displayed: %q", out.String())
	}
	if len(probe.commands) != 1 || probe.commands[0].Path != filepath.Join(root, "llama-server.exe") ||
		!reflect.DeepEqual(probe.commands[0].Args, []string{"--version"}) || probe.commands[0].Dir != root ||
		probe.timeouts[0] != 30*time.Second {
		t.Fatalf("unexpected installation probe: commands=%#v timeouts=%#v", probe.commands, probe.timeouts)
	}
}

func TestMainUsesExistingAPIKeyWithoutStartupPrompt(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	if err := os.MkdirAll(filepath.Dir(DefaultConfigPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	if err := WriteDefaultConfig(DefaultConfigPath(root), config); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	oldKey := strings.Repeat("k", MinAPIKeyLength)
	if err := WriteAPIKeyFile(root, keyPath, oldKey); err != nil {
		t.Fatal(err)
	}

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	code := runTestMain(nil, menuInput("q"), out, errOut, &fakeExecutor{}, &fakeInstallationProbe{})
	if code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "是否重置 API key") {
		t.Fatalf("startup still asks to reset API key: %q", out.String())
	}
	saved, exists, err := ReadAPIKeyFile(root, keyPath)
	if err != nil || !exists || saved != oldKey {
		t.Fatalf("default reset answer changed key: %q exists=%v err=%v", saved, exists, err)
	}
}

func TestMenuResetsAPIKeyOnlyAfterConfirmation(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, ConfigDirectoryName, DefaultAPIKeyName)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	oldKey := strings.Repeat("k", MinAPIKeyLength)
	if err := WriteAPIKeyFile(root, keyPath, oldKey); err != nil {
		t.Fatal(err)
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &Application{
		Root: root, Config: DefaultConfig(), Paths: ResolvedPaths{APIKeyFile: keyPath},
		Stdin: menuInput("9", "y", "", "q"), Stdout: out, Stderr: errOut,
	}
	if code := app.RunMenu(); code != 0 {
		t.Fatalf("menu returned %d: %s", code, errOut.String())
	}
	newKey, exists, err := ReadAPIKeyFile(root, keyPath)
	if err != nil || !exists || newKey == oldKey || len(newKey) != GeneratedAPIKeyLength {
		t.Fatalf("menu reset key=%q exists=%v err=%v", newKey, exists, err)
	}
	if !strings.Contains(out.String(), "旧 key 将立即失效") || !strings.Contains(out.String(), "已重置") {
		t.Fatalf("menu reset output missing confirmation/result: %q", out.String())
	}
}

func TestQReturnsFromInteractivePrompts(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	touchFile(t, filepath.Join(root, "models", "chat.gguf"))

	tests := []struct {
		name  string
		input *bytes.Buffer
	}{
		{name: "main menu", input: menuInput("Q")},
		{name: "model selection", input: menuInput("1", "q", "q")},
		{name: "parameter prompt", input: menuInput("1", "1", "", "q", "q")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
			executor := &fakeExecutor{}
			code := runTestMain(nil, test.input, out, errOut, executor, &fakeInstallationProbe{})
			if code != 0 || len(executor.commands) != 0 {
				t.Fatalf("q did not return cleanly: code=%d commands=%#v stderr=%q", code, executor.commands, errOut.String())
			}
		})
	}
}

func TestMainRejectsRemovedPathFlagsWithoutSideEffects(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	for _, args := range [][]string{{"--root", root}, {"serve", "--config=other.json"}} {
		probe := &fakeInstallationProbe{}
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		code := runTestMain(args, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{}, probe)
		if code != 2 || !strings.Contains(errOut.String(), "已移除") || len(probe.commands) != 0 {
			t.Fatalf("removed flag was not rejected early: args=%#v code=%d stderr=%q probes=%d", args, code, errOut.String(), len(probe.commands))
		}
		if _, err := os.Stat(filepath.Join(root, "config")); !os.IsNotExist(err) {
			t.Fatalf("removed flag created files: %v", err)
		}
	}
}

func TestMainProbeFailureDoesNotCreateLayout(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	probe := &fakeInstallationProbe{output: "not a llama version"}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	code := runTestMain(nil, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{}, probe)
	if code != 1 || !strings.Contains(errOut.String(), "输出无法识别") {
		t.Fatalf("bad probe output was accepted: code=%d stderr=%q", code, errOut.String())
	}
	for _, directory := range []string{"config", "models", "embeddings", "rerank", "mmproj"} {
		if _, err := os.Stat(filepath.Join(root, directory)); !os.IsNotExist(err) {
			t.Fatalf("probe failure created %s: %v", directory, err)
		}
	}
}

func TestMainMissingServerDoesNotCreateLayout(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	probe := &fakeInstallationProbe{}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	code := runTestMain(nil, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{}, probe)
	if code != 1 || !strings.Contains(errOut.String(), "找不到 llama-server") || len(probe.commands) != 0 {
		t.Fatalf("missing server was not rejected early: code=%d stderr=%q probes=%d", code, errOut.String(), len(probe.commands))
	}
	for _, directory := range []string{"config", "models", "embeddings", "rerank", "mmproj"} {
		if _, err := os.Stat(filepath.Join(root, directory)); !os.IsNotExist(err) {
			t.Fatalf("missing server created %s: %v", directory, err)
		}
	}
}

func TestMainCorruptConfigDoesNotCreateModelDirectories(t *testing.T) {
	root := t.TempDir()
	mockExecutableInBin(t, root)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	path := DefaultConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"paths":{"models":"elsewhere"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	code := runTestMain(nil, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{}, &fakeInstallationProbe{})
	if code != 1 || !strings.Contains(errOut.String(), "unknown field") {
		t.Fatalf("old config was accepted: code=%d stderr=%q", code, errOut.String())
	}
	for _, directory := range []string{"models", "embeddings", "rerank", "mmproj"} {
		if _, err := os.Stat(filepath.Join(root, directory)); !os.IsNotExist(err) {
			t.Fatalf("corrupt config created %s: %v", directory, err)
		}
	}
}

func TestHelpAndUnknownCommandDoNotProbeOrCreateFiles(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"serve", "--help"}, {"unknown"}} {
		root := t.TempDir()
		mockExecutableInBin(t, root)
		probe := &fakeInstallationProbe{}
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		_ = runTestMain(args, bytes.NewBuffer(nil), out, errOut, &fakeExecutor{}, probe)
		if len(probe.commands) != 0 {
			t.Fatalf("help/unknown command triggered probe: args=%#v", args)
		}
		if _, err := os.Stat(filepath.Join(root, "config")); !os.IsNotExist(err) {
			t.Fatalf("help/unknown command created files: args=%#v err=%v", args, err)
		}
	}
}
