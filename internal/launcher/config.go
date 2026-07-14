package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const DefaultConfigName = "launcher.json"

var executablePath = os.Executable

type Config struct {
	Paths     PathsConfig     `json:"paths"`
	Server    ServerConfig    `json:"server"`
	Embedding EmbeddingConfig `json:"embedding"`
	Router    RouterConfig    `json:"router"`
}

type PathsConfig struct {
	Server       string `json:"server"`
	CLI          string `json:"cli"`
	Models       string `json:"models"`
	Embeddings   string `json:"embeddings"`
	Rerank       string `json:"rerank"`
	Mmproj       string `json:"mmproj"`
	RouterManual string `json:"router_manual"`
	RouterAuto   string `json:"router_auto"`
}

type ServerConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	GPULayers      string `json:"n_gpu_layers"`
	ContextSize    int    `json:"ctx_size"`
	Threads        int    `json:"threads"`
	BatchSize      int    `json:"batch_size"`
	UBatchSize     int    `json:"ubatch_size"`
	FlashAttention string `json:"flash_attention"`
	Parallel       int    `json:"parallel"`
	UI             bool   `json:"ui"`
}

type EmbeddingConfig struct {
	Pooling    string `json:"pooling"`
	BatchSize  int    `json:"batch_size"`
	UBatchSize int    `json:"ubatch_size"`
	Normalize  int    `json:"normalize"`
}

type RouterConfig struct {
	ModelsMax int  `json:"models_max"`
	Autoload  bool `json:"autoload"`
}

func DefaultConfig() Config {
	return Config{
		Paths: PathsConfig{
			Server:       "llama-server.exe",
			CLI:          "llama-cli.exe",
			Models:       "models",
			Embeddings:   "embeddings",
			Rerank:       "rerank",
			Mmproj:       "mmproj",
			RouterManual: filepath.Join("bin", "router-models.ini"),
			RouterAuto:   filepath.Join("bin", "router-models.auto.ini"),
		},
		Server: ServerConfig{
			Host:           "127.0.0.1",
			Port:           29856,
			GPULayers:      "auto",
			Threads:        -1,
			BatchSize:      2048,
			UBatchSize:     512,
			FlashAttention: "auto",
			Parallel:       -1,
			UI:             false,
		},
		Embedding: EmbeddingConfig{Pooling: "last", BatchSize: 8192, UBatchSize: 8192, Normalize: 2},
		Router:    RouterConfig{ModelsMax: 1, Autoload: true},
	}
}

func ExecutableRoot() (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("无法确定启动器所在目录: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("无法解析启动器路径: %w", err)
	}
	return launcherRootFromExecutable(exe)
}

func launcherRootFromExecutable(executable string) (string, error) {
	executableDir := filepath.Dir(executable)
	if !strings.EqualFold(filepath.Base(executableDir), "bin") {
		return "", fmt.Errorf("启动器必须放在 bin 目录下，当前目录: %s；请将 llama-launcher.exe 移动到 bin 目录后重试", executableDir)
	}
	return filepath.Dir(executableDir), nil
}

func ResolvePath(root, value string) string {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) || isWindowsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(root, value))
}

var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func isWindowsAbs(value string) bool {
	return windowsDrivePath.MatchString(value) || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `//`)
}

func LoadOrCreateConfig(root, configPath string) (Config, string, bool, error) {
	if configPath == "" {
		configPath = filepath.Join(root, DefaultConfigName)
	} else {
		configPath = ResolvePath(root, configPath)
	}

	config := DefaultConfig()
	f, err := os.Open(configPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeDefaultConfig(configPath, config); err != nil {
			return Config{}, configPath, false, err
		}
		return config, configPath, true, nil
	}
	if err != nil {
		return Config{}, configPath, false, fmt.Errorf("无法读取配置文件 %s: %w", configPath, err)
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, configPath, false, fmt.Errorf("配置文件损坏 %s: %w", configPath, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("包含多余的 JSON 内容")
		}
		return Config{}, configPath, false, fmt.Errorf("配置文件损坏 %s: %w", configPath, err)
	}
	// Older generated configs used an empty pooling value. Treat that legacy
	// placeholder as unset so upgrades receive the new default.
	if strings.TrimSpace(config.Embedding.Pooling) == "" {
		config.Embedding.Pooling = DefaultConfig().Embedding.Pooling
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, configPath, false, err
	}
	return config, configPath, false, nil
}

func writeDefaultConfig(path string, config Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("无法创建配置目录: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("无法生成默认配置: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("无法写入默认配置 %s: %w", path, err)
	}
	return nil
}

func ValidateConfig(config Config) error {
	if strings.TrimSpace(config.Server.Host) == "" {
		return errors.New("配置错误: server.host 不能为空")
	}
	if err := ValidatePort(config.Server.Port); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateGPULayers(config.Server.GPULayers); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if config.Server.ContextSize < 0 {
		return errors.New("配置错误: server.ctx_size 不能小于 0")
	}
	if err := ValidateThreads(config.Server.Threads); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidatePositive("batch-size", config.Server.BatchSize); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateUBatchSize(config.Server.UBatchSize); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateBatchPair(config.Server.BatchSize, config.Server.UBatchSize); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateFlashAttention(config.Server.FlashAttention); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateParallel(config.Server.Parallel); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidatePooling(config.Embedding.Pooling); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateUBatchSize(config.Embedding.UBatchSize); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidatePositive("embedding.batch_size", config.Embedding.BatchSize); err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	if err := ValidateBatchPair(config.Embedding.BatchSize, config.Embedding.UBatchSize); err != nil {
		return fmt.Errorf("配置错误: Embedding %w", err)
	}
	if config.Embedding.Normalize < -1 {
		return errors.New("配置错误: embedding.normalize 必须不小于 -1")
	}
	if config.Router.ModelsMax < 0 {
		return errors.New("配置错误: router.models_max 不能小于 0")
	}
	return nil
}

func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口必须在 1 到 65535 之间，当前为 %d", port)
	}
	return nil
}

func ValidateGPULayers(value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "auto" || value == "all" {
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("GPU 层数必须是 auto、all 或非负整数，当前为 %q", value)
	}
	return nil
}

func ValidatePooling(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "mean", "cls", "last", "rank":
		return nil
	default:
		return fmt.Errorf("pooling 必须是 none、mean、cls、last、rank 之一或留空，当前为 %q", value)
	}
}

func ValidateUBatchSize(value int) error {
	return ValidatePositive("ubatch-size", value)
}

func ValidateBatchPair(batchSize, ubatchSize int) error {
	if batchSize < ubatchSize {
		return fmt.Errorf("batch-size (%d) 不能小于 ubatch-size (%d)", batchSize, ubatchSize)
	}
	return nil
}

func ValidatePositive(name string, value int) error {
	if value < 1 {
		return fmt.Errorf("%s 必须是正整数，当前为 %d", name, value)
	}
	return nil
}

func ValidateThreads(value int) error {
	if value != -1 && value < 1 {
		return fmt.Errorf("threads 必须是 -1（自动）或正整数，当前为 %d", value)
	}
	return nil
}

func ValidateParallel(value int) error {
	if value != -1 && value < 1 {
		return fmt.Errorf("parallel 必须是 -1（自动）或正整数，当前为 %d", value)
	}
	return nil
}

func ValidateFlashAttention(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "on", "off":
		return nil
	default:
		return fmt.Errorf("flash-attn 必须是 auto、on 或 off，当前为 %q", value)
	}
}

type ResolvedPaths struct {
	Server       string
	CLI          string
	Models       string
	Embeddings   string
	Rerank       string
	Mmproj       string
	RouterManual string
	RouterAuto   string
}

func (config Config) ResolvePaths(root string) ResolvedPaths {
	return ResolvedPaths{
		Server:       ResolvePath(root, config.Paths.Server),
		CLI:          ResolvePath(root, config.Paths.CLI),
		Models:       ResolvePath(root, config.Paths.Models),
		Embeddings:   ResolvePath(root, config.Paths.Embeddings),
		Rerank:       ResolvePath(root, config.Paths.Rerank),
		Mmproj:       ResolvePath(root, config.Paths.Mmproj),
		RouterManual: ResolvePath(root, config.Paths.RouterManual),
		RouterAuto:   ResolvePath(root, config.Paths.RouterAuto),
	}
}
