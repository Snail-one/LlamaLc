package launcher

import (
	"bytes"
	"testing"
)

func TestClearTerminalIgnoresRedirectedWriter(t *testing.T) {
	var output bytes.Buffer
	if clearTerminal(&output) {
		t.Fatal("non-terminal writer was reported as cleared")
	}
	if output.Len() != 0 {
		t.Fatalf("non-terminal output was modified: %q", output.String())
	}
}
