//go:build windows

package launcher

import (
	"fmt"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func ensureFreeSpace(path string, required int64) error {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var available uint64
	result, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&available)),
		0,
		0,
	)
	if result == 0 {
		return fmt.Errorf("无法检查可用磁盘空间: %w", callErr)
	}
	if available < uint64(required) {
		return fmt.Errorf("磁盘空间不足：需要约 %d 字节，可用 %d 字节", required, available)
	}
	return nil
}
