package updater

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinishUpdateAutomaticallyStartsWindowsLauncher(t *testing.T) {
	original := launchUpdatedLauncher
	t.Cleanup(func() { launchUpdatedLauncher = original })
	var startedRoot string
	var startedVersion string
	launchUpdatedLauncher = func(root, version string, _, _ io.Writer) error {
		startedRoot = root
		startedVersion = version
		return nil
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root := filepath.Join(t.TempDir(), "LlamaLc")
	if code := finishUpdate(root, "windows", "v1.2.3", nil, stdout, stderr); code != 0 {
		t.Fatalf("finish code=%d stderr=%s", code, stderr)
	}
	if startedRoot != root {
		t.Fatalf("started root=%q, want %q", startedRoot, root)
	}
	if startedVersion != "v1.2.3" {
		t.Fatalf("started version=%q, want v1.2.3", startedVersion)
	}
	for _, want := range []string{"更新完成", "启动器: v1.2.3", "文件替换成功", "正在自动启动新版本"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("completion output missing %q: %s", want, stdout)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
}

func TestFinishUpdateReportsAutomaticStartFailure(t *testing.T) {
	original := launchUpdatedLauncher
	t.Cleanup(func() { launchUpdatedLauncher = original })
	launchUpdatedLauncher = func(string, string, io.Writer, io.Writer) error {
		return errors.New("start failed")
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := finishUpdate(t.TempDir(), "windows", "v1.2.3", bytes.NewBufferString("\n"), stdout, stderr); code != 1 {
		t.Fatalf("finish code=%d, want 1", code)
	}
	for _, want := range []string{"文件已更新，但无法自动启动", "请手动启动 bin\\llamalc.exe", "按 Enter 关闭"} {
		if !bytes.Contains(stderr.Bytes(), []byte(want)) {
			t.Fatalf("failure output missing %q: %s", want, stderr)
		}
	}
}

func TestVersionDoesNotRequireDeploymentLayout(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Main([]string{"--version"}, nil, stdout, stderr); code != 0 {
		t.Fatalf("version code=%d stderr=%s", code, stderr)
	}
	if stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestDirectInvocationExplainsLauncherMenuEntry(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Main(nil, bytes.NewBufferString("\n"), stdout, stderr); code != 2 {
		t.Fatalf("direct invocation code=%d, want 2", code)
	}
	for _, want := range []string{"内部更新组件", "llamalc.exe", "[3] 升级维护 -> [2] 更新启动器"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("direct invocation output missing %q: %s", want, stdout)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
}

func TestApplyUpdateUsesFixedTargets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	launcherTarget := filepath.Join(bin, "llamalc")
	updaterTarget := filepath.Join(bin, "llamaup")
	launcherSourceName := ".llamalc-new-0123456789abcdef"
	updaterSourceName := ".llamaup-new-0123456789abcdef"
	for path, content := range map[string]string{
		launcherTarget:                         "old launcher",
		updaterTarget:                          "old updater",
		filepath.Join(bin, launcherSourceName): "new launcher",
		filepath.Join(bin, updaterSourceName):  "new updater",
	} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyUpdate(root, "linux", launcherSourceName, updaterSourceName); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{launcherTarget: "new launcher", updaterTarget: "new updater"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("target %s=%q err=%v", path, content, err)
		}
	}
}

func TestApplyUpdateRestoresUpdaterWhenLauncherReplacementFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	launcherTarget := filepath.Join(bin, "llamalc")
	updaterTarget := filepath.Join(bin, "llamaup")
	launcherSourceName := ".llamalc-new-0123456789abcdef"
	updaterSourceName := ".llamaup-new-0123456789abcdef"
	launcherSource := filepath.Join(bin, launcherSourceName)
	updaterSource := filepath.Join(bin, updaterSourceName)
	for path, content := range map[string]string{
		launcherTarget: "old launcher",
		updaterTarget:  "old updater",
		launcherSource: "new launcher",
		updaterSource:  "new updater",
	} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	original := updateReplaceFile
	t.Cleanup(func() { updateReplaceFile = original })
	updateReplaceFile = func(source, destination string) error {
		if source == launcherSource {
			return errors.New("simulated launcher replacement failure")
		}
		_ = os.Remove(destination)
		return os.Rename(source, destination)
	}

	err := applyUpdate(root, "linux", launcherSourceName, updaterSourceName)
	if err == nil || !strings.Contains(err.Error(), "已恢复原更新器") {
		t.Fatalf("apply update error=%v", err)
	}
	for path, want := range map[string]string{launcherTarget: "old launcher", updaterTarget: "old updater"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != want {
			t.Fatalf("target %s=%q err=%v, want %q", path, content, readErr, want)
		}
	}
	backups, err := filepath.Glob(filepath.Join(bin, ".*-rollback-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("successful rollback left backups: %v", backups)
	}
}

func TestRejectsArbitrarySourceName(t *testing.T) {
	for _, name := range []string{"launcher.exe", "../.llamalc-new-0123456789abcdef.exe", ".llamalc-new-x.exe", ".llamalc-new-0123456789ABCDEG.exe"} {
		if err := validateStagedName(name, stagedLauncherPrefix, "启动器", "windows"); err == nil {
			t.Fatalf("accepted unsafe source name %q", name)
		}
	}
	if err := validateStagedName(".llamalc-new-0123456789abcdef.exe", stagedUpdaterPrefix, "更新器", "windows"); err == nil {
		t.Fatal("accepted launcher staging name as updater staging name")
	}
}
