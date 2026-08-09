// Package tui implements the numbered Chinese interactive menu.
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

type App struct {
	Reader                                            *bufio.Reader
	Out, Err                                          io.Writer
	Root, LauncherVersion, LlamaVersion, UpdateNotice string
	Run                                               func([]string) int
	Ready                                             func() error
}
type category struct {
	title, summary string
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
	{title: "启动", summary: "API / 多模型 Router / CLI", items: []item{{"启动单模型 API", []string{"run", "api"}, true, false, false}, {"启动 Embedding API", []string{"run", "embedding"}, true, false, false}, {"启动 Rerank API", []string{"run", "rerank"}, true, false, false}, {"启动多模型 Router", []string{"run", "router"}, false, false, false}, {"启动 CLI 聊天", []string{"run", "chat"}, true, false, false}}},
	{title: "配置", summary: "Router 配置 / API key", items: []item{{"生成 Router 配置", []string{"config", "router", "generate"}, false, false, false}, {"显示 API key", []string{"config", "key", "show"}, false, false, false}, {"重置 API key", []string{"config", "key", "reset"}, false, false, false}}},
	{title: "升级维护", summary: "更新 / 清理恢复", items: []item{{"检查全部更新", []string{"update", "check", "all"}, false, false, false}, {"安装/更新 llama.cpp", []string{"update", "llama"}, false, true, false}, {"更新启动器", []string{"update", "launcher"}, false, false, true}, {"清理与恢复", []string{"maintenance", "cleanup"}, false, false, false}}},
}

func (a *App) RunMenu() int {
	ready := false
	for {
		a.printMain()
		if !ready && a.Ready != nil {
			if err := a.Ready(); err != nil {
				fmt.Fprintln(a.Err, "警告: 无法报告菜单就绪:", err)
			}
			ready = true
		}
		choice, err := a.read("选择: ")
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
		index, ok := number(choice, len(categories))
		if !ok {
			fmt.Fprintln(a.Err, "错误: 请输入 1、2、3 或 q")
			continue
		}
		if a.submenu(categories[index]) {
			return 0
		}
	}
}
func (a *App) printMain() {
	fmt.Fprintf(a.Out, "\n============================================================\n LlamaLc\n============================================================\n运行状态\n  启动器版本: %s\n  llama.cpp:   %s\n  根目录:      %s\n", a.LauncherVersion, empty(a.LlamaVersion, "未安装"), a.Root)
	if a.UpdateNotice != "" {
		fmt.Fprintf(a.Out, "  更新状态:    %s 更新成功，已自动重新启动\n", a.UpdateNotice)
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
		choice, err := a.read("选择: ")
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
		index, ok := number(choice, len(c.items))
		if !ok {
			fmt.Fprintln(a.Err, "错误: 选项无效")
			continue
		}
		item := c.items[index]
		command := append([]string(nil), item.command...)
		if item.promptModel {
			value, e := a.read("模型文件名或路径（0/q 返回）: ")
			if e != nil {
				return errors.Is(e, io.EOF)
			}
			if value == "0" || strings.EqualFold(value, "q") {
				continue
			}
			command = append(command, "--model", value)
		}
		if item.promptBackend {
			value, e := a.read("后端 ID（首次安装必填，回车沿用当前后端）: ")
			if e != nil {
				return errors.Is(e, io.EOF)
			}
			if value != "" {
				command = append(command, "--backend", value)
			}
		}
		code := a.Run(command)
		if code == 0 {
			fmt.Fprintln(a.Out, "操作完成。")
		} else {
			fmt.Fprintf(a.Err, "操作失败（退出码 %d）。\n", code)
		}
		if item.handoff && code == 0 {
			return true
		}
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
	if len(value) != 1 || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	n := int(value[0] - '1')
	return n, n < max
}
func empty(v, f string) string {
	if strings.TrimSpace(v) == "" {
		return f
	}
	return v
}
