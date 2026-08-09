//go:build linux

package cli

import "os/exec"

func openCleanupPath(path string) error {
	return exec.Command("xdg-open", path).Start()
}
