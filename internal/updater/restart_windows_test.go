//go:build windows

package updater

import (
	"syscall"
	"testing"
)

func TestWindowsReadyEventNameAndWait(t *testing.T) {
	name, err := newUpdateReadyEventName()
	if err != nil {
		t.Fatal(err)
	}
	if len(name) != len(updateReadyEventPrefix)+32 {
		t.Fatalf("event name=%q", name)
	}
	handle, err := createUpdateReadyEvent(name)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	result, _, callErr := kernel32Restart.NewProc("SetEvent").Call(uintptr(handle))
	if result == 0 {
		t.Fatalf("SetEvent: %v", callErr)
	}
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForLauncherReady(handle, process); err != nil {
		t.Fatal(err)
	}
}
