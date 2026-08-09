//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

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
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return fmt.Errorf("检查磁盘空间: %w", err)
	}
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	if available < uint64(required) {
		return fmt.Errorf("磁盘空间不足: 至少需要 %d 字节，可用 %d 字节", required, available)
	}
	return nil
}
