# llama.cpp Windows Go 启动器

一个不依赖 PowerShell 的 `llama.cpp` 启动器。Windows 用户只需运行 `llama-launcher.exe`，即可启动生成模型、Embedding、Rerank、多模型 Router 或命令行聊天。

实现只使用 Go 标准库。启动参数以 [llama.cpp 官方 Server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md) 为准。

## 构建

需要 Go 1.22 或更高版本。在项目根目录运行：

```powershell
go test ./...
go build -o llama-launcher.exe ./cmd/llama-launcher
```

从 Linux 交叉编译 Windows x64 版本：

```sh
GOOS=windows GOARCH=amd64 go build -o llama-launcher.exe ./cmd/llama-launcher
```

项目不提交编译后的 exe。把 `llama-launcher.exe` 放到 llama.cpp Windows 可执行文件所在目录即可。

查看当前版本只使用 `-v`：

```powershell
.\llama-launcher.exe -v
```

该命令只打印版本号并退出，不会创建配置或扫描目录。交互菜单标题也会显示相同版本。

## 目录布局

```text
llama.cpp/
├─ llama-launcher.exe
├─ llama-server.exe
├─ llama-cli.exe
├─ launcher.json                  # 首次运行自动生成
├─ models/                        # 生成/聊天模型：gguf、bin、ggml
├─ embeddings/                    # Embedding 模型：gguf
├─ rerank/                        # Rerank 模型：gguf
├─ mmproj/                        # 多模态投影文件：gguf
└─ bin/
   ├─ router-models.ini           # 可选手动配置，优先使用且不会自动覆盖
   └─ router-models.auto.ini      # 每次启动 Router 自动刷新
```

模型目录会递归扫描并稳定排序。Router 只接收 GGUF，API model id 使用完整 GGUF 文件名；如果三个模型目录中存在同名文件，启动器会列出所有冲突并拒绝生成配置，避免静默覆盖。

相对路径始终相对于启动器根目录。默认根目录是 `llama-launcher.exe` 所在目录，也可用全局 `--root` 指定。`--config` 可指定另一个 JSON 配置文件。

## 交互菜单

双击 `llama-launcher.exe`，或不带参数运行，即可进入中文菜单：

```text
1. 单模型 API 服务
2. Embedding API
3. Rerank API
4. 生成手动 Router 配置
5. 多模型 Router
6. CLI 命令行聊天
0. 退出
```

菜单只读取 `launcher.json`，不会把交互选择写回配置。生成模型可从 `mmproj/` 中选择投影文件；启动器会按文件名公共前缀给出自动匹配项。

## 子命令

子命令适合脚本和自动化调用。用 `--` 分隔启动器参数和需要原样传给 llama.cpp 的额外参数。

```powershell
# 单模型 API
.\llama-launcher.exe serve --model Qwen.gguf --ctx-size 8192 --gpu-layers auto

# 多模态模型
.\llama-launcher.exe serve --model Qwen-VL.gguf --mmproj mmproj-Qwen-VL-F16.gguf

# Embedding；不指定 pooling 时由模型元数据决定
.\llama-launcher.exe embedding --model bge-m3.gguf --pooling mean

# Rerank 专用模式
.\llama-launcher.exe rerank --model bge-reranker-v2-m3.gguf

# 创建手动 preset；已有文件时必须显式 --force 才会覆盖
.\llama-launcher.exe router-config
.\llama-launcher.exe router-config --force

# 启动 Router；仍会刷新 auto preset，但手动 preset 存在时优先使用
.\llama-launcher.exe router --models-max 1 --autoload=true

# 命令行聊天
.\llama-launcher.exe chat --model Qwen.gguf --ctx-size 8192

# 额外参数原样转发
.\llama-launcher.exe serve --model Qwen.gguf -- --threads 8 --flash-attn on
```

通用服务选项包括 `--model`、`--host`、`--port`、`--gpu-layers`（也接受 `--n-gpu-layers`）、`--ctx-size` 和 `--ui`。布尔选项可写成 `--ui=false`、`--autoload=false`。运行 `<子命令> --help` 可查看该模式的完整选项。

配置优先级固定为：命令行 flags > `launcher.json` > 内置默认值。模型或可执行文件不存在、端口越界、GPU 层数或 pooling 非法时，启动器会在创建子进程前用中文报错。

## launcher.json

首次运行会自动生成默认配置，也可复制 [launcher.example.json](launcher.example.json) 后自行修改：

```json
{
  "paths": {
    "server": "llama-server.exe",
    "cli": "llama-cli.exe",
    "models": "models",
    "embeddings": "embeddings",
    "rerank": "rerank",
    "mmproj": "mmproj",
    "router_manual": "bin/router-models.ini",
    "router_auto": "bin/router-models.auto.ini"
  },
  "server": {
    "host": "127.0.0.1",
    "port": 29856,
    "n_gpu_layers": "auto",
    "ctx_size": 0,
    "ui": false
  },
  "embedding": {
    "pooling": ""
  },
  "router": {
    "models_max": 1,
    "autoload": true
  }
}
```

`embedding.pooling` 留空时使用模型元数据，也可设为 `none`、`mean`、`cls`、`last` 或 `rank`。所有路径均可改为绝对路径；Windows 盘符和 UNC 路径受支持。

## API 示例

Embedding 服务启动后使用 OpenAI 兼容接口：

```powershell
curl.exe http://127.0.0.1:29856/v1/embeddings `
  -H "Content-Type: application/json" `
  -d '{"input":["你好","世界"],"model":"bge-m3.gguf","encoding_format":"float"}'
```

Rerank 服务：

```powershell
curl.exe http://127.0.0.1:29856/v1/rerank `
  -H "Content-Type: application/json" `
  -d '{"model":"bge-reranker-v2-m3.gguf","query":"什么是熊猫？","top_n":2,"documents":["一种编程语言","熊猫是熊科动物","今天天气很好"]}'
```

Router 列出模型并按文件名路由：

```http
GET http://127.0.0.1:29856/models
```

```json
{
  "model": "Qwen.gguf",
  "messages": [{"role": "user", "content": "你好"}]
}
```

自动 Router preset 同时收录三类目录：普通模型可自动写入匹配的 `mmproj`，Embedding preset 写入 `embedding = true` 和可选 pooling，Rerank preset 写入 `reranking = true`。

## 进程与退出码

启动器通过参数数组直接创建进程，不经过 shell。子进程连接当前终端的 stdin/stdout/stderr，因此 CLI 交互、日志和 Ctrl+C 保持原生行为。以子命令方式运行时，`llama-server` 或 `llama-cli` 的退出码会由启动器原样返回。

## 从旧脚本迁移

PowerShell 与 BAT 业务入口已删除。旧版路径保持兼容：已有 `bin/router-models.ini` 会继续优先使用且不会被自动覆盖；`bin/router-models.auto.ini` 仍是自动生成位置。原先只使用 `models/` 和 `mmproj/` 的用户只需新增 `llama-launcher.exe`，Embedding 与 Rerank 模型分别放入新目录即可。

## 自动发版

推送形如 `v1.0.0` 的 Git tag 会触发 GitHub Actions：运行测试与 `go vet`，交叉编译 Windows amd64 版本，将版本号注入 exe，随后上传 ZIP 和 `SHA256SUMS.txt` 到 GitHub Release。普通 push 和 Pull Request 不运行工作流。

```sh
git tag v1.0.0
git push origin v1.0.0
```

Dependabot 每周检查 Go Modules 与 GitHub Actions 更新。
