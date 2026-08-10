package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Snail-one/LlamaLc/internal/layout"
)

func TestCleanupIgnoresLegacyAndStrictlyRecognizesUpdaterRunner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	legacy := filepath.Join(root, "data", "llama.cpp")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep.gguf"), []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(l.Bin, ".llamaup-run-0123456789abcdef")
	if err := os.WriteFile(runner, []byte("runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	items, err := CleanupCandidates(l)
	if err != nil {
		t.Fatal(err)
	}
	var runnerItem *CleanupCandidate
	for i := range items {
		switch items[i].Path {
		case runner:
			runnerItem = &items[i]
		}
	}
	for _, item := range items {
		if item.Path == legacy {
			t.Fatalf("legacy path was inspected: %+v", item)
		}
	}
	if runnerItem == nil || !runnerItem.Automatic || runnerItem.Recent {
		t.Fatalf("runner=%+v", runnerItem)
	}
	invalid := filepath.Join(l.Bin, ".llamaup-run-not-a-token")
	if err := os.WriteFile(invalid, []byte("runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	items, _ = CleanupCandidates(l)
	for _, item := range items {
		if item.Path == invalid {
			t.Fatal("accepted invalid runner name")
		}
	}
}

func TestCleanupDamagedStateStillListsRuntimeWithWarning(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	runtimeDirectory := filepath.Join(l.LlamaRuntimeDir, "cpu", "b123")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.UpdateStateFile, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := CleanupCandidates(l)
	if err != nil {
		t.Fatal(err)
	}
	foundRuntime, foundWarning := false, false
	for _, item := range items {
		if item.Path == runtimeDirectory && strings.Contains(item.Reason, "扫描警告") {
			foundRuntime = true
		}
		if item.Warning && strings.Contains(item.Reason, "更新状态损坏") {
			foundWarning = true
		}
	}
	if !foundRuntime || !foundWarning {
		t.Fatalf("items=%+v", items)
	}
}

func TestCleanupRequiresValidOwnershipMarkerForTempDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	path := filepath.Join(l.LlamaRuntimeDir, ".staging-0123456789abcdef")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	items, _ := CleanupCandidates(l)
	for _, item := range items {
		if item.Path == path && item.Automatic {
			t.Fatal("unmarked directory accepted")
		}
	}
	if err := createOwnedTempDirectory(l, path+"2", "0123456789abcdef", "llama-runtime-staging"); err == nil {
		t.Fatal("accepted path whose name does not match token")
	}
}

func TestCleanupCandidatesDoesNotConsumePendingCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	active := filepath.Join(l.LlamaRuntimeDir, "cpu", "b124")
	pending := filepath.Join(l.LlamaRuntimeDir, "cpu", "b123")
	for _, directory := range []string{active, pending, l.StateDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pending, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := State{Schema: StateSchema, LlamaTag: "b124", Backend: "cpu", ActiveRuntime: runtimeRelative("cpu", "b124"), Assets: []InstalledAsset{{Name: "runtime.tar.gz", SHA256: strings.Repeat("a", 64)}}, PendingCleanup: []string{runtimeRelative("cpu", "b123")}}
	if err := SaveState(l, state); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupCandidates(l); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pending, "keep")); err != nil {
		t.Fatalf("scan deleted pending runtime: %v", err)
	}
	loaded, _, err := LoadState(l)
	if err != nil || len(loaded.PendingCleanup) != 1 {
		t.Fatalf("pending changed: %v err=%v", loaded.PendingCleanup, err)
	}
}

func TestStartupHousekeepingRemovesOnlyOldOwnedStaging(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(l.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(l.RuntimeDir, ".launcher-update-0123456789abcdef")
	if err := createOwnedTempDirectory(l, owned, "0123456789abcdef", "launcher-update-staging"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owned, "payload"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmarked := filepath.Join(l.RuntimeDir, ".launcher-update-fedcba9876543210")
	if err := os.MkdirAll(unmarked, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	for _, path := range []string{filepath.Join(owned, ownershipMarkerName), filepath.Join(owned, "payload"), owned, unmarked} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	result := StartupHousekeeping(l)
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings=%v", result.Warnings)
	}
	if _, err := os.Lstat(owned); !os.IsNotExist(err) {
		t.Fatalf("old owned staging still exists: %v", err)
	}
	if _, err := os.Stat(unmarked); err != nil {
		t.Fatalf("unmarked staging was removed: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != owned {
		t.Fatalf("removed=%v", result.Removed)
	}
}

func TestAutomaticCleanupRejectsPathReplacementAfterSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	path := filepath.Join(l.Bin, ".llamaup-run-0123456789abcdef")
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := inspectCleanupPath(path)
	if err != nil {
		t.Fatal(err)
	}
	originalElsewhere := filepath.Join(l.Bin, "original-elsewhere")
	if err := os.Rename(path, originalElsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeAutomaticCleanupPath(l, path, info, snapshot); err == nil || !strings.Contains(err.Error(), "身份") {
		t.Fatalf("replacement was not rejected: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement was not restored: data=%q err=%v", data, err)
	}
	data, err = os.ReadFile(originalElsewhere)
	if err != nil || string(data) != "original" {
		t.Fatalf("original changed: data=%q err=%v", data, err)
	}
}

func TestStartupHousekeepingImmediatelyRemovesDeadLockButKeepsLiveLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(l.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead, err := acquireLlamaInstallLock(l)
	if err != nil {
		t.Fatal(err)
	}
	marker, valid := readOwnershipMarker(dead, ".llama-lock-", "llama-global-install-lock")
	if !valid {
		t.Fatal("dead lock marker invalid")
	}
	owner := lockOwner{Schema: 1, Token: marker.Token, Kind: marker.Kind, PID: 2_147_483_647, Identity: "missing"}
	data, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(dead, lockOwnerName), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	result := StartupHousekeeping(l)
	if len(result.Warnings) != 0 || len(result.Removed) != 1 || result.Removed[0] != dead {
		t.Fatalf("dead lock cleanup=%+v", result)
	}
	if _, err := os.Lstat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead lock still exists: %v", err)
	}

	live, err := acquireLlamaInstallLock(l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(live) })
	old := time.Now().Add(-25 * time.Hour)
	for _, path := range []string{filepath.Join(live, ownershipMarkerName), filepath.Join(live, lockOwnerName), live} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	result = StartupHousekeeping(l)
	if len(result.Removed) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("live lock was touched: %+v", result)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live lock missing: %v", err)
	}
}
