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

func (manager *UpdateManager) installLauncherBinary(ctx context.Context, source, target string, release GitHubRelease) error {
	tool, err := updaterToolEnsurer(ctx, manager, release)
	if err != nil {
		return fmt.Errorf("无法准备独立更新器: %w", err)
	}
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
	handoffStarted := false
	defer func() {
		if !handoffStarted {
			_ = os.Remove(temporaryPath)
		}
	}()
	command := exec.Command(tool,
		"apply-launcher",
		"--source-name", filepath.Base(temporaryPath),
		"--release-version", release.TagName,
		"--wait-parent-pid", strconv.Itoa(os.Getpid()),
	)
	command.Dir, command.Stdout, command.Stderr = manager.Root, manager.Stdout, manager.Stderr
	if err := startUpdaterHidden(command); err != nil {
		return err
	}
	handoffStarted = true
	return errUpdaterHandoff
}
