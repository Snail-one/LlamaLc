//go:build !windows

package launcher

import (
	"errors"
	"os/exec"
)

func startUpdaterHidden(_ *exec.Cmd) error {
	return errors.New("当前平台不使用后台更新器交接")
}
