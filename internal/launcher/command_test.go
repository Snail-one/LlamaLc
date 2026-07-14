package launcher

import (
	"reflect"
	"testing"
)

func TestBuildCommands(t *testing.T) {
	tests := []struct {
		name  string
		build func() (Command, error)
		want  []string
	}{
		{
			name: "serve",
			build: func() (Command, error) {
				return BuildServerCommand(ModeServe, "server.exe", `C:\root`, ServerOptions{Model: "m.gguf", Host: "127.0.0.1", Port: 29856, GPULayers: "auto"})
			},
			want: []string{"--model", "m.gguf", "--n-gpu-layers", "auto", "--host", "127.0.0.1", "--port", "29856", "--no-ui"},
		},
		{
			name: "serve multimodal UI and extra",
			build: func() (Command, error) {
				return BuildServerCommand(ModeServe, "server.exe", ".", ServerOptions{Model: "m.gguf", Mmproj: "p.gguf", ImageMinTokens: 1024, Host: "0.0.0.0", Port: 9000, GPULayers: "all", ContextSize: 8192, UI: true, Extra: []string{"--threads", "8"}})
			},
			want: []string{"--model", "m.gguf", "--ctx-size", "8192", "--n-gpu-layers", "all", "--mmproj", "p.gguf", "--image-min-tokens", "1024", "--host", "0.0.0.0", "--port", "9000", "--ui", "--threads", "8"},
		},
		{
			name: "embedding",
			build: func() (Command, error) {
				return BuildServerCommand(ModeEmbedding, "server.exe", ".", ServerOptions{Model: "e.gguf", Host: "localhost", Port: 1, GPULayers: "0", Pooling: "mean", UBatchSize: 8192})
			},
			want: []string{"--model", "e.gguf", "--n-gpu-layers", "0", "--embedding", "--pooling", "mean", "--ubatch-size", "8192", "--host", "localhost", "--port", "1", "--no-ui"},
		},
		{
			name: "rerank",
			build: func() (Command, error) {
				return BuildServerCommand(ModeRerank, "server.exe", ".", ServerOptions{Model: "r.gguf", Host: "localhost", Port: 2, GPULayers: "auto"})
			},
			want: []string{"--model", "r.gguf", "--n-gpu-layers", "auto", "--reranking", "--host", "localhost", "--port", "2", "--no-ui"},
		},
		{
			name: "router",
			build: func() (Command, error) {
				return BuildRouterCommand("server.exe", ".", RouterOptions{Preset: "router.ini", Host: "127.0.0.1", Port: 3, GPULayers: "auto", ModelsMax: 1, Autoload: true})
			},
			want: []string{"--models-preset", "router.ini", "--models-max", "1", "--models-autoload", "--n-gpu-layers", "auto", "--host", "127.0.0.1", "--port", "3", "--no-ui"},
		},
		{
			name: "chat",
			build: func() (Command, error) {
				return BuildChatCommand("cli.exe", ".", ServerOptions{Model: "m.bin", GPULayers: "auto", ContextSize: 4096, Extra: []string{"--temp", "0.7"}})
			},
			want: []string{"--model", "m.bin", "--ctx-size", "4096", "--n-gpu-layers", "auto", "-cnv", "--temp", "0.7"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(command.Args, test.want) {
				t.Fatalf("args mismatch\n got: %#v\nwant: %#v", command.Args, test.want)
			}
		})
	}
}

func TestBuildEmbeddingCommandRejectsInvalidUBatchSize(t *testing.T) {
	_, err := BuildServerCommand(ModeEmbedding, "server.exe", ".", ServerOptions{
		Model: "e.gguf", Host: "localhost", Port: 29856, Pooling: "last",
	})
	if err == nil {
		t.Fatal("zero ubatch-size was accepted")
	}
}
