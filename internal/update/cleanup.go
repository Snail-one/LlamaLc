package update

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

type CleanupCandidate struct {
	Path, Kind, Reason string
	Warning            bool
	Size               int64
	SizeKnown          bool
	Automatic          bool
	Recent             bool
	Snapshot           string
}

const automaticCleanupMinAge = 24 * time.Hour

func CleanupCandidates(l layout.Layout) ([]CleanupCandidate, error) {
	var out []CleanupCandidate
	appendCandidate := func(path, kind, reason string, automatic bool) {
		info, err := os.Lstat(path)
		if err != nil {
			return
		}
		candidate := CleanupCandidate{Path: filepath.Clean(path), Kind: kind, Reason: reason, Automatic: automatic}
		size, snapshot, inspectErr := inspectCleanupPath(path)
		if inspectErr == nil {
			candidate.Size, candidate.SizeKnown, candidate.Snapshot = size, true, snapshot
		} else if automatic {
			candidate.Automatic = false
			candidate.Reason += "；无法完整验证内容，不能批量清理"
		}
		if candidate.Automatic && kind != "updater 运行副本" && kind != "已登记待清理运行时" && !oldEnoughForAutomaticCleanup(path, info) {
			candidate.Automatic = false
			candidate.Recent = true
			candidate.Reason += "；最近 24 小时内创建或修改，可能仍在使用"
		}
		out = append(out, candidate)
	}
	s, _, stateErr := LoadState(l)
	if stateErr != nil {
		s = State{Schema: StateSchema}
		if _, statErr := os.Lstat(l.UpdateStateFile); statErr == nil {
			size, snapshot, _ := inspectCleanupPath(l.UpdateStateFile)
			out = append(out, CleanupCandidate{Path: l.UpdateStateFile, Kind: "扫描警告", Reason: "更新状态损坏；所有运行时和恢复目录均需人工确认: " + stateErr.Error(), Warning: true, Size: size, SizeKnown: snapshot != "", Snapshot: snapshot})
		}
	}
	for _, relative := range s.PendingCleanup {
		path := filepath.Join(l.Root, filepath.FromSlash(relative))
		appendCandidate(path, "已登记待清理运行时", "更新状态登记的非活动运行时或同版本重装备份", true)
	}
	if entries, readErr := os.ReadDir(l.Bin); readErr == nil {
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(l.Bin, entry.Name())
			switch {
			case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validRunnerName(name):
				appendCandidate(path, "updater 运行副本", "更新交接后遗留的严格命名副本", true)
			case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && (validProgramTempName(name, ".llamalc-new-") || validProgramTempName(name, ".llamaup-new-")):
				appendCandidate(path, "启动器更新残留", "更新交接中断留下的暂存程序", true)
			case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && (validProgramTempName(name, ".llamalc-rollback-") || validProgramTempName(name, ".llamaup-rollback-")):
				appendCandidate(path, "启动器更新恢复备份", "双文件更新回滚时保留的原程序，需要人工确认", false)
			}
		}
	}
	for _, directory := range []string{l.ConfigDir, l.SecretsDir, l.StateDir, l.RouterConfigDir, l.RouterStateDir} {
		if entries, readErr := os.ReadDir(directory); readErr == nil {
			for _, entry := range entries {
				name := entry.Name()
				if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validAtomicTempName(name) {
					appendCandidate(filepath.Join(directory, entry.Name()), "配置写入残留", "原子写入中断留下的临时文件", true)
				}
			}
		}
	}
	if entries, readErr := os.ReadDir(l.RuntimeDir); readErr == nil {
		for _, entry := range entries {
			path := filepath.Join(l.RuntimeDir, entry.Name())
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".llama-lock-") {
				if validOwnedTempDirectory(path, ".llama-lock-", "llama-global-install-lock") {
					appendCandidate(path, "llama.cpp 更新锁残留", "llama.cpp 更新中断留下的全局锁", true)
				}
			} else if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-lock-") {
				if validOwnedTempDirectory(path, ".launcher-lock-", "launcher-install-lock") {
					appendCandidate(path, "启动器更新锁残留", "启动器交接中断留下的更新锁", true)
				}
			} else if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-update-") {
				if validOwnedTempDirectory(path, ".launcher-update-", "launcher-update-staging") {
					appendCandidate(path, "启动器下载暂存", "启动器下载或校验中断留下的目录", true)
				}
			}
		}
	}
	if entries, readErr := os.ReadDir(l.LlamaRuntimeDir); readErr == nil {
		known := make(map[string]bool)
		if s.ActiveRuntime != "" {
			known[filepath.Clean(filepath.Join(l.Root, filepath.FromSlash(s.ActiveRuntime)))] = true
		}
		for _, pending := range s.PendingCleanup {
			known[filepath.Clean(filepath.Join(l.Root, filepath.FromSlash(pending)))] = true
		}
		for _, entry := range entries {
			if strings.HasPrefix(strings.ToLower(entry.Name()), ".install-lock-") {
				path := filepath.Join(l.LlamaRuntimeDir, entry.Name())
				if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validOwnedTempDirectory(path, ".install-lock-", "llama-install-lock") {
					appendCandidate(path, "运行时安装锁残留", "llama.cpp 安装中断留下的目标锁", true)
				}
				continue
			}
			if strings.HasPrefix(strings.ToLower(entry.Name()), ".staging-") {
				if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validOwnedTempDirectory(filepath.Join(l.LlamaRuntimeDir, entry.Name()), ".staging-", "llama-runtime-staging") {
					appendCandidate(filepath.Join(l.LlamaRuntimeDir, entry.Name()), "运行时下载暂存", "llama.cpp 下载或解压中断留下的目录", true)
				}
				continue
			}
			backendPath := filepath.Join(l.LlamaRuntimeDir, entry.Name())
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				appendCandidate(backendPath, "异常运行时项目", "运行时根目录中的项目不符合 <backend>/<version> 布局", false)
				continue
			}
			versions, versionErr := os.ReadDir(backendPath)
			if versionErr != nil {
				continue
			}
			for _, version := range versions {
				path := filepath.Join(backendPath, version.Name())
				if known[filepath.Clean(path)] {
					continue
				}
				reason := "不属于当前活动运行时，也未登记为待清理目录"
				if stateErr != nil {
					reason = "扫描警告: 更新状态损坏；" + reason
				}
				appendCandidate(path, "未登记运行时目录", reason, false)
			}
		}
	}
	if entries, readErr := os.ReadDir(l.RecoveryDir); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				path := filepath.Join(l.RecoveryDir, entry.Name())
				reason := recoveryReason(path)
				if stateErr != nil {
					reason = "扫描警告: 更新状态损坏；" + reason
				}
				appendCandidate(path, "恢复备份", reason, false)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Automatic != out[j].Automatic {
			return out[i].Automatic
		}
		return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
	})
	return out, nil
}

// HousekeepingResult reports best-effort startup cleanup. Failures never make
// an otherwise valid command fail; callers should surface Warnings so the same
// items can be retried on the next launch.
type HousekeepingResult struct {
	Removed  []string
	Warnings []error
}

// StartupHousekeeping performs the deliberately small automatic cleanup set.
// It does not enumerate recovery backups, unknown runtimes, legacy layouts, or
// unmarked directories.
func StartupHousekeeping(l layout.Layout) HousekeepingResult {
	var result HousekeepingResult
	removeFile := func(path string, immediate bool) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
		if !immediate && !oldEnoughForAutomaticCleanup(path, info) {
			return
		}
		_, snapshot, err := inspectCleanupPath(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("复核清理目标 %s: %w", path, err))
			return
		}
		if err := removeAutomaticCleanupPath(l, path, info, snapshot); err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("清理 %s: %w", path, err))
			return
		}
		result.Removed = append(result.Removed, path)
	}

	if state, exists, err := LoadState(l); err != nil {
		if _, statErr := os.Lstat(l.UpdateStateFile); statErr == nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("读取 pending_cleanup: %w", err))
		}
	} else if exists && len(state.PendingCleanup) > 0 {
		remaining := make([]string, 0, len(state.PendingCleanup))
		changed := false
		for _, relative := range state.PendingCleanup {
			path := filepath.Join(l.Root, filepath.FromSlash(relative))
			if err := removeManagedRuntime(l, path); err != nil {
				remaining = append(remaining, relative)
				result.Warnings = append(result.Warnings, fmt.Errorf("清理 pending_cleanup %s: %w", path, err))
				continue
			}
			changed = true
			result.Removed = append(result.Removed, path)
		}
		if changed {
			state.PendingCleanup = remaining
			if err := SaveState(l, state); err != nil {
				result.Warnings = append(result.Warnings, fmt.Errorf("保存 pending_cleanup: %w", err))
			}
		}
	}

	if entries, err := os.ReadDir(l.Bin); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(l.Bin, entry.Name())
			switch {
			case validRunnerName(name):
				removeFile(path, true)
			case validProgramTempName(name, ".llamalc-new-") || validProgramTempName(name, ".llamaup-new-"):
				removeFile(path, false)
			}
		}
	}
	for _, directory := range []string{l.ConfigDir, l.SecretsDir, l.StateDir, l.RouterConfigDir, l.RouterStateDir} {
		if entries, err := os.ReadDir(directory); err == nil {
			for _, entry := range entries {
				if validAtomicTempName(entry.Name()) {
					removeFile(filepath.Join(directory, entry.Name()), false)
				}
			}
		}
	}
	removeOwnedDirectory := func(path, prefix, kind string) {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !validOwnedTempDirectory(path, prefix, kind) || !oldEnoughForAutomaticCleanup(path, info) {
			return
		}
		_, snapshot, err := inspectCleanupPath(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("复核清理目标 %s: %w", path, err))
			return
		}
		if err := removeAutomaticCleanupPath(l, path, info, snapshot); err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("清理 %s: %w", path, err))
			return
		}
		result.Removed = append(result.Removed, path)
	}
	if entries, err := os.ReadDir(l.RuntimeDir); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(strings.ToLower(entry.Name()), ".llama-lock-") {
				removeOwnedDirectory(filepath.Join(l.RuntimeDir, entry.Name()), ".llama-lock-", "llama-global-install-lock")
			} else if strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-lock-") {
				removeOwnedDirectory(filepath.Join(l.RuntimeDir, entry.Name()), ".launcher-lock-", "launcher-install-lock")
			} else if strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-update-") {
				removeOwnedDirectory(filepath.Join(l.RuntimeDir, entry.Name()), ".launcher-update-", "launcher-update-staging")
			}
		}
	}
	if entries, err := os.ReadDir(l.LlamaRuntimeDir); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(strings.ToLower(entry.Name()), ".install-lock-") {
				removeOwnedDirectory(filepath.Join(l.LlamaRuntimeDir, entry.Name()), ".install-lock-", "llama-install-lock")
			} else if strings.HasPrefix(strings.ToLower(entry.Name()), ".staging-") {
				removeOwnedDirectory(filepath.Join(l.LlamaRuntimeDir, entry.Name()), ".staging-", "llama-runtime-staging")
			}
		}
	}
	sort.Strings(result.Removed)
	return result
}

// removeAutomaticCleanupPath prevents a validated cleanup path from being
// swapped between inspection and deletion. The exact inode is first moved to
// a private sibling name, then its identity and complete snapshot are checked
// again before anything is removed.
func removeAutomaticCleanupPath(l layout.Layout, path string, expected os.FileInfo, expectedSnapshot string) (err error) {
	if expectedSnapshot == "" {
		return errors.New("缺少清理目标快照")
	}
	if err = managedfs.Within(l.Root, path); err != nil {
		return err
	}
	if err = managedfs.Validate(l.Root, path, false); err != nil {
		return err
	}
	quarantine := filepath.Join(filepath.Dir(path), ".llamalc-delete-"+randomToken())
	if err = managedfs.Within(l.Root, quarantine); err != nil {
		return err
	}
	if err = os.Rename(path, quarantine); err != nil {
		return fmt.Errorf("隔离清理目标: %w", err)
	}
	restore := true
	defer func() {
		if !restore {
			return
		}
		if restoreErr := os.Rename(quarantine, path); restoreErr != nil {
			if err == nil {
				err = fmt.Errorf("恢复隔离目标: %w", restoreErr)
			} else {
				err = fmt.Errorf("%v；恢复隔离目标失败: %w", err, restoreErr)
			}
		}
	}()
	movedInfo, statErr := os.Lstat(quarantine)
	if statErr != nil || !os.SameFile(expected, movedInfo) {
		return errors.New("清理目标文件身份在隔离时发生变化")
	}
	_, movedSnapshot, inspectErr := inspectCleanupPath(quarantine)
	if inspectErr != nil || movedSnapshot != expectedSnapshot {
		return errors.New("清理目标隔离后的最终快照不一致")
	}
	if movedInfo.IsDir() {
		err = os.RemoveAll(quarantine)
	} else {
		err = os.Remove(quarantine)
	}
	if err != nil {
		return err
	}
	restore = false
	if _, statErr = os.Lstat(quarantine); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("清理隔离路径仍然存在，最终状态复检失败")
	}
	if _, statErr = os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("原路径在清理期间被重新创建，最终状态复检失败")
	}
	return syncParentDirectory(filepath.Dir(path))
}

func recoveryReason(path string) string {
	metadataPath := filepath.Join(path, ".llamalc-recovery.json")
	file, err := os.Open(metadataPath)
	if err != nil {
		return "更新、状态修复或回滚保留的恢复内容（没有可用元数据）"
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 64<<10 {
		return "更新、状态修复或回滚保留的恢复内容（元数据不可用）"
	}
	var metadata recoveryMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.Schema != 1 || strings.TrimSpace(metadata.Reason) == "" {
		return "更新、状态修复或回滚保留的恢复内容（元数据无效）"
	}
	reason := metadata.Reason
	runes := []rune(reason)
	if len(runes) > 512 {
		reason = string(runes[:512]) + "…"
	}
	if created, err := time.Parse(time.RFC3339, metadata.CreatedAt); err == nil {
		return reason + "；创建时间 " + created.UTC().Format(time.RFC3339)
	}
	return reason
}

func validRunnerName(name string) bool {
	if name != strings.ToLower(name) {
		return false
	}
	value := strings.TrimSuffix(strings.ToLower(name), ".exe")
	suffix := strings.TrimPrefix(value, ".llamaup-run-")
	if suffix == value || len(suffix) != 16 {
		return false
	}
	for _, character := range suffix {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validProgramTempName(name, prefix string) bool {
	if name != strings.ToLower(name) {
		return false
	}
	value := strings.TrimSuffix(strings.ToLower(name), ".exe")
	token := strings.TrimPrefix(value, prefix)
	return token != value && len(token) == 16 && safeHex(token)
}

func validAtomicTempName(name string) bool {
	if name != strings.ToLower(name) {
		return false
	}
	index := strings.LastIndex(strings.ToLower(name), ".tmp-")
	if index <= 1 {
		return false
	}
	token := name[index+5:]
	return len(token) == 16 && safeHex(strings.ToLower(token))
}

func validOwnedTempDirectory(path, prefix, kind string) bool {
	base := filepath.Base(path)
	if base != strings.ToLower(base) {
		return false
	}
	name := strings.ToLower(base)
	token := strings.TrimPrefix(name, prefix)
	if token == name || len(token) != 16 || !safeHex(token) {
		return false
	}
	file, err := os.Open(filepath.Join(path, ownershipMarkerName))
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return false
	}
	var marker ownershipMarker
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false
	}
	return marker.Schema == 1 && marker.Token == token && marker.Kind == kind
}

func DeleteCandidate(l layout.Layout, c CleanupCandidate) error {
	if c.Warning {
		return errors.New("扫描警告项目不可删除")
	}
	current, err := CleanupCandidates(l)
	if err != nil {
		return err
	}
	var found *CleanupCandidate
	for _, x := range current {
		if filepath.Clean(x.Path) == filepath.Clean(c.Path) && x.Kind == c.Kind {
			copy := x
			found = &copy
			break
		}
	}
	if found == nil {
		return errors.New("清理目标状态已变化，请刷新")
	}
	if c.Recent || found.Recent {
		return errors.New("项目最近仍有变化，可能正在使用，拒绝删除")
	}
	if c.Snapshot == "" || found.Snapshot == "" || c.Snapshot != found.Snapshot {
		return errors.New("清理目标内容在扫描后发生变化，拒绝删除")
	}
	if err = managedfs.Within(l.Root, c.Path); err != nil {
		return err
	}
	if err = managedfs.Validate(l.Root, c.Path, false); err != nil {
		return err
	}
	info, err := os.Lstat(c.Path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("拒绝清理符号链接")
	}
	quarantine := filepath.Join(filepath.Dir(c.Path), ".llamalc-delete-"+randomToken())
	if err = managedfs.Within(l.Root, quarantine); err != nil {
		return err
	}
	if err = os.Rename(c.Path, quarantine); err != nil {
		return fmt.Errorf("隔离清理目标 %s: %w", c.Path, err)
	}
	restore := true
	defer func() {
		if restore {
			_ = os.Rename(quarantine, c.Path)
		}
	}()
	quarantineInfo, statErr := os.Lstat(quarantine)
	if statErr != nil || !os.SameFile(info, quarantineInfo) {
		return errors.New("清理目标文件身份在隔离时发生变化")
	}
	_, quarantineSnapshot, inspectErr := inspectCleanupPath(quarantine)
	if inspectErr != nil || quarantineSnapshot != c.Snapshot {
		return errors.New("清理目标隔离后的最终快照不一致")
	}
	if quarantineInfo.IsDir() {
		err = os.RemoveAll(quarantine)
	} else {
		err = os.Remove(quarantine)
	}
	if err != nil {
		return fmt.Errorf("清理 %s: %w", c.Path, err)
	}
	restore = false
	if _, statErr := os.Lstat(quarantine); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("清理隔离路径仍然存在，最终状态复检失败")
	}
	if _, statErr := os.Lstat(c.Path); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("原路径在清理期间被重新创建，最终状态复检失败")
	}
	if err = syncParentDirectory(filepath.Dir(c.Path)); err != nil {
		return fmt.Errorf("同步清理目录: %w", err)
	}
	if c.Kind == "已登记待清理运行时" {
		s, exists, e := LoadState(l)
		if e != nil {
			return fmt.Errorf("目标已删除，但无法读取 pending_cleanup: %w", e)
		}
		if !exists {
			return errors.New("目标已删除，但更新状态已不存在")
		}
		if exists {
			var pending []string
			for _, p := range s.PendingCleanup {
				if filepath.Clean(filepath.Join(l.Root, filepath.FromSlash(p))) != filepath.Clean(c.Path) {
					pending = append(pending, p)
				}
			}
			s.PendingCleanup = pending
			if saveErr := SaveState(l, s); saveErr != nil {
				return fmt.Errorf("目标已删除，但无法更新 pending_cleanup: %w", saveErr)
			}
		}
	}
	return nil
}

func syncParentDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func oldEnoughForAutomaticCleanup(path string, rootInfo os.FileInfo) bool {
	cutoff := time.Now().Add(-automaticCleanupMinAge)
	if rootInfo.ModTime().After(cutoff) {
		return false
	}
	oldEnough := true
	_ = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			oldEnough = false
			return fs.SkipAll
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			oldEnough = false
			return fs.SkipAll
		}
		return nil
	})
	return oldEnough
}

func inspectCleanupPath(path string) (int64, string, error) {
	root := filepath.Clean(path)
	var total int64
	hash := sha256.New()
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("包含符号链接")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("包含特殊文件")
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\n", filepath.ToSlash(relative), info.Mode().String(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return total, "", err
	}
	return total, fmt.Sprintf("%x", hash.Sum(nil)), nil
}
