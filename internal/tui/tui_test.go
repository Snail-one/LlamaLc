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
	input := bufio.NewReader(strings.NewReader("3\n1\n\n9\n2\nq\nq\n"))
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
	input := bufio.NewReader(strings.NewReader("3\n1\n\nq\nq\n"))
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

func TestModelChoicesMatchV015MenuBehavior(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("1\n1\n\nq\nq\n"))
	var out, stderr bytes.Buffer
	var command []string
	a := App{
		Reader: input, Out: &out, Err: &stderr, Root: "/LlamaLc", LauncherVersion: "dev",
		ModelOptions: func(kind string) (string, []ModelOption, error) {
			if kind != "generation" {
				t.Fatalf("kind=%q", kind)
			}
			return "/LlamaLc/models/llm", []ModelOption{
				{ID: "A.gguf", Path: "/LlamaLc/models/llm/A.gguf", Size: 2 << 30},
				{ID: "B.gguf", Path: "/LlamaLc/models/llm/B.gguf", Size: 512 << 20},
			}, nil
		},
		Run: func(args []string) int { command = append([]string(nil), args...); return 0 },
	}
	if code := a.RunMenu(); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"选择模型", "目录: /LlamaLc/models/llm", "1. A.gguf  (2.00 GB)", "2. B.gguf  (512.00 MB)", "[0/q] 返回主菜单"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if got := strings.Join(command, " "); got != "run api --model /LlamaLc/models/llm/A.gguf" {
		t.Fatalf("command=%q", got)
	}
}

func TestEachLaunchModeUsesItsModelDirectory(t *testing.T) {
	tests := []struct {
		choice, kind, mode string
	}{
		{"1", "generation", "api"},
		{"2", "embedding", "embedding"},
		{"3", "rerank", "rerank"},
		{"5", "generation", "chat"},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			input := bufio.NewReader(strings.NewReader("1\n" + test.choice + "\n1\nq\nq\n"))
			var command []string
			a := App{Reader: input, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, ModelOptions: func(kind string) (string, []ModelOption, error) {
				if kind != test.kind {
					t.Fatalf("kind=%q, want %q", kind, test.kind)
				}
				return "/models/" + kind, []ModelOption{{ID: "model.gguf", Path: "/models/" + kind + "/model.gguf", Size: 1}}, nil
			}, Run: func(args []string) int { command = append([]string(nil), args...); return 0 }}
			if code := a.RunMenu(); code != 0 {
				t.Fatal(code)
			}
			if got := strings.Join(command, " "); got != "run "+test.mode+" --model /models/"+test.kind+"/model.gguf" {
				t.Fatalf("command=%q", got)
			}
		})
	}
}

func TestLauncherUpdateWaitsInsteadOfFlashingClosed(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("3\n2\n\n"))
	var out bytes.Buffer
	a := App{Reader: input, Out: &out, Err: &bytes.Buffer{}, Run: func(args []string) int {
		if got := strings.Join(args, " "); got != "update launcher" {
			t.Fatalf("command=%q", got)
		}
		return 0
	}}
	if code := a.RunMenu(); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"操作完成。", "按 Enter 退出当前程序", "更新完成后将自动启动新版本"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}

func TestV015LaunchWizardRestoresParametersAndConfirmation(t *testing.T) {
	input := strings.Join([]string{
		"", "16", "32", // auto-matched mmproj and image tokens
		"", "", "", "", "", "", "", // runtime defaults
		"", "", "", // network defaults
		`--jinja --chat-template "my template"`, "", // custom and confirmation
	}, "\n") + "\n"
	var out, stderr bytes.Buffer
	a := App{
		Reader: bufio.NewReader(strings.NewReader(input)), Out: &out, Err: &stderr, LaunchWizard: true,
		Defaults: LaunchDefaults{GPULayers: "auto", ContextSize: 0, Threads: -1, BatchSize: 2048, UBatchSize: 512, FlashAttention: "auto", Parallel: -1, Host: "127.0.0.1", Port: 29856},
		ModelOptions: func(kind string) (string, []ModelOption, error) {
			if kind != "mmproj" {
				t.Fatalf("kind=%q", kind)
			}
			return "/models/mmproj", []ModelOption{{ID: "mmproj-Qwen-VL.gguf", Path: "/models/mmproj/mmproj-Qwen-VL.gguf"}}, nil
		},
	}
	command, err := a.configureLaunch("api", []string{"run", "api", "--model", "/models/generation/Qwen-VL.gguf"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command, " ")
	for _, want := range []string{"--mmproj /models/mmproj/mmproj-Qwen-VL.gguf", "--image-min-tokens 16", "--image-max-tokens 32", "--context-size 0", "--gpu-layers auto", "--threads -1", "--batch-size 2048", "--ubatch-size 512", "--flash-attention auto", "--parallel -1", "--host 127.0.0.1", "--port 29856", "--ui=false", "-- --jinja --chat-template my template"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, want := range []string{"选择 mmproj", "[自动匹配]", "运行参数", "网络参数", "高级选项"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestMaintenanceModeInstallsThenEntersMainMenu(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("1\n1\nq\n"))
	var out bytes.Buffer
	var command []string
	a := App{
		Reader: input, Out: &out, Err: &bytes.Buffer{}, Root: "/LlamaLc", ClassicInteraction: true,
		BackendOptions:      func() (string, []string, string, error) { return "b123", []string{"cpu"}, "", nil },
		Run:                 func(args []string) int { command = append([]string(nil), args...); return 0 },
		RefreshLlamaVersion: func() string { return "b123 / cpu" },
	}
	if code := a.RunMenu(); code != 0 {
		t.Fatal(code)
	}
	if got := strings.Join(command, " "); got != "update llama --backend cpu --reinstall" {
		t.Fatalf("command=%q", got)
	}
	for _, want := range []string{"LlamaLc 维护模式", "[1] 安装 llama.cpp", "安装完成，正在进入主菜单", "b123 / cpu"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}

func TestV015DefaultMenuChoicesAndAPIKeyCancellation(t *testing.T) {
	t.Run("default launch", func(t *testing.T) {
		input := bufio.NewReader(strings.NewReader("\n\n1\n\nq\n"))
		var command []string
		a := App{
			Reader: input, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, RuntimeInstalled: true, ClassicInteraction: true,
			ModelOptions: func(string) (string, []ModelOption, error) {
				return "/models", []ModelOption{{ID: "model.gguf", Path: "/models/model.gguf"}}, nil
			},
			Run: func(args []string) int { command = append([]string(nil), args...); return 0 },
		}
		if code := a.RunMenu(); code != 0 {
			t.Fatal(code)
		}
		if got := strings.Join(command, " "); got != "run api --model /models/model.gguf" {
			t.Fatalf("command=%q", got)
		}
	})
	t.Run("key confirmation", func(t *testing.T) {
		input := bufio.NewReader(strings.NewReader("2\n3\nn\n\nq\n"))
		var out bytes.Buffer
		calls := 0
		a := App{Reader: input, Out: &out, Err: &bytes.Buffer{}, RuntimeInstalled: true, ClassicInteraction: true, Run: func([]string) int { calls++; return 0 }}
		if code := a.RunMenu(); code != 0 {
			t.Fatal(code)
		}
		if calls != 0 || !strings.Contains(out.String(), "终端未共享或录屏") || !strings.Contains(out.String(), "已取消，未显示 API key") {
			t.Fatalf("calls=%d out=%s", calls, out.String())
		}
	})
}
