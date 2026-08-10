package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/procinfo"
	"github.com/Snail-one/LlamaLc/internal/release"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

const (
	LlamaRepository    = "ggml-org/llama.cpp"
	LauncherRepository = "Snail-one/LlamaLc"
)

var ErrAlreadyCurrent = errors.New("已是最新版本")

const (
	ownershipMarkerName = ".llamalc-owned.json"
	lockOwnerName       = ".llamalc-lock-owner.json"
)

type Source interface {
	Latest(context.Context, string) (release.GitHubRelease, error)
	Download(context.Context, release.Asset, string) error
}

type taggedSource interface {
	Release(context.Context, string, string) (release.GitHubRelease, error)
}

type LlamaOptions struct {
	Version        string
	Backend        string
	Reinstall      bool
	AllowDowngrade bool
}

type LauncherOptions struct {
	Version        string
	Reinstall      bool
	AllowDowngrade bool
}
type Manager struct {
	Layout       layout.Layout
	Source       Source
	GOOS, GOARCH string
	Out, Err     io.Writer
}
type CheckResult struct {
	Component string `json:"component"`
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
}

func NewManager(l layout.Layout, source Source) *Manager {
	return &Manager{Layout: l, Source: source, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

func (m *Manager) release(ctx context.Context, repository, version string) (release.GitHubRelease, error) {
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, "latest") {
		return m.Source.Latest(ctx, repository)
	}
	source, ok := m.Source.(taggedSource)
	if !ok {
		return release.GitHubRelease{}, errors.New("当前 Release 源不支持指定版本")
	}
	return source.Release(ctx, repository, version)
}

// AvailableLlamaBackends returns the backend IDs published for the current
// platform together with the backend recorded by an existing installation.
func (m *Manager) AvailableLlamaBackends(ctx context.Context) (tag string, ids []string, current string, err error) {
	state, exists, err := LoadState(m.Layout)
	if err != nil {
		// A damaged state must not prevent the maintenance menu from offering a
		// repair installation. UpdateLlama will quarantine it transactionally.
		state, exists = State{}, false
	}
	if exists {
		state, err = m.retryPendingCleanup(state)
		if err != nil {
			return "", nil, "", err
		}
	}
	rel, err := m.Source.Latest(ctx, LlamaRepository)
	if err != nil {
		return "", nil, "", err
	}
	if _, err = CompareLlamaTag(rel.Tag, rel.Tag); err != nil {
		return "", nil, "", err
	}
	options, err := release.LlamaAssets(rel, m.GOOS, m.GOARCH)
	if err != nil {
		return "", nil, "", err
	}
	ids = make([]string, len(options))
	for i, option := range options {
		ids[i] = option.ID
	}
	if exists {
		current = state.Backend
	}
	return rel.Tag, ids, current, nil
}
func (m *Manager) Check(ctx context.Context, target string) ([]CheckResult, error) {
	var out []CheckResult
	s, _, err := LoadState(m.Layout)
	if err != nil {
		return nil, err
	}
	s, err = m.retryPendingCleanup(s)
	if err != nil {
		return nil, err
	}
	if target == "all" || target == "llama" {
		r, err := m.Source.Latest(ctx, LlamaRepository)
		if err != nil {
			return nil, err
		}
		if _, err := CompareLlamaTag(r.Tag, r.Tag); err != nil {
			return nil, err
		}
		available := s.LlamaTag == ""
		if s.LlamaTag != "" {
			cmp, e := CompareLlamaTag(s.LlamaTag, r.Tag)
			if e != nil {
				return nil, e
			}
			available = cmp < 0
		}
		out = append(out, CheckResult{Component: "llama", Installed: s.LlamaTag, Latest: r.Tag, Available: available})
	}
	if target == "all" || target == "launcher" {
		r, err := m.Source.Latest(ctx, LauncherRepository)
		if err != nil {
			return nil, err
		}
		if _, err := CompareSemVer(r.Tag, r.Tag); err != nil {
			return nil, fmt.Errorf("最新启动器版本无效: %w", err)
		}
		available := strings.EqualFold(strings.TrimSpace(buildversion.Version), "dev")
		if !available {
			cmp, e := CompareSemVer(buildversion.Version, r.Tag)
			if e != nil {
				return nil, fmt.Errorf("当前启动器版本 %q 不是发布 SemVer: %w", buildversion.Version, e)
			}
			available = cmp < 0
		}
		out = append(out, CheckResult{Component: "launcher", Installed: buildversion.Version, Latest: r.Tag, Available: available})
	}
	return out, nil
}

func (m *Manager) UpdateLlama(ctx context.Context, backend string, reinstall bool) (State, error) {
	return m.UpdateLlamaWithOptions(ctx, LlamaOptions{Backend: backend, Reinstall: reinstall})
}

func (m *Manager) UpdateLlamaWithOptions(ctx context.Context, options LlamaOptions) (State, error) {
	plan, err := m.PrepareLlama(ctx, options)
	if err != nil {
		return State{}, err
	}
	if plan.NeedsBackend {
		return State{}, fmt.Errorf("首次安装或当前后端不可用，必须使用 --backend；可用值: %s", strings.Join(plan.AvailableBackends, ", "))
	}
	return m.ApplyLlama(ctx, plan)
}

func (m *Manager) ApplyLlama(ctx context.Context, plan *LlamaPlan) (State, error) {
	if err := m.verifyLlamaPlan(plan); err != nil {
		return State{}, err
	}
	if err := managedfs.EnsureDir(m.Layout.Root, m.Layout.RuntimeDir, 0o700); err != nil {
		return State{}, err
	}
	lock, err := acquireLlamaInstallLock(m.Layout)
	if err != nil {
		return State{}, err
	}
	defer func() {
		if removeErr := os.RemoveAll(lock); removeErr != nil && m.Err != nil {
			fmt.Fprintln(m.Err, "警告: 无法释放 llama.cpp 更新锁，下次启动将重试:", removeErr)
		}
	}()
	stopHeartbeat := startOwnedLockHeartbeat(lock)
	defer stopHeartbeat()
	// The state may have changed between the initial preflight check and lock
	// acquisition. Recheck only after the global lock is held.
	if err := m.verifyLlamaPlan(plan); err != nil {
		return State{}, err
	}
	if plan.CurrentExists && len(plan.Current.PendingCleanup) > 0 {
		cleaned, err := retryPendingCleanupLocked(m.Layout, plan.Current)
		if err != nil {
			return State{}, err
		}
		plan.Current = cleaned
		plan.stateSnapshot, _ = pathSnapshot(m.Layout.UpdateStateFile)
	}
	var recovery recoveryTransaction
	if plan.NeedsRecovery {
		if m.Err != nil {
			fmt.Fprintln(m.Err, "警告: 未找到有效的受管运行时，将隔离当前状态后重新安装:", plan.RecoveryReason)
		}
		var err error
		recovery, err = quarantineInvalidRuntimeTo(m.Layout, errors.New(plan.RecoveryReason), plan.RecoveryDirectory)
		if err != nil {
			return State{}, err
		}
		plan.Current, plan.CurrentExists = State{Schema: StateSchema}, false
		plan.stateSnapshot, _ = pathSnapshot(m.Layout.UpdateStateFile)
		plan.targetSnapshot, _ = pathSnapshot(plan.Target)
	}
	state, err := m.applyLlamaPlanCore(ctx, plan)
	if err != nil && recovery.directory != "" {
		if rollbackErr := rollbackRecovery(m.Layout, recovery); rollbackErr != nil {
			return State{}, fmt.Errorf("重新安装失败: %w；同时无法完整恢复已隔离的运行时: %v", err, rollbackErr)
		}
		return State{}, err
	}
	if err == nil && recovery.directory != "" && m.Out != nil {
		fmt.Fprintln(m.Out, "旧状态/运行时未自动删除，已保留为恢复备份:", recovery.directory)
	}
	return state, err
}

func (m *Manager) ApplyLlamaUpdate(ctx context.Context, plan *LlamaPlan) (State, error) {
	return m.ApplyLlama(ctx, plan)
}

func (m *Manager) applyLlamaPlanCore(ctx context.Context, plan *LlamaPlan) (State, error) {
	options, current, exists := plan.Options, plan.Current, plan.CurrentExists
	r, selected := plan.Release, plan.Backend
	reinstall := options.Reinstall
	var err error
	if err := managedfs.EnsureDir(m.Layout.Root, m.Layout.LlamaRuntimeDir, 0o700); err != nil {
		return State{}, err
	}
	token := randomToken()
	staging := filepath.Join(m.Layout.LlamaRuntimeDir, ".staging-"+token)
	if err = createOwnedTempDirectory(m.Layout, staging, token, "llama-runtime-staging"); err != nil {
		return State{}, err
	}
	defer os.RemoveAll(staging)
	var installed []InstalledAsset
	budget := release.NewExtractBudget(20_000, 8<<30)
	for _, a := range selected.Assets {
		archive := filepath.Join(staging, a.Name)
		if err = m.Source.Download(ctx, a, archive); err != nil {
			return State{}, err
		}
		digest, _ := release.Digest(a.Digest)
		installed = append(installed, InstalledAsset{Name: a.Name, SHA256: digest})
		extract := filepath.Join(staging, "payload")
		if m.Out != nil {
			fmt.Fprintln(m.Out, "正在安全解压:", a.Name)
		}
		if err = release.ExtractWithBudget(archive, extract, budget); err != nil {
			return State{}, fmt.Errorf("解压 %s: %w", a.Name, err)
		}
		if m.Out != nil {
			fmt.Fprintln(m.Out, "解压完成:", a.Name)
		}
		_ = os.Remove(archive)
	}
	payload := filepath.Join(staging, "payload")
	rt, err := llama.Locate(payload, m.GOOS)
	if err != nil {
		return State{}, fmt.Errorf("校验新运行时: %w", err)
	}
	detectedVersion, probeErr := llama.ProbeVersion(ctx, rt.Server)
	if probeErr != nil {
		return State{}, probeErr
	}
	if !matchesLlamaTag(detectedVersion, r.Tag) {
		return State{}, fmt.Errorf("新运行时版本签名与目标 tag %s 不匹配: %s", r.Tag, detectedVersion)
	}
	target := plan.Target
	if err = m.verifyLlamaPlan(plan); err != nil {
		return State{}, err
	}
	if err = managedfs.Validate(m.Layout.LlamaRuntimeDir, target, true); err != nil {
		return State{}, err
	}
	backup := ""
	if _, err = os.Lstat(target); err == nil {
		if !reinstall {
			return State{}, fmt.Errorf("目标运行时已存在: %s", target)
		}
		if !exists || !sameManagedPath(current.ActiveRuntime, runtimeRelative(selected.ID, r.Tag)) {
			return State{}, fmt.Errorf("拒绝 --reinstall 覆盖未由更新状态登记的目标: %s", target)
		}
		backup = target + ".reinstall-" + token
		if err = os.Rename(target, backup); err != nil {
			return State{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return State{}, err
	}
	if err = os.Rename(payload, target); err != nil {
		if backup != "" {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				return State{}, fmt.Errorf("安装新运行时: %w；同时无法恢复原运行时 %s: %v", err, backup, restoreErr)
			}
		}
		return State{}, err
	}
	_ = os.RemoveAll(staging)
	previousRuntime := current.ActiveRuntime
	current.Schema = StateSchema
	current.LlamaTag = r.Tag
	current.Backend = selected.ID
	current.ActiveRuntime = runtimeRelative(selected.ID, r.Tag)
	current.Assets = installed
	if current.LauncherVersion == "" {
		current.LauncherVersion = buildversion.Version
	}
	// Persist every old path before deleting it. If the later cleanup-state
	// write fails, the durable state still knows what must be retried.
	var cleanupTargets []string
	if backup != "" {
		cleanupTargets = append(cleanupTargets, backup)
	}
	if previousRuntime != "" && !sameManagedPath(previousRuntime, current.ActiveRuntime) {
		cleanupTargets = append(cleanupTargets, filepath.Join(m.Layout.Root, filepath.FromSlash(previousRuntime)))
	}
	for _, old := range cleanupTargets {
		current.PendingCleanup = appendUnique(current.PendingCleanup, filepath.ToSlash(mustRel(m.Layout.Root, old)))
	}
	if err = SaveState(m.Layout, current); err != nil {
		if managedfs.IsPublishedError(err) {
			return current, fmt.Errorf("运行时和待清理状态已经切换，但状态目录未能确认持久化；已保留旧运行时供下次重试: %w", err)
		}
		if rollbackErr := rollbackInstalledRuntime(target, backup); rollbackErr != nil {
			return State{}, fmt.Errorf("运行时状态写入失败: %w；同时回滚新运行时失败: %v", err, rollbackErr)
		}
		return State{}, fmt.Errorf("运行时已安装但状态写入失败: %w", err)
	}
	// Once the new state is durable, old active content is no longer needed.
	// Delete it immediately and remove only successful paths from the durable
	// pending list.
	for _, old := range cleanupTargets {
		if removeErr := removeManagedRuntime(m.Layout, old); removeErr != nil {
			if m.Err != nil {
				fmt.Fprintln(m.Err, "警告: 旧运行时暂时无法删除，已登记 pending_cleanup:", removeErr)
			}
			continue
		}
		current.PendingCleanup = removePendingCleanupPath(m.Layout, current.PendingCleanup, old)
	}
	if err = SaveState(m.Layout, current); err != nil {
		return State{}, fmt.Errorf("运行时已切换，但无法保存清理状态: %w", err)
	}
	if _, err = ValidateActiveRuntime(ctx, m.Layout, m.GOOS); err != nil {
		return State{}, fmt.Errorf("安装后活动运行时校验失败: %w", err)
	}
	return current, nil
}

func rollbackInstalledRuntime(target, backup string) error {
	var failures []string
	if err := os.RemoveAll(target); err != nil {
		failures = append(failures, "移除新运行时: "+err.Error())
	}
	if backup != "" {
		if err := os.Rename(backup, target); err != nil {
			failures = append(failures, "恢复原运行时: "+err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

func removePendingCleanupPath(l layout.Layout, pending []string, absolute string) []string {
	out := pending[:0]
	for _, relative := range pending {
		if !sameManagedPath(filepath.Join(l.Root, filepath.FromSlash(relative)), absolute) {
			out = append(out, relative)
		}
	}
	return out
}

func matchesLlamaTag(versionOutput, tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if _, err := CompareLlamaTag(tag, tag); err != nil {
		return false
	}
	lower := strings.ToLower(versionOutput)
	if index := strings.Index(lower, "version:"); index >= 0 {
		value := strings.TrimSpace(lower[index+len("version:"):])
		end := 0
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		if end > 0 {
			comparison, err := CompareLlamaTag("b"+value[:end], tag)
			return err == nil && comparison == 0
		}
	}
	for index := 0; index+len(tag) <= len(lower); index++ {
		if lower[index:index+len(tag)] != tag {
			continue
		}
		beforeOK := index == 0 || !isASCIIAlphaNumeric(lower[index-1])
		after := index + len(tag)
		afterOK := after == len(lower) || !isASCIIAlphaNumeric(lower[after])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

type recoveryTransaction struct {
	directory, runtimeBackup, stateBackup string
}

type recoveryMetadata struct {
	Schema       int    `json:"schema"`
	CreatedAt    string `json:"created_at"`
	OriginalPath string `json:"original_path"`
	Reason       string `json:"reason"`
}

type limitedOutput struct {
	buffer strings.Builder
	size   int
}

func (output *limitedOutput) Write(data []byte) (int, error) {
	const limit = 1 << 20
	if output.size+len(data) > limit {
		remaining := limit - output.size
		if remaining > 0 {
			_, _ = output.buffer.Write(data[:remaining])
			output.size += remaining
		}
		return len(data), errors.New("版本探测输出超过 1 MiB")
	}
	output.size += len(data)
	return output.buffer.Write(data)
}

func (output *limitedOutput) String() string { return output.buffer.String() }

func (m *Manager) updateLlamaWithRecovery(ctx context.Context, options LlamaOptions, cause error) (State, error) {
	if m.Err != nil {
		fmt.Fprintln(m.Err, "警告: 未找到有效的受管运行时，将隔离当前状态后重新安装:", cause)
	}
	transaction, err := quarantineInvalidRuntime(m.Layout, cause)
	if err != nil {
		return State{}, err
	}
	result, updateErr := m.UpdateLlamaWithOptions(ctx, options)
	if updateErr == nil {
		if m.Out != nil {
			fmt.Fprintln(m.Out, "旧状态/运行时未自动删除，已保留为恢复备份:", transaction.directory)
		}
		return result, nil
	}
	if rollbackErr := rollbackRecovery(m.Layout, transaction); rollbackErr != nil {
		return State{}, fmt.Errorf("重新安装失败: %w；同时无法完整恢复已隔离的运行时: %v", updateErr, rollbackErr)
	}
	return State{}, updateErr
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if sameManagedPath(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func removeManagedRuntime(l layout.Layout, path string) error {
	if err := managedfs.Within(l.LlamaRuntimeDir, path); err != nil {
		return err
	}
	if sameManagedPath(path, l.LlamaRuntimeDir) {
		return errors.New("拒绝删除运行时根目录")
	}
	if err := managedfs.Validate(l.Root, path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := stableCleanupInfo(path)
	if err != nil {
		return err
	}
	if err := ensureCleanupTargetIsNotActive(l, path, info); err != nil {
		return err
	}
	quarantine := filepath.Join(filepath.Dir(path), ".llamalc-delete-"+randomToken())
	if err := os.Rename(path, quarantine); err != nil {
		return err
	}
	movedInfo, err := os.Lstat(quarantine)
	if err != nil || !os.SameFile(info, movedInfo) {
		return restoreQuarantinedPath(quarantine, path, errors.New("运行时文件身份在清理时发生变化"))
	}
	if err := removeCleanupTree(quarantine); err != nil {
		return fmt.Errorf("运行时递归清理未完成，未恢复可能已部分删除的目录；残留保留在 %s: %w", quarantine, err)
	}
	if runtime.GOOS != "windows" {
		directory, openErr := os.Open(filepath.Dir(path))
		if openErr != nil {
			return openErr
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func ensureCleanupTargetIsNotActive(l layout.Layout, path string, info os.FileInfo) error {
	state, exists, err := LoadState(l)
	if err != nil {
		return fmt.Errorf("无法确认清理目标不是活动运行时: %w", err)
	}
	if !exists || state.ActiveRuntime == "" {
		return nil
	}
	activePath := RuntimePath(l, state)
	if sameManagedPath(path, activePath) {
		return errors.New("拒绝删除活动运行时")
	}
	activeInfo, err := stableCleanupInfo(activePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("复核活动运行时身份: %w", err)
	}
	if os.SameFile(info, activeInfo) {
		return errors.New("拒绝删除与活动运行时身份相同的目录")
	}
	return nil
}

func (m *Manager) retryPendingCleanup(state State) (State, error) {
	if len(state.PendingCleanup) == 0 {
		return state, nil
	}
	if err := managedfs.EnsureDir(m.Layout.Root, m.Layout.RuntimeDir, 0o700); err != nil {
		return state, err
	}
	lock, err := acquireLlamaInstallLock(m.Layout)
	if err != nil {
		return state, err
	}
	stopHeartbeat := startOwnedLockHeartbeat(lock)
	defer func() {
		stopHeartbeat()
		_ = os.RemoveAll(lock)
	}()
	latest, exists, err := LoadState(m.Layout)
	if err != nil {
		return state, err
	}
	if !exists {
		return latest, nil
	}
	return retryPendingCleanupLocked(m.Layout, latest)
}

func retryPendingCleanupLocked(l layout.Layout, state State) (State, error) {
	if len(state.PendingCleanup) == 0 {
		return state, nil
	}
	remaining := state.PendingCleanup[:0]
	var failures []string
	for _, relative := range state.PendingCleanup {
		path := filepath.Join(l.Root, filepath.FromSlash(relative))
		if err := removeManagedRuntime(l, path); err != nil {
			remaining = append(remaining, relative)
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
		}
	}
	state.PendingCleanup = remaining
	if err := SaveState(l, state); err != nil {
		return state, fmt.Errorf("保存 pending_cleanup: %w", err)
	}
	if len(failures) > 0 {
		return state, errors.New("清理 pending_cleanup 失败: " + strings.Join(failures, "；"))
	}
	return state, nil
}

func quarantineInvalidRuntime(l layout.Layout, cause error) (recoveryTransaction, error) {
	return quarantineInvalidRuntimeTo(l, cause, "")
}

func quarantineInvalidRuntimeTo(l layout.Layout, cause error, directory string) (recoveryTransaction, error) {
	if err := managedfs.EnsureDir(l.Root, l.RecoveryDir, 0o700); err != nil {
		return recoveryTransaction{}, err
	}
	if directory == "" {
		name := "repair-" + time.Now().UTC().Format("20060102T150405Z") + "-" + randomToken()
		directory = filepath.Join(l.RecoveryDir, name)
	}
	if err := managedfs.Within(l.RecoveryDir, directory); err != nil || sameManagedPath(directory, l.RecoveryDir) {
		return recoveryTransaction{}, errors.New("恢复目录超出受管 recovery 路径")
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return recoveryTransaction{}, err
	}
	transaction := recoveryTransaction{directory: directory}
	failed := true
	defer func() {
		if failed {
			_ = rollbackRecovery(l, transaction)
		}
	}()
	if _, err := os.Lstat(l.UpdateStateFile); err == nil {
		stateBackup := filepath.Join(directory, "update.json.corrupt")
		if err := os.Rename(l.UpdateStateFile, stateBackup); err != nil {
			return recoveryTransaction{}, err
		}
		transaction.stateBackup = stateBackup
	} else if !errors.Is(err, os.ErrNotExist) {
		return recoveryTransaction{}, err
	}
	if entries, err := os.ReadDir(l.LlamaRuntimeDir); err == nil && len(entries) > 0 {
		runtimeBackup := filepath.Join(directory, "llama.cpp")
		if err := os.Rename(l.LlamaRuntimeDir, runtimeBackup); err != nil {
			return recoveryTransaction{}, err
		}
		transaction.runtimeBackup = runtimeBackup
		if err := managedfs.EnsureDir(l.Root, l.LlamaRuntimeDir, 0o700); err != nil {
			return recoveryTransaction{}, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return recoveryTransaction{}, err
	}
	metadata := recoveryMetadata{Schema: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339), OriginalPath: l.LlamaRuntimeDir, Reason: cause.Error()}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return recoveryTransaction{}, err
	}
	if err := managedfs.AtomicWrite(l.Root, filepath.Join(directory, ".llamalc-recovery.json"), append(data, '\n'), 0o600); err != nil {
		return recoveryTransaction{}, err
	}
	failed = false
	return transaction, nil
}

func rollbackRecovery(l layout.Layout, transaction recoveryTransaction) error {
	var failures []string
	if transaction.runtimeBackup != "" {
		if err := os.RemoveAll(l.LlamaRuntimeDir); err != nil {
			failures = append(failures, "移除新运行时: "+err.Error())
		} else if err := os.Rename(transaction.runtimeBackup, l.LlamaRuntimeDir); err != nil {
			failures = append(failures, "恢复旧运行时: "+err.Error())
		}
	}
	if transaction.stateBackup != "" {
		_ = os.Remove(l.UpdateStateFile)
		if err := os.Rename(transaction.stateBackup, l.UpdateStateFile); err != nil {
			failures = append(failures, "恢复旧状态: "+err.Error())
		}
	}
	if len(failures) == 0 && transaction.directory != "" {
		_ = os.RemoveAll(transaction.directory)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}
func randomToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		fallback := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())))
		copy(b[:], fallback[:8])
	}
	return hex.EncodeToString(b[:])
}
func mustRel(root, path string) string {
	p, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return p
}

// PrepareLauncher downloads and validates a v1 archive, then returns staged binaries.
func (m *Manager) PrepareLauncher(ctx context.Context) (tag, launcherPath, updaterPath, staging string, err error) {
	return m.PrepareLauncherWithOptions(ctx, LauncherOptions{})
}

func (m *Manager) PrepareLauncherWithOptions(ctx context.Context, options LauncherOptions) (tag, launcherPath, updaterPath, staging string, err error) {
	plan, e := m.PrepareLauncherPlan(ctx, options)
	if e != nil {
		err = e
		return
	}
	return m.stageLauncherPlan(ctx, plan)
}

func (m *Manager) stageLauncherPlan(ctx context.Context, plan *LauncherPlan) (tag, launcherPath, updaterPath, staging string, err error) {
	if e := m.verifyLauncherPlan(plan); e != nil {
		err = e
		return
	}
	r, asset, sumsAsset := plan.Release, plan.Asset, plan.SumsAsset
	var e error
	token := randomToken()
	staging = filepath.Join(m.Layout.RuntimeDir, ".launcher-update-"+token)
	if e = createOwnedTempDirectory(m.Layout, staging, token, "launcher-update-staging"); e != nil {
		err = e
		return
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)
		}
	}()
	archive := filepath.Join(staging, asset.Name)
	sumsPath := filepath.Join(staging, sumsAsset.Name)
	if e = m.Source.Download(ctx, sumsAsset, sumsPath); e != nil {
		err = e
		return
	}
	sumsData, e := os.ReadFile(sumsPath)
	if e != nil {
		err = e
		return
	}
	sums, e := release.ParseSHA256Sums(sumsData)
	if e != nil {
		err = e
		return
	}
	assetDigest, _ := release.Digest(asset.Digest)
	if sums[asset.Name] != assetDigest {
		err = fmt.Errorf("SHA256SUMS.txt 与 %s 的 GitHub digest 不一致", asset.Name)
		return
	}
	if e = m.Source.Download(ctx, asset, archive); e != nil {
		err = e
		return
	}
	if m.Out != nil {
		fmt.Fprintln(m.Out, "正在安全解压:", asset.Name)
	}
	if e = release.ExtractWithBudget(archive, filepath.Join(staging, "extract"), release.NewExtractBudget(20_000, 8<<30)); e != nil {
		err = e
		return
	}
	if m.Out != nil {
		fmt.Fprintln(m.Out, "解压完成:", asset.Name)
	}
	ext := ""
	if m.GOOS == "windows" {
		ext = ".exe"
	}
	base := filepath.Join(staging, "extract", "LlamaLc", "bin")
	launcherPath = filepath.Join(base, "llamalc"+ext)
	updaterPath = filepath.Join(base, "llamaup"+ext)
	if e = validateBundle(filepath.Join(staging, "extract"), launcherPath, updaterPath); e != nil {
		err = e
		return
	}
	for _, program := range []string{launcherPath, updaterPath} {
		if e = probeBundleVersion(ctx, program, r.Tag); e != nil {
			err = e
			return
		}
	}
	tag = r.Tag
	return
}

type ownershipMarker struct {
	Schema int    `json:"schema"`
	Token  string `json:"token"`
	Kind   string `json:"kind"`
}

type lockOwner struct {
	Schema   int    `json:"schema"`
	Token    string `json:"token"`
	Kind     string `json:"kind"`
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

func createOwnedTempDirectory(l layout.Layout, directory, token, kind string) error {
	if len(token) != 16 || !safeHex(token) {
		return errors.New("临时目录 token 无效")
	}
	prefix := map[string]string{"llama-runtime-staging": ".staging-", "launcher-update-staging": ".launcher-update-", "llama-install-lock": ".install-lock-", "llama-global-install-lock": ".llama-lock-", "launcher-install-lock": ".launcher-lock-"}[kind]
	if prefix == "" || filepath.Base(directory) != prefix+token {
		return errors.New("临时目录名称或类型无效")
	}
	if err := managedfs.Within(l.Root, directory); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return err
	}
	marker := ownershipMarker{Schema: 1, Token: token, Kind: kind}
	data, err := json.Marshal(marker)
	if err != nil {
		_ = os.Remove(directory)
		return err
	}
	if err := managedfs.AtomicWrite(l.Root, filepath.Join(directory, ownershipMarkerName), append(data, '\n'), 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return err
	}
	if kind == "llama-global-install-lock" || kind == "launcher-install-lock" {
		if err := setOwnedLockOwner(l, directory, token, kind, os.Getpid()); err != nil {
			_ = os.RemoveAll(directory)
			return fmt.Errorf("记录更新锁持有进程: %w", err)
		}
	}
	return nil
}

func setOwnedLockOwner(l layout.Layout, directory, token, kind string, pid int) error {
	identity, alive, err := procinfo.Identity(pid)
	if err != nil {
		return fmt.Errorf("查询锁持有进程: %w", err)
	}
	if !alive || identity == "" {
		return errors.New("锁持有进程已经退出")
	}
	owner := lockOwner{Schema: 1, Token: token, Kind: kind, PID: pid, Identity: identity}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	return managedfs.AtomicWrite(l.Root, filepath.Join(directory, lockOwnerName), append(data, '\n'), 0o600)
}

func reclaimDeadOwnedLock(l layout.Layout, directory, prefix, kind string) (reclaimed, ownerLive bool, err error) {
	marker, valid := readOwnershipMarker(directory, prefix, kind)
	if !valid {
		return false, false, nil
	}
	owner, valid := readLockOwner(directory, marker.Token, kind)
	if !valid {
		return false, false, nil
	}
	identity, alive, err := procinfo.Identity(owner.PID)
	if err != nil {
		// Unknown process state cannot authorize deletion. Heartbeat age and
		// the 24-hour housekeeping rule remain the conservative fallback.
		return false, false, nil
	}
	if alive && identity == owner.Identity {
		return false, true, nil
	}
	info, err := stableCleanupInfo(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, false, nil
		}
		return false, false, err
	}
	_, snapshot, err := inspectCleanupPath(directory)
	if err != nil {
		return false, false, err
	}
	if err := removeAutomaticCleanupPath(l, directory, info, snapshot); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func readLockOwner(directory, token, kind string) (lockOwner, bool) {
	path := filepath.Join(directory, lockOwnerName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return lockOwner{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return lockOwner{}, false
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() > 4096 || !os.SameFile(info, openedInfo) {
		return lockOwner{}, false
	}
	var owner lockOwner
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&owner) != nil {
		return lockOwner{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return lockOwner{}, false
	}
	if owner.Schema != 1 || owner.Token != token || owner.Kind != kind || owner.PID <= 0 || owner.Identity == "" {
		return lockOwner{}, false
	}
	return owner, true
}

func acquireLlamaInstallLock(l layout.Layout) (string, error) {
	digest := sha256.Sum256([]byte(filepath.Clean(l.Root) + "\x00llama.cpp"))
	token := hex.EncodeToString(digest[:8])
	directory := filepath.Join(l.RuntimeDir, ".llama-lock-"+token)
	for attempt := 0; attempt < 2; attempt++ {
		if err := createOwnedTempDirectory(l, directory, token, "llama-global-install-lock"); err == nil {
			return directory, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		reclaimed, _, err := reclaimDeadOwnedLock(l, directory, ".llama-lock-", "llama-global-install-lock")
		if err != nil {
			return "", fmt.Errorf("回收异常退出的 llama.cpp 更新锁: %w", err)
		}
		if !reclaimed {
			return "", errors.New("另一个进程正在安装或更新 llama.cpp，请稍后重试")
		}
	}
	return "", errors.New("llama.cpp 更新锁状态持续变化，请重试")
}

func startOwnedLockHeartbeat(directory string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				_ = os.Chtimes(filepath.Join(directory, ownershipMarkerName), now, now)
				_ = os.Chtimes(directory, now, now)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func safeHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
func probeBundleVersion(ctx context.Context, program, tag string) error {
	probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeContext, program, "--version")
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err != nil {
		if errors.Is(probeContext.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("校验 %s 版本超时", filepath.Base(program))
		}
		return fmt.Errorf("校验 %s 版本: %w", filepath.Base(program), err)
	}
	found := false
	for _, line := range strings.Split(strings.ReplaceAll(output.String(), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(strings.TrimPrefix(line, "Version:")) == tag && strings.HasPrefix(strings.TrimSpace(line), "Version:") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s 未报告目标版本 %s", filepath.Base(program), tag)
	}
	return nil
}
func validateBundle(root, launcherPath, updaterPath string) error {
	expected := map[string]bool{filepath.Clean(launcherPath): true, filepath.Clean(updaterPath): true}
	seen := 0
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("启动器归档包含符号链接")
		}
		if e.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || !expected[filepath.Clean(path)] {
			return fmt.Errorf("启动器归档包含意外文件: %s", path)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != 2 {
		return errors.New("启动器归档必须仅包含 llamalc 和 llamaup")
	}
	return nil
}

// StartLauncherUpdate stages both programs in bin and starts llamaup. The caller
// must exit promptly after this returns so the updater can complete the handoff.
func (m *Manager) StartLauncherUpdate(ctx context.Context) (string, error) {
	return m.StartLauncherUpdateWithOptions(ctx, LauncherOptions{})
}

func (m *Manager) StartLauncherUpdateWithOptions(ctx context.Context, options LauncherOptions) (string, error) {
	plan, err := m.PrepareLauncherPlan(ctx, options)
	if err != nil {
		return "", err
	}
	return m.ApplyLauncher(ctx, plan)
}

func (m *Manager) ApplyLauncher(ctx context.Context, plan *LauncherPlan) (string, error) {
	if err := m.verifyLauncherPlan(plan); err != nil {
		return "", err
	}
	launcherLock, launcherLockToken, err := acquireLauncherInstallLock(m.Layout)
	if err != nil {
		return "", err
	}
	lockHandedOff := false
	defer func() {
		if !lockHandedOff {
			_ = os.RemoveAll(launcherLock)
		}
	}()
	stopHeartbeat := startOwnedLockHeartbeat(launcherLock)
	defer stopHeartbeat()
	if state, exists, loadErr := LoadState(m.Layout); loadErr == nil && exists && len(state.PendingCleanup) > 0 {
		if _, cleanupErr := m.retryPendingCleanup(state); cleanupErr != nil {
			return "", cleanupErr
		}
	}
	tag, newLauncher, newUpdater, staging, err := m.stageLauncherPlan(ctx, plan)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err = m.verifyLauncherPlan(plan); err != nil {
		return "", err
	}
	token := randomToken()
	ext := ""
	if m.GOOS == "windows" {
		ext = ".exe"
	}
	launcherName := ".llamalc-new-" + token + ext
	updaterName := ".llamaup-new-" + token + ext
	launcherStage := filepath.Join(m.Layout.Bin, launcherName)
	updaterStage := filepath.Join(m.Layout.Bin, updaterName)
	if err = managedfs.CopyFile(m.Layout.Root, newLauncher, launcherStage, 0o755); err != nil {
		return "", err
	}
	if err = managedfs.CopyFile(m.Layout.Root, newUpdater, updaterStage, 0o755); err != nil {
		_ = os.Remove(launcherStage)
		return "", err
	}
	args := []string{"apply-update", "--launcher-source-name", launcherName, "--updater-source-name", updaterName, "--release-version", tag, "--wait-parent-pid", strconv.Itoa(os.Getpid()), "--lock-token", launcherLockToken}
	runnerName := ".llamaup-run-" + token + ext
	runner := filepath.Join(m.Layout.Bin, runnerName)
	if err = managedfs.CopyFile(m.Layout.Root, newUpdater, runner, 0o755); err != nil {
		_ = os.Remove(launcherStage)
		_ = os.Remove(updaterStage)
		return "", err
	}
	cmd := exec.Command(runner, args...)
	cmd.Dir = m.Layout.Root
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		_ = os.Remove(launcherStage)
		_ = os.Remove(updaterStage)
		_ = os.Remove(runner)
		return "", err
	}
	if err = setOwnedLockOwner(m.Layout, launcherLock, launcherLockToken, "launcher-install-lock", cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.Remove(launcherStage)
		_ = os.Remove(updaterStage)
		_ = os.Remove(runner)
		return "", fmt.Errorf("交接启动器更新锁: %w", err)
	}
	_ = cmd.Process.Release()
	lockHandedOff = true
	return tag, nil
}

func (m *Manager) ApplyLauncherUpdate(ctx context.Context, plan *LauncherPlan) (string, error) {
	return m.ApplyLauncher(ctx, plan)
}

func acquireLauncherInstallLock(l layout.Layout) (string, string, error) {
	digest := sha256.Sum256([]byte(filepath.Clean(l.Root)))
	token := hex.EncodeToString(digest[:8])
	directory := filepath.Join(l.RuntimeDir, ".launcher-lock-"+token)
	for attempt := 0; attempt < 2; attempt++ {
		if err := createOwnedTempDirectory(l, directory, token, "launcher-install-lock"); err == nil {
			return directory, token, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", "", err
		}
		reclaimed, _, err := reclaimDeadOwnedLock(l, directory, ".launcher-lock-", "launcher-install-lock")
		if err != nil {
			return "", "", fmt.Errorf("回收异常退出的启动器更新锁: %w", err)
		}
		if !reclaimed {
			return "", "", errors.New("另一个启动器更新正在进行，请稍后重试")
		}
	}
	return "", "", errors.New("启动器更新锁状态持续变化，请重试")
}
