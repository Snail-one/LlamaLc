# LlamaLc

`LlamaLc` 是只使用 Go 标准库的 llama.cpp 启动、配置和更新工具，支持 Linux/Windows 的 amd64 与 arm64。v1.0.0 使用全新部署布局，不兼容旧版配置，也不会迁移或自动删除旧版运行时和模型。

## 快速安装

从 Releases 下载 `llamalc-<os>-<arch>-<version>`，校验 `SHA256SUMS.txt` 后解压。归档会创建：

```text
LlamaLc/bin/llamalc[.exe]
LlamaLc/bin/llamaup[.exe]
```

首次安装运行时并启动 API：

```sh
LlamaLc/bin/llamalc update llama --backend cpu
LlamaLc/bin/llamalc run api --model your-model.gguf
```

不带参数运行 `llamalc` 可进入中文菜单。常用命令：

```sh
llamalc run embedding --model embedding.gguf
llamalc config router generate
llamalc config key reset
llamalc update check all
llamalc update all
llamalc maintenance cleanup
llamalc version
```

## 文档

- [架构与包职责](docs/architecture.md)
- [CLI、菜单和配置](docs/cli.md)
- [部署与全新安装](docs/deployment.md)
- [更新、安全校验和恢复](docs/updates.md)
- [构建、测试和发布](docs/development.md)
