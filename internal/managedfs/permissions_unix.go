//go:build !windows

package managedfs

import "os"

func protectPath(path string, permission os.FileMode) error { return os.Chmod(path, permission) }
