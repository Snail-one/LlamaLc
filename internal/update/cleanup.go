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

var removeCleanupTree = os.RemoveAll

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
	scanEntries := func(directory string) []os.DirEntry {
		entries, err := os.ReadDir(directory)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			out = append(out, CleanupCandidate{Path: directory, Kind: "扫描警告", Reason: "无法读取清理目录: " + err.Error(), Warning: true})
		}
		return entries
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
	for _, entry := range scanEntries(l.Bin) {
		name := entry.Name()
		path := filepath.Join(l.Bin, entry.Name())
		switch {
		case validDeleteQuarantineName(name):
			appendCandidate(path, "中断的隔离清理残留", "清理在隔离改名后中断；内容可能已部分删除，需要人工确认", false)
		case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validRunnerName(name):
			appendCandidate(path, "updater 运行副本", "更新交接后遗留的严格命名副本", true)
		case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && (validProgramTempName(name, ".llamalc-new-") || validProgramTempName(name, ".llamaup-new-")):
			appendCandidate(path, "启动器更新残留", "更新交接中断留下的暂存程序", true)
		case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && (validProgramTempName(name, ".llamalc-rollback-") || validProgramTempName(name, ".llamaup-rollback-")):
			appendCandidate(path, "启动器更新恢复备份", "双文件更新回滚时保留的原程序，需要人工确认", false)
		}
	}
	for _, location := range atomicTempLocations(l) {
		for _, entry := range scanEntries(location.directory) {
			name := entry.Name()
			if validDeleteQuarantineName(name) {
				appendCandidate(filepath.Join(location.directory, entry.Name()), "中断的隔离清理残留", "清理在隔离改名后中断；内容可能已部分删除，需要人工确认", false)
			} else if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validAtomicTempName(name, location.base) {
				appendCandidate(filepath.Join(location.directory, entry.Name()), "配置写入残留", "原子写入中断留下的临时文件", true)
			}
		}
	}
	for _, entry := range scanEntries(l.RuntimeDir) {
		path := filepath.Join(l.RuntimeDir, entry.Name())
		if validDeleteQuarantineName(entry.Name()) {
			appendCandidate(path, "中断的隔离清理残留", "清理在隔离改名后中断；内容可能已部分删除，需要人工确认", false)
		} else if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".llama-lock-") {
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
	{
		entries := scanEntries(l.LlamaRuntimeDir)
		known := make(map[string]bool)
		if s.ActiveRuntime != "" {
			known[managedPathKey(filepath.Join(l.Root, filepath.FromSlash(s.ActiveRuntime)))] = true
		}
		for _, pending := range s.PendingCleanup {
			known[managedPathKey(filepath.Join(l.Root, filepath.FromSlash(pending)))] = true
		}
		for _, entry := range entries {
			if validDeleteQuarantineName(entry.Name()) {
				appendCandidate(filepath.Join(l.LlamaRuntimeDir, entry.Name()), "中断的隔离清理残留", "清理在隔离改名后中断；内容可能已部分删除，需要人工确认", false)
				continue
			}
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
			versions := scanEntries(backendPath)
			for _, version := range versions {
				path := filepath.Join(backendPath, version.Name())
				if validDeleteQuarantineName(version.Name()) {
					appendCandidate(path, "中断的隔离清理残留", "运行时清理在隔离后中断；内容可能已部分删除，需要人工确认", false)
					continue
				}
				if known[managedPathKey(path)] {
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
	for _, entry := range scanEntries(l.RecoveryDir) {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			path := filepath.Join(l.RecoveryDir, entry.Name())
			if validDeleteQuarantineName(entry.Name()) {
				appendCandidate(path, "中断的隔离清理残留", "恢复备份清理在隔离后中断；内容可能已部分删除，需要人工确认", false)
				continue
			}
			reason := recoveryReason(path)
			if stateErr != nil {
				reason = "扫描警告: 更新状态损坏；" + reason
			}
			appendCandidate(path, "恢复备份", reason, false)
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
	scanEntries := func(directory string) []os.DirEntry {
		entries, err := os.ReadDir(directory)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result.Warnings = append(result.Warnings, fmt.Errorf("读取清理目录 %s: %w", directory, err))
		}
		return entries
	}
	removeFile := func(path string, immediate bool) {
		info, err := stableCleanupInfo(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("复核清理目标 %s: %w", path, err))
			return
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
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
		lock, lockErr := acquireLlamaInstallLock(l)
		if lockErr != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("暂无法取得运行时清理锁: %w", lockErr))
		} else {
			stopHeartbeat := startOwnedLockHeartbeat(lock)
			latest, latestExists, loadErr := LoadState(l)
			if loadErr != nil {
				result.Warnings = append(result.Warnings, fmt.Errorf("重新读取 pending_cleanup: %w", loadErr))
			} else if latestExists {
				before := append([]string(nil), latest.PendingCleanup...)
				cleaned, cleanupErr := retryPendingCleanupLocked(l, latest)
				remaining := make(map[string]bool, len(cleaned.PendingCleanup))
				for _, relative := range cleaned.PendingCleanup {
					remaining[managedPathKey(relative)] = true
				}
				for _, relative := range before {
					if !remaining[managedPathKey(relative)] {
						result.Removed = append(result.Removed, filepath.Join(l.Root, filepath.FromSlash(relative)))
					}
				}
				if cleanupErr != nil {
					result.Warnings = append(result.Warnings, cleanupErr)
				}
			}
			stopHeartbeat()
			if removeErr := os.RemoveAll(lock); removeErr != nil {
				result.Warnings = append(result.Warnings, fmt.Errorf("释放运行时清理锁: %w", removeErr))
			}
		}
	}

	for _, entry := range scanEntries(l.Bin) {
		name := entry.Name()
		path := filepath.Join(l.Bin, entry.Name())
		switch {
		case validRunnerName(name):
			removeFile(path, true)
		case validProgramTempName(name, ".llamalc-new-") || validProgramTempName(name, ".llamaup-new-"):
			removeFile(path, false)
		}
	}
	for _, location := range atomicTempLocations(l) {
		for _, entry := range scanEntries(location.directory) {
			if validAtomicTempName(entry.Name(), location.base) {
				removeFile(filepath.Join(location.directory, entry.Name()), false)
			}
		}
	}
	removeOwnedDirectory := func(path, prefix, kind string) {
		info, err := stableCleanupInfo(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("复核清理目录 %s: %w", path, err))
			return
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !validOwnedTempDirectory(path, prefix, kind) {
			return
		}
		if kind == "llama-global-install-lock" || kind == "launcher-install-lock" {
			reclaimed, ownerLive, reclaimErr := reclaimDeadOwnedLock(l, path, prefix, kind)
			if reclaimErr != nil {
				result.Warnings = append(result.Warnings, fmt.Errorf("回收异常退出的更新锁 %s: %w", path, reclaimErr))
				return
			}
			if reclaimed {
				result.Removed = append(result.Removed, path)
				return
			}
			if ownerLive {
				return
			}
		}
		if !oldEnoughForAutomaticCleanup(path, info) {
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
	for _, entry := range scanEntries(l.RuntimeDir) {
		if strings.HasPrefix(strings.ToLower(entry.Name()), ".llama-lock-") {
			removeOwnedDirectory(filepath.Join(l.RuntimeDir, entry.Name()), ".llama-lock-", "llama-global-install-lock")
		} else if strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-lock-") {
			removeOwnedDirectory(filepath.Join(l.RuntimeDir, entry.Name()), ".launcher-lock-", "launcher-install-lock")
		} else if strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-update-") {
			removeOwnedDirectory(filepath.Join(l.RuntimeDir, entry.Name()), ".launcher-update-", "launcher-update-staging")
		}
	}
	for _, entry := range scanEntries(l.LlamaRuntimeDir) {
		if strings.HasPrefix(strings.ToLower(entry.Name()), ".install-lock-") {
			removeOwnedDirectory(filepath.Join(l.LlamaRuntimeDir, entry.Name()), ".install-lock-", "llama-install-lock")
		} else if strings.HasPrefix(strings.ToLower(entry.Name()), ".staging-") {
			removeOwnedDirectory(filepath.Join(l.LlamaRuntimeDir, entry.Name()), ".staging-", "llama-runtime-staging")
		}
	}
	sort.Strings(result.Removed)
	return result
}

// stableCleanupInfo returns FileInfo whose identity was captured from an open
// handle. On Windows, os.Lstat defers loading the file ID until os.SameFile is
// called; after a rename that makes the old path unavailable and produces a
// false identity mismatch. File.Stat records the ID while the handle is valid,
// so the returned identity remains usable after the handle is closed and the
// path is moved to quarantine.
func stableCleanupInfo(path string) (os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return pathInfo, nil
	}
	if !pathInfo.Mode().IsRegular() && !pathInfo.IsDir() {
		return nil, errors.New("清理目标不是常规文件或目录")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	handleInfo, statErr := file.Stat()
	sameFile := statErr == nil && os.SameFile(pathInfo, handleInfo)
	closeErr := file.Close()
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !sameFile {
		return nil, errors.New("清理目标文件身份在检查时发生变化")
	}
	return handleInfo, nil
}

// removeAutomaticCleanupPath prevents a validated cleanup path from being
// swapped between inspection and deletion. The exact inode is first moved to
// a private sibling name, then its identity and complete snapshot are checked
// again before anything is removed.
func removeAutomaticCleanupPath(l layout.Layout, path string, expected os.FileInfo, expectedSnapshot string) error {
	if expectedSnapshot == "" {
		return errors.New("缺少清理目标快照")
	}
	if err := managedfs.Within(l.Root, path); err != nil {
		return err
	}
	if err := managedfs.Validate(l.Root, path, false); err != nil {
		return err
	}
	quarantine := filepath.Join(filepath.Dir(path), ".llamalc-delete-"+randomToken())
	if err := managedfs.Within(l.Root, quarantine); err != nil {
		return err
	}
	if err := os.Rename(path, quarantine); err != nil {
		return fmt.Errorf("隔离清理目标: %w", err)
	}
	movedInfo, statErr := os.Lstat(quarantine)
	if statErr != nil || !os.SameFile(expected, movedInfo) {
		return restoreQuarantinedPath(quarantine, path, errors.New("清理目标文件身份在隔离时发生变化"))
	}
	_, movedSnapshot, inspectErr := inspectCleanupPath(quarantine)
	if inspectErr != nil || movedSnapshot != expectedSnapshot {
		return restoreQuarantinedPath(quarantine, path, errors.New("清理目标隔离后的最终快照不一致"))
	}
	if movedInfo.IsDir() {
		if err := removeCleanupTree(quarantine); err != nil {
			return fmt.Errorf("递归清理未完成，未恢复可能已部分删除的目录；残留保留在 %s: %w", quarantine, err)
		}
	} else {
		if err := os.Remove(quarantine); err != nil {
			return restoreQuarantinedPath(quarantine, path, err)
		}
	}
	if _, statErr := os.Lstat(quarantine); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("清理隔离路径仍然存在，最终状态复检失败")
	}
	if _, statErr = os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("原路径在清理期间被重新创建，最终状态复检失败")
	}
	return syncParentDirectory(filepath.Dir(path))
}

func restoreQuarantinedPath(quarantine, original string, cause error) error {
	if restoreErr := os.Rename(quarantine, original); restoreErr != nil {
		return fmt.Errorf("%v；恢复隔离目标失败，残留位于 %s: %w", cause, quarantine, restoreErr)
	}
	return cause
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

func validDeleteQuarantineName(name string) bool {
	if name != strings.ToLower(name) {
		return false
	}
	token := strings.TrimPrefix(name, ".llamalc-delete-")
	return token != name && len(token) == 16 && safeHex(token)
}

type atomicTempLocation struct {
	directory string
	base      string
}

func atomicTempLocations(l layout.Layout) []atomicTempLocation {
	return []atomicTempLocation{
		{l.ConfigDir, filepath.Base(l.ConfigFile)},
		{l.SecretsDir, filepath.Base(l.APIKeyFile)},
		{l.StateDir, filepath.Base(l.UpdateStateFile)},
		{l.RouterConfigDir, filepath.Base(l.RouterPreset)},
		{l.RouterStateDir, filepath.Base(l.AutoRouterPreset)},
	}
}

func validAtomicTempName(name, base string) bool {
	if name != strings.ToLower(name) {
		return false
	}
	prefix := "." + strings.ToLower(base) + ".tmp-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	token := strings.TrimPrefix(name, prefix)
	return len(token) == 16 && safeHex(token)
}

func validOwnedTempDirectory(path, prefix, kind string) bool {
	_, valid := readOwnershipMarker(path, prefix, kind)
	return valid
}

func readOwnershipMarker(path, prefix, kind string) (ownershipMarker, bool) {
	base := filepath.Base(path)
	if base != strings.ToLower(base) {
		return ownershipMarker{}, false
	}
	name := strings.ToLower(base)
	token := strings.TrimPrefix(name, prefix)
	if token == name || len(token) != 16 || !safeHex(token) {
		return ownershipMarker{}, false
	}
	markerPath := filepath.Join(path, ownershipMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || markerInfo.Size() > 4096 {
		return ownershipMarker{}, false
	}
	file, err := os.Open(markerPath)
	if err != nil {
		return ownershipMarker{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || !os.SameFile(markerInfo, info) {
		return ownershipMarker{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return ownershipMarker{}, false
	}
	var marker ownershipMarker
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil {
		return ownershipMarker{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ownershipMarker{}, false
	}
	if marker.Schema != 1 || marker.Token != token || marker.Kind != kind {
		return ownershipMarker{}, false
	}
	return marker, true
}

func DeleteCandidate(l layout.Layout, c CleanupCandidate) error {
	if c.Warning {
		return errors.New("扫描警告项目不可删除")
	}
	var cleanupLock string
	var stopHeartbeat func()
	if c.Kind == "已登记待清理运行时" || managedfs.Within(l.LlamaRuntimeDir, c.Path) == nil {
		var lockErr error
		cleanupLock, lockErr = acquireLlamaInstallLock(l)
		if lockErr != nil {
			return fmt.Errorf("取得运行时清理锁: %w", lockErr)
		}
		stopHeartbeat = startOwnedLockHeartbeat(cleanupLock)
		defer func() {
			stopHeartbeat()
			_ = os.RemoveAll(cleanupLock)
		}()
	}
	current, err := CleanupCandidates(l)
	if err != nil {
		return err
	}
	var found *CleanupCandidate
	for _, x := range current {
		if sameManagedPath(x.Path, c.Path) && x.Kind == c.Kind {
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
	info, err := stableCleanupInfo(c.Path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("拒绝清理符号链接")
	}
	if managedfs.Within(l.LlamaRuntimeDir, c.Path) == nil {
		if activeErr := ensureCleanupTargetIsNotActive(l, c.Path, info); activeErr != nil {
			return activeErr
		}
	}
	quarantine := filepath.Join(filepath.Dir(c.Path), ".llamalc-delete-"+randomToken())
	if err = managedfs.Within(l.Root, quarantine); err != nil {
		return err
	}
	if err = os.Rename(c.Path, quarantine); err != nil {
		return fmt.Errorf("隔离清理目标 %s: %w", c.Path, err)
	}
	quarantineInfo, statErr := os.Lstat(quarantine)
	if statErr != nil || !os.SameFile(info, quarantineInfo) {
		return restoreQuarantinedPath(quarantine, c.Path, errors.New("清理目标文件身份在隔离时发生变化"))
	}
	_, quarantineSnapshot, inspectErr := inspectCleanupPath(quarantine)
	if inspectErr != nil || quarantineSnapshot != c.Snapshot {
		return restoreQuarantinedPath(quarantine, c.Path, errors.New("清理目标隔离后的最终快照不一致"))
	}
	if quarantineInfo.IsDir() {
		if err = removeCleanupTree(quarantine); err != nil {
			return fmt.Errorf("清理 %s 未完成，未恢复可能已部分删除的目录；残留保留在 %s: %w", c.Path, quarantine, err)
		}
	} else {
		if err = os.Remove(quarantine); err != nil {
			return restoreQuarantinedPath(quarantine, c.Path, fmt.Errorf("清理 %s: %w", c.Path, err))
		}
	}
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
				if !sameManagedPath(filepath.Join(l.Root, filepath.FromSlash(p)), c.Path) {
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
	_ = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			oldEnough = false
			return fs.SkipAll
		}
		// Relative library soname symlinks are expected in modern llama.cpp
		// runtimes; still refuse to walk through directory symlinks.
		if entry.Type()&os.ModeSymlink != 0 {
			// WalkDir's DirEntry.IsDir reports the link itself, not its target.
			// Validate the resolved target explicitly.
			_, info, err := inspectCleanupSymlink(filepath.Clean(path), current)
			if err != nil || info.ModTime().After(cutoff) {
				oldEnough = false
				return fs.SkipAll
			}
			return nil
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
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		// Allow relative file symlinks (llama.cpp .so sonames). Directory
		// symlinks are still rejected so snapshots cannot escape the tree.
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, info, linkErr := inspectCleanupSymlink(root, current)
			if linkErr != nil {
				return linkErr
			}
			fmt.Fprintf(hash, "%s\x00symlink\x00%s\x00%d\n", filepath.ToSlash(relative), filepath.ToSlash(linkTarget), info.ModTime().UnixNano())
			return nil
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
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\n", filepath.ToSlash(relative), info.Mode().String(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return total, "", err
	}
	return total, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func inspectCleanupSymlink(root, current string) (string, os.FileInfo, error) {
	linkTarget, err := os.Readlink(current)
	if err != nil {
		return "", nil, err
	}
	if filepath.IsAbs(linkTarget) {
		return "", nil, errors.New("包含绝对符号链接")
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(current), linkTarget))
	if err := managedfs.Within(root, resolved); err != nil {
		return "", nil, errors.New("包含越界符号链接")
	}
	targetInfo, err := os.Stat(current)
	if err != nil {
		return "", nil, err
	}
	if targetInfo.IsDir() {
		return "", nil, errors.New("包含目录符号链接")
	}
	linkInfo, err := os.Lstat(current)
	if err != nil {
		return "", nil, err
	}
	return linkTarget, linkInfo, nil
}
