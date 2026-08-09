//go:build !windows

package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (manager *UpdateManager) installLauncherBinaries(_ context.Context, launcherSource, updaterSource, launcherTarget, updaterTarget string, release GitHubRelease) error {
	bin := filepath.Dir(launcherTarget)
	newUpdater, err := stageUnixExecutable(updaterSource, bin, stagedUpdaterTempPrefix)
	if err != nil {
		return err
	}
	defer os.Remove(newUpdater)
	newLauncher, err := stageUnixExecutable(launcherSource, bin, ".llama-launcher-new-")
	if err != nil {
		return err
	}
	defer os.Remove(newLauncher)
	if err := replaceFile(newUpdater, updaterTarget); err != nil {
		return fmt.Errorf("无法原子替换更新器: %w", err)
	}
	if err := replaceFile(newLauncher, launcherTarget); err != nil {
		return fmt.Errorf("无法原子替换启动器: %w", err)
	}
	if err := syncDirectory(bin); err != nil {
		return err
	}
	fmt.Fprintf(manager.Stdout, "启动器与更新器已更新到 %s；请重新运行命令。\n", release.TagName)
	// The executable on disk is new, but this process still contains the old
	// embedded version and code. Exit through the same successful restart path
	// used by Windows instead of returning to a stale interactive menu.
	return errUpdaterHandoff
}

func stageUnixExecutable(source, directory, pattern string) (string, error) {
	temporary, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	_ = os.Remove(temporaryPath)
	if err := copyAndSyncExecutable(source, temporaryPath); err != nil {
		return "", err
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	return temporaryPath, nil
}
