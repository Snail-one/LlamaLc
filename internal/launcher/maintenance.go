package launcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func RunMaintenanceMenu(manager *UpdateManager, stdin io.Reader) int {
	reader := bufio.NewReader(stdin)
	for {
		fmt.Fprintf(manager.Stdout, `
llama.cpp 维护模式
根目录: %s
未找到有效的受管 llama.cpp 运行时。

  1. 安装 llama.cpp
  2. 更新启动器
  q. 退出
`, manager.Root)
		fmt.Fprint(manager.Stdout, "请选择: ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(manager.Stderr, "错误:", err)
			return 1
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "q":
			return 0
		case "1":
			code, commandErr := runManagementCommand(context.Background(), manager, "install", nil, reader, true)
			if commandErr != nil {
				fmt.Fprintln(manager.Stderr, "错误:", commandErr)
				if errors.Is(err, io.EOF) {
					return code
				}
				continue
			}
			return code
		case "2":
			code, commandErr := runManagementCommand(context.Background(), manager, "update", []string{"--component", "launcher"}, reader, true)
			if commandErr != nil {
				fmt.Fprintln(manager.Stderr, "错误:", commandErr)
				if errors.Is(err, io.EOF) {
					return code
				}
				continue
			}
			return code
		default:
			fmt.Fprintln(manager.Stderr, "错误: 请输入 1、2 或 q")
			if errors.Is(err, io.EOF) {
				return 1
			}
		}
	}
}
