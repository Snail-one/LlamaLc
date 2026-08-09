# 部署与全新安装

程序必须位于字面命名为 `LlamaLc` 的根目录下的 `bin`。首次运行会创建固定目录：

```text
LlamaLc/
├─ bin/llamalc[.exe], llamaup[.exe]
├─ config/llamalc.json, router/models.ini
├─ secrets/api-key
├─ state/update.json, router/models.auto.ini
├─ runtime/llama.cpp/<backend>/<version>
├─ runtime/recovery/
└─ models/generation, embedding, rerank, mmproj
```

当前目录架构只支持全新安装。发现 `data/llama.cpp`、`launcher.json` 或 `models/` 下的顶层文件时，程序只显示原路径。它们不会被读取或迁移；清理命令也必须逐项查看并二次确认后才会删除，绝不会进入批量清理。

把模型手动放入相应分类目录。不带参数进入菜单时，程序会显示模型编号、文件名和大小；命令行也可使用完整文件名或绝对路径。模型文件不允许是符号链接。

`Server_README.md` 不是 LlamaLc 的运行文件；如果部署根目录中存在该文件，它通常是 llama.cpp 上游 server 文档的手工副本，LlamaLc 不读取它。运行时只从 `runtime/llama.cpp/<backend>/<version>` 定位 `llama-server` 和 `llama-cli`。
