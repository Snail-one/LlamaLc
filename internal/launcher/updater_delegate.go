package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errUpdaterHandoff = errors.New("已将操作交给独立更新器")

func updaterAssetName(tag, goos, goarch string) string {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("llama-updater-%s-%s-%s%s", tag, goos, goarch, ext)
}

func updaterReleaseAssets(release GitHubRelease, goos, goarch string) (GitHubAsset, GitHubAsset, error) {
	want := updaterAssetName(release.TagName, goos, goarch)
	var updater, sums GitHubAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case want:
			updater = asset
		case "SHA256SUMS.txt":
			sums = asset
		}
	}
	if updater.Name == "" || sums.Name == "" {
		return GitHubAsset{}, GitHubAsset{}, fmt.Errorf("启动器 Release %s 缺少 %s 或 SHA256SUMS.txt", release.TagName, want)
	}
	if _, err := digestHex(updater.Digest); err != nil {
		return GitHubAsset{}, GitHubAsset{}, errors.New("独立更新器资产缺少 API SHA-256 digest")
	}
	if _, err := digestHex(sums.Digest); err != nil {
		return GitHubAsset{}, GitHubAsset{}, errors.New("SHA256SUMS.txt 缺少 API SHA-256 digest")
	}
	return updater, sums, nil
}

func prepareEphemeralUpdater(ctx context.Context, manager *UpdateManager, release GitHubRelease) (string, error) {
	asset, sums, err := updaterReleaseAssets(release, manager.GOOS, manager.GOARCH)
	if err != nil {
		return "", err
	}
	if err := preflightDownloadSizes(manager.Root, []GitHubAsset{asset, sums}); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Join(manager.Root, "bin"), ".updater-bootstrap-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	sumsPath := filepath.Join(staging, sums.Name)
	if _, err := manager.Client.Download(ctx, sums, sumsPath, manager.Stdout); err != nil {
		return "", err
	}
	sumsData, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", err
	}
	checksums, err := parseSHA256SUMS(sumsData)
	if err != nil {
		return "", err
	}
	source := filepath.Join(staging, filepath.Base(asset.Name))
	actual, err := manager.Client.Download(ctx, asset, source, manager.Stdout)
	if err != nil {
		return "", err
	}
	if expected, ok := checksums[asset.Name]; !ok || !strings.EqualFold(expected, actual) {
		return "", errors.New("独立更新器资产与 SHA256SUMS.txt 不一致")
	}
	if manager.GOOS == "linux" {
		if err := os.Chmod(source, 0o755); err != nil {
			return "", err
		}
	}
	if !updaterToolMatches(source, release.TagName, manager) {
		return "", fmt.Errorf("独立更新器嵌入版本与 Release %s 不一致", release.TagName)
	}
	temporaryPattern := ".llama-updater-run-*"
	if manager.GOOS == "windows" {
		temporaryPattern += ".exe"
	}
	temporary, err := os.CreateTemp(filepath.Join(manager.Root, "bin"), temporaryPattern)
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(temporaryPath)
	if err := copyAndSyncExecutable(source, temporaryPath); err != nil {
		return "", err
	}
	if manager.GOOS == "linux" {
		if err := os.Chmod(temporaryPath, 0o755); err != nil {
			return "", err
		}
	}
	keep = true
	fmt.Fprintf(manager.Stdout, "临时独立更新器 %s 已准备到 %s\n", release.TagName, temporaryPath)
	return temporaryPath, nil
}

func updaterToolMatches(path, version string, manager *UpdateManager) bool {
	if err := requireFile(path, "独立更新器"); err != nil {
		return false
	}
	probe := manager.LauncherProbe
	if probe == nil {
		probe = OSInstallationProbe{}
	}
	output, err := probe.Probe(Command{Path: path, Args: []string{"--version"}, Dir: manager.Root}, installationProbeTimeout)
	if err != nil {
		return false
	}
	if version == "dev" {
		return strings.Contains(strings.ToLower(output), "version:")
	}
	return strings.Contains(strings.ToLower(output), "version:   "+strings.ToLower(version))
}

var ephemeralUpdaterPreparer = prepareEphemeralUpdater
