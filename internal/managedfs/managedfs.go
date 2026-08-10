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
	"time"
)

var atomicCreateLink = os.Link
var atomicWriteSyncDir = syncDir

const atomicCreateFallbackTimeout = 30 * time.Second
const atomicCreateFallbackStaleAge = 24 * time.Hour

// PublishedError means the new path was made visible, but the containing
// directory could not be durably synchronized. Callers must not assume the old
// value is still installed and blindly roll back dependent data.
type PublishedError struct{ Err error }

func (e *PublishedError) Error() string {
	return "文件已发布但目录同步失败: " + e.Err.Error()
}
func (e *PublishedError) Unwrap() error { return e.Err }

func IsPublishedError(err error) bool {
	var published *PublishedError
	return errors.As(err, &published)
}

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
	if err = atomicWriteSyncDir(dir); err != nil {
		return &PublishedError{Err: err}
	}
	return nil
}

// AtomicCreate durably publishes a new file without ever replacing an
// existing path.  The temporary file is fully written and protected before a
// hard link makes it visible; os.Link is an atomic create-if-absent operation
// on the supported Linux and Windows filesystems. Filesystems without hard-link
// support use a serialized O_EXCL fallback which retains the no-overwrite
// guarantee and does not let another AtomicCreate return while the winner is
// still writing.
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
	if err = atomicCreateLink(tmp, path); err == nil {
		return syncDir(dir)
	} else if errors.Is(err, os.ErrExist) {
		// A fallback writer creates the destination before its contents are
		// complete. Wait for its sidecar lock so callers never validate a
		// partially written concurrent winner.
		if waitErr := waitForAtomicCreateFallback(path); waitErr != nil {
			return waitErr
		}
		return err
	}
	return atomicCreateWithoutLink(path, data, perm)
}

func atomicCreateWithoutLink(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	lock := filepath.Join(dir, "."+filepath.Base(path)+".create-lock")
	deadline := time.Now().Add(atomicCreateFallbackTimeout)
	for {
		err = os.Mkdir(lock, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("创建 AtomicCreate 兼容锁: %w", err)
		}
		if err = waitForAtomicCreateFallbackUntil(lock, deadline); err != nil {
			return err
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			return os.ErrExist
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	defer func() {
		if removeErr := os.Remove(lock); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("释放 AtomicCreate 兼容锁: %w", removeErr)
		}
	}()
	if _, statErr := os.Lstat(path); statErr == nil {
		return os.ErrExist
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	createdInfo, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return statErr
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		current, statErr := os.Lstat(path)
		if statErr == nil && os.SameFile(createdInfo, current) {
			_ = os.Remove(path)
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
	if err = protectPath(path, perm); err != nil {
		return err
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	complete = true
	return nil
}

func waitForAtomicCreateFallback(path string) error {
	lock := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".create-lock")
	return waitForAtomicCreateFallbackUntil(lock, time.Now().Add(atomicCreateFallbackTimeout))
}

func waitForAtomicCreateFallbackUntil(lock string, deadline time.Time) error {
	for {
		info, err := os.Lstat(lock)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("AtomicCreate 兼容锁不是普通目录")
		}
		if time.Now().After(deadline) {
			if time.Since(info.ModTime()) >= atomicCreateFallbackStaleAge {
				entries, readErr := os.ReadDir(lock)
				if readErr != nil {
					return readErr
				}
				if len(entries) == 0 {
					if removeErr := os.Remove(lock); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
						return nil
					}
				}
			}
			return errors.New("等待另一个 AtomicCreate 完成超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
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
