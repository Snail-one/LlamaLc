//go:build windows

package launcher

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
	advapi32Permissions                                 = syscall.NewLazyDLL("advapi32.dll")
	convertStringSecurityDescriptorToSecurityDescriptor = advapi32Permissions.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	setFileSecurity                                     = advapi32Permissions.NewProc("SetFileSecurityW")
	kernel32Permissions                                 = syscall.NewLazyDLL("kernel32.dll")
	localFree                                           = kernel32Permissions.NewProc("LocalFree")
)

func applyFilePermissions(path string, perm os.FileMode) error {
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	if perm.Perm()&0o077 != 0 {
		return nil
	}

	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("无法确定当前 Windows 用户: %w", err)
	}
	if current.Uid == "" {
		return fmt.Errorf("当前 Windows 用户 SID 为空")
	}

	// Protect the DACL from inherited entries and grant access only to the
	// current user and LocalSystem. Administrators can still take ownership as
	// part of the Windows security model, but do not receive a readable ACE.
	sddl := fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;SY)", current.Uid)
	sddlPtr, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return err
	}
	var descriptor uintptr
	result, _, callErr := convertStringSecurityDescriptorToSecurityDescriptor.Call(
		uintptr(unsafe.Pointer(sddlPtr)),
		sddlRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 {
		return fmt.Errorf("ConvertStringSecurityDescriptorToSecurityDescriptorW: %w", callErr)
	}
	defer localFree.Call(descriptor)

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	result, _, callErr = setFileSecurity.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		daclSecurityInformation|protectedDACLInformation,
		descriptor,
	)
	if result == 0 {
		return fmt.Errorf("SetFileSecurityW: %w", callErr)
	}
	return nil
}
