package launcher

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DownloadLogName = "download.log"

var downloadLogMu sync.Mutex

func appendDownloadLog(root, event string) error {
	if root == "" {
		return nil
	}
	downloadLogMu.Lock()
	defer downloadLogMu.Unlock()

	directory := filepath.Join(root, ConfigDirectoryName)
	if err := validateManagedPath(root, directory, "下载日志目录", true, true); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("无法创建下载日志目录: %w", err)
	}
	if err := validateManagedPath(root, directory, "下载日志目录", false, true); err != nil {
		return err
	}
	if err := applyFilePermissions(directory, 0o700); err != nil {
		return fmt.Errorf("无法保护下载日志目录: %w", err)
	}

	path := filepath.Join(directory, DownloadLogName)
	if err := validateManagedPath(root, path, "下载日志", true, false); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("无法打开下载日志: %w", err)
	}
	if err := applyFilePermissions(path, 0o600); err != nil {
		file.Close()
		return fmt.Errorf("无法保护下载日志: %w", err)
	}
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339), safeTerminalText(event))
	if _, err := io.WriteString(file, line); err != nil {
		file.Close()
		return fmt.Errorf("无法写入下载日志: %w", err)
	}
	return file.Close()
}
