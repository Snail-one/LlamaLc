package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
		if repairState {
			statePath := UpdateStatePath(manager.Root)
			if fileErr := validateManagedPath(manager.Root, statePath, "损坏的更新状态", false, false); fileErr != nil {
				return 1, stateErr
			}
			fmt.Fprintf(manager.Stderr, "警告: 更新状态损坏，将在确认后隔离并重新安装: %v\n", stateErr)
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
		if err := requireConfirmation(stdin, manager.Stdout, *yes, interactiveOverride, fmt.Sprintf("安装 llama.cpp %s (%s)", release.TagName, selected)); err != nil {
			return 1, err
		}
		cleanupLauncherTemps(manager.Root, manager.Stderr)
		if repairState {
			return 0, installWithQuarantinedState(ctx, manager, release, selected)
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
			if err := preflightDownloadSizes(manager.Root, []GitHubAsset{archive, sums}); err != nil {
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

func installWithQuarantinedState(ctx context.Context, manager *UpdateManager, release GitHubRelease, backend string) error {
	statePath := UpdateStatePath(manager.Root)
	backup, err := uniqueMissingPath(statePath + ".corrupt")
	if err != nil {
		return err
	}
	if err := os.Rename(statePath, backup); err != nil {
		return fmt.Errorf("无法隔离损坏的更新状态: %w", err)
	}
	if err := manager.InstallLlama(ctx, release, backend, false, false, false); err != nil {
		_ = os.Rename(backup, statePath)
		return err
	}
	if err := os.Remove(backup); err != nil {
		fmt.Fprintf(manager.Stderr, "警告: 无法删除已隔离的旧状态 %s: %v\n", backup, err)
	}
	return nil
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
