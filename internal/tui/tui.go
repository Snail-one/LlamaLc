// Package tui implements the numbered Chinese interactive menu.
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ModelOption struct {
	ID, Path string
	Size     int64
}

type CommandResult struct {
	Code      int
	Success   bool
	Cancelled bool
	Back      bool
	Handoff   bool
}

type App struct {
	Reader                                            *bufio.Reader
	Out, Err                                          io.Writer
	Root, LauncherVersion, LlamaVersion, UpdateNotice string
	Run                                               func([]string) int
	RunResult                                         func([]string) CommandResult
	Ready                                             func() error
	BackendOptions                                    func() (tag string, ids []string, current string, err error)
	ModelOptions                                      func(kind string) (directory string, models []ModelOption, err error)
	RouterPresetExists                                func() bool
	RuntimeInstalled                                  bool
	RefreshLlamaVersion                               func() string
	AfterLlamaInstall                                 func() error
	LaunchWizard                                      bool
	ClassicInteraction                                bool
	Defaults                                          LaunchDefaults
}
type category struct {
	title, summary string
	defaultFirst   bool
	items          []item
}
type item struct {
	label         string
	command       []string
	promptModel   bool
	promptBackend bool
	handoff       bool
}

var categories = []category{
	{title: "启动", summary: "API / 多模型 Router / CLI", defaultFirst: true, items: []item{{"启动单模型 API", []string{"run", "api"}, true, false, false}, {"启动 Embedding API", []string{"run", "embedding"}, true, false, false}, {"启动 Rerank API", []string{"run", "rerank"}, true, false, false}, {"启动多模型 Router", []string{"run", "router"}, false, false, false}, {"启动 CLI 聊天", []string{"run", "chat"}, true, false, false}}},
	{title: "配置", summary: "Router 配置 / API key", defaultFirst: true, items: []item{{"生成 Router 配置", []string{"router", "generate"}, false, false, false}, {"重置 API key", []string{"key", "reset"}, false, false, false}, {"显示 API key", []string{"key", "show"}, false, false, false}}},
	{title: "升级维护", summary: "更新 / 清理恢复", items: []item{{"安装/更新 llama.cpp", []string{"update", "llama"}, false, true, false}, {"更新启动器", []string{"update", "launcher"}, false, false, true}, {"清理与恢复", []string{"cleanup"}, false, false, false}, {"检查全部更新", []string{"update", "check", "all"}, false, false, false}}},
}

func (a *App) RunMenu() int {
	ready := false
	if a.ClassicInteraction && !a.RuntimeInstalled {
		installed, exit, code := a.maintenanceMenu(&ready)
		if exit {
			return code
		}
		if installed {
			a.RuntimeInstalled = true
			fmt.Fprintln(a.Out, "llama.cpp 安装完成，正在进入主菜单……")
			if a.AfterLlamaInstall != nil {
				if err := a.AfterLlamaInstall(); err != nil {
					fmt.Fprintln(a.Err, "错误: 安装完成但无法初始化运行环境:", err)
					return 1
				}
			}
			if a.LlamaVersion == "" && a.RefreshLlamaVersion != nil {
				a.LlamaVersion = a.RefreshLlamaVersion()
			}
		}
	}
	for {
		a.printMain()
		if !ready && a.Ready != nil {
			if err := a.Ready(); err != nil {
				fmt.Fprintln(a.Err, "警告: 无法报告菜单就绪:", err)
			}
			ready = true
		}
		choice, err := a.read("请选择功能目录 [1]: ")
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(a.Err, "错误:", err)
			continue
		}
		if strings.EqualFold(choice, "q") {
			return 0
		}
		if choice == "" {
			choice = "1"
		}
		index, ok := number(choice, len(categories))
		if !ok {
			fmt.Fprintln(a.Err, "错误: 请输入 1、2、3 或 q")
			continue
		}
		clearTerminal(a.Out)
		if a.submenu(categories[index]) {
			return 0
		}
		clearTerminal(a.Out)
	}
}

func (a *App) maintenanceMenu(ready *bool) (installed, exit bool, code int) {
	for {
		fmt.Fprintln(a.Out, "\n============================================================\n LlamaLc 维护模式\n============================================================")
		if a.UpdateNotice != "" {
			fmt.Fprintf(a.Out, "\n更新结果\n  启动器: %s\n  状态: 更新成功，已自动重新启动\n", safeText(a.UpdateNotice))
		}
		fmt.Fprintf(a.Out, "\n运行状态\n  llama.cpp: 未安装或运行时无效\n  根目录:    %s\n\n可用操作\n  [1] 安装 llama.cpp\n  [2] 更新启动器\n  [q] 退出\n\n------------------------------------------------------------\n", safeText(a.Root))
		if !*ready && a.Ready != nil {
			if err := a.Ready(); err != nil {
				fmt.Fprintln(a.Err, "警告: 无法报告菜单就绪:", err)
			}
			*ready = true
		}
		choice, err := a.read("请选择操作: ")
		if errors.Is(err, io.EOF) || strings.EqualFold(choice, "q") {
			return false, true, 0
		}
		if err != nil {
			fmt.Fprintln(a.Err, "错误:", err)
			continue
		}
		switch choice {
		case "1":
			command := []string{"update", "llama", "--reinstall"}
			if a.RunResult == nil {
				backend, back, err := a.selectBackend()
				if err != nil {
					fmt.Fprintln(a.Err, "错误: 无法获取可用后端:", err)
					continue
				}
				if back {
					continue
				}
				command = []string{"update", "llama", "--backend", backend, "--reinstall"}
			}
			result := a.invoke(command)
			if result.Cancelled || result.Back {
				continue
			}
			if result.Code != 0 {
				fmt.Fprintf(a.Err, "操作失败（退出码 %d）。\n", result.Code)
				continue
			}
			return true, false, 0
		case "2":
			result := a.invoke([]string{"update", "launcher"})
			if result.Cancelled || result.Back {
				continue
			}
			if result.Code != 0 {
				fmt.Fprintf(a.Err, "操作失败（退出码 %d）。\n", result.Code)
				continue
			}
			if result.Handoff || a.RunResult == nil {
				fmt.Fprint(a.Out, "\n按 Enter 退出当前程序；更新完成后将自动启动新版本...")
				_, _ = a.Reader.ReadString('\n')
				return false, true, 0
			}
		default:
			fmt.Fprintln(a.Err, "错误: 请输入 1、2 或 q。")
		}
	}
}
func (a *App) printMain() {
	fmt.Fprintf(a.Out, "\n============================================================\n LlamaLc\n============================================================\n运行状态\n  启动器版本: %s\n  llama.cpp:   %s\n  根目录:      %s\n", safeText(a.LauncherVersion), safeText(empty(a.LlamaVersion, "未安装")), safeText(a.Root))
	if a.UpdateNotice != "" {
		fmt.Fprintf(a.Out, "  更新状态:    %s 更新成功，已自动重新启动\n", safeText(a.UpdateNotice))
	}
	fmt.Fprintln(a.Out, "\n功能目录")
	for i, c := range categories {
		fmt.Fprintf(a.Out, "  [%d] %s（%s）\n", i+1, c.title, c.summary)
	}
	fmt.Fprintln(a.Out, "  [q] 退出\n\n选择目录后再选择具体操作；子菜单输入 0 或 q 返回主菜单。")
}
func (a *App) submenu(c category) (exit bool) {
	for {
		fmt.Fprintf(a.Out, "\n%s\n", c.title)
		for i, item := range c.items {
			fmt.Fprintf(a.Out, "  [%d] %s\n", i+1, item.label)
		}
		fmt.Fprintln(a.Out, "  [0/q] 返回主菜单")
		prompt := "请选择操作: "
		if c.defaultFirst {
			prompt = "请选择操作 [1]: "
		}
		choice, err := a.read(prompt)
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			fmt.Fprintln(a.Err, "错误:", err)
			continue
		}
		if choice == "0" || strings.EqualFold(choice, "q") {
			return false
		}
		if choice == "" && c.defaultFirst {
			choice = "1"
		} else if choice == "" {
			fmt.Fprintln(a.Err, "错误: 升级维护不使用默认选项，请输入明确编号。")
			continue
		}
		index, ok := number(choice, len(c.items))
		if !ok {
			fmt.Fprintln(a.Err, "错误: 选项无效")
			continue
		}
		item := c.items[index]
		clearTerminal(a.Out)
		fmt.Fprintf(a.Out, "\n%s\n------------------------------------------------------------\n", item.label)
		command := append([]string(nil), item.command...)
		if item.promptModel {
			value, back, e := a.selectModel(modelKind(item.command))
			if e != nil {
				if errors.Is(e, io.EOF) {
					return true
				}
				fmt.Fprintln(a.Err, "错误:", e)
				continue
			}
			if back {
				return false
			}
			command = append(command, "--model", value)
		}
		if item.promptBackend && a.RunResult == nil {
			value, back, e := a.selectBackend()
			if e != nil {
				fmt.Fprintln(a.Err, "错误: 无法获取可用后端:", e)
				continue
			}
			if back {
				return false
			}
			command = append(command, "--backend", value)
		}
		if len(item.command) >= 2 && item.command[0] == "run" {
			configured, e := a.configureLaunch(item.command[1], command)
			if errors.Is(e, errLaunchBack) {
				return false
			}
			if errors.Is(e, errLaunchCancelled) {
				fmt.Fprintln(a.Out, "已取消启动。")
				if a.ClassicInteraction && a.pause() {
					return true
				}
				if a.ClassicInteraction {
					return false
				}
				continue
			}
			if e != nil {
				if errors.Is(e, io.EOF) {
					return true
				}
				fmt.Fprintln(a.Err, "错误:", e)
				continue
			}
			command = configured
		}
		if a.ClassicInteraction && len(command) == 2 && command[0] == "router" && command[1] == "generate" {
			configured, e := a.configureRouterPreset(command)
			if errors.Is(e, errLaunchBack) {
				return false
			}
			if errors.Is(e, errLaunchCancelled) {
				fmt.Fprintln(a.Out, "已取消，原文件未修改。")
				if a.pause() {
					return true
				}
				return false
			}
			if e != nil {
				fmt.Fprintln(a.Err, "错误:", e)
				continue
			}
			command = configured
		}
		if a.ClassicInteraction && len(command) == 2 && command[0] == "key" {
			prompt := "将生成新的 API key，旧 key 将立即失效，是否继续"
			cancelled := "已取消，API key 未修改。"
			if command[1] == "show" {
				prompt = "API key 将以明文显示，请确认终端未共享或录屏，是否继续"
				cancelled = "已取消，未显示 API key。"
			}
			confirmed, e := a.readYesNo(prompt, false)
			if errors.Is(e, errLaunchBack) {
				return false
			}
			if command[1] == "reset" {
				command = append(command, "--yes")
			}
			if e != nil {
				return errors.Is(e, io.EOF)
			}
			if !confirmed {
				fmt.Fprintln(a.Out, cancelled)
				if a.pause() {
					return true
				}
				return false
			}
		}
		result := a.invoke(command)
		code := result.Code
		if result.Back {
			return false
		}
		if result.Success || a.RunResult == nil && code == 0 && !result.Cancelled && !result.Handoff {
			fmt.Fprintln(a.Out, "操作完成。")
		} else if code != 0 {
			fmt.Fprintf(a.Err, "操作失败（退出码 %d）。\n", code)
		}
		if item.handoff && (result.Handoff || a.RunResult == nil && code == 0) {
			fmt.Fprint(a.Out, "\n按 Enter 退出当前程序；更新完成后将自动启动新版本...")
			_, _ = a.Reader.ReadString('\n')
			return true
		}
		if code == 0 && len(command) >= 2 && command[0] == "update" && command[1] == "llama" && a.RefreshLlamaVersion != nil {
			a.LlamaVersion = a.RefreshLlamaVersion()
		}
		if a.ClassicInteraction && len(command) == 1 && command[0] == "cleanup" {
			return false
		}
		if a.ClassicInteraction && a.pause() {
			return true
		}
		if a.ClassicInteraction {
			return false
		}
	}
}

func (a *App) invoke(command []string) CommandResult {
	if a.RunResult != nil {
		return a.RunResult(command)
	}
	if a.Run == nil {
		return CommandResult{Code: 1}
	}
	code := a.Run(command)
	return CommandResult{Code: code, Success: code == 0}
}

func (a *App) pause() bool {
	value, err := a.read("\n按 Enter 返回主菜单...")
	if errors.Is(err, io.EOF) {
		return true
	}
	if strings.EqualFold(value, "q") {
		return false
	}
	return false
}

func modelKind(command []string) string {
	if len(command) < 2 {
		return ""
	}
	switch command[1] {
	case "api", "chat":
		return "generation"
	case "embedding":
		return "embedding"
	case "rerank":
		return "rerank"
	default:
		return ""
	}
}

func (a *App) selectModel(kind string) (string, bool, error) {
	if a.ModelOptions == nil {
		return "", false, errors.New("模型目录服务不可用")
	}
	directory, options, err := a.ModelOptions(kind)
	if err != nil {
		return "", false, err
	}
	title := "选择模型"
	if kind == "generation" {
		title = "选择对话/生成模型"
	}
	fmt.Fprintln(a.Out, "\n"+title)
	fmt.Fprintln(a.Out, "------------------------------------------------------------")
	fmt.Fprintf(a.Out, "目录: %s\n", safeText(directory))
	fmt.Fprintln(a.Out, "  [0/q] 返回主菜单")
	if len(options) == 0 {
		fmt.Fprintln(a.Out, "  当前目录无模型，可输入完整路径")
	}
	for i, option := range options {
		fmt.Fprintf(a.Out, "  %2d. %s  (%s)\n", i+1, safeText(option.ID), formatModelSize(option.Size))
	}
	for {
		prompt := "请输入模型完整路径（0/q 返回）: "
		if len(options) > 0 {
			prompt = "请选择模型编号、文件名或完整路径 [1]: "
		}
		value, readErr := a.read(prompt)
		if readErr != nil {
			return "", false, readErr
		}
		if value == "0" || strings.EqualFold(value, "q") {
			return "", true, nil
		}
		if value == "" && len(options) > 0 {
			return options[0].Path, false, nil
		}
		if value == "" {
			fmt.Fprintln(a.Err, "错误: 当前目录没有默认模型，请输入完整路径。")
			continue
		}
		if index, parseErr := strconv.Atoi(value); parseErr == nil {
			if index >= 1 && index <= len(options) {
				return options[index-1].Path, false, nil
			}
			fmt.Fprintf(a.Err, "错误: 模型编号必须在 1 到 %d 之间。\n", len(options))
			continue
		}
		if filepath.IsAbs(value) || strings.ContainsAny(value, `/\\`) {
			return value, false, nil
		}
		matches := make([]ModelOption, 0, 1)
		for _, option := range options {
			if strings.EqualFold(value, option.ID) {
				matches = append(matches, option)
			}
		}
		if len(matches) == 1 {
			return matches[0].Path, false, nil
		}
		if len(matches) > 1 {
			fmt.Fprintf(a.Err, "错误: 模型名 %q 不唯一，请输入编号或完整路径。\n", value)
			continue
		}
		fmt.Fprintf(a.Err, "错误: 列表中没有模型 %q，请输入编号、文件名或完整路径。\n", value)
	}
}

func (a *App) selectBackend() (string, bool, error) {
	if a.BackendOptions == nil {
		return "", false, errors.New("后端目录服务不可用")
	}
	tag, ids, current, err := a.BackendOptions()
	if err != nil {
		return "", false, err
	}
	if len(ids) == 0 {
		return "", false, errors.New("当前平台没有可用后端")
	}
	fmt.Fprintf(a.Out, "\nllama.cpp Release: %s\n可用后端:\n", safeText(tag))
	currentAvailable := false
	for i, id := range ids {
		marker := ""
		if current != "" && strings.EqualFold(id, current) {
			marker = "（当前）"
			currentAvailable = true
		}
		fmt.Fprintf(a.Out, "  [%d] %s%s\n", i+1, safeText(id), marker)
	}
	if current != "" && !currentAvailable {
		fmt.Fprintf(a.Out, "  当前后端 %s 已不在此 Release 的可用列表中，必须重新选择。\n", safeText(current))
	}
	for {
		prompt := "请选择后端编号或完整 ID（0/q 返回）: "
		if currentAvailable {
			prompt = fmt.Sprintf("请选择后端编号或完整 ID（回车沿用 %s，0/q 返回）: ", current)
		}
		value, readErr := a.read(prompt)
		if readErr != nil {
			return "", false, readErr
		}
		if value == "0" || strings.EqualFold(value, "q") {
			return "", true, nil
		}
		if value == "" {
			if currentAvailable {
				for _, id := range ids {
					if strings.EqualFold(id, current) {
						return id, false, nil
					}
				}
			}
			fmt.Fprintln(a.Err, "错误: 首次安装必须选择一个后端。")
			continue
		}
		if index, parseErr := strconv.Atoi(value); parseErr == nil {
			if index >= 1 && index <= len(ids) {
				return ids[index-1], false, nil
			}
			fmt.Fprintf(a.Err, "错误: 后端编号必须在 1 到 %d 之间。\n", len(ids))
			continue
		}
		for _, id := range ids {
			if strings.EqualFold(value, id) {
				return id, false, nil
			}
		}
		fmt.Fprintf(a.Err, "错误: 后端 %q 不在当前可用列表中。\n", value)
	}
}
func (a *App) read(prompt string) (string, error) {
	fmt.Fprint(a.Out, prompt)
	line, err := a.Reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), err
}
func number(value string, max int) (int, bool) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > max {
		return 0, false
	}
	return parsed - 1, true
}

func clearTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	_, err = fmt.Fprint(file, "\x1b[2J\x1b[H")
	return err == nil
}
func empty(v, f string) string {
	if strings.TrimSpace(v) == "" {
		return f
	}
	return v
}

func formatModelSize(size int64) string {
	const (
		mib = 1024 * 1024
		gib = 1024 * mib
	)
	if size >= gib {
		return fmt.Sprintf("%.2f GB", float64(size)/gib)
	}
	if size >= mib {
		return fmt.Sprintf("%.2f MB", float64(size)/mib)
	}
	return fmt.Sprintf("%d B", size)
}

func safeText(value string) string {
	var result strings.Builder
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			result.WriteRune('�')
		} else {
			result.WriteRune(character)
		}
	}
	return result.String()
}
