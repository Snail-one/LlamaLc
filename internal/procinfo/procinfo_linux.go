//go:build linux

package procinfo

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func platformIdentity(pid int) (string, bool, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// comm (field 2) may contain spaces and parentheses, so split only after
	// its final closing parenthesis. starttime is field 22, index 19 below.
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 || closing+2 >= len(data) {
		return "", false, fmt.Errorf("无法解析 /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[closing+2:]))
	if len(fields) <= 19 || fields[19] == "" {
		return "", false, fmt.Errorf("/proc/%d/stat 缺少进程启动身份", pid)
	}
	return fields[19], true, nil
}
