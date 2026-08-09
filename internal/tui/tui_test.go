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
	a := App{Reader: input, Out: &out, Err: &err, Root: "/LlamaLc", LauncherVersion: "v1.2.3", Run: func([]string) int { calls++; return 0 }}
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

func TestBackendChoicesAreShownBeforeUpdate(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("3\n2\n\n9\n2\nq\nq\n"))
	var out, stderr bytes.Buffer
	var command []string
	a := App{
		Reader: input, Out: &out, Err: &stderr, Root: "/LlamaLc", LauncherVersion: "dev",
		BackendOptions: func() (string, []string, string, error) { return "b123", []string{"cpu", "vulkan"}, "", nil },
		Run:            func(args []string) int { command = append([]string(nil), args...); return 0 },
	}
	if code := a.RunMenu(); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"llama.cpp Release: b123", "[1] cpu", "[2] vulkan"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if !strings.Contains(stderr.String(), "首次安装必须选择") || !strings.Contains(stderr.String(), "1 到 2") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if got := strings.Join(command, " "); got != "update llama --backend vulkan" {
		t.Fatalf("command=%q", got)
	}
}

func TestBackendEnterKeepsCurrentSelection(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("3\n2\n\nq\nq\n"))
	var out, stderr bytes.Buffer
	var command []string
	a := App{Reader: input, Out: &out, Err: &stderr, BackendOptions: func() (string, []string, string, error) {
		return "b124", []string{"cpu", "vulkan"}, "CPU", nil
	}, Run: func(args []string) int { command = append([]string(nil), args...); return 0 }}
	if code := a.RunMenu(); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "cpu（当前）") {
		t.Fatalf("out=%s", out.String())
	}
	if got := strings.Join(command, " "); got != "update llama --backend cpu" {
		t.Fatalf("command=%q", got)
	}
}
