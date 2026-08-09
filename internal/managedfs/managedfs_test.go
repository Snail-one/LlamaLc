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
