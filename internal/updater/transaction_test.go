package updater

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type installFixture struct {
	root, bin, launcher, updater, launcherName, updaterName string
}

func makeInstallFixture(t *testing.T) installFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "LlamaLc")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	f := installFixture{root: root, bin: bin, launcher: filepath.Join(bin, "llamalc"+ext), updater: filepath.Join(bin, "llamaup"+ext), launcherName: ".llamalc-new-0123456789abcdef" + ext, updaterName: ".llamaup-new-0123456789abcdef" + ext}
	for path, data := range map[string]string{f.launcher: "old launcher", f.updater: "old updater", filepath.Join(bin, f.launcherName): "new launcher", filepath.Join(bin, f.updaterName): "new updater"} {
		if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func assertProgram(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s=%q err=%v want=%q", path, data, err, want)
	}
}

func TestHealthFailureRestoresBothPrograms(t *testing.T) {
	f := makeInstallFixture(t)
	err := applyUpdateWithValidation(f.root, runtime.GOOS, f.launcherName, f.updaterName, func() error {
		assertProgram(t, f.launcher, "new launcher")
		assertProgram(t, f.updater, "new updater")
		tx, err := readTransaction(f.bin, runtime.GOOS)
		if err != nil || tx.Phase != "prepared" {
			t.Fatalf("journal=%+v err=%v", tx, err)
		}
		for _, entry := range tx.Files {
			if _, err := os.Stat(filepath.Join(f.bin, entry.Backup)); err != nil {
				t.Fatal(err)
			}
		}
		return errors.New("new launcher crashed")
	})
	if err == nil || !strings.Contains(err.Error(), "已恢复原启动器和原更新器") {
		t.Fatalf("err=%v", err)
	}
	assertProgram(t, f.launcher, "old launcher")
	assertProgram(t, f.updater, "old updater")
	if transactionPending(f.root) {
		t.Fatal("successful rollback left journal")
	}
	backups, _ := filepath.Glob(filepath.Join(f.bin, ".*-rollback-*"))
	if len(backups) != 0 {
		t.Fatalf("backups=%v", backups)
	}
}

func TestHealthSuccessCommitsAndDeletesBackups(t *testing.T) {
	f := makeInstallFixture(t)
	if err := applyUpdateWithValidation(f.root, runtime.GOOS, f.launcherName, f.updaterName, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertProgram(t, f.launcher, "new launcher")
	assertProgram(t, f.updater, "new updater")
	if transactionPending(f.root) {
		t.Fatal("committed update left journal")
	}
	backups, _ := filepath.Glob(filepath.Join(f.bin, ".*-rollback-*"))
	if len(backups) != 0 {
		t.Fatalf("backups=%v", backups)
	}
}

func leaveInterruptedTransaction(t *testing.T, f installFixture) transaction {
	t.Helper()
	// A process that could not be stopped must leave both executables and all
	// recovery material untouched. This also models a crash before commit.
	err := applyUpdateWithValidation(f.root, runtime.GOOS, f.launcherName, f.updaterName, func() error { return &launcherStillRunningError{errors.New("cannot stop process")} })
	if err == nil || !transactionPending(f.root) {
		t.Fatalf("err=%v pending=%v", err, transactionPending(f.root))
	}
	tx, err := readTransaction(f.bin, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestInterruptedInstallCanRecoverAfterOneTargetWasRestored(t *testing.T) {
	f := makeInstallFixture(t)
	tx := leaveInterruptedTransaction(t, f)
	// Simulate a crash part-way through a previous rollback, including its
	// completely written but not yet renamed restoration file.
	first := tx.Files[0]
	data, err := os.ReadFile(filepath.Join(f.bin, first.Backup))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.updater, data, 0o700); err != nil {
		t.Fatal(err)
	}
	second := tx.Files[1]
	data, err = os.ReadFile(filepath.Join(f.bin, second.Backup))
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(f.bin, strings.Replace(second.Backup, "-rollback-", "-new-", 1))
	if err := os.WriteFile(temp, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(f.root, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	assertProgram(t, f.updater, "old updater")
	assertProgram(t, f.launcher, "old launcher")
	if transactionPending(f.root) {
		t.Fatal("recovery left journal")
	}
}

func TestRecoveryRejectsCorruptBackupBeforeChangingTargets(t *testing.T) {
	f := makeInstallFixture(t)
	tx := leaveInterruptedTransaction(t, f)
	if err := os.WriteFile(filepath.Join(f.bin, tx.Files[1].Backup), []byte("corrupted backup"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(f.root, runtime.GOOS); err == nil {
		t.Fatal("accepted corrupt backup")
	}
	assertProgram(t, f.updater, "new updater")
	assertProgram(t, f.launcher, "new launcher")
	if !transactionPending(f.root) {
		t.Fatal("lost recovery record")
	}
}

func TestCommittedRecoveryOnlyCleansAndNeverRollsBack(t *testing.T) {
	f := makeInstallFixture(t)
	tx := leaveInterruptedTransaction(t, f)
	tx.Phase = "committed"
	if err := writeTransaction(f.bin, tx); err != nil {
		t.Fatal(err)
	}
	// Simulate interrupted backup deletion after commit.
	if err := os.Remove(filepath.Join(f.bin, tx.Files[0].Backup)); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(f.root, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	assertProgram(t, f.updater, "new updater")
	assertProgram(t, f.launcher, "new launcher")
	if transactionPending(f.root) {
		t.Fatal("cleanup left journal")
	}
}

func TestRepairSupportsMissingLauncher(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f := makeInstallFixture(t)
			if err := os.Remove(f.launcher); err != nil {
				t.Fatal(err)
			}
			err := applyUpdateWithValidation(f.root, runtime.GOOS, f.launcherName, f.updaterName, func() error {
				if fail {
					return errors.New("unhealthy")
				}
				return nil
			})
			if fail {
				if err == nil {
					t.Fatal("accepted health failure")
				}
				if _, err := os.Lstat(f.launcher); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("launcher should remain absent: %v", err)
				}
				assertProgram(t, f.updater, "old updater")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertProgram(t, f.launcher, "new launcher")
			}
		})
	}
}

func TestRecoveryRejectsPathsOutsideFixedProgramNames(t *testing.T) {
	f := makeInstallFixture(t)
	tx := leaveInterruptedTransaction(t, f)
	tx.Files[1].Backup = "../arbitrary-file"
	if err := writeTransaction(f.bin, tx); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(f.root, runtime.GOOS); err == nil {
		t.Fatal("accepted arbitrary backup path")
	}
	assertProgram(t, f.launcher, "new launcher")
}
