//go:build !windows

package launcher

import (
	"errors"
	"fmt"
	"os"

	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

func signalUpdateReady() error {
	fd := os.Getenv("LLAMALC_UPDATE_READY_FD")
	_ = os.Unsetenv("LLAMALC_UPDATE_READY_FD")
	if fd == "" {
		return nil
	}
	if fd != "3" {
		return errors.New("更新就绪文件描述符无效")
	}
	pipe := os.NewFile(3, "update-ready")
	defer pipe.Close()
	_, err := fmt.Fprintln(pipe, "Version: "+buildversion.Version)
	return err
}
