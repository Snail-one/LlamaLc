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

func launcherAssetName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("llama-launcher-%s-%s-%s%s", tag, goos, goarch, ext)
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
	archive, sums, err := launcherReleaseAssets(release, manager.GOOS, manager.GOARCH)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Join(manager.Root, "bin"), ".launcher-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
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
	executableName := "llama-launcher"
	if manager.GOOS == "windows" {
		executableName += ".exe"
	}
	wantPath := filepath.Join(extracted, "llama.cpp", "bin", executableName)
	if err := requireFile(wantPath, "新启动器"); err != nil {
		return fmt.Errorf("Release archive 结构无效: %w", err)
	}
	if err := ensureOnlyLauncherFile(extracted, wantPath); err != nil {
		return err
	}
	probe := manager.LauncherProbe
	if probe == nil {
		probe = OSInstallationProbe{}
	}
	probeOutput, err := probe.Probe(Command{Path: wantPath, Args: []string{"--version"}, Dir: manager.Root}, installationProbeTimeout)
	if err != nil {
		return fmt.Errorf("新启动器版本探测失败: %w%s", err, formatProbeOutput(probeOutput))
	}
	wantVersionLine := "version:   " + strings.ToLower(release.TagName)
	if !strings.Contains(strings.ToLower(probeOutput), wantVersionLine) {
		return fmt.Errorf("新启动器嵌入版本与 Release %s 不一致%s", release.TagName, formatProbeOutput(probeOutput))
	}
	currentName := "llama-launcher"
	if manager.GOOS == "windows" {
		currentName += ".exe"
	}
	current := filepath.Join(manager.Root, "bin", currentName)
	if err := validateManagedPath(manager.Root, current, "当前启动器", false, false); err != nil {
		return err
	}
	return manager.installLauncherBinary(ctx, wantPath, current, release)
}

func ensureOnlyLauncherFile(root, want string) error {
	files := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files++
			if filepath.Clean(path) != filepath.Clean(want) {
				return fmt.Errorf("启动器 archive 含多余文件: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if files != 1 {
		return errors.New("启动器 archive 必须只含一个可执行文件")
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
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
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
		isEphemeralUpdater := strings.HasPrefix(name, ".llama-updater-run-")
		isLegacyUpdater := name == "llama-updater.exe" || name == "llama-updater"
		if isEphemeralUpdater || isLegacyUpdater {
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
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(stderr, "警告: 无法清理更新器残留 %s: %v\n", path, err)
			}
			continue
		}
		if !strings.HasPrefix(name, ".llama-launcher-new-") &&
			!strings.HasPrefix(name, ".llama-updater-new-") &&
			!strings.HasPrefix(name, ".updater-bootstrap-") &&
			!strings.HasPrefix(name, ".launcher-update-") {
			continue
		}
		path := filepath.Join(bin, name)
		if err := os.RemoveAll(path); err != nil {
			fmt.Fprintf(stderr, "警告: 无法清理启动器更新残留 %s: %v\n", path, err)
		}
	}
}
