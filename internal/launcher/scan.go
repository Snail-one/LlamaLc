package launcher

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type ModelKind string

const (
	GenerationModel ModelKind = "generation"
	EmbeddingModel  ModelKind = "embedding"
	RerankModel     ModelKind = "rerank"
)

type ModelFile struct {
	ID   string
	Path string
	Kind ModelKind
	Size int64
}

var generationExtensions = map[string]bool{".gguf": true, ".bin": true, ".ggml": true}
var ggufExtension = map[string]bool{".gguf": true}

func ScanModels(root string, kind ModelKind, extensions map[string]bool) ([]ModelFile, error) {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法访问模型目录 %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("模型路径不是目录: %s", root)
	}

	var models []ModelFile
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !extensions[ext] {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("模型文件不允许使用符号链接: %s", path)
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("模型不是普通文件: %s", path)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		models = append(models, ModelFile{ID: entry.Name(), Path: filepath.Clean(absolute), Kind: kind, Size: fileInfo.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描模型目录 %s 失败: %w", root, err)
	}
	sortModels(models)
	return models, nil
}

func sortModels(models []ModelFile) {
	sort.Slice(models, func(i, j int) bool {
		left, right := strings.ToLower(models[i].ID), strings.ToLower(models[j].ID)
		if left != right {
			return left < right
		}
		return strings.ToLower(models[i].Path) < strings.ToLower(models[j].Path)
	})
}

func ResolveModel(root, input string, kind ModelKind, extensions map[string]bool) (ModelFile, error) {
	return ResolveModelAt(root, root, input, kind, extensions)
}

// ResolveModelAt searches bare filenames in searchRoot first, allowing callers
// to pass an API model ID such as "model.gguf". If no scanned model has that ID,
// the same input is treated as a path relative to pathRoot. Inputs containing a
// path separator always use pathRoot directly.
func ResolveModelAt(searchRoot, pathRoot, input string, kind ModelKind, extensions map[string]bool) (ModelFile, error) {
	input = strings.TrimSpace(strings.Trim(input, `"'`))
	if input == "" {
		return ModelFile{}, errors.New("必须使用 --model 指定模型文件名或路径")
	}
	if filepath.IsAbs(input) || isWindowsAbs(input) || strings.ContainsAny(input, `/\\`) {
		path := ResolvePath(pathRoot, input)
		return modelFromPath(path, kind, extensions)
	}
	models, err := ScanModels(searchRoot, kind, extensions)
	if err != nil {
		return ModelFile{}, err
	}
	var matches []ModelFile
	for _, model := range models {
		if strings.EqualFold(model.ID, input) {
			matches = append(matches, model)
		}
	}
	if len(matches) == 0 {
		// A bare filename is ambiguous: it can be a scanned model ID or a path
		// relative to the launcher root. Prefer the categorized model directory,
		// then fall back to the documented root-relative path behavior.
		rootRelativePath := ResolvePath(pathRoot, input)
		if _, statErr := os.Stat(rootRelativePath); statErr == nil {
			return modelFromPath(rootRelativePath, kind, extensions)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return ModelFile{}, fmt.Errorf("无法访问模型文件 %s: %w", rootRelativePath, statErr)
		}
		return ModelFile{}, fmt.Errorf("在 %s 中找不到模型 %q", searchRoot, input)
	}
	if len(matches) > 1 {
		paths := make([]string, 0, len(matches))
		for _, match := range matches {
			paths = append(paths, match.Path)
		}
		return ModelFile{}, fmt.Errorf("模型文件名 %q 不唯一，请使用完整路径:\n  - %s", input, strings.Join(paths, "\n  - "))
	}
	return matches[0], nil
}

func modelFromPath(path string, kind ModelKind, extensions map[string]bool) (ModelFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ModelFile{}, fmt.Errorf("找不到模型文件: %s", path)
		}
		return ModelFile{}, fmt.Errorf("无法访问模型文件 %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ModelFile{}, fmt.Errorf("模型文件不允许使用符号链接: %s", path)
	}
	if !info.Mode().IsRegular() {
		return ModelFile{}, fmt.Errorf("模型必须是普通文件: %s", path)
	}
	if !extensions[strings.ToLower(filepath.Ext(path))] {
		return ModelFile{}, fmt.Errorf("不支持的模型扩展名: %s", filepath.Ext(path))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ModelFile{}, err
	}
	return ModelFile{ID: filepath.Base(path), Path: filepath.Clean(absolute), Kind: kind, Size: info.Size()}, nil
}

func FindMatchingMmproj(model ModelFile, projectors []ModelFile) *ModelFile {
	modelTokens := matchTokens(model.ID)
	bestScore := 0
	bestIndex := -1
	for i := range projectors {
		projectorTokens := matchTokens(projectors[i].ID)
		score := commonPrefix(modelTokens, projectorTokens)
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	if bestIndex < 0 {
		return nil
	}
	matched := projectors[bestIndex]
	return &matched
}

func matchTokens(name string) []string {
	stem := strings.TrimSuffix(strings.ToLower(name), strings.ToLower(filepath.Ext(name)))
	parts := strings.FieldsFunc(stem, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	filtered := parts[:0]
	for _, part := range parts {
		if part == "mmproj" || part == "projector" {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

func commonPrefix(left, right []string) int {
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
