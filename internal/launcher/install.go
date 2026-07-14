package launcher

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	installationProbeTimeout = 30 * time.Second
	maxProbeOutputSize       = 1 << 20
)

type cappedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (output *cappedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = output.buffer.Write(data[:remaining])
	}
	if len(data) > remaining {
		output.exceeded = true
	}
	return len(data), nil
}

func (output *cappedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func (output *cappedOutput) Exceeded() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.exceeded
}

type InstallationProbe interface {
	Probe(command Command, timeout time.Duration) (string, error)
}

type OSInstallationProbe struct{}

func (OSInstallationProbe) Probe(command Command, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir
	output := &cappedOutput{limit: maxProbeOutputSize}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return output.String(), fmt.Errorf("探测超时（%s）", timeout)
	}
	if err != nil {
		return output.String(), fmt.Errorf("执行 --version 失败: %w", err)
	}
	if output.Exceeded() {
		return output.String(), fmt.Errorf("--version 输出超过 %d 字节限制", maxProbeOutputSize)
	}
	return output.String(), nil
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
			return safeTerminalText(line)
		}
	}
	return "version: unknown"
}

func formatProbeOutput(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	return "；输出: " + safeTerminalText(output)
}
