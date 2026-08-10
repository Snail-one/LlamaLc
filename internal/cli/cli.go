// Package cli implements the public LlamaLc command-line interface.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/config"
	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/models"
	"github.com/Snail-one/LlamaLc/internal/secrets"
	"github.com/Snail-one/LlamaLc/internal/update"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

type App struct {
	Layout      layout.Layout
	Config      config.Config
	In          io.Reader
	Out, Err    io.Writer
	Executor    llama.Executor
	Updates     *update.Manager
	GOOS        string
	Interactive bool
	// PreparedRuntime is populated by the launcher after a read-only preflight
	// so direct CLI runs cannot create configuration before runtime validation.
	PreparedRuntime *update.ActiveRuntime
	RuntimeReporter func(update.ActiveRuntime)
	commandOutcome  CommandOutcome
}

type CommandOutcome struct {
	Code      int
	Success   bool
	Cancelled bool
	Back      bool
	Handoff   bool
}

var errSyntax = errors.New("命令语法错误")
var errCancelled = errors.New("操作已取消")
var ErrHandoff = errors.New("更新器交接完成")
var errLaunchCancelledCLI = errors.New("启动已取消")
var errBack = errors.New("返回上级菜单")

func (a *App) Run(args []string) int {
	return a.RunWithResult(args).Code
}

func (a *App) RunWithResult(args []string) CommandOutcome {
	a.commandOutcome = CommandOutcome{}
	a.commandOutcome.Code = a.runCode(args)
	a.commandOutcome.Success = a.commandOutcome.Code == 0 && !a.commandOutcome.Cancelled && !a.commandOutcome.Back && !a.commandOutcome.Handoff
	return a.commandOutcome
}

func (a *App) runCode(args []string) int {
	if len(args) == 0 {
		Usage(a.Out)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		Usage(a.Out)
		return 0
	}
	if args[0] == "-v" || args[0] == "--version" {
		if len(args) != 1 {
			return a.syntax("--version 不接受参数")
		}
		fmt.Fprintln(a.Out, buildversion.String())
		return 0
	}
	if !publicTopLevel(args[0]) {
		return a.syntax(fmt.Sprintf("未知命令 %q", args[0]))
	}
	if hasHelp(args[1:]) {
		commandUsage(a.Out, args)
		return 0
	}
	var err error
	switch args[0] {
	case "version":
		if len(args) != 1 {
			return a.syntax("version 不接受参数")
		}
		fmt.Fprintln(a.Out, buildversion.String())
		return 0
	case "run":
		err = a.run(args[1:])
	case "router":
		err = a.router(args[1:])
	case "key":
		err = a.key(args[1:])
	case "update":
		if len(args) == 1 {
			commandUsage(a.Out, args)
			return 0
		}
		err = a.update(args[1:])
	case "cleanup":
		if len(args) != 1 {
			err = fmt.Errorf("%w: cleanup 不接受参数", errSyntax)
		} else {
			err = a.runCleanupMenu()
		}
	default:
		return a.syntax(fmt.Sprintf("未知命令 %q", args[0]))
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, ErrHandoff) {
		a.commandOutcome.Handoff = true
	}
	if errors.Is(err, errCancelled) || errors.Is(err, errLaunchCancelledCLI) {
		a.commandOutcome.Cancelled = true
	}
	if errors.Is(err, errBack) {
		a.commandOutcome.Back = true
	}
	if errors.Is(err, flag.ErrHelp) || errors.Is(err, ErrHandoff) || errors.Is(err, errCancelled) || errors.Is(err, errLaunchCancelledCLI) || errors.Is(err, errBack) {
		return 0
	}
	var processExit *processExitError
	if errors.As(err, &processExit) {
		fmt.Fprintln(a.Err, "错误:", err)
		return processExit.code
	}
	code := 1
	if errors.Is(err, errSyntax) {
		code = 2
	}
	fmt.Fprintln(a.Err, "错误:", err)
	return code
}

func publicTopLevel(name string) bool {
	switch name {
	case "run", "router", "key", "update", "cleanup", "version":
		return true
	}
	return false
}

func hasHelp(args []string) bool {
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

func Usage(w io.Writer) {
	fmt.Fprint(w, `LlamaLc 命令行

用法:
  llamalc run api [选项] [-- llama.cpp 参数]
  llamalc run embedding [选项] [-- llama.cpp 参数]
  llamalc run rerank [选项] [-- llama.cpp 参数]
  llamalc run router [选项] [-- llama.cpp 参数]
  llamalc run chat [选项] [-- llama.cpp 参数]
  llamalc router generate [选项]
  llamalc key show
  llamalc key reset [--yes]
  llamalc update check [all|llama|launcher] [--json]
  llamalc update llama [--version TAG] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]
  llamalc update launcher [--version SEMVER] [--reinstall] [--allow-downgrade] [--yes]
  llamalc update all [--llama-version TAG] [--launcher-version SEMVER] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]
  llamalc cleanup
  llamalc version
  llamalc help
`)
}

func commandUsage(w io.Writer, args []string) {
	name := strings.Join(args, " ")
	if i := strings.Index(name, " --help"); i >= 0 {
		name = name[:i]
	}
	if i := strings.Index(name, " -h"); i >= 0 {
		name = name[:i]
	}
	switch {
	case strings.HasPrefix(name, "run "):
		fields := strings.Fields(name)
		mode := "api|embedding|rerank|router|chat"
		if len(fields) > 1 {
			mode = fields[1]
		}
		fmt.Fprintf(w, "用法: llamalc run %s [该模式选项] [-- llama.cpp 参数]\n", mode)
		fmt.Fprintln(w, "推荐参数名: --gpu-layers、--context-size、--flash-attention、--normalize")
		common := "--model --gpu-layers --context-size --threads --batch-size --ubatch-size --flash-attention"
		switch mode {
		case "api":
			fmt.Fprintln(w, "有效选项:", common, "--mmproj --image-min-tokens --image-max-tokens --host --port --parallel --ui")
		case "embedding":
			fmt.Fprintln(w, "有效选项:", common, "--pooling --normalize --host --port --parallel --ui")
		case "rerank":
			fmt.Fprintln(w, "有效选项:", common, "--host --port --parallel --ui")
		case "router":
			fmt.Fprintln(w, "有效选项: --preset --gpu-layers --context-size --threads --batch-size --ubatch-size --flash-attention --pooling --embedding-batch-size --embedding-ubatch-size --models-max --autoload --host --port --parallel --ui")
		case "chat":
			fmt.Fprintln(w, "有效选项:", common)
		}
	case name == "router" || name == "router generate":
		fmt.Fprintln(w, "用法: llamalc router generate [--force] [Router 模型默认参数]")
	case name == "key" || strings.HasPrefix(name, "key "):
		fmt.Fprintln(w, "用法: llamalc key show | llamalc key reset [--yes]")
	case name == "update":
		fmt.Fprint(w, `用法:
  llamalc update check [all|llama|launcher] [--json]
  llamalc update llama [--version TAG] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]
  llamalc update launcher [--version SEMVER] [--reinstall] [--allow-downgrade] [--yes]
  llamalc update all [--llama-version TAG] [--launcher-version SEMVER] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]
`)
	case strings.HasPrefix(name, "update check"):
		fmt.Fprintln(w, "用法: llamalc update check [all|llama|launcher] [--json]")
	case strings.HasPrefix(name, "update llama"):
		fmt.Fprintln(w, "用法: llamalc update llama [--version TAG] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]")
	case strings.HasPrefix(name, "update launcher"):
		fmt.Fprintln(w, "用法: llamalc update launcher [--version SEMVER] [--reinstall] [--allow-downgrade] [--yes]")
	case strings.HasPrefix(name, "update all"):
		fmt.Fprintln(w, "用法: llamalc update all [--llama-version TAG] [--launcher-version SEMVER] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]")
	case name == "cleanup":
		fmt.Fprintln(w, "用法: llamalc cleanup")
	default:
		Usage(w)
	}
}

func (a *App) syntax(message string) int {
	fmt.Fprintln(a.Err, "错误:", message)
	Usage(a.Err)
	return 2
}

type runFlags struct {
	model, mmproj, preset, host, gpu, flash, pooling                           string
	port, contextSize, threads, batch, ubatch, parallel, normalize             int
	embeddingBatch, embeddingUBatch, modelsMax, imageMinTokens, imageMaxTokens int
	ui, autoload                                                               bool
}

func splitExtra(args []string) (flags, extra []string, explicit bool) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

func (a *App) run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: run 需要 api、embedding、rerank、router 或 chat", errSyntax)
	}
	mode := llama.Mode(args[0])
	switch mode {
	case llama.API, llama.Embedding, llama.Rerank, llama.Router, llama.Chat:
	default:
		return fmt.Errorf("%w: 未知 run 模式 %q", errSyntax, args[0])
	}
	flagArgs, extra, _ := splitExtra(args[1:])
	set := flag.NewFlagSet("run "+args[0], flag.ContinueOnError)
	set.SetOutput(a.Err)
	f := a.addRunFlags(set, mode)
	if err := set.Parse(flagArgs); err != nil {
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	if set.NArg() != 0 {
		return fmt.Errorf("%w: 额外 llama.cpp 参数必须放在 -- 后", errSyntax)
	}

	var active update.ActiveRuntime
	var err error
	if a.PreparedRuntime != nil {
		a.PreparedRuntime = nil
		active, err = update.ValidateActiveRuntime(context.Background(), a.Layout, a.goos())
		if err != nil {
			return err
		}
	} else {
		active, err = update.ValidateActiveRuntime(context.Background(), a.Layout, a.goos())
		if err != nil {
			if !runtimeFixtureFallback(err, a.Executor) {
				if errors.Is(err, update.ErrRuntimeNotInstalled) {
					return errors.New("llama.cpp 尚未安装；请运行 llamalc update llama --backend <ID>")
				}
				return err
			}
			// Older package embedders used a non-native placeholder executable
			// together with an injected process executor. Keep that narrow test
			// seam while all real launcher paths require a successful probe.
			state, exists, loadErr := update.LoadState(a.Layout)
			if loadErr != nil {
				return loadErr
			}
			if !exists || state.ActiveRuntime == "" {
				return errors.New("llama.cpp 尚未安装；请运行 llamalc update llama --backend <ID>")
			}
			runtime, locateErr := llama.Locate(update.RuntimePath(a.Layout, state), a.goos())
			if locateErr != nil {
				return locateErr
			}
			active = update.ActiveRuntime{State: state, Runtime: runtime}
		}
	}
	if a.RuntimeReporter != nil {
		a.RuntimeReporter(active)
	}
	rt := active.Runtime
	o := llama.Options{
		Preset: f.preset, Host: f.host, Port: f.port, GPULayers: strings.ToLower(strings.TrimSpace(f.gpu)),
		ContextSize: f.contextSize, Threads: f.threads, BatchSize: f.batch, UBatchSize: f.ubatch,
		FlashAttention: strings.ToLower(strings.TrimSpace(f.flash)), Parallel: f.parallel, UI: f.ui,
		Pooling: strings.ToLower(strings.TrimSpace(f.pooling)), Normalize: f.normalize, NormalizeSet: mode == llama.Embedding,
		ModelsMax: f.modelsMax, Autoload: f.autoload, Extra: extra,
		ImageMinTokens: f.imageMinTokens, ImageMaxTokens: f.imageMaxTokens,
	}
	if mode != llama.Router {
		kind := models.Generation
		if mode == llama.Embedding {
			kind = models.Embedding
		} else if mode == llama.Rerank {
			kind = models.Rerank
		}
		model, resolveErr := models.Resolve(a.Layout, kind, f.model)
		if resolveErr != nil {
			return resolveErr
		}
		o.Model = model.Path
	} else {
		files, projectors, scanErr := models.CollectRouterModels(a.Layout)
		if scanErr != nil {
			return scanErr
		}
		if err := validateRouterPresetOptions(f.gpu, f.contextSize, f.pooling, f.embeddingBatch, f.embeddingUBatch); err != nil {
			return err
		}
		options := models.PresetOptions{GPULayers: o.GPULayers, ContextSize: f.contextSize, Pooling: o.Pooling, BatchSize: f.embeddingBatch, UBatchSize: f.embeddingUBatch}
		if err := models.WriteRouterPresetWithOptions(a.Layout, a.Layout.AutoRouterPreset, files, projectors, options); err != nil {
			return err
		}
		if f.preset != a.Layout.AutoRouterPreset {
			info, statErr := os.Lstat(f.preset)
			if statErr != nil {
				return statErr
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("Router preset 必须是普通文件且不能是符号链接")
			}
		}
	}
	if f.mmproj != "" {
		projector, resolveErr := models.Resolve(a.Layout, models.MMProj, f.mmproj)
		if resolveErr != nil {
			return resolveErr
		}
		o.MMProj = projector.Path
	}
	if mode != llama.Chat {
		o.APIKeyFile = a.Layout.APIKeyFile
		if err := llama.ValidateExposure(f.host, a.Layout.APIKeyFile, append([]string{"--api-key-file", a.Layout.APIKeyFile}, extra...)); err != nil {
			return err
		}
	}
	command, err := llama.Build(mode, rt, a.Layout.Root, o)
	if err != nil {
		return err
	}
	if mode == llama.Router {
		if f.preset == a.Layout.RouterPreset {
			fmt.Fprintln(a.Out, "检测到手动配置，运行时优先使用且不会覆盖:", f.preset)
		} else {
			fmt.Fprintln(a.Out, "已生成自动 Router 配置:", a.Layout.AutoRouterPreset)
		}
	}
	fmt.Fprintln(a.Out, "最终命令:")
	fmt.Fprintln(a.Out, " ", llama.Format(command))
	if mode != llama.Chat {
		path := map[llama.Mode]string{llama.Embedding: "/v1/embeddings", llama.Rerank: "/v1/rerank", llama.Router: "/models"}[mode]
		fmt.Fprintln(a.Out, "访问地址:", serviceURL(llama.EffectiveHost(f.host, extra), llama.EffectivePort(f.port, extra), path))
		if effective := llama.EffectiveHost(f.host, extra); !llama.IsLocalHost(effective) {
			fmt.Fprintf(a.Err, "安全警告: 服务将监听非本机地址 %s，请确认防火墙和 API key 配置。\n", effective)
			fmt.Fprintln(a.Err, "安全提示: 模型列表、健康检查和 UI 静态资源可能不受 API key 保护。")
		}
	}
	if a.Interactive {
		confirmed, err := a.askYesNo("确认启动以上命令", true)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(a.Out, "已取消启动。")
			return errLaunchCancelledCLI
		}
	}
	code, err := a.Executor.Execute(command, a.In, a.Out, a.Err)
	if err != nil {
		return err
	}
	if code != 0 {
		return &processExitError{code: code}
	}
	return nil
}

func runtimeFixtureFallback(err error, executor llama.Executor) bool {
	switch executor.(type) {
	case llama.OSExecutor, *llama.OSExecutor:
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "exec format error") || strings.Contains(message, "not a valid win32 application") || strings.Contains(message, "executable file format")
}

func (a *App) addRunFlags(set *flag.FlagSet, mode llama.Mode) *runFlags {
	cfg := a.Config
	if cfg.Schema == 0 {
		cfg = config.Default()
	}
	f := &runFlags{gpu: cfg.Runtime.GPULayers, contextSize: cfg.Runtime.ContextSize, threads: cfg.Runtime.Threads, batch: cfg.Runtime.BatchSize, ubatch: cfg.Runtime.UBatchSize, flash: cfg.Runtime.FlashAttention}
	if mode == llama.Embedding {
		f.batch, f.ubatch = cfg.Embedding.BatchSize, cfg.Embedding.UBatchSize
	}
	if mode != llama.Router {
		set.StringVar(&f.model, "model", "", "模型文件名或路径")
	}
	set.StringVar(&f.gpu, "gpu-layers", f.gpu, "GPU layers（推荐）")
	set.StringVar(&f.gpu, "n-gpu-layers", f.gpu, "--gpu-layers 的别名")
	set.IntVar(&f.contextSize, "context-size", f.contextSize, "上下文大小（推荐）")
	set.IntVar(&f.contextSize, "ctx-size", f.contextSize, "--context-size 的别名")
	set.IntVar(&f.threads, "threads", f.threads, "线程数")
	set.IntVar(&f.batch, "batch-size", f.batch, "batch size")
	set.IntVar(&f.ubatch, "ubatch-size", f.ubatch, "ubatch size")
	set.StringVar(&f.flash, "flash-attention", f.flash, "auto|on|off（推荐）")
	set.StringVar(&f.flash, "flash-attn", f.flash, "--flash-attention 的别名")
	if mode == llama.Chat {
		return f
	}
	f.host, f.port, f.parallel, f.ui = cfg.API.Host, cfg.API.Port, cfg.API.Parallel, cfg.API.UI
	set.StringVar(&f.host, "host", f.host, "监听地址")
	set.IntVar(&f.port, "port", f.port, "监听端口")
	set.IntVar(&f.parallel, "parallel", f.parallel, "并发数")
	set.BoolVar(&f.ui, "ui", f.ui, "启用 Web UI")
	if mode == llama.API {
		set.StringVar(&f.mmproj, "mmproj", "", "多模态 projector")
		set.IntVar(&f.imageMinTokens, "image-min-tokens", 0, "最小图片 token 数")
		set.IntVar(&f.imageMaxTokens, "image-max-tokens", 0, "最大图片 token 数")
	}
	if mode == llama.Embedding {
		f.pooling, f.normalize = cfg.Embedding.Pooling, cfg.Embedding.Normalize
		set.StringVar(&f.pooling, "pooling", f.pooling, "Embedding pooling")
		set.IntVar(&f.normalize, "normalize", f.normalize, "Embedding normalize（推荐）")
		set.IntVar(&f.normalize, "embd-normalize", f.normalize, "--normalize 的别名")
	}
	if mode == llama.Router {
		f.preset = a.Layout.AutoRouterPreset
		if info, err := os.Lstat(a.Layout.RouterPreset); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			f.preset = a.Layout.RouterPreset
		}
		f.pooling, f.embeddingBatch, f.embeddingUBatch = cfg.Embedding.Pooling, cfg.Embedding.BatchSize, cfg.Embedding.UBatchSize
		f.modelsMax, f.autoload = cfg.Router.ModelsMax, cfg.Router.Autoload
		set.StringVar(&f.preset, "preset", f.preset, "Router preset")
		set.StringVar(&f.pooling, "pooling", f.pooling, "Embedding pooling")
		set.IntVar(&f.embeddingBatch, "embedding-batch-size", f.embeddingBatch, "Router Embedding batch size")
		set.IntVar(&f.embeddingUBatch, "embedding-ubatch-size", f.embeddingUBatch, "Router Embedding ubatch size")
		set.IntVar(&f.modelsMax, "models-max", f.modelsMax, "Router 最大已加载模型数")
		set.BoolVar(&f.autoload, "autoload", f.autoload, "Router 自动加载")
	}
	return f
}

func (a *App) router(args []string) error {
	if len(args) == 0 || args[0] != "generate" {
		return fmt.Errorf("%w: router 仅支持 generate", errSyntax)
	}
	set := flag.NewFlagSet("router generate", flag.ContinueOnError)
	set.SetOutput(a.Err)
	cfg := a.Config
	if cfg.Schema == 0 {
		cfg = config.Default()
	}
	force := set.Bool("force", false, "覆盖已有手动 preset")
	gpu := set.String("gpu-layers", cfg.Runtime.GPULayers, "每个模型的 GPU layers")
	contextSize := set.Int("context-size", cfg.Runtime.ContextSize, "每个模型的上下文大小")
	pooling := set.String("pooling", cfg.Embedding.Pooling, "Embedding pooling")
	batch := set.Int("embedding-batch-size", cfg.Embedding.BatchSize, "Embedding batch size")
	ubatch := set.Int("embedding-ubatch-size", cfg.Embedding.UBatchSize, "Embedding ubatch size")
	mmprojAuto := set.Bool("mmproj-auto", true, "按文件名前缀自动匹配 mmproj")
	if err := set.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	if set.NArg() != 0 {
		return fmt.Errorf("%w: router generate 参数无效", errSyntax)
	}
	if err := validateRouterPresetOptions(*gpu, *contextSize, *pooling, *batch, *ubatch); err != nil {
		return err
	}
	if info, err := os.Lstat(a.Layout.RouterPreset); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Router preset 必须是普通文件且不能是符号链接")
		}
		if !*force {
			return errors.New("手动 Router preset 已存在；确认覆盖后使用 --force")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files, projectors, err := models.CollectRouterModels(a.Layout)
	if err != nil {
		return err
	}
	options := models.PresetOptions{GPULayers: *gpu, ContextSize: *contextSize, Pooling: *pooling, BatchSize: *batch, UBatchSize: *ubatch, DisableMMProjAuto: !*mmprojAuto, Manual: true, CreateOnly: !*force}
	if err := models.WriteRouterPresetWithOptions(a.Layout, a.Layout.RouterPreset, files, projectors, options); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "已生成 Router preset:", a.Layout.RouterPreset)
	return nil
}

func (a *App) key(args []string) error {
	if len(args) == 1 && args[0] == "show" {
		key, err := secrets.Read(a.Layout)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "\nAPI key（请勿共享）")
		fmt.Fprintln(a.Out, "------------------------------------------------------------")
		fmt.Fprintln(a.Out, key)
		fmt.Fprintln(a.Out, "\nAPI key 文件:", a.Layout.APIKeyFile)
		return nil
	}
	if len(args) >= 1 && args[0] == "reset" {
		set := flag.NewFlagSet("key reset", flag.ContinueOnError)
		set.SetOutput(a.Err)
		yes := set.Bool("yes", false, "跳过确认")
		if err := set.Parse(args[1:]); err != nil || set.NArg() != 0 {
			if err == nil {
				err = errors.New("参数无效")
			}
			return fmt.Errorf("%w: %v", errSyntax, err)
		}
		if err := a.confirmDestructive("重置 API key 会立即使旧 key 失效，是否继续", *yes); err != nil {
			return err
		}
		key, err := secrets.Reset(a.Layout)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "已重置 %d 位 API key 并保存到: %s\n", len(key), a.Layout.APIKeyFile)
		return nil
	}
	return fmt.Errorf("%w: key 仅支持 show 或 reset", errSyntax)
}

func (a *App) update(args []string) error {
	if a.Updates == nil {
		return errors.New("更新管理器未初始化")
	}
	ctx := context.Background()
	switch args[0] {
	case "check":
		asJSON := false
		target := "all"
		targetSet := false
		for _, argument := range args[1:] {
			switch argument {
			case "--json":
				if asJSON {
					return fmt.Errorf("%w: --json 重复", errSyntax)
				}
				asJSON = true
			default:
				if strings.HasPrefix(argument, "-") || targetSet {
					return fmt.Errorf("%w: update check 参数无效", errSyntax)
				}
				target, targetSet = argument, true
			}
		}
		if target != "all" && target != "llama" && target != "launcher" {
			return fmt.Errorf("%w: 检查目标必须为 all、llama 或 launcher", errSyntax)
		}
		results, err := a.Updates.Check(ctx, target)
		if err != nil {
			return err
		}
		if asJSON {
			encoder := json.NewEncoder(a.Out)
			encoder.SetEscapeHTML(false)
			return encoder.Encode(results)
		}
		for _, r := range results {
			status := "已是最新"
			if r.Available {
				status = "有可用更新"
			}
			fmt.Fprintf(a.Out, "%s: 当前 %s，最新 %s，%s\n", r.Component, empty(r.Installed, "未安装"), r.Latest, status)
		}
		return nil
	case "llama":
		return a.updateLlama(ctx, args[1:])
	case "launcher":
		return a.updateLauncher(ctx, args[1:])
	case "all":
		return a.updateAll(ctx, args[1:])
	default:
		return fmt.Errorf("%w: 未知 update 子命令 %q", errSyntax, args[0])
	}
}

func (a *App) updateLlama(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("update llama", flag.ContinueOnError)
	set.SetOutput(a.Err)
	options := update.LlamaOptions{}
	set.StringVar(&options.Version, "version", "", "目标 tag（默认 latest）")
	set.StringVar(&options.Backend, "backend", "", "运行时后端")
	set.BoolVar(&options.Reinstall, "reinstall", false, "重装同版本")
	set.BoolVar(&options.AllowDowngrade, "allow-downgrade", false, "允许显式降级")
	yes := set.Bool("yes", false, "跳过确认")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		if err == nil {
			err = errors.New("参数无效")
		}
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	plan, err := a.Updates.PrepareLlama(ctx, options)
	if errors.Is(err, update.ErrAlreadyCurrent) {
		fmt.Fprintln(a.Out, "llama.cpp 已是当前版本。")
		return nil
	}
	if err != nil {
		return err
	}
	if plan.NeedsBackend {
		backend, selectErr := a.chooseLlamaBackend(plan)
		if selectErr != nil {
			return selectErr
		}
		if err := a.Updates.SelectLlamaBackend(plan, backend); err != nil {
			return err
		}
	}
	a.printLlamaPlan(plan)
	if err := a.confirmAction("执行以上 llama.cpp 安装计划，是否继续", *yes, true); err != nil {
		return err
	}
	state, err := a.Updates.ApplyLlama(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "llama.cpp %s (%s) 已安装到 %s\n", state.LlamaTag, state.Backend, update.RuntimePath(a.Layout, state))
	active, err := update.ValidateActiveRuntime(ctx, a.Layout, a.goos())
	if err != nil {
		return err
	}
	if a.RuntimeReporter != nil {
		a.RuntimeReporter(active)
	}
	return nil
}

func (a *App) updateLauncher(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("update launcher", flag.ContinueOnError)
	set.SetOutput(a.Err)
	options := update.LauncherOptions{}
	set.StringVar(&options.Version, "version", "", "目标 SemVer（默认 latest）")
	set.BoolVar(&options.Reinstall, "reinstall", false, "重装同版本")
	set.BoolVar(&options.AllowDowngrade, "allow-downgrade", false, "允许显式降级")
	yes := set.Bool("yes", false, "跳过确认")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		if err == nil {
			err = errors.New("参数无效")
		}
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	plan, err := a.Updates.PrepareLauncherPlan(ctx, options)
	if errors.Is(err, update.ErrAlreadyCurrent) {
		fmt.Fprintln(a.Out, "启动器已是最新版本。")
		return nil
	}
	if err != nil {
		return err
	}
	a.printLauncherPlan(plan)
	if err := a.confirmDestructive("执行以上启动器更新计划并自动重启，是否继续", *yes); err != nil {
		return err
	}
	tag, err := a.Updates.ApplyLauncher(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "启动器 %s 已交给 llamaup；当前进程即将退出。\n", tag)
	return ErrHandoff
}

func (a *App) updateAll(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("update all", flag.ContinueOnError)
	set.SetOutput(a.Err)
	llamaOptions := update.LlamaOptions{}
	launcherOptions := update.LauncherOptions{}
	set.StringVar(&llamaOptions.Version, "llama-version", "", "llama.cpp 目标 tag")
	set.StringVar(&launcherOptions.Version, "launcher-version", "", "启动器目标 SemVer")
	set.StringVar(&llamaOptions.Backend, "backend", "", "运行时后端")
	reinstall := set.Bool("reinstall", false, "重装同版本")
	allowDowngrade := set.Bool("allow-downgrade", false, "允许显式降级")
	yes := set.Bool("yes", false, "跳过确认")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		if err == nil {
			err = errors.New("参数无效")
		}
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	// The public shared switches apply to both components.
	llamaOptions.Reinstall, launcherOptions.Reinstall = *reinstall, *reinstall
	llamaOptions.AllowDowngrade, launcherOptions.AllowDowngrade = *allowDowngrade, *allowDowngrade
	llamaPlan, llamaErr := a.Updates.PrepareLlama(ctx, llamaOptions)
	if llamaErr != nil && !errors.Is(llamaErr, update.ErrAlreadyCurrent) {
		return llamaErr
	}
	launcherPlan, launcherErr := a.Updates.PrepareLauncherPlan(ctx, launcherOptions)
	if launcherErr != nil && !errors.Is(launcherErr, update.ErrAlreadyCurrent) {
		return launcherErr
	}
	if llamaPlan != nil && llamaPlan.NeedsBackend {
		backend, selectErr := a.chooseLlamaBackend(llamaPlan)
		if selectErr != nil {
			return selectErr
		}
		if err := a.Updates.SelectLlamaBackend(llamaPlan, backend); err != nil {
			return err
		}
	}
	if llamaPlan != nil {
		a.printLlamaPlan(llamaPlan)
	} else {
		fmt.Fprintln(a.Out, "llama.cpp: 已是当前版本")
	}
	if launcherPlan != nil {
		a.printLauncherPlan(launcherPlan)
	} else {
		fmt.Fprintln(a.Out, "启动器: 已是当前版本")
	}
	if llamaPlan == nil && launcherPlan == nil {
		return nil
	}
	if err := a.confirmDestructive("执行以上全部更新计划，是否继续", *yes); err != nil {
		return err
	}
	result, err := a.Updates.ApplyAll(ctx, &update.AllPlan{Llama: llamaPlan, Launcher: launcherPlan})
	if result.LlamaApplied {
		fmt.Fprintf(a.Out, "llama.cpp %s (%s) 已安装到 %s\n", result.Llama.LlamaTag, result.Llama.Backend, update.RuntimePath(a.Layout, result.Llama))
		active, validateErr := update.ValidateActiveRuntime(ctx, a.Layout, a.goos())
		if validateErr != nil {
			return validateErr
		}
		if a.RuntimeReporter != nil {
			a.RuntimeReporter(active)
		}
	}
	if err != nil {
		return err
	}
	if result.Handoff {
		fmt.Fprintf(a.Out, "启动器 %s 已交给 llamaup；当前进程即将退出。\n", result.LauncherTag)
		return ErrHandoff
	}
	return nil
}

func (a *App) chooseLlamaBackend(plan *update.LlamaPlan) (string, error) {
	if !a.Interactive {
		return "", fmt.Errorf("非交互模式必须使用 --backend；可用值: %s", strings.Join(plan.AvailableBackends, ", "))
	}
	fmt.Fprintf(a.Out, "\nllama.cpp Release: %s\n可用后端:\n", plan.Release.Tag)
	for index, backend := range plan.AvailableBackends {
		fmt.Fprintf(a.Out, "  [%d] %s\n", index+1, backend)
	}
	if plan.Current.Backend != "" {
		fmt.Fprintf(a.Out, "  当前后端 %s 已不在此 Release 的可用列表中，必须重新选择。\n", plan.Current.Backend)
	}
	reader, ok := a.In.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(a.In)
	}
	for {
		fmt.Fprint(a.Out, "请选择后端编号或完整 ID（0/q 取消）: ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "0" || strings.EqualFold(value, "q") {
			return "", errBack
		}
		if value == "" && errors.Is(err, io.EOF) {
			fmt.Fprintln(a.Out, "操作已取消。")
			return "", errCancelled
		}
		if index, parseErr := strconv.Atoi(value); parseErr == nil {
			if index >= 1 && index <= len(plan.AvailableBackends) {
				return plan.AvailableBackends[index-1], nil
			}
		} else {
			for _, backend := range plan.AvailableBackends {
				if strings.EqualFold(value, backend) {
					return backend, nil
				}
			}
		}
		fmt.Fprintln(a.Err, "错误: 后端选项无效。")
		if errors.Is(err, io.EOF) {
			return "", errCancelled
		}
	}
}

func (a *App) printLlamaPlan(plan *update.LlamaPlan) {
	current := "未安装"
	if plan.CurrentExists && plan.Current.LlamaTag != "" {
		current = plan.Current.LlamaTag + " / " + plan.Current.Backend
	}
	fmt.Fprintf(a.Out, "\nllama.cpp 更新计划\n  当前版本: %s\n  目标版本: %s\n  后端:     %s\n  安装目录: %s\n  下载大小: %s\n", current, plan.Release.Tag, plan.Backend.ID, plan.Target, humanBytes(plan.DownloadSize))
	if plan.NeedsRecovery {
		fmt.Fprintf(a.Out, "  修复操作: 当前状态/运行时无效（%s）\n  恢复目录: %s\n", plan.RecoveryReason, plan.RecoveryDirectory)
	}
}

func (a *App) printLauncherPlan(plan *update.LauncherPlan) {
	fmt.Fprintf(a.Out, "\n启动器更新计划\n  当前版本: %s\n  目标版本: %s\n  安装目录: %s\n  下载大小: %s\n", plan.CurrentVersion, plan.Release.Tag, plan.InstallDir, humanBytes(plan.DownloadSize))
}

func (a *App) confirmDestructive(prompt string, yes bool) error {
	return a.confirmAction(prompt, yes, false)
}

func (a *App) confirmAction(prompt string, yes, defaultValue bool) error {
	if yes {
		return nil
	}
	if !a.Interactive {
		return errors.New("非交互输入必须显式使用 --yes")
	}
	confirmed, err := a.askYesNo(prompt, defaultValue)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(a.Out, "操作已取消。")
		return errCancelled
	}
	return nil
}

func (a *App) askYesNo(prompt string, defaultValue bool) (bool, error) {
	label := "y/N"
	if defaultValue {
		label = "Y/n"
	}
	reader, ok := a.In.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(a.In)
	}
	for {
		fmt.Fprintf(a.Out, "%s [%s]: ", prompt, label)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			return defaultValue, nil
		}
		if value == "y" || value == "yes" {
			return true, nil
		}
		if value == "n" || value == "no" {
			return false, nil
		}
		fmt.Fprintln(a.Err, "错误: 请输入 Y 或 N。")
		if errors.Is(err, io.EOF) {
			return false, io.EOF
		}
	}
}

func validateRouterPresetOptions(gpu string, contextSize int, pooling string, batch, ubatch int) error {
	gpu = strings.ToLower(strings.TrimSpace(gpu))
	if gpu != "auto" && gpu != "all" {
		value, err := strconv.Atoi(gpu)
		if err != nil || value < -1 {
			return errors.New("gpu-layers 必须为 auto、all 或不小于 -1 的整数")
		}
	}
	if contextSize < 0 {
		return errors.New("context-size 不能小于 0")
	}
	switch strings.ToLower(strings.TrimSpace(pooling)) {
	case "", "none", "mean", "cls", "last", "rank":
	default:
		return errors.New("pooling 无效")
	}
	if batch <= 0 || ubatch <= 0 || ubatch > batch {
		return errors.New("embedding batch-size/ubatch-size 无效")
	}
	return nil
}

func serviceURL(host string, port int, path string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.HasSuffix(strings.ToLower(host), ".sock") {
		return host + path
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
}

func (a *App) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

type processExitError struct{ code int }

func (e *processExitError) Error() string { return fmt.Sprintf("llama.cpp 退出码 %d", e.code) }
func empty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
