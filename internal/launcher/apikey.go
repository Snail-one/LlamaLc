package launcher

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// MaxAPIKeyLength is the longest ASCII key that fits in llama.cpp's default
// 8192-byte cpp-httplib header buffer as "Authorization: Bearer <key>\r\n",
// including the terminating NUL byte used by its line reader.
const MaxAPIKeyLength = 8167

// MinAPIKeyLength rejects accidentally short manually configured credentials.
// Generated keys are substantially longer than this minimum.
const MinAPIKeyLength = 32

// GeneratedAPIKeyLength keeps generated credentials practical for clients and
// configuration while still providing far more entropy than normally needed.
const GeneratedAPIKeyLength = 128

func ValidateAPIKey(value string) error {
	if value == "" {
		return errors.New("API key 不能为空")
	}
	if len(value) > MaxAPIKeyLength {
		return fmt.Errorf("API key 不能超过 %d 个 ASCII 字符，当前为 %d", MaxAPIKeyLength, len(value))
	}
	if len(value) < MinAPIKeyLength {
		return fmt.Errorf("API key 不能少于 %d 个字符，当前为 %d", MinAPIKeyLength, len(value))
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' {
			return errors.New("API key 只能包含 ASCII 字母、数字、连字符和下划线")
		}
	}
	return nil
}

func WriteAPIKeyFile(root, path, key string) error {
	if key == "" {
		return errors.New("拒绝写入空 API key")
	}
	if err := ValidateAPIKey(key); err != nil {
		return err
	}
	if err := validateManagedPath(root, path, "API key 文件", true, false); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("无法原子更新 API key 文件 %s: %w", path, err)
	}
	return nil
}

func ReadAPIKeyFile(root, path string) (string, bool, error) {
	if err := validateManagedPath(root, path, "API key 文件", true, false); err != nil {
		return "", false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("无法访问 API key 文件 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("API key 文件不是普通文件: %s", path)
	}
	if info.Size() > MaxAPIKeyLength+2 {
		return "", false, fmt.Errorf("API key 文件过大: %s", path)
	}
	if err := applyFilePermissions(path, 0o600); err != nil {
		return "", false, fmt.Errorf("无法保护 API key 文件权限 %s: %w", path, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("无法读取 API key 文件 %s: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxAPIKeyLength+3))
	if err != nil {
		return "", false, fmt.Errorf("无法读取 API key 文件 %s: %w", path, err)
	}
	if len(data) > MaxAPIKeyLength+2 {
		return "", false, fmt.Errorf("API key 文件过大: %s", path)
	}
	key := strings.TrimSuffix(string(data), "\n")
	key = strings.TrimSuffix(key, "\r")
	if key == "" {
		return "", false, errors.New("API key 文件不能为空")
	}
	if err := ValidateAPIKey(key); err != nil {
		return "", false, fmt.Errorf("API key 文件无效: %w", err)
	}
	return key, true, nil
}

func GenerateAPIKey() (string, error) {
	// Raw URL encoding uses only printable ASCII, has no comma separator, and
	// can be copied directly into Authorization headers without quoting.
	byteCount := (GeneratedAPIKeyLength*6 + 7) / 8
	randomBytes := make([]byte, byteCount)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return "", fmt.Errorf("无法生成安全随机 API key: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(randomBytes)
	return encoded[:GeneratedAPIKeyLength], nil
}

// ensureAPIKey validates the private API key file and creates it on first use.
func ensureAPIKey(root, keyPath string, stdout io.Writer) error {
	_, exists, err := ReadAPIKeyFile(root, keyPath)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	key, err := GenerateAPIKey()
	if err != nil {
		return err
	}
	if err := WriteAPIKeyFile(root, keyPath, key); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已自动生成 %d 位 API key 并保存到: %s\n", len(key), keyPath)
	printAPIKeyLocation(stdout, keyPath)
	return nil
}

func resetAPIKey(root, keyPath string, stdout io.Writer) error {
	key, err := GenerateAPIKey()
	if err != nil {
		return err
	}
	if err := WriteAPIKeyFile(root, keyPath, key); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已重置 %d 位 API key 并保存到: %s\n", len(key), keyPath)
	printAPIKeyLocation(stdout, keyPath)
	return nil
}

func printAPIKeyLocation(writer io.Writer, keyPath string) {
	fmt.Fprintf(writer, "API key 文件: %s\n", keyPath)
}
