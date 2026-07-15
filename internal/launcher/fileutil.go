package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	managedTempMarkerName = ".llamalc-managed-temp"
	managedTempMarkerData = "llama-launcher managed temporary directory\n"
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

func markManagedTempDirectory(parent, path string) error {
	parent = filepath.Clean(parent)
	path = filepath.Clean(path)
	if filepath.Dir(path) != parent {
		return fmt.Errorf("临时目录不是受管父目录的直接子目录: %s", path)
	}
	if err := validateManagedPath(parent, path, "受管临时目录", false, true); err != nil {
		return err
	}
	marker := filepath.Join(path, managedTempMarkerName)
	if err := writeFileExclusive(marker, []byte(managedTempMarkerData), 0o600); err != nil {
		return fmt.Errorf("无法标记受管临时目录 %s: %w", path, err)
	}
	return nil
}

func removeMarkedTempDirectory(parent, path string) error {
	parent = filepath.Clean(parent)
	path = filepath.Clean(path)
	if filepath.Dir(path) != parent {
		return fmt.Errorf("拒绝清理非直接子目录: %s", path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝清理不是普通目录的路径: %s", path)
	}
	if err := validateManagedPath(parent, path, "受管临时目录", false, true); err != nil {
		return err
	}
	marker := filepath.Join(path, managedTempMarkerName)
	markerInfo, err := os.Lstat(marker)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝清理没有有效所有权标记的目录: %s", path)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != managedTempMarkerData {
		return fmt.Errorf("拒绝清理所有权标记无效的目录: %s", path)
	}
	if err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("临时目录包含符号链接或重解析点: %s", current)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("临时目录包含特殊文件: %s", current)
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func numericTempSuffix(name, prefix, suffix string) bool {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(strings.ToLower(name), strings.ToLower(suffix)) {
		return false
	}
	end := len(name) - len(suffix)
	value := name[len(prefix):end]
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func cleanupAtomicWriteTemps(root string, stderr io.Writer) {
	directory := filepath.Join(root, ConfigDirectoryName)
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := validateManagedPath(root, directory, "配置目录", false, true); err != nil {
		fmt.Fprintf(stderr, "警告: 无法检查配置写入残留: %v\n", err)
		return
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		fmt.Fprintf(stderr, "警告: 无法扫描配置写入残留: %v\n", err)
		return
	}
	prefixes := []string{
		"." + DefaultConfigName + ".tmp-",
		"." + DefaultAPIKeyName + ".tmp-",
		"." + UpdateStateName + ".tmp-",
		".router-models.ini.tmp-",
		".router-models.auto.ini.tmp-",
	}
	for _, entry := range entries {
		managed := false
		for _, prefix := range prefixes {
			if numericTempSuffix(entry.Name(), prefix, "") {
				managed = true
				break
			}
		}
		if !managed {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(stderr, "警告: 拒绝清理不是普通文件的配置写入残留: %s\n", path)
			continue
		}
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(stderr, "警告: 无法清理配置写入残留 %s: %v\n", path, err)
		}
	}
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
