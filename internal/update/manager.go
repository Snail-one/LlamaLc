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
		state = m.retryPendingCleanup(state)
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
	s = m.retryPendingCleanup(s)
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
	backend, reinstall := strings.TrimSpace(options.Backend), options.Reinstall
	current, exists, err := LoadState(m.Layout)
	if err != nil {
		return m.updateLlamaWithRecovery(ctx, options, err)
	}
	if exists {
		current = m.retryPendingCleanup(current)
	}
	if !exists {
		entries, readErr := os.ReadDir(m.Layout.LlamaRuntimeDir)
		if readErr == nil && len(entries) > 0 {
			return m.updateLlamaWithRecovery(ctx, options, errors.New("更新状态不存在，但运行时目录中仍有内容"))
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return State{}, readErr
		}
	}
	r, err := m.release(ctx, LlamaRepository, options.Version)
	if err != nil {
		return State{}, err
	}
	if _, err = CompareLlamaTag(r.Tag, r.Tag); err != nil {
		return State{}, err
	}
	if exists && current.LlamaTag != "" {
		if backend == "" {
			backend = current.Backend
		}
		cmp, e := CompareLlamaTag(current.LlamaTag, r.Tag)
		if e != nil {
			return State{}, e
		}
		if cmp > 0 && !options.AllowDowngrade {
			return State{}, fmt.Errorf("拒绝从 %s 降级到 %s", current.LlamaTag, r.Tag)
		}
		if cmp == 0 && strings.EqualFold(backend, current.Backend) && !reinstall {
			return current, fmt.Errorf("%w: llama.cpp %s；使用 --reinstall 重装", ErrAlreadyCurrent, r.Tag)
		}
	}
	backends, err := release.LlamaAssets(r, m.GOOS, m.GOARCH)
	if err != nil {
		return State{}, err
	}
	selected := release.Backend{}
	for _, o := range backends {
		if strings.EqualFold(o.ID, backend) {
			selected = o
			break
		}
	}
	if selected.ID == "" {
		available := make([]string, len(backends))
		for i, o := range backends {
			available[i] = o.ID
		}
		if backend == "" {
			return State{}, fmt.Errorf("首次安装必须使用 --backend；可用值: %s", strings.Join(available, ", "))
		}
		return State{}, fmt.Errorf("后端 %q 不可用；可用值: %s", backend, strings.Join(available, ", "))
	}
	var totalDownload int64
	for _, asset := range selected.Assets {
		if asset.Size <= 0 || asset.Size > 2<<30 || asset.Size > (4<<30)-totalDownload {
			return State{}, errors.New("资产组合下载量超过 4 GiB 或大小无效")
		}
		totalDownload += asset.Size
	}
	if err := ensureFreeSpace(m.Layout.RuntimeDir, totalDownload*3); err != nil {
		return State{}, err
	}
	if !safeComponent.MatchString(r.Tag) || !safeComponent.MatchString(selected.ID) {
		return State{}, errors.New("Release tag 或后端 ID 不能安全用作目录名")
	}
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
	if !strings.Contains(strings.ToLower(detectedVersion), strings.ToLower(r.Tag)) {
		return State{}, fmt.Errorf("新运行时版本签名与目标 tag %s 不匹配: %s", r.Tag, detectedVersion)
	}
	target := filepath.Join(m.Layout.LlamaRuntimeDir, selected.ID, r.Tag)
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
	return current, nil
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

func (m *Manager) retryPendingCleanup(state State) State {
	if len(state.PendingCleanup) == 0 {
		return state
	}
	remaining := state.PendingCleanup[:0]
	for _, relative := range state.PendingCleanup {
		path := filepath.Join(m.Layout.Root, filepath.FromSlash(relative))
		if err := removeManagedRuntime(m.Layout, path); err != nil {
			remaining = append(remaining, relative)
		}
	}
	state.PendingCleanup = remaining
	_ = SaveState(m.Layout, state)
	return state
}

func quarantineInvalidRuntime(l layout.Layout, cause error) (recoveryTransaction, error) {
	if err := managedfs.EnsureDir(l.Root, l.RecoveryDir, 0o700); err != nil {
		return recoveryTransaction{}, err
	}
	name := "repair-" + time.Now().UTC().Format("20060102T150405Z") + "-" + randomToken()
	directory := filepath.Join(l.RecoveryDir, name)
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
	r, e := m.release(ctx, LauncherRepository, options.Version)
	if e != nil {
		err = e
		return
	}
	if _, e = CompareSemVer(r.Tag, r.Tag); e != nil {
		err = fmt.Errorf("目标启动器版本 %q 不是发布 SemVer: %w", r.Tag, e)
		return
	}
	// Development builds are intentionally allowed to move to a formal
	// release. Formal builds still use strict SemVer ordering.
	if strings.EqualFold(strings.TrimSpace(buildversion.Version), "dev") {
		// Any valid formal target is newer than a local development build for
		// update-policy purposes.
	} else if cmp, compareErr := CompareSemVer(buildversion.Version, r.Tag); compareErr != nil {
		err = fmt.Errorf("当前启动器版本 %q 不是发布 SemVer: %w", buildversion.Version, compareErr)
		return
	} else if cmp > 0 && !options.AllowDowngrade {
		err = fmt.Errorf("拒绝从 %s 降级到 %s", buildversion.Version, r.Tag)
		return
	} else if cmp == 0 && !options.Reinstall {
		err = fmt.Errorf("%w: 启动器 %s；使用 --reinstall 重装", ErrAlreadyCurrent, buildversion.Version)
		return
	}
	asset, e := release.LauncherAsset(r, m.GOOS, m.GOARCH)
	if e != nil {
		err = e
		return
	}
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
	sumsAsset, e := release.SHA256SumsAsset(r)
	if e != nil {
		err = e
		return
	}
	if asset.Size <= 0 || sumsAsset.Size <= 0 || asset.Size > 2<<30 || sumsAsset.Size > 4<<20 {
		err = errors.New("启动器资产大小无效，或 SHA256SUMS.txt 超过 4 MiB")
		return
	}
	if e = ensureFreeSpace(m.Layout.RuntimeDir, (asset.Size+sumsAsset.Size)*3); e != nil {
		err = e
		return
	}
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
	prefix := map[string]string{"llama-runtime-staging": ".staging-", "launcher-update-staging": ".launcher-update-"}[kind]
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
	if state, exists, loadErr := LoadState(m.Layout); loadErr == nil && exists {
		_ = m.retryPendingCleanup(state)
	}
	tag, newLauncher, newUpdater, staging, err := m.PrepareLauncherWithOptions(ctx, options)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
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
	args := []string{"apply-update", "--launcher-source-name", launcherName, "--updater-source-name", updaterName, "--release-version", tag, "--wait-parent-pid", strconv.Itoa(os.Getpid())}
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
	return tag, nil
}
