// Package cli implements the stable grouped command-line interface.
package cli

import (
	"context"
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
	Layout   layout.Layout
	Config   config.Config
	In       io.Reader
	Out, Err io.Writer
	Executor llama.Executor
	Updates  *update.Manager
	GOOS     string
}

func (a *App) Run(args []string) int {
	if len(args) == 0 {
		Usage(a.Out)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		Usage(a.Out)
		return 0
	}
	if hasHelp(args[1:]) {
		commandUsage(a.Out, args)
		return 0
	}
	var err error
	code := 0
	switch args[0] {
	case "version":
		if len(args) != 1 {
			return a.syntax("version 不接受参数")
		}
		fmt.Fprintln(a.Out, buildversion.String())
		return 0
	case "run":
		err = a.run(args[1:])
	case "config":
		err = a.config(args[1:])
	case "update":
		err = a.update(args[1:])
	case "maintenance":
		err = a.maintenance(args[1:])
	default:
		return a.syntax(fmt.Sprintf("未知命令 %q", args[0]))
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) || errors.Is(err, ErrHandoff) {
			return 0
		}
		var processExit *processExitError
		if errors.As(err, &processExit) {
			fmt.Fprintln(a.Err, "错误:", err)
			return processExit.code
		}
		if errors.Is(err, errSyntax) {
			code = 2
		} else {
			code = 1
		}
		fmt.Fprintln(a.Err, "错误:", err)
	}
	return code
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
		fmt.Fprintf(w, "用法: llamalc %s [--model 文件] [运行选项] [-- llama.cpp 参数]\n", name)
	case name == "config router generate":
		fmt.Fprintln(w, "用法: llamalc config router generate")
	case strings.HasPrefix(name, "config key "):
		fmt.Fprintf(w, "用法: llamalc %s\n", name)
	case name == "update check":
		fmt.Fprintln(w, "用法: llamalc update check [all|llama|launcher]")
	case name == "update llama" || name == "update all":
		fmt.Fprintf(w, "用法: llamalc %s [--backend ID] [--reinstall]\n", name)
	case name == "update launcher":
		fmt.Fprintln(w, "用法: llamalc update launcher")
	case name == "maintenance cleanup":
		fmt.Fprintln(w, "用法: llamalc maintenance cleanup")
	default:
		Usage(w)
	}
}

var errSyntax = errors.New("命令语法错误")

func (a *App) syntax(message string) int {
	fmt.Fprintln(a.Err, "错误:", message)
	Usage(a.Err)
	return 2
}
func Usage(w io.Writer) {
	fmt.Fprint(w, `LlamaLc 命令行

用法:
  llamalc run api [选项] [-- llama.cpp 参数]
  llamalc run embedding [选项] [-- llama.cpp 参数]
  llamalc run rerank [选项] [-- llama.cpp 参数]
  llamalc run router [选项] [-- llama.cpp 参数]
  llamalc run chat [选项] [-- llama.cpp 参数]
  llamalc config router generate
  llamalc config key show|reset
  llamalc update check [all|llama|launcher]
  llamalc update llama [--backend ID] [--reinstall]
  llamalc update launcher
  llamalc update all [--backend ID]
  llamalc maintenance cleanup
  llamalc version
`)
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
	set := flag.NewFlagSet("run "+args[0], flag.ContinueOnError)
	set.SetOutput(a.Err)
	model := set.String("model", "", "模型文件名或路径")
	mmproj := set.String("mmproj", "", "多模态 projector")
	imageMinTokens := set.Int("image-min-tokens", 0, "最小图片 token 数")
	imageMaxTokens := set.Int("image-max-tokens", 0, "最大图片 token 数")
	defaultPreset := a.Layout.AutoRouterPreset
	if info, statErr := os.Lstat(a.Layout.RouterPreset); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		defaultPreset = a.Layout.RouterPreset
	}
	preset := set.String("preset", defaultPreset, "Router preset")
	host := set.String("host", a.Config.API.Host, "监听地址")
	port := set.Int("port", a.Config.API.Port, "监听端口")
	gpu := a.Config.Runtime.GPULayers
	set.StringVar(&gpu, "gpu-layers", gpu, "GPU layers")
	set.StringVar(&gpu, "n-gpu-layers", gpu, "--gpu-layers 的兼容参数名")
	ctx := a.Config.Runtime.ContextSize
	set.IntVar(&ctx, "context-size", ctx, "上下文大小")
	set.IntVar(&ctx, "ctx-size", ctx, "--context-size 的兼容参数名")
	threads := set.Int("threads", a.Config.Runtime.Threads, "线程数")
	batchDefault := a.Config.Runtime.BatchSize
	ubatchDefault := a.Config.Runtime.UBatchSize
	if mode == llama.Embedding {
		batchDefault = a.Config.Embedding.BatchSize
		ubatchDefault = a.Config.Embedding.UBatchSize
	}
	batch := set.Int("batch-size", batchDefault, "batch size")
	ubatch := set.Int("ubatch-size", ubatchDefault, "ubatch size")
	flash := a.Config.Runtime.FlashAttention
	set.StringVar(&flash, "flash-attention", flash, "auto|on|off")
	set.StringVar(&flash, "flash-attn", flash, "--flash-attention 的兼容参数名")
	parallel := set.Int("parallel", a.Config.API.Parallel, "并发数")
	ui := set.Bool("ui", a.Config.API.UI, "启用 Web UI")
	pooling := set.String("pooling", a.Config.Embedding.Pooling, "Embedding pooling")
	normalize := a.Config.Embedding.Normalize
	set.IntVar(&normalize, "normalize", normalize, "Embedding normalize")
	set.IntVar(&normalize, "embd-normalize", normalize, "--normalize 的兼容参数名")
	embeddingBatch := set.Int("embedding-batch-size", a.Config.Embedding.BatchSize, "Router Embedding batch size")
	embeddingUBatch := set.Int("embedding-ubatch-size", a.Config.Embedding.UBatchSize, "Router Embedding ubatch size")
	modelsMax := set.Int("models-max", a.Config.Router.ModelsMax, "Router 最大已加载模型数")
	autoload := set.Bool("autoload", a.Config.Router.Autoload, "Router 自动加载")
	if err := set.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	extra := set.Args()
	s, exists, err := update.LoadState(a.Layout)
	if err != nil {
		return err
	}
	if !exists || s.ActiveRuntime == "" {
		return errors.New("llama.cpp 尚未安装；请运行 llamalc update llama --backend <ID>")
	}
	rt, err := llama.Locate(update.RuntimePath(a.Layout, s), a.goos())
	if err != nil {
		return err
	}
	o := llama.Options{Preset: *preset, Host: *host, Port: *port, GPULayers: strings.ToLower(strings.TrimSpace(gpu)), ContextSize: ctx, Threads: *threads, BatchSize: *batch, UBatchSize: *ubatch, FlashAttention: strings.ToLower(strings.TrimSpace(flash)), Parallel: *parallel, UI: *ui, Pooling: strings.ToLower(strings.TrimSpace(*pooling)), Normalize: normalize, NormalizeSet: true, ModelsMax: *modelsMax, Autoload: *autoload, APIKeyFile: a.Layout.APIKeyFile, Extra: extra, ImageMinTokens: *imageMinTokens, ImageMaxTokens: *imageMaxTokens}
	if mode != llama.Router {
		kind := models.Generation
		if mode == llama.Embedding {
			kind = models.Embedding
		}
		if mode == llama.Rerank {
			kind = models.Rerank
		}
		f, err := models.Resolve(a.Layout, kind, *model)
		if err != nil {
			return err
		}
		o.Model = f.Path
	} else {
		files, projectors, scanErr := models.CollectRouterModels(a.Layout)
		if scanErr != nil {
			return scanErr
		}
		presetOptions := models.PresetOptions{
			GPULayers: strings.ToLower(strings.TrimSpace(gpu)), ContextSize: ctx,
			Pooling: strings.ToLower(strings.TrimSpace(*pooling)), BatchSize: *embeddingBatch,
			UBatchSize: *embeddingUBatch,
		}
		if err := validateRouterPresetOptions(gpu, ctx, *pooling, *embeddingBatch, *embeddingUBatch); err != nil {
			return err
		}
		if writeErr := models.WriteRouterPresetWithOptions(a.Layout, a.Layout.AutoRouterPreset, files, projectors, presetOptions); writeErr != nil {
			return writeErr
		}
		if *preset != a.Layout.AutoRouterPreset {
			info, statErr := os.Lstat(*preset)
			if statErr != nil {
				return statErr
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("Router preset 必须是普通文件且不能是符号链接")
			}
		}
	}
	if *mmproj != "" {
		if mode != llama.API {
			return errors.New("--mmproj 仅适用于单模型 API 模式")
		}
		f, err := models.Resolve(a.Layout, models.MMProj, *mmproj)
		if err != nil {
			return err
		}
		o.MMProj = f.Path
	}
	if mode != llama.Chat {
		if err := llama.ValidateExposure(*host, a.Layout.APIKeyFile, append([]string{"--api-key-file", a.Layout.APIKeyFile}, extra...)); err != nil {
			return err
		}
		effectiveHost := llama.EffectiveHost(*host, extra)
		if !llama.IsLocalHost(effectiveHost) {
			label := "服务"
			if mode == llama.Router {
				label = "Router"
			}
			fmt.Fprintf(a.Err, "安全警告: %s 将监听非本机地址 %s，请确认防火墙和 API key 配置。\n", label, effectiveHost)
			fmt.Fprintln(a.Err, "安全提示: llama-server 的 /models、/v1/models、健康检查和 UI 静态资源不受 API key 保护。")
		}
	}
	command, err := llama.Build(mode, rt, a.Layout.Root, o)
	if err != nil {
		return err
	}
	if mode == llama.Router {
		if *preset == a.Layout.RouterPreset {
			fmt.Fprintln(a.Out, "检测到手动配置，运行时优先使用且不会覆盖:", *preset)
		} else {
			fmt.Fprintln(a.Out, "已生成自动 Router 配置:", a.Layout.AutoRouterPreset)
		}
		files, _, _ := models.CollectRouterModels(a.Layout)
		fmt.Fprintf(a.Out, "Router 共发现 %d 个模型。\n", len(files))
	}
	fmt.Fprintln(a.Out, "最终命令:")
	fmt.Fprintln(a.Out, " ", llama.Format(command))
	if mode != llama.Chat {
		path := ""
		switch mode {
		case llama.Embedding:
			path = "/v1/embeddings"
		case llama.Rerank:
			path = "/v1/rerank"
		case llama.Router:
			path = "/models"
		}
		fmt.Fprintln(a.Out, "访问地址:", serviceURL(llama.EffectiveHost(*host, extra), *port, path))
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
func (a *App) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

func (a *App) config(args []string) error {
	if len(args) >= 2 && args[0] == "router" && args[1] == "generate" {
		set := flag.NewFlagSet("config router generate", flag.ContinueOnError)
		set.SetOutput(a.Err)
		force := set.Bool("force", false, "覆盖已有手动 preset")
		gpu := set.String("gpu-layers", a.Config.Runtime.GPULayers, "每个模型的 GPU layers")
		contextSize := set.Int("context-size", a.Config.Runtime.ContextSize, "每个模型的上下文大小")
		pooling := set.String("pooling", a.Config.Embedding.Pooling, "Embedding pooling")
		batch := set.Int("embedding-batch-size", a.Config.Embedding.BatchSize, "Embedding batch size")
		ubatch := set.Int("embedding-ubatch-size", a.Config.Embedding.UBatchSize, "Embedding ubatch size")
		mmprojAuto := set.Bool("mmproj-auto", true, "按文件名前缀自动匹配 mmproj")
		if err := set.Parse(args[2:]); err != nil {
			return fmt.Errorf("%w: %v", errSyntax, err)
		}
		if set.NArg() != 0 {
			return fmt.Errorf("%w: config router generate 参数无效", errSyntax)
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
		options := models.PresetOptions{GPULayers: *gpu, ContextSize: *contextSize, Pooling: *pooling, BatchSize: *batch, UBatchSize: *ubatch, DisableMMProjAuto: !*mmprojAuto, Manual: true}
		if err = models.WriteRouterPresetWithOptions(a.Layout, a.Layout.RouterPreset, files, projectors, options); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "已生成 Router preset:", a.Layout.RouterPreset)
		return nil
	}
	if len(args) == 2 && args[0] == "key" {
		switch args[1] {
		case "show":
			key, err := secrets.Read(a.Layout)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, "\nAPI key（请勿共享）")
			fmt.Fprintln(a.Out, "------------------------------------------------------------")
			fmt.Fprintln(a.Out, key)
			fmt.Fprintln(a.Out, "\nAPI key 文件:", a.Layout.APIKeyFile)
			return nil
		case "reset":
			key, err := secrets.Reset(a.Layout)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "已重置 %d 位 API key 并保存到: %s\n", len(key), a.Layout.APIKeyFile)
			fmt.Fprintln(a.Out, "API key 文件:", a.Layout.APIKeyFile)
			return nil
		}
	}
	return fmt.Errorf("%w: config 仅支持 router generate、key show、key reset", errSyntax)
}

func serviceURL(host string, port int, path string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.HasSuffix(strings.ToLower(host), ".sock") {
		return host + path
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
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

func (a *App) update(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: update 需要子命令", errSyntax)
	}
	ctx := context.Background()
	switch args[0] {
	case "check":
		target := "all"
		if len(args) > 2 {
			return fmt.Errorf("%w: update check 参数过多", errSyntax)
		}
		if len(args) == 2 {
			target = args[1]
		}
		if target != "all" && target != "llama" && target != "launcher" {
			return fmt.Errorf("%w: 检查目标必须为 all、llama 或 launcher", errSyntax)
		}
		results, err := a.Updates.Check(ctx, target)
		if err != nil {
			return err
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
		if len(args) != 1 {
			return fmt.Errorf("%w: update launcher 不接受参数", errSyntax)
		}
		tag, err := a.Updates.StartLauncherUpdate(ctx)
		if err != nil {
			if errors.Is(err, update.ErrAlreadyCurrent) {
				fmt.Fprintln(a.Out, "启动器已是最新版本。")
				return nil
			}
			return err
		}
		fmt.Fprintf(a.Out, "启动器 %s 已交给 llamaup；当前进程即将退出。\n", tag)
		return ErrHandoff
	case "all":
		if err := a.updateLlama(ctx, args[1:]); err != nil {
			return err
		}
		tag, err := a.Updates.StartLauncherUpdate(ctx)
		if err != nil {
			if errors.Is(err, update.ErrAlreadyCurrent) {
				fmt.Fprintln(a.Out, "启动器已是最新版本。")
				return nil
			}
			return err
		}
		fmt.Fprintf(a.Out, "启动器 %s 已交给 llamaup；当前进程即将退出。\n", tag)
		return ErrHandoff
	default:
		return fmt.Errorf("%w: 未知 update 子命令 %q", errSyntax, args[0])
	}
}

var ErrHandoff = errors.New("更新器交接完成")

func (a *App) updateLlama(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("update llama", flag.ContinueOnError)
	set.SetOutput(a.Err)
	backend := set.String("backend", "", "运行时后端")
	reinstall := set.Bool("reinstall", false, "重装同版本")
	if err := set.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errSyntax, err)
	}
	if set.NArg() != 0 {
		return fmt.Errorf("%w: update llama 参数无效", errSyntax)
	}
	s, err := a.Updates.UpdateLlama(ctx, *backend, *reinstall)
	if err != nil {
		if errors.Is(err, update.ErrAlreadyCurrent) {
			fmt.Fprintf(a.Out, "llama.cpp %s 已是最新版本。\n", s.LlamaTag)
			return nil
		}
		return err
	}
	fmt.Fprintf(a.Out, "llama.cpp %s (%s) 安装/更新成功。\n", s.LlamaTag, s.Backend)
	return nil
}

type processExitError struct{ code int }

func (e *processExitError) Error() string { return fmt.Sprintf("llama.cpp 退出码 %d", e.code) }
func empty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func (a *App) maintenance(args []string) error {
	if len(args) != 1 || args[0] != "cleanup" {
		return fmt.Errorf("%w: maintenance 仅支持 cleanup", errSyntax)
	}
	return a.runCleanupMenu()
}
