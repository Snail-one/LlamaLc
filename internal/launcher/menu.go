package launcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

var errMenuBack = errors.New("返回主菜单")

type menu struct {
	app    *Application
	reader *bufio.Reader
}

func (app *Application) RunMenu() int {
	m := &menu{app: app, reader: bufio.NewReader(app.Stdin)}
	// Pass the same reader to children so any bytes already buffered by the menu
	// remain visible to llama-cli/llama-server.
	app.Stdin = m.reader
	clearBeforeMenu := false
	for {
		if clearBeforeMenu {
			clearTerminal(app.Stdout)
		}
		clearBeforeMenu = false
		fmt.Fprintf(app.Stdout, `
llama.cpp Go 启动器
启动器版本: %s
llama.cpp: %s
根目录: %s
提示: 操作中输入 q 返回主菜单（主菜单输入 q 退出）

  1. 单模型 API 服务
  2. Embedding API
  3. Rerank API
  4. 生成手动 Router 配置
  5. 多模型 Router
  6. CLI 命令行聊天
  7. 检查并更新启动器与 llama.cpp
  q. 退出
`, buildversion.Version, app.llamaVersionDisplay(), app.Root)
		choice, err := m.readChoice("请选择", 1, 1, 7)
		if errors.Is(err, io.EOF) {
			return 0
		}
		if errors.Is(err, errMenuBack) {
			return 0
		}
		if errors.Is(err, errUpdaterHandoff) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(app.Stderr, "错误:", err)
			continue
		}
		clearTerminal(app.Stdout)
		if err := m.runChoice(choice); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			if errors.Is(err, errUpdaterHandoff) {
				return 0
			}
			if errors.Is(err, errMenuBack) {
				clearBeforeMenu = true
				continue
			}
			fmt.Fprintln(app.Stderr, "错误:", err)
			continue
		}
		clearBeforeMenu = true
	}
}

func (app *Application) llamaVersionDisplay() string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(app.LlamaTag) != "" {
		parts = append(parts, app.LlamaTag)
	}
	if strings.TrimSpace(app.LlamaBackend) != "" {
		parts = append(parts, app.LlamaBackend)
	}
	managed := strings.Join(parts, " / ")
	detected := strings.TrimSpace(app.LlamaVersion)
	if managed != "" && detected != "" {
		return managed + " — " + detected
	}
	if managed != "" {
		return managed
	}
	if detected != "" {
		return detected
	}
	return "unknown"
}

func (m *menu) runChoice(choice int) error {
	switch choice {
	case 1:
		model, err := m.selectModel(m.app.Paths.Models, GenerationModel, generationExtensions)
		if err != nil {
			return err
		}
		args := []string{"--model", model.Path}
		projectors, err := ScanModels(m.app.Paths.Mmproj, GenerationModel, ggufExtension)
		if err != nil {
			return err
		}
		if len(projectors) > 0 {
			projector, err := m.selectProjector(model, projectors)
			if err != nil {
				return err
			}
			if projector != nil {
				args = append(args, "--mmproj", projector.Path)
			}
		}
		if !hasFlag(args, "--mmproj") {
			customProjector, err := m.readLine("其他 mmproj 路径（留空跳过）: ")
			if err != nil {
				return err
			}
			if customProjector != "" {
				projector, err := ResolveModelAt(m.app.Paths.Mmproj, m.app.Root, customProjector, GenerationModel, ggufExtension)
				if err != nil {
					return fmt.Errorf("mmproj 无效: %w", err)
				}
				args = append(args, "--mmproj", projector.Path)
			}
		}
		if hasFlag(args, "--mmproj") {
			imageMin, err := m.readNonNegativeInt("图片最小 token --image-min-tokens（0 使用模型默认）", 0)
			if err != nil {
				return err
			}
			imageMax, err := m.readNonNegativeInt("图片最大 token --image-max-tokens（0 使用模型默认）", 0)
			if err != nil {
				return err
			}
			args = append(args, "--image-min-tokens", strconv.Itoa(imageMin), "--image-max-tokens", strconv.Itoa(imageMax))
		}
		runtimeArgs, err := m.readRuntimeArguments(m.app.Config.Server.BatchSize, m.app.Config.Server.UBatchSize, true)
		if err != nil {
			return err
		}
		args = append(args, runtimeArgs...)
		networkArgs, err := m.readNetworkArguments()
		if err != nil {
			return err
		}
		args = append(args, networkArgs...)
		return m.confirmAndRun("serve", args)
	case 2:
		model, err := m.selectModel(m.app.Paths.Embeddings, EmbeddingModel, ggufExtension)
		if err != nil {
			return err
		}
		args := []string{"--model", model.Path}
		runtimeArgs, err := m.readRuntimeArguments(m.app.Config.Embedding.BatchSize, m.app.Config.Embedding.UBatchSize, true)
		if err != nil {
			return err
		}
		args = append(args, runtimeArgs...)
		pooling, err := m.readPooling(m.app.Config.Embedding.Pooling)
		if err != nil {
			return err
		}
		normalize, err := m.readInteger("向量归一化 --embd-normalize（官方默认 2，-1 禁用）", m.app.Config.Embedding.Normalize, func(value int) error {
			if value < -1 {
				return errors.New("必须不小于 -1")
			}
			return nil
		})
		if err != nil {
			return err
		}
		args = append(args, "--pooling", pooling, "--embd-normalize", strconv.Itoa(normalize))
		networkArgs, err := m.readNetworkArguments()
		if err != nil {
			return err
		}
		return m.confirmAndRun("embedding", append(args, networkArgs...))
	case 3:
		model, err := m.selectModel(m.app.Paths.Rerank, RerankModel, ggufExtension)
		if err != nil {
			return err
		}
		args := []string{"--model", model.Path}
		runtimeArgs, err := m.readRuntimeArguments(m.app.Config.Server.BatchSize, m.app.Config.Server.UBatchSize, true)
		if err != nil {
			return err
		}
		networkArgs, err := m.readNetworkArguments()
		if err != nil {
			return err
		}
		args = append(args, runtimeArgs...)
		args = append(args, networkArgs...)
		return m.confirmAndRun("rerank", args)
	case 4:
		args := []string{}
		if _, err := os.Stat(m.app.Paths.RouterManual); err == nil {
			overwrite, err := m.readYesNo("手动配置已存在，是否覆盖", false)
			if err != nil {
				return err
			}
			if !overwrite {
				fmt.Fprintln(m.app.Stdout, "已取消，原文件未修改。")
				return m.pause()
			}
			args = append(args, "--force")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		gpuLayers, err := m.readValidatedString("GPU 层数 --n-gpu-layers", m.app.Config.Server.GPULayers, ValidateGPULayers)
		if err != nil {
			return err
		}
		ctx, err := m.readNonNegativeInt("上下文长度 --ctx-size（0 使用模型元数据）", m.app.Config.Server.ContextSize)
		if err != nil {
			return err
		}
		pooling, err := m.readPooling(m.app.Config.Embedding.Pooling)
		if err != nil {
			return err
		}
		embeddingBatch, embeddingUBatch, err := m.readBatchPair("Embedding ", m.app.Config.Embedding.BatchSize, m.app.Config.Embedding.UBatchSize)
		if err != nil {
			return err
		}
		autoMatch := true
		projectors, err := ScanModels(m.app.Paths.Mmproj, GenerationModel, ggufExtension)
		if err != nil {
			return err
		}
		if len(projectors) > 0 {
			autoMatch, err = m.readYesNo("是否按文件名前缀自动匹配 mmproj", true)
			if err != nil {
				return err
			}
		}
		args = append(args,
			"--n-gpu-layers", gpuLayers,
			"--ctx-size", strconv.Itoa(ctx),
			"--pooling", pooling,
			"--batch-size", strconv.Itoa(embeddingBatch),
			"--ubatch-size", strconv.Itoa(embeddingUBatch),
			"--mmproj-auto="+strconv.FormatBool(autoMatch),
		)
		_, err = m.app.RunCommand("router-config", args)
		if err != nil {
			return err
		}
		return m.pause()
	case 5:
		args, err := m.readRuntimeArguments(m.app.Config.Server.BatchSize, m.app.Config.Server.UBatchSize, true)
		if err != nil {
			return err
		}
		modelsMax, err := m.readNonNegativeInt("最多同时加载模型数 --models-max（0 不限制）", m.app.Config.Router.ModelsMax)
		if err != nil {
			return err
		}
		autoload, err := m.readYesNo("是否按请求自动加载模型 --autoload", m.app.Config.Router.Autoload)
		if err != nil {
			return err
		}
		pooling, err := m.readPooling(m.app.Config.Embedding.Pooling)
		if err != nil {
			return err
		}
		embeddingBatch, embeddingUBatch, err := m.readBatchPair("Embedding ", m.app.Config.Embedding.BatchSize, m.app.Config.Embedding.UBatchSize)
		if err != nil {
			return err
		}
		networkArgs, err := m.readNetworkArguments()
		if err != nil {
			return err
		}
		args = append(args,
			"--models-max", strconv.Itoa(modelsMax),
			"--autoload="+strconv.FormatBool(autoload),
			"--pooling", pooling,
			"--embedding-batch-size", strconv.Itoa(embeddingBatch),
			"--embedding-ubatch-size", strconv.Itoa(embeddingUBatch),
		)
		args = append(args, networkArgs...)
		return m.confirmAndRun("router", args)
	case 6:
		model, err := m.selectModel(m.app.Paths.Models, GenerationModel, generationExtensions)
		if err != nil {
			return err
		}
		args := []string{"--model", model.Path}
		runtimeArgs, err := m.readRuntimeArguments(m.app.Config.Server.BatchSize, m.app.Config.Server.UBatchSize, false)
		if err != nil {
			return err
		}
		return m.confirmAndRun("chat", append(args, runtimeArgs...))
	case 7:
		if m.app.Updater == nil {
			return errors.New("更新器未初始化")
		}
		confirmed, err := m.readYesNo("将联网检查并更新启动器与 llama.cpp，是否继续", false)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(m.app.Stdout, "已取消。")
			return m.pause()
		}
		_, err = runManagementCommand(context.Background(), m.app.Updater, "update", []string{"--component", "all", "--yes"}, m.reader, true)
		if err != nil {
			return err
		}
		return m.pause()
	}
	return nil
}

func (m *menu) selectModel(directory string, kind ModelKind, extensions map[string]bool) (ModelFile, error) {
	models, err := ScanModels(directory, kind, extensions)
	if err != nil {
		return ModelFile{}, err
	}
	if len(models) == 0 {
		return ModelFile{}, fmt.Errorf("目录中没有找到支持的模型: %s", directory)
	}
	fmt.Fprintln(m.app.Stdout, "\n发现模型:")
	fmt.Fprintln(m.app.Stdout, "   q. 返回主菜单")
	for i, model := range models {
		fmt.Fprintf(m.app.Stdout, "  %2d. %s  (%s)\n", i+1, safeTerminalText(model.ID), formatSize(model.Size))
	}
	choice, err := m.readChoice("请选择模型", 1, 1, len(models))
	if err != nil {
		return ModelFile{}, err
	}
	return models[choice-1], nil
}

func (m *menu) selectProjector(model ModelFile, projectors []ModelFile) (*ModelFile, error) {
	recommended := FindMatchingMmproj(model, projectors)
	defaultChoice := 0
	fmt.Fprintln(m.app.Stdout, "\n可用 mmproj:")
	fmt.Fprintln(m.app.Stdout, "   q. 返回主菜单")
	fmt.Fprintln(m.app.Stdout, "   0. 不使用 mmproj")
	for i, projector := range projectors {
		label := ""
		if recommended != nil && projector.Path == recommended.Path {
			defaultChoice = i + 1
			label = "  [自动匹配]"
		}
		fmt.Fprintf(m.app.Stdout, "  %2d. %s%s\n", i+1, safeTerminalText(projector.ID), label)
	}
	choice, err := m.readChoice("请选择 mmproj", defaultChoice, 0, len(projectors))
	if err != nil {
		return nil, err
	}
	if choice == 0 {
		return nil, nil
	}
	return &projectors[choice-1], nil
}

func (m *menu) confirmAndRun(command string, args []string) error {
	extra, err := m.readCustomArguments()
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		args = append(args, "--")
		args = append(args, extra...)
	}
	originalExecutor := m.app.Executor
	confirmation := &menuConfirmExecutor{menu: m, next: originalExecutor}
	m.app.Executor = confirmation
	defer func() { m.app.Executor = originalExecutor }()
	code, err := m.app.RunCommand(command, args)
	if err != nil {
		return err
	}
	if confirmation.cancelled {
		fmt.Fprintln(m.app.Stdout, "已取消启动。")
		return m.pause()
	}
	if code != 0 {
		fmt.Fprintf(m.app.Stderr, "进程退出码: %d\n", code)
	} else {
		fmt.Fprintln(m.app.Stdout, "进程已结束。")
	}
	return m.pause()
}

type menuConfirmExecutor struct {
	menu      *menu
	next      Executor
	cancelled bool
}

func (executor *menuConfirmExecutor) Execute(command Command, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	ok, err := executor.menu.readYesNo("确认使用以上参数启动", true)
	if err != nil {
		return 1, err
	}
	if !ok {
		executor.cancelled = true
		return 0, nil
	}
	return executor.next.Execute(command, stdin, stdout, stderr)
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}

func (m *menu) readRuntimeArguments(batchDefault, ubatchDefault int, includeParallel bool) ([]string, error) {
	ctx, err := m.readNonNegativeInt("上下文长度 --ctx-size（0 使用模型元数据）", m.app.Config.Server.ContextSize)
	if err != nil {
		return nil, err
	}
	gpuLayers, err := m.readValidatedString("GPU 层数 --n-gpu-layers", m.app.Config.Server.GPULayers, ValidateGPULayers)
	if err != nil {
		return nil, err
	}
	threads, err := m.readInteger("CPU 生成线程 --threads（-1 自动）", m.app.Config.Server.Threads, ValidateThreads)
	if err != nil {
		return nil, err
	}
	batchSize, ubatchSize, err := m.readBatchPair("", batchDefault, ubatchDefault)
	if err != nil {
		return nil, err
	}
	flashAttention, err := m.readValidatedString("Flash Attention --flash-attn（auto/on/off）", m.app.Config.Server.FlashAttention, ValidateFlashAttention)
	if err != nil {
		return nil, err
	}
	args := []string{
		"--ctx-size", strconv.Itoa(ctx),
		"--n-gpu-layers", gpuLayers,
		"--threads", strconv.Itoa(threads),
		"--batch-size", strconv.Itoa(batchSize),
		"--ubatch-size", strconv.Itoa(ubatchSize),
		"--flash-attn", strings.ToLower(flashAttention),
	}
	if includeParallel {
		parallel, err := m.readInteger("服务并发槽位 --parallel（-1 自动）", m.app.Config.Server.Parallel, ValidateParallel)
		if err != nil {
			return nil, err
		}
		args = append(args, "--parallel", strconv.Itoa(parallel))
	}
	return args, nil
}

func (m *menu) readBatchPair(prefix string, batchDefault, ubatchDefault int) (int, int, error) {
	for {
		batchSize, err := m.readPositiveInt(prefix+"逻辑批次 --batch-size", batchDefault)
		if err != nil {
			return 0, 0, err
		}
		ubatchSize, err := m.readPositiveInt(prefix+"物理批次 --ubatch-size", ubatchDefault)
		if err != nil {
			return 0, 0, err
		}
		if err := ValidateBatchPair(batchSize, ubatchSize); err == nil {
			return batchSize, ubatchSize, nil
		} else {
			fmt.Fprintln(m.app.Stderr, "错误:", err)
		}
	}
}

func (m *menu) readNetworkArguments() ([]string, error) {
	host, err := m.readValidatedString("监听地址 --host", m.app.Config.Server.Host, func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("监听地址不能为空")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	port, err := m.readInteger("监听端口 --port", m.app.Config.Server.Port, ValidatePort)
	if err != nil {
		return nil, err
	}
	ui, err := m.readYesNo("是否启用 Web UI --ui", m.app.Config.Server.UI)
	if err != nil {
		return nil, err
	}
	return []string{"--host", host, "--port", strconv.Itoa(port), "--ui=" + strconv.FormatBool(ui)}, nil
}

func (m *menu) readValidatedString(prompt, defaultValue string, validate func(string) error) (string, error) {
	for {
		line, err := m.readLine(fmt.Sprintf("%s [%s]: ", prompt, defaultValue))
		if err != nil {
			return "", err
		}
		if line == "" {
			line = defaultValue
		}
		line = strings.TrimSpace(line)
		if err := validate(line); err == nil {
			return line, nil
		} else {
			fmt.Fprintln(m.app.Stderr, "错误:", err)
		}
	}
}

func (m *menu) readInteger(prompt string, defaultValue int, validate func(int) error) (int, error) {
	for {
		line, err := m.readLine(fmt.Sprintf("%s [%d]: ", prompt, defaultValue))
		if err != nil {
			return 0, err
		}
		if line == "" {
			line = strconv.Itoa(defaultValue)
		}
		value, parseErr := strconv.Atoi(line)
		if parseErr == nil {
			if err := validate(value); err == nil {
				return value, nil
			} else {
				fmt.Fprintln(m.app.Stderr, "错误:", err)
				continue
			}
		}
		fmt.Fprintln(m.app.Stderr, "请输入有效整数。")
	}
}

func (m *menu) readNonNegativeInt(prompt string, defaultValue int) (int, error) {
	return m.readInteger(prompt, defaultValue, func(value int) error {
		if value < 0 {
			return errors.New("必须是非负整数")
		}
		return nil
	})
}

func (m *menu) readPooling(defaultValue string) (string, error) {
	for {
		line, err := m.readLine(fmt.Sprintf("Pooling --pooling [%s]: ", defaultValue))
		if err != nil {
			return "", err
		}
		if line == "" {
			line = defaultValue
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if err := ValidatePooling(line); err == nil {
			return line, nil
		} else {
			fmt.Fprintln(m.app.Stderr, "错误:", err)
		}
	}
}

func (m *menu) readPositiveInt(prompt string, defaultValue int) (int, error) {
	return m.readInteger(prompt, defaultValue, func(value int) error {
		return ValidatePositive("该参数", value)
	})
}

func (m *menu) pause() error {
	_, err := m.readLine("\n按 Enter 返回主菜单...")
	return err
}

func (m *menu) readCustomArguments() ([]string, error) {
	for {
		line, err := m.readLine("自定义 llama.cpp 参数（留空跳过）: ")
		if err != nil {
			return nil, err
		}
		args, err := SplitCustomArguments(line)
		if err == nil {
			return args, nil
		}
		fmt.Fprintln(m.app.Stderr, "错误:", err)
	}
}

func (m *menu) readChoice(prompt string, defaultValue, min, max int) (int, error) {
	for {
		line, err := m.readLine(fmt.Sprintf("%s [%d]: ", prompt, defaultValue))
		if err != nil {
			return 0, err
		}
		if line == "" {
			return defaultValue, nil
		}
		value, err := strconv.Atoi(line)
		if err == nil && value >= min && value <= max {
			return value, nil
		}
		fmt.Fprintf(m.app.Stderr, "请输入 %d 到 %d 之间的数字。\n", min, max)
	}
}

func (m *menu) readYesNo(prompt string, defaultValue bool) (bool, error) {
	label := "Y/n"
	if !defaultValue {
		label = "y/N"
	}
	for {
		line, err := m.readLine(fmt.Sprintf("%s [%s]: ", prompt, label))
		if err != nil {
			return false, err
		}
		switch strings.ToLower(line) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(m.app.Stderr, "请输入 Y 或 N。")
		}
	}
}

func (m *menu) readLine(prompt string) (string, error) {
	fmt.Fprint(m.app.Stdout, prompt)
	line, err := m.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	if strings.EqualFold(line, "q") {
		return "", errMenuBack
	}
	return line, nil
}

func formatSize(bytes int64) string {
	const (
		mib = 1024 * 1024
		gib = 1024 * mib
	)
	if bytes >= gib {
		return fmt.Sprintf("%.2f GB", float64(bytes)/gib)
	}
	if bytes >= mib {
		return fmt.Sprintf("%.2f MB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%d B", bytes)
}
