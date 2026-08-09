//go:build windows

package updater

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func startUpdatedLauncher(root string, stdout, stderr io.Writer) error {
	launcher := filepath.Join(root, "bin", "llama-launcher.exe")
	command := exec.Command(launcher)
	command.Dir = root
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return err
	}
	_ = command.Process.Release()
	return nil
}
