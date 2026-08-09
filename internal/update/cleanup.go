package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

type CleanupCandidate struct {
	Path, Kind, Reason string
	Legacy             bool
}

func CleanupCandidates(l layout.Layout) ([]CleanupCandidate, error) {
	var out []CleanupCandidate
	s, _, err := LoadState(l)
	if err != nil {
		return nil, err
	}
	for _, relative := range s.PendingCleanup {
		path := filepath.Join(l.Root, filepath.FromSlash(relative))
		out = append(out, CleanupCandidate{Path: path, Kind: "旧运行时", Reason: "更新状态登记的非活动运行时"})
	}
	for _, path := range l.LegacyPaths() {
		out = append(out, CleanupCandidate{Path: path, Kind: "旧版布局", Reason: "v1.0.0 不会迁移或自动删除此路径", Legacy: true})
	}
	if entries, readErr := os.ReadDir(l.Bin); readErr == nil {
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(strings.ToLower(entry.Name()), ".llamaup-run-") {
				out = append(out, CleanupCandidate{Path: filepath.Join(l.Bin, entry.Name()), Kind: "updater 运行副本", Reason: "更新交接后遗留的严格命名副本"})
			}
		}
	}
	if entries, readErr := os.ReadDir(l.RecoveryDir); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				out = append(out, CleanupCandidate{Path: filepath.Join(l.RecoveryDir, entry.Name()), Kind: "恢复备份", Reason: "更新或回滚保留的恢复内容"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out, nil
}
func DeleteCandidate(l layout.Layout, c CleanupCandidate) error {
	current, err := CleanupCandidates(l)
	if err != nil {
		return err
	}
	found := false
	for _, x := range current {
		if filepath.Clean(x.Path) == filepath.Clean(c.Path) && x.Kind == c.Kind {
			found = true
			break
		}
	}
	if !found {
		return errors.New("清理目标状态已变化，请刷新")
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
	} else if c.Kind == "updater 运行副本" {
		if err = managedfs.Within(l.Bin, c.Path); err != nil {
			return err
		}
	} else if c.Kind == "恢复备份" {
		if err = managedfs.Within(l.RecoveryDir, c.Path); err != nil {
			return err
		}
	} else if err = managedfs.Within(l.LlamaRuntimeDir, c.Path); err != nil {
		return err
	}
	if info.IsDir() {
		err = os.RemoveAll(c.Path)
	} else {
		err = os.Remove(c.Path)
	}
	if err != nil {
		return fmt.Errorf("清理 %s: %w", c.Path, err)
	}
	if !c.Legacy {
		s, exists, e := LoadState(l)
		if e == nil && exists {
			var pending []string
			for _, p := range s.PendingCleanup {
				if filepath.Clean(filepath.Join(l.Root, filepath.FromSlash(p))) != filepath.Clean(c.Path) {
					pending = append(pending, p)
				}
			}
			s.PendingCleanup = pending
			_ = SaveState(l, s)
		}
	}
	return nil
}
