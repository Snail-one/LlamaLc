package launcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type maintenanceMenuResult struct {
	code      int
	installed bool
	input     io.Reader
}

func RunMaintenanceMenu(manager *UpdateManager, stdin io.Reader) int {
	return runMaintenanceMenu(manager, stdin).code
}

func runMaintenanceMenu(manager *UpdateManager, stdin io.Reader) maintenanceMenuResult {
	reader := bufio.NewReader(stdin)
	for {
		fmt.Fprintf(manager.Stdout, `
============================================================
 llama.cpp 维护模式
============================================================

运行状态
  llama.cpp: 未安装或运行时无效
  根目录:    %s

可用操作
  [1] 安装 llama.cpp
  [2] 更新启动器
  [q] 退出

------------------------------------------------------------
`, manager.Root)
		fmt.Fprint(manager.Stdout, "请选择操作: ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(manager.Stderr, "错误:", err)
			return maintenanceMenuResult{code: 1, input: reader}
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "q":
			return maintenanceMenuResult{input: reader}
		case "1":
			code, commandErr := runManagementCommand(context.Background(), manager, "install", nil, reader, true)
			if commandErr != nil {
				fmt.Fprintln(manager.Stderr, "错误:", commandErr)
				if errors.Is(err, io.EOF) {
					return maintenanceMenuResult{code: code, input: reader}
				}
				continue
			}
			if code != 0 {
				return maintenanceMenuResult{code: code, input: reader}
			}
			fmt.Fprintln(manager.Stdout, "llama.cpp 安装完成，正在进入主菜单……")
			return maintenanceMenuResult{installed: true, input: reader}
		case "2":
			code, commandErr := runManagementCommand(context.Background(), manager, "update", []string{"--component", "launcher", "--yes"}, reader, false)
			if errors.Is(commandErr, errUpdaterHandoff) {
				waitForLauncherUpdateExit(reader, manager.Stdout)
				return maintenanceMenuResult{code: code, input: reader}
			}
			if commandErr != nil {
				fmt.Fprintln(manager.Stderr, "错误:", commandErr)
				if errors.Is(err, io.EOF) {
					return maintenanceMenuResult{code: code, input: reader}
				}
				continue
			}
			return maintenanceMenuResult{code: code, input: reader}
		default:
			fmt.Fprintln(manager.Stderr, "错误: 请输入 1、2 或 q。")
			if errors.Is(err, io.EOF) {
				return maintenanceMenuResult{code: 1, input: reader}
			}
		}
	}
}
