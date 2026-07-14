package launcher

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const installationProbeTimeout = 30 * time.Second

type InstallationProbe interface {
	Probe(command Command, timeout time.Duration) (string, error)
}

type OSInstallationProbe struct{}

func (OSInstallationProbe) Probe(command Command, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(output), fmt.Errorf("探测超时（%s）", timeout)
	}
	if err != nil {
		return string(output), fmt.Errorf("执行 --version 失败: %w", err)
	}
	return string(output), nil
}

func VerifyInstallation(root string, paths ResolvedPaths, probe InstallationProbe) (string, error) {
	if err := requireFile(paths.Server, "llama-server"); err != nil {
		return "", err
	}
	output, err := probe.Probe(Command{
		Path: paths.Server,
		Args: []string{"--version"},
		Dir:  root,
	}, installationProbeTimeout)
	if err != nil {
		return "", fmt.Errorf("llama.cpp 安装探测失败: %w%s", err, formatProbeOutput(output))
	}
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "version:") || !strings.Contains(lower, "built with") {
		return "", fmt.Errorf("llama.cpp 安装探测失败: --version 输出无法识别%s", formatProbeOutput(output))
	}
	return versionSummary(output), nil
}

func versionSummary(output string) string {
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(line), "version:") {
			return line
		}
	}
	return "version: unknown"
}

func formatProbeOutput(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	return "；输出: " + output
}
