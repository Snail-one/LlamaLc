//go:build windows

package cli

import "os/exec"

func openCleanupPath(path string) error {
	return exec.Command("explorer.exe", path).Start()
}
