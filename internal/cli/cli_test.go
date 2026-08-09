package cli

import (
	"bytes"
	"github.com/Snail-one/LlamaLc/internal/config"
	"strings"
	"testing"
)

func TestUsageContainsCompleteV1Tree(t *testing.T) {
	var out, err bytes.Buffer
	a := App{Config: config.Default(), In: strings.NewReader(""), Out: &out, Err: &err}
	if code := a.Run([]string{"help"}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"run api", "run embedding", "run rerank", "run router", "run chat", "config router generate", "config key show|reset", "update check", "update llama", "update launcher", "maintenance cleanup", "version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
func TestHelpAndRemovedAliases(t *testing.T) {
	for _, args := range [][]string{{"run", "api", "--help"}, {"update", "llama", "--help"}, {"maintenance", "cleanup", "--help"}} {
		var out, err bytes.Buffer
		a := App{Config: config.Default(), Out: &out, Err: &err}
		if code := a.Run(args); code != 0 || !strings.Contains(out.String(), "用法:") {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out.String(), err.String())
		}
	}
	for _, old := range []string{"serve", "install", "router-config"} {
		var out, err bytes.Buffer
		a := App{Out: &out, Err: &err}
		if code := a.Run([]string{old}); code != 2 {
			t.Fatalf("%s code=%d", old, code)
		}
	}
}
