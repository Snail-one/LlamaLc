// Package layout defines every path owned by a LlamaLc deployment.
package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const RootName = "LlamaLc"

// Layout contains absolute paths for the fixed v1 deployment layout.
type Layout struct {
	Root, Bin, ConfigDir, RouterConfigDir, SecretsDir, StateDir, RouterStateDir string
	RuntimeDir, LlamaRuntimeDir, RecoveryDir, ModelsDir                         string
	GenerationModels, EmbeddingModels, RerankModels, MMProjModels               string
	ConfigFile, APIKeyFile, UpdateStateFile, RouterPreset, AutoRouterPreset     string
	Launcher, Updater                                                           string
}

func New(root, goos string) (Layout, error) {
	if strings.TrimSpace(root) == "" {
		return Layout{}, errors.New("部署根目录不能为空")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, fmt.Errorf("解析部署根目录: %w", err)
	}
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	if goos != "windows" && goos != "linux" {
		return Layout{}, fmt.Errorf("不支持的平台 %q", goos)
	}
	l := Layout{Root: filepath.Clean(abs)}
	l.Bin = filepath.Join(l.Root, "bin")
	l.ConfigDir = filepath.Join(l.Root, "config")
	l.RouterConfigDir = filepath.Join(l.ConfigDir, "router")
	l.SecretsDir = filepath.Join(l.Root, "secrets")
	l.StateDir = filepath.Join(l.Root, "state")
	l.RouterStateDir = filepath.Join(l.StateDir, "router")
	l.RuntimeDir = filepath.Join(l.Root, "runtime")
	l.LlamaRuntimeDir = filepath.Join(l.RuntimeDir, "llama.cpp")
	l.RecoveryDir = filepath.Join(l.RuntimeDir, "recovery")
	l.ModelsDir = filepath.Join(l.Root, "models")
	l.GenerationModels = filepath.Join(l.ModelsDir, "generation")
	l.EmbeddingModels = filepath.Join(l.ModelsDir, "embedding")
	l.RerankModels = filepath.Join(l.ModelsDir, "rerank")
	l.MMProjModels = filepath.Join(l.ModelsDir, "mmproj")
	l.ConfigFile = filepath.Join(l.ConfigDir, "llamalc.json")
	l.APIKeyFile = filepath.Join(l.SecretsDir, "api-key")
	l.UpdateStateFile = filepath.Join(l.StateDir, "update.json")
	l.RouterPreset = filepath.Join(l.RouterConfigDir, "models.ini")
	l.AutoRouterPreset = filepath.Join(l.RouterStateDir, "models.auto.ini")
	l.Launcher = filepath.Join(l.Bin, "llamalc"+ext)
	l.Updater = filepath.Join(l.Bin, "llamaup"+ext)
	return l, nil
}

func FromExecutable(executable, goos string) (Layout, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return Layout{}, fmt.Errorf("解析程序路径: %w", err)
	}
	dir := filepath.Dir(resolved)
	if !strings.EqualFold(filepath.Base(dir), "bin") {
		return Layout{}, fmt.Errorf("llamalc 必须位于 LlamaLc/bin，当前目录: %s", dir)
	}
	root := filepath.Dir(dir)
	validRootName := filepath.Base(root) == RootName
	if goos == "windows" {
		validRootName = strings.EqualFold(filepath.Base(root), RootName)
	}
	if !validRootName {
		return Layout{}, fmt.Errorf("部署根目录必须命名为 %s，当前为 %s", RootName, filepath.Base(root))
	}
	return New(root, goos)
}

func Detect() (Layout, error) {
	exe, err := os.Executable()
	if err != nil {
		return Layout{}, fmt.Errorf("确定程序路径: %w", err)
	}
	return FromExecutable(exe, runtime.GOOS)
}

func (l Layout) Directories() []string {
	return []string{l.Bin, l.ConfigDir, l.RouterConfigDir, l.SecretsDir, l.StateDir,
		l.RouterStateDir, l.LlamaRuntimeDir, l.RecoveryDir, l.GenerationModels,
		l.EmbeddingModels, l.RerankModels, l.MMProjModels}
}

// LegacyPaths reports old-layout artifacts without reading or modifying them.
func (l Layout) LegacyPaths() []string {
	candidates := []string{
		filepath.Join(l.Root, "data", "llama.cpp"), filepath.Join(l.Root, "config", "launcher.json"),
		filepath.Join(l.Root, "launcher.json"), filepath.Join(l.Root, "models"), filepath.Join(l.Root, "embeddings"),
		filepath.Join(l.Root, "rerank"), filepath.Join(l.Root, "mmproj"),
	}
	var found []string
	for _, p := range candidates {
		if p == l.ModelsDir { // The directory is v1-owned; only flat model files are legacy.
			entries, err := os.ReadDir(p)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					found = append(found, filepath.Join(p, e.Name()))
				}
			}
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			found = append(found, p)
		}
	}
	return found
}
