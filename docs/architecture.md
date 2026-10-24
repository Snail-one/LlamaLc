# 架构

入口只有 `cmd/llamalc` 与 `cmd/llamaup`。主程序由 `internal/launcher` 初始化并装配 `cli` 和 `tui`；交互层调用 `config`、`secrets`、`models`、`llama` 与 `update`。启动器自身更新通过注入的回调启动独立 `llamaup` 进程，主程序不执行自身的安装计划、下载或替换。`updater` 负责更新交互、更新任务交接、双文件事务、启动验证与恢复，复用 `update` 中的 Release 选择、下载校验和暂存逻辑。

```text
launcher -> cli / tui
launcher -> updater.RunExternal -> llamaup 独立进程
cli,tui  -> config / secrets / models / llama / update
update   -> layout / llama / release / managedfs / version
updater  -> update / cli.DownloadReporter / managedfs / layout / procinfo
config,secrets,models,release -> managedfs / layout
```

各包只拥有一种状态：`config` 只读写主配置，`secrets` 只拥有 API key，`update` 只拥有 `state/update.json`，`updater` 拥有 `bin/.llamalc-update.json` 自身更新事务记录。模型不属于更新事务，布局之外的内容永远不进入发现或清理范围。

交接分两步：主程序等待独立更新器完成下载；独立更新器等待临时安装副本确认接管后返回专用退出状态，主程序才退出。安装副本等待两个旧进程结束，再备份和替换。新版主菜单报告就绪后提交事务；新版失败先停止进程，再回滚并重启原版。断电后的恢复使用相同的跨进程锁，备份通过复制恢复以支持中断重试，提交后的清理中断不会触发版本回退。
