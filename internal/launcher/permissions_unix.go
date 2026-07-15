//go:build !windows

package launcher

import (
	"os"
)

func applyFilePermissions(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}
