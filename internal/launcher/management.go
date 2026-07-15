package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

type componentSelection string

const (
	componentAll      componentSelection = "all"
	componentLauncher componentSelection = "launcher"
	componentLlama    componentSelection = "llama"
)

func parseComponent(value string) (componentSelection, error) {
	switch componentSelection(strings.ToLower(strings.TrimSpace(value))) {
	case componentAll, componentLauncher, componentLlama:
		return componentSelection(strings.ToLower(strings.TrimSpace(value))), nil
	default:
		return "", fmt.Errorf("component 必须是 all、launcher 或 llama")
	}
}

type updateCheckItem struct {
	Installed string `json:"installed,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Status    string `json:"status"`
	Backend   string `json:"backend,omitempty"`
}

type updateCheckResult struct {
	Schema   int              `json:"schema"`
	Launcher *updateCheckItem `json:"launcher,omitempty"`
	Llama    *updateCheckItem `json:"llama,omitempty"`
}

func runManagementCommand(ctx context.Context, manager *UpdateManager, name string, args []string, stdin io.Reader, interactiveOverride bool) (int, error) {
	switch name {
	case "install":
		set := newFlagSet("install", manager.Stderr)
		version := set.String("version", "", "llama.cpp Release tag（默认 latest stable）")
		backend := set.String("backend", "", "后端 ID")
		yes := set.Bool("yes", false, "无需确认")
		if err := set.Parse(args); err != nil {
			return 2, err
		}
		if set.NArg() != 0 {
			return 2, fmt.Errorf("无法识别的参数: %v", set.Args())
		}
		state, exists, stateErr := LoadUpdateState(manager.Root)
		repairState := stateErr != nil
		if exists {
			if _, err := ResolveManagedPaths(manager.Root, manager.GOOS, state); err == nil {
				return 1, errors.New("llama.cpp 已安装；请使用 update --component llama")
			} else {
				repairState = true
				stateErr = fmt.Errorf("活动运行时损坏: %w", err)
			}
		}
		if !exists && stateErr == nil {
			if _, err := os.Lstat(managedRuntimeRoot(manager.Root)); err == nil {
				repairState = true
				stateErr = errors.New("未找到 config/update-state.json，但受管运行时目录仍存在")
			} else if !errors.Is(err, os.ErrNotExist) {
				return 1, fmt.Errorf("无法检查受管运行时目录: %w", err)
			}
		}
		if repairState {
			fmt.Fprintf(manager.Stderr, "警告: 未找到有效的受管运行时，将在确认后隔离 data/llama.cpp 并重新安装: %v\n", stateErr)
		}
		release, err := manager.Client.Release(ctx, llamaRepository, *version)
		if err != nil {
			return 1, err
		}
		selected, err := resolveBackendInput(release, manager, *backend, stdin, interactiveOverride, false)
		if err != nil {
			return 1, err
		}
		if err := preflightLlamaAssets(manager, release, selected); err != nil {
			return 1, err
		}
		description := fmt.Sprintf("安装 llama.cpp %s (%s)", release.TagName, selected)
		if repairState {
			description += "，并将现有 data/llama.cpp 保留为恢复备份"
		}
		if err := requireConfirmation(stdin, manager.Stdout, *yes, interactiveOverride, description); err != nil {
			return 1, err
		}
		cleanupLauncherTemps(manager.Root, manager.Stderr)
		if repairState {
			return 0, installWithQuarantinedRuntime(ctx, manager, release, selected)
		}
		return 0, manager.InstallLlama(ctx, release, selected, false, false, false)
	case "check-update":
		set := newFlagSet("check-update", manager.Stderr)
		componentValue := set.String("component", "all", "all|launcher|llama")
		jsonOutput := set.Bool("json", false, "输出 JSON")
		if err := set.Parse(args); err != nil {
			return 2, err
		}
		component, err := parseComponent(*componentValue)
		if err != nil {
			return 2, err
		}
		result, err := checkUpdates(ctx, manager, component)
		if err != nil {
			return 1, err
		}
		if *jsonOutput {
			encoder := json.NewEncoder(manager.Stdout)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(result); err != nil {
				return 1, err
			}
		} else {
			printUpdateCheck(manager.Stdout, result)
		}
		return 0, nil
	case "update":
		set := newFlagSet("update", manager.Stderr)
		componentValue := set.String("component", "all", "all|launcher|llama")
		launcherVersion := set.String("launcher-version", "", "启动器 Release tag")
		llamaVersion := set.String("llama-version", "", "llama.cpp Release tag")
		backend := set.String("backend", "", "后端 ID")
		yes := set.Bool("yes", false, "无需确认")
		force := set.Bool("force", false, "强制重装同版本")
		allowDowngrade := set.Bool("allow-downgrade", false, "允许降级")
		if err := set.Parse(args); err != nil {
			return 2, err
		}
		component, err := parseComponent(*componentValue)
		if err != nil {
			return 2, err
		}
		var installedState UpdateState
		if component == componentAll || component == componentLlama {
			var exists bool
			installedState, exists, err = LoadUpdateState(manager.Root)
			if err != nil {
				return 1, err
			}
			if !exists {
				return 1, errors.New("尚未安装受管 llama.cpp；请先运行 install")
			}
			if _, err := ResolveManagedPaths(manager.Root, manager.GOOS, installedState); err != nil {
				return 1, fmt.Errorf("活动 llama.cpp 运行时损坏: %w", err)
			}
		}
		var launcherRelease, llamaRelease GitHubRelease
		selectedBackend := *backend
		if component == componentAll || component == componentLauncher {
			launcherRelease, err = manager.Client.Release(ctx, launcherRepository, *launcherVersion)
			if err != nil {
				return 1, err
			}
			archive, sums, assetErr := launcherReleaseAssets(launcherRelease, manager.GOOS, manager.GOARCH)
			if assetErr != nil {
				return 1, assetErr
			}
			assets := []GitHubAsset{archive, sums}
			if err := preflightDownloadSizes(manager.Root, assets); err != nil {
				return 1, err
			}
		}
		if component == componentAll || component == componentLlama {
			llamaRelease, err = manager.Client.Release(ctx, llamaRepository, *llamaVersion)
			if err != nil {
				return 1, err
			}
			if strings.TrimSpace(selectedBackend) == "" {
				selectedBackend = installedState.Backend
			}
			selectedBackend, err = resolveBackendInput(llamaRelease, manager, selectedBackend, stdin, interactiveOverride, strings.TrimSpace(*backend) == "")
			if err != nil {
				return 1, err
			}
			if err := preflightLlamaAssets(manager, llamaRelease, selectedBackend); err != nil {
				return 1, err
			}
		}
		description := "更新"
		if component == componentAll {
			description = fmt.Sprintf("更新 llama.cpp 到 %s (%s)，再更新启动器到 %s", llamaRelease.TagName, selectedBackend, launcherRelease.TagName)
		} else if component == componentLlama {
			description = fmt.Sprintf("更新 llama.cpp 到 %s (%s)", llamaRelease.TagName, selectedBackend)
		} else {
			description = fmt.Sprintf("更新启动器到 %s", launcherRelease.TagName)
		}
		if err := requireConfirmation(stdin, manager.Stdout, *yes, interactiveOverride, description); err != nil {
			return 1, err
		}
		cleanupLauncherTemps(manager.Root, manager.Stderr)
		if component == componentLauncher {
			if err := manager.RetryPendingCleanup(); err != nil {
				return 1, err
			}
		}
		if component == componentAll || component == componentLlama {
			if err := manager.InstallLlama(ctx, llamaRelease, selectedBackend, *force, *allowDowngrade, true); err != nil {
				return 1, err
			}
		}
		if component == componentAll || component == componentLauncher {
			if err := manager.UpdateLauncher(ctx, launcherRelease, *force, *allowDowngrade); err != nil {
				return 1, err
			}
		}
		return 0, nil
	default:
		return 2, fmt.Errorf("未知管理命令 %q", name)
	}
}

func preflightLlamaAssets(manager *UpdateManager, release GitHubRelease, backend string) error {
	options, err := ResolveLlamaAssets(release, manager.GOOS, manager.GOARCH)
	if err != nil {
		return err
	}
	option, err := SelectBackend(options, backend)
	if err != nil {
		return err
	}
	return preflightDownloadSizes(manager.Root, option.Assets)
}

func preflightDownloadSizes(path string, assets []GitHubAsset) error {
	var total int64
	for _, asset := range assets {
		if asset.Size <= 0 || asset.Size > maxAssetDownload || asset.Size > maxTotalDownload-total {
			return fmt.Errorf("资产组合下载量超过 4 GiB 或大小无效")
		}
		total += asset.Size
	}
	return ensureFreeSpace(path, total*3)
}

var removeRecoveryTree = os.RemoveAll

type installRecoveryQuarantine struct {
	stateBackup   string
	runtimeRoot   string
	runtimeOrphan string
}

const recoveryMetadataSchema = 1

type recoveryMetadata struct {
	Schema       int    `json:"schema"`
	CreatedAt    string `json:"created_at"`
	OriginalPath string `json:"original_path"`
	Reason       string `json:"reason"`
}

func installWithQuarantinedRuntime(ctx context.Context, manager *UpdateManager, release GitHubRelease, backend string) error {
	quarantine, err := createInstallRecoveryQuarantine(manager.Root)
	if err != nil {
		return err
	}
	if err := manager.InstallLlama(ctx, release, backend, false, false, false); err != nil {
		if rollbackErr := rollbackInstallRecovery(manager.Root, quarantine); rollbackErr != nil {
			return fmt.Errorf("重新安装失败: %w；同时无法完整恢复已隔离的运行时: %v", err, rollbackErr)
		}
		return err
	}
	if quarantine.runtimeOrphan != "" {
		preserved, err := preserveRecoveryRuntime(manager.Root, quarantine.runtimeOrphan)
		if err != nil {
			location := quarantine.runtimeOrphan
			if preserved != "" {
				location = preserved
			}
			fmt.Fprintf(manager.Stderr, "警告: 新运行时已启用；旧目录未删除并保留在 %s，但无法整理恢复目录: %v\n", location, err)
		} else {
			fmt.Fprintf(manager.Stdout, "旧目录未自动删除，已保留为恢复备份: %s\n", preserved)
		}
		if preserved != "" && quarantine.stateBackup != "" {
			stateDestination, stateErr := uniqueMissingPath(filepath.Join(preserved, ".update-state.json.corrupt"))
			if stateErr == nil {
				stateErr = os.Rename(quarantine.stateBackup, stateDestination)
			}
			if stateErr == nil {
				quarantine.stateBackup = ""
			} else {
				fmt.Fprintf(manager.Stderr, "警告: 无法把损坏的旧状态移入恢复备份: %v\n", stateErr)
			}
		}
	}
	if quarantine.stateBackup != "" {
		fmt.Fprintf(manager.Stderr, "警告: 损坏的旧状态未自动删除，保留在: %s\n", quarantine.stateBackup)
	}
	return nil
}

func preserveRecoveryRuntime(root, orphan string) (string, error) {
	dataDirectory := filepath.Join(root, "data")
	if err := validateManagedPath(root, dataDirectory, "恢复备份父目录", false, true); err != nil {
		return "", err
	}
	if err := validateManagedPath(managedRuntimeRoot(root), orphan, "待保留的恢复目录", false, true); err != nil {
		return "", err
	}
	destination, err := uniqueMissingPath(filepath.Join(dataDirectory, "llama.cpp-recovery"))
	if err != nil {
		return "", err
	}
	if err := os.Rename(orphan, destination); err != nil {
		return "", err
	}
	metadata := recoveryMetadata{
		Schema:       recoveryMetadataSchema,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		OriginalPath: managedRuntimeRoot(root),
		Reason:       "更新状态缺失或损坏，无法确认旧运行时中所有文件的归属",
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return destination, err
	}
	data = append(data, '\n')
	metadataPath, err := uniqueMissingPath(filepath.Join(destination, ".llamalc-recovery.json"))
	if err != nil {
		return destination, err
	}
	if err := writeFileExclusive(metadataPath, data, 0o600); err != nil {
		return destination, fmt.Errorf("无法写入恢复元数据: %w", err)
	}
	if err := syncDirectory(destination); err != nil {
		return destination, err
	}
	if err := syncDirectory(dataDirectory); err != nil {
		return destination, err
	}
	return destination, nil
}

func createInstallRecoveryQuarantine(root string) (installRecoveryQuarantine, error) {
	quarantine := installRecoveryQuarantine{runtimeRoot: managedRuntimeRoot(root)}
	statePath := UpdateStatePath(root)
	if _, err := os.Lstat(statePath); err == nil {
		if err := validateManagedPath(root, statePath, "待隔离的更新状态", false, false); err != nil {
			return quarantine, err
		}
		quarantine.stateBackup, err = uniqueMissingPath(statePath + ".corrupt")
		if err != nil {
			return quarantine, err
		}
		if err := os.Rename(statePath, quarantine.stateBackup); err != nil {
			return quarantine, fmt.Errorf("无法隔离损坏的更新状态: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return quarantine, fmt.Errorf("无法检查更新状态: %w", err)
	}

	if _, err := os.Lstat(quarantine.runtimeRoot); errors.Is(err, os.ErrNotExist) {
		return quarantine, nil
	} else if err != nil {
		primary := fmt.Errorf("无法检查受管运行时目录: %w", err)
		return quarantine, recoverySetupError(primary, restoreQuarantinedState(statePath, quarantine.stateBackup))
	}
	if err := validateRecoveryRuntimeTree(root, quarantine.runtimeRoot); err != nil {
		return quarantine, recoverySetupError(err, restoreQuarantinedState(statePath, quarantine.stateBackup))
	}

	dataDirectory := filepath.Dir(quarantine.runtimeRoot)
	external, err := uniqueMissingPath(filepath.Join(dataDirectory, ".llama.cpp.recovery"))
	if err != nil {
		return quarantine, recoverySetupError(err, restoreQuarantinedState(statePath, quarantine.stateBackup))
	}
	if err := os.Rename(quarantine.runtimeRoot, external); err != nil {
		primary := fmt.Errorf("无法隔离受管运行时: %w", err)
		return quarantine, recoverySetupError(primary, restoreQuarantinedState(statePath, quarantine.stateBackup))
	}
	if err := os.Mkdir(quarantine.runtimeRoot, 0o755); err != nil {
		primary := fmt.Errorf("无法重建受管运行时目录: %w", err)
		return quarantine, recoverySetupError(primary,
			os.Rename(external, quarantine.runtimeRoot),
			restoreQuarantinedState(statePath, quarantine.stateBackup),
		)
	}
	quarantine.runtimeOrphan, err = uniqueMissingPath(filepath.Join(quarantine.runtimeRoot, ".orphan-recovery"))
	if err == nil {
		err = os.Rename(external, quarantine.runtimeOrphan)
	}
	if err != nil {
		primary := fmt.Errorf("无法完成受管运行时隔离: %w", err)
		return quarantine, recoverySetupError(primary,
			os.Remove(quarantine.runtimeRoot),
			os.Rename(external, quarantine.runtimeRoot),
			restoreQuarantinedState(statePath, quarantine.stateBackup),
		)
	}
	return quarantine, nil
}

func rollbackInstallRecovery(root string, quarantine installRecoveryQuarantine) error {
	var rollbackErrors []error
	if quarantine.runtimeOrphan != "" {
		dataDirectory := filepath.Dir(quarantine.runtimeRoot)
		external, err := uniqueMissingPath(filepath.Join(dataDirectory, ".llama.cpp.rollback"))
		preservedAt := quarantine.runtimeOrphan
		if err == nil {
			err = os.Rename(quarantine.runtimeOrphan, external)
			if err == nil {
				preservedAt = external
			}
		}
		if err == nil {
			err = removeRecoveryTree(quarantine.runtimeRoot)
		}
		if err == nil {
			err = os.Rename(external, quarantine.runtimeRoot)
		}
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复受管运行时失败，原运行时保留在 %s: %w", preservedAt, err))
		}
	} else if err := removeRecoveryTree(quarantine.runtimeRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("清理失败的新运行时失败: %w", err))
	}

	statePath := UpdateStatePath(root)
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("清理新更新状态失败: %w", err))
	} else if quarantine.stateBackup != "" {
		if err := os.Rename(quarantine.stateBackup, statePath); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复原更新状态失败: %w", err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func restoreQuarantinedState(statePath, backup string) error {
	if backup == "" {
		return nil
	}
	return os.Rename(backup, statePath)
}

func recoverySetupError(primary error, rollbackErrors ...error) error {
	filtered := rollbackErrors[:0]
	for _, err := range rollbackErrors {
		if err != nil {
			filtered = append(filtered, err)
		}
	}
	if len(filtered) == 0 {
		return primary
	}
	return fmt.Errorf("%w；同时无法完整回滚隔离操作: %v", primary, errors.Join(filtered...))
}

func validateRecoveryRuntimeTree(root, runtimeRoot string) error {
	if err := validateManagedPath(root, runtimeRoot, "待隔离的受管运行时", false, true); err != nil {
		return err
	}
	return filepath.WalkDir(runtimeRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("待隔离的受管运行时不允许符号链接或重解析点: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("待隔离的受管运行时包含非普通文件: %s", path)
		}
		return nil
	})
}

func resolveBackendInput(release GitHubRelease, manager *UpdateManager, requested string, stdin io.Reader, interactive, reselectMissing bool) (string, error) {
	options, err := ResolveLlamaAssets(release, manager.GOOS, manager.GOARCH)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(requested) != "" {
		selected, err := SelectBackend(options, requested)
		if err == nil {
			return selected.ID, nil
		}
		if !reselectMissing || (!interactive && !isInteractiveInput(stdin)) {
			return "", err
		}
		fmt.Fprintf(manager.Stderr, "警告: 已保存后端 %q 在 Release %s 中不可用，请重新选择。\n", requested, release.TagName)
	}
	if !interactive && !isInteractiveInput(stdin) {
		return "", fmt.Errorf("必须使用 --backend 指定后端；可用值: %s", strings.Join(backendIDs(options), ", "))
	}
	fmt.Fprintln(manager.Stdout, "可用 llama.cpp 后端:")
	for index, option := range options {
		fmt.Fprintf(manager.Stdout, "  %d. %s\n", index+1, option.ID)
	}
	fmt.Fprint(manager.Stdout, "请选择后端: ")
	reader := asBufferedReader(stdin)
	var choice int
	if _, err := fmt.Fscanln(reader, &choice); err != nil || choice < 1 || choice > len(options) {
		return "", errors.New("后端选择无效")
	}
	return options[choice-1].ID, nil
}

func requireConfirmation(stdin io.Reader, stdout io.Writer, yes, interactive bool, action string) error {
	if yes {
		return nil
	}
	if !interactive && !isInteractiveInput(stdin) {
		return errors.New("非交互输入必须提供 --yes，未执行任何写入")
	}
	fmt.Fprintf(stdout, "%s，是否继续 [y/N]: ", action)
	reader := asBufferedReader(stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return errors.New("已取消，未执行任何写入")
	}
	return nil
}

func asBufferedReader(reader io.Reader) *bufio.Reader {
	if buffered, ok := reader.(*bufio.Reader); ok {
		return buffered
	}
	return bufio.NewReader(reader)
}

func isInteractiveInput(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func checkUpdates(ctx context.Context, manager *UpdateManager, component componentSelection) (updateCheckResult, error) {
	result := updateCheckResult{Schema: 1}
	if component == componentAll || component == componentLauncher {
		release, err := manager.Client.Release(ctx, launcherRepository, "")
		if err != nil {
			return result, err
		}
		if _, _, err := launcherReleaseAssets(release, manager.GOOS, manager.GOARCH); err != nil {
			return result, err
		}
		status, _ := currentLauncherStatus(release.TagName)
		result.Launcher = &updateCheckItem{Installed: buildversion.Version, Latest: release.TagName, Status: status}
	}
	if component == componentAll || component == componentLlama {
		release, err := manager.Client.Release(ctx, llamaRepository, "")
		if err != nil {
			return result, err
		}
		options, err := ResolveLlamaAssets(release, manager.GOOS, manager.GOARCH)
		if err != nil {
			return result, err
		}
		state, exists, err := LoadUpdateState(manager.Root)
		if err != nil {
			return result, err
		}
		item := &updateCheckItem{Latest: release.TagName, Status: "not-installed"}
		if exists {
			item.Installed, item.Backend = state.LlamaTag, state.Backend
			if _, backendErr := SelectBackend(options, state.Backend); backendErr != nil {
				item.Status = "backend-unavailable"
				result.Llama = item
				return result, nil
			}
			comparison, compareErr := CompareLlamaTags(state.LlamaTag, release.TagName)
			if compareErr != nil {
				item.Status = "unknown"
			} else if comparison < 0 {
				item.Status = "update-available"
			} else if comparison > 0 {
				item.Status = "newer"
			} else {
				item.Status = "current"
			}
		}
		result.Llama = item
	}
	return result, nil
}

func printUpdateCheck(writer io.Writer, result updateCheckResult) {
	if result.Launcher != nil {
		fmt.Fprintf(writer, "启动器: 已安装 %s，最新 %s，状态 %s\n", result.Launcher.Installed, result.Launcher.Latest, result.Launcher.Status)
	}
	if result.Llama != nil {
		fmt.Fprintf(writer, "llama.cpp: 已安装 %s，最新 %s，后端 %s，状态 %s\n", result.Llama.Installed, result.Llama.Latest, result.Llama.Backend, result.Llama.Status)
	}
}
