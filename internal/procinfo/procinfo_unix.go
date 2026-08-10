//go:build aix || darwin || dragonfly || freebsd || netbsd || openbsd || solaris

package procinfo

import (
	"errors"
	"fmt"
	"syscall"
)

func platformIdentity(pid int) (string, bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		// These platforms do not expose a portable process creation value in
		// the standard library. PID identity still permits immediate dead-owner
		// recovery; heartbeat age remains the PID-reuse fallback.
		return fmt.Sprintf("pid:%d", pid), true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return "", false, nil
	}
	return "", false, err
}
