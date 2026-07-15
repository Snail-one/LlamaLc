//go:build windows

package launcher

import (
	"os/exec"
	"syscall"
)

func startUpdaterHidden(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	return command.Start()
}
