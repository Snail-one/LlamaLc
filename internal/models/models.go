// Package models scans categorized model directories and writes router presets.
package models

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

type Kind string

const (
	Generation Kind = "generation"
	Embedding  Kind = "embedding"
	Rerank     Kind = "rerank"
	MMProj     Kind = "mmproj"
)

type File struct {
	ID, Path string
	Kind     Kind
	Size     int64
}

type PresetOptions struct {
	GPULayers         string
	ContextSize       int
	Pooling           string
	BatchSize         int
	UBatchSize        int
	DisableMMProjAuto bool
	Manual            bool
	CreateOnly        bool
}

func Directory(l layout.Layout, kind Kind) (string, error) {
	switch kind {
	case Generation:
		return l.GenerationModels, nil
	case Embedding:
		return l.EmbeddingModels, nil
	case Rerank:
		return l.RerankModels, nil
	case MMProj:
		return l.MMProjModels, nil
	default:
		return "", fmt.Errorf("未知模型类型 %q", kind)
	}
}

func Scan(l layout.Layout, kind Kind) ([]File, error) {
	root, err := Directory(l, kind)
	if err != nil {
		return nil, err
	}
	var result []File
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if e.IsDir() {
			return nil
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("模型文件不允许符号链接: %s", path)
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".gguf" && !(kind == Generation && (ext == ".bin" || ext == ".ggml")) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("模型不是普通文件: %s", path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		result = append(result, File{ID: e.Name(), Path: abs, Kind: kind, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 %s: %w", root, err)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := strings.ToLower(result[i].ID), strings.ToLower(result[j].ID)
		if a == b {
			return result[i].Path < result[j].Path
		}
		return a < b
	})
	return result, nil
}

func Resolve(l layout.Layout, kind Kind, input string) (File, error) {
	input = strings.TrimSpace(strings.Trim(input, `"'`))
	if input == "" {
		return File{}, errors.New("必须使用 --model 指定模型")
	}
	if filepath.IsAbs(input) || strings.ContainsAny(input, `/\\`) {
		path := input
		if !filepath.IsAbs(path) {
			path = filepath.Join(l.Root, filepath.FromSlash(path))
		}
		return fromPath(path, kind)
	}
	files, err := Scan(l, kind)
	if err != nil {
		return File{}, err
	}
	var found []File
	for _, f := range files {
		if strings.EqualFold(f.ID, input) {
			found = append(found, f)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return File{}, fmt.Errorf("模型名 %q 不唯一，请使用完整路径", input)
	}
	return File{}, fmt.Errorf("在 %s 中找不到模型 %q", l.ModelsDir, input)
}

func fromPath(path string, kind Kind) (File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return File{}, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return File{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return File{}, errors.New("模型必须是普通文件且不能是符号链接")
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if ext != ".gguf" && !(kind == Generation && (ext == ".bin" || ext == ".ggml")) {
		return File{}, fmt.Errorf("模型扩展名无效: %s", ext)
	}
	return File{ID: filepath.Base(abs), Path: abs, Kind: kind, Size: info.Size()}, nil
}

func MatchProjector(model File, projectors []File) *File {
	modelTokens := matchTokens(model.ID)
	best := -1
	bestScore := 0
	for i, p := range projectors {
		score := commonTokenPrefix(modelTokens, matchTokens(p.ID))
		if score > bestScore {
			bestScore = score
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	p := projectors[best]
	return &p
}

func CollectRouterModels(l layout.Layout) ([]File, []File, error) {
	var all []File
	for _, kind := range []Kind{Generation, Embedding, Rerank} {
		files, err := Scan(l, kind)
		if err != nil {
			return nil, nil, err
		}
		for _, file := range files {
			// Router model IDs and presets are defined only for GGUF. Generation
			// .bin/.ggml files remain usable by single-model API/chat modes.
			if strings.EqualFold(filepath.Ext(file.Path), ".gguf") {
				all = append(all, file)
			}
		}
	}
	projectors, err := Scan(l, MMProj)
	if err != nil {
		return nil, nil, err
	}
	if len(all) == 0 {
		return nil, projectors, errors.New("llm、embedding 和 rerank 目录中没有可用于 Router 的 .gguf 模型")
	}
	if err := CheckModelIDConflicts(all); err != nil {
		return nil, nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := strings.ToLower(all[i].ID), strings.ToLower(all[j].ID)
		if a == b {
			return all[i].Path < all[j].Path
		}
		return a < b
	})
	return all, projectors, nil
}

func CheckModelIDConflicts(files []File) error {
	byID := make(map[string][]File)
	for _, file := range files {
		byID[strings.ToLower(file.ID)] = append(byID[strings.ToLower(file.ID)], file)
	}
	var keys []string
	for key, matches := range byID {
		if len(matches) > 1 {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		matches := byID[key]
		lines = append(lines, matches[0].ID+":")
		for _, match := range matches {
			lines = append(lines, fmt.Sprintf("  - [%s] %s", match.Kind, match.Path))
		}
	}
	return fmt.Errorf("Router API model id 使用文件名，发现同名冲突，请重命名后重试:\n%s", strings.Join(lines, "\n"))
}
func matchTokens(name string) []string {
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

func WriteRouterPreset(l layout.Layout, path string, files []File) error {
	return WriteRouterPresetWithOptions(l, path, files, nil, PresetOptions{})
}

func WriteRouterPresetWithOptions(l layout.Layout, path string, files, projectors []File, options PresetOptions) error {
	if path == "" {
		path = l.RouterPreset
	}
	if err := managedfs.Within(l.Root, path); err != nil {
		return err
	}
	var b strings.Builder
	w := bufio.NewWriter(&b)
	fmt.Fprintln(w, "; Generated by LlamaLc. Edit config/router/models.ini only.")
	fmt.Fprintln(w, "; API model ids are the GGUF filenames.")
	fmt.Fprintln(w, "version = 1")
	fmt.Fprintln(w)
	written := 0
	for _, f := range files {
		if f.Kind != Generation && f.Kind != Embedding && f.Kind != Rerank {
			continue
		}
		if err := validatePresetField(f.ID, true); err != nil {
			return fmt.Errorf("模型文件名不能安全写入 Router preset %q: %w", f.ID, err)
		}
		if err := validatePresetField(f.Path, false); err != nil {
			return fmt.Errorf("模型路径不能安全写入 Router preset %q: %w", f.Path, err)
		}
		fmt.Fprintf(w, "[%s]\nmodel = %s\n", f.ID, f.Path)
		if options.GPULayers != "" {
			fmt.Fprintf(w, "n-gpu-layers = %s\n", options.GPULayers)
		}
		if options.ContextSize > 0 {
			fmt.Fprintf(w, "ctx-size = %d\n", options.ContextSize)
		} else if options.Manual {
			fmt.Fprintln(w, "; ctx-size = 8192")
		}
		fmt.Fprintln(w, "load-on-startup = false")
		switch f.Kind {
		case Generation:
			if projector := MatchProjector(f, projectors); projector != nil && !options.DisableMMProjAuto {
				if err := validatePresetField(projector.Path, false); err != nil {
					return fmt.Errorf("mmproj 路径不能安全写入 Router preset %q: %w", projector.Path, err)
				}
				fmt.Fprintf(w, "mmproj = %s\n", projector.Path)
			} else if options.Manual {
				fmt.Fprintln(w, "; mmproj = /path/to/mmproj.gguf")
			}
		case Embedding:
			fmt.Fprintln(w, "embedding = true")
			if options.Pooling != "" {
				fmt.Fprintf(w, "pooling = %s\n", options.Pooling)
			}
			if options.BatchSize > 0 {
				fmt.Fprintf(w, "batch-size = %d\n", options.BatchSize)
			}
			if options.UBatchSize > 0 {
				fmt.Fprintf(w, "ubatch-size = %d\n", options.UBatchSize)
			}
		case Rerank:
			fmt.Fprintln(w, "reranking = true")
		}
		fmt.Fprintln(w)
		written++
	}
	_ = w.Flush()
	if written == 0 {
		return errors.New("没有可写入 Router preset 的模型")
	}
	if options.CreateOnly {
		if err := managedfs.AtomicCreate(l.Root, path, []byte(b.String()), 0o600); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			if validateErr := validateExistingPreset(path); validateErr != nil {
				return fmt.Errorf("Router preset 被并发创建且胜出文件无效: %w", validateErr)
			}
			return fmt.Errorf("Router preset 已被另一个进程创建，未覆盖: %w", os.ErrExist)
		}
		return nil
	}
	return managedfs.AtomicWrite(l.Root, path, []byte(b.String()), 0o600)
}

func validateExistingPreset(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return errors.New("必须是小于 1 MiB 的普通文件且不能是符号链接")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > 1<<20 || !utf8.Valid(data) {
		return errors.New("内容为空、过大或不是有效 UTF-8")
	}
	return nil
}

func validatePresetField(value string, section bool) error {
	if value == "" || !utf8.ValidString(value) {
		return errors.New("内容为空或不是有效 UTF-8")
	}
	if section && (strings.TrimSpace(value) != value || strings.ContainsAny(value, "[]")) {
		return errors.New("section 名不能包含首尾空白、[ 或 ]")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("包含控制字符 U+%04X", character)
		}
	}
	return nil
}
