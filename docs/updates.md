# 更新、安全校验与恢复

未指定版本时使用 latest，也可用 `--version` 或 `--llama-version/--launcher-version` 选择正式 Release。相同版本需要 `--reinstall`；降级需要 `--allow-downgrade`；`dev` 启动器允许更新到正式版本。首次安装或已保存后端不再可用时必须选择本平台后端，非交互错误会列出完整可用值。

Release 响应有 4 MiB 上限，拒绝尾随 JSON、draft/prerelease、无效 tag 和重复资产。下载要求 HTTPS、有效大小与 GitHub SHA-256 digest。启动器额外与 `SHA256SUMS.txt` 交叉校验。系统代理、Windows PAC/WPAD 和 `LLAMALC_GITHUB_PROXY` URL 前缀均受支持；请求失败、异常状态或中途断开时会删除部分文件并只直连重试一次，显示信息会移除凭据、查询串和片段。

所有组合资产共享 20,000 条目和 8 GiB 解压预算。归档拒绝路径穿越、重复路径、链接、特殊文件与不完整条目。新运行时必须唯一包含 `llama-server` 和 `llama-cli`，并通过带超时、1 MiB 输出上限和目标 tag 签名的版本探测。启动器归档必须且只能包含 `LlamaLc/bin/llamalc[.exe]` 与 `llamaup[.exe]`；两个程序必须在精确 `Version:` 字段报告目标版本。

运行时切换和状态写入成功后立即删除上一版本；只有删除失败才写入 `pending_cleanup`，后续管理命令会自动重试。`--reinstall` 只替换状态登记的活动目标，拒绝覆盖未登记目录。状态损坏时，原状态和运行时先隔离到 `runtime/recovery`；失败完整回滚，成功保留恢复备份。

启动器更新会暂存并再次摘要校验两个程序，由 `llamaup` 执行双文件替换和回滚，然后自动重启。Windows 使用受保护的当前用户/LocalSystem DACL、写穿透原子替换和经格式校验的就绪事件；Linux 会明确检测新版是否启动后立即失败。

`cleanup` 只识别精确程序名、固定 16 位小写十六进制 token，以及带有效所有权标记的临时目录。删除前后复检路径范围、符号链接、特殊文件、文件身份和内容快照，先原子隔离目标，再删除并同步父目录。状态损坏时仍列出新布局运行时和恢复目录，但全部标记为扫描警告或需人工确认。任何旧布局都不在扫描范围内。
