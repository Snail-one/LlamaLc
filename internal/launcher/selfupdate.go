package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

var errUpdaterHandoff = errors.New("已将操作交给独立更新器")

const (
	updaterBaseName          = "llamaup"
	runningUpdaterTempPrefix = ".llamaup-run-"
	stagedUpdaterTempPrefix  = ".llamaup-new-"
	rollbackUpdaterPrefix    = ".llamaup-rollback-"
	rollbackLauncherPrefix   = ".llama-launcher-rollback-"
)

func updaterExecutableName(base, goos string) string {
	if goos == "windows" {
		return base + ".exe"
	}
	return base
}

func isRunningUpdaterTemp(name string) bool {
	return numericTempSuffix(name, runningUpdaterTempPrefix, "") ||
		numericTempSuffix(name, runningUpdaterTempPrefix, ".exe")
}

func isStagedUpdaterTemp(name string) bool {
	return numericTempSuffix(name, stagedUpdaterTempPrefix, "") ||
		numericTempSuffix(name, stagedUpdaterTempPrefix, ".exe")
}

func isUpdateRollbackBackup(name string) bool {
	return numericTempSuffix(name, rollbackUpdaterPrefix, "") ||
		numericTempSuffix(name, rollbackUpdaterPrefix, ".exe") ||
		numericTempSuffix(name, rollbackLauncherPrefix, "") ||
		numericTempSuffix(name, rollbackLauncherPrefix, ".exe")
}

func launcherAssetName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("llama-launcher-%s-%s-%s%s", goos, goarch, tag, ext)
}

func launcherReleaseAssets(release GitHubRelease, goos, goarch string) (GitHubAsset, GitHubAsset, error) {
	want := launcherAssetName(release.TagName, goos, goarch)
	var archive, sums GitHubAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case want:
			archive = asset
		case "SHA256SUMS.txt":
			sums = asset
		}
	}
	if archive.Name == "" || sums.Name == "" {
		return GitHubAsset{}, GitHubAsset{}, fmt.Errorf("启动器 Release %s 缺少 %s 或 SHA256SUMS.txt", release.TagName, want)
	}
	if _, err := digestHex(archive.Digest); err != nil {
		return GitHubAsset{}, GitHubAsset{}, fmt.Errorf("启动器资产缺少 API SHA-256 digest")
	}
	if _, err := digestHex(sums.Digest); err != nil {
		return GitHubAsset{}, GitHubAsset{}, fmt.Errorf("SHA256SUMS.txt 缺少 API SHA-256 digest")
	}
	return archive, sums, nil
}

func (manager *UpdateManager) UpdateLauncher(ctx context.Context, release GitHubRelease, force, allowDowngrade bool) error {
	if buildversion.Version != "dev" {
		comparison, err := CompareSemVer(buildversion.Version, release.TagName)
		if err != nil {
			return fmt.Errorf("无法比较启动器版本: %w", err)
		}
		if comparison == 0 && !force {
			fmt.Fprintf(manager.Stdout, "启动器已是 %s，无需更新。\n", release.TagName)
			return nil
		}
		if comparison > 0 && !allowDowngrade {
			return fmt.Errorf("拒绝从 %s 降级到 %s；如确需降级请加 --allow-downgrade", buildversion.Version, release.TagName)
		}
	} else {
		fmt.Fprintln(manager.Stderr, "警告: 当前启动器版本为 dev，无法可靠判断新旧，将更新到稳定 Release。")
	}
	launcherName := "llama-launcher"
	updaterName := updaterExecutableName(updaterBaseName, manager.GOOS)
	if manager.GOOS == "windows" {
		launcherName += ".exe"
	}
	currentLauncher := filepath.Join(manager.Root, "bin", launcherName)
	currentUpdater := filepath.Join(manager.Root, "bin", updaterName)
	if err := validateManagedPath(manager.Root, currentLauncher, "当前启动器", false, false); err != nil {
		return err
	}
	if err := validateManagedPath(manager.Root, currentUpdater, "当前更新器", false, false); err != nil {
		return fmt.Errorf("无法使用当前更新器执行自更新: %w", err)
	}
	archive, sums, err := launcherReleaseAssets(release, manager.GOOS, manager.GOARCH)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Join(manager.Root, "bin"), ".launcher-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := markManagedTempDirectory(filepath.Join(manager.Root, "bin"), staging); err != nil {
		return err
	}
	sumsPath := filepath.Join(staging, sums.Name)
	if _, err := manager.Client.Download(ctx, sums, sumsPath, manager.Stdout); err != nil {
		return err
	}
	sumsData, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	checksums, err := parseSHA256SUMS(sumsData)
	if err != nil {
		return err
	}
	archivePath := filepath.Join(staging, archive.Name)
	actual, err := manager.Client.Download(ctx, archive, archivePath, manager.Stdout)
	if err != nil {
		return err
	}
	if expected, ok := checksums[archive.Name]; !ok || !strings.EqualFold(expected, actual) {
		return errors.New("启动器资产与 SHA256SUMS.txt 不一致")
	}
	extracted := filepath.Join(staging, "extracted")
	if err := os.Mkdir(extracted, 0o700); err != nil {
		return err
	}
	if err := ExtractArchive(archivePath, extracted, newExtractBudget(), manager.Stdout); err != nil {
		return err
	}
	wantLauncher := filepath.Join(extracted, "llama.cpp", "bin", launcherName)
	wantUpdater := filepath.Join(extracted, "llama.cpp", "bin", updaterName)
	if err := requireFile(wantLauncher, "新启动器"); err != nil {
		return fmt.Errorf("Release archive 结构无效: %w", err)
	}
	if err := requireFile(wantUpdater, "新更新器"); err != nil {
		return fmt.Errorf("Release archive 结构无效: %w", err)
	}
	if err := ensureOnlyLauncherFiles(extracted, wantLauncher, wantUpdater); err != nil {
		return err
	}
	probe := manager.LauncherProbe
	if probe == nil {
		probe = OSInstallationProbe{}
	}
	wantVersionLine := "version:   " + strings.ToLower(release.TagName)
	for _, item := range []struct {
		label string
		path  string
	}{{"新启动器", wantLauncher}, {"新更新器", wantUpdater}} {
		probeOutput, probeErr := probe.Probe(Command{Path: item.path, Args: []string{"--version"}, Dir: manager.Root}, installationProbeTimeout)
		if probeErr != nil {
			return fmt.Errorf("%s版本探测失败: %w%s", item.label, probeErr, formatProbeOutput(probeOutput))
		}
		if !strings.Contains(strings.ToLower(probeOutput), wantVersionLine) {
			return fmt.Errorf("%s嵌入版本与 Release %s 不一致%s", item.label, release.TagName, formatProbeOutput(probeOutput))
		}
	}
	return manager.installLauncherBinaries(ctx, wantLauncher, wantUpdater, currentLauncher, currentUpdater, release)
}

func ensureOnlyLauncherFiles(root string, wants ...string) error {
	wanted := make(map[string]bool, len(wants))
	for _, path := range wants {
		wanted[filepath.Clean(path)] = true
	}
	files := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files++
			if !wanted[filepath.Clean(path)] {
				return fmt.Errorf("启动器 archive 含多余文件: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if files != len(wanted) {
		return errors.New("启动器 archive 必须且只能包含 launcher 和 updater")
	}
	return nil
}

func currentLauncherStatus(latest string) (string, bool) {
	if buildversion.Version == "dev" {
		return "unknown", true
	}
	comparison, err := CompareSemVer(buildversion.Version, latest)
	if err != nil {
		return "unknown", true
	}
	if comparison < 0 {
		return "update-available", true
	}
	if comparison > 0 {
		return "newer", false
	}
	return "current", false
}

func copyAndSyncExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	completed = true
	return nil
}

func cleanupLauncherTemps(root string, stderr io.Writer) {
	bin := filepath.Join(root, "bin")
	if err := validateManagedPath(root, bin, "启动器目录", false, true); err != nil {
		fmt.Fprintf(stderr, "警告: 无法检查启动器更新残留: %v\n", err)
		return
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		fmt.Fprintf(stderr, "警告: 无法扫描启动器更新残留: %v\n", err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		isRunningUpdater := isRunningUpdaterTemp(name)
		isExecutableTemp := isRunningUpdater || numericTempSuffix(name, ".llama-launcher-new-", "") ||
			numericTempSuffix(name, ".llama-launcher-new-", ".exe") ||
			isStagedUpdaterTemp(name)
		if isExecutableTemp {
			path := filepath.Join(bin, name)
			info, infoErr := entry.Info()
			if infoErr != nil {
				fmt.Fprintf(stderr, "警告: 无法检查更新器残留 %s: %v\n", path, infoErr)
				continue
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				fmt.Fprintf(stderr, "警告: 拒绝清理不是普通文件的更新器残留: %s\n", path)
				continue
			}
			if !isRunningUpdater && !oldEnoughForAutomaticCleanupPath(path, info) {
				continue
			}
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(stderr, "警告: 无法清理更新器残留 %s: %v\n", path, err)
			}
			continue
		}
		if !numericTempSuffix(name, ".launcher-update-", "") {
			continue
		}
		path := filepath.Join(bin, name)
		info, infoErr := entry.Info()
		if infoErr != nil || !oldEnoughForAutomaticCleanupPath(path, info) {
			continue
		}
		if err := removeMarkedTempDirectory(bin, path); err != nil {
			fmt.Fprintf(stderr, "警告: 无法清理启动器更新残留 %s: %v\n", path, err)
		}
	}
}

func cleanupRuntimeTemps(root string, stderr io.Writer) {
	base := managedRuntimeRoot(root)
	if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := validateManagedPath(root, base, "受管运行时根目录", false, true); err != nil {
		fmt.Fprintf(stderr, "警告: 无法检查运行时更新残留: %v\n", err)
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		fmt.Fprintf(stderr, "警告: 无法扫描运行时更新残留: %v\n", err)
		return
	}
	for _, entry := range entries {
		if !numericTempSuffix(entry.Name(), ".staging-", "") {
			continue
		}
		path := filepath.Join(base, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil || !oldEnoughForAutomaticCleanupPath(path, info) {
			continue
		}
		if err := removeMarkedTempDirectory(base, path); err != nil {
			fmt.Fprintf(stderr, "警告: 无法清理运行时更新残留 %s: %v\n", path, err)
		}
	}
}
