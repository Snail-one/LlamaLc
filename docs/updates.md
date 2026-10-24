# 更新、安全校验与恢复

未指定版本时使用 latest，也可用 `--version` 或 `--llama-version/--launcher-version` 选择 Release。相同版本需要 `--reinstall`；降级需要 `--allow-downgrade`；`dev` 启动器允许更新到正式版本。首次安装或已保存后端不再可用时必须选择本平台后端，非交互错误会列出完整可用值。

llama.cpp 默认使用预览版通道（`--channel preview`），从 Releases 列表按返回顺序选择首个非 draft 的 `b<数字>` 构建，包括 prerelease；最多检查 100 条。使用 `--channel stable` 切换到正式版通道，跟随 GitHub `/releases/latest` 返回的非预发布版本。交互安装会先显示通道选项，回车选择预览版；非交互或 `--yes` 不询问通道。

正式版可能直接使用 `b<数字>`，也可能使用 `v0.7.0` 等语义版本。语义版本通过其 `nightly-tag.txt` 定位对应的二进制构建；该文件要求 HTTPS、有效大小（最多 128 字节）和 SHA-256 校验，且只能指向一个 `b<数字>` tag。正式版所引用的构建即使标记为 prerelease，也按正式版的固定构建安装。状态、目录、运行时探测和升级/降级判断统一使用实际构建号，避免将 `v…` 和 `b…` 错误比较。正式版缺少有效构建指向时会报错，不回退到预览版。

`update llama`、`update all` 和 `update check` 均支持 `--channel preview|stable`，其中通道只影响 llama.cpp。指定 `--version` / `--llama-version` 优先于通道，可直接选择 `b<数字>` 或有构建指向的语义版本。通道每次默认 preview，不随上一次安装改变；从较新的预览构建切换到较旧的正式构建仍需 `--allow-downgrade`。启动器继续使用正式版并拒绝预发布版本。

```sh
llamalc update llama --backend cpu --yes
llamalc update llama --channel stable --backend cpu --yes
llamalc update check llama --channel stable --json
llamalc update all --channel preview --backend cpu --yes
```

Release 响应有 4 MiB 上限，拒绝尾随 JSON、draft、无效 tag 和重复资产；只有 llama.cpp 的 `b<数字>` 构建允许 prerelease。下载要求 HTTPS、有效大小与 GitHub SHA-256 digest。启动器额外与 `SHA256SUMS.txt` 交叉校验。系统代理、Windows PAC/WPAD 和 `LLAMALC_GITHUB_PROXY` URL 前缀均受支持；请求失败、异常状态或中途断开时会删除部分文件并只直连重试一次，显示信息会移除凭据、查询串和片段。

Windows CUDA 后端的 `cudart-llama-bin-win-cuda-*.zip` 按 SHA-256 缓存到 `runtime/download-cache/cuda`。首次下载并校验后保留压缩包；后续升级、重装或失败重试，只要上游摘要和大小相同，就校验并复用缓存，继续安全解压，无需再次下载 CUDA 运行库。缓存损坏或摘要变化时重新下载，未完成或校验失败的下载不会写入缓存。llama.cpp 主程序仍按目标版本下载。更新计划显示预计下载量及缓存复用量，执行时再次校验缓存。

缓存独立于运行时目录，在旧运行时清理、修复安装或安装失败后仍可复用。旧版本已经删除的压缩包无法直接恢复为缓存，启用缓存后的首次更新仍需下载一次。缓存不随旧运行时自动删除；需要释放空间时可在没有更新任务运行时删除 `runtime/download-cache/cuda`，下次安装会重新下载。

所有组合资产共享 20,000 条目和 8 GiB 解压预算。归档拒绝路径穿越、重复路径、绝对/`..` 符号链接、硬链接、特殊文件与不完整条目；允许解压根内的相对符号链接（llama.cpp 的 `.so` soname）。新运行时必须唯一包含 `llama-server` 和 `llama-cli`，并通过带超时、1 MiB 输出上限和目标 tag 签名的版本探测。启动器归档必须且只能包含 `LlamaLc/bin/llamalc[.exe]` 与 `llamaup[.exe]`；两个程序必须在精确 `Version:` 字段报告目标版本。

运行时切换时会先把旧路径持久登记到 `pending_cleanup`，状态提交成功后立即删除上一版本并清除成功项；因此任何中途失败都仍可在下次重试。所有待清理状态变更与运行时更新共用跨进程锁，删除前还会以实际文件身份复核目标不是活动运行时。`--reinstall` 只替换状态登记的活动目标，拒绝覆盖未登记目录。状态损坏时，原状态和运行时先隔离到 `runtime/recovery`；失败完整报告回滚结果，成功保留恢复备份。

启动器自身更新由独立的 `llamaup` 完整负责：检查版本、展示计划、确认、下载和校验、安装、启动验证及失败回滚。`llamalc update launcher` 和菜单只启动更新器并等待结果；取消、检查或下载失败会保留主程序。下载及校验成功后，更新器创建当前更新器的临时运行副本，等待其确认接管任务，再要求旧主程序和下载更新器退出。临时副本会等待两个旧进程结束，重新校验暂存程序摘要，然后备份并替换 `llamaup` 和 `llamalc`。使用当前更新器的副本，避免依赖目标版本的内部交接协议。

Windows 使用受保护的当前用户/LocalSystem DACL、写穿透原子替换和经格式校验的就绪事件；Linux 使用继承给新主程序的专用管道，要求报告精确目标版本。两平台均等待主菜单或维护菜单完成初始化，最多 60 秒。新版提前退出、未报告就绪或超时会先停止新版，再恢复原程序并尝试启动旧版；无法确认新版已经停止时，保留备份与恢复记录，不覆盖仍在使用的程序。新版报告就绪后才提交更新并清理备份，文件替换成功不单独视为更新完成。

目标程序必须支持对应平台的就绪通知。不支持管道通知的旧 Linux 版本即使指定 `--allow-downgrade`，也会因启动验证失败而回滚；恢复原程序时使用普通启动方式，不要求原版支持新协议。

`llamaup` 可独立运行，不依赖主程序、llama.cpp 运行时或其状态文件正常。无参数启动等同于 `llamaup update`，非交互更新必须显式使用 `--yes`。主程序损坏或缺失时，可使用 `--reinstall` 修复；原文件缺失时，失败回滚也恢复为原先的缺失状态。

```sh
LlamaLc/bin/llamaup check --json
LlamaLc/bin/llamaup update --yes
LlamaLc/bin/llamaup update --version v1.2.3 --reinstall --yes
LlamaLc/bin/llamaup recover --yes
```

替换前在 `bin/.llamalc-update.json` 持久记录两个程序的备份名、安装前后 SHA-256 和事务阶段。中途退出或断电后，先关闭主程序，再运行 `llamaup recover`：未提交的更新恢复旧程序，已提交或已回滚的事务只补做清理。恢复前会验证全部备份、当前程序和固定路径；备份损坏、程序被外部修改或记录无效时保留现场并报错。存在恢复记录时禁止开始新更新，清理菜单也不会提供相关暂存程序和备份供删除。

`update all` 分两阶段执行：主程序先展示、确认并更新 llama.cpp，然后由独立更新器检查并确认启动器更新；`--yes` 同时作用于两阶段。启动器更新失败不会撤销已经完成的 llama.cpp 更新。独立执行 `llamaup` 更新自身时，应先关闭其他正在使用同一部署目录的主程序。

`cleanup` 只识别精确程序名、五个确切受管配置/状态文件的原子临时名、固定 16 位小写十六进制 token，以及带有效所有权标记的临时目录。删除前后复检路径范围、相对文件符号链接、特殊文件、文件身份和结构快照，先原子隔离目标，再删除并同步父目录。递归删除失败不会把可能已部分删除的目录伪装成完整原目录；隔离中断残留会明确列出并要求人工确认。状态损坏时仍列出新布局运行时和恢复目录，但全部标记为扫描警告或需人工确认。任何旧布局都不在扫描范围内。
