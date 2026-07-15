package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateManagedPath(root, path, label string, allowMissing, wantDirectory bool) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%s 路径超出启动器根目录: %s", label, path)
	}

	current := root
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			info, statErr := os.Lstat(current)
			if errors.Is(statErr, os.ErrNotExist) && allowMissing {
				return nil
			}
			if statErr != nil {
				return fmt.Errorf("无法检查%s %s: %w", label, current, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s 不允许使用符号链接或重解析点: %s", label, current)
			}
		}
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("无法检查%s %s: %w", label, path, err)
	}
	if wantDirectory && !info.IsDir() {
		return fmt.Errorf("%s 路径不是目录: %s", label, path)
	}
	if !wantDirectory && !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件: %s", label, path)
	}
	return nil
}

func writeFileExclusive(path string, data []byte, perm os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := applyFilePermissions(temporaryPath, perm); err != nil {
		temporary.Close()
		return err
	}
	if err := writeAndSync(temporary, data); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := applyFilePermissions(temporaryPath, perm); err != nil {
		temporary.Close()
		return err
	}
	if err := writeAndSync(temporary, data); err != nil {
		return err
	}
	return replaceFile(temporaryPath, path)
}

func writeAndSync(file *os.File, data []byte) error {
	written := 0
	for written < len(data) {
		n, err := file.Write(data[written:])
		written += n
		if err != nil {
			file.Close()
			return err
		}
		if n == 0 {
			file.Close()
			return errors.New("写入文件时没有取得进展")
		}
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	// Windows does not permit syncing directory handles; replaceFile provides
	// the strongest primitive available there.
	if err := directory.Sync(); err != nil && os.PathSeparator != '\\' {
		return err
	}
	return nil
}
