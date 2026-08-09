# CLI、菜单与配置

完整公开接口：

```text
llamalc run api|embedding|rerank|router|chat [模式选项] [-- llama.cpp 参数]
llamalc router generate [--force] [preset 选项]
llamalc key show
llamalc key reset [--yes]
llamalc update check [all|llama|launcher] [--json]
llamalc update llama [--version TAG] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]
llamalc update launcher [--version SEMVER] [--reinstall] [--allow-downgrade] [--yes]
llamalc update all [--llama-version TAG] [--launcher-version SEMVER] [--backend ID] [--reinstall] [--allow-downgrade] [--yes]
llamalc cleanup
llamalc version
llamalc help
```

裸 `llamalc update` 只显示帮助。`serve`、`install`、`check-update`、`router-config`、`config` 和 `maintenance` 均不是别名。

每种运行模式使用独立参数集。例如 Router 不接受 `--model`，Chat 不接受 `--host`、`--port` 或其他网络参数。推荐使用 `--gpu-layers`、`--context-size`、`--flash-attention` 和 `--normalize`；现有的 `--n-gpu-layers`、`--ctx-size`、`--flash-attn` 与 `--embd-normalize` 仍可使用。额外 llama.cpp 参数必须位于 `--` 后。

配置优先级为命令行、schema 1 的 `config/llamalc.json`、内置默认值。API/Chat 使用 `models/llm`，Embedding、Rerank 和 mmproj 分别使用同名分类目录。Router 仅收录 GGUF，并拒绝跨目录模型 ID 冲突。

菜单回车默认进入第一目录和第一操作；模型列表回车默认选择第一项，`0/q` 返回。参数向导只收集输入，模型解析、最终校验和命令生成完成后才显示经过脱敏的真实命令并请求启动确认。

更新、检查和清理不加载主配置或 API key；Router 配置不要求运行时；只有服务启动才加载配置、密钥和运行时。没有有效运行时时，无参数启动直接进入维护模式，安装成功后才创建正常运行所需的配置、密钥和模型目录。
