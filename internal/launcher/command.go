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
	Host           string
	Port           int
	GPULayers      string
	ContextSize    int
	UI             bool
	Pooling        string
	UBatchSize     int
	Extra          []string
}

type RouterOptions struct {
	Preset      string
	Host        string
	Port        int
	GPULayers   string
	ContextSize int
	UI          bool
	ModelsMax   int
	Autoload    bool
	Extra       []string
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
	if options.ContextSize < 0 || options.ImageMinTokens < 0 {
		return Command{}, fmt.Errorf("ctx-size 和 image-min-tokens 不能小于 0")
	}
	if err := ValidatePooling(options.Pooling); err != nil {
		return Command{}, err
	}
	if mode == ModeEmbedding {
		if err := ValidateUBatchSize(options.UBatchSize); err != nil {
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
	if options.Mmproj != "" {
		args = append(args, "--mmproj", options.Mmproj)
		if options.ImageMinTokens > 0 {
			args = append(args, "--image-min-tokens", strconv.Itoa(options.ImageMinTokens))
		}
	}
	switch mode {
	case ModeEmbedding:
		args = append(args, "--embedding")
		if options.Pooling != "" {
			args = append(args, "--pooling", options.Pooling)
		}
		args = append(args, "--ubatch-size", strconv.Itoa(options.UBatchSize))
	case ModeRerank:
		args = append(args, "--reranking")
	}
	args = append(args, "--host", options.Host, "--port", strconv.Itoa(options.Port))
	if options.UI {
		args = append(args, "--ui")
	} else {
		args = append(args, "--no-ui")
	}
	args = append(args, options.Extra...)
	return Command{Path: executable, Args: args, Dir: root}, nil
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
	args := []string{"--model", options.Model}
	if options.ContextSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(options.ContextSize))
	}
	if options.GPULayers != "" {
		args = append(args, "--n-gpu-layers", options.GPULayers)
	}
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
	args = append(args, "--host", options.Host, "--port", strconv.Itoa(options.Port))
	if options.UI {
		args = append(args, "--ui")
	} else {
		args = append(args, "--no-ui")
	}
	args = append(args, options.Extra...)
	return Command{Path: executable, Args: args, Dir: root}, nil
}
