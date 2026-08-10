package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/release"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

type LlamaPlan struct {
	Options           LlamaOptions
	Release           release.GitHubRelease
	Backend           release.Backend
	AvailableBackends []string
	Current           State
	CurrentExists     bool
	Target            string
	DownloadSize      int64
	NeedsBackend      bool
	NeedsRecovery     bool
	RecoveryReason    string
	RecoveryDirectory string
	stateSnapshot     string
	targetSnapshot    string
}

type LauncherPlan struct {
	Options        LauncherOptions
	Release        release.GitHubRelease
	Asset          release.Asset
	SumsAsset      release.Asset
	CurrentVersion string
	DownloadSize   int64
	InstallDir     string
	launcherSnap   string
	updaterSnap    string
}

type AllPlan struct {
	Llama    *LlamaPlan
	Launcher *LauncherPlan
}

type AllResult struct {
	Llama        State
	LlamaApplied bool
	LauncherTag  string
	Handoff      bool
}

func pathSnapshot(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink", nil
	}
	if info.IsDir() {
		_, snapshot, inspectErr := inspectCleanupPath(path)
		return "dir:" + snapshot, inspectErr
	}
	if !info.Mode().IsRegular() {
		return "special", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("file:%x:%d:%d", digest.Sum(nil), info.Mode().Perm(), info.Size()), nil
}

func (m *Manager) PrepareLlama(ctx context.Context, options LlamaOptions) (*LlamaPlan, error) {
	plan := &LlamaPlan{Options: options}
	current, exists, stateErr := LoadState(m.Layout)
	if stateErr != nil {
		plan.NeedsRecovery = true
		plan.RecoveryReason = stateErr.Error()
	} else {
		plan.Current, plan.CurrentExists = current, exists
		if !exists {
			entries, readErr := os.ReadDir(m.Layout.LlamaRuntimeDir)
			if readErr == nil && len(entries) > 0 {
				plan.NeedsRecovery = true
				plan.RecoveryReason = "更新状态不存在，但运行时目录中仍有内容"
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return nil, readErr
			}
		}
	}
	rel, err := m.release(ctx, LlamaRepository, options.Version)
	if err != nil {
		return nil, err
	}
	if _, err := CompareLlamaTag(rel.Tag, rel.Tag); err != nil {
		return nil, err
	}
	plan.Release = rel
	backends, err := release.LlamaAssets(rel, m.GOOS, m.GOARCH)
	if err != nil {
		return nil, err
	}
	for _, backend := range backends {
		plan.AvailableBackends = append(plan.AvailableBackends, backend.ID)
	}
	requested := strings.TrimSpace(options.Backend)
	if requested == "" && exists && !plan.NeedsRecovery {
		requested = current.Backend
	}
	if requested == "" {
		plan.NeedsBackend = true
		return plan, nil
	}
	available := false
	for _, id := range plan.AvailableBackends {
		if strings.EqualFold(id, requested) {
			available = true
			break
		}
	}
	if !available && options.Backend == "" && exists {
		plan.NeedsBackend = true
		return plan, nil
	}
	if err := m.SelectLlamaBackend(plan, requested); err != nil {
		return nil, err
	}
	return plan, nil
}

func (m *Manager) SelectLlamaBackend(plan *LlamaPlan, backend string) error {
	if plan == nil || plan.Release.Tag == "" {
		return errors.New("llama.cpp 更新计划无效")
	}
	options, err := release.LlamaAssets(plan.Release, m.GOOS, m.GOARCH)
	if err != nil {
		return err
	}
	var selected release.Backend
	for _, candidate := range options {
		if strings.EqualFold(candidate.ID, strings.TrimSpace(backend)) {
			selected = candidate
			break
		}
	}
	if selected.ID == "" {
		return fmt.Errorf("后端 %q 不可用；可用值: %s", backend, strings.Join(plan.AvailableBackends, ", "))
	}
	if plan.CurrentExists && plan.Current.LlamaTag != "" && !plan.NeedsRecovery {
		cmp, err := CompareLlamaTag(plan.Current.LlamaTag, plan.Release.Tag)
		if err != nil {
			return err
		}
		if cmp > 0 && !plan.Options.AllowDowngrade {
			return fmt.Errorf("拒绝从 %s 降级到 %s", plan.Current.LlamaTag, plan.Release.Tag)
		}
		if cmp == 0 && strings.EqualFold(selected.ID, plan.Current.Backend) && !plan.Options.Reinstall {
			return fmt.Errorf("%w: llama.cpp %s；使用 --reinstall 重装", ErrAlreadyCurrent, plan.Release.Tag)
		}
	}
	var total int64
	for _, asset := range selected.Assets {
		if asset.Size <= 0 || asset.Size > 2<<30 || asset.Size > (4<<30)-total {
			return errors.New("资产组合下载量超过 4 GiB 或大小无效")
		}
		total += asset.Size
	}
	if err := ensureFreeSpace(m.Layout.RuntimeDir, total*3); err != nil {
		return err
	}
	if !safeComponent.MatchString(plan.Release.Tag) || !safeComponent.MatchString(selected.ID) {
		return errors.New("Release tag 或后端 ID 不能安全用作目录名")
	}
	plan.Backend, plan.DownloadSize, plan.NeedsBackend = selected, total, false
	plan.Target = filepath.Join(m.Layout.LlamaRuntimeDir, selected.ID, plan.Release.Tag)
	plan.stateSnapshot, err = pathSnapshot(m.Layout.UpdateStateFile)
	if err != nil {
		return err
	}
	plan.targetSnapshot, err = pathSnapshot(plan.Target)
	if err != nil {
		return err
	}
	if plan.NeedsRecovery {
		name := "repair-" + time.Now().UTC().Format("20060102T150405Z") + "-" + randomToken()
		plan.RecoveryDirectory = filepath.Join(m.Layout.RecoveryDir, name)
	}
	return nil
}

func (m *Manager) verifyLlamaPlan(plan *LlamaPlan) error {
	if plan == nil || plan.NeedsBackend || plan.Backend.ID == "" || plan.Target == "" {
		return errors.New("llama.cpp 更新计划尚未选择后端")
	}
	stateSnapshot, err := pathSnapshot(m.Layout.UpdateStateFile)
	if err != nil {
		return err
	}
	targetSnapshot, err := pathSnapshot(plan.Target)
	if err != nil {
		return err
	}
	if stateSnapshot != plan.stateSnapshot || targetSnapshot != plan.targetSnapshot {
		return errors.New("更新状态或目标目录在确认后发生变化，已中止本次更新")
	}
	return nil
}

func (m *Manager) PrepareLauncherPlan(ctx context.Context, options LauncherOptions) (*LauncherPlan, error) {
	rel, err := m.release(ctx, LauncherRepository, options.Version)
	if err != nil {
		return nil, err
	}
	if _, err = CompareSemVer(rel.Tag, rel.Tag); err != nil {
		return nil, fmt.Errorf("目标启动器版本 %q 不是发布 SemVer: %w", rel.Tag, err)
	}
	if !strings.EqualFold(strings.TrimSpace(buildversion.Version), "dev") {
		cmp, compareErr := CompareSemVer(buildversion.Version, rel.Tag)
		if compareErr != nil {
			return nil, fmt.Errorf("当前启动器版本 %q 不是发布 SemVer: %w", buildversion.Version, compareErr)
		}
		if cmp > 0 && !options.AllowDowngrade {
			return nil, fmt.Errorf("拒绝从 %s 降级到 %s", buildversion.Version, rel.Tag)
		}
		if cmp == 0 && !options.Reinstall {
			return nil, fmt.Errorf("%w: 启动器 %s；使用 --reinstall 重装", ErrAlreadyCurrent, buildversion.Version)
		}
	}
	asset, err := release.LauncherAsset(rel, m.GOOS, m.GOARCH)
	if err != nil {
		return nil, err
	}
	sums, err := release.SHA256SumsAsset(rel)
	if err != nil {
		return nil, err
	}
	if asset.Size <= 0 || sums.Size <= 0 || asset.Size > 2<<30 || sums.Size > 4<<20 {
		return nil, errors.New("启动器资产大小无效，或 SHA256SUMS.txt 超过 4 MiB")
	}
	if err := ensureFreeSpace(m.Layout.RuntimeDir, (asset.Size+sums.Size)*3); err != nil {
		return nil, err
	}
	plan := &LauncherPlan{Options: options, Release: rel, Asset: asset, SumsAsset: sums, CurrentVersion: buildversion.Version, DownloadSize: asset.Size + sums.Size, InstallDir: m.Layout.Bin}
	plan.launcherSnap, err = pathSnapshot(m.Layout.Launcher)
	if err != nil {
		return nil, err
	}
	plan.updaterSnap, err = pathSnapshot(m.Layout.Updater)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (m *Manager) verifyLauncherPlan(plan *LauncherPlan) error {
	if plan == nil || plan.Release.Tag == "" {
		return errors.New("启动器更新计划无效")
	}
	launcher, err := pathSnapshot(m.Layout.Launcher)
	if err != nil {
		return err
	}
	updater, err := pathSnapshot(m.Layout.Updater)
	if err != nil {
		return err
	}
	if launcher != plan.launcherSnap || updater != plan.updaterSnap {
		return errors.New("启动器文件在确认后发生变化，已中止本次更新")
	}
	return nil
}

func (m *Manager) PrepareAll(ctx context.Context, llamaOptions LlamaOptions, launcherOptions LauncherOptions) (*AllPlan, error) {
	llamaPlan, err := m.PrepareLlama(ctx, llamaOptions)
	if err != nil && !errors.Is(err, ErrAlreadyCurrent) {
		return nil, err
	}
	launcherPlan, launcherErr := m.PrepareLauncherPlan(ctx, launcherOptions)
	if launcherErr != nil && !errors.Is(launcherErr, ErrAlreadyCurrent) {
		return nil, launcherErr
	}
	return &AllPlan{Llama: llamaPlan, Launcher: launcherPlan}, nil
}

func (m *Manager) ApplyAll(ctx context.Context, plan *AllPlan) (AllResult, error) {
	if plan == nil {
		return AllResult{}, errors.New("全部更新计划无效")
	}
	var result AllResult
	if plan.Llama != nil {
		state, err := m.ApplyLlama(ctx, plan.Llama)
		if err != nil {
			return result, fmt.Errorf("llama.cpp 更新失败: %w", err)
		}
		result.Llama, result.LlamaApplied = state, true
	}
	if plan.Launcher != nil {
		tag, err := m.ApplyLauncher(ctx, plan.Launcher)
		if err != nil {
			return result, fmt.Errorf("启动器更新失败: %w", err)
		}
		result.LauncherTag, result.Handoff = tag, true
	}
	return result, nil
}

// Compatibility aliases with explicit update wording.
func (m *Manager) PrepareLlamaUpdate(ctx context.Context, options LlamaOptions) (*LlamaPlan, error) {
	return m.PrepareLlama(ctx, options)
}

func (m *Manager) PrepareLauncherUpdate(ctx context.Context, options LauncherOptions) (*LauncherPlan, error) {
	return m.PrepareLauncherPlan(ctx, options)
}
