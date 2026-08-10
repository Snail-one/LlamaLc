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
	"github.com/Snail-one/LlamaLc/internal/release"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

const (
	LlamaRepository    = "ggml-org/llama.cpp"
	LauncherRepository = "Snail-one/LlamaLc"
)

var ErrAlreadyCurrent = errors.New("已是最新版本")

const ownershipMarkerName = ".llamalc-owned.json"

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
	if plan.CurrentExists && len(plan.Current.PendingCleanup) > 0 {
		cleaned, err := m.retryPendingCleanup(plan.Current)
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
	lock, err := acquireLlamaInstallLock(m.Layout, plan.Target)
	if err != nil {
		return State{}, err
	}
	defer os.RemoveAll(lock)
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
		if !exists || filepath.Clean(current.ActiveRuntime) != filepath.Clean(runtimeRelative(selected.ID, r.Tag)) {
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
			_ = os.Rename(backup, target)
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
	if err = SaveState(m.Layout, current); err != nil {
		_ = os.RemoveAll(target)
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return State{}, fmt.Errorf("运行时已安装但状态写入失败: %w", err)
	}
	// Once the new state is durable, old active content is no longer needed.
	// Delete it immediately; pending_cleanup is only a record of an actual
	// deletion failure, never the normal update path.
	var cleanupTargets []string
	if backup != "" {
		cleanupTargets = append(cleanupTargets, backup)
	}
	if previousRuntime != "" && filepath.Clean(previousRuntime) != filepath.Clean(current.ActiveRuntime) {
		cleanupTargets = append(cleanupTargets, filepath.Join(m.Layout.Root, filepath.FromSlash(previousRuntime)))
	}
	for _, old := range cleanupTargets {
		if removeErr := removeManagedRuntime(m.Layout, old); removeErr != nil {
			current.PendingCleanup = appendUnique(current.PendingCleanup, filepath.ToSlash(mustRel(m.Layout.Root, old)))
			if m.Err != nil {
				fmt.Fprintln(m.Err, "警告: 旧运行时暂时无法删除，已登记 pending_cleanup:", removeErr)
			}
		}
	}
	if err = SaveState(m.Layout, current); err != nil {
		return State{}, fmt.Errorf("运行时已切换，但无法保存清理状态: %w", err)
	}
	if _, err = ValidateActiveRuntime(ctx, m.Layout, m.GOOS); err != nil {
		return State{}, fmt.Errorf("安装后活动运行时校验失败: %w", err)
	}
	return current, nil
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
		if filepath.Clean(existing) == filepath.Clean(value) {
			return values
		}
	}
	return append(values, value)
}

func removeManagedRuntime(l layout.Layout, path string) error {
	if err := managedfs.Within(l.LlamaRuntimeDir, path); err != nil {
		return err
	}
	if filepath.Clean(path) == filepath.Clean(l.LlamaRuntimeDir) {
		return errors.New("拒绝删除运行时根目录")
	}
	if err := managedfs.Validate(l.Root, path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	quarantine := filepath.Join(filepath.Dir(path), ".llamalc-delete-"+randomToken())
	if err := os.Rename(path, quarantine); err != nil {
		return err
	}
	restore := true
	defer func() {
		if restore {
			_ = os.Rename(quarantine, path)
		}
	}()
	movedInfo, err := os.Lstat(quarantine)
	if err != nil || !os.SameFile(info, movedInfo) {
		return errors.New("运行时文件身份在清理时发生变化")
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return err
	}
	restore = false
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

func (m *Manager) retryPendingCleanup(state State) (State, error) {
	if len(state.PendingCleanup) == 0 {
		return state, nil
	}
	remaining := state.PendingCleanup[:0]
	var failures []string
	for _, relative := range state.PendingCleanup {
		path := filepath.Join(m.Layout.Root, filepath.FromSlash(relative))
		if err := removeManagedRuntime(m.Layout, path); err != nil {
			remaining = append(remaining, relative)
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
		}
	}
	state.PendingCleanup = remaining
	if err := SaveState(m.Layout, state); err != nil {
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
	if err := managedfs.Within(l.RecoveryDir, directory); err != nil || filepath.Clean(directory) == filepath.Clean(l.RecoveryDir) {
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

func createOwnedTempDirectory(l layout.Layout, directory, token, kind string) error {
	if len(token) != 16 || !safeHex(token) {
		return errors.New("临时目录 token 无效")
	}
	prefix := map[string]string{"llama-runtime-staging": ".staging-", "launcher-update-staging": ".launcher-update-", "llama-install-lock": ".install-lock-", "launcher-install-lock": ".launcher-lock-"}[kind]
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
	return nil
}

func acquireLlamaInstallLock(l layout.Layout, target string) (string, error) {
	digest := sha256.Sum256([]byte(filepath.Clean(target)))
	token := hex.EncodeToString(digest[:8])
	directory := filepath.Join(l.LlamaRuntimeDir, ".install-lock-"+token)
	if err := createOwnedTempDirectory(l, directory, token, "llama-install-lock"); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", errors.New("另一个进程正在安装同一 llama.cpp 目标，请稍后重试")
		}
		return "", err
	}
	return directory, nil
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
	if err := createOwnedTempDirectory(l, directory, token, "launcher-install-lock"); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", "", errors.New("另一个启动器更新正在进行，请稍后重试")
		}
		return "", "", err
	}
	return directory, token, nil
}
