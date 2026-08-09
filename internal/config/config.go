// Package config owns the strict schema-1 JSON configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

const Schema = 1
const maxSize = 1 << 20

type Config struct {
	Schema    int             `json:"schema"`
	Runtime   RuntimeConfig   `json:"runtime"`
	API       APIConfig       `json:"api"`
	Embedding EmbeddingConfig `json:"embedding"`
	Router    RouterConfig    `json:"router"`
}
type RuntimeConfig struct {
	GPULayers      string `json:"gpu_layers"`
	ContextSize    int    `json:"context_size"`
	Threads        int    `json:"threads"`
	BatchSize      int    `json:"batch_size"`
	UBatchSize     int    `json:"ubatch_size"`
	FlashAttention string `json:"flash_attention"`
}
type APIConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Parallel int    `json:"parallel"`
	UI       bool   `json:"ui"`
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

func Default() Config {
	return Config{
		Schema:    Schema,
		Runtime:   RuntimeConfig{GPULayers: "auto", ContextSize: 0, Threads: -1, BatchSize: 2048, UBatchSize: 512, FlashAttention: "auto"},
		API:       APIConfig{Host: "127.0.0.1", Port: 29856, Parallel: -1, UI: false},
		Embedding: EmbeddingConfig{Pooling: "last", BatchSize: 8192, UBatchSize: 8192, Normalize: 2},
		Router:    RouterConfig{ModelsMax: 1, Autoload: true},
	}
}

func Load(l layout.Layout) (Config, bool, error) {
	if err := managedfs.Validate(l.Root, l.ConfigFile, true); err != nil {
		return Config{}, false, err
	}
	f, err := os.Open(l.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), true, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("读取配置: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Config{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return Config{}, false, fmt.Errorf("配置文件无效或超过 %d 字节", maxSize)
	}
	_ = os.Chmod(l.ConfigFile, 0o600)
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return Config{}, false, err
	}
	cfg, err := Parse(data)
	return cfg, false, err
}

func Parse(data []byte) (Config, error) {
	var envelope struct {
		Schema *int `json:"schema"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Config{}, fmt.Errorf("配置 JSON 损坏: %w", err)
	}
	if envelope.Schema == nil || *envelope.Schema != Schema {
		return Config{}, fmt.Errorf("配置 schema 必须为 %d", Schema)
	}
	cfg := Default()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("配置字段无效: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("配置包含多余 JSON 内容")
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Validate(c Config) error {
	if c.Schema != Schema {
		return fmt.Errorf("配置 schema 必须为 %d", Schema)
	}
	if strings.TrimSpace(c.Runtime.GPULayers) == "" {
		return errors.New("runtime.gpu_layers 不能为空")
	}
	if c.Runtime.GPULayers != "auto" {
		if _, err := parseInteger(c.Runtime.GPULayers, -1); err != nil {
			return errors.New("runtime.gpu_layers 必须为 auto 或不小于 -1 的整数")
		}
	}
	if c.Runtime.ContextSize < 0 {
		return errors.New("runtime.context_size 不能小于 0")
	}
	if c.Runtime.Threads < -1 || c.Runtime.Threads == 0 {
		return errors.New("runtime.threads 必须为 -1 或正整数")
	}
	if c.Runtime.BatchSize <= 0 || c.Runtime.UBatchSize <= 0 || c.Runtime.UBatchSize > c.Runtime.BatchSize {
		return errors.New("runtime batch_size/ubatch_size 无效")
	}
	switch c.Runtime.FlashAttention {
	case "auto", "on", "off":
	default:
		return errors.New("runtime.flash_attention 必须为 auto、on 或 off")
	}
	if strings.TrimSpace(c.API.Host) == "" {
		return errors.New("api.host 不能为空")
	}
	if c.API.Port < 1 || c.API.Port > 65535 {
		return errors.New("api.port 必须在 1 到 65535 之间")
	}
	if c.API.Parallel < -1 || c.API.Parallel == 0 {
		return errors.New("api.parallel 必须为 -1 或正整数")
	}
	switch c.Embedding.Pooling {
	case "none", "mean", "cls", "last", "rank":
	default:
		return errors.New("embedding.pooling 无效")
	}
	if c.Embedding.BatchSize <= 0 || c.Embedding.UBatchSize <= 0 || c.Embedding.UBatchSize > c.Embedding.BatchSize {
		return errors.New("embedding batch_size/ubatch_size 无效")
	}
	if c.Embedding.Normalize < -1 {
		return errors.New("embedding.normalize 不能小于 -1")
	}
	if c.Router.ModelsMax < 0 {
		return errors.New("router.models_max 不能小于 0")
	}
	return nil
}

func parseInteger(value string, minimum int64) (int64, error) {
	var n int64
	if value == "" {
		return 0, errors.New("empty")
	}
	negative := value[0] == '-'
	start := 0
	if negative {
		start = 1
	}
	if start == len(value) {
		return 0, errors.New("invalid")
	}
	for _, r := range value[start:] {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid")
		}
		n = n*10 + int64(r-'0')
	}
	if negative {
		n = -n
	}
	if n < minimum {
		return 0, errors.New("range")
	}
	return n, nil
}

func Save(l layout.Layout, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return managedfs.AtomicWrite(l.Root, l.ConfigFile, append(data, '\n'), 0o600)
}

func Ensure(l layout.Layout) (Config, bool, error) {
	cfg, missing, err := Load(l)
	if err != nil {
		return Config{}, false, err
	}
	if missing {
		if err := Save(l, cfg); err != nil {
			return Config{}, false, err
		}
	}
	return cfg, missing, nil
}
