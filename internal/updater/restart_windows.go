//go:build windows

package updater

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const updatedVersionEnvironment = "LLAMALC_UPDATED_VERSION"

func startUpdatedLauncher(root, releaseVersion string, stdout, stderr io.Writer) error {
	launcher := filepath.Join(root, "bin", "llama-launcher.exe")
	command := exec.Command(launcher)
	command.Dir = root
	command.Env = updatedLauncherEnvironment(releaseVersion)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return err
	}
	_ = command.Process.Release()
	return nil
}

func updatedLauncherEnvironment(releaseVersion string) []string {
	prefix := strings.ToUpper(updatedVersionEnvironment) + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), prefix) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, updatedVersionEnvironment+"="+releaseVersion)
}
