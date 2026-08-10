//go:build !windows

package managedfs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAtomicWriteAndSymlinkRejection(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "state", "value.json")
	if err := AtomicWrite(root, target, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(root, target, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "two" {
		t.Fatalf("value=%q", b)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(root, filepath.Join(root, "link", "bad"), []byte("x"), 0o600); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestAtomicWriteReportsPublishedSyncFailure(t *testing.T) {
	originalSync := atomicWriteSyncDir
	t.Cleanup(func() { atomicWriteSyncDir = originalSync })
	atomicWriteSyncDir = func(string) error { return errors.New("simulated sync failure") }
	root := t.TempDir()
	target := filepath.Join(root, "state", "value.json")
	err := AtomicWrite(root, target, []byte("published"), 0o600)
	if !IsPublishedError(err) {
		t.Fatalf("error=%v, want PublishedError", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "published" {
		t.Fatalf("published data=%q err=%v", data, readErr)
	}
}

func TestAtomicCreateNeverOverwritesConcurrentWinner(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "config", "value")
	values := [][]byte{[]byte("first"), []byte("second")}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, len(values))
	for _, value := range values {
		value := value
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen <- AtomicCreate(root, target, value, 0o600)
		}()
	}
	wait.Wait()
	close(errorsSeen)
	successes, exists := 0, 0
	for err := range errorsSeen {
		if err == nil {
			successes++
		} else if errors.Is(err, os.ErrExist) {
			exists++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || exists != 1 {
		t.Fatalf("successes=%d exists=%d", successes, exists)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" && string(data) != "second" {
		t.Fatalf("unexpected winner %q", data)
	}
}

func TestAtomicCreateFallbackNeverOverwritesWinner(t *testing.T) {
	originalLink := atomicCreateLink
	t.Cleanup(func() { atomicCreateLink = originalLink })
	atomicCreateLink = func(string, string) error {
		return errors.New("hard links unsupported")
	}

	root := t.TempDir()
	target := filepath.Join(root, "config", "value")
	if err := AtomicCreate(root, target, []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicCreate(root, target, []byte("loser"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second create error=%v, want os.ErrExist", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "winner" {
		t.Fatalf("winner overwritten: %q", data)
	}
	lock := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".create-lock")
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback lock was not released: %v", err)
	}
}

func TestAtomicCreateFallbackReclaimsOldEmptyLock(t *testing.T) {
	root := t.TempDir()
	lock := filepath.Join(root, ".value.create-lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-atomicCreateFallbackStaleAge - time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := waitForAtomicCreateFallbackUntil(lock, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale lock still exists: %v", err)
	}
}

func TestEnsureDirPreservesExistingPermissions(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "models", "llm")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(root, target, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("EnsureDir changed existing directory permissions to %o", got)
	}
}
