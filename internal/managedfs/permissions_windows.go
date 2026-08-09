//go:build windows

package managedfs

import (
	"fmt"
	"os"
	"os/user"
	"syscall"
	"unsafe"
)

const (
	sddlRevision1            = 1
	daclSecurityInformation  = 0x00000004
	protectedDACLInformation = 0x80000000
)

var (
	advapi32Permissions       = syscall.NewLazyDLL("advapi32.dll")
	convertSecurityDescriptor = advapi32Permissions.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	setFileSecurity           = advapi32Permissions.NewProc("SetFileSecurityW")
	localFree                 = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
)

func protectPath(path string, permission os.FileMode) error {
	if err := os.Chmod(path, permission); err != nil {
		return err
	}
	if permission.Perm()&0o077 != 0 {
		return nil
	}
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("无法确定当前 Windows 用户 SID: %w", err)
	}
	if current.Uid == "" {
		return fmt.Errorf("当前 Windows 用户 SID 为空")
	}
	sddlPointer, err := syscall.UTF16PtrFromString(fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;SY)", current.Uid))
	if err != nil {
		return err
	}
	var descriptor uintptr
	result, _, callErr := convertSecurityDescriptor.Call(uintptr(unsafe.Pointer(sddlPointer)), sddlRevision1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if result == 0 {
		return fmt.Errorf("ConvertStringSecurityDescriptorToSecurityDescriptorW: %w", callErr)
	}
	defer localFree.Call(descriptor)
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	result, _, callErr = setFileSecurity.Call(uintptr(unsafe.Pointer(pathPointer)), daclSecurityInformation|protectedDACLInformation, descriptor)
	if result == 0 {
		return fmt.Errorf("SetFileSecurityW: %w", callErr)
	}
	return nil
}
