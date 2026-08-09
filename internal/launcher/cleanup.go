package launcher

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type cleanupCandidate struct {
	Path      string
	Kind      string
	Reason    string
	Size      int64
	SizeKnown bool
	Automatic bool
	Recent    bool
	Identity  os.FileInfo
	Snapshot  string
}

func scanCleanupCandidates(root string) ([]cleanupCandidate, []string) {
	var candidates []cleanupCandidate
	var warnings []string
	appendCandidate := func(path, kind, reason string, automatic bool) {
		identity, identityErr := os.Lstat(path)
		if identityErr != nil {
			warnings = append(warnings, fmt.Sprintf("无法检查 %s: %v", path, identityErr))
			return
		}
		size, snapshot, err := inspectCleanupPath(path)
		candidate := cleanupCandidate{Path: filepath.Clean(path), Kind: kind, Reason: reason, Automatic: automatic, Identity: identity}
		if automatic && kind != "临时 updater 副本" && !oldEnoughForAutomaticCleanupPath(path, identity) {
			candidate.Kind = "近期" + kind
			candidate.Reason = fmt.Sprintf("最近 %s内创建或修改，可能仍被其他 launcher 使用，暂不允许删除", formatCleanupDuration(automaticCleanupMinAge))
			candidate.Automatic = false
			candidate.Recent = true
		}
		if err == nil {
			candidate.Size, candidate.SizeKnown, candidate.Snapshot = size, true, snapshot
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
			runningUpdaterTemp := isRunningUpdaterTemp(name)
			executableTemp := runningUpdaterTemp || numericTempSuffix(name, ".llama-launcher-new-", "") ||
				numericTempSuffix(name, ".llama-launcher-new-", ".exe") ||
				isStagedUpdaterTemp(name)
			if executableTemp {
				if entry.Type().IsRegular() {
					kind := "启动器更新残留"
					reason := "更新交接中断留下的严格命名临时程序"
					if runningUpdaterTemp {
						kind = "临时 updater 副本"
						reason = "Windows 更新交接完成后由新版 launcher 清理的运行副本"
					}
					appendCandidate(path, kind, reason, true)
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
			if clean == active {
				continue
			}
			if pending[clean] {
				if entry.IsDir() && validateManagedPath(base, path, "已登记待清理运行时", false, true) == nil {
					appendCandidate(path, "已登记待清理运行时", "更新状态明确登记的旧运行时或恢复暂存", true)
				} else {
					appendCandidate(path, "异常待清理路径", "状态文件登记为待清理目录，但当前文件类型不安全", false)
				}
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

func formatCleanupDuration(value time.Duration) string {
	if value > 0 && value%time.Hour == 0 {
		return fmt.Sprintf("%d 小时", int64(value/time.Hour))
	}
	if value > 0 && value%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int64(value/time.Minute))
	}
	return value.String()
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
	suffix := strings.TrimPrefix(name, "llama.cpp-recovery-")
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

func recoveryReason(path string) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "状态损坏修复时保留的旧目录"
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".llamalc-recovery.json") || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() < 0 || info.Size() > 64<<10 {
			continue
		}
		file, err := os.Open(filepath.Join(path, entry.Name()))
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > 64<<10 {
			continue
		}
		var metadata recoveryMetadata
		if json.Unmarshal(data, &metadata) == nil && metadata.Schema == recoveryMetadataSchema && strings.TrimSpace(metadata.Reason) != "" {
			reason := safeTerminalText(metadata.Reason)
			runes := []rune(reason)
			if len(runes) > 512 {
				reason = string(runes[:512]) + "…"
			}
			if createdAt, parseErr := time.Parse(time.RFC3339, metadata.CreatedAt); parseErr == nil {
				return reason + "；创建时间 " + createdAt.UTC().Format(time.RFC3339)
			}
			return reason
		}
	}
	return "状态损坏修复时保留的旧目录（没有可用元数据）"
}

func cleanupPathSize(path string) (int64, error) {
	size, _, err := inspectCleanupPath(path)
	return size, err
}

func inspectCleanupPath(path string) (int64, string, error) {
	var total int64
	hash := sha256.New()
	root := filepath.Clean(path)
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
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
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\n",
			filepath.ToSlash(relative), info.Mode().String(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return total, "", err
	}
	return total, fmt.Sprintf("%x", hash.Sum(nil)), nil
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
	if selected.Identity == nil || current.Identity == nil || !os.SameFile(selected.Identity, current.Identity) {
		return errors.New("目标已被替换，请刷新并重新检查后再操作")
	}
	if selected.Snapshot != current.Snapshot {
		return errors.New("目标目录结构或内容已经变化，请刷新并重新检查后再操作")
	}
	if automatic && !current.Automatic {
		return errors.New("目标不再满足自动清理条件")
	}
	if current.Recent {
		return fmt.Errorf("目标在最近 %s内创建或修改，可能仍在使用，拒绝删除", formatCleanupDuration(automaticCleanupMinAge))
	}
	_, finalSnapshot, err := inspectCleanupPath(current.Path)
	if err != nil {
		return fmt.Errorf("拒绝删除无法安全遍历的目标: %w", err)
	}
	if finalSnapshot != current.Snapshot {
		return errors.New("目标在最终检查时发生变化，拒绝删除")
	}
	info, err := os.Lstat(current.Path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("拒绝删除符号链接或重解析点")
	}
	if !os.SameFile(current.Identity, info) {
		return errors.New("目标在最终检查时已被替换，拒绝删除")
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
	if err := syncDirectory(filepath.Dir(current.Path)); err != nil {
		return err
	}
	if current.Kind == "已登记待清理运行时" {
		if err := clearPendingCleanupEntry(root, current.Path); err != nil {
			return fmt.Errorf("目录已删除，但无法更新待清理状态: %w", err)
		}
	}
	return nil
}

func clearPendingCleanupEntry(root, path string) error {
	state, exists, err := LoadUpdateState(root)
	if err != nil || !exists {
		return err
	}
	remaining := state.PendingCleanup[:0]
	for _, relative := range state.PendingCleanup {
		clean, cleanErr := validateRuntimeChildPath(relative)
		if cleanErr != nil || filepath.Clean(filepath.Join(root, clean)) != filepath.Clean(path) {
			remaining = append(remaining, relative)
		}
	}
	state.PendingCleanup = remaining
	return WriteUpdateState(root, state)
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
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

var launchCleanupPath = openCleanupPath

func cleanupSizeDisplay(candidate cleanupCandidate) string {
	if !candidate.SizeKnown {
		return "大小未知"
	}
	return formatSize(candidate.Size)
}
