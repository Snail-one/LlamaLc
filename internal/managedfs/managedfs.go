// Package managedfs provides symlink-safe managed paths and atomic writes.
package managedfs

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Within(root, path string) error {
	r, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(r, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("路径超出受管根目录: %s", path)
	}
	return nil
}

// Validate rejects symlinks in every existing component below root.
func Validate(root, path string, allowMissing bool) error {
	if err := Within(root, path); err != nil {
		return err
	}
	rel, _ := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	current := filepath.Clean(root)
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("受管根目录不能是符号链接: %s", current)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("受管路径不能包含符号链接: %s", current)
		}
	}
	return nil
}

func EnsureDir(root, path string, perm os.FileMode) error {
	if err := Validate(root, filepath.Dir(path), true); err != nil {
		return err
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	// Directory permissions belong to the deployment owner. In particular,
	// replacing a Windows directory DACL here can make user-provided models
	// unreadable when the launcher is later run under a different token.
	// MkdirAll applies perm only while creating missing directories; existing
	// directories and their ACLs must remain untouched.
	return Validate(root, path, false)
}

// Protect reapplies the platform-specific private permissions to an existing
// managed path. On Windows this installs a protected current-user/LocalSystem
// DACL; on Unix it applies the requested mode.
func Protect(root, path string, perm os.FileMode) error {
	if err := Validate(root, path, false); err != nil {
		return err
	}
	return protectPath(path, perm)
}

func AtomicWrite(root, path string, data []byte, perm os.FileMode) error {
	if err := Validate(root, path, true); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := EnsureDir(root, dir, 0o700); err != nil {
		return err
	}
	suffix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, suffix); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%x", filepath.Base(path), suffix))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = protectPath(tmp, perm); err != nil {
		return err
	}
	if err = replaceFile(tmp, path); err != nil {
		return err
	}
	ok = true
	return syncDir(dir)
}

// AtomicCreate durably publishes a new file without ever replacing an
// existing path.  The temporary file is fully written and protected before a
// hard link makes it visible; os.Link is an atomic create-if-absent operation
// on the supported Linux and Windows filesystems.
func AtomicCreate(root, path string, data []byte, perm os.FileMode) error {
	if err := Validate(root, path, true); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := EnsureDir(root, dir, 0o700); err != nil {
		return err
	}
	suffix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, suffix); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%x", filepath.Base(path), suffix))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = protectPath(tmp, perm); err != nil {
		return err
	}
	if err = os.Link(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func CopyFile(root, source, destination string, perm os.FileMode) error {
	if err := Validate(root, destination, true); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("源文件不是普通文件")
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return AtomicWrite(root, destination, data, perm)
}
