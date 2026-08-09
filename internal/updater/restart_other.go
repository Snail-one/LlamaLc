//go:build !windows

package updater

import (
	"errors"
	"io"
)

func startUpdatedLauncher(_ string, _, _ io.Writer) error {
	return errors.New("自动启动新版 launcher 仅用于 Windows")
}
