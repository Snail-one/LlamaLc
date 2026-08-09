//go:build windows

package updater

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const updatedVersionEnvironment = "LLAMALC_UPDATED_VERSION"

const createNewConsole = 0x00000010

func startUpdatedLauncher(root, releaseVersion string, _, _ io.Writer) error {
	launcher := filepath.Join(root, "bin", "llama-launcher.exe")
	launcherPointer, err := syscall.UTF16PtrFromString(launcher)
	if err != nil {
		return err
	}
	commandLine, err := syscall.UTF16PtrFromString(`"` + launcher + `"`)
	if err != nil {
		return err
	}
	rootPointer, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return err
	}

	previousVersion, hadPreviousVersion := os.LookupEnv(updatedVersionEnvironment)
	if err := os.Setenv(updatedVersionEnvironment, releaseVersion); err != nil {
		return err
	}
	defer func() {
		if hadPreviousVersion {
			_ = os.Setenv(updatedVersionEnvironment, previousVersion)
		} else {
			_ = os.Unsetenv(updatedVersionEnvironment)
		}
	}()

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
		return err
	}
	_ = syscall.CloseHandle(processInfo.Thread)
	_ = syscall.CloseHandle(processInfo.Process)
	return nil
}
