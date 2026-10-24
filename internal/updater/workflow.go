package updater

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/cli"
	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/procinfo"
	"github.com/Snail-one/LlamaLc/internal/release"
	"github.com/Snail-one/LlamaLc/internal/update"
)

// These statuses are a process protocol, not failures. The launcher must exit
// only after the updater has successfully prepared and handed off installation.
const (
	exitHandoff   = 75
	exitCancelled = 76
)

var newWorkflowManager = func(l layout.Layout, out, stderr io.Writer) *update.Manager {
	client := release.NewClient(os.Getenv("LLAMALC_GITHUB_PROXY"))
	client.Progress = cli.NewDownloadReporter(out)
	manager := update.NewManager(l, client)
	manager.Out, manager.Err = out, stderr
	return manager
}

// RunExternal keeps the launcher alive while the updater checks, confirms and
// downloads. Only an explicit installation handoff asks the launcher to exit.
func RunExternal(ctx context.Context, l layout.Layout, options update.LauncherOptions, yes bool, in io.Reader, out, stderr io.Writer) (cli.CommandOutcome, error) {
	if err := managedfs.Validate(l.Root, l.Updater, false); err != nil {
		return cli.CommandOutcome{}, err
	}
	if _, err := stableUpdaterFileInfo(l.Updater); err != nil {
		return cli.CommandOutcome{}, fmt.Errorf("无法启动独立更新器: %w", err)
	}
	identity, alive, err := procinfo.Identity(os.Getpid())
	if err != nil || !alive || identity == "" {
		return cli.CommandOutcome{}, errors.New("无法确认主程序进程身份")
	}
	args := []string{"update", "--launcher-parent-pid", strconv.Itoa(os.Getpid()), "--launcher-parent-identity", identity}
	if options.Version != "" {
		args = append(args, "--version", options.Version)
	}
	if options.Reinstall {
		args = append(args, "--reinstall")
	}
	if options.AllowDowngrade {
		args = append(args, "--allow-downgrade")
	}
	if yes {
		args = append(args, "--yes")
	}
	cmd := exec.CommandContext(ctx, l.Updater, args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = l.Root, in, out, stderr
	// A detached installer inherits output. Bound waits on os/exec's optional
	// copying pipes when the caller supplies writers rather than terminal files.
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if err == nil {
		return cli.CommandOutcome{Success: true}, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case exitHandoff:
			return cli.CommandOutcome{Handoff: true}, nil
		case exitCancelled:
			return cli.CommandOutcome{Cancelled: true}, nil
		}
	}
	return cli.CommandOutcome{}, fmt.Errorf("独立更新器执行失败: %w", err)
}

func runWorkflow(args []string, in io.Reader, out, stderr io.Writer) int {
	command := "update"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	if command != "update" && command != "check" && command != "recover" {
		printUsage(stderr)
		return 2
	}
	set := flag.NewFlagSet("llamaup "+command, flag.ContinueOnError)
	set.SetOutput(stderr)
	options := update.LauncherOptions{}
	yes, asJSON := false, false
	parentPID, parentIdentity := 0, ""
	if command == "update" {
		set.StringVar(&options.Version, "version", "", "目标正式版本，默认 latest")
		set.BoolVar(&options.Reinstall, "reinstall", false, "重装同版本")
		set.BoolVar(&options.AllowDowngrade, "allow-downgrade", false, "允许降级")
		set.BoolVar(&yes, "yes", false, "跳过确认")
		set.IntVar(&parentPID, "launcher-parent-pid", 0, "内部交接参数")
		set.StringVar(&parentIdentity, "launcher-parent-identity", "", "内部交接参数")
	} else if command == "check" {
		set.BoolVar(&asJSON, "json", false, "JSON 输出")
	} else {
		set.BoolVar(&yes, "yes", false, "跳过恢复确认")
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if set.NArg() != 0 {
		fmt.Fprintln(stderr, "错误: 不接受位置参数")
		return 2
	}
	if parentPID != 0 || parentIdentity != "" {
		identity, alive, err := procinfo.Identity(parentPID)
		if parentPID != os.Getppid() || err != nil || !alive || identity != parentIdentity {
			fmt.Fprintln(stderr, "错误: 主程序交接进程身份无效")
			return 1
		}
	}
	root, err := executableRoot()
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	l, err := layout.New(root, runtime.GOOS)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	manager := newWorkflowManager(l, out, stderr)
	manager.LauncherParentPID, manager.LauncherParentIdentity = parentPID, parentIdentity
	if command == "recover" {
		if !transactionPending(root) {
			fmt.Fprintln(out, "没有需要恢复的启动器更新。")
			return 0
		}
		// Parse and validate before requesting confirmation or launching a runner.
		if _, err := readTransaction(l.Bin, runtime.GOOS); err != nil {
			fmt.Fprintln(stderr, "错误: 恢复记录无效，备份已保留:", err)
			return 1
		}
		if !yes {
			if !terminalInput(in) {
				fmt.Fprintln(stderr, "错误: 非交互输入必须显式使用 --yes")
				return 1
			}
			fmt.Fprint(out, "恢复中断的更新并启动主程序，是否继续 [y/N]: ")
			line, err := bufio.NewReader(in).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				fmt.Fprintln(stderr, "错误:", err)
				return 1
			}
			answer := strings.ToLower(strings.TrimSpace(line))
			if answer != "y" && answer != "yes" {
				fmt.Fprintln(out, "操作已取消。")
				return 0
			}
		}
		if err := manager.StartLauncherRecovery(context.Background()); err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		fmt.Fprintln(out, "恢复任务已交接，请保持主程序关闭。")
		return 0
	}
	if command == "check" {
		// Self-update checks do not depend on llama.cpp's installation state.
		results, err := manager.CheckLauncher(context.Background())
		if err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		if asJSON {
			if err := json.NewEncoder(out).Encode(results); err != nil {
				fmt.Fprintln(stderr, "错误:", err)
				return 1
			}
		} else {
			for _, r := range results {
				status := "已是最新"
				if r.Available {
					status = "有可用更新"
				}
				fmt.Fprintf(out, "launcher: 当前 %s，最新 %s，%s\n", r.Installed, r.Latest, status)
			}
		}
		return 0
	}
	code := performUpdate(context.Background(), manager, options, yes, terminalInput(in), in, out, stderr)
	// A standalone invocation is also successful when it hands off installation.
	if parentPID == 0 && (code == exitHandoff || code == exitCancelled) {
		return 0
	}
	return code
}

func performUpdate(ctx context.Context, manager *update.Manager, options update.LauncherOptions, yes, interactive bool, in io.Reader, out, stderr io.Writer) int {
	if transactionPending(manager.Layout.Root) {
		fmt.Fprintln(stderr, "错误: 存在未完成的更新，请先退出主程序并运行 llamaup recover。")
		return 1
	}
	if err := managedfs.EnsureDir(manager.Layout.Root, manager.Layout.RuntimeDir, 0o700); err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	plan, err := manager.PrepareLauncherPlan(ctx, options)
	if errors.Is(err, update.ErrAlreadyCurrent) {
		fmt.Fprintln(out, "启动器已是最新版本。")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	fmt.Fprintf(out, "\n启动器更新计划\n  当前版本: %s\n  目标版本: %s\n  安装目录: %s\n  下载大小: %d 字节\n", plan.CurrentVersion, plan.Release.Tag, plan.InstallDir, plan.DownloadSize)
	if !yes {
		if !interactive {
			fmt.Fprintln(stderr, "错误: 非交互输入必须显式使用 --yes")
			return 1
		}
		fmt.Fprint(out, "执行以上启动器更新计划并自动重启，是否继续 [y/N]: ")
		line, readErr := bufio.NewReader(in).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			fmt.Fprintln(stderr, "错误:", readErr)
			return 1
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(out, "操作已取消。")
			return exitCancelled
		}
	}
	tag, err := manager.ApplyLauncher(ctx, plan)
	if err != nil {
		fmt.Fprintln(stderr, "错误:", err)
		return 1
	}
	fmt.Fprintf(out, "启动器 %s 已准备完成，正在交接安装并退出旧进程。\n", tag)
	return exitHandoff
}

func terminalInput(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
