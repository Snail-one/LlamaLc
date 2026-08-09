//go:build !windows

package managedfs

import (
	"os"
	"path/filepath"
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
