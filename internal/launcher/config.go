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

const (
	ConfigDirectoryName = "config"
	DefaultConfigName   = "launcher.json"
)

var executablePath = os.Executable

func DefaultConfigPath(root string) string {
	return filepath.Join(root, ConfigDirectoryName, DefaultConfigName)
}

type Config struct {
	Server    ServerConfig    `json:"server"`
	Embedding EmbeddingConfig `json:"embedding"`
	Router    RouterConfig    `json:"router"`
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
		return "", fmt.Errorf("启动器必须放在 bin 目录下，当前目录: %s；请将 llama-launcher 可执行文件移动到 bin 目录后重试", executableDir)
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

func LoadConfig(root string) (Config, string, bool, error) {
	configPath := DefaultConfigPath(root)
	_, err := os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		config := DefaultConfig()
		return config, configPath, true, nil
	}
	if err != nil {
		return Config{}, configPath, false, fmt.Errorf("无法访问配置文件 %s: %w", configPath, err)
	}
	config, err := readConfig(configPath)
	if err != nil {
		return Config{}, configPath, false, err
	}
	return config, configPath, false, nil
}

func readConfig(configPath string) (Config, error) {
	config := DefaultConfig()
	f, err := os.Open(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("无法读取配置文件 %s: %w", configPath, err)
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("配置文件损坏 %s: %w", configPath, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("包含多余的 JSON 内容")
		}
		return Config{}, fmt.Errorf("配置文件损坏 %s: %w", configPath, err)
	}
	// An empty pooling value is treated as unset and receives the launcher default.
	if strings.TrimSpace(config.Embedding.Pooling) == "" {
		config.Embedding.Pooling = DefaultConfig().Embedding.Pooling
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func WriteDefaultConfig(path string, config Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("无法生成默认配置: %w", err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("无法写入默认配置 %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("无法写入默认配置 %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("无法保存默认配置 %s: %w", path, err)
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
	CLIFallback  string
	Models       string
	Embeddings   string
	Rerank       string
	Mmproj       string
	RouterManual string
	RouterAuto   string
}

func EnsureRuntimeDirectories(root string) ([]string, error) {
	directories := []struct {
		label string
		path  string
	}{
		{label: "config", path: filepath.Join(root, ConfigDirectoryName)},
		{label: "models", path: filepath.Join(root, "models")},
		{label: "embeddings", path: filepath.Join(root, "embeddings")},
		{label: "rerank", path: filepath.Join(root, "rerank")},
		{label: "mmproj", path: filepath.Join(root, "mmproj")},
	}

	seen := make(map[string]bool)
	var missing []string
	for _, directory := range directories {
		path := filepath.Clean(strings.TrimSpace(directory.path))
		if path == "." || path == "" {
			return nil, fmt.Errorf("%s 目录路径不能为空", directory.label)
		}
		key := strings.ToLower(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("%s 路径不是目录: %s", directory.label, path)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("无法检查 %s 目录 %s: %w", directory.label, path, err)
		}
		missing = append(missing, path)
	}

	var created []string
	for _, path := range missing {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return created, fmt.Errorf("无法创建运行目录 %s: %w", path, err)
		}
		created = append(created, path)
	}
	return created, nil
}

func ResolveFixedPaths(root, goos string) (ResolvedPaths, error) {
	serverName, cliName, cliFallback := "", "", ""
	switch goos {
	case "windows":
		serverName, cliName, cliFallback = "llama-server.exe", "llama-cli.exe", "llama.exe"
	case "linux":
		serverName, cliName, cliFallback = "llama-server", "llama-cli", "llama"
	default:
		return ResolvedPaths{}, fmt.Errorf("当前操作系统 %q 暂不支持，仅支持 Windows 和 Linux", goos)
	}
	return ResolvedPaths{
		Server:       filepath.Join(root, serverName),
		CLI:          filepath.Join(root, cliName),
		CLIFallback:  filepath.Join(root, cliFallback),
		Models:       filepath.Join(root, "models"),
		Embeddings:   filepath.Join(root, "embeddings"),
		Rerank:       filepath.Join(root, "rerank"),
		Mmproj:       filepath.Join(root, "mmproj"),
		RouterManual: filepath.Join(root, ConfigDirectoryName, "router-models.ini"),
		RouterAuto:   filepath.Join(root, ConfigDirectoryName, "router-models.auto.ini"),
	}, nil
}
