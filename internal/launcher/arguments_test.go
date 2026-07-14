package launcher

import (
	"reflect"
	"testing"
)

func TestSplitCustomArguments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty", input: "   ", want: nil},
		{name: "plain", input: "--threads 8 --flash-attn on", want: []string{"--threads", "8", "--flash-attn", "on"}},
		{name: "quoted value", input: `--log-prefix "hello world" --name='qwen embed'`, want: []string{"--log-prefix", "hello world", "--name=qwen embed"}},
		{name: "Windows path", input: `--path C:\models\embed.gguf`, want: []string{"--path", `C:\models\embed.gguf`}},
		{name: "empty value", input: `--prefix ""`, want: []string{"--prefix", ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SplitCustomArguments(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments mismatch: got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSplitCustomArgumentsRejectsUnclosedQuote(t *testing.T) {
	if _, err := SplitCustomArguments(`--name "unfinished`); err == nil {
		t.Fatal("unclosed quote was accepted")
	}
}
