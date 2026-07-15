package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	UpdateStateSchema  = 1
	UpdateStateName    = "update-state.json"
	ManagedRuntimeBase = "data/llama.cpp"
	maxUpdateStateSize = 1 << 20
)

type InstalledAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type UpdateState struct {
	Schema         int              `json:"schema"`
	LlamaTag       string           `json:"llama_tag"`
	Backend        string           `json:"backend"`
	ActiveRuntime  string           `json:"active_runtime"`
	Assets         []InstalledAsset `json:"assets"`
	PendingCleanup []string         `json:"pending_cleanup,omitempty"`
}

func UpdateStatePath(root string) string {
	return filepath.Join(root, ConfigDirectoryName, UpdateStateName)
}

func managedRuntimeRoot(root string) string {
	return filepath.Join(root, filepath.FromSlash(ManagedRuntimeBase))
}

func validateRuntimeRelativePath(value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" && allowEmpty {
		return "", nil
	}
	if value == "" || filepath.IsAbs(value) || isWindowsAbs(value) {
		return "", fmt.Errorf("受管运行时路径必须是相对路径: %q", value)
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	base := filepath.Clean(filepath.FromSlash(ManagedRuntimeBase))
	if clean == base || !strings.HasPrefix(clean, base+string(filepath.Separator)) {
		return "", fmt.Errorf("受管运行时路径必须位于 %s 下: %q", ManagedRuntimeBase, value)
	}
	return clean, nil
}

func validateRuntimeChildPath(value string) (string, error) {
	clean, err := validateRuntimeRelativePath(value, false)
	if err != nil {
		return "", err
	}
	base := filepath.Clean(filepath.FromSlash(ManagedRuntimeBase))
	relative, err := filepath.Rel(base, clean)
	if err != nil || relative == "." || filepath.Dir(relative) != "." {
		return "", fmt.Errorf("受管运行时必须是 %s 的直接子目录: %q", ManagedRuntimeBase, value)
	}
	return clean, nil
}

func validHistoricalRuntimeName(name string) bool {
	if name == "" || sanitizeRuntimeName(name) != name {
		return false
	}
	separator := strings.IndexByte(name, '-')
	if separator < 0 || separator == len(name)-1 {
		return false
	}
	_, err := LlamaBuildNumber(name[:separator])
	return err == nil
}

func validPendingCleanupName(name string) bool {
	if validHistoricalRuntimeName(name) {
		return true
	}
	if strings.HasPrefix(name, ".old-") {
		return validHistoricalRuntimeName(strings.TrimPrefix(name, ".old-"))
	}
	if name == ".orphan-recovery" {
		return true
	}
	if !strings.HasPrefix(name, ".orphan-recovery-") {
		return false
	}
	suffix := strings.TrimPrefix(name, ".orphan-recovery-")
	if suffix == "" {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func LoadUpdateState(root string) (UpdateState, bool, error) {
	path := UpdateStatePath(root)
	if err := validateManagedPath(root, path, "更新状态文件", true, false); err != nil {
		return UpdateState{}, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return UpdateState{}, false, nil
	}
	if err != nil {
		return UpdateState{}, false, fmt.Errorf("无法读取更新状态 %s: %w", path, err)
	}
	defer f.Close()
	if info, statErr := f.Stat(); statErr != nil {
		return UpdateState{}, false, statErr
	} else if info.Size() > maxUpdateStateSize {
		return UpdateState{}, false, fmt.Errorf("更新状态文件过大: %s", path)
	}
	var state UpdateState
	decoder := json.NewDecoder(io.LimitReader(f, maxUpdateStateSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return UpdateState{}, false, fmt.Errorf("更新状态文件损坏 %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return UpdateState{}, false, fmt.Errorf("更新状态文件包含多余内容: %s", path)
	}
	if err := ValidateUpdateState(root, state); err != nil {
		return UpdateState{}, false, err
	}
	return state, true, nil
}

func ValidateUpdateState(root string, state UpdateState) error {
	if state.Schema != UpdateStateSchema {
		return fmt.Errorf("不支持的更新状态 schema %d", state.Schema)
	}
	if strings.TrimSpace(state.LlamaTag) == "" || strings.TrimSpace(state.Backend) == "" {
		return errors.New("更新状态缺少 llama tag 或后端 ID")
	}
	if _, err := LlamaBuildNumber(state.LlamaTag); err != nil {
		return fmt.Errorf("更新状态中的 llama tag 无效: %w", err)
	}
	if sanitizeRuntimeName(state.Backend) != state.Backend {
		return fmt.Errorf("更新状态中的后端 ID 未规范化: %q", state.Backend)
	}
	if len(state.Assets) == 0 {
		return errors.New("更新状态缺少已校验资产记录")
	}
	base := managedRuntimeRoot(root)
	if err := validateManagedPath(root, base, "受管运行时根目录", true, true); err != nil {
		return err
	}
	active, err := validateRuntimeChildPath(state.ActiveRuntime)
	if err != nil {
		return err
	}
	expectedActive := filepath.Join(filepath.Clean(filepath.FromSlash(ManagedRuntimeBase)), sanitizeRuntimeName(state.LlamaTag+"-"+state.Backend))
	if active != expectedActive {
		return fmt.Errorf("活动运行时路径与 llama tag/后端不匹配: %q", state.ActiveRuntime)
	}
	seenCleanup := make(map[string]bool, len(state.PendingCleanup))
	paths := append([]string{state.ActiveRuntime}, state.PendingCleanup...)
	for index, relative := range paths {
		clean, err := validateRuntimeChildPath(relative)
		if err != nil {
			return err
		}
		if index > 0 {
			if clean == active {
				return errors.New("待清理目录不能指向活动运行时")
			}
			name := filepath.Base(clean)
			if !validPendingCleanupName(name) {
				return fmt.Errorf("待清理目录名称不属于启动器保留格式: %q", relative)
			}
			key := strings.ToLower(clean)
			if seenCleanup[key] {
				return fmt.Errorf("更新状态包含重复待清理目录: %q", relative)
			}
			seenCleanup[key] = true
		}
		absolute := filepath.Join(root, clean)
		if err := validateManagedPath(base, absolute, "受管运行时", true, true); err != nil {
			return err
		}
	}
	for _, asset := range state.Assets {
		if strings.TrimSpace(asset.Name) == "" || filepath.Base(asset.Name) != asset.Name || !validSHA256(asset.SHA256) {
			return fmt.Errorf("更新状态包含无效资产记录: %q", asset.Name)
		}
	}
	return nil
}

func WriteUpdateState(root string, state UpdateState) error {
	if err := ValidateUpdateState(root, state); err != nil {
		return err
	}
	directory := filepath.Join(root, ConfigDirectoryName)
	if err := validateManagedPath(root, directory, "配置目录", true, true); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("无法创建配置目录: %w", err)
	}
	if err := validateManagedPath(root, directory, "配置目录", false, true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(UpdateStatePath(root), data, 0o600); err != nil {
		return fmt.Errorf("无法原子写入更新状态: %w", err)
	}
	return syncDirectory(directory)
}

func ResolveManagedPaths(root, goos string, state UpdateState) (ResolvedPaths, error) {
	if err := ValidateUpdateState(root, state); err != nil {
		return ResolvedPaths{}, err
	}
	clean, _ := validateRuntimeRelativePath(state.ActiveRuntime, false)
	runtimeDir := filepath.Join(root, clean)
	if err := validateManagedPath(managedRuntimeRoot(root), runtimeDir, "活动运行时", false, true); err != nil {
		return ResolvedPaths{}, err
	}
	serverName, cliNames, err := platformExecutableNames(goos)
	if err != nil {
		return ResolvedPaths{}, err
	}
	server, err := findUniqueRegularFile(runtimeDir, serverName)
	if err != nil {
		return ResolvedPaths{}, fmt.Errorf("活动运行时无效: %w", err)
	}
	cli := ""
	for _, name := range cliNames {
		found, findErr := findUniqueRegularFile(runtimeDir, name)
		if findErr == nil {
			cli = found
			break
		}
	}
	paths, err := ResolveFixedPaths(root, goos)
	if err != nil {
		return ResolvedPaths{}, err
	}
	paths.Server, paths.CLI, paths.CLIFallback = server, cli, ""
	return paths, nil
}

func platformExecutableNames(goos string) (string, []string, error) {
	switch goos {
	case "windows":
		return "llama-server.exe", []string{"llama-cli.exe", "llama.exe"}, nil
	case "linux":
		return "llama-server", []string{"llama-cli", "llama"}, nil
	default:
		return "", nil, fmt.Errorf("当前操作系统 %q 暂不支持，仅支持 Windows 和 Linux", goos)
	}
}

func findUniqueRegularFile(root, name string) (string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("运行时不允许符号链接或重解析点: %s", path)
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), name) {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s 不是普通文件", path)
			}
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("必须且只能找到一个 %s，实际找到 %d 个", name, len(matches))
	}
	return matches[0], nil
}
