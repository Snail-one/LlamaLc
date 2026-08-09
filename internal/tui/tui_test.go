package tui

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestMenuNumbersAndBack(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("1\n0\n2\nq\n3\n0\nq\n"))
	var out, err bytes.Buffer
	calls := 0
	a := App{Reader: input, Out: &out, Err: &err, Root: "/LlamaLc", LauncherVersion: "v1.0.0", Run: func([]string) int { calls++; return 0 }}
	if code := a.RunMenu(); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"[1] 启动", "[2] 配置", "[3] 升级维护", "[0/q] 返回主菜单"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	if calls != 0 {
		t.Fatalf("unexpected calls=%d", calls)
	}
}
