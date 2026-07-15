package launcher

import (
	"fmt"
	"strconv"
	"strings"
)

type Mode string

const (
	ModeServe     Mode = "serve"
	ModeEmbedding Mode = "embedding"
	ModeRerank    Mode = "rerank"
	ModeRouter    Mode = "router"
	ModeChat      Mode = "chat"
)

type ServerOptions struct {
	Model          string
	Mmproj         string
	ImageMinTokens int
	ImageMaxTokens int
	Host           string
	Port           int
	GPULayers      string
	ContextSize    int
	Threads        int
	BatchSize      int
	UBatchSize     int
	FlashAttention string
	Parallel       int
	UI             bool
	Pooling        string
	Normalize      int
	NormalizeSet   bool
	APIKeyFile     string
	Extra          []string
}

type RouterOptions struct {
	Preset         string
	Host           string
	Port           int
	GPULayers      string
	ContextSize    int
	Threads        int
	BatchSize      int
	UBatchSize     int
	FlashAttention string
	Parallel       int
	UI             bool
	ModelsMax      int
	Autoload       bool
	APIKeyFile     string
	Extra          []string
}

type Command struct {
	Path string
	Args []string
	Dir  string
}

func BuildServerCommand(mode Mode, executable, root string, options ServerOptions) (Command, error) {
	if mode != ModeServe && mode != ModeEmbedding && mode != ModeRerank {
		return Command{}, fmt.Errorf("不支持的服务模式: %s", mode)
	}
	if options.Model == "" {
		return Command{}, fmt.Errorf("模型路径不能为空")
	}
	if strings.TrimSpace(options.Host) == "" {
		return Command{}, fmt.Errorf("监听地址不能为空")
	}
	if err := ValidatePort(options.Port); err != nil {
		return Command{}, err
	}
	if err := ValidateGPULayers(options.GPULayers); err != nil {
		return Command{}, err
	}
	if options.ContextSize < 0 || options.ImageMinTokens < 0 || options.ImageMaxTokens < 0 {
		return Command{}, fmt.Errorf("ctx-size、image-min-tokens 和 image-max-tokens 不能小于 0")
	}
	if err := validateRuntimeOptions(options, true); err != nil {
		return Command{}, err
	}
	if err := ValidatePooling(options.Pooling); err != nil {
		return Command{}, err
	}
	if mode == ModeEmbedding {
		if err := ValidatePositive("batch-size", options.BatchSize); err != nil {
			return Command{}, err
		}
		if err := ValidateUBatchSize(options.UBatchSize); err != nil {
			return Command{}, err
		}
		if options.NormalizeSet && options.Normalize < -1 {
			return Command{}, fmt.Errorf("embd-normalize 必须不小于 -1")
		}
	}
	if options.BatchSize > 0 && options.UBatchSize > 0 {
		if err := ValidateBatchPair(options.BatchSize, options.UBatchSize); err != nil {
			return Command{}, err
		}
	}

	args := []string{"--model", options.Model}
	if options.ContextSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(options.ContextSize))
	}
	if options.GPULayers != "" {
		args = append(args, "--n-gpu-layers", options.GPULayers)
	}
	args = appendRuntimeArgs(args, options, true, mode != ModeEmbedding)
	if options.Mmproj != "" {
		args = append(args, "--mmproj", options.Mmproj)
		if options.ImageMinTokens > 0 {
			args = append(args, "--image-min-tokens", strconv.Itoa(options.ImageMinTokens))
		}
		if options.ImageMaxTokens > 0 {
			args = append(args, "--image-max-tokens", strconv.Itoa(options.ImageMaxTokens))
		}
	}
	switch mode {
	case ModeEmbedding:
		args = append(args, "--embedding")
		if options.Pooling != "" {
			args = append(args, "--pooling", options.Pooling)
		}
		args = append(args, "--ubatch-size", strconv.Itoa(options.UBatchSize))
		if options.NormalizeSet {
			args = append(args, "--embd-normalize", strconv.Itoa(options.Normalize))
		}
	case ModeRerank:
		args = append(args, "--reranking")
	}
	args = append(args, "--host", options.Host, "--port", strconv.Itoa(options.Port))
	if options.UI {
		args = append(args, "--ui")
	} else {
		args = append(args, "--no-ui")
	}
	if options.APIKeyFile != "" {
		args = append(args, "--api-key-file", options.APIKeyFile)
	}
	args = append(args, options.Extra...)
	return Command{Path: executable, Args: args, Dir: root}, nil
}

func validateRuntimeOptions(options ServerOptions, includeParallel bool) error {
	if options.Threads != 0 {
		if err := ValidateThreads(options.Threads); err != nil {
			return err
		}
	}
	if options.BatchSize != 0 {
		if err := ValidatePositive("batch-size", options.BatchSize); err != nil {
			return err
		}
	}
	if options.UBatchSize != 0 {
		if err := ValidateUBatchSize(options.UBatchSize); err != nil {
			return err
		}
	}
	if options.FlashAttention != "" {
		if err := ValidateFlashAttention(options.FlashAttention); err != nil {
			return err
		}
	}
	if includeParallel && options.Parallel != 0 {
		if err := ValidateParallel(options.Parallel); err != nil {
			return err
		}
	}
	return nil
}

func appendRuntimeArgs(args []string, options ServerOptions, includeParallel, includeUBatch bool) []string {
	if options.Threads != 0 {
		args = append(args, "--threads", strconv.Itoa(options.Threads))
	}
	if options.BatchSize > 0 {
		args = append(args, "--batch-size", strconv.Itoa(options.BatchSize))
	}
	if includeUBatch && options.UBatchSize > 0 {
		args = append(args, "--ubatch-size", strconv.Itoa(options.UBatchSize))
	}
	if options.FlashAttention != "" {
		args = append(args, "--flash-attn", options.FlashAttention)
	}
	if includeParallel && options.Parallel != 0 {
		args = append(args, "--parallel", strconv.Itoa(options.Parallel))
	}
	return args
}

func BuildChatCommand(executable, root string, options ServerOptions) (Command, error) {
	if options.Model == "" {
		return Command{}, fmt.Errorf("模型路径不能为空")
	}
	if err := ValidateGPULayers(options.GPULayers); err != nil {
		return Command{}, err
	}
	if options.ContextSize < 0 {
		return Command{}, fmt.Errorf("ctx-size 不能小于 0")
	}
	if options.BatchSize > 0 && options.UBatchSize > 0 {
		if err := ValidateBatchPair(options.BatchSize, options.UBatchSize); err != nil {
			return Command{}, err
		}
	}
	if err := validateRuntimeOptions(options, false); err != nil {
		return Command{}, err
	}
	args := []string{"--model", options.Model}
	if options.ContextSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(options.ContextSize))
	}
	if options.GPULayers != "" {
		args = append(args, "--n-gpu-layers", options.GPULayers)
	}
	args = appendRuntimeArgs(args, options, false, true)
	args = append(args, "-cnv")
	args = append(args, options.Extra...)
	return Command{Path: executable, Args: args, Dir: root}, nil
}

func BuildRouterCommand(executable, root string, options RouterOptions) (Command, error) {
	if options.Preset == "" {
		return Command{}, fmt.Errorf("Router preset 路径不能为空")
	}
	if strings.TrimSpace(options.Host) == "" {
		return Command{}, fmt.Errorf("监听地址不能为空")
	}
	if err := ValidatePort(options.Port); err != nil {
		return Command{}, err
	}
	if err := ValidateGPULayers(options.GPULayers); err != nil {
		return Command{}, err
	}
	if options.ContextSize < 0 || options.ModelsMax < 0 {
		return Command{}, fmt.Errorf("ctx-size 和 models-max 不能小于 0")
	}
	args := []string{"--models-preset", options.Preset, "--models-max", strconv.Itoa(options.ModelsMax)}
	if options.Autoload {
		args = append(args, "--models-autoload")
	} else {
		args = append(args, "--no-models-autoload")
	}
	if options.ContextSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(options.ContextSize))
	}
	if options.GPULayers != "" {
		args = append(args, "--n-gpu-layers", options.GPULayers)
	}
	serverRuntime := ServerOptions{
		Threads: options.Threads, BatchSize: options.BatchSize, UBatchSize: options.UBatchSize,
		FlashAttention: options.FlashAttention, Parallel: options.Parallel,
	}
	if err := validateRuntimeOptions(serverRuntime, true); err != nil {
		return Command{}, err
	}
	if options.BatchSize > 0 && options.UBatchSize > 0 {
		if err := ValidateBatchPair(options.BatchSize, options.UBatchSize); err != nil {
			return Command{}, err
		}
	}
	args = appendRuntimeArgs(args, serverRuntime, true, true)
	args = append(args, "--host", options.Host, "--port", strconv.Itoa(options.Port))
	if options.UI {
		args = append(args, "--ui")
	} else {
		args = append(args, "--no-ui")
	}
	if options.APIKeyFile != "" {
		args = append(args, "--api-key-file", options.APIKeyFile)
	}
	args = append(args, options.Extra...)
	return Command{Path: executable, Args: args, Dir: root}, nil
}
