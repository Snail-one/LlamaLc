//go:build !windows

package managedfs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
