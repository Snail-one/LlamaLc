//go:build !windows

package updater

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func startUpdatedLauncher(root, version string, stdout, stderr io.Writer) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command(filepath.Join(root, "bin", "llamalc"))
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = root, stdout, stderr, os.Stdin
	cmd.ExtraFiles = []*os.File{writer}
	cmd.Env = append(os.Environ(), "LLAMALC_UPDATED_VERSION="+version, "LLAMALC_UPDATE_READY_FD=3")
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = writer.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, 256))
		if err == nil && string(data) != "Version: "+version+"\n" {
			err = fmt.Errorf("新版 launcher 未报告匹配的就绪版本")
		}
		ready <- err
	}()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	stop := func(err error) error {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return &launcherStillRunningError{fmt.Errorf("%w；无法停止未就绪的新版进程: %v", err, killErr)}
		}
		<-exited
		return err
	}
	select {
	case err := <-exited:
		// A noninteractive menu may report readiness and exit on EOF. It has
		// still completed initialization; prefer the actual readiness message.
		select {
		case readyErr := <-ready:
			if readyErr == nil {
				return nil
			}
		case <-time.After(100 * time.Millisecond):
		}
		return fmt.Errorf("新版 launcher 在报告就绪前退出: %v", err)
	case err := <-ready:
		if err != nil {
			return stop(err)
		}
		return nil
	case <-timer.C:
		return stop(fmt.Errorf("新版 launcher 在 60 秒内未报告主菜单就绪"))
	}
}

func startRestoredLauncher(root string, stdout, stderr io.Writer) error {
	cmd := exec.Command(filepath.Join(root, "bin", "llamalc"))
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = root, os.Stdin, stdout, stderr
	cmd.Env = restoredEnvironment()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
