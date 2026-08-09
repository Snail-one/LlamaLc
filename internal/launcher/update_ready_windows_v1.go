//go:build windows

package launcher

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

func signalUpdateReady() error {
	name := strings.TrimSpace(os.Getenv("LLAMALC_UPDATE_READY_EVENT"))
	_ = os.Unsetenv("LLAMALC_UPDATE_READY_EVENT")
	if name == "" {
		return nil
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	handle, _, callErr := kernel.NewProc("OpenEventW").Call(0x0002, 0, uintptr(unsafe.Pointer(p)))
	if handle == 0 {
		return callErr
	}
	defer kernel.NewProc("CloseHandle").Call(handle)
	result, _, callErr := kernel.NewProc("SetEvent").Call(handle)
	if result == 0 {
		return callErr
	}
	return nil
}
