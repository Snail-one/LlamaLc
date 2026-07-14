//go:build !windows

package launcher

import (
	"fmt"
	"os"
)

func clearTerminalFile(file *os.File) error {
	_, err := fmt.Fprint(file, "\x1b[2J\x1b[H")
	return err
}
