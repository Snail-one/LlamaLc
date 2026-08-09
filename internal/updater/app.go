package updater

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

const (
	stagedLauncherPrefix = ".llamalc-new-"
	stagedUpdaterPrefix  = ".llamaup-new-"
)

var launchUpdatedLauncher = startUpdatedLauncher

var updateReplaceFile = replaceFile

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version" || args[0] == "version") {
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stdout, `llamaup 是 LlamaLc 的内部更新组件，不能单独执行更新。
请启动 llamalc（Windows 为 llamalc.exe），然后选择 [3] 升级维护 -> [2] 更新启动器。`)
		if runtime.GOOS == "windows" && stdin != nil {
			fmt.Fprint(stdout, "\n按 Enter 关闭...")
			_, _ = bufio.NewReader(stdin).ReadString('\n')
		}
		return 2
	}
	if args[0] != "apply-update" {
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
		return handoffFailure(stdin, stderr, "等待启动器退出失败", err)
	}
	if err := applyUpdate(root, runtime.GOOS, *launcherSourceName, *updaterSourceName); err != nil {
		return handoffFailure(stdin, stderr, "无法应用更新", err)
	}
	return finishUpdate(root, runtime.GOOS, *releaseVersion, stdin, stdout, stderr)
}

func handoffFailure(stdin io.Reader, stderr io.Writer, label string, err error) int {
	fmt.Fprintf(stderr, "错误: %s: %v\n", label, err)
	if runtime.GOOS == "windows" && stdin != nil {
		fmt.Fprint(stderr, "\n按 Enter 关闭...")
		_, _ = bufio.NewReader(stdin).ReadString('\n')
	}
	return 1
}

func finishUpdate(root, goos, releaseVersion string, stdin io.Reader, stdout, stderr io.Writer) int {
	if goos == "windows" {
		fmt.Fprintf(stdout, `
更新完成
  启动器: %s
  更新器: %s
  状态: 文件替换成功
  下一步: 正在自动启动新版本
`, releaseVersion, releaseVersion)
		if err := launchUpdatedLauncher(root, releaseVersion, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "错误: 文件已更新，但无法自动启动新版 llamalc:", err)
			fmt.Fprintln(stderr, "请手动启动 bin\\llamalc.exe。")
			if stdin != nil {
				fmt.Fprint(stderr, "\n按 Enter 关闭...")
				_, _ = bufio.NewReader(stdin).ReadString('\n')
			}
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, `
更新完成
  启动器: %s
  更新器: %s
  状态: 文件替换成功
  下一步: 正在自动启动新版本
`, releaseVersion, releaseVersion)
	if err := launchUpdatedLauncher(root, releaseVersion, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "错误: 文件已更新，但无法自动启动新版 llamalc:", err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "用法: llamaup apply-update --launcher-source-name <文件名> --updater-source-name <文件名> --release-version <tag> --wait-parent-pid <pid>")
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
		return "", fmt.Errorf("独立更新器必须直接位于 LlamaLc/bin，当前为: %s", real)
	}
	root := filepath.Dir(bin)
	validRootName := filepath.Base(root) == "LlamaLc"
	if runtime.GOOS == "windows" {
		validRootName = strings.EqualFold(filepath.Base(root), "LlamaLc")
	}
	if !validRootName {
		return "", fmt.Errorf("部署根目录必须命名为 LlamaLc，当前为: %s", root)
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
	if name != strings.ToLower(name) || filepath.Base(name) != name || !strings.HasPrefix(name, prefix) {
		return fmt.Errorf("暂存%s文件名无效", label)
	}
	value := name
	if goos == "windows" {
		if !strings.HasSuffix(strings.ToLower(name), ".exe") {
			return fmt.Errorf("Windows 暂存%s必须是 .exe", label)
		}
		value = name[:len(name)-4]
	} else if strings.HasSuffix(strings.ToLower(name), ".exe") {
		return fmt.Errorf("非 Windows 暂存%s不能是 .exe", label)
	}
	token := strings.TrimPrefix(value, prefix)
	if token == value || len(token) != 16 || !lowerHex(token) {
		return fmt.Errorf("暂存%s文件名必须包含固定 16 位十六进制 token", label)
	}
	return nil
}

func lowerHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func applyUpdate(root, goos, launcherSourceName, updaterSourceName string) error {
	if err := validateStagedName(launcherSourceName, stagedLauncherPrefix, "启动器", goos); err != nil {
		return err
	}
	if err := validateStagedName(updaterSourceName, stagedUpdaterPrefix, "更新器", goos); err != nil {
		return err
	}
	bin := filepath.Join(root, "bin")
	launcherTargetName := "llamalc"
	updaterTargetName := "llamaup"
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
	return replaceUpdateFiles(bin, updaterSource, updaterTarget, launcherSource, launcherTarget)
}

func replaceUpdateFiles(bin, updaterSource, updaterTarget, launcherSource, launcherTarget string) error {
	updaterBackup, err := copyUpdateBackup(updaterTarget)
	if err != nil {
		return fmt.Errorf("备份当前更新器: %w", err)
	}
	removeUpdaterBackup := true
	defer func() {
		if removeUpdaterBackup {
			_ = os.Remove(updaterBackup)
		}
	}()

	launcherBackup, err := copyUpdateBackup(launcherTarget)
	if err != nil {
		return fmt.Errorf("备份当前启动器: %w", err)
	}
	removeLauncherBackup := true
	defer func() {
		if removeLauncherBackup {
			_ = os.Remove(launcherBackup)
		}
	}()

	if err := updateReplaceFile(updaterSource, updaterTarget); err != nil {
		return fmt.Errorf("替换更新器: %w", err)
	}
	if err := updateReplaceFile(launcherSource, launcherTarget); err != nil {
		rollbackErr := updateReplaceFile(updaterBackup, updaterTarget)
		if rollbackErr != nil {
			removeUpdaterBackup = false
			return fmt.Errorf("替换启动器: %w；同时无法恢复原更新器，备份保留在 %s: %v", err, updaterBackup, rollbackErr)
		}
		return fmt.Errorf("替换启动器: %w；已恢复原更新器", err)
	}
	if err := syncDirectory(bin); err != nil {
		launcherRollbackErr := updateReplaceFile(launcherBackup, launcherTarget)
		if launcherRollbackErr != nil {
			removeLauncherBackup = false
		}
		updaterRollbackErr := updateReplaceFile(updaterBackup, updaterTarget)
		if updaterRollbackErr != nil {
			removeUpdaterBackup = false
		}
		if launcherRollbackErr != nil || updaterRollbackErr != nil {
			return fmt.Errorf("同步更新目录失败: %w；回滚也未完整成功（launcher: %v，updater: %v）", err, launcherRollbackErr, updaterRollbackErr)
		}
		return fmt.Errorf("同步更新目录失败: %w；已恢复原启动器和更新器", err)
	}
	return nil
}

func copyUpdateBackup(source string) (string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("不是普通文件: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()

	extension := filepath.Ext(source)
	base := strings.TrimSuffix(filepath.Base(source), extension)
	var tokenBytes [8]byte
	if _, err := io.ReadFull(rand.Reader, tokenBytes[:]); err != nil {
		return "", err
	}
	backupPath := filepath.Join(filepath.Dir(source), "."+base+"-rollback-"+hex.EncodeToString(tokenBytes[:])+extension)
	backup, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return "", err
	}
	completed := false
	defer func() {
		if !completed {
			_ = backup.Close()
			_ = os.Remove(backupPath)
		}
	}()
	if _, err := io.Copy(backup, input); err != nil {
		return "", err
	}
	if err := backup.Sync(); err != nil {
		return "", err
	}
	if err := backup.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(backupPath, info.Mode().Perm()); err != nil {
		return "", err
	}
	completed = true
	return backupPath, nil
}
