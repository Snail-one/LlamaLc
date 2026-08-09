package secrets

import (
	"github.com/Snail-one/LlamaLc/internal/layout"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureResetAndRejectWhitespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	key, created, err := Ensure(l)
	if err != nil || !created || len(key) < 32 {
		t.Fatalf("key=%q created=%v err=%v", key, created, err)
	}
	next, err := Reset(l)
	if err != nil || next == key {
		t.Fatalf("next=%q err=%v", next, err)
	}
	if err = os.WriteFile(l.APIKeyFile, []byte("abcdefghijklmnopqrstuvwxyzABCDEF bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(l); err == nil {
		t.Fatal("accepted whitespace")
	}
}
