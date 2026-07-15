package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type UpdateManager struct {
	Root          string
	GOOS          string
	GOARCH        string
	Client        *GitHubClient
	Probe         InstallationProbe
	LauncherProbe InstallationProbe
	Stdout        io.Writer
	Stderr        io.Writer
}

func NewUpdateManager(root string, probe InstallationProbe, stdout, stderr io.Writer) *UpdateManager {
	return &UpdateManager{
		Root: root, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Client: NewGitHubClient(), Probe: probe, LauncherProbe: OSInstallationProbe{}, Stdout: stdout, Stderr: stderr,
	}
}

func (manager *UpdateManager) retryPendingCleanup(state *UpdateState) {
	if state == nil || len(state.PendingCleanup) == 0 {
		return
	}
	remaining := state.PendingCleanup[:0]
	for _, relative := range state.PendingCleanup {
		clean, err := validateRuntimeRelativePath(relative, false)
		if err != nil {
			remaining = append(remaining, relative)
			continue
		}
		path := filepath.Join(manager.Root, clean)
		if err := os.RemoveAll(path); err != nil {
			remaining = append(remaining, relative)
			fmt.Fprintf(manager.Stderr, "警告: 仍无法清理旧运行时 %s: %v\n", path, err)
		}
	}
	state.PendingCleanup = remaining
}

func (manager *UpdateManager) RetryPendingCleanup() error {
	state, exists, err := LoadUpdateState(manager.Root)
	if err != nil {
		fmt.Fprintf(manager.Stderr, "警告: 更新状态损坏，跳过待清理目录重试: %v\n", err)
		return nil
	}
	if !exists {
		return nil
	}
	before := append([]string(nil), state.PendingCleanup...)
	manager.retryPendingCleanup(&state)
	if len(before) != len(state.PendingCleanup) {
		return WriteUpdateState(manager.Root, state)
	}
	return nil
}

func (manager *UpdateManager) InstallLlama(ctx context.Context, release GitHubRelease, backend string, force, allowDowngrade, requireExisting bool) error {
	state, exists, err := LoadUpdateState(manager.Root)
	if err != nil {
		return err
	}
	if requireExisting && !exists {
		return errors.New("尚未安装受管 llama.cpp；请先运行 install")
	}
	if !requireExisting && exists {
		if _, resolveErr := ResolveManagedPaths(manager.Root, manager.GOOS, state); resolveErr == nil {
			return errors.New("llama.cpp 已安装；请使用 update --component llama")
		}
		return fmt.Errorf("已有损坏的更新状态；请修复或移除 %s 后重试", UpdateStatePath(manager.Root))
	}
	pendingBefore := len(state.PendingCleanup)
	manager.retryPendingCleanup(&state)
	if exists && pendingBefore != len(state.PendingCleanup) {
		if err := WriteUpdateState(manager.Root, state); err != nil {
			return err
		}
	}
	if exists {
		comparison, compareErr := CompareLlamaTags(state.LlamaTag, release.TagName)
		if compareErr != nil {
			return compareErr
		}
		if comparison == 0 && !force {
			fmt.Fprintf(manager.Stdout, "llama.cpp 已是 %s，无需更新。\n", release.TagName)
			return nil
		}
		if comparison > 0 && !allowDowngrade {
			return fmt.Errorf("拒绝从 %s 降级到 %s；如确需降级请加 --allow-downgrade", state.LlamaTag, release.TagName)
		}
		if strings.TrimSpace(backend) == "" {
			backend = state.Backend
		}
	}
	options, err := ResolveLlamaAssets(release, manager.GOOS, manager.GOARCH)
	if err != nil {
		return err
	}
	option, err := SelectBackend(options, backend)
	if err != nil {
		return err
	}
	return manager.installResolvedLlama(ctx, release, option, state, exists, force)
}

func (manager *UpdateManager) installResolvedLlama(ctx context.Context, release GitHubRelease, option BackendOption, oldState UpdateState, existed, force bool) error {
	var total int64
	for _, asset := range option.Assets {
		if asset.Size <= 0 || asset.Size > maxAssetDownload || asset.Size > maxTotalDownload-total {
			return fmt.Errorf("资产组合下载量超过 4 GiB 或大小无效")
		}
		total += asset.Size
	}
	base := managedRuntimeRoot(manager.Root)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if err := validateManagedPath(manager.Root, base, "受管运行时根目录", false, true); err != nil {
		return err
	}
	if err := ensureFreeSpace(base, total*3); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(base, ".staging-")
	if err != nil {
		return err
	}
	defer func() { os.RemoveAll(staging) }()
	downloads := filepath.Join(staging, "downloads")
	extracted := filepath.Join(staging, "runtime")
	if err := os.Mkdir(downloads, 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(extracted, 0o755); err != nil {
		return err
	}
	installed := make([]InstalledAsset, 0, len(option.Assets))
	budget := newExtractBudget()
	for index, asset := range option.Assets {
		archivePath := filepath.Join(downloads, fmt.Sprintf("%02d-%s", index, filepath.Base(asset.Name)))
		digest, err := manager.Client.Download(ctx, asset, archivePath, manager.Stdout)
		if err != nil {
			return err
		}
		installed = append(installed, InstalledAsset{Name: asset.Name, SHA256: digest})
		if err := ExtractArchive(archivePath, extracted, budget, manager.Stdout); err != nil {
			return fmt.Errorf("安全解压 %s 失败: %w", asset.Name, err)
		}
	}
	serverName, cliNames, err := platformExecutableNames(manager.GOOS)
	if err != nil {
		return err
	}
	server, err := findUniqueRegularFile(extracted, serverName)
	if err != nil {
		return err
	}
	cli := ""
	for _, name := range cliNames {
		if found, findErr := findUniqueRegularFile(extracted, name); findErr == nil {
			cli = found
			break
		}
	}
	if cli == "" {
		return errors.New("解压结果中找不到唯一的 llama-cli")
	}
	if manager.GOOS == "linux" {
		if err := os.Chmod(server, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(cli, 0o755); err != nil {
			return err
		}
	}
	if _, err := VerifyInstallation(manager.Root, ResolvedPaths{Server: server}, manager.Probe); err != nil {
		return err
	}
	directoryName := sanitizeRuntimeName(release.TagName + "-" + option.ID)
	target := filepath.Join(base, directoryName)
	relative, err := filepath.Rel(manager.Root, target)
	if err != nil {
		return err
	}
	newState := UpdateState{
		Schema: UpdateStateSchema, LlamaTag: release.TagName, Backend: option.ID,
		ActiveRuntime: filepath.ToSlash(relative), Assets: installed,
		PendingCleanup: append([]string(nil), oldState.PendingCleanup...),
	}
	var backup string
	if _, err := os.Lstat(target); err == nil {
		if !force {
			return fmt.Errorf("目标运行时目录已存在: %s", target)
		}
		backup = filepath.Join(base, ".old-"+directoryName)
		backup, err = uniqueMissingPath(backup)
		if err != nil {
			return err
		}
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("无法暂存旧运行时: %w", err)
		}
	}
	if err := os.Rename(extracted, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if err := syncDirectory(base); err != nil {
		_ = os.RemoveAll(target)
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if err := WriteUpdateState(manager.Root, newState); err != nil {
		_ = os.RemoveAll(target)
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return err
	}
	cleanup := backup
	if cleanup == "" && existed {
		oldClean, _ := validateRuntimeRelativePath(oldState.ActiveRuntime, false)
		oldPath := filepath.Join(manager.Root, oldClean)
		if filepath.Clean(oldPath) != filepath.Clean(target) {
			cleanup = oldPath
		}
	}
	if cleanup != "" {
		if err := os.RemoveAll(cleanup); err != nil {
			cleanupRelative, _ := filepath.Rel(manager.Root, cleanup)
			newState.PendingCleanup = append(newState.PendingCleanup, filepath.ToSlash(cleanupRelative))
			if stateErr := WriteUpdateState(manager.Root, newState); stateErr != nil {
				return fmt.Errorf("新版本已启用，但无法记录待清理目录 %s: %w", cleanup, stateErr)
			}
			fmt.Fprintf(manager.Stderr, "警告: 旧运行时暂时无法删除，已记录待清理: %s\n", cleanup)
		}
	}
	fmt.Fprintf(manager.Stdout, "llama.cpp %s (%s) 已安装到 %s\n", release.TagName, option.ID, target)
	return nil
}

func sanitizeRuntimeName(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), ".-")
	if result == "" {
		return "runtime"
	}
	return result
}

func uniqueMissingPath(base string) (string, error) {
	for index := 0; index < 1000; index++ {
		candidate := base
		if index > 0 {
			candidate = fmt.Sprintf("%s-%d", base, index)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("无法分配临时运行时目录名")
}

func backendIDs(options []BackendOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	sort.Strings(ids)
	return ids
}
