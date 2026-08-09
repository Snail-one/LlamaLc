//go:build windows

package launcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

func (manager *UpdateManager) installLauncherBinaries(_ context.Context, launcherSource, updaterSource, launcherTarget, updaterTarget string, release GitHubRelease) error {
	bin := filepath.Dir(launcherTarget)
	runningUpdater, err := stageUpdateExecutable(updaterTarget, bin, runningUpdaterTempPrefix+"*.exe")
	if err != nil {
		return err
	}
	newUpdater, err := stageUpdateExecutable(updaterSource, bin, stagedUpdaterTempPrefix+"*.exe")
	if err != nil {
		_ = os.Remove(runningUpdater)
		return err
	}
	newLauncher, err := stageUpdateExecutable(launcherSource, bin, ".llama-launcher-new-*.exe")
	if err != nil {
		_ = os.Remove(runningUpdater)
		_ = os.Remove(newUpdater)
		return err
	}

	handoffStarted := false
	defer func() {
		if !handoffStarted {
			_ = os.Remove(runningUpdater)
			_ = os.Remove(newUpdater)
			_ = os.Remove(newLauncher)
		}
	}()
	command := exec.Command(runningUpdater,
		"apply-update",
		"--launcher-source-name", filepath.Base(newLauncher),
		"--updater-source-name", filepath.Base(newUpdater),
		"--release-version", release.TagName,
		"--wait-parent-pid", strconv.Itoa(os.Getpid()),
	)
	command.Dir, command.Stdout, command.Stderr = manager.Root, manager.Stdout, manager.Stderr
	if err := startUpdaterHidden(command); err != nil {
		return err
	}
	handoffStarted = true
	fmt.Fprintf(manager.Stdout, `
更新文件准备完成
  目标版本: %s
  启动器: 已下载并验证
  更新器: 已下载并验证
  下一步: 退出当前程序后自动完成替换
`, release.TagName)
	return errUpdaterHandoff
}

func stageUpdateExecutable(source, directory, pattern string) (string, error) {
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
		_ = os.Remove(temporaryPath)
		return "", err
	}
	return temporaryPath, nil
}
