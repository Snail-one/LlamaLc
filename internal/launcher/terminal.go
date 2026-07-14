package launcher

import (
	"io"
	"os"
)

// clearTerminal refreshes interactive console output without adding control
// sequences to redirected output, logs, or tests.
func clearTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return clearTerminalFile(file) == nil
}
