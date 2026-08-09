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

v1.0.0 只支持全新安装。发现 `data/llama.cpp`、`launcher.json` 或 `models/` 下的顶层文件时，程序只显示原路径。它们不会被读取或迁移；清理命令也必须逐项确认后才会删除。

把模型手动放入相应分类目录，再用完整文件名或绝对路径启动。模型文件不允许是符号链接。
