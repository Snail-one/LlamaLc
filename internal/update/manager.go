package update

import (
	"context"
	"crypto/rand"
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

type Source interface {
	Latest(context.Context, string) (release.GitHubRelease, error)
	Download(context.Context, release.Asset, string) error
}
type Manager struct {
	Layout       layout.Layout
	Source       Source
	GOOS, GOARCH string
	Out, Err     io.Writer
}
type CheckResult struct {
	Component, Installed, Latest string
	Available                    bool
}

func NewManager(l layout.Layout, source Source) *Manager {
	return &Manager{Layout: l, Source: source, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
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
	if target == "all" || target == "llama" {
		r, err := m.Source.Latest(ctx, LlamaRepository)
		if err != nil {
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
		cmp, e := CompareSemVer(buildversion.Version, r.Tag)
		if e != nil {
			return nil, fmt.Errorf("当前启动器版本 %q 不是发布 SemVer；开发构建不支持自更新: %w", buildversion.Version, e)
		}
		out = append(out, CheckResult{Component: "launcher", Installed: buildversion.Version, Latest: r.Tag, Available: cmp < 0})
	}
	return out, nil
}

func (m *Manager) UpdateLlama(ctx context.Context, backend string, reinstall bool) (State, error) {
	current, exists, err := LoadState(m.Layout)
	if err != nil {
		return m.updateLlamaWithRecovery(ctx, backend, reinstall, err)
	}
	if !exists {
		entries, readErr := os.ReadDir(m.Layout.LlamaRuntimeDir)
		if readErr == nil && len(entries) > 0 {
			return m.updateLlamaWithRecovery(ctx, backend, reinstall, errors.New("更新状态不存在，但运行时目录中仍有内容"))
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return State{}, readErr
		}
	}
	r, err := m.Source.Latest(ctx, LlamaRepository)
	if err != nil {
		return State{}, err
	}
	if _, err = CompareLlamaTag(r.Tag, r.Tag); err != nil {
		return State{}, err
	}
	if exists && current.LlamaTag != "" {
		cmp, e := CompareLlamaTag(current.LlamaTag, r.Tag)
		if e != nil {
			return State{}, e
		}
		if cmp > 0 {
			return State{}, fmt.Errorf("拒绝从 %s 降级到 %s", current.LlamaTag, r.Tag)
		}
		if cmp == 0 && !reinstall {
			return current, fmt.Errorf("%w: llama.cpp %s；使用 --reinstall 重装", ErrAlreadyCurrent, r.Tag)
		}
		if backend == "" {
			backend = current.Backend
		}
	}
	options, err := release.LlamaAssets(r, m.GOOS, m.GOARCH)
	if err != nil {
		return State{}, err
	}
	selected := release.Backend{}
	for _, o := range options {
		if strings.EqualFold(o.ID, backend) {
			selected = o
			break
		}
	}
	if selected.ID == "" {
		available := make([]string, len(options))
		for i, o := range options {
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
	if err = os.Mkdir(staging, 0o700); err != nil {
		return State{}, err
	}
	defer os.RemoveAll(staging)
	var installed []InstalledAsset
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
		if err = release.Extract(archive, extract); err != nil {
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
	if _, err = llama.ProbeVersion(ctx, rt.Server); err != nil {
		return State{}, err
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
		backup = target + ".reinstall-" + token
		if err = os.Rename(target, backup); err != nil {
			return State{}, err
		}
		current.PendingCleanup = append(current.PendingCleanup, filepath.ToSlash(mustRel(m.Layout.Root, backup)))
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
	if current.ActiveRuntime != "" && filepath.Clean(current.ActiveRuntime) != filepath.Clean(runtimeRelative(selected.ID, r.Tag)) {
		current.PendingCleanup = append(current.PendingCleanup, current.ActiveRuntime)
	}
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

func (m *Manager) updateLlamaWithRecovery(ctx context.Context, backend string, reinstall bool, cause error) (State, error) {
	if m.Err != nil {
		fmt.Fprintln(m.Err, "警告: 未找到有效的受管运行时，将隔离当前状态后重新安装:", cause)
	}
	transaction, err := quarantineInvalidRuntime(m.Layout, cause)
	if err != nil {
		return State{}, err
	}
	result, updateErr := m.UpdateLlama(ctx, backend, reinstall)
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
		transaction.stateBackup = filepath.Join(directory, "update.json.corrupt")
		if err := os.Rename(l.UpdateStateFile, transaction.stateBackup); err != nil {
			return recoveryTransaction{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return recoveryTransaction{}, err
	}
	if entries, err := os.ReadDir(l.LlamaRuntimeDir); err == nil && len(entries) > 0 {
		transaction.runtimeBackup = filepath.Join(directory, "llama.cpp")
		if err := os.Rename(l.LlamaRuntimeDir, transaction.runtimeBackup); err != nil {
			return recoveryTransaction{}, err
		}
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
		return fmt.Sprint(os.Getpid())
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
	r, e := m.Source.Latest(ctx, LauncherRepository)
	if e != nil {
		err = e
		return
	}
	if cmp, e := CompareSemVer(buildversion.Version, r.Tag); e != nil {
		err = fmt.Errorf("当前启动器版本 %q 不是发布 SemVer；开发构建不支持自更新: %w", buildversion.Version, e)
		return
	} else if cmp >= 0 {
		err = fmt.Errorf("%w: 启动器 %s", ErrAlreadyCurrent, buildversion.Version)
		return
	}
	asset, e := release.LauncherAsset(r, m.GOOS, m.GOARCH)
	if e != nil {
		err = e
		return
	}
	staging = filepath.Join(m.Layout.RuntimeDir, ".launcher-update-"+randomToken())
	if e = os.MkdirAll(staging, 0o700); e != nil {
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
	if asset.Size <= 0 || sumsAsset.Size <= 0 || asset.Size > 2<<30 || sumsAsset.Size > 2<<30 {
		err = errors.New("启动器资产大小无效或超过 2 GiB")
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
	if e = release.Extract(archive, filepath.Join(staging, "extract")); e != nil {
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
func probeBundleVersion(ctx context.Context, program, tag string) error {
	cmd := exec.CommandContext(ctx, program, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("校验 %s 版本: %w", filepath.Base(program), err)
	}
	if !strings.Contains(string(output), tag) {
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
	tag, newLauncher, newUpdater, staging, err := m.PrepareLauncher(ctx)
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
