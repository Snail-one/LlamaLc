// Package models scans categorized model directories and writes router presets.
package models

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	stem := strings.ToLower(strings.TrimSuffix(model.ID, filepath.Ext(model.ID)))
	best := -1
	score := 0
	for i, p := range projectors {
		s := commonPrefix(stem, strings.ToLower(strings.TrimSuffix(p.ID, filepath.Ext(p.ID))))
		if s > score {
			score = s
			best = i
		}
	}
	if best < 0 || score < 3 {
		return nil
	}
	p := projectors[best]
	return &p
}
func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func WriteRouterPreset(l layout.Layout, path string, files []File) error {
	if path == "" {
		path = l.RouterPreset
	}
	if err := managedfs.Within(l.Root, path); err != nil {
		return err
	}
	var b strings.Builder
	w := bufio.NewWriter(&b)
	for _, f := range files {
		if f.Kind != Generation {
			continue
		}
		fmt.Fprintf(w, "[%s]\nmodel = %s\n\n", sanitizeID(f.ID), f.Path)
	}
	_ = w.Flush()
	if b.Len() == 0 {
		return errors.New("没有可写入 Router preset 的生成模型")
	}
	return managedfs.AtomicWrite(l.Root, path, []byte(b.String()), 0o600)
}
func sanitizeID(v string) string {
	v = strings.TrimSuffix(v, filepath.Ext(v))
	var b strings.Builder
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
