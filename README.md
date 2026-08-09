# LlamaLc

`LlamaLc` 是仅使用 Go 标准库的 llama.cpp 启动、配置与更新工具，支持 Linux/Windows 的 amd64 和 arm64。

发布归档严格包含：

```text
LlamaLc/bin/llamalc[.exe]
LlamaLc/bin/llamaup[.exe]
```

全新部署后先安装运行时，再把对话/生成模型放入 `LlamaLc/models/llm`：

```sh
LlamaLc/bin/llamalc update llama --backend cpu --yes
LlamaLc/bin/llamalc run api --model your-model.gguf
```

不带参数运行可进入中文菜单。公开命令只有：

```text
llamalc run api|embedding|rerank|router|chat
llamalc router generate
llamalc key show
llamalc key reset [--yes]
llamalc update check [all|llama|launcher] [--json]
llamalc update llama|launcher|all [更新选项]
llamalc cleanup
llamalc version
llamalc help
```

运行参数按模式独立校验；透传给 llama.cpp 的参数必须放在 `--` 后。更新与密钥重置在交互终端要求确认，非交互调用必须给出 `--yes`。

本项目不兼容、不检测、不迁移也不清理任何旧布局。`models/generation` 同样不会被读取、提示或操作。

详细说明：

- [CLI、菜单和配置](docs/cli.md)
- [部署目录](docs/deployment.md)
- [更新、安全校验和恢复](docs/updates.md)
- [架构](docs/architecture.md)
- [开发与发布](docs/development.md)
