//go:build windows

package managedfs

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsAtomicWriteReplaceAndProtectedDACL(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "state", "secret")
	if err := AtomicWrite(root, target, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(root, target, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "two" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	sddl := securityDescriptorString(t, target)
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	systemPresent := strings.Contains(sddl, "S-1-5-18") || strings.Contains(sddl, ";;;SY")
	if !strings.Contains(sddl, "P") || !strings.Contains(sddl, current.Uid) || !systemPresent {
		t.Fatalf("DACL is not protected for current user and LocalSystem: %s", sddl)
	}
}

func securityDescriptorString(t *testing.T, path string) string {
	t.Helper()
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	advapi := syscall.NewLazyDLL("advapi32.dll")
	var descriptor uintptr
	result, _, callErr := advapi.NewProc("GetNamedSecurityInfoW").Call(uintptr(unsafe.Pointer(pathPointer)), 1, daclSecurityInformation, 0, 0, 0, 0, uintptr(unsafe.Pointer(&descriptor)))
	if result != 0 {
		t.Fatalf("GetNamedSecurityInfoW: %v", callErr)
	}
	defer localFree.Call(descriptor)
	var textPointer uintptr
	var length uint32
	result, _, callErr = advapi.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW").Call(descriptor, sddlRevision1, daclSecurityInformation, uintptr(unsafe.Pointer(&textPointer)), uintptr(unsafe.Pointer(&length)))
	if result == 0 {
		t.Fatalf("ConvertSecurityDescriptorToStringSecurityDescriptorW: %v", callErr)
	}
	defer localFree.Call(textPointer)
	characters := unsafe.Slice((*uint16)(unsafe.Pointer(textPointer)), int(length))
	return syscall.UTF16ToString(characters)
}
