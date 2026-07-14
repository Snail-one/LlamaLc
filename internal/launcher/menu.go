package launcher

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	buildversion "github.com/joker/llama-launcher/internal/version"
)

type menu struct {
	app    *Application
	reader *bufio.Reader
}

func (app *Application) RunMenu() int {
	m := &menu{app: app, reader: bufio.NewReader(app.Stdin)}
	// Pass the same reader to children so any bytes already buffered by the menu
	// remain visible to llama-cli/llama-server.
	app.Stdin = m.reader
	for {
		fmt.Fprintf(app.Stdout, `
llama.cpp Go 启动器 %s
根目录: %s

  1. 单模型 API 服务
  2. Embedding API
  3. Rerank API
  4. 生成手动 Router 配置
  5. 多模型 Router
  6. CLI 命令行聊天
  0. 退出
`, buildversion.Version, app.Root)
		choice, err := m.readChoice("请选择", 1, 0, 6)
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(app.Stderr, "错误:", err)
			continue
		}
		if choice == 0 {
			return 0
		}
		if err := m.runChoice(choice); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintln(app.Stderr, "错误:", err)
		}
	}
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
		return m.confirmAndRun("serve", args)
	case 2:
		model, err := m.selectModel(m.app.Paths.Embeddings, EmbeddingModel, ggufExtension)
		if err != nil {
			return err
		}
		pooling, err := m.readPooling(m.app.Config.Embedding.Pooling)
		if err != nil {
			return err
		}
		ubatchSize, err := m.readPositiveInt("物理批次大小 --ubatch-size", m.app.Config.Embedding.UBatchSize)
		if err != nil {
			return err
		}
		return m.confirmAndRun("embedding", []string{
			"--model", model.Path,
			"--pooling", pooling,
			"--ubatch-size", strconv.Itoa(ubatchSize),
		})
	case 3:
		model, err := m.selectModel(m.app.Paths.Rerank, RerankModel, ggufExtension)
		if err != nil {
			return err
		}
		return m.confirmAndRun("rerank", []string{"--model", model.Path})
	case 4:
		args := []string{}
		if _, err := os.Stat(m.app.Paths.RouterManual); err == nil {
			overwrite, err := m.readYesNo("手动配置已存在，是否覆盖", false)
			if err != nil {
				return err
			}
			if !overwrite {
				fmt.Fprintln(m.app.Stdout, "已取消，原文件未修改。")
				return nil
			}
			args = append(args, "--force")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err := m.app.RunCommand("router-config", args)
		return err
	case 5:
		return m.confirmAndRun("router", nil)
	case 6:
		model, err := m.selectModel(m.app.Paths.Models, GenerationModel, generationExtensions)
		if err != nil {
			return err
		}
		return m.confirmAndRun("chat", []string{"--model", model.Path})
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
	for i, model := range models {
		fmt.Fprintf(m.app.Stdout, "  %2d. %s  (%s)\n", i+1, model.ID, formatSize(model.Size))
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
	fmt.Fprintln(m.app.Stdout, "   0. 不使用 mmproj")
	for i, projector := range projectors {
		label := ""
		if recommended != nil && projector.Path == recommended.Path {
			defaultChoice = i + 1
			label = "  [自动匹配]"
		}
		fmt.Fprintf(m.app.Stdout, "  %2d. %s%s\n", i+1, projector.ID, label)
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
	ok, err := m.readYesNo("确认启动", true)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(m.app.Stdout, "已取消启动。")
		return nil
	}
	code, err := m.app.RunCommand(command, args)
	if err != nil {
		return err
	}
	if code != 0 {
		fmt.Fprintf(m.app.Stderr, "进程退出码: %d\n", code)
	} else {
		fmt.Fprintln(m.app.Stdout, "进程已结束。")
	}
	return nil
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
	for {
		line, err := m.readLine(fmt.Sprintf("%s [%d]: ", prompt, defaultValue))
		if err != nil {
			return 0, err
		}
		if line == "" {
			return defaultValue, nil
		}
		value, err := strconv.Atoi(line)
		if err == nil && ValidateUBatchSize(value) == nil {
			return value, nil
		}
		fmt.Fprintln(m.app.Stderr, "请输入正整数。")
	}
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
