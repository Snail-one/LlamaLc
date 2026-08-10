// Package llama locates llama.cpp, builds commands, validates exposure and executes processes.
package llama

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Mode string

const (
	API       Mode = "api"
	Embedding Mode = "embedding"
	Rerank    Mode = "rerank"
	Router    Mode = "router"
	Chat      Mode = "chat"
)

type Runtime struct{ Directory, Server, CLI, Version string }
type Options struct {
	Model, MMProj, Preset, Host, GPULayers, FlashAttention, Pooling, APIKeyFile       string
	Port, ContextSize, Threads, BatchSize, UBatchSize, Parallel, Normalize, ModelsMax int
	ImageMinTokens, ImageMaxTokens                                                    int
	NormalizeSet, UI, Autoload                                                        bool
	Extra                                                                             []string
}
type Command struct {
	Path string
	Args []string
	Dir  string
}

func Locate(directory, goos string) (Runtime, error) {
	serverName, cliName := "llama-server", "llama-cli"
	if goos == "windows" {
		serverName = "llama-server.exe"
		cliName = "llama-cli.exe"
	}
	if goos != "linux" && goos != "windows" {
		return Runtime{}, fmt.Errorf("不支持的平台 %s", goos)
	}
	server, err := uniqueFile(directory, serverName)
	if err != nil {
		return Runtime{}, err
	}
	cli, err := uniqueFile(directory, cliName)
	if err != nil {
		return Runtime{}, err
	}
	return Runtime{Directory: directory, Server: server, CLI: cli}, nil
}
func uniqueFile(root, name string) (string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// llama.cpp Linux packages ship relative .so soname symlinks. Allow them
		// when they are not the server/CLI we are locating; never follow
		// directory symlinks out of the runtime tree.
		if e.Type()&os.ModeSymlink != 0 {
			if e.IsDir() {
				return filepath.SkipDir
			}
			if strings.EqualFold(e.Name(), name) {
				return fmt.Errorf("%s 不能是符号链接: %s", name, path)
			}
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !e.IsDir() && strings.EqualFold(e.Name(), name) {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s 不是普通文件", path)
			}
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("%s 中必须且只能有一个 %s，实际 %d 个", root, name, len(found))
	}
	return found[0], nil
}

func ProbeVersion(ctx context.Context, executable string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--version")
	// Run next to the binary so $ORIGIN / adjacent shared libraries resolve the
	// same way a normal launch from the runtime directory would.
	cmd.Dir = filepath.Dir(executable)
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("探测 llama.cpp 版本超时")
		}
		detail := strings.TrimSpace(out.String())
		if detail != "" {
			return "", fmt.Errorf("探测 llama.cpp 版本: %w (%s)", err, compressProbeDetail(detail))
		}
		return "", fmt.Errorf("探测 llama.cpp 版本: %w", err)
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		return "", errors.New("llama.cpp --version 没有输出")
	}
	return versionSummary(line)
}

func compressProbeDetail(detail string) string {
	detail = strings.ReplaceAll(detail, "\r\n", "\n")
	detail = strings.Join(strings.Fields(strings.ReplaceAll(detail, "\n", " ")), " ")
	runes := []rune(detail)
	if len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return detail
}

func versionSummary(output string) (string, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(output), "\r\n", "\n")
	lower := strings.ToLower(normalized)
	officialSignature := strings.Contains(lower, "version:") && strings.Contains(lower, "built with")
	llamaSignature := strings.Contains(lower, "llama")
	if !officialSignature && !llamaSignature {
		return "", errors.New("llama.cpp --version 输出缺少可识别签名")
	}
	if officialSignature {
		for _, line := range strings.Split(normalized, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(strings.ToLower(line), "version:") {
				return line, nil
			}
		}
	}
	for _, line := range strings.Split(normalized, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line, nil
		}
	}
	return "", errors.New("llama.cpp --version 没有输出")
}

type cappedBuffer struct {
	bytes.Buffer
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	const limit = 1 << 20
	if buffer.Len()+len(data) > limit {
		remaining := limit - buffer.Len()
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(data[:remaining])
		}
		return len(data), errors.New("版本探测输出超过 1 MiB")
	}
	return buffer.Buffer.Write(data)
}

func Build(mode Mode, runtime Runtime, root string, o Options) (Command, error) {
	o.GPULayers = strings.ToLower(strings.TrimSpace(o.GPULayers))
	o.FlashAttention = strings.ToLower(strings.TrimSpace(o.FlashAttention))
	o.Pooling = strings.ToLower(strings.TrimSpace(o.Pooling))
	if mode == Chat && runtime.CLI == "" {
		return Command{}, errors.New("当前运行时没有 llama-cli")
	}
	path := runtime.Server
	if mode == Chat {
		path = runtime.CLI
	}
	if mode != Router && strings.TrimSpace(o.Model) == "" {
		return Command{}, errors.New("模型路径不能为空")
	}
	if mode == Router && strings.TrimSpace(o.Preset) == "" {
		return Command{}, errors.New("Router preset 路径不能为空")
	}
	if mode != Chat {
		if strings.TrimSpace(o.Host) == "" || o.Port < 1 || o.Port > 65535 {
			return Command{}, errors.New("监听地址或端口无效")
		}
	}
	if strings.TrimSpace(o.GPULayers) == "" {
		return Command{}, errors.New("gpu-layers 不能为空")
	}
	if o.GPULayers != "auto" && o.GPULayers != "all" {
		value, err := strconv.Atoi(o.GPULayers)
		if err != nil || value < -1 {
			return Command{}, errors.New("gpu-layers 必须为 auto、all 或不小于 -1 的整数")
		}
	}
	if o.FlashAttention != "auto" && o.FlashAttention != "on" && o.FlashAttention != "off" {
		return Command{}, errors.New("flash-attention 必须为 auto、on 或 off")
	}
	if o.ContextSize < 0 || o.BatchSize <= 0 || o.UBatchSize <= 0 || o.UBatchSize > o.BatchSize {
		return Command{}, errors.New("运行参数 batch/context 无效")
	}
	if o.ImageMinTokens < 0 || o.ImageMaxTokens < 0 {
		return Command{}, errors.New("image token 参数不能小于 0")
	}
	if o.Threads < -1 || o.Threads == 0 {
		return Command{}, errors.New("threads 必须为 -1 或正整数")
	}
	if mode != Chat && (o.Parallel < -1 || o.Parallel == 0) {
		return Command{}, errors.New("parallel 必须为 -1 或正整数")
	}
	if mode == Router && o.ModelsMax < 0 {
		return Command{}, errors.New("models-max 不能小于 0")
	}
	if mode == Embedding {
		switch o.Pooling {
		case "", "none", "mean", "cls", "last", "rank":
		default:
			return Command{}, errors.New("pooling 无效")
		}
		if o.NormalizeSet && o.Normalize < -1 {
			return Command{}, errors.New("normalize 不能小于 -1")
		}
	}
	var args []string
	if mode == Router {
		args = append(args, "--models-preset", o.Preset, "--models-max", strconv.Itoa(o.ModelsMax))
		if o.Autoload {
			args = append(args, "--models-autoload")
		} else {
			args = append(args, "--no-models-autoload")
		}
	} else {
		args = append(args, "--model", o.Model)
	}
	if o.ContextSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(o.ContextSize))
	}
	args = append(args, "--n-gpu-layers", o.GPULayers, "--threads", strconv.Itoa(o.Threads), "--batch-size", strconv.Itoa(o.BatchSize), "--ubatch-size", strconv.Itoa(o.UBatchSize), "--flash-attn", o.FlashAttention)
	if mode == Chat {
		args = append(args, "-cnv")
		args = append(args, o.Extra...)
		return Command{Path: path, Args: args, Dir: root}, nil
	}
	if o.Parallel != 0 {
		args = append(args, "--parallel", strconv.Itoa(o.Parallel))
	}
	if o.MMProj != "" {
		args = append(args, "--mmproj", o.MMProj)
		if o.ImageMinTokens > 0 {
			args = append(args, "--image-min-tokens", strconv.Itoa(o.ImageMinTokens))
		}
		if o.ImageMaxTokens > 0 {
			args = append(args, "--image-max-tokens", strconv.Itoa(o.ImageMaxTokens))
		}
	}
	if mode == Embedding {
		args = append(args, "--embedding")
		if o.Pooling != "" {
			args = append(args, "--pooling", o.Pooling)
		}
		if o.NormalizeSet {
			args = append(args, "--embd-normalize", strconv.Itoa(o.Normalize))
		}
	}
	if mode == Rerank {
		args = append(args, "--reranking")
	}
	args = append(args, "--host", o.Host, "--port", strconv.Itoa(o.Port))
	if o.UI {
		args = append(args, "--ui")
	} else {
		args = append(args, "--no-ui")
	}
	if o.APIKeyFile != "" {
		args = append(args, "--api-key-file", o.APIKeyFile)
	}
	args = append(args, o.Extra...)
	return Command{Path: path, Args: args, Dir: root}, nil
}

func ValidateExposure(host, keyFile string, extra []string) error {
	effective := EffectiveHost(host, extra)
	effectiveKey := keyFile
	for i := 0; i < len(extra); i++ {
		if extra[i] == "--api-key-file" && i+1 < len(extra) {
			effectiveKey = extra[i+1]
			i++
		} else if strings.HasPrefix(extra[i], "--api-key-file=") {
			effectiveKey = strings.TrimPrefix(extra[i], "--api-key-file=")
		}
	}
	if IsLocalHost(effective) {
		return nil
	}
	if effectiveKey != keyFile {
		return fmt.Errorf("拒绝在非本机地址 %q 上无托管认证启动", effective)
	}
	return nil
}

func EffectiveHost(initial string, extra []string) string {
	effective := initial
	for i := 0; i < len(extra); i++ {
		if extra[i] == "--host" && i+1 < len(extra) {
			effective = extra[i+1]
			i++
		} else if strings.HasPrefix(extra[i], "--host=") {
			effective = strings.TrimPrefix(extra[i], "--host=")
		}
	}
	return strings.TrimSpace(effective)
}

func EffectivePort(initial int, extra []string) int {
	effective := initial
	for index := 0; index < len(extra); index++ {
		value := ""
		if extra[index] == "--port" && index+1 < len(extra) {
			value = extra[index+1]
			index++
		} else if strings.HasPrefix(extra[index], "--port=") {
			value = strings.TrimPrefix(extra[index], "--port=")
		}
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 1 && parsed <= 65535 {
			effective = parsed
		}
	}
	return effective
}

func IsLocalHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	return strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".sock") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

type Executor interface {
	Execute(Command, io.Reader, io.Writer, io.Writer) (int, error)
}
type OSExecutor struct{}

func (OSExecutor) Execute(c Command, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 1, err
}
func Format(c Command) string {
	items := append([]string{c.Path}, c.Args...)
	for i, v := range items {
		if i > 0 && items[i-1] == "--api-key" {
			v = "******"
		} else if strings.HasPrefix(v, "--api-key=") {
			v = "--api-key=******"
		}
		v = safeTerminalArgument(v)
		if strings.ContainsAny(v, " \t\"") {
			v = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
		}
		items[i] = v
	}
	return strings.Join(items, " ")
}

func safeTerminalArgument(value string) string {
	var output strings.Builder
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			fmt.Fprintf(&output, "\\u%04X", character)
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}
