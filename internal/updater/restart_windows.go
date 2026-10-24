//go:build windows

package updater

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	updatedVersionEnvironment = "LLAMALC_UPDATED_VERSION"
	updateReadyEnvironment    = "LLAMALC_UPDATE_READY_EVENT"
	updateReadyEventPrefix    = `Local\LlamaLcUpdateReady-`
	createNewConsole          = 0x00000010
	updateReadyTimeoutMS      = 60_000
	waitObject0               = 0x00000000
	waitTimeout               = 0x00000102
	waitFailed                = 0xffffffff
)

var (
	kernel32Restart       = syscall.NewLazyDLL("kernel32.dll")
	createEventW          = kernel32Restart.NewProc("CreateEventW")
	waitForMultipleObject = kernel32Restart.NewProc("WaitForMultipleObjects")
)

func startUpdatedLauncher(root, releaseVersion string, _, _ io.Writer) error {
	eventName, err := newUpdateReadyEventName()
	if err != nil {
		return err
	}
	eventHandle, err := createUpdateReadyEvent(eventName)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(eventHandle)

	restoreVersion, err := setTemporaryEnvironment(updatedVersionEnvironment, releaseVersion)
	if err != nil {
		return err
	}
	defer restoreVersion()
	restoreEvent, err := setTemporaryEnvironment(updateReadyEnvironment, eventName)
	if err != nil {
		return err
	}
	defer restoreEvent()

	processInfo, err := createLauncherProcess(root)
	if err != nil {
		return err
	}
	_ = syscall.CloseHandle(processInfo.Thread)
	defer syscall.CloseHandle(processInfo.Process)

	if err := waitForLauncherReady(eventHandle, processInfo.Process); err != nil {
		if stopErr := syscall.TerminateProcess(processInfo.Process, 1); stopErr != nil {
			status, waitErr := syscall.WaitForSingleObject(processInfo.Process, 0)
			if waitErr != nil || status != waitObject0 {
				return &launcherStillRunningError{fmt.Errorf("%w；无法停止未就绪的新版进程: %v", err, stopErr)}
			}
		}
		status, waitErr := syscall.WaitForSingleObject(processInfo.Process, 10_000)
		if waitErr != nil || status != waitObject0 {
			return &launcherStillRunningError{fmt.Errorf("%w；无法确认新版进程退出: %v", err, waitErr)}
		}
		return err
	}
	return nil
}

func startRestoredLauncher(root string, _, _ io.Writer) error {
	restoreVersion, err := setTemporaryEnvironment(updatedVersionEnvironment, "")
	if err != nil {
		return err
	}
	defer restoreVersion()
	restoreEvent, err := setTemporaryEnvironment(updateReadyEnvironment, "")
	if err != nil {
		return err
	}
	defer restoreEvent()
	processInfo, err := createLauncherProcess(root)
	if err != nil {
		return err
	}
	_ = syscall.CloseHandle(processInfo.Thread)
	return syscall.CloseHandle(processInfo.Process)
}

func createLauncherProcess(root string) (*syscall.ProcessInformation, error) {
	launcher := filepath.Join(root, "bin", "llamalc.exe")
	launcherPointer, err := syscall.UTF16PtrFromString(launcher)
	if err != nil {
		return nil, err
	}
	commandLine, err := syscall.UTF16PtrFromString(`"` + launcher + `"`)
	if err != nil {
		return nil, err
	}
	rootPointer, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}

	startupInfo := &syscall.StartupInfo{Cb: uint32(unsafe.Sizeof(syscall.StartupInfo{}))}
	processInfo := &syscall.ProcessInformation{}
	if err := syscall.CreateProcess(
		launcherPointer,
		commandLine,
		nil,
		nil,
		false,
		createNewConsole,
		nil,
		rootPointer,
		startupInfo,
		processInfo,
	); err != nil {
		return nil, err
	}

	return processInfo, nil
}

func newUpdateReadyEventName() (string, error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", fmt.Errorf("无法生成更新就绪事件名称: %w", err)
	}
	return updateReadyEventPrefix + hex.EncodeToString(random[:]), nil
}

func createUpdateReadyEvent(name string) (syscall.Handle, error) {
	namePointer, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	handle, _, callErr := createEventW.Call(0, 0, 0, uintptr(unsafe.Pointer(namePointer)))
	if handle == 0 {
		return 0, fmt.Errorf("CreateEventW: %w", callErr)
	}
	return syscall.Handle(handle), nil
}

func waitForLauncherReady(eventHandle, processHandle syscall.Handle) error {
	handles := [...]syscall.Handle{eventHandle, processHandle}
	result, _, callErr := waitForMultipleObject.Call(
		uintptr(len(handles)),
		uintptr(unsafe.Pointer(&handles[0])),
		0,
		updateReadyTimeoutMS,
	)
	runtime.KeepAlive(handles)
	switch result {
	case waitObject0:
		return nil
	case waitObject0 + 1:
		var exitCode uint32
		if err := syscall.GetExitCodeProcess(processHandle, &exitCode); err != nil {
			return fmt.Errorf("新版 launcher 在报告就绪前退出，且无法读取退出码: %w", err)
		}
		return fmt.Errorf("新版 launcher 在报告就绪前退出，退出码 %d", exitCode)
	case waitTimeout:
		return fmt.Errorf("新版 launcher 在 %d 秒内未报告主菜单就绪", updateReadyTimeoutMS/1000)
	case waitFailed:
		return fmt.Errorf("等待新版 launcher 就绪失败: %w", callErr)
	default:
		return fmt.Errorf("等待新版 launcher 就绪返回未知状态 0x%x", result)
	}
}

func setTemporaryEnvironment(name, value string) (func(), error) {
	previous, existed := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil {
		return nil, err
	}
	return func() {
		if existed {
			_ = os.Setenv(name, previous)
		} else {
			_ = os.Unsetenv(name)
		}
	}, nil
}
