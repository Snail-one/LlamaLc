//go:build !windows

package updater

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func startUpdatedLauncher(root, version string, stdout, stderr io.Writer) error {
	cmd := exec.Command(filepath.Join(root, "bin", "llamalc"))
	cmd.Dir = root
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "LLAMALC_UPDATED_VERSION="+version)
	return cmd.Start()
}
