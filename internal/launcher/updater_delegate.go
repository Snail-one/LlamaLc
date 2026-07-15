package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

var errUpdaterHandoff = errors.New("已将操作交给独立更新器")

func updaterExecutableName(goos string) string {
	if goos == "windows" {
		return "llama-updater.exe"
	}
	return "llama-updater"
}

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

func ensureUpdaterTool(ctx context.Context, manager *UpdateManager) (string, error) {
	target := filepath.Join(manager.Root, "bin", updaterExecutableName(manager.GOOS))
	if updaterToolMatches(target, buildversion.Version, manager) {
		return target, nil
	}
	tag := buildversion.Version
	if tag == "dev" {
		tag = ""
		fmt.Fprintln(manager.Stderr, "警告: 当前为 dev 版本，将获取最新稳定版独立更新器。")
	}
	release, err := manager.Client.Release(ctx, launcherRepository, tag)
	if err != nil {
		return "", fmt.Errorf("无法获取独立更新器: %w", err)
	}
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
	temporary, err := os.CreateTemp(filepath.Dir(target), ".llama-updater-new-")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(temporaryPath)
	defer os.Remove(temporaryPath)
	if err := copyAndSyncExecutable(source, temporaryPath); err != nil {
		return "", err
	}
	if manager.GOOS == "linux" {
		if err := os.Chmod(temporaryPath, 0o755); err != nil {
			return "", err
		}
	}
	if err := replaceFile(temporaryPath, target); err != nil {
		return "", fmt.Errorf("无法安装独立更新器: %w", err)
	}
	fmt.Fprintf(manager.Stdout, "独立更新器 %s 已安装到 %s\n", release.TagName, target)
	return target, nil
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

var updaterManagementRunner = runUpdaterManagementProcess
var updaterToolEnsurer = ensureUpdaterTool

func delegateManagement(ctx context.Context, manager *UpdateManager, args []string, stdin io.Reader, interactive, handoff bool) (int, error) {
	tool, err := updaterToolEnsurer(ctx, manager)
	if err != nil {
		return 1, err
	}
	return updaterManagementRunner(tool, manager.Root, manager.GOOS, args, stdin, manager.Stdout, manager.Stderr, interactive, handoff)
}

func runUpdaterManagementProcess(tool, root, goos string, args []string, stdin io.Reader, stdout, stderr io.Writer, interactive, handoff bool) (int, error) {
	commandArgs := append([]string(nil), args...)
	if goos == "windows" && handoff {
		commandArgs = append([]string{"--wait-parent-pid", strconv.Itoa(os.Getpid())}, commandArgs...)
	}
	if interactive {
		commandArgs = append([]string{"--internal-interactive"}, commandArgs...)
	}
	command := exec.Command(tool, commandArgs...)
	command.Dir, command.Stdin, command.Stdout, command.Stderr = root, stdin, stdout, stderr
	if goos == "windows" && handoff {
		if err := startUpdaterHidden(command); err != nil {
			return 1, err
		}
		return 0, errUpdaterHandoff
	}
	err := command.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), fmt.Errorf("独立更新器退出，状态码 %d", exitErr.ExitCode())
	}
	return 1, err
}
