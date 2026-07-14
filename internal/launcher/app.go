package launcher

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

type Application struct {
	Root     string
	Config   Config
	Paths    ResolvedPaths
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Executor Executor
}

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, executor Executor) int {
	if isVersionCommand(args) {
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}

	rootFlag, configFlag, remaining, err := parseGlobalFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		printUsage(stderr)
		return 2
	}
	// The executable location is mandatory even when --root is provided. This
	// prevents configuration and generated presets from being split across an
	// accidental launcher directory.
	root, err := ExecutableRoot()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if len(remaining) > 0 && (remaining[0] == "help" || remaining[0] == "--help" || remaining[0] == "-h") {
		printUsage(stdout)
		return 0
	}
	if strings.TrimSpace(rootFlag) != "" {
		root, err = determineRoot(rootFlag)
		if err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
	}
	config, configPath, created, err := LoadOrCreateConfig(root, configFlag)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if created {
		fmt.Fprintf(stdout, "已生成默认配置: %s\n", configPath)
	}
	app := &Application{
		Root: root, Config: config, Paths: config.ResolvePaths(root),
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Executor: executor,
	}
	if len(remaining) == 0 {
		return app.RunMenu()
	}
	code, err := app.RunCommand(remaining[0], remaining[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	return code
}

func isVersionCommand(args []string) bool {
	if len(args) != 1 {
		return false
	}
	return args[0] == "-v" || args[0] == "--version" || args[0] == "version"
}

func parseGlobalFlags(args []string) (root, config string, remaining []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--root" || arg == "--config":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("%s 缺少值", arg)
			}
			i++
			if arg == "--root" {
				root = args[i]
			} else {
				config = args[i]
			}
		case strings.HasPrefix(arg, "--root="):
			root = strings.TrimPrefix(arg, "--root=")
		case strings.HasPrefix(arg, "--config="):
			config = strings.TrimPrefix(arg, "--config=")
		default:
			return root, config, args[i:], nil
		}
	}
	return root, config, nil, nil
}

func determineRoot(root string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(strings.Trim(root, `"'`)))
	if err != nil {
		return "", fmt.Errorf("无法解析根目录 %q: %w", root, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("无法访问根目录 %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("根路径不是目录: %s", absolute)
	}
	return filepath.Clean(absolute), nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `llama.cpp Go 启动器

用法:
  llama-launcher -v | --version | version  打印版本信息并退出
  llama-launcher [--root DIR] [--config FILE] <子命令> [选项] [-- llama.cpp参数]
  llama-launcher                         进入中文交互菜单

子命令:
  serve          启动生成/聊天模型 API
  embedding      启动 Embedding API
  rerank         启动 Rerank API
  router-config  生成手动 Router preset
  router         生成自动 preset 并启动多模型 Router
  chat           使用 llama-cli 命令行聊天

运行 llama-launcher <子命令> --help 查看具体选项。`)
}

func (app *Application) RunCommand(name string, args []string) (int, error) {
	switch name {
	case "serve", "embedding", "rerank":
		return app.runServerSubcommand(Mode(name), args)
	case "chat":
		return app.runChatSubcommand(args)
	case "router":
		return app.runRouterSubcommand(args)
	case "router-config":
		return app.runRouterConfigSubcommand(args)
	default:
		return 1, fmt.Errorf("未知子命令 %q", name)
	}
}

func splitForwarded(args []string) (launcherArgs, forwarded []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	return set
}

func (app *Application) runServerSubcommand(mode Mode, args []string) (int, error) {
	launcherArgs, forwarded := splitForwarded(args)
	set := newFlagSet(string(mode), app.Stderr)
	model := set.String("model", "", "模型文件名或路径")
	host := set.String("host", app.Config.Server.Host, "监听地址")
	port := set.Int("port", app.Config.Server.Port, "监听端口")
	gpu := app.Config.Server.GPULayers
	set.StringVar(&gpu, "gpu-layers", gpu, "GPU 层数: auto/all/非负整数")
	set.StringVar(&gpu, "n-gpu-layers", gpu, "--gpu-layers 的别名")
	ctx := set.Int("ctx-size", app.Config.Server.ContextSize, "上下文长度，0 使用模型默认")
	ui := set.Bool("ui", app.Config.Server.UI, "启用 Web UI（可用 --ui=false）")
	mmproj := set.String("mmproj", "", "mmproj 文件路径（serve）")
	imageMinTokens := set.Int("image-min-tokens", 0, "最小图片 token 数")
	pooling := set.String("pooling", app.Config.Embedding.Pooling, "Embedding pooling: mean/cls/last/rank/none")
	ubatchSize := set.Int("ubatch-size", app.Config.Embedding.UBatchSize, "Embedding 物理批次大小")
	if err := set.Parse(launcherArgs); err != nil {
		return 2, err
	}
	if set.NArg() != 0 {
		return 2, fmt.Errorf("无法识别的参数 %q；传给 llama.cpp 的参数请放在 -- 之后", set.Args())
	}

	directory, kind, extensions := app.Paths.Models, GenerationModel, generationExtensions
	if mode == ModeEmbedding {
		directory, kind, extensions = app.Paths.Embeddings, EmbeddingModel, ggufExtension
	} else if mode == ModeRerank {
		directory, kind, extensions = app.Paths.Rerank, RerankModel, ggufExtension
	}
	selected, err := ResolveModelAt(directory, app.Root, *model, kind, extensions)
	if err != nil {
		return 1, err
	}
	mmprojPath := ""
	if *mmproj != "" {
		if mode != ModeServe {
			return 1, errors.New("--mmproj 仅适用于 serve 模式")
		}
		projector, err := ResolveModelAt(app.Paths.Mmproj, app.Root, *mmproj, GenerationModel, ggufExtension)
		if err != nil {
			return 1, fmt.Errorf("mmproj 无效: %w", err)
		}
		mmprojPath = projector.Path
	}
	if err := requireFile(app.Paths.Server, "llama-server.exe"); err != nil {
		return 1, err
	}
	command, err := BuildServerCommand(mode, app.Paths.Server, app.Root, ServerOptions{
		Model: selected.Path, Mmproj: mmprojPath, ImageMinTokens: *imageMinTokens,
		Host: *host, Port: *port, GPULayers: gpu, ContextSize: *ctx, UI: *ui,
		Pooling: strings.ToLower(strings.TrimSpace(*pooling)), UBatchSize: *ubatchSize, Extra: forwarded,
	})
	if err != nil {
		return 1, err
	}
	endpoint := ""
	switch mode {
	case ModeEmbedding:
		endpoint = "/v1/embeddings"
	case ModeRerank:
		endpoint = "/v1/rerank"
	}
	app.showLaunch(command, fmt.Sprintf("http://%s:%d%s", *host, *port, endpoint))
	return app.Executor.Execute(command, app.Stdin, app.Stdout, app.Stderr)
}

func (app *Application) runChatSubcommand(args []string) (int, error) {
	launcherArgs, forwarded := splitForwarded(args)
	set := newFlagSet("chat", app.Stderr)
	model := set.String("model", "", "模型文件名或路径")
	gpu := app.Config.Server.GPULayers
	set.StringVar(&gpu, "gpu-layers", gpu, "GPU 层数")
	set.StringVar(&gpu, "n-gpu-layers", gpu, "--gpu-layers 的别名")
	ctx := set.Int("ctx-size", app.Config.Server.ContextSize, "上下文长度")
	if err := set.Parse(launcherArgs); err != nil {
		return 2, err
	}
	if set.NArg() != 0 {
		return 2, fmt.Errorf("无法识别的参数 %q；额外参数请放在 -- 之后", set.Args())
	}
	selected, err := ResolveModelAt(app.Paths.Models, app.Root, *model, GenerationModel, generationExtensions)
	if err != nil {
		return 1, err
	}
	cli := app.Paths.CLI
	if _, err := os.Stat(cli); errors.Is(err, os.ErrNotExist) && filepath.Base(cli) == "llama-cli.exe" {
		fallback := filepath.Join(filepath.Dir(cli), "llama.exe")
		if _, fallbackErr := os.Stat(fallback); fallbackErr == nil {
			cli = fallback
		}
	}
	if err := requireFile(cli, "llama-cli.exe"); err != nil {
		return 1, err
	}
	command, err := BuildChatCommand(cli, app.Root, ServerOptions{Model: selected.Path, GPULayers: gpu, ContextSize: *ctx, Extra: forwarded})
	if err != nil {
		return 1, err
	}
	app.showLaunch(command, "")
	return app.Executor.Execute(command, app.Stdin, app.Stdout, app.Stderr)
}

func (app *Application) runRouterSubcommand(args []string) (int, error) {
	launcherArgs, forwarded := splitForwarded(args)
	set := newFlagSet("router", app.Stderr)
	host := set.String("host", app.Config.Server.Host, "监听地址")
	port := set.Int("port", app.Config.Server.Port, "监听端口")
	gpu := app.Config.Server.GPULayers
	set.StringVar(&gpu, "gpu-layers", gpu, "所有 Router 模型的 GPU 层数")
	set.StringVar(&gpu, "n-gpu-layers", gpu, "--gpu-layers 的别名")
	ctx := set.Int("ctx-size", app.Config.Server.ContextSize, "所有 Router 模型的上下文长度")
	ui := set.Bool("ui", app.Config.Server.UI, "启用 Web UI")
	modelsMax := set.Int("models-max", app.Config.Router.ModelsMax, "最多同时加载的模型数，0 不限制")
	autoload := set.Bool("autoload", app.Config.Router.Autoload, "按请求自动加载模型")
	pooling := set.String("pooling", app.Config.Embedding.Pooling, "自动 preset 中 Embedding pooling")
	ubatchSize := set.Int("ubatch-size", app.Config.Embedding.UBatchSize, "自动 preset 中 Embedding 物理批次大小")
	if err := set.Parse(launcherArgs); err != nil {
		return 2, err
	}
	if set.NArg() != 0 {
		return 2, fmt.Errorf("无法识别的参数 %q；额外参数请放在 -- 之后", set.Args())
	}
	if err := ValidatePooling(*pooling); err != nil {
		return 1, err
	}
	if err := ValidateUBatchSize(*ubatchSize); err != nil {
		return 1, err
	}
	if strings.TrimSpace(*host) == "" {
		return 1, errors.New("监听地址不能为空")
	}
	if err := ValidatePort(*port); err != nil {
		return 1, err
	}
	if err := ValidateGPULayers(gpu); err != nil {
		return 1, err
	}
	if *ctx < 0 || *modelsMax < 0 {
		return 1, errors.New("ctx-size 和 models-max 不能小于 0")
	}
	preset, manual, models, err := PrepareRouter(app.Paths, PresetOptions{GPULayers: gpu, ContextSize: *ctx, Pooling: strings.ToLower(strings.TrimSpace(*pooling)), UBatchSize: *ubatchSize})
	if err != nil {
		return 1, err
	}
	if err := requireFile(app.Paths.Server, "llama-server.exe"); err != nil {
		return 1, err
	}
	command, err := BuildRouterCommand(app.Paths.Server, app.Root, RouterOptions{
		Preset: preset, Host: *host, Port: *port, GPULayers: gpu, ContextSize: *ctx,
		UI: *ui, ModelsMax: *modelsMax, Autoload: *autoload, Extra: forwarded,
	})
	if err != nil {
		return 1, err
	}
	if manual {
		fmt.Fprintf(app.Stdout, "检测到手动配置，运行时优先使用且不会覆盖: %s\n", preset)
	} else {
		fmt.Fprintf(app.Stdout, "已生成自动 Router 配置: %s\n", preset)
	}
	fmt.Fprintf(app.Stdout, "Router 共发现 %d 个模型。\n", len(models))
	app.showLaunch(command, fmt.Sprintf("http://%s:%d/models", *host, *port))
	return app.Executor.Execute(command, app.Stdin, app.Stdout, app.Stderr)
}

func (app *Application) runRouterConfigSubcommand(args []string) (int, error) {
	set := newFlagSet("router-config", app.Stderr)
	force := set.Bool("force", false, "覆盖已有手动配置")
	gpu := app.Config.Server.GPULayers
	set.StringVar(&gpu, "gpu-layers", gpu, "每个 preset 的 GPU 层数")
	set.StringVar(&gpu, "n-gpu-layers", gpu, "--gpu-layers 的别名")
	ctx := set.Int("ctx-size", app.Config.Server.ContextSize, "每个 preset 的上下文长度")
	pooling := set.String("pooling", app.Config.Embedding.Pooling, "Embedding pooling")
	ubatchSize := set.Int("ubatch-size", app.Config.Embedding.UBatchSize, "Embedding 物理批次大小")
	launcherArgs, forwarded := splitForwarded(args)
	if len(forwarded) != 0 {
		return 2, errors.New("router-config 不接受 -- 后的转发参数")
	}
	if err := set.Parse(launcherArgs); err != nil {
		return 2, err
	}
	if set.NArg() != 0 {
		return 2, fmt.Errorf("无法识别的参数 %q", set.Args())
	}
	if err := ValidateGPULayers(gpu); err != nil {
		return 1, err
	}
	if *ctx < 0 {
		return 1, errors.New("ctx-size 不能小于 0")
	}
	if err := ValidatePooling(*pooling); err != nil {
		return 1, err
	}
	if err := ValidateUBatchSize(*ubatchSize); err != nil {
		return 1, err
	}
	models, projectors, err := CollectRouterModels(app.Paths)
	if err != nil {
		return 1, err
	}
	content := RenderRouterPreset(models, projectors, PresetOptions{
		GPULayers: gpu, ContextSize: *ctx, Pooling: strings.ToLower(strings.TrimSpace(*pooling)), UBatchSize: *ubatchSize, Manual: true,
	})
	if err := WriteRouterPreset(app.Paths.RouterManual, content, *force); err != nil {
		return 1, err
	}
	fmt.Fprintf(app.Stdout, "已生成手动 Router 配置（%d 个模型）: %s\n", len(models), app.Paths.RouterManual)
	return 0, nil
}

func requireFile(path, label string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("找不到 %s: %s", label, path)
	}
	if err != nil {
		return fmt.Errorf("无法访问 %s %s: %w", label, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s 路径是目录: %s", label, path)
	}
	return nil
}

func (app *Application) showLaunch(command Command, url string) {
	fmt.Fprintln(app.Stdout, "最终命令:")
	fmt.Fprintln(app.Stdout, " ", FormatCommand(command))
	if url != "" {
		fmt.Fprintln(app.Stdout, "访问地址:", url)
	}
}
