//go:build windows

package updater

import (
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceFile(source, destination string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileEx.Call(
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(destinationPtr)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return callErr
	}
	return nil
}

func syncDirectory(_ string) error { return nil }

func waitForParent(pid int) error {
	const (
		processSynchronize = 0x00100000
		waitInfinite       = 0xffffffff
	)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	handle, _, callErr := kernel32.NewProc("OpenProcess").Call(processSynchronize, 0, uintptr(uint32(pid)))
	if handle == 0 {
		return callErr
	}
	defer kernel32.NewProc("CloseHandle").Call(handle)
	result, _, callErr := kernel32.NewProc("WaitForSingleObject").Call(handle, waitInfinite)
	if result == waitInfinite {
		return callErr
	}
	return nil
}
