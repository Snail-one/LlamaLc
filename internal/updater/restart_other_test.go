//go:build !windows

package updater

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxRestartDetectsImmediateLauncherFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "bin", "llamalc")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := startUpdatedLauncher(root, "v1.2.3", io.Discard, io.Discard); err == nil {
		t.Fatal("immediate failure reported as success")
	}
}
