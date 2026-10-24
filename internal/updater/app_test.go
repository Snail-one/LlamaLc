package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/procinfo"
)

func TestVersionDoesNotRequireDeploymentLayout(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Main([]string{"--version"}, nil, stdout, stderr); code != 0 {
		t.Fatalf("version code=%d stderr=%s", code, stderr)
	}
	if stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestHelpDescribesIndependentUpdater(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Main([]string{"--help"}, nil, stdout, stderr); code != 0 {
		t.Fatalf("help code=%d", code)
	}
	for _, want := range []string{"llamaup [update]", "llamaup check", "llamaup recover"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q: %s", want, stdout)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%s", stderr)
	}
}

func TestUpdaterValidatesAndClaimsLauncherLockOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	token := "0123456789abcdef"
	directory := filepath.Join(root, "runtime", ".launcher-lock-"+token)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := struct {
		Schema int    `json:"schema"`
		Token  string `json:"token"`
		Kind   string `json:"kind"`
	}{Schema: 1, Token: token, Kind: "launcher-install-lock"}
	markerData, _ := json.Marshal(marker)
	if err := os.WriteFile(filepath.Join(directory, ".llamalc-owned.json"), append(markerData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, alive, err := procinfo.Identity(os.Getpid())
	if err != nil || !alive {
		t.Fatalf("current identity=%q alive=%v err=%v", identity, alive, err)
	}
	owner := updateLockOwner{Schema: 1, Token: token, Kind: "launcher-install-lock", PID: os.Getpid(), Identity: identity}
	ownerData, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(directory, lockOwnerName), append(ownerData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := validateUpdateLock(root, token, os.Getpid()); err != nil || got != directory {
		t.Fatalf("validate directory=%q err=%v", got, err)
	}
	if err := claimUpdateLock(root, directory, token); err != nil {
		t.Fatal(err)
	}
	claimed, exists, err := readUpdateLockOwner(directory)
	if err != nil || !exists || claimed.PID != os.Getpid() || claimed.Identity != identity {
		t.Fatalf("claimed=%+v exists=%v err=%v", claimed, exists, err)
	}

	owner.PID = 2_147_483_647
	ownerData, _ = json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(directory, lockOwnerName), append(ownerData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateUpdateLock(root, token, os.Getpid()); err == nil || !strings.Contains(err.Error(), "交接不匹配") {
		t.Fatalf("foreign owner accepted: %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "已恢复原启动器和原更新器") {
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

func TestUpdaterIdentityCheckRejectsPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llamaup")
	if err := os.WriteFile(path, []byte("original"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := stableUpdaterFileInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyUpdaterFileIdentity(path, identity); err == nil {
		t.Fatal("replacement was accepted as the staged updater")
	}
}

func TestPublicSubcommandHelpDoesNotRequireDeployment(t *testing.T) {
	for _, command := range []string{"update", "check", "recover"} {
		var stdout, stderr bytes.Buffer
		if code := Main([]string{command, "--help"}, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("%s --help code=%d stderr=%s", command, code, &stderr)
		}
	}
}
