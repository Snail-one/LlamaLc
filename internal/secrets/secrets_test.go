package secrets

import (
	"github.com/Snail-one/LlamaLc/internal/layout"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureResetAndRejectWhitespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	key, created, err := Ensure(l)
	if err != nil || !created || len(key) != 128 {
		t.Fatalf("key=%q created=%v err=%v", key, created, err)
	}
	next, err := Reset(l)
	if err != nil || next == key || len(next) != 128 {
		t.Fatalf("next=%q err=%v", next, err)
	}
	if err = os.WriteFile(l.APIKeyFile, []byte("abcdefghijklmnopqrstuvwxyzABCDEF bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(l); err == nil {
		t.Fatal("accepted whitespace")
	}
}

func TestReadAcceptsMaximumLengthKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(filepath.Dir(l.APIKeyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("A", 8167)
	if err := os.WriteFile(l.APIKeyFile, []byte(want+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(l)
	if err != nil || got != want {
		t.Fatalf("length=%d err=%v", len(got), err)
	}
}
