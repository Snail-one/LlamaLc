# 更新

`update llama` 在没有运行时时执行首次安装，之后更新当前后端。首次安装必须给出 `--backend`；同版本只有显式 `--reinstall` 才重装，降级会拒绝。运行时目录是 `runtime/llama.cpp/<backend>/<release-tag>`，tag 长度不固定。

元数据来自 GitHub Releases。资产必须带 SHA-256 digest；启动器归档还会与 `SHA256SUMS.txt` 交叉校验。下载后先校验大小和摘要，再进行防路径穿越、链接、特殊文件和解压体积的安全解压。可用 `LLAMALC_GITHUB_PROXY` 配置 HTTPS 下载前缀；环境代理优先，Windows 还支持当前用户的手动代理及 PAC/WPAD，代理失败时会直接连接重试。

启动器归档必须且只能包含 `LlamaLc/bin/llamalc[.exe]` 与 `LlamaLc/bin/llamaup[.exe]`。`llamaup` 等待旧进程退出，备份并替换两个程序；第二次替换失败会回滚。完成后自动启动新版，Windows 还会等待菜单就绪事件。

旧运行时只登记为待清理项，不和更新事务一起删除。`maintenance cleanup` 对每个旧运行时、恢复项和旧版路径逐项询问。
