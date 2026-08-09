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
	"sort"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

type CleanupCandidate struct {
	Path, Kind, Reason string
	Legacy             bool
	Size               int64
	SizeKnown          bool
	Automatic          bool
	Recent             bool
	Snapshot           string
}

const automaticCleanupMinAge = 24 * time.Hour

func CleanupCandidates(l layout.Layout) ([]CleanupCandidate, error) {
	var out []CleanupCandidate
	appendCandidate := func(path, kind, reason string, automatic, legacy bool) {
		info, err := os.Lstat(path)
		if err != nil {
			return
		}
		candidate := CleanupCandidate{Path: filepath.Clean(path), Kind: kind, Reason: reason, Automatic: automatic, Legacy: legacy}
		size, snapshot, inspectErr := inspectCleanupPath(path)
		if inspectErr == nil {
			candidate.Size, candidate.SizeKnown, candidate.Snapshot = size, true, snapshot
		} else if automatic {
			candidate.Automatic = false
			candidate.Reason += "；无法完整验证内容，不能批量清理"
		}
		if candidate.Automatic && kind != "updater 运行副本" && !oldEnoughForAutomaticCleanup(path, info) {
			candidate.Automatic = false
			candidate.Recent = true
			candidate.Reason += "；最近 24 小时内创建或修改，可能仍在使用"
		}
		out = append(out, candidate)
	}
	s, _, err := LoadState(l)
	if err != nil {
		return nil, err
	}
	for _, relative := range s.PendingCleanup {
		path := filepath.Join(l.Root, filepath.FromSlash(relative))
		appendCandidate(path, "已登记待清理运行时", "更新状态登记的非活动运行时或同版本重装备份", true, false)
	}
	for _, path := range l.LegacyPaths() {
		appendCandidate(path, "旧版布局", "新版不会迁移或自动删除此路径，必须逐项确认", false, true)
	}
	if entries, readErr := os.ReadDir(l.Bin); readErr == nil {
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			path := filepath.Join(l.Bin, entry.Name())
			switch {
			case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && validRunnerName(name):
				appendCandidate(path, "updater 运行副本", "更新交接后遗留的严格命名副本", true, false)
			case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && (strings.HasPrefix(name, ".llamalc-new-") || strings.HasPrefix(name, ".llamaup-new-")):
				appendCandidate(path, "启动器更新残留", "更新交接中断留下的暂存程序", true, false)
			case !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.Contains(name, "-rollback-") && strings.HasPrefix(name, "."):
				appendCandidate(path, "启动器更新恢复备份", "双文件更新回滚时保留的原程序，需要人工确认", false, false)
			}
		}
	}
	for _, directory := range []string{l.ConfigDir, l.SecretsDir, l.StateDir, l.RouterConfigDir, l.RouterStateDir} {
		if entries, readErr := os.ReadDir(directory); readErr == nil {
			for _, entry := range entries {
				name := strings.ToLower(entry.Name())
				if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-") {
					appendCandidate(filepath.Join(directory, entry.Name()), "配置写入残留", "原子写入中断留下的临时文件", true, false)
				}
			}
		}
	}
	if entries, readErr := os.ReadDir(l.RuntimeDir); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".launcher-update-") {
				appendCandidate(filepath.Join(l.RuntimeDir, entry.Name()), "启动器下载暂存", "启动器下载或校验中断留下的目录", true, false)
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
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".staging-") {
				appendCandidate(filepath.Join(l.LlamaRuntimeDir, entry.Name()), "运行时下载暂存", "llama.cpp 下载或解压中断留下的目录", true, false)
				continue
			}
			backendPath := filepath.Join(l.LlamaRuntimeDir, entry.Name())
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				appendCandidate(backendPath, "异常运行时项目", "运行时根目录中的项目不符合 <backend>/<version> 布局", false, false)
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
				appendCandidate(path, "未登记运行时目录", "不属于当前活动运行时，也未登记为待清理目录", false, false)
			}
		}
	}
	if entries, readErr := os.ReadDir(l.RecoveryDir); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				path := filepath.Join(l.RecoveryDir, entry.Name())
				appendCandidate(path, "恢复备份", recoveryReason(path), false, false)
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
func DeleteCandidate(l layout.Layout, c CleanupCandidate) error {
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
	if c.Legacy { // Only paths emitted by Layout.LegacyPaths can enter this branch.
		allowed := false
		for _, p := range l.LegacyPaths() {
			if filepath.Clean(p) == filepath.Clean(c.Path) {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("旧版路径未通过重新识别")
		}
	}
	if info.IsDir() {
		err = os.RemoveAll(c.Path)
	} else {
		err = os.Remove(c.Path)
	}
	if err != nil {
		return fmt.Errorf("清理 %s: %w", c.Path, err)
	}
	if c.Kind == "已登记待清理运行时" {
		s, exists, e := LoadState(l)
		if e == nil && exists {
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
