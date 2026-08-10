// Package update owns llama.cpp and LlamaLc update state and transactions.
package update

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

const StateSchema = 1

type InstalledAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}
type State struct {
	Schema          int              `json:"schema"`
	LauncherVersion string           `json:"launcher_version,omitempty"`
	LlamaTag        string           `json:"llama_tag,omitempty"`
	Backend         string           `json:"backend,omitempty"`
	ActiveRuntime   string           `json:"active_runtime,omitempty"`
	Assets          []InstalledAsset `json:"assets,omitempty"`
	PendingCleanup  []string         `json:"pending_cleanup,omitempty"`
}

var safeComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

func runtimeRelative(backend, version string) string {
	return filepath.ToSlash(filepath.Join("runtime", "llama.cpp", backend, version))
}
func RuntimePath(l layout.Layout, s State) string {
	return filepath.Join(l.Root, filepath.FromSlash(s.ActiveRuntime))
}

func sameManagedPath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func managedPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func ValidateState(l layout.Layout, s State) error {
	if s.Schema != StateSchema {
		return fmt.Errorf("不支持的更新状态 schema %d", s.Schema)
	}
	if s.LauncherVersion != "" && !strings.EqualFold(s.LauncherVersion, "dev") {
		if _, err := CompareSemVer(s.LauncherVersion, s.LauncherVersion); err != nil {
			return fmt.Errorf("更新状态中的 launcher_version 无效: %w", err)
		}
	}
	if s.ActiveRuntime == "" {
		if s.LlamaTag != "" || s.Backend != "" || len(s.Assets) > 0 {
			return errors.New("更新状态中的运行时字段不完整")
		}
	} else if !safeComponent.MatchString(s.LlamaTag) || !safeComponent.MatchString(s.Backend) {
		return errors.New("更新状态中的 tag 或 backend 无效")
	} else if _, err := CompareLlamaTag(s.LlamaTag, s.LlamaTag); err != nil {
		return err
	} else if expected := runtimeRelative(s.Backend, s.LlamaTag); filepath.Clean(filepath.FromSlash(s.ActiveRuntime)) != filepath.Clean(filepath.FromSlash(expected)) {
		return errors.New("活动运行时必须为 runtime/llama.cpp/<backend>/<version>")
	} else if len(s.Assets) == 0 {
		return errors.New("更新状态缺少资产摘要")
	} else if err := managedfs.Within(l.LlamaRuntimeDir, RuntimePath(l, s)); err != nil {
		return err
	}
	seenAssets := make(map[string]struct{})
	for _, a := range s.Assets {
		if filepath.Base(a.Name) != a.Name || a.Name == "" || len(a.SHA256) != 64 {
			return errors.New("更新状态包含无效资产")
		}
		if _, err := hex.DecodeString(a.SHA256); err != nil {
			return errors.New("更新状态包含非十六进制资产摘要")
		}
		key := strings.ToLower(a.Name)
		if _, exists := seenAssets[key]; exists {
			return errors.New("更新状态包含重复资产")
		}
		seenAssets[key] = struct{}{}
	}
	seenCleanup := make(map[string]struct{})
	for _, p := range s.PendingCleanup {
		absolute := filepath.Join(l.Root, filepath.FromSlash(p))
		if err := managedfs.Within(l.LlamaRuntimeDir, absolute); err != nil {
			return err
		}
		rel, err := filepath.Rel(l.LlamaRuntimeDir, absolute)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
		if len(parts) != 2 || !safeComponent.MatchString(parts[0]) || !safeComponent.MatchString(parts[1]) {
			return errors.New("待清理路径必须是完整的 <backend>/<version> 运行时目录")
		}
		if sameManagedPath(filepath.FromSlash(p), filepath.FromSlash(s.ActiveRuntime)) {
			return errors.New("待清理路径不能是活动运行时")
		}
		key := managedPathKey(filepath.FromSlash(p))
		if _, exists := seenCleanup[key]; exists {
			return errors.New("更新状态包含重复待清理路径")
		}
		seenCleanup[key] = struct{}{}
	}
	return nil
}
func LoadState(l layout.Layout) (State, bool, error) {
	if err := managedfs.Validate(l.Root, l.UpdateStateFile, true); err != nil {
		return State{}, false, err
	}
	f, err := os.Open(l.UpdateStateFile)
	if errors.Is(err, os.ErrNotExist) {
		return State{Schema: StateSchema}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return State{}, false, err
	}
	if info.Size() > 1<<20 {
		return State{}, false, errors.New("更新状态文件过大")
	}
	var s State
	dec := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&s); err != nil {
		return State{}, false, fmt.Errorf("更新状态损坏: %w", err)
	}
	var extra any
	if err = dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return State{}, false, errors.New("更新状态包含多余内容")
	}
	if err = ValidateState(l, s); err != nil {
		return State{}, false, err
	}
	return s, true, nil
}
func SaveState(l layout.Layout, s State) error {
	if err := ValidateState(l, s); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return managedfs.AtomicWrite(l.Root, l.UpdateStateFile, append(data, '\n'), 0o600)
}

func CompareLlamaTag(a, b string) (int, error) {
	parse := func(v string) (string, error) {
		v = strings.TrimSpace(v)
		if len(v) < 2 || (v[0] != 'b' && v[0] != 'B') {
			return "", fmt.Errorf("llama.cpp tag 必须为 b<数字>: %q", v)
		}
		digits := strings.TrimLeft(v[1:], "0")
		if digits == "" {
			digits = "0"
		}
		for _, r := range digits {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("llama.cpp tag 必须为 b<数字>: %q", v)
			}
		}
		return digits, nil
	}
	x, e := parse(a)
	if e != nil {
		return 0, e
	}
	y, e := parse(b)
	if e != nil {
		return 0, e
	}
	if len(x) < len(y) {
		return -1, nil
	}
	if len(x) > len(y) {
		return 1, nil
	}
	return strings.Compare(x, y), nil
}

type semver struct {
	major, minor, patch string
	pre                 []string
}

func CompareSemVer(a, b string) (int, error) {
	parse := func(v string) (semver, error) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		buildParts := strings.Split(v, "+")
		if len(buildParts) > 2 {
			return semver{}, errors.New("SemVer build metadata 无效")
		}
		if len(buildParts) == 2 {
			if err := validateIdentifiers(buildParts[1], false); err != nil {
				return semver{}, err
			}
		}
		v = buildParts[0]
		p := strings.SplitN(v, "-", 2)
		core := strings.Split(p[0], ".")
		if len(core) != 3 {
			return semver{}, errors.New("不是完整 SemVer")
		}
		for _, n := range core {
			if n == "" || (len(n) > 1 && n[0] == '0') {
				return semver{}, errors.New("SemVer 数字无效")
			}
			for _, r := range n {
				if r < '0' || r > '9' {
					return semver{}, errors.New("SemVer 数字无效")
				}
			}
		}
		s := semver{major: core[0], minor: core[1], patch: core[2]}
		if len(p) == 2 {
			if p[1] == "" {
				return s, errors.New("SemVer 预发布标识为空")
			}
			s.pre = strings.Split(p[1], ".")
			if err := validateIdentifiers(p[1], true); err != nil {
				return s, err
			}
		}
		return s, nil
	}
	x, e := parse(a)
	if e != nil {
		return 0, e
	}
	y, e := parse(b)
	if e != nil {
		return 0, e
	}
	cmpNum := func(x, y string) int {
		x = strings.TrimLeft(x, "0")
		y = strings.TrimLeft(y, "0")
		if x == "" {
			x = "0"
		}
		if y == "" {
			y = "0"
		}
		if len(x) < len(y) {
			return -1
		}
		if len(x) > len(y) {
			return 1
		}
		return strings.Compare(x, y)
	}
	for _, pair := range [][2]string{{x.major, y.major}, {x.minor, y.minor}, {x.patch, y.patch}} {
		if c := cmpNum(pair[0], pair[1]); c != 0 {
			return c, nil
		}
	}
	if len(x.pre) == 0 && len(y.pre) > 0 {
		return 1, nil
	}
	if len(y.pre) == 0 && len(x.pre) > 0 {
		return -1, nil
	}
	for i := 0; i < len(x.pre) && i < len(y.pre); i++ {
		xn, xe := isDigits(x.pre[i])
		yn, ye := isDigits(y.pre[i])
		var c int
		if xe && ye {
			c = cmpNum(xn, yn)
		} else if xe {
			c = -1
		} else if ye {
			c = 1
		} else {
			c = strings.Compare(x.pre[i], y.pre[i])
		}
		if c != 0 {
			return c, nil
		}
	}
	if len(x.pre) < len(y.pre) {
		return -1, nil
	}
	if len(x.pre) > len(y.pre) {
		return 1, nil
	}
	return 0, nil
}
func validateIdentifiers(value string, prerelease bool) error {
	for _, id := range strings.Split(value, ".") {
		if id == "" {
			return errors.New("SemVer 标识符为空")
		}
		numeric := true
		for _, r := range id {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return errors.New("SemVer 标识符包含非法字符")
			}
			if r < '0' || r > '9' {
				numeric = false
			}
		}
		if prerelease && numeric && len(id) > 1 && id[0] == '0' {
			return errors.New("SemVer 数字预发布标识不能有前导零")
		}
	}
	return nil
}
func isDigits(v string) (string, bool) {
	if v == "" {
		return v, false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return v, false
		}
	}
	return v, true
}
