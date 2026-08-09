//go:build !windows

package managedfs

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
