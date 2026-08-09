package tui

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

var errLaunchBack = errors.New("返回主菜单")
var errLaunchCancelled = errors.New("取消启动")

// LaunchDefaults mirrors the schema-1 configuration values used by the
// interactive launch wizard. The CLI remains the final validator.
type LaunchDefaults struct {
	GPULayers, FlashAttention, Host, Pooling              string
	ContextSize, Threads, BatchSize, UBatchSize, Parallel int
	Port, EmbeddingBatch, EmbeddingUBatch, Normalize      int
	ModelsMax                                             int
	UI, Autoload                                          bool
}

func (a *App) configureLaunch(mode string, command []string) ([]string, error) {
	if !a.LaunchWizard {
		return command, nil
	}
	result := append([]string(nil), command...)
	var extra []string

	if mode == "api" {
		model := argumentValue(result, "--model")
		projector, err := a.selectProjector(model)
		if err != nil {
			return nil, err
		}
		if projector != "" {
			result = append(result, "--mmproj", projector)
			minimum, err := a.readInteger("图片最小 token --image-min-tokens（0 使用模型默认）", 0, nonNegative)
			if err != nil {
				return nil, err
			}
			maximum, err := a.readInteger("图片最大 token --image-max-tokens（0 使用模型默认）", 0, nonNegative)
			if err != nil {
				return nil, err
			}
			if minimum > 0 {
				extra = append(extra, "--image-min-tokens", strconv.Itoa(minimum))
			}
			if maximum > 0 {
				extra = append(extra, "--image-max-tokens", strconv.Itoa(maximum))
			}
		}
	}

	fmt.Fprintln(a.Out, "\n运行参数")
	fmt.Fprintln(a.Out, "------------------------------------------------------------")
	fmt.Fprintln(a.Out, "直接按 Enter 使用方括号中的配置默认值；输入 q 返回主菜单。")
	contextSize, err := a.readInteger("上下文长度 --context-size（0 使用模型元数据）", a.Defaults.ContextSize, nonNegative)
	if err != nil {
		return nil, err
	}
	gpuLayers, err := a.readString("GPU 层数 --gpu-layers", a.Defaults.GPULayers, validateGPULayers)
	if err != nil {
		return nil, err
	}
	threads, err := a.readInteger("CPU 生成线程 --threads（-1 自动）", a.Defaults.Threads, func(value int) error {
		if value < -1 || value == 0 {
			return errors.New("必须为 -1 或正整数")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	batchDefault, ubatchDefault := a.Defaults.BatchSize, a.Defaults.UBatchSize
	if mode == "embedding" {
		batchDefault, ubatchDefault = a.Defaults.EmbeddingBatch, a.Defaults.EmbeddingUBatch
	}
	batch, ubatch, err := a.readBatchPair(batchDefault, ubatchDefault)
	if err != nil {
		return nil, err
	}
	flash, err := a.readString("Flash Attention --flash-attention（auto/on/off）", a.Defaults.FlashAttention, func(value string) error {
		switch strings.ToLower(value) {
		case "auto", "on", "off":
			return nil
		default:
			return errors.New("必须为 auto、on 或 off")
		}
	})
	if err != nil {
		return nil, err
	}
	result = append(result,
		"--context-size", strconv.Itoa(contextSize),
		"--gpu-layers", strings.ToLower(gpuLayers),
		"--threads", strconv.Itoa(threads),
		"--batch-size", strconv.Itoa(batch),
		"--ubatch-size", strconv.Itoa(ubatch),
		"--flash-attention", strings.ToLower(flash),
	)

	if mode != "chat" {
		parallel, readErr := a.readInteger("服务并发槽位 --parallel（-1 自动）", a.Defaults.Parallel, func(value int) error {
			if value < -1 || value == 0 {
				return errors.New("必须为 -1 或正整数")
			}
			return nil
		})
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, "--parallel", strconv.Itoa(parallel))
	}

	if mode == "embedding" {
		pooling, readErr := a.readString("Pooling --pooling", a.Defaults.Pooling, validatePooling)
		if readErr != nil {
			return nil, readErr
		}
		normalize, readErr := a.readInteger("向量归一化 --normalize（官方默认 2，-1 禁用）", a.Defaults.Normalize, func(value int) error {
			if value < -1 {
				return errors.New("必须不小于 -1")
			}
			return nil
		})
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, "--pooling", strings.ToLower(pooling), "--normalize", strconv.Itoa(normalize))
	}
	if mode == "router" {
		modelsMax, readErr := a.readInteger("最多同时加载模型数 --models-max（0 不限制）", a.Defaults.ModelsMax, nonNegative)
		if readErr != nil {
			return nil, readErr
		}
		autoload, readErr := a.readYesNo("是否按请求自动加载模型 --autoload", a.Defaults.Autoload)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, "--models-max", strconv.Itoa(modelsMax), "--autoload="+strconv.FormatBool(autoload))
		pooling, readErr := a.readString("Pooling --pooling", a.Defaults.Pooling, validatePooling)
		if readErr != nil {
			return nil, readErr
		}
		embeddingBatch, embeddingUBatch, readErr := a.readBatchPair(a.Defaults.EmbeddingBatch, a.Defaults.EmbeddingUBatch)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result,
			"--pooling", strings.ToLower(pooling),
			"--embedding-batch-size", strconv.Itoa(embeddingBatch),
			"--embedding-ubatch-size", strconv.Itoa(embeddingUBatch),
		)
	}

	if mode != "chat" {
		fmt.Fprintln(a.Out, "\n网络参数")
		fmt.Fprintln(a.Out, "------------------------------------------------------------")
		host, readErr := a.readString("监听地址 --host", a.Defaults.Host, func(value string) error {
			if strings.TrimSpace(value) == "" {
				return errors.New("监听地址不能为空")
			}
			return nil
		})
		if readErr != nil {
			return nil, readErr
		}
		port, readErr := a.readInteger("监听端口 --port", a.Defaults.Port, func(value int) error {
			if value < 1 || value > 65535 {
				return errors.New("必须在 1 到 65535 之间")
			}
			return nil
		})
		if readErr != nil {
			return nil, readErr
		}
		ui, readErr := a.readYesNo("是否启用 Web UI --ui", a.Defaults.UI)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, "--host", host, "--port", strconv.Itoa(port), "--ui="+strconv.FormatBool(ui))
	}

	fmt.Fprintln(a.Out, "\n高级选项")
	fmt.Fprintln(a.Out, "------------------------------------------------------------")
	for {
		line, readErr := a.readOperation("自定义 llama.cpp 参数（留空跳过）: ")
		if readErr != nil {
			return nil, readErr
		}
		custom, parseErr := splitArguments(line)
		if parseErr != nil {
			fmt.Fprintln(a.Err, "错误:", parseErr)
			continue
		}
		extra = append(extra, custom...)
		break
	}
	if len(extra) > 0 {
		result = append(result, "--")
		result = append(result, extra...)
	}
	fmt.Fprintln(a.Out, "启动参数:", safeText(strings.Join(result, " ")))
	ok, err := a.readYesNo("确认使用以上参数启动", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errLaunchCancelled
	}
	return result, nil
}

func (a *App) configureRouterPreset(command []string) ([]string, error) {
	result := append([]string(nil), command...)
	if a.RouterPresetExists != nil && a.RouterPresetExists() {
		overwrite, err := a.readYesNo("手动配置已存在，是否覆盖", false)
		if err != nil {
			return nil, err
		}
		if !overwrite {
			return nil, errLaunchCancelled
		}
		result = append(result, "--force")
	}
	gpu, err := a.readString("GPU 层数 --gpu-layers", a.Defaults.GPULayers, validateGPULayers)
	if err != nil {
		return nil, err
	}
	contextSize, err := a.readInteger("上下文长度 --context-size（0 使用模型元数据）", a.Defaults.ContextSize, nonNegative)
	if err != nil {
		return nil, err
	}
	pooling, err := a.readString("Pooling --pooling", a.Defaults.Pooling, validatePooling)
	if err != nil {
		return nil, err
	}
	batch, ubatch, err := a.readBatchPair(a.Defaults.EmbeddingBatch, a.Defaults.EmbeddingUBatch)
	if err != nil {
		return nil, err
	}
	mmprojAuto := true
	if a.ModelOptions != nil {
		_, projectors, scanErr := a.ModelOptions("mmproj")
		if scanErr != nil {
			return nil, scanErr
		}
		if len(projectors) > 0 {
			mmprojAuto, err = a.readYesNo("是否按文件名前缀自动匹配 mmproj", true)
			if err != nil {
				return nil, err
			}
		}
	}
	result = append(result,
		"--gpu-layers", gpu,
		"--context-size", strconv.Itoa(contextSize),
		"--pooling", pooling,
		"--embedding-batch-size", strconv.Itoa(batch),
		"--embedding-ubatch-size", strconv.Itoa(ubatch),
		"--mmproj-auto="+strconv.FormatBool(mmprojAuto),
	)
	return result, nil
}

func (a *App) selectProjector(modelPath string) (string, error) {
	if a.ModelOptions == nil {
		return "", nil
	}
	_, options, err := a.ModelOptions("mmproj")
	if err != nil {
		return "", err
	}
	selected := ""
	if len(options) > 0 {
		defaultChoice := 0
		modelTokens := projectorMatchTokens(filepath.Base(modelPath))
		bestScore := 0
		fmt.Fprintln(a.Out, "\n选择 mmproj")
		fmt.Fprintln(a.Out, "------------------------------------------------------------")
		fmt.Fprintln(a.Out, "  [q] 返回主菜单")
		fmt.Fprintln(a.Out, "  [0] 不使用 mmproj")
		for i, option := range options {
			score := commonTokenPrefix(modelTokens, projectorMatchTokens(option.ID))
			if score > bestScore {
				bestScore, defaultChoice = score, i+1
			}
		}
		for i, option := range options {
			marker := ""
			if i+1 == defaultChoice {
				marker = "  [自动匹配]"
			}
			fmt.Fprintf(a.Out, "  %2d. %s%s\n", i+1, safeText(option.ID), marker)
		}
		for {
			value, readErr := a.readOperation(fmt.Sprintf("请选择 mmproj [%d]: ", defaultChoice))
			if readErr != nil {
				return "", readErr
			}
			if value == "" {
				value = strconv.Itoa(defaultChoice)
			}
			choice, parseErr := strconv.Atoi(value)
			if parseErr == nil && choice >= 0 && choice <= len(options) {
				if choice > 0 {
					selected = options[choice-1].Path
				}
				break
			}
			fmt.Fprintf(a.Err, "错误: 请输入 0 到 %d 之间的数字。\n", len(options))
		}
	}
	if selected != "" {
		return selected, nil
	}
	custom, err := a.readOperation("其他 mmproj 路径（留空跳过）: ")
	if err != nil {
		return "", err
	}
	return custom, nil
}

func (a *App) readBatchPair(batchDefault, ubatchDefault int) (int, int, error) {
	for {
		batch, err := a.readInteger("逻辑批次 --batch-size", batchDefault, positive)
		if err != nil {
			return 0, 0, err
		}
		ubatch, err := a.readInteger("物理批次 --ubatch-size", ubatchDefault, positive)
		if err != nil {
			return 0, 0, err
		}
		if ubatch <= batch {
			return batch, ubatch, nil
		}
		fmt.Fprintln(a.Err, "错误: ubatch-size 不能大于 batch-size。")
	}
}

func (a *App) readString(prompt, defaultValue string, validate func(string) error) (string, error) {
	for {
		value, err := a.readOperation(fmt.Sprintf("%s [%s]: ", prompt, defaultValue))
		if err != nil {
			return "", err
		}
		if value == "" {
			value = defaultValue
		}
		value = strings.TrimSpace(value)
		if err := validate(value); err == nil {
			return value, nil
		} else {
			fmt.Fprintln(a.Err, "错误:", err)
		}
	}
}

func (a *App) readInteger(prompt string, defaultValue int, validate func(int) error) (int, error) {
	for {
		value, err := a.readOperation(fmt.Sprintf("%s [%d]: ", prompt, defaultValue))
		if err != nil {
			return 0, err
		}
		if value == "" {
			value = strconv.Itoa(defaultValue)
		}
		number, parseErr := strconv.Atoi(value)
		if parseErr == nil {
			if validateErr := validate(number); validateErr == nil {
				return number, nil
			} else {
				fmt.Fprintln(a.Err, "错误:", validateErr)
				continue
			}
		}
		fmt.Fprintln(a.Err, "错误: 请输入有效整数。")
	}
}

func (a *App) readYesNo(prompt string, defaultValue bool) (bool, error) {
	label := "Y/n"
	if !defaultValue {
		label = "y/N"
	}
	for {
		value, err := a.readOperation(fmt.Sprintf("%s [%s]: ", prompt, label))
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(a.Err, "错误: 请输入 Y 或 N。")
		}
	}
}

func (a *App) readOperation(prompt string) (string, error) {
	value, err := a.read(prompt)
	if err != nil {
		if errors.Is(err, io.EOF) && value != "" {
			return value, nil
		}
		return "", err
	}
	if strings.EqualFold(value, "q") {
		return "", errLaunchBack
	}
	return value, nil
}

func validateGPULayers(value string) error {
	if strings.EqualFold(value, "auto") || strings.EqualFold(value, "all") {
		return nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < -1 {
		return errors.New("必须为 auto、all 或不小于 -1 的整数")
	}
	return nil
}

func validatePooling(value string) error {
	switch strings.ToLower(value) {
	case "", "none", "mean", "cls", "last", "rank":
		return nil
	default:
		return errors.New("必须为 none、mean、cls、last 或 rank")
	}
}

func nonNegative(value int) error {
	if value < 0 {
		return errors.New("必须是非负整数")
	}
	return nil
}

func positive(value int) error {
	if value <= 0 {
		return errors.New("必须是正整数")
	}
	return nil
}

func argumentValue(arguments []string, name string) string {
	for i := 0; i+1 < len(arguments); i++ {
		if arguments[i] == name {
			return arguments[i+1]
		}
	}
	return ""
}

func projectorMatchTokens(name string) []string {
	stem := strings.TrimSuffix(strings.ToLower(name), strings.ToLower(filepath.Ext(name)))
	parts := strings.FieldsFunc(stem, func(character rune) bool { return !unicode.IsLetter(character) && !unicode.IsDigit(character) })
	filtered := parts[:0]
	for _, part := range parts {
		if part != "mmproj" && part != "projector" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func commonTokenPrefix(left, right []string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i] != right[i] {
			return i
		}
	}
	return limit
}

func splitArguments(input string) ([]string, error) {
	runes := []rune(strings.TrimSpace(input))
	var arguments []string
	var current strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			arguments = append(arguments, current.String())
			current.Reset()
			started = false
		}
	}
	for i := 0; i < len(runes); i++ {
		character := runes[i]
		if quote == 0 {
			switch {
			case unicode.IsSpace(character):
				flush()
			case character == '\'' || character == '"':
				quote, started = character, true
			case character == '\\' && i+1 < len(runes) && (unicode.IsSpace(runes[i+1]) || runes[i+1] == '\'' || runes[i+1] == '"'):
				i++
				current.WriteRune(runes[i])
				started = true
			default:
				current.WriteRune(character)
				started = true
			}
			continue
		}
		if character == quote {
			quote = 0
			continue
		}
		if quote == '"' && character == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
			i++
			current.WriteRune(runes[i])
			continue
		}
		current.WriteRune(character)
	}
	if quote != 0 {
		return nil, fmt.Errorf("自定义参数存在未闭合的 %c 引号", quote)
	}
	flush()
	return arguments, nil
}
