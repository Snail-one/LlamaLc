# 部署与全新安装

程序必须直接位于字面命名为 `LlamaLc` 的根目录下的 `bin`：

```text
LlamaLc/
├─ bin/llamalc[.exe], llamaup[.exe]
├─ config/llamalc.json, router/models.ini
├─ secrets/api-key
├─ state/update.json, router/models.auto.ini
├─ runtime/llama.cpp/<backend>/<version>/
├─ runtime/recovery/
└─ models/
   ├─ llm/
   ├─ embedding/
   ├─ rerank/
   └─ mmproj/
```

`models/llm` 是唯一的对话/生成模型目录。程序不会读取、迁移、提示或清理 `models/generation`，也不会接触任何其他旧布局。

帮助和版本命令不要求部署目录且绝不写文件；所有实际操作都会验证 `LlamaLc/bin`。首次无参数启动如果没有有效运行时，只创建更新管理所需目录并进入维护模式。运行时安装成功后才生成 schema 1 配置、128 字符 URL-safe API key 和四个模型目录。

模型扫描拒绝符号链接和特殊文件。命令行可使用分类目录中的文件名或显式模型路径。
