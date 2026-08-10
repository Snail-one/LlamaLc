//go:build windows

package procinfo

import (
	"errors"
	"fmt"
	"syscall"
)

func platformIdentity(pid int) (string, bool, error) {
	const (
		processSynchronize             = 0x00100000
		processQueryLimitedInformation = 0x00001000
		waitObject0                    = 0x00000000
		waitTimeout                    = 0x00000102
	)
	handle, err := syscall.OpenProcess(processSynchronize|processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.Errno(87)) { // ERROR_INVALID_PARAMETER: PID is absent.
			return "", false, nil
		}
		return "", false, err
	}
	defer syscall.CloseHandle(handle)
	status, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", false, err
	}
	if status == waitObject0 {
		return "", false, nil
	}
	if status != waitTimeout {
		return "", false, fmt.Errorf("未知进程等待状态: 0x%x", status)
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%08x%08x", created.HighDateTime, created.LowDateTime), true, nil
}
