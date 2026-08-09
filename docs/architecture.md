# 架构

入口只有 `cmd/llamalc` 与 `cmd/llamaup`。主程序由 `internal/launcher` 初始化并装配 `cli` 和 `tui`；交互层调用 `config`、`secrets`、`models`、`llama` 与 `update`。`update` 组合 `layout`、`llama`、`release`、`managedfs` 和 `version`，独立更新交接由 `updater` 完成。

```text
launcher -> cli / tui
cli,tui  -> config / secrets / models / llama / update
update   -> layout / llama / release / managedfs / version
config,secrets,models,release,updater -> managedfs / layout
```

各包只拥有一种状态：`config` 只读写主配置，`secrets` 只拥有 API key，`update` 只拥有 `state/update.json`。模型和旧版内容不属于更新事务。
