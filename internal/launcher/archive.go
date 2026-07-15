package launcher

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxExtractedSize  = int64(8 << 30)
	maxArchiveEntries = 20_000
)

type extractBudget struct {
	Bytes   int64
	Entries int
	seen    map[string]bool
}

func newExtractBudget() *extractBudget {
	return &extractBudget{seen: make(map[string]bool)}
}

func (budget *extractBudget) reserve(name string, size int64, directory bool) error {
	if size < 0 || size > maxExtractedSize-budget.Bytes {
		return fmt.Errorf("解压总量超过 8 GiB")
	}
	budget.Entries++
	if budget.Entries > maxArchiveEntries {
		return fmt.Errorf("archive 条目超过 %d", maxArchiveEntries)
	}
	key := strings.ToLower(filepath.Clean(name))
	if budget.seen[key] && !directory {
		return fmt.Errorf("archive 包含重复冲突文件: %s", name)
	}
	budget.seen[key] = true
	budget.Bytes += size
	return nil
}

func safeArchivePath(destination, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") || isWindowsAbs(name) {
		return "", fmt.Errorf("archive 包含绝对或空路径: %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive 路径穿越: %q", name)
	}
	target := filepath.Join(destination, clean)
	relative, err := filepath.Rel(destination, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive 路径穿越: %q", name)
	}
	return target, nil
}

func ExtractArchive(archivePath, destination string, budget *extractBudget, out io.Writer) error {
	if budget == nil {
		budget = newExtractBudget()
	}
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZIP(archivePath, destination, budget, out)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGZ(archivePath, destination, budget, out)
	default:
		return fmt.Errorf("不支持的 archive 格式: %s", filepath.Base(archivePath))
	}
}

func extractZIP(archivePath, destination string, budget *extractBudget, out io.Writer) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return fmt.Errorf("ZIP 含不允许的链接或特殊文件: %s", entry.Name)
		}
		target, err := safeArchivePath(destination, entry.Name)
		if err != nil {
			return err
		}
		if entry.UncompressedSize64 > uint64(maxExtractedSize) {
			return errors.New("ZIP 单项解压大小超过限制")
		}
		if err := budget.reserve(target, int64(entry.UncompressedSize64), entry.FileInfo().IsDir()); err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := extractZIPFile(entry, target, mode); err != nil {
			return err
		}
		if out != nil {
			fmt.Fprintf(out, "\r解压: %d 字节 (%d 项)", budget.Bytes, budget.Entries)
		}
	}
	if out != nil {
		fmt.Fprintln(out)
	}
	return nil
}

func extractZIPFile(entry *zip.File, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != int64(entry.UncompressedSize64) {
		return fmt.Errorf("ZIP 条目大小不符: %s", entry.Name)
	}
	return nil
}

func extractTarGZ(archivePath, destination string, budget *extractBudget, out io.Writer) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		directory := header.Typeflag == tar.TypeDir
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && !directory {
			return fmt.Errorf("TAR 含不允许的链接或特殊文件: %s", header.Name)
		}
		target, err := safeArchivePath(destination, header.Name)
		if err != nil {
			return err
		}
		if err := budget.reserve(target, header.Size, directory); err != nil {
			return err
		}
		if directory {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(header.Mode).Perm()
		if perm == 0 {
			perm = 0o644
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(reader, header.Size+1))
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != header.Size {
			return fmt.Errorf("TAR 条目大小不符: %s", header.Name)
		}
		if out != nil {
			fmt.Fprintf(out, "\r解压: %d 字节 (%d 项)", budget.Bytes, budget.Entries)
		}
	}
	if out != nil {
		fmt.Fprintln(out)
	}
	return nil
}
