//go:build !windows

package launcher

import (
	"fmt"
	"syscall"
)

func ensureFreeSpace(path string, required int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("无法检查可用磁盘空间: %w", err)
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if available < required {
		return fmt.Errorf("磁盘空间不足：需要约 %d 字节，可用 %d 字节", required, available)
	}
	return nil
}
