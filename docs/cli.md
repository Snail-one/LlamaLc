# CLI、菜单与配置

命令按功能分组：

```text
llamalc run api|embedding|rerank|router|chat
llamalc config router generate [--force] [preset 选项]
llamalc config key show|reset
llamalc update check [all|llama|launcher]
llamalc update llama|launcher|all
llamalc maintenance cleanup
llamalc version
```

运行命令用 `--` 分隔要原样传给 llama.cpp 的参数。配置优先级是命令行参数、`config/llamalc.json`、内置默认值。旧的 `serve`、`install`、`router-config` 等命令不是别名，会按未知命令拒绝。

主菜单固定为 `[1] 启动`、`[2] 配置`、`[3] 升级维护`。主菜单直接回车进入启动目录；启动和配置子菜单直接回车选择第一项，升级维护必须输入明确编号。子菜单、模型选择和参数向导输入 `q` 返回主菜单；允许值为 `0` 的参数仍把 `0` 当作参数值。

启动菜单会递归扫描分类模型目录并稳定排序，显示编号、文件名和大小：

- API 与聊天读取 `models/generation`；
- Embedding 读取 `models/embedding`；
- Rerank 读取 `models/rerank`；
- API 还会读取 `models/mmproj`，标记文件名匹配的推荐项，也允许填写其他路径。

选定模型后，菜单逐项询问上下文、GPU 层、CPU 线程、batch/ubatch、Flash Attention、服务并发、监听地址、端口和 Web UI。Embedding 还询问 pooling 与归一化；Router 还询问模型加载上限、autoload 和 Embedding preset 参数。直接回车使用 `llamalc.json` 默认值，最后可填写带引号的自定义 llama.cpp 参数，并在启动前确认。进程退出后按 Enter 返回主菜单；llama.cpp 的退出码会原样返回给命令行调用者。

`config router generate` 同时收集 generation、embedding 和 rerank 模型，为生成模型匹配 mmproj，并拒绝跨目录同名 ID。已有手动 preset 默认不覆盖，必须使用 `--force` 或在菜单中明确确认。

显示或重置 API key 前，菜单会分别要求明文显示确认或失效确认。自动生成的 key 是 128 个 URL-safe 字符。

菜单安装或更新 llama.cpp 时，会先读取当前 Release 并按编号列出本平台的全部可用后端。首次安装必须选择；已有安装会标记当前后端，直接回车即可沿用。当前后端已被上游移除时必须重新选择。

没有有效运行时时，无参数启动会先进入维护模式，可安装 llama.cpp、更新启动器或退出。状态损坏或存在未登记的新布局运行时时，修复安装会先隔离到 `runtime/recovery`；安装失败会恢复原状态。

配置必须是 schema 1，未知字段、尾随 JSON 或非法值会立即报错。完整示例见 [examples/llamalc.json](../examples/llamalc.json)。API key 与更新状态不会写入主配置。
