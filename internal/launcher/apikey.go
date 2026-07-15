package launcher

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
		// Empty is accepted while loading a legacy config. Startup will generate
		// and persist a key before any server command can be constructed.
		return nil
	}
	if len(value) > MaxAPIKeyLength {
		return fmt.Errorf("server.api_key 不能超过 %d 个 ASCII 字符，当前为 %d", MaxAPIKeyLength, len(value))
	}
	if len(value) < MinAPIKeyLength {
		return fmt.Errorf("server.api_key 不能少于 %d 个字符，当前为 %d", MinAPIKeyLength, len(value))
	}
	for _, character := range value {
		if character > unicode.MaxASCII || unicode.IsControl(character) ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' {
			return errors.New("server.api_key 只能包含 ASCII 字母、数字、连字符和下划线")
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

// prepareAPIKey migrates configs without a key and optionally rotates an
// existing key. The returned reader must be passed to the child/menu so bytes
// already buffered while reading the answer are not lost.
func prepareAPIKey(config *Config, configPath string, needsCreate bool, stdin io.Reader, stdout io.Writer) (io.Reader, error) {
	if config.Server.APIKey == "" {
		key, err := GenerateAPIKey()
		if err != nil {
			return stdin, err
		}
		config.Server.APIKey = key
		if !needsCreate {
			if err := WriteConfig(configPath, *config); err != nil {
				return stdin, err
			}
			fmt.Fprintf(stdout, "已自动生成 %d 位 API key 并保存到: %s\n", len(key), configPath)
		} else {
			fmt.Fprintf(stdout, "已自动生成 %d 位 API key，将保存到: %s\n", len(key), configPath)
		}
		printAPIKeyLocation(stdout, configPath)
		return stdin, nil
	}

	reader := bufio.NewReader(stdin)
	reset, err := readStartupYesNo(reader, stdout, "是否重置 API key", false)
	if err != nil {
		return reader, err
	}
	if !reset {
		fmt.Fprintln(stdout, "继续使用配置文件中已生成的 API key。")
		printAPIKeyLocation(stdout, configPath)
		return reader, nil
	}

	key, err := GenerateAPIKey()
	if err != nil {
		return reader, err
	}
	config.Server.APIKey = key
	if err := WriteConfig(configPath, *config); err != nil {
		return reader, err
	}
	fmt.Fprintf(stdout, "已重置 %d 位 API key 并保存到: %s\n", len(key), configPath)
	printAPIKeyLocation(stdout, configPath)
	return reader, nil
}

func printAPIKeyLocation(writer io.Writer, configPath string) {
	fmt.Fprintf(writer, "请在配置文件中查看 API key: %s（字段 server.api_key）\n", configPath)
}

func readStartupYesNo(reader *bufio.Reader, writer io.Writer, prompt string, defaultValue bool) (bool, error) {
	label := "Y/n"
	if !defaultValue {
		label = "y/N"
	}
	for {
		fmt.Fprintf(writer, "%s [%s]: ", prompt, label)
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		switch strings.ToLower(line) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			if errors.Is(err, io.EOF) {
				return false, errors.New("重置 API key 请输入 Y 或 N")
			}
			fmt.Fprintln(writer, "请输入 Y 或 N。")
		}
	}
}
