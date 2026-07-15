package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type cleanupCandidate struct {
	Path      string
	Kind      string
	Reason    string
	Size      int64
	SizeKnown bool
	Automatic bool
}

func scanCleanupCandidates(root string) ([]cleanupCandidate, []string) {
	var candidates []cleanupCandidate
	var warnings []string
	appendCandidate := func(path, kind, reason string, automatic bool) {
		size, err := cleanupPathSize(path)
		candidate := cleanupCandidate{Path: filepath.Clean(path), Kind: kind, Reason: reason, Automatic: automatic}
		if err == nil {
			candidate.Size, candidate.SizeKnown = size, true
		} else {
			warnings = append(warnings, fmt.Sprintf("无法完整统计 %s: %v", path, err))
		}
		candidates = append(candidates, candidate)
	}

	config := filepath.Join(root, ConfigDirectoryName)
	if entries, err := readManagedDirectory(root, config, "配置目录"); err == nil {
		prefixes := []string{
			"." + DefaultConfigName + ".tmp-",
			"." + DefaultAPIKeyName + ".tmp-",
			"." + UpdateStateName + ".tmp-",
			".router-models.ini.tmp-",
			".router-models.auto.ini.tmp-",
		}
		for _, entry := range entries {
			matched := false
			for _, prefix := range prefixes {
				if numericTempSuffix(entry.Name(), prefix, "") {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			path := filepath.Join(config, entry.Name())
			if entry.Type().IsRegular() {
				appendCandidate(path, "配置写入残留", "原子写入中断留下的临时文件", true)
			} else {
				appendCandidate(path, "未验证配置残留", "名称类似临时文件，但不是启动器可自动删除的普通文件", false)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, err.Error())
	}

	bin := filepath.Join(root, "bin")
	if entries, err := readManagedDirectory(root, bin, "启动器目录"); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(bin, name)
			executableTemp := numericTempSuffix(name, ".llama-updater-run-", "") ||
				numericTempSuffix(name, ".llama-updater-run-", ".exe") ||
				numericTempSuffix(name, ".llama-launcher-new-", "") ||
				numericTempSuffix(name, ".llama-launcher-new-", ".exe") ||
				numericTempSuffix(name, ".llama-updater-new-", "") ||
				numericTempSuffix(name, ".llama-updater-new-", ".exe")
			if executableTemp {
				if entry.Type().IsRegular() {
					appendCandidate(path, "启动器更新残留", "更新交接中断留下的严格命名临时程序", true)
				} else {
					appendCandidate(path, "未验证启动器残留", "名称类似更新临时程序，但不是普通文件", false)
				}
				continue
			}
			if numericTempSuffix(name, ".launcher-update-", "") {
				if validateMarkedTempDirectory(bin, path) == nil {
					appendCandidate(path, "启动器下载暂存", "含有效 launcher 所有权标记", true)
				} else {
					appendCandidate(path, "未验证启动器暂存", "缺少有效所有权标记，需要人工检查", false)
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, err.Error())
	}

	active, pending := knownRuntimePaths(root, &warnings)
	base := managedRuntimeRoot(root)
	if entries, err := readManagedDirectory(root, base, "受管运行时根目录"); err == nil {
		for _, entry := range entries {
			path := filepath.Join(base, entry.Name())
			clean := filepath.Clean(path)
			if clean == active || pending[clean] {
				continue
			}
			if numericTempSuffix(entry.Name(), ".staging-", "") {
				if validateMarkedTempDirectory(base, path) == nil {
					appendCandidate(path, "运行时下载暂存", "含有效 launcher 所有权标记", true)
				} else {
					appendCandidate(path, "未验证运行时暂存", "缺少有效所有权标记，需要人工检查", false)
				}
				continue
			}
			if entry.IsDir() {
				appendCandidate(path, "未登记运行时目录", "不属于当前活动运行时，也未登记为待清理目录", false)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, err.Error())
	}

	data := filepath.Join(root, "data")
	if entries, err := readManagedDirectory(root, data, "数据目录"); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !recoveryDirectoryName(entry.Name()) {
				continue
			}
			path := filepath.Join(data, entry.Name())
			reason := recoveryReason(path)
			appendCandidate(path, "恢复备份", reason, false)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, err.Error())
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Automatic != candidates[j].Automatic {
			return candidates[i].Automatic
		}
		return strings.ToLower(candidates[i].Path) < strings.ToLower(candidates[j].Path)
	})
	return candidates, warnings
}

func readManagedDirectory(root, path, label string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s不是普通目录: %s", label, path)
	}
	if err := validateManagedPath(root, path, label, false, true); err != nil {
		return nil, err
	}
	return os.ReadDir(path)
}

func knownRuntimePaths(root string, warnings *[]string) (string, map[string]bool) {
	pending := make(map[string]bool)
	state, exists, err := LoadUpdateState(root)
	if err != nil {
		*warnings = append(*warnings, "更新状态不可用，运行时目录将全部按需人工检查: "+err.Error())
		return "", pending
	}
	if !exists {
		return "", pending
	}
	activeClean, _ := validateRuntimeChildPath(state.ActiveRuntime)
	active := filepath.Clean(filepath.Join(root, activeClean))
	for _, relative := range state.PendingCleanup {
		clean, _ := validateRuntimeChildPath(relative)
		pending[filepath.Clean(filepath.Join(root, clean))] = true
	}
	return active, pending
}

func recoveryDirectoryName(name string) bool {
	if name == "llama.cpp-recovery" {
		return true
	}
	if !strings.HasPrefix(name, "llama.cpp-recovery-") {
		return false
	}
	_, err := strconv.Atoi(strings.TrimPrefix(name, "llama.cpp-recovery-"))
	return err == nil
}

func recoveryReason(path string) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "状态损坏修复时保留的旧目录"
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".llamalc-recovery.json") || !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil || len(data) > 64<<10 {
			continue
		}
		var metadata recoveryMetadata
		if json.Unmarshal(data, &metadata) == nil && metadata.Schema == recoveryMetadataSchema && metadata.Reason != "" {
			if metadata.CreatedAt != "" {
				return metadata.Reason + "；创建时间 " + metadata.CreatedAt
			}
			return metadata.Reason
		}
	}
	return "状态损坏修复时保留的旧目录（没有可用元数据）"
}

func cleanupPathSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("包含符号链接或重解析点")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("包含特殊文件")
		}
		if info.Mode().IsRegular() {
			if info.Size() > int64(^uint64(0)>>1)-total {
				return errors.New("目录大小溢出")
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func deleteCleanupCandidate(root string, selected cleanupCandidate, automatic bool) error {
	candidates, _ := scanCleanupCandidates(root)
	var current *cleanupCandidate
	for index := range candidates {
		if filepath.Clean(candidates[index].Path) == filepath.Clean(selected.Path) && candidates[index].Kind == selected.Kind {
			current = &candidates[index]
			break
		}
	}
	if current == nil {
		return errors.New("目录状态已经变化，请刷新后重试")
	}
	if automatic && !current.Automatic {
		return errors.New("目标不再满足自动清理条件")
	}
	if _, err := cleanupPathSize(current.Path); err != nil {
		return fmt.Errorf("拒绝删除无法安全遍历的目标: %w", err)
	}
	info, err := os.Lstat(current.Path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("拒绝删除符号链接或重解析点")
	}
	if info.IsDir() {
		if err := os.RemoveAll(current.Path); err != nil {
			return err
		}
	} else if info.Mode().IsRegular() {
		if err := os.Remove(current.Path); err != nil {
			return err
		}
	} else {
		return errors.New("拒绝删除特殊文件")
	}
	return syncDirectory(filepath.Dir(current.Path))
}

func openCleanupPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer.exe", path)
	case "linux":
		command = exec.Command("xdg-open", path)
	default:
		return fmt.Errorf("当前系统不支持自动打开目录: %s", path)
	}
	return command.Start()
}

var launchCleanupPath = openCleanupPath

func cleanupSizeDisplay(candidate cleanupCandidate) string {
	if !candidate.SizeKnown {
		return "大小未知"
	}
	return formatSize(candidate.Size)
}
