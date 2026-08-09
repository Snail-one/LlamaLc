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

const menuRule = "------------------------------------------------------------"

type menu struct {
	app    *Application
	reader *bufio.Reader
}

type menuAction int

const (
	actionServe menuAction = iota + 1
	actionEmbedding
	actionRerank
	actionRouterConfig
	actionRouter
	actionChat
	actionUpdateLlama
	actionUpdateLauncher
	actionResetAPIKey
	actionShowAPIKey
	actionCleanup
)

type menuOption struct {
	label  string
	action menuAction
}

type menuCategory struct {
	title        string
	summary      string
	defaultFirst bool
	options      []menuOption
}

var menuCategories = []menuCategory{
	{
		title: "启动", summary: "API / 多模型 Router / CLI", defaultFirst: true,
		options: []menuOption{
			{label: "启动单模型 API", action: actionServe},
			{label: "启动 Embedding API", action: actionEmbedding},
			{label: "启动 Rerank API", action: actionRerank},
			{label: "启动多模型 Router", action: actionRouter},
			{label: "启动 CLI 聊天", action: actionChat},
		},
	},
	{
		title: "配置", summary: "Router 配置 / API key", defaultFirst: true,
		options: []menuOption{
			{label: "生成 Router 配置", action: actionRouterConfig},
			{label: "重置 API key", action: actionResetAPIKey},
			{label: "显示 API key", action: actionShowAPIKey},
		},
	},
	{
		title: "升级维护", summary: "更新 / 清理恢复",
		options: []menuOption{
			{label: "更新 llama.cpp", action: actionUpdateLlama},
			{label: "更新启动器", action: actionUpdateLauncher},
			{label: "清理与恢复", action: actionCleanup},
		},
	},
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
		app.printMainMenu()
		choice, err := m.readMainChoice()
		if errors.Is(err, io.EOF) {
			return 0
		}
		if errors.Is(err, errMenuBack) {
			return 0
		}
		if errors.Is(err, errUpdaterHandoff) {
			waitForLauncherUpdateExit(m.reader, app.Stdout)
			return 0
		}
		if err != nil {
			fmt.Fprintln(app.Stderr, "错误:", safeTerminalText(err.Error()))
			continue
		}
		clearTerminal(app.Stdout)
		m.printOperationHeader(choice)
		if err := m.runChoice(choice); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			if errors.Is(err, errUpdaterHandoff) {
				waitForLauncherUpdateExit(m.reader, app.Stdout)
				return 0
			}
			if errors.Is(err, errMenuBack) {
				clearBeforeMenu = true
				continue
			}
			fmt.Fprintln(app.Stderr, "错误:", safeTerminalText(err.Error()))
			continue
		}
		clearBeforeMenu = true
	}
}

func waitForLauncherUpdateExit(reader *bufio.Reader, stdout io.Writer) {
	fmt.Fprint(stdout, "\n按 Enter 退出当前程序；更新完成后将自动启动新版本...")
	_, _ = reader.ReadString('\n')
}

func (app *Application) printMainMenu() {
	fmt.Fprintf(app.Stdout, `
============================================================
 llama.cpp Go 启动器
============================================================
`)
	if app.UpdateNotice != "" {
		fmt.Fprintf(app.Stdout, `
更新结果
  启动器: %s
  状态: 更新成功，已自动重新启动
`, safeTerminalText(app.UpdateNotice))
	}
	fmt.Fprintf(app.Stdout, `
运行状态
  启动器版本: %s
  llama.cpp:   %s
  根目录:      %s

功能目录
`, buildversion.Version, app.llamaVersionDisplay(), safeTerminalText(app.Root))
	for index, category := range menuCategories {
		fmt.Fprintf(app.Stdout, "  [%d] %s（%s）\n", index+1, category.title, category.summary)
	}
	fmt.Fprintf(app.Stdout, `  [q] 退出

选择目录后再选择具体操作；子菜单输入 0 或 q 返回主菜单。
%s
`, menuRule)
}

func (m *menu) printOperationHeader(choice menuAction) {
	if choice == actionCleanup {
		return
	}
	for _, category := range menuCategories {
		for _, option := range category.options {
			if option.action == choice {
				fmt.Fprintf(m.app.Stdout, "\n%s\n%s\n", option.label, menuRule)
				return
			}
		}
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

func (m *menu) runChoice(choice menuAction) error {
	switch choice {
	case actionServe:
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
	case actionEmbedding:
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
	case actionRerank:
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
	case actionRouterConfig:
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
	case actionRouter:
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
	case actionChat:
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
	case actionUpdateLlama:
		return m.updateComponent(componentLlama)
	case actionUpdateLauncher:
		return m.updateComponent(componentLauncher)
	case actionResetAPIKey:
		return m.resetAPIKey()
	case actionShowAPIKey:
		return m.showAPIKey()
	case actionCleanup:
		return m.runCleanupMenu()
	}
	return nil
}

func (m *menu) readMainChoice() (menuAction, error) {
	for {
		line, err := m.readLine("请选择功能目录 [1]: ")
		if err != nil {
			return 0, err
		}
		if line == "" {
			line = "1"
		}
		category, parseErr := strconv.Atoi(line)
		if parseErr != nil || category < 1 || category > len(menuCategories) {
			fmt.Fprintf(m.app.Stderr, "请输入 1 到 %d 或 q；直接按 Enter 进入启动菜单。\n", len(menuCategories))
			continue
		}

		clearTerminal(m.app.Stdout)
		action, err := m.readSubmenuChoice(menuCategories[category-1])
		if errors.Is(err, errMenuBack) {
			clearTerminal(m.app.Stdout)
			m.app.printMainMenu()
			continue
		}
		return action, err
	}
}

func (m *menu) readSubmenuChoice(category menuCategory) (menuAction, error) {
	fmt.Fprintf(m.app.Stdout, "\n%s\n%s\n", category.title, menuRule)
	for index, option := range category.options {
		fmt.Fprintf(m.app.Stdout, "  [%d] %s\n", index+1, option.label)
	}
	fmt.Fprintln(m.app.Stdout, "  [0/q] 返回主菜单")
	prompt := "请选择操作: "
	if category.defaultFirst {
		prompt = "请选择操作 [1]: "
	}
	for {
		line, err := m.readLine(prompt)
		if err != nil {
			return 0, err
		}
		if line == "0" {
			return 0, errMenuBack
		}
		if line == "" {
			if category.defaultFirst {
				return category.options[0].action, nil
			}
			fmt.Fprintf(m.app.Stderr, "请输入 0 到 %d；升级维护不使用默认选项。\n", len(category.options))
			continue
		}
		choice, parseErr := strconv.Atoi(line)
		if parseErr == nil && choice >= 1 && choice <= len(category.options) {
			return category.options[choice-1].action, nil
		}
		if category.defaultFirst {
			fmt.Fprintf(m.app.Stderr, "请输入 0 到 %d 或 q；0 返回，直接按 Enter 选择 1。\n", len(category.options))
		} else {
			fmt.Fprintf(m.app.Stderr, "请输入 0 到 %d 或 q；0 返回。\n", len(category.options))
		}
	}
}

func (m *menu) runCleanupMenu() error {
	for {
		candidates, warnings := scanCleanupCandidates(m.app.Root)
		fmt.Fprintf(m.app.Stdout, "\n清理与恢复\n%s\n", menuRule)
		for _, warning := range warnings {
			fmt.Fprintln(m.app.Stderr, "警告:", safeTerminalText(warning))
		}
		automatic, review, recent := cleanupCandidateCounts(candidates)
		if len(candidates) == 0 {
			fmt.Fprintln(m.app.Stdout, "未发现需要处理的残留或恢复目录。")
		} else {
			fmt.Fprintf(m.app.Stdout, "发现 %d 项：可安全清理 %d，需确认 %d，暂不处理 %d。\n", len(candidates), automatic, review, recent)
			fmt.Fprintln(m.app.Stdout, "批量清理只处理“可安全清理”项目。")
		}
		fmt.Fprintln(m.app.Stdout, "\n操作")
		if automatic > 0 {
			fmt.Fprintf(m.app.Stdout, "  [1] 清理全部安全项（%d 项）\n", automatic)
		} else {
			fmt.Fprintln(m.app.Stdout, "  [1] 清理全部安全项（当前无可清理项）")
		}
		fmt.Fprintln(m.app.Stdout, "  [0/q] 返回主菜单")
		if len(candidates) > 0 {
			fmt.Fprintln(m.app.Stdout, "\n待处理项目")
			for index, candidate := range candidates {
				fmt.Fprintf(m.app.Stdout, "\n[%d] %s\n", index+2, candidate.Kind)
				fmt.Fprintf(m.app.Stdout, "    状态: %s\n", cleanupCandidateStatus(candidate))
				fmt.Fprintf(m.app.Stdout, "    大小: %s\n", cleanupSizeDisplay(candidate))
				fmt.Fprintf(m.app.Stdout, "    路径: %s\n", safeTerminalText(candidate.Path))
				fmt.Fprintf(m.app.Stdout, "    说明: %s\n", safeTerminalText(candidate.Reason))
			}
		}
		line, err := m.readLine("请选择操作或项目编号: ")
		if errors.Is(err, errMenuBack) {
			return nil
		}
		if err != nil {
			return err
		}
		if line == "0" {
			return nil
		}
		if line == "1" {
			if automatic == 0 {
				fmt.Fprintln(m.app.Stdout, "当前没有可安全批量清理的项目。")
				continue
			}
			cleaned := 0
			for _, candidate := range candidates {
				if !candidate.Automatic {
					continue
				}
				if err := deleteCleanupCandidate(m.app.Root, candidate, true); err != nil {
					fmt.Fprintf(m.app.Stderr, "警告: 无法清理 %s: %v\n", safeTerminalText(candidate.Path), err)
					continue
				}
				cleaned++
				fmt.Fprintln(m.app.Stdout, "已清理:", safeTerminalText(candidate.Path))
			}
			if cleaned == 0 {
				fmt.Fprintln(m.app.Stdout, "没有可自动清理的安全残留。")
			}
			continue
		}
		selection, parseErr := strconv.Atoi(line)
		index := selection - 2
		if parseErr != nil || index < 0 || index >= len(candidates) {
			fmt.Fprintf(m.app.Stderr, "请输入 0 到 %d 之间的有效编号，或输入 q。\n", len(candidates)+1)
			continue
		}
		if err := m.manageCleanupCandidate(candidates[index]); err != nil {
			if errors.Is(err, errMenuBack) {
				return nil
			}
			return err
		}
	}
}

func cleanupCandidateCounts(candidates []cleanupCandidate) (automatic, review, recent int) {
	for _, candidate := range candidates {
		switch {
		case candidate.Automatic:
			automatic++
		case candidate.Recent:
			recent++
		default:
			review++
		}
	}
	return automatic, review, recent
}

func cleanupCandidateStatus(candidate cleanupCandidate) string {
	switch {
	case candidate.Automatic:
		return "可安全清理"
	case candidate.Recent:
		return "暂不处理（可能正在使用）"
	default:
		return "需手动确认"
	}
}

func (m *menu) manageCleanupCandidate(candidate cleanupCandidate) error {
	fmt.Fprintf(m.app.Stdout, "\n类型: %s\n大小: %s\n原因: %s\n完整路径: %s\n", candidate.Kind, cleanupSizeDisplay(candidate), safeTerminalText(candidate.Reason), safeTerminalText(candidate.Path))
	fmt.Fprintln(m.app.Stdout, "  [1] 查看目录内容")
	fmt.Fprintln(m.app.Stdout, "  [2] 使用系统文件管理器打开")
	if candidate.Recent {
		fmt.Fprintln(m.app.Stdout, "  [3] 永久删除（当前不可用）")
	} else {
		fmt.Fprintln(m.app.Stdout, "  [3] 永久删除")
	}
	fmt.Fprintln(m.app.Stdout, "  [0] 返回列表")
	fmt.Fprintln(m.app.Stdout, "  [q] 返回主菜单")
	line, err := m.readLine("请选择操作: ")
	if err != nil {
		return err
	}
	switch strings.ToLower(line) {
	case "", "0":
		return nil
	case "1":
		return m.viewCleanupCandidate(candidate)
	case "2":
		if err := launchCleanupPath(candidate.Path); err != nil {
			return fmt.Errorf("无法打开目录 %s: %w", candidate.Path, err)
		}
		fmt.Fprintln(m.app.Stdout, "已请求系统文件管理器打开:", safeTerminalText(candidate.Path))
		return nil
	case "3":
		if candidate.Recent {
			fmt.Fprintln(m.app.Stdout, "该项目可能仍在使用，当前不允许删除。")
			return nil
		}
		fmt.Fprintln(m.app.Stdout, "即将永久删除完整路径:", safeTerminalText(candidate.Path))
		confirmed, err := m.readYesNo("确认已检查并转移需要保留的文件，是否继续删除", false)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(m.app.Stdout, "已取消，未修改任何文件。")
			return nil
		}
		if err := deleteCleanupCandidate(m.app.Root, candidate, false); err != nil {
			return err
		}
		fmt.Fprintln(m.app.Stdout, "已删除:", safeTerminalText(candidate.Path))
		return nil
	default:
		fmt.Fprintln(m.app.Stderr, "请输入 0 到 3，或输入 q 返回主菜单。")
		return nil
	}
}

func (m *menu) viewCleanupCandidate(candidate cleanupCandidate) error {
	info, err := os.Lstat(candidate.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		fmt.Fprintf(m.app.Stdout, "普通文件: %s（%s）\n", safeTerminalText(candidate.Path), cleanupSizeDisplay(candidate))
		return nil
	}
	entries, err := os.ReadDir(candidate.Path)
	if err != nil {
		return err
	}
	fmt.Fprintln(m.app.Stdout, "目录内容（最多显示 50 项）:")
	limit := len(entries)
	if limit > 50 {
		limit = 50
	}
	for _, entry := range entries[:limit] {
		kind := "文件"
		if entry.IsDir() {
			kind = "目录"
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = "链接"
		}
		fmt.Fprintf(m.app.Stdout, "  [%s] %s\n", kind, safeTerminalText(entry.Name()))
	}
	if len(entries) > limit {
		fmt.Fprintf(m.app.Stdout, "  ……另有 %d 项未显示\n", len(entries)-limit)
	}
	return nil
}

func (m *menu) resetAPIKey() error {
	confirmed, err := m.readYesNo("将生成新的 API key，旧 key 将立即失效，是否继续", false)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(m.app.Stdout, "已取消，API key 未修改。")
		return m.pause()
	}
	if err := resetAPIKey(m.app.Root, m.app.Paths.APIKeyFile, m.app.Stdout); err != nil {
		return err
	}
	return m.pause()
}

func (m *menu) showAPIKey() error {
	confirmed, err := m.readYesNo("API key 将以明文显示，请确认终端未共享或录屏，是否继续", false)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(m.app.Stdout, "已取消，未显示 API key。")
		return m.pause()
	}

	key, exists, err := ReadAPIKeyFile(m.app.Root, m.app.Paths.APIKeyFile)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("API key 文件不存在")
	}

	fmt.Fprintln(m.app.Stdout, "\nAPI key（请勿共享）")
	fmt.Fprintln(m.app.Stdout, menuRule)
	fmt.Fprintln(m.app.Stdout, key)
	fmt.Fprintln(m.app.Stdout, "\nAPI key 文件:", safeTerminalText(m.app.Paths.APIKeyFile))
	return m.pause()
}

func (m *menu) updateComponent(component componentSelection) error {
	if m.app.Updater == nil {
		return errors.New("更新管理器未初始化")
	}
	_, err := runManagementCommand(context.Background(), m.app.Updater, "update", []string{"--component", string(component), "--yes"}, m.reader, true)
	if err != nil {
		return err
	}
	if component == componentLlama {
		if err := m.app.refreshManagedRuntime(); err != nil {
			return fmt.Errorf("llama.cpp 已更新，但无法刷新主页状态: %w", err)
		}
		fmt.Fprintln(m.app.Stdout, "已载入活动 llama.cpp:", m.app.llamaVersionDisplay())
	}
	return m.pause()
}

func (app *Application) refreshManagedRuntime() error {
	if app.Updater == nil {
		return errors.New("更新管理器未初始化")
	}
	state, exists, err := LoadUpdateState(app.Root)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("未找到 config/update-state.json")
	}
	paths, err := ResolveManagedPaths(app.Root, app.Updater.GOOS, state)
	if err != nil {
		return err
	}
	probe := app.Updater.Probe
	if probe == nil {
		probe = OSInstallationProbe{}
	}
	detectedVersion, err := VerifyInstallation(app.Root, paths, probe)
	if err != nil {
		return err
	}

	// Commit the new in-memory view only after the state, paths, and executable
	// have all been validated. A failed refresh must not leave a mixed view.
	app.Paths = paths
	app.LlamaTag = state.LlamaTag
	app.LlamaBackend = state.Backend
	app.LlamaVersion = detectedVersion
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
	fmt.Fprintln(m.app.Stdout, "\n选择模型")
	fmt.Fprintln(m.app.Stdout, menuRule)
	fmt.Fprintln(m.app.Stdout, "  [0/q] 返回主菜单")
	for i, model := range models {
		fmt.Fprintf(m.app.Stdout, "  %2d. %s  (%s)\n", i+1, safeTerminalText(model.ID), formatSize(model.Size))
	}
	choice, err := m.readChoice("请选择模型", 1, 0, len(models))
	if err != nil {
		return ModelFile{}, err
	}
	if choice == 0 {
		return ModelFile{}, errMenuBack
	}
	return models[choice-1], nil
}

func (m *menu) selectProjector(model ModelFile, projectors []ModelFile) (*ModelFile, error) {
	recommended := FindMatchingMmproj(model, projectors)
	defaultChoice := 0
	fmt.Fprintln(m.app.Stdout, "\n选择 mmproj")
	fmt.Fprintln(m.app.Stdout, menuRule)
	fmt.Fprintln(m.app.Stdout, "  [q] 返回主菜单")
	fmt.Fprintln(m.app.Stdout, "  [0] 不使用 mmproj")
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
	fmt.Fprintln(m.app.Stdout, "\n运行参数")
	fmt.Fprintln(m.app.Stdout, menuRule)
	fmt.Fprintln(m.app.Stdout, "直接按 Enter 使用方括号中的默认值。")
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
	fmt.Fprintln(m.app.Stdout, "\n网络参数")
	fmt.Fprintln(m.app.Stdout, menuRule)
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
	fmt.Fprintln(m.app.Stdout, "\n高级选项")
	fmt.Fprintln(m.app.Stdout, menuRule)
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
