package updater

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/procinfo"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

const (
	stagedLauncherPrefix = ".llamalc-new-"
	stagedUpdaterPrefix  = ".llamaup-new-"
	lockOwnerName        = ".llamalc-lock-owner.json"
)

type updateLockOwner struct {
	Schema   int    `json:"schema"`
	Token    string `json:"token"`
	Kind     string `json:"kind"`
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

var launchUpdatedLauncher = startUpdatedLauncher

var updateReplaceFile = replaceFile

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version" || args[0] == "version") {
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		printUsage(stdout)
		return 0
	}
	if len(args) == 0 || (args[0] != "apply-update" && args[0] != "apply-recovery") {
		return runWorkflow(args, stdin, stdout, stderr)
	}

	recovering := args[0] == "apply-recovery"
	set := flag.NewFlagSet(args[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	launcherSourceName := set.String("launcher-source-name", "", "bin 目录中的暂存启动器文件名")
	updaterSourceName := set.String("updater-source-name", "", "bin 目录中的暂存更新器文件名")
	releaseVersion := set.String("release-version", "", "目标 Release 版本")
	parentPID := set.Int("wait-parent-pid", 0, "退出前需要等待的 launcher PID")
	parentIdentity := set.String("wait-parent-identity", "", "下载更新器进程启动身份")
	launcherDigest := set.String("launcher-sha256", "", "暂存启动器摘要")
	updaterDigest := set.String("updater-sha256", "", "暂存更新器摘要")
	launcherPID := set.Int("wait-launcher-pid", 0, "原主程序 PID")
	launcherIdentity := set.String("wait-launcher-identity", "", "原主程序启动身份")
	lockToken := set.String("lock-token", "", "启动器交接锁 token")
	if err := set.Parse(args[1:]); err != nil {
		return 2
	}
	if set.NArg() != 0 || (!recovering && (*launcherSourceName == "" || *updaterSourceName == "" || *releaseVersion == "")) || *parentPID <= 0 || len(*lockToken) != 16 || !lowerHex(*lockToken) {
		printUsage(stderr)
		return 2
	}
	root, err := executableRoot()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if !recovering {
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
	}

	lockDirectory, err := validateUpdateLock(root, *lockToken, *parentPID)
	if err != nil {
		fmt.Fprintln(stderr, "错误: 启动器更新锁无效:", err)
		return 1
	}
	if err := claimUpdateLock(root, lockDirectory, *lockToken); err != nil {
		fmt.Fprintln(stderr, "错误: 无法接管启动器更新锁:", err)
		return 1
	}
	if err := acknowledgeHandoff(root, lockDirectory, *lockToken); err != nil {
		fmt.Fprintln(stderr, "错误: 无法确认接管更新任务:", err)
		return 1
	}
	defer os.RemoveAll(lockDirectory)
	stopHeartbeat := startUpdateLockHeartbeat(lockDirectory)
	defer stopHeartbeat()
	waitParent := func() error {
		if *parentIdentity != "" {
			return waitForLauncherExit(*parentPID, *parentIdentity)
		}
		return waitForParent(*parentPID)
	}
	if err := waitParent(); err != nil {
		return handoffFailure(stdin, stderr, "等待启动器退出失败", err)
	}
	if *launcherPID > 0 {
		if err := waitForLauncherExit(*launcherPID, *launcherIdentity); err != nil {
			return handoffFailure(stdin, stderr, "等待原主程序退出失败", err)
		}
	}
	if recovering {
		if err := recoverTransaction(root, runtime.GOOS); err != nil {
			return handoffFailure(stdin, stderr, "恢复更新失败，备份已保留", err)
		}
		fmt.Fprintln(stdout, "中断的更新已恢复，正在启动主程序。")
		if err := startRestoredLauncher(root, stdout, stderr); err != nil {
			return handoffFailure(stdin, stderr, "恢复完成但主程序启动失败", err)
		}
		return 0
	}
	for _, entry := range [][2]string{{*launcherSourceName, *launcherDigest}, {*updaterSourceName, *updaterDigest}} {
		// Empty digests retain the previous release's internal handoff protocol.
		if entry[1] == "" {
			continue
		}
		digest, err := fileDigest(filepath.Join(root, "bin", entry[0]))
		if err != nil || !validDigest(entry[1]) || digest != entry[1] {
			return handoffFailure(stdin, stderr, "暂存程序摘要不匹配，原程序未替换", errors.New(entry[0]))
		}
	}
	started := false
	if err := applyUpdateWithValidation(root, runtime.GOOS, *launcherSourceName, *updaterSourceName, func() error {
		fmt.Fprintln(stdout, "文件替换完成，正在启动新版并等待就绪……")
		err := launchUpdatedLauncher(root, *releaseVersion, stdout, stderr)
		started = err == nil
		return err
	}); err != nil {
		fmt.Fprintln(stderr, "错误: 更新未完成:", err)
		if !started && !transactionPending(root) {
			if restartErr := startRestoredLauncher(root, stdout, stderr); restartErr != nil {
				fmt.Fprintln(stderr, "错误: 无法自动启动原版本，请手动启动 llamalc:", restartErr)
			}
		}
		return 1
	}
	fmt.Fprintf(stdout, "更新完成：启动器和更新器 %s，新版已就绪。\n", *releaseVersion)
	return 0
}

func startUpdateLockHeartbeat(directory string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				_ = os.Chtimes(filepath.Join(directory, ".llamalc-owned.json"), now, now)
				_ = os.Chtimes(filepath.Join(directory, lockOwnerName), now, now)
				_ = os.Chtimes(directory, now, now)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func handoffFailure(stdin io.Reader, stderr io.Writer, label string, err error) int {
	fmt.Fprintf(stderr, "错误: %s: %v\n", label, err)
	if runtime.GOOS == "windows" && stdin != nil {
		fmt.Fprint(stderr, "\n按 Enter 关闭...")
		_, _ = bufio.NewReader(stdin).ReadString('\n')
	}
	return 1
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "用法: llamaup [update] [--version TAG] [--reinstall] [--allow-downgrade] [--yes]\n      llamaup check [--json]\n      llamaup recover [--yes]\n内部交接: llamaup apply-update --launcher-source-name <文件名> --updater-source-name <文件名> --release-version <tag> --wait-parent-pid <pid> --lock-token <16位hex>")
}

func validateUpdateLock(root, token string, parentPID int) (string, error) {
	directory := filepath.Join(root, "runtime", ".launcher-lock-"+token)
	if err := managedfs.Validate(root, directory, false); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("锁路径不是普通目录")
	}
	markerPath := filepath.Join(directory, ".llamalc-owned.json")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return "", err
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || markerInfo.Size() > 4096 {
		return "", errors.New("锁所有权标记无效")
	}
	file, err := os.Open(markerPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	openedMarkerInfo, err := file.Stat()
	if err != nil || !openedMarkerInfo.Mode().IsRegular() || openedMarkerInfo.Size() > 4096 || !os.SameFile(markerInfo, openedMarkerInfo) {
		return "", errors.New("锁所有权标记身份发生变化")
	}
	var marker struct {
		Schema int    `json:"schema"`
		Token  string `json:"token"`
		Kind   string `json:"kind"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("锁所有权标记包含多余内容")
	}
	if marker.Schema != 1 || marker.Token != token || marker.Kind != "launcher-install-lock" {
		return "", errors.New("锁所有权标记不匹配")
	}
	owner, exists, err := readUpdateLockOwner(directory)
	if err != nil {
		return "", err
	}
	if exists && (owner.Token != token || owner.Kind != "launcher-install-lock" || (owner.PID != parentPID && owner.PID != os.Getpid())) {
		return "", errors.New("锁持有进程与更新交接不匹配")
	}
	if exists {
		identity, alive, identityErr := procinfo.Identity(owner.PID)
		if identityErr == nil && alive && identity != owner.Identity {
			return "", errors.New("锁持有进程 PID 已被复用")
		}
	}
	return directory, nil
}

func claimUpdateLock(root, directory, token string) error {
	identity, alive, err := procinfo.Identity(os.Getpid())
	if err != nil {
		return err
	}
	if !alive || identity == "" {
		return errors.New("无法确认 updater 进程身份")
	}
	owner := updateLockOwner{Schema: 1, Token: token, Kind: "launcher-install-lock", PID: os.Getpid(), Identity: identity}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	return managedfs.AtomicWrite(root, filepath.Join(directory, lockOwnerName), append(data, '\n'), 0o600)
}

func acknowledgeHandoff(root, directory, token string) error {
	identity, alive, err := procinfo.Identity(os.Getpid())
	if err != nil {
		return err
	}
	if !alive {
		return errors.New("更新器已退出")
	}
	data, err := json.Marshal(updateLockOwner{Schema: 1, Token: token, Kind: "launcher-install-lock", PID: os.Getpid(), Identity: identity})
	if err != nil {
		return err
	}
	return managedfs.AtomicWrite(root, filepath.Join(directory, ".llamalc-handoff-ready.json"), append(data, '\n'), 0o600)
}

func readUpdateLockOwner(directory string) (updateLockOwner, bool, error) {
	path := filepath.Join(directory, lockOwnerName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return updateLockOwner{}, false, nil
	}
	if err != nil {
		return updateLockOwner{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return updateLockOwner{}, false, errors.New("锁持有者标记无效")
	}
	file, err := os.Open(path)
	if err != nil {
		return updateLockOwner{}, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() > 4096 || !os.SameFile(info, openedInfo) {
		return updateLockOwner{}, false, errors.New("锁持有者标记身份发生变化")
	}
	var owner updateLockOwner
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil {
		return updateLockOwner{}, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return updateLockOwner{}, false, errors.New("锁持有者标记包含多余内容")
	}
	if owner.Schema != 1 || owner.PID <= 0 || owner.Identity == "" {
		return updateLockOwner{}, false, errors.New("锁持有者标记内容无效")
	}
	return owner, true, nil
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
	return applyUpdateWithValidation(root, goos, launcherSourceName, updaterSourceName, nil)
}

func applyUpdateWithValidation(root, goos, launcherSourceName, updaterSourceName string, verify func() error) error {
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
	identities := make(map[string]os.FileInfo, 4)
	for _, item := range []struct {
		label string
		path  string
	}{
		{"暂存启动器", launcherSource},
		{"暂存更新器", updaterSource},
		{"当前启动器", launcherTarget},
		{"当前更新器", updaterTarget},
	} {
		info, err := stableUpdaterFileInfo(item.path)
		if errors.Is(err, os.ErrNotExist) && item.path == launcherTarget {
			identities[item.path] = nil
			continue
		}
		if err != nil {
			return fmt.Errorf("无法检查%s %s: %w", item.label, item.path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s不是普通文件: %s", item.label, item.path)
		}
		identities[item.path] = info
	}
	return installTransaction(bin, updaterSource, updaterTarget, launcherSource, launcherTarget, identities, verify)
}

func stableUpdaterFileInfo(path string) (os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("不是普通文件: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	handleInfo, statErr := file.Stat()
	same := statErr == nil && os.SameFile(pathInfo, handleInfo)
	closeErr := file.Close()
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !same {
		return nil, fmt.Errorf("文件身份在检查时发生变化: %s", path)
	}
	return handleInfo, nil
}

func verifyUpdaterFileIdentity(path string, expected os.FileInfo) error {
	if expected == nil {
		return fmt.Errorf("缺少文件身份: %s", path)
	}
	current, err := stableUpdaterFileInfo(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return fmt.Errorf("文件在更新交接期间被替换: %s", path)
	}
	return nil
}

func copyUpdateBackup(source string, expected os.FileInfo) (string, error) {
	if err := verifyUpdaterFileIdentity(source, expected); err != nil {
		return "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(expected, info) {
		return "", fmt.Errorf("备份源文件身份发生变化: %s", source)
	}

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
