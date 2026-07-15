//go:build !windows

package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (manager *UpdateManager) installLauncherBinary(_ context.Context, source, target string, release GitHubRelease) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".llama-launcher-new-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(temporaryPath)
	defer os.Remove(temporaryPath)
	if err := copyAndSyncExecutable(source, temporaryPath); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, target); err != nil {
		return fmt.Errorf("无法原子替换启动器: %w", err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	fmt.Fprintf(manager.Stdout, "启动器已更新到 %s；请重新运行命令。\n", release.TagName)
	return nil
}
