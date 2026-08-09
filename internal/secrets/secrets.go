// Package secrets creates and protects the managed API key.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

func Generate() (string, error) {
	// 96 random bytes encode to the configured 128 URL-safe characters.
	b := make([]byte, 96)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func Read(l layout.Layout) (string, error) {
	if err := managedfs.Validate(l.Root, l.APIKeyFile, false); err != nil {
		return "", err
	}
	f, err := os.Open(l.APIKeyFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("API key 不是普通文件")
	}
	// 8167 key characters plus an optional CRLF terminator.
	if info.Size() > 8169 {
		return "", errors.New("API key 文件过大")
	}
	if err := f.Chmod(0o600); err != nil {
		return "", fmt.Errorf("保护 API key 权限: %w", err)
	}
	if err := managedfs.Protect(l.Root, l.APIKeyFile, 0o600); err != nil {
		return "", fmt.Errorf("保护 API key ACL: %w", err)
	}
	b, err := io.ReadAll(io.LimitReader(f, 8170))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if len(key) < 32 || len(key) > 8167 {
		return "", errors.New("API key 无效")
	}
	raw := string(b)
	if (raw != key && raw != key+"\n" && raw != key+"\r\n") || strings.ContainsAny(key, "\r\n\t ") {
		return "", errors.New("API key 包含空白或多行内容")
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", errors.New("API key 只能包含 URL-safe 字符")
		}
	}
	return key, nil
}

func Reset(l layout.Layout) (string, error) {
	key, err := Generate()
	if err != nil {
		return "", err
	}
	if err = managedfs.AtomicWrite(l.Root, l.APIKeyFile, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}
func Ensure(l layout.Layout) (string, bool, error) {
	key, err := Read(l)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	key, err = Reset(l)
	return key, true, err
}
