package updater

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

const (
	stagedLauncherPrefix = ".llama-launcher-new-"
	stagedUpdaterPrefix  = ".llama-updater-new-"
)

func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version" || args[0] == "version") {
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}
	if len(args) == 0 || args[0] != "apply-update" {
		printUsage(stderr)
		return 2
	}

	set := flag.NewFlagSet("apply-update", flag.ContinueOnError)
	set.SetOutput(stderr)
	launcherSourceName := set.String("launcher-source-name", "", "bin 目录中的暂存启动器文件名")
	updaterSourceName := set.String("updater-source-name", "", "bin 目录中的暂存更新器文件名")
	releaseVersion := set.String("release-version", "", "目标 Release 版本")
	parentPID := set.Int("wait-parent-pid", 0, "退出前需要等待的 launcher PID")
	if err := set.Parse(args[1:]); err != nil {
		return 2
	}
	if set.NArg() != 0 || *launcherSourceName == "" || *updaterSourceName == "" || *releaseVersion == "" || *parentPID <= 0 {
		printUsage(stderr)
		return 2
	}
	root, err := executableRoot()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if err := validateReleaseVersion(*releaseVersion); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if err := validateStagedName(*launcherSourceName, stagedLauncherPrefix, "启动器", runtime.GOOS); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if err := validateStagedName(*updaterSourceName, stagedUpdaterPrefix, "更新器", runtime.GOOS); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if err := waitForParent(*parentPID); err != nil {
		fmt.Fprintln(stderr, "错误: 等待启动器退出失败:", err)
		return 1
	}
	if err := applyUpdate(root, runtime.GOOS, *launcherSourceName, *updaterSourceName); err != nil {
		fmt.Fprintln(stderr, "错误: 无法应用更新:", err)
		return 1
	}
	fmt.Fprintf(stdout, "启动器与更新器已更新到 %s；请重新运行命令。\n", *releaseVersion)
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "用法: llama-updater apply-update --launcher-source-name <文件名> --updater-source-name <文件名> --release-version <tag> --wait-parent-pid <pid>")
}

func executableRoot() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	bin := filepath.Dir(real)
	if filepath.Base(bin) != "bin" {
		return "", fmt.Errorf("独立更新器必须直接位于 llama.cpp/bin，当前为: %s", real)
	}
	root := filepath.Dir(bin)
	if filepath.Base(root) != "llama.cpp" {
		return "", fmt.Errorf("部署根目录必须字面命名为 llama.cpp，当前为: %s", root)
	}
	return root, nil
}

func validateReleaseVersion(version string) error {
	if len(version) > 128 || strings.TrimSpace(version) != version || version == "" {
		return errors.New("目标 Release 版本无效")
	}
	for _, char := range version {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._+-", char) {
			continue
		}
		return errors.New("目标 Release 版本包含不允许的字符")
	}
	return nil
}

func validateStagedName(name, prefix, label, goos string) error {
	if filepath.Base(name) != name || !strings.HasPrefix(name, prefix) {
		return fmt.Errorf("暂存%s文件名无效", label)
	}
	if goos == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return fmt.Errorf("Windows 暂存%s必须是 .exe", label)
	}
	return nil
}

func applyUpdate(root, goos, launcherSourceName, updaterSourceName string) error {
	if err := validateStagedName(launcherSourceName, stagedLauncherPrefix, "启动器", goos); err != nil {
		return err
	}
	if err := validateStagedName(updaterSourceName, stagedUpdaterPrefix, "更新器", goos); err != nil {
		return err
	}
	bin := filepath.Join(root, "bin")
	launcherTargetName := "llama-launcher"
	updaterTargetName := "llama-updater"
	if goos == "windows" {
		launcherTargetName += ".exe"
		updaterTargetName += ".exe"
	}
	launcherSource := filepath.Join(bin, launcherSourceName)
	updaterSource := filepath.Join(bin, updaterSourceName)
	launcherTarget := filepath.Join(bin, launcherTargetName)
	updaterTarget := filepath.Join(bin, updaterTargetName)
	for _, item := range []struct {
		label string
		path  string
	}{
		{"暂存启动器", launcherSource},
		{"暂存更新器", updaterSource},
		{"当前启动器", launcherTarget},
		{"当前更新器", updaterTarget},
	} {
		info, err := os.Lstat(item.path)
		if err != nil {
			return fmt.Errorf("无法检查%s %s: %w", item.label, item.path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s不是普通文件: %s", item.label, item.path)
		}
	}
	if err := replaceFile(updaterSource, updaterTarget); err != nil {
		return fmt.Errorf("替换更新器: %w", err)
	}
	if err := replaceFile(launcherSource, launcherTarget); err != nil {
		return fmt.Errorf("替换启动器: %w", err)
	}
	return syncDirectory(bin)
}
