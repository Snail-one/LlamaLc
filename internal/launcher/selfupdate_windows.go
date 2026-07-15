//go:build windows

package launcher

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	processSynchronize = 0x00100000
	waitInfinite       = 0xffffffff
)

var (
	kernel32SelfUpdate  = syscall.NewLazyDLL("kernel32.dll")
	openProcessUpdate   = kernel32SelfUpdate.NewProc("OpenProcess")
	waitForSingleObject = kernel32SelfUpdate.NewProc("WaitForSingleObject")
	closeHandleUpdate   = kernel32SelfUpdate.NewProc("CloseHandle")
)

func installLauncherBinary(source, target, version string, out io.Writer) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".llama-launcher-new-*.exe")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(temporaryPath)
	if err := copyAndSyncExecutable(source, temporaryPath); err != nil {
		return err
	}
	command := exec.Command(temporaryPath, "--internal-replace", "--parent-pid", strconv.Itoa(os.Getpid()), "--source", temporaryPath, "--target", target)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	fmt.Fprintf(out, "启动器 %s 已校验；退出后将完成替换，请重新运行命令。\n", version)
	return nil
}

func runInternalReplace(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "--internal-replace" {
		return false, 0
	}
	set := flag.NewFlagSet("internal-replace", flag.ContinueOnError)
	set.SetOutput(stderr)
	parentPID := set.Int("parent-pid", 0, "")
	source := set.String("source", "", "")
	target := set.String("target", "", "")
	if err := set.Parse(args[1:]); err != nil || *parentPID <= 0 || *source == "" || *target == "" {
		fmt.Fprintln(stderr, "invalid internal replace arguments")
		return true, 2
	}
	handle, _, _ := openProcessUpdate.Call(processSynchronize, 0, uintptr(uint32(*parentPID)))
	if handle != 0 {
		_, _, _ = waitForSingleObject.Call(handle, waitInfinite)
		_, _, _ = closeHandleUpdate.Call(handle)
	}
	if !strings.EqualFold(filepath.Clean(filepath.Dir(*source)), filepath.Clean(filepath.Dir(*target))) {
		fmt.Fprintln(stderr, "internal replace files must share a directory")
		return true, 1
	}
	if err := replaceFile(*source, *target); err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	fmt.Fprintln(stdout, "launcher replaced")
	return true, 0
}
