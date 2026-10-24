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
llamalc update check [all|llama|launcher] [--channel preview|stable] [--json]
llamalc update llama|launcher|all [更新选项]
llamalc cleanup
llamalc version
llamalc help
```

llama.cpp 默认更新到预览版；使用 `update llama --channel stable` 可选择正式版，菜单中回车选择预览版。启动器始终使用正式版。

项目自身更新由独立 `llamaup` 完成，入口为 `llamalc update launcher`。也可直接运行 `llamaup update --yes`；主程序损坏时可加 `--reinstall` 修复。新版就绪后才清理备份，启动失败会恢复原版。中断的更新可在关闭主程序后通过 `llamaup recover` 恢复，详见更新文档。

运行参数按模式独立校验；透传给 llama.cpp 的参数必须放在 `--` 后。更新与密钥重置在交互终端要求确认，非交互调用必须给出 `--yes`。

本项目不兼容、不检测、不迁移也不清理任何旧布局。`models/generation` 同样不会被读取、提示或操作。

详细说明：

- [CLI、菜单和配置](docs/cli.md)
- [部署目录](docs/deployment.md)
- [更新、安全校验和恢复](docs/updates.md)
- [架构](docs/architecture.md)
- [开发与发布](docs/development.md)
