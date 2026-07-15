package updater

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionDoesNotRequireDeploymentLayout(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Main([]string{"--version"}, stdout, stderr); code != 0 {
		t.Fatalf("version code=%d stderr=%s", code, stderr)
	}
	if stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestApplyUpdateUsesFixedTargets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	launcherTarget := filepath.Join(bin, "llama-launcher")
	updaterTarget := filepath.Join(bin, "llamaup")
	launcherSourceName := ".llama-launcher-new-test"
	updaterSourceName := ".llamaup-new-test"
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

func TestRejectsArbitrarySourceName(t *testing.T) {
	for _, name := range []string{"launcher.exe", "../.llama-launcher-new-x.exe", ".llama-launcher-new-x"} {
		if err := validateStagedName(name, stagedLauncherPrefix, "启动器", "windows"); err == nil {
			t.Fatalf("accepted unsafe source name %q", name)
		}
	}
	if err := validateStagedName(".llama-launcher-new-x.exe", stagedUpdaterPrefix, "更新器", "windows"); err == nil {
		t.Fatal("accepted launcher staging name as updater staging name")
	}
	if err := validateStagedName(".llama-updater-new-x.exe", stagedUpdaterPrefix, "更新器", "windows"); err == nil {
		t.Fatal("accepted legacy updater staging name")
	}
}
