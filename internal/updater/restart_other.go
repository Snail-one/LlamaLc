//go:build !windows

package updater

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func startUpdatedLauncher(root, version string, stdout, stderr io.Writer) error {
	cmd := exec.Command(filepath.Join(root, "bin", "llamalc"))
	cmd.Dir = root
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "LLAMALC_UPDATED_VERSION="+version)
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			return fmt.Errorf("新版 launcher 在报告启动成功前退出")
		}
		return fmt.Errorf("新版 launcher 启动后立即失败: %w", err)
	case <-time.After(time.Second):
		return nil
	}
}
