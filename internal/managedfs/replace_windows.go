//go:build windows

package managedfs

import (
	"errors"
	"os"
)

func replaceFile(source, destination string) error {
	backup := destination + ".replace-backup"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
func syncDir(string) error { return nil }
