package launcher

import (
	"fmt"
	"strings"
	"unicode"
)

// SplitCustomArguments converts one interactive input line into an argument
// array without invoking a shell. Single and double quotes may group values
// containing spaces; ordinary Windows path backslashes are preserved.
func SplitCustomArguments(input string) ([]string, error) {
	runes := []rune(strings.TrimSpace(input))
	var args []string
	var current strings.Builder
	var quote rune
	tokenStarted := false

	flush := func() {
		if tokenStarted {
			args = append(args, current.String())
			current.Reset()
			tokenStarted = false
		}
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote == 0 {
			switch {
			case unicode.IsSpace(r):
				flush()
			case r == '\'' || r == '"':
				quote = r
				tokenStarted = true
			case r == '\\' && i+1 < len(runes) && (unicode.IsSpace(runes[i+1]) || runes[i+1] == '\'' || runes[i+1] == '"'):
				i++
				current.WriteRune(runes[i])
				tokenStarted = true
			default:
				current.WriteRune(r)
				tokenStarted = true
			}
			continue
		}

		if r == quote {
			quote = 0
			continue
		}
		if quote == '"' && r == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
			i++
			current.WriteRune(runes[i])
			continue
		}
		current.WriteRune(r)
	}

	if quote != 0 {
		return nil, fmt.Errorf("自定义参数存在未闭合的 %c 引号", quote)
	}
	flush()
	return args, nil
}
