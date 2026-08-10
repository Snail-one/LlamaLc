// Package launcher initializes the deployment and wires CLI/TUI dependencies.
package launcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/cli"
	"github.com/Snail-one/LlamaLc/internal/config"
	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/models"
	"github.com/Snail-one/LlamaLc/internal/release"
	"github.com/Snail-one/LlamaLc/internal/secrets"
	"github.com/Snail-one/LlamaLc/internal/tui"
	"github.com/Snail-one/LlamaLc/internal/update"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

var detectLayout = layout.Detect

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, executor llama.Executor) int {
	if len(args) > 0 && (args[0] == "version" || args[0] == "-v" || args[0] == "--version" || args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return (&cli.App{In: stdin, Out: stdout, Err: stderr}).Run(args)
	}
	if len(args) > 0 && !knownTopLevel(args[0]) {
		fmt.Fprintf(stderr, "错误: 未知命令 %q\n", args[0])
		cli.Usage(stderr)
		return 2
	}
	if (len(args) == 1 && args[0] == "update") || helpRequested(args) {
		return (&cli.App{In: stdin, Out: stdout, Err: stderr}).Run(args)
	}
	l, err := detectLayout()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	return MainWithLayout(args, l, stdin, stdout, stderr, executor)
}

func helpRequested(args []string) bool {
	for _, argument := range args {
		if argument == "--" {
			return false
		}
		if argument == "-h" || argument == "--help" {
			return true
		}
	}
	return false
}
func knownTopLevel(name string) bool {
	switch name {
	case "run", "router", "key", "update", "cleanup", "version", "help", "-h", "--help", "-v", "--version":
		return true
	}
	return false
}
func MainWithLayout(args []string, l layout.Layout, stdin io.Reader, stdout, stderr io.Writer, executor llama.Executor) int {
	if err := validateDeploymentBin(l); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	housekeeping := update.StartupHousekeeping(l)
	if len(housekeeping.Removed) > 0 {
		fmt.Fprintf(stdout, "启动清理完成: %d 项\n", len(housekeeping.Removed))
	}
	for _, warning := range housekeeping.Warnings {
		fmt.Fprintln(stderr, "警告: 启动清理未完成，下次将重试:", warning)
	}
	client := release.NewClient(os.Getenv("LLAMALC_GITHUB_PROXY"))
	client.Progress = cli.NewDownloadReporter(stdout)
	manager := update.NewManager(l, client)
	manager.Out, manager.Err = stdout, stderr
	app := &cli.App{Layout: l, Config: config.Default(), In: stdin, Out: stdout, Err: stderr, Executor: executor, Updates: manager, GOOS: runtime.GOOS, Interactive: inputIsTerminal(stdin)}
	lastRuntimeReport := ""
	reportRuntime := func(active update.ActiveRuntime) {
		key := active.Runtime.Server + "\x00" + active.State.LlamaTag + "\x00" + active.Detected
		if key == lastRuntimeReport {
			return
		}
		fmt.Fprintln(stdout, "实际探测文件:", active.Runtime.Server)
		fmt.Fprintln(stdout, "已识别 llama.cpp:", active.Detected)
		lastRuntimeReport = key
	}
	app.RuntimeReporter = reportRuntime
	if len(args) > 0 {
		if err := initializeForCommand(args, l, app, stdout, reportRuntime); err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		return app.Run(args)
	}
	active, runtimeErr := update.ValidateActiveRuntime(context.Background(), l, runtime.GOOS)
	runtimeInstalled := runtimeErr == nil
	if runtimeErr != nil && !errors.Is(runtimeErr, update.ErrRuntimeNotInstalled) {
		fmt.Fprintln(stderr, "错误:", runtimeErr)
		return 1
	}
	if runtimeInstalled {
		reportRuntime(active)
	}
	if err := ensureManagementDirectories(l); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	reader := bufio.NewReader(stdin)
	app.In = reader
	app.Interactive = true
	llamaVersion := ""
	if runtimeInstalled {
		llamaVersion = active.Display()
	}
	cfg := config.Default()
	if runtimeInstalled {
		var initErr error
		cfg, initErr = initializeOperational(l, stdout)
		if initErr != nil {
			fmt.Fprintln(stderr, "错误:", initErr)
			return 1
		}
		app.Config = cfg
	}
	notice := strings.TrimSpace(os.Getenv("LLAMALC_UPDATED_VERSION"))
	_ = os.Unsetenv("LLAMALC_UPDATED_VERSION")
	if notice != buildversion.Version {
		notice = ""
	}
	var menu *tui.App
	initializeInstalledRuntime := func() error {
		validated, err := update.ValidateActiveRuntime(context.Background(), l, runtime.GOOS)
		if err != nil {
			return err
		}
		reportRuntime(validated)
		version := validated.Display()
		loaded, err := initializeOperational(l, stdout)
		if err != nil {
			return err
		}
		app.Config = loaded
		if menu != nil {
			menu.Defaults = launchDefaults(loaded)
			menu.LlamaVersion = version
		}
		return nil
	}
	runFromMenu := func(command []string) int {
		firstInstall := menu != nil && !menu.RuntimeInstalled && len(command) >= 2 && command[0] == "update" && command[1] == "llama"
		code := app.Run(command)
		if code == 0 && !firstInstall && len(command) >= 2 && command[0] == "update" && command[1] == "llama" {
			if err := initializeInstalledRuntime(); err != nil {
				fmt.Fprintln(stderr, "错误: 安装完成但无法初始化运行目录:", err)
				return 1
			}
		}
		return code
	}
	runResultFromMenu := func(command []string) tui.CommandResult {
		firstInstall := menu != nil && !menu.RuntimeInstalled && len(command) >= 2 && command[0] == "update" && command[1] == "llama"
		outcome := app.RunWithResult(command)
		if outcome.Code == 0 && !outcome.Cancelled && !firstInstall && len(command) >= 2 && command[0] == "update" && command[1] == "llama" {
			if err := initializeInstalledRuntime(); err != nil {
				fmt.Fprintln(stderr, "错误: 安装完成但无法初始化运行目录:", err)
				return tui.CommandResult{Code: 1}
			}
		}
		return tui.CommandResult{Code: outcome.Code, Success: outcome.Success, Cancelled: outcome.Cancelled, Back: outcome.Back, Handoff: outcome.Handoff}
	}
	menu = &tui.App{Reader: reader, Out: stdout, Err: stderr, Root: l.Root, LauncherVersion: buildversion.Version, LlamaVersion: llamaVersion, UpdateNotice: notice, Run: runFromMenu, Ready: signalUpdateReady,
		RunResult:    runResultFromMenu,
		LaunchWizard: true, ClassicInteraction: true, RuntimeInstalled: runtimeInstalled, AfterLlamaInstall: initializeInstalledRuntime,
		RefreshLlamaVersion: func() string {
			validated, err := update.ValidateActiveRuntime(context.Background(), l, runtime.GOOS)
			if err != nil {
				return ""
			}
			reportRuntime(validated)
			return validated.Display()
		},
		Defaults:       launchDefaults(cfg),
		BackendOptions: func() (string, []string, string, error) { return manager.AvailableLlamaBackends(context.Background()) },
		RouterPresetExists: func() bool {
			info, err := os.Lstat(l.RouterPreset)
			return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
		},
		ModelOptions: func(kind string) (string, []tui.ModelOption, error) {
			modelKind := models.Kind(kind)
			directory, err := models.Directory(l, modelKind)
			if err != nil {
				return "", nil, err
			}
			files, err := models.Scan(l, modelKind)
			if err != nil {
				return directory, nil, err
			}
			options := make([]tui.ModelOption, 0, len(files))
			for _, file := range files {
				options = append(options, tui.ModelOption{ID: file.ID, Path: file.Path, Size: file.Size})
			}
			return directory, options, nil
		}}
	return menu.RunMenu()
}

func validateDeploymentBin(l layout.Layout) error {
	if filepath.Base(l.Root) != layout.RootName && !(runtime.GOOS == "windows" && strings.EqualFold(filepath.Base(l.Root), layout.RootName)) {
		return fmt.Errorf("部署根目录必须命名为 %s", layout.RootName)
	}
	info, err := os.Lstat(l.Bin)
	if err != nil {
		return fmt.Errorf("实际操作要求有效的 %s: %w", filepath.Join(layout.RootName, "bin"), err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("LlamaLc/bin 必须是普通目录且不能是符号链接")
	}
	return nil
}

func inputIsTerminal(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func ensureManagementDirectories(l layout.Layout) error {
	for _, directory := range []string{l.StateDir, l.RuntimeDir, l.LlamaRuntimeDir, l.RecoveryDir} {
		if err := managedfs.EnsureDir(l.Root, directory, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func initializeOperational(l layout.Layout, output io.Writer) (config.Config, error) {
	// Existing business files are validated before any missing directory or
	// file is created. A damaged config/key therefore leaves the deployment
	// byte-for-byte unchanged.
	preflightConfig, configMissing, err := config.Load(l)
	if err != nil {
		return config.Config{}, err
	}
	_, keyErr := secrets.Read(l)
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
		return config.Config{}, keyErr
	}
	for _, directory := range l.Directories() {
		created, err := ensureDirectory(l.Root, directory)
		if err != nil {
			return config.Config{}, err
		}
		if created && isModelDirectory(l, directory) {
			fmt.Fprintln(output, "已创建目录:", directory)
		}
	}
	key, keyCreated, err := secrets.Ensure(l)
	if err != nil {
		return config.Config{}, err
	}
	if keyCreated {
		fmt.Fprintf(output, "已自动生成 %d 位 API key 并保存到: %s\n", len(key), l.APIKeyFile)
	}
	fmt.Fprintln(output, "API key 文件:", l.APIKeyFile)
	cfg, created, err := config.Ensure(l)
	if err != nil {
		return config.Config{}, err
	}
	if created {
		fmt.Fprintln(output, "已生成配置:", l.ConfigFile)
	}
	if !configMissing {
		cfg = preflightConfig
	}
	return cfg, nil
}

func ensureDirectory(root, directory string) (bool, error) {
	_, err := os.Lstat(directory)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return false, err
	}
	if err := managedfs.EnsureDir(root, directory, 0o700); err != nil {
		return false, err
	}
	return created, nil
}

func isModelDirectory(l layout.Layout, directory string) bool {
	directory = filepath.Clean(directory)
	for _, candidate := range []string{l.GenerationModels, l.EmbeddingModels, l.RerankModels, l.MMProjModels} {
		if directory == filepath.Clean(candidate) {
			return true
		}
	}
	return false
}

func reportActiveRuntime(l layout.Layout, output io.Writer) (string, error) {
	active, err := update.ValidateActiveRuntime(context.Background(), l, runtime.GOOS)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(output, "实际探测文件:", active.Runtime.Server)
	fmt.Fprintln(output, "已识别 llama.cpp:", active.Detected)
	return active.Display(), nil
}

func initializeForCommand(args []string, l layout.Layout, app *cli.App, output io.Writer, report func(update.ActiveRuntime)) error {
	switch args[0] {
	case "update", "cleanup":
		return ensureManagementDirectories(l)
	case "router":
		cfg, _, err := config.Load(l)
		if err != nil {
			return err
		}
		for _, directory := range []string{l.ConfigDir, l.RouterConfigDir, l.StateDir, l.RouterStateDir, l.GenerationModels, l.EmbeddingModels, l.RerankModels, l.MMProjModels} {
			if err := managedfs.EnsureDir(l.Root, directory, 0o700); err != nil {
				return err
			}
		}
		app.Config = cfg
		return nil
	case "key":
		if err := managedfs.EnsureDir(l.Root, l.SecretsDir, 0o700); err != nil {
			return err
		}
		if len(args) >= 2 && args[1] == "reset" {
			return nil
		}
		_, _, err := secrets.Ensure(l)
		return err
	case "run":
		active, err := update.ValidateActiveRuntime(context.Background(), l, app.GOOS)
		if err != nil {
			return err
		}
		app.PreparedRuntime = &active
		if report != nil {
			report(active)
		}
		cfg, err := initializeOperational(l, output)
		app.Config = cfg
		return err
	}
	return nil
}

func launchDefaults(cfg config.Config) tui.LaunchDefaults {
	return tui.LaunchDefaults{GPULayers: cfg.Runtime.GPULayers, FlashAttention: cfg.Runtime.FlashAttention, Host: cfg.API.Host, Pooling: cfg.Embedding.Pooling, ContextSize: cfg.Runtime.ContextSize, Threads: cfg.Runtime.Threads, BatchSize: cfg.Runtime.BatchSize, UBatchSize: cfg.Runtime.UBatchSize, Parallel: cfg.API.Parallel, Port: cfg.API.Port, EmbeddingBatch: cfg.Embedding.BatchSize, EmbeddingUBatch: cfg.Embedding.UBatchSize, Normalize: cfg.Embedding.Normalize, ModelsMax: cfg.Router.ModelsMax, UI: cfg.API.UI, Autoload: cfg.Router.Autoload}
}
