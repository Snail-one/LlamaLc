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

func TestApplyLauncherUsesFixedTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(bin, "llama-launcher")
	sourceName := ".llama-launcher-new-test"
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, sourceName), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyLauncher(root, "linux", sourceName); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "new" {
		t.Fatalf("target=%q err=%v", content, err)
	}
}

func TestRejectsArbitrarySourceName(t *testing.T) {
	for _, name := range []string{"launcher.exe", "../.llama-launcher-new-x.exe", ".llama-launcher-new-x"} {
		if err := validateStagedName(name, "windows"); err == nil {
			t.Fatalf("accepted unsafe source name %q", name)
		}
	}
}
