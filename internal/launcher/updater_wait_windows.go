//go:build windows

package launcher

import (
	"fmt"
	"syscall"
)

const (
	processSynchronize = 0x00100000
	waitInfinite       = 0xffffffff
)

var (
	kernel32UpdaterWait = syscall.NewLazyDLL("kernel32.dll")
	openUpdaterProcess  = kernel32UpdaterWait.NewProc("OpenProcess")
	waitUpdaterProcess  = kernel32UpdaterWait.NewProc("WaitForSingleObject")
	closeUpdaterHandle  = kernel32UpdaterWait.NewProc("CloseHandle")
)

func waitForUpdaterParent(pid int) error {
	handle, _, callErr := openUpdaterProcess.Call(processSynchronize, 0, uintptr(uint32(pid)))
	if handle == 0 {
		return fmt.Errorf("OpenProcess(%d): %v", pid, callErr)
	}
	defer closeUpdaterHandle.Call(handle)
	result, _, callErr := waitUpdaterProcess.Call(handle, waitInfinite)
	if result == 0xffffffff {
		return fmt.Errorf("WaitForSingleObject(%d): %v", pid, callErr)
	}
	return nil
}
