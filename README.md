# llama.cpp Windows Launcher

一个用于 Windows 版 `llama.cpp` 的轻量启动器。脚本会扫描本目录下的 `models` 和 `mmproj`，帮助你用菜单启动单模型服务、命令行聊天，或使用 `llama-server` 原生 router mode 运行多模型 API。

## 文件结构

```text
llama.cpp/
├─ bin/
│  ├─ llama-launcher.bat
│  ├─ llama-launcher.ps1
│  └─ router-models.ini        # 路由模式启动时自动生成
├─ models/
│  └─ *.gguf
├─ mmproj/
│  └─ *.gguf
└─ llama-server.exe
```

## 快速启动

双击或在 PowerShell 中运行：

```powershell
.\bin\llama-launcher.bat
```

如果直接运行 `.ps1` 被系统策略拦截，可以继续使用 `.bat`，它会用临时 `ExecutionPolicy Bypass` 启动 PowerShell 脚本。

## 启动模式

启动器提供 3 种模式：

```text
1. 单模型 API 服务（llama-server）
2. 命令行聊天（llama-cli）
3. API 多模型路由
```

单模型 API 服务会让你选择模型、可选 `mmproj`、上下文长度、GPU 层数、host、port 和是否启用 Web UI。

命令行聊天使用 `llama-cli.exe`，适合直接在终端里交互。

## 多模型 API 路由

路由模式使用 `llama-server` 原生 router 能力。启动时脚本会：

- 扫描 `models/*.gguf`
- 自动生成 `bin/router-models.ini`
- 使用文件名去掉 `.gguf` 作为 API model id
- 默认 `n-gpu-layers = auto`
- 默认 `load-on-startup = false`

最终启动命令类似：

```powershell
.\llama-server.exe --models-preset .\bin\router-models.ini --models-max 1 --models-autoload --host 127.0.0.1 --port 29856 --no-ui
```

`--models-max 1` 适合 16GB 显存环境，避免多个大模型同时占用显存。

## API 示例

查看所有模型：

```http
GET http://127.0.0.1:29856/models
```

手动加载模型：

```http
POST http://127.0.0.1:29856/models/load
Content-Type: application/json

{"model":"Qwen3.5-9B-UD-Q4_K_XL"}
```

OpenAI 兼容接口选择模型：

```http
POST http://127.0.0.1:29856/v1/chat/completions
Content-Type: application/json

{
  "model": "Qwen3.5-9B-UD-Q4_K_XL",
  "messages": [
    {"role": "user", "content": "你好"}
  ]
}
```

## 说明

`mmproj` 文件不是单独模型，它是多模态/视觉模型使用的投影器。单模型 API 服务模式会让你手动选择或跳过 `mmproj`；多模型路由模式默认只生成主模型 preset，不自动匹配 `mmproj`。

多模型路由基于 `llama.cpp` 官方 `llama-server` router mode。官方 server README 说明 `--models-preset` 可使用 INI 文件定义模型预设，每个 section 是一个模型 preset，API 可通过 `/models`、`/models/load` 和请求里的 `model` 字段管理与路由模型。
