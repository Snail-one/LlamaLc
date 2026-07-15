package launcher

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strconv"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

// UpdaterMain is the standalone entry point for all installation and update
// operations. Users continue to invoke llama-launcher; it delegates here.
func UpdaterMain(args []string, stdin io.Reader, stdout, stderr io.Writer, probe InstallationProbe) int {
	interactiveOverride := false
	if len(args) > 0 && args[0] == "--internal-interactive" {
		interactiveOverride = true
		args = args[1:]
	}
	if len(args) >= 2 && args[0] == "--wait-parent-pid" {
		pid, err := strconv.Atoi(args[1])
		if err != nil || pid <= 0 {
			fmt.Fprintln(stderr, "错误: 无效的父进程 PID")
			return 2
		}
		if err := waitForUpdaterParent(pid); err != nil {
			fmt.Fprintln(stderr, "错误: 等待启动器退出失败:", err)
			return 1
		}
		args = args[2:]
	}
	if isVersionCommand(args) {
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}
	if len(args) == 0 || !isManagementCommand(args[0]) {
		fmt.Fprintln(stderr, "用法: llama-updater <install|check-update|update> [选项]")
		return 2
	}
	root, err := ExecutableRoot()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	manager := NewUpdateManager(root, probe, stdout, stderr)
	manager.GOOS, manager.GOARCH = runtime.GOOS, runtime.GOARCH
	code, commandErr := runManagementCommand(context.Background(), manager, args[0], args[1:], stdin, interactiveOverride)
	if errors.Is(commandErr, flag.ErrHelp) {
		return 0
	}
	if commandErr != nil {
		fmt.Fprintln(stderr, "错误:", commandErr)
		return code
	}
	return code
}
