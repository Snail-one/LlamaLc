package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func migrateLegacyUpdater(root, goos string, stdout io.Writer) error {
	bin := filepath.Join(root, "bin")
	if err := validateManagedPath(root, bin, "启动器目录", false, true); err != nil {
		return err
	}
	legacy := filepath.Join(bin, updaterExecutableName(releaseUpdaterBaseName, goos))
	current := filepath.Join(bin, updaterExecutableName(installedUpdaterBaseName, goos))

	if _, err := os.Lstat(current); err == nil {
		return validateManagedPath(root, current, "当前 llamaup", false, false)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("无法检查当前 llamaup %s: %w", current, err)
	}
	if _, err := os.Lstat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("无法检查旧更新器 %s: %w", legacy, err)
	}
	if err := validateManagedPath(root, legacy, "旧正式更新器", false, false); err != nil {
		return err
	}
	if err := copyAndSyncExecutable(legacy, current); err != nil {
		return fmt.Errorf("复制旧正式更新器到 llamaup: %w", err)
	}
	if err := os.Remove(legacy); err != nil {
		return fmt.Errorf("llamaup 已创建，但无法删除旧正式更新器 %s: %w", legacy, err)
	}
	if err := syncDirectory(bin); err != nil {
		return err
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "已将正式更新器迁移为: %s\n", current)
	}
	return nil
}
