package launcher

import (
	"strings"
	"testing"
)

func TestFormatCommandRedactsAPIKeyAndControlCharacters(t *testing.T) {
	formatted := FormatCommand(Command{
		Path: "server",
		Args: []string{"--api-key", "secret-one", "--api-key=secret-two", "--model", "bad\x1b.gguf"},
	})
	if strings.Contains(formatted, "secret-one") || strings.Contains(formatted, "secret-two") {
		t.Fatalf("API key leaked in command preview: %q", formatted)
	}
	if strings.ContainsRune(formatted, '\x1b') || !strings.Contains(formatted, `\u001B`) {
		t.Fatalf("terminal control character was not escaped: %q", formatted)
	}
}
