// Package procinfo identifies a live process without relying on its PID alone.
package procinfo

import "errors"

var ErrUnsupported = errors.New("当前平台无法查询进程身份")

// Identity returns a stable creation identity for pid, whether that process is
// currently alive, and any error that made the status indeterminate.
func Identity(pid int) (identity string, alive bool, err error) {
	if pid <= 0 {
		return "", false, nil
	}
	return platformIdentity(pid)
}
