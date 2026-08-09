package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
)

func testLayout(t *testing.T) layout.Layout {
	t.Helper()
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, err := layout.New(root, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return l
}
func TestEnsureDefaultAndPermissions(t *testing.T) {
	l := testLayout(t)
	cfg, created, err := Ensure(l)
	if err != nil {
		t.Fatal(err)
	}
	if !created || cfg.Schema != 1 || cfg.API.Port != 29856 {
		t.Fatalf("cfg=%+v created=%v", cfg, created)
	}
	info, err := os.Stat(l.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
func TestStrictSchemaAndUnknownFields(t *testing.T) {
	base := string(mustJSON(t, Default()))
	for _, tc := range []string{strings.Replace(base, `"schema": 1`, `"schema": 2`, 1), strings.Replace(base, `"schema": 1`, `"schema": 1, "unknown": true`, 1), strings.Replace(base, `"schema": 1`, "", 1)} {
		if _, err := Parse([]byte(tc)); err == nil {
			t.Fatalf("accepted %s", tc)
		}
	}
}
func mustJSON(t *testing.T, c Config) []byte {
	t.Helper()
	l := testLayout(t)
	if err := Save(l, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
