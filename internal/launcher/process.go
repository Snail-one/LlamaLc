package launcher

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type Executor interface {
	Execute(command Command, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

type OSExecutor struct{}

func (OSExecutor) Execute(command Command, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("无法启动 %s: %w", command.Path, err)
	}
	return 0, nil
}

func FormatCommand(command Command) string {
	items := append([]string{command.Path}, command.Args...)
	for i, item := range items {
		item = redactArgument(items, i, item)
		item = safeTerminalText(item)
		if strings.ContainsAny(item, " \t\"") {
			items[i] = `"` + strings.ReplaceAll(item, `"`, `\"`) + `"`
		} else {
			items[i] = item
		}
	}
	return strings.Join(items, " ")
}

func redactArgument(items []string, index int, item string) string {
	if index > 0 && items[index-1] == "--api-key" {
		return "******"
	}
	if strings.HasPrefix(item, "--api-key=") {
		return "--api-key=******"
	}
	return item
}
