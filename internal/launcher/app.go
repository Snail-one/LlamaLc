package launcher

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

type Application struct {
	Root         string
	Config       Config
	Paths        ResolvedPaths
	LlamaVersion string
	LlamaTag     string
	LlamaBackend string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Executor     Executor
	Updater      *UpdateManager
}

var updateManagerFactory = NewUpdateManager

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, executor Executor) int {
	return mainWithProbe(args, stdin, stdout, stderr, executor, OSInstallationProbe{}, runtime.GOOS)
}

func mainWithProbe(args []string, stdin io.Reader, stdout, stderr io.Writer, executor Executor, probe InstallationProbe, goos string) int {
	if isVersionCommand(args) {
		if !runningGoTestBinary() {
			if _, err := ExecutableRoot(); err != nil {
				fmt.Fprintln(stderr, "错误:", err)
				return 1
			}
		}
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}

	if err := rejectRemovedPathFlags(args); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		printUsage(stderr)
		return 2
	}
	remaining := args
	root, err := ExecutableRoot()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if len(remaining) > 0 && (remaining[0] == "help" || remaining[0] == "--help" || remaining[0] == "-h") {
		printUsage(stdout)
		return 0
	}
	if len(remaining) > 0 && !isKnownCommand(remaining[0]) {
		fmt.Fprintf(stderr, "错误: 未知子命令 %q\n", remaining[0])
		return 1
	}
	manager := updateManagerFactory(root, probe, stdout, stderr)
	manager.GOOS = goos
	if len(remaining) > 0 && isManagementCommand(remaining[0]) {
		handoff := remaining[0] == "update"
		code, commandErr := delegateManagement(context.Background(), manager, remaining, stdin, false, handoff)
		if errors.Is(commandErr, errUpdaterHandoff) {
			return code
		}
		if errors.Is(commandErr, flag.ErrHelp) {
			return 0
		}
		if commandErr != nil {
			fmt.Fprintln(stderr, "错误:", commandErr)
			return code
		}
		return code
	}
	if len(remaining) > 0 && hasHelpFlag(remaining[1:]) {
		app := &Application{Root: root, Config: DefaultConfig(), Stdin: stdin, Stdout: stdout, Stderr: stderr, Executor: executor}
		_, err := app.RunCommand(remaining[0], remaining[1:])
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 2
		}
		return 0
	}
	paths, stateErr := resolveStartupPaths(root, goos)
	if stateErr != nil && filepath.Base(root) == "llama.cpp" {
		if len(remaining) > 0 {
			fmt.Fprintf(stderr, "错误: llama.cpp 运行时缺失或损坏（%v）；请运行 bin/llama-launcher install --backend <ID>\n", stateErr)
			return 1
		}
		maintenance := runMaintenanceMenu(manager, stdin)
		if !maintenance.installed {
			return maintenance.code
		}
		stdin = maintenance.input
		paths, stateErr = resolveStartupPaths(root, goos)
		if stateErr != nil {
			fmt.Fprintln(stderr, "错误: 安装完成后无法加载 llama.cpp 运行时:", stateErr)
			return 1
		}
	}
	if stateErr != nil {
		// Compatibility for historical unit fixtures whose temporary root is not
		// a real deployable llama.cpp directory. Real deployments never inspect
		// flat root-level server/cli files.
		paths, err = ResolveFixedPaths(root, goos)
		if err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
	}
	detectedVersion, err := VerifyInstallation(root, paths, probe)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	fmt.Fprintln(stdout, "实际探测文件:", paths.Server)
	fmt.Fprintln(stdout, "已识别 llama.cpp:", detectedVersion)
	llamaTag, llamaBackend := "", ""
	if state, exists, stateLoadErr := LoadUpdateState(root); stateLoadErr == nil && exists {
		llamaTag, llamaBackend = state.LlamaTag, state.Backend
	}

	config, configPath, needsCreate, err := LoadConfig(root)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	createdDirectories, err := EnsureRuntimeDirectories(root)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	for _, directory := range createdDirectories {
		fmt.Fprintf(stdout, "已创建目录: %s\n", directory)
	}
	startupInput, err := prepareAPIKey(&config, configPath, needsCreate, stdin, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if needsCreate {
		if err := WriteDefaultConfig(configPath, config); err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		fmt.Fprintf(stdout, "已生成配置: %s\n", configPath)
	}
	if err := WriteAPIKeyFile(root, paths.APIKeyFile, config.Server.APIKey); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	app := &Application{
		Root: root, Config: config, Paths: paths,
		LlamaVersion: detectedVersion, LlamaTag: llamaTag, LlamaBackend: llamaBackend,
		Stdin: startupInput, Stdout: stdout, Stderr: stderr, Executor: executor, Updater: manager,
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

func runningGoTestBinary() bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".test")
}

func isVersionCommand(args []string) bool {
	if len(args) != 1 {
		return false
	}
	return args[0] == "-v" || args[0] == "--version" || args[0] == "version"
}

func rejectRemovedPathFlags(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--root" || strings.HasPrefix(arg, "--root=") {
			return errors.New("--root 已移除；启动器根目录固定为 bin 的上一级目录")
		}
		if arg == "--config" || strings.HasPrefix(arg, "--config=") {
			return errors.New("--config 已移除；配置文件固定为 config/launcher.json")
		}
	}
	return nil
}

func isKnownCommand(name string) bool {
	switch name {
	case "serve", "embedding", "rerank", "router-config", "router", "chat", "install", "check-update", "update":
		return true
	default:
		return false
	}
}

func isManagementCommand(name string) bool {
	return name == "install" || name == "check-update" || name == "update"
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `llama.cpp Go 启动器

用法:
  llama-launcher -v | --version | version  打印版本信息并退出
  llama-launcher <子命令> [选项] [-- llama.cpp参数]
  llama-launcher                         进入中文交互菜单

子命令:
  install        安装缺失的 llama.cpp 运行时
  check-update   手动检查启动器与 llama.cpp 更新
  update         手动更新一个或两个组件
  serve          启动生成/聊天模型 API
  embedding      启动 Embedding API
  rerank         启动 Rerank API
  router-config  生成手动 Router preset
  router         生成自动 preset 并启动多模型 Router
  chat           使用 llama-cli 命令行聊天

运行 llama-launcher <子命令> --help 查看具体选项。`)
}

func resolveStartupPaths(root, goos string) (ResolvedPaths, error) {
	state, exists, err := LoadUpdateState(root)
	if err != nil {
		return ResolvedPaths{}, err
	}
	if !exists {
		return ResolvedPaths{}, errors.New("未找到 config/update-state.json")
	}
	return ResolveManagedPaths(root, goos, state)
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
	threads := set.Int("threads", app.Config.Server.Threads, "生成线程数，-1 自动")
	batchDefault := app.Config.Server.BatchSize
	ubatchDefault := app.Config.Server.UBatchSize
	if mode == ModeEmbedding {
		batchDefault = app.Config.Embedding.BatchSize
		ubatchDefault = app.Config.Embedding.UBatchSize
	}
	batchSize := set.Int("batch-size", batchDefault, "逻辑批次大小")
	ubatchSize := set.Int("ubatch-size", ubatchDefault, "物理批次大小")
	flashAttention := set.String("flash-attn", app.Config.Server.FlashAttention, "Flash Attention: auto/on/off")
	parallel := set.Int("parallel", app.Config.Server.Parallel, "服务并发槽位数，-1 自动")
	ui := set.Bool("ui", app.Config.Server.UI, "启用 Web UI（可用 --ui=false）")
	mmproj := set.String("mmproj", "", "mmproj 文件路径（serve）")
	imageMinTokens := set.Int("image-min-tokens", 0, "最小图片 token 数")
	imageMaxTokens := set.Int("image-max-tokens", 0, "最大图片 token 数")
	pooling := set.String("pooling", app.Config.Embedding.Pooling, "Embedding pooling: mean/cls/last/rank/none")
	normalize := set.Int("embd-normalize", app.Config.Embedding.Normalize, "Embedding 归一化方式，官方默认 2")
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
	if err := requireFile(app.Paths.Server, "llama-server"); err != nil {
		return 1, err
	}
	command, err := BuildServerCommand(mode, app.Paths.Server, app.Root, ServerOptions{
		Model: selected.Path, Mmproj: mmprojPath, ImageMinTokens: *imageMinTokens, ImageMaxTokens: *imageMaxTokens,
		Host: *host, Port: *port, GPULayers: gpu, ContextSize: *ctx, UI: *ui,
		Threads: *threads, BatchSize: *batchSize, UBatchSize: *ubatchSize,
		FlashAttention: strings.ToLower(strings.TrimSpace(*flashAttention)), Parallel: *parallel,
		Pooling: strings.ToLower(strings.TrimSpace(*pooling)), Normalize: *normalize, NormalizeSet: mode == ModeEmbedding,
		APIKeyFile: app.Paths.APIKeyFile,
		Extra:      forwarded,
	})
	if err != nil {
		return 1, err
	}
	effectiveHost, remote, err := validateNetworkExposure(*host, command.Args, app.Paths.APIKeyFile)
	if err != nil {
		return 1, err
	}
	if remote {
		fmt.Fprintf(app.Stderr, "安全警告: 服务将监听非本机地址 %s，请确认防火墙和 API key 配置。\n", effectiveHost)
		fmt.Fprintln(app.Stderr, "安全提示: llama-server 的 /models、/v1/models、健康检查和 UI 静态资源不受 API key 保护。")
	}
	endpoint := ""
	switch mode {
	case ModeEmbedding:
		endpoint = "/v1/embeddings"
	case ModeRerank:
		endpoint = "/v1/rerank"
	}
	app.showLaunch(command, serviceURL(effectiveHost, *port, endpoint))
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
	threads := set.Int("threads", app.Config.Server.Threads, "生成线程数，-1 自动")
	batchSize := set.Int("batch-size", app.Config.Server.BatchSize, "逻辑批次大小")
	ubatchSize := set.Int("ubatch-size", app.Config.Server.UBatchSize, "物理批次大小")
	flashAttention := set.String("flash-attn", app.Config.Server.FlashAttention, "Flash Attention: auto/on/off")
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
	if _, err := os.Stat(cli); errors.Is(err, os.ErrNotExist) {
		if _, fallbackErr := os.Stat(app.Paths.CLIFallback); fallbackErr == nil {
			cli = app.Paths.CLIFallback
		}
	}
	if err := requireFile(cli, "llama-cli"); err != nil {
		return 1, err
	}
	command, err := BuildChatCommand(cli, app.Root, ServerOptions{
		Model: selected.Path, GPULayers: gpu, ContextSize: *ctx,
		Threads: *threads, BatchSize: *batchSize, UBatchSize: *ubatchSize,
		FlashAttention: strings.ToLower(strings.TrimSpace(*flashAttention)), Extra: forwarded,
	})
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
	threads := set.Int("threads", app.Config.Server.Threads, "生成线程数，-1 自动")
	batchSize := set.Int("batch-size", app.Config.Server.BatchSize, "逻辑批次大小")
	ubatchSize := set.Int("ubatch-size", app.Config.Server.UBatchSize, "通用物理批次大小")
	flashAttention := set.String("flash-attn", app.Config.Server.FlashAttention, "Flash Attention: auto/on/off")
	parallel := set.Int("parallel", app.Config.Server.Parallel, "服务并发槽位数，-1 自动")
	ui := set.Bool("ui", app.Config.Server.UI, "启用 Web UI")
	modelsMax := set.Int("models-max", app.Config.Router.ModelsMax, "最多同时加载的模型数，0 不限制")
	autoload := set.Bool("autoload", app.Config.Router.Autoload, "按请求自动加载模型")
	pooling := set.String("pooling", app.Config.Embedding.Pooling, "自动 preset 中 Embedding pooling")
	embeddingBatchSize := set.Int("embedding-batch-size", app.Config.Embedding.BatchSize, "自动 preset 中 Embedding 逻辑批次大小")
	embeddingUBatchSize := set.Int("embedding-ubatch-size", app.Config.Embedding.UBatchSize, "自动 preset 中 Embedding 物理批次大小")
	if err := set.Parse(launcherArgs); err != nil {
		return 2, err
	}
	if set.NArg() != 0 {
		return 2, fmt.Errorf("无法识别的参数 %q；额外参数请放在 -- 之后", set.Args())
	}
	if err := ValidatePooling(*pooling); err != nil {
		return 1, err
	}
	if err := ValidateUBatchSize(*embeddingUBatchSize); err != nil {
		return 1, err
	}
	if err := ValidatePositive("embedding-batch-size", *embeddingBatchSize); err != nil {
		return 1, err
	}
	if err := ValidateBatchPair(*embeddingBatchSize, *embeddingUBatchSize); err != nil {
		return 1, fmt.Errorf("Embedding %w", err)
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
	if err := ValidateThreads(*threads); err != nil {
		return 1, err
	}
	if err := ValidatePositive("batch-size", *batchSize); err != nil {
		return 1, err
	}
	if err := ValidateUBatchSize(*ubatchSize); err != nil {
		return 1, err
	}
	if err := ValidateBatchPair(*batchSize, *ubatchSize); err != nil {
		return 1, err
	}
	if err := ValidateFlashAttention(*flashAttention); err != nil {
		return 1, err
	}
	if err := ValidateParallel(*parallel); err != nil {
		return 1, err
	}
	if *ctx < 0 || *modelsMax < 0 {
		return 1, errors.New("ctx-size 和 models-max 不能小于 0")
	}
	preset, manual, models, err := PrepareRouter(app.Paths, PresetOptions{
		GPULayers: gpu, ContextSize: *ctx, Pooling: strings.ToLower(strings.TrimSpace(*pooling)),
		BatchSize: *embeddingBatchSize, UBatchSize: *embeddingUBatchSize,
	})
	if err != nil {
		return 1, err
	}
	if err := requireFile(app.Paths.Server, "llama-server"); err != nil {
		return 1, err
	}
	command, err := BuildRouterCommand(app.Paths.Server, app.Root, RouterOptions{
		Preset: preset, Host: *host, Port: *port, GPULayers: gpu, ContextSize: *ctx,
		Threads: *threads, BatchSize: *batchSize, UBatchSize: *ubatchSize,
		FlashAttention: strings.ToLower(strings.TrimSpace(*flashAttention)), Parallel: *parallel,
		UI: *ui, ModelsMax: *modelsMax, Autoload: *autoload, Extra: forwarded,
		APIKeyFile: app.Paths.APIKeyFile,
	})
	if err != nil {
		return 1, err
	}
	effectiveHost, remote, err := validateNetworkExposure(*host, command.Args, app.Paths.APIKeyFile)
	if err != nil {
		return 1, err
	}
	if remote {
		fmt.Fprintf(app.Stderr, "安全警告: Router 将监听非本机地址 %s，请确认防火墙和 API key 配置。\n", effectiveHost)
		fmt.Fprintln(app.Stderr, "安全提示: llama-server 的 /models、/v1/models、健康检查和 UI 静态资源不受 API key 保护。")
	}
	if manual {
		fmt.Fprintf(app.Stdout, "检测到手动配置，运行时优先使用且不会覆盖: %s\n", preset)
	} else {
		fmt.Fprintf(app.Stdout, "已生成自动 Router 配置: %s\n", preset)
	}
	fmt.Fprintf(app.Stdout, "Router 共发现 %d 个模型。\n", len(models))
	app.showLaunch(command, serviceURL(effectiveHost, *port, "/models"))
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
	batchSize := set.Int("batch-size", app.Config.Embedding.BatchSize, "Embedding 逻辑批次大小")
	ubatchSize := set.Int("ubatch-size", app.Config.Embedding.UBatchSize, "Embedding 物理批次大小")
	mmprojAuto := set.Bool("mmproj-auto", true, "按文件名前缀自动匹配 mmproj")
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
	if err := ValidatePositive("batch-size", *batchSize); err != nil {
		return 1, err
	}
	if err := ValidateBatchPair(*batchSize, *ubatchSize); err != nil {
		return 1, fmt.Errorf("Embedding %w", err)
	}
	models, projectors, err := CollectRouterModels(app.Paths)
	if err != nil {
		return 1, err
	}
	content, err := RenderRouterPreset(models, projectors, PresetOptions{
		GPULayers: gpu, ContextSize: *ctx, Pooling: strings.ToLower(strings.TrimSpace(*pooling)),
		BatchSize: *batchSize, UBatchSize: *ubatchSize, DisableMmprojAuto: !*mmprojAuto, Manual: true,
	})
	if err != nil {
		return 1, err
	}
	if err := WriteRouterPreset(app.Paths.RouterManual, content, *force); err != nil {
		return 1, err
	}
	fmt.Fprintf(app.Stdout, "已生成手动 Router 配置（%d 个模型）: %s\n", len(models), app.Paths.RouterManual)
	return 0, nil
}

func requireFile(path, label string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("找不到 %s: %s", label, path)
	}
	if err != nil {
		return fmt.Errorf("无法访问 %s %s: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s 不允许使用符号链接或重解析点: %s", label, path)
	}
	if info.IsDir() {
		return fmt.Errorf("%s 路径是目录: %s", label, path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件: %s", label, path)
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
