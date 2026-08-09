# CLI、菜单与配置

命令按功能分组：

```text
llamalc run api|embedding|rerank|router|chat
llamalc config router generate
llamalc config key show|reset
llamalc update check [all|llama|launcher]
llamalc update llama|launcher|all
llamalc maintenance cleanup
llamalc version
```

运行命令用 `--` 分隔要原样传给 llama.cpp 的参数。配置优先级是命令行参数、`config/llamalc.json`、内置默认值。旧的 `serve`、`install`、`router-config` 等命令不是别名，会按未知命令拒绝。

主菜单固定为 `[1] 启动`、`[2] 配置`、`[3] 升级维护`。子菜单输入 `0` 或 `q` 返回，主菜单输入 `q` 退出。

菜单安装或更新 llama.cpp 时，会先读取当前 Release 并按编号列出本平台的全部可用后端。首次安装必须选择；已有安装会标记当前后端，直接回车即可沿用。当前后端已被上游移除时必须重新选择。

配置必须是 schema 1，未知字段、尾随 JSON 或非法值会立即报错。完整示例见 [examples/llamalc.json](../examples/llamalc.json)。API key 与更新状态不会写入主配置。
