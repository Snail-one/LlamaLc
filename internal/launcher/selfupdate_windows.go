//go:build windows

package launcher

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func installLauncherBinary(source, target, version string, out io.Writer) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".llama-launcher-new-*.exe")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(temporaryPath)
	if err := copyAndSyncExecutable(source, temporaryPath); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	if err := replaceFile(temporaryPath, target); err != nil {
		return fmt.Errorf("无法替换启动器；请确认 llama-launcher 已退出: %w", err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	fmt.Fprintf(out, "启动器已更新到 %s；请重新运行命令。\n", version)
	return nil
}
