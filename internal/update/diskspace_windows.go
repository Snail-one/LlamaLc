//go:build windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func ensureFreeSpace(path string, required int64) error {
	if required <= 0 {
		return nil
	}
	for {
		if _, err := os.Stat(path); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("检查磁盘空间路径: %w", err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return fmt.Errorf("检查磁盘空间路径不存在: %s", path)
		}
		path = parent
	}
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var available uint64
	result, _, callErr := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(&available)), 0, 0)
	if result == 0 {
		return fmt.Errorf("检查磁盘空间: %w", callErr)
	}
	if available < uint64(required) {
		return fmt.Errorf("磁盘空间不足: 至少需要 %d 字节，可用 %d 字节", required, available)
	}
	return nil
}
