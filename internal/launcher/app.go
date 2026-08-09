// Package launcher initializes the deployment and wires CLI/TUI dependencies.
package launcher

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/cli"
	"github.com/Snail-one/LlamaLc/internal/config"
	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/llama"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/release"
	"github.com/Snail-one/LlamaLc/internal/secrets"
	"github.com/Snail-one/LlamaLc/internal/tui"
	"github.com/Snail-one/LlamaLc/internal/update"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

var detectLayout = layout.Detect

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, executor llama.Executor) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "-v" || args[0] == "--version") {
		fmt.Fprintln(stdout, buildversion.String())
		return 0
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		cli.Usage(stdout)
		return 0
	}
	if len(args) > 0 && !knownTopLevel(args[0]) {
		fmt.Fprintf(stderr, "错误: 未知命令 %q\n", args[0])
		cli.Usage(stderr)
		return 2
	}
	l, err := detectLayout()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	return MainWithLayout(args, l, stdin, stdout, stderr, executor)
}
func knownTopLevel(name string) bool {
	switch name {
	case "run", "config", "update", "maintenance", "version":
		return true
	}
	return false
}
func MainWithLayout(args []string, l layout.Layout, stdin io.Reader, stdout, stderr io.Writer, executor llama.Executor) int {
	for _, directory := range l.Directories() {
		if err := managedfs.EnsureDir(l.Root, directory, 0o700); err != nil {
			fmt.Fprintln(stderr, "错误: 创建部署目录:", err)
			return 1
		}
	}
	cleanupUpdaterRunners(l, stderr)
	cfg, created, err := config.Ensure(l)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if created {
		fmt.Fprintln(stdout, "已生成配置:", l.ConfigFile)
	}
	_, keyCreated, err := secrets.Ensure(l)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	if keyCreated {
		fmt.Fprintln(stdout, "已生成 API key:", l.APIKeyFile)
	}
	for _, path := range l.LegacyPaths() {
		fmt.Fprintf(stderr, "旧版路径提示: %s（不会自动迁移或删除；可在清理界面逐项处理）\n", path)
	}
	client := release.NewClient(os.Getenv("LLAMALC_GITHUB_PROXY"))
	manager := update.NewManager(l, client)
	app := &cli.App{Layout: l, Config: cfg, In: stdin, Out: stdout, Err: stderr, Executor: executor, Updates: manager, GOOS: runtime.GOOS}
	if len(args) > 0 {
		return app.Run(args)
	}
	reader := bufio.NewReader(stdin)
	app.In = reader
	llamaVersion := ""
	if state, exists, e := update.LoadState(l); e == nil && exists && state.ActiveRuntime != "" {
		if rt, e := llama.Locate(update.RuntimePath(l, state), runtime.GOOS); e == nil {
			if v, e := llama.ProbeVersion(context.Background(), rt.Server); e == nil {
				llamaVersion = state.LlamaTag + " / " + state.Backend + " — " + v
			} else {
				llamaVersion = state.LlamaTag + " / " + state.Backend
			}
		}
	}
	notice := strings.TrimSpace(os.Getenv("LLAMALC_UPDATED_VERSION"))
	_ = os.Unsetenv("LLAMALC_UPDATED_VERSION")
	if notice != buildversion.Version {
		notice = ""
	}
	menu := &tui.App{Reader: reader, Out: stdout, Err: stderr, Root: l.Root, LauncherVersion: buildversion.Version, LlamaVersion: llamaVersion, UpdateNotice: notice, Run: app.Run, Ready: signalUpdateReady,
		BackendOptions: func() (string, []string, string, error) { return manager.AvailableLlamaBackends(context.Background()) }}
	return menu.RunMenu()
}

func cleanupUpdaterRunners(l layout.Layout, stderr io.Writer) {
	entries, err := os.ReadDir(l.Bin)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if !strings.HasPrefix(name, ".llamaup-run-") || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.Remove(l.Bin + string(os.PathSeparator) + entry.Name()); err != nil {
			fmt.Fprintln(stderr, "警告: 暂时无法清理 updater 运行副本:", entry.Name())
		}
	}
}
