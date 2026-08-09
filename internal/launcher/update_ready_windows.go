//go:build windows

package launcher

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	updateReadyEnvironment = "LLAMALC_UPDATE_READY_EVENT"
	updateReadyEventPrefix = `Local\LlamaLcUpdateReady-`
	eventModifyState       = 0x0002
)

var (
	kernel32UpdateReady = syscall.NewLazyDLL("kernel32.dll")
	openEventW          = kernel32UpdateReady.NewProc("OpenEventW")
	setEvent            = kernel32UpdateReady.NewProc("SetEvent")
)

func signalUpdateReady() error {
	eventName := strings.TrimSpace(os.Getenv(updateReadyEnvironment))
	_ = os.Unsetenv(updateReadyEnvironment)
	if eventName == "" {
		return nil
	}
	if !validUpdateReadyEventName(eventName) {
		return errors.New("更新就绪事件名称无效")
	}
	namePointer, err := syscall.UTF16PtrFromString(eventName)
	if err != nil {
		return err
	}
	handle, _, callErr := openEventW.Call(eventModifyState, 0, uintptr(unsafe.Pointer(namePointer)))
	if handle == 0 {
		return fmt.Errorf("OpenEventW: %w", callErr)
	}
	defer kernel32UpdateReady.NewProc("CloseHandle").Call(handle)
	result, _, callErr := setEvent.Call(handle)
	if result == 0 {
		return fmt.Errorf("SetEvent: %w", callErr)
	}
	return nil
}

func validUpdateReadyEventName(value string) bool {
	if !strings.HasPrefix(value, updateReadyEventPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(value, updateReadyEventPrefix)
	if len(suffix) != 32 {
		return false
	}
	for _, character := range suffix {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
