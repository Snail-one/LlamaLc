//go:build !windows

package updater

import (
	"errors"
	"os"
)

func replaceFile(source, destination string) error { return os.Rename(source, destination) }

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func waitForParent(_ int) error {
	return errors.New("独立更新器的 launcher 替换操作仅用于 Windows")
}
