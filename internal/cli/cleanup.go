package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/update"
)

var errCleanupMain = errors.New("返回主菜单")

func (a *App) runCleanupMenu() error {
	reader, ok := a.In.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(a.In)
	}
	for {
		items, err := update.CleanupCandidates(a.Layout)
		if err != nil {
			return err
		}
		automatic, review, recent := cleanupCounts(items)
		fmt.Fprintln(a.Out, "\n清理与恢复")
		fmt.Fprintln(a.Out, "------------------------------------------------------------")
		if len(items) == 0 {
			fmt.Fprintln(a.Out, "未发现需要处理的残留或恢复目录。")
		} else {
			fmt.Fprintf(a.Out, "发现 %d 项：可安全清理 %d，需确认 %d，暂不处理 %d。\n", len(items), automatic, review, recent)
			fmt.Fprintln(a.Out, "批量清理只处理“可安全清理”项目；旧版路径绝不会批量删除。")
		}
		fmt.Fprintln(a.Out, "\n操作")
		fmt.Fprintf(a.Out, "  [1] 清理全部安全项（%d 项）\n", automatic)
		fmt.Fprintln(a.Out, "  [0/q] 返回主菜单")
		if len(items) > 0 {
			fmt.Fprintln(a.Out, "\n待处理项目")
			for index, item := range items {
				fmt.Fprintf(a.Out, "\n[%d] %s\n", index+2, safeOutput(item.Kind))
				fmt.Fprintf(a.Out, "    状态: %s\n", cleanupStatus(item))
				fmt.Fprintf(a.Out, "    大小: %s\n", cleanupSize(item))
				fmt.Fprintf(a.Out, "    路径: %s\n", safeOutput(item.Path))
				fmt.Fprintf(a.Out, "    说明: %s\n", safeOutput(item.Reason))
			}
		}
		value, err := cleanupRead(reader, a.Out, "请选择操作或项目编号: ")
		if err != nil {
			return err
		}
		if value == "0" || strings.EqualFold(value, "q") {
			return nil
		}
		if value == "1" {
			if automatic == 0 {
				fmt.Fprintln(a.Out, "当前没有可安全批量清理的项目。")
				continue
			}
			for _, item := range items {
				if !item.Automatic {
					continue
				}
				if err := update.DeleteCandidate(a.Layout, item); err != nil {
					fmt.Fprintf(a.Err, "警告: 无法清理 %s: %v\n", safeOutput(item.Path), err)
				} else {
					fmt.Fprintln(a.Out, "已清理:", safeOutput(item.Path))
				}
			}
			continue
		}
		selection, parseErr := strconv.Atoi(value)
		index := selection - 2
		if parseErr != nil || index < 0 || index >= len(items) {
			fmt.Fprintf(a.Err, "错误: 请输入 0 到 %d 之间的有效编号，或输入 q。\n", len(items)+1)
			continue
		}
		if err := a.manageCleanupCandidate(reader, items[index]); err != nil {
			if errors.Is(err, errCleanupMain) {
				return nil
			}
			return err
		}
	}
}

func (a *App) manageCleanupCandidate(reader *bufio.Reader, item update.CleanupCandidate) error {
	fmt.Fprintf(a.Out, "\n类型: %s\n大小: %s\n原因: %s\n完整路径: %s\n", safeOutput(item.Kind), cleanupSize(item), safeOutput(item.Reason), safeOutput(item.Path))
	for {
		fmt.Fprintln(a.Out, "\n项目操作")
		fmt.Fprintln(a.Out, "  [1] 查看目录内容")
		fmt.Fprintln(a.Out, "  [2] 使用系统文件管理器打开")
		if item.Recent {
			fmt.Fprintln(a.Out, "  [3] 永久删除（当前不可用）")
		} else {
			fmt.Fprintln(a.Out, "  [3] 永久删除")
		}
		fmt.Fprintln(a.Out, "  [0] 返回列表")
		fmt.Fprintln(a.Out, "  [q] 返回主菜单")
		value, err := cleanupRead(reader, a.Out, "请选择操作: ")
		if err != nil {
			return err
		}
		switch strings.ToLower(value) {
		case "", "0":
			return nil
		case "q":
			return errCleanupMain
		case "1":
			if err := viewCleanupCandidate(a.Out, item); err != nil {
				return err
			}
		case "2":
			if err := openCleanupPath(item.Path); err != nil {
				return fmt.Errorf("无法打开目录 %s: %w", item.Path, err)
			}
			fmt.Fprintln(a.Out, "已请求系统文件管理器打开:", safeOutput(item.Path))
		case "3":
			if item.Recent {
				fmt.Fprintln(a.Out, "该项目可能仍在使用，当前不允许删除。")
				continue
			}
			fmt.Fprintln(a.Out, "即将永久删除完整路径:", safeOutput(item.Path))
			confirmed, err := cleanupYesNo(reader, a.Out, a.Err, "确认已检查并转移需要保留的文件，是否继续删除", false)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(a.Out, "已取消，未修改任何文件。")
				continue
			}
			if err := update.DeleteCandidate(a.Layout, item); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, "已删除:", safeOutput(item.Path))
			return nil
		default:
			fmt.Fprintln(a.Err, "错误: 请输入 0 到 3，或输入 q 返回主菜单。")
		}
	}
}

func viewCleanupCandidate(output io.Writer, item update.CleanupCandidate) error {
	info, err := os.Lstat(item.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		fmt.Fprintf(output, "普通文件: %s（%s）\n", safeOutput(item.Path), cleanupSize(item))
		return nil
	}
	entries, err := os.ReadDir(item.Path)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "目录内容（最多显示 50 项）:")
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
		fmt.Fprintf(output, "  [%s] %s\n", kind, safeOutput(entry.Name()))
	}
	if len(entries) > limit {
		fmt.Fprintf(output, "  ……另有 %d 项未显示\n", len(entries)-limit)
	}
	return nil
}

func cleanupCounts(items []update.CleanupCandidate) (automatic, review, recent int) {
	for _, item := range items {
		switch {
		case item.Automatic:
			automatic++
		case item.Recent:
			recent++
		default:
			review++
		}
	}
	return
}

func cleanupStatus(item update.CleanupCandidate) string {
	if item.Automatic {
		return "可安全清理"
	}
	if item.Recent {
		return "暂不处理（可能正在使用）"
	}
	return "需手动确认"
}

func cleanupSize(item update.CleanupCandidate) string {
	if !item.SizeKnown {
		return "未知（包含不可安全检查的内容）"
	}
	return humanBytes(item.Size)
}

func cleanupRead(reader *bufio.Reader, output io.Writer, prompt string) (string, error) {
	fmt.Fprint(output, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if errors.Is(err, io.EOF) && value == "" {
		return "", io.EOF
	}
	return value, nil
}

func cleanupYesNo(reader *bufio.Reader, output, errorOutput io.Writer, prompt string, defaultValue bool) (bool, error) {
	label := "Y/n"
	if !defaultValue {
		label = "y/N"
	}
	for {
		value, err := cleanupRead(reader, output, fmt.Sprintf("%s [%s]: ", prompt, label))
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
			fmt.Fprintln(errorOutput, "错误: 请输入 Y 或 N。")
		}
	}
}

func safeOutput(value string) string {
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
