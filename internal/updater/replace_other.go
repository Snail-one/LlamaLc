//go:build !windows

package updater

import (
	"os"
	"syscall"
	"time"
)

func replaceFile(source, destination string) error { return os.Rename(source, destination) }

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func waitForParent(pid int) error {
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err != nil && err != syscall.EPERM {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
