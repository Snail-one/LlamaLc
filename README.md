# llama.cpp Windows Launcher

一个用于 Windows 版 `llama.cpp` 的轻量启动器。脚本会扫描本目录下的 `models` 和 `mmproj`，帮助你用菜单启动单模型服务、命令行聊天，或使用 `llama-server` 原生 router mode 运行多模型 API。

## 文件结构

```text
llama.cpp/
├─ bin/
│  ├─ llama-launcher.bat
│  ├─ llama-launcher.ps1
│  ├─ router-models.ini        # 可选：手动路由配置，不会被覆盖
│  └─ router-models.auto.ini   # 路由模式自动生成
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

启动器提供 4 种模式：

```text
1. 单模型 API 服务（llama-server）
2. API 多模型路由
3. 命令行聊天（llama-cli）
4. 创建/刷新手动路由配置
```

单模型 API 服务会让你选择模型、可选 `mmproj`、上下文长度、GPU 层数、host、port 和是否启用 Web UI。

命令行聊天使用 `llama-cli.exe`，适合直接在终端里交互。

## 多模型 API 路由

路由模式使用 `llama-server` 原生 router 能力。启动时脚本会：

- 扫描 `models/*.gguf`
- 每次都会刷新自动配置 `bin/router-models.auto.ini`
- 如果存在手动配置 `bin/router-models.ini`，运行时优先使用它且不会覆盖
- 如果不存在手动配置，运行时使用 `bin/router-models.auto.ini`
- 使用完整 GGUF 文件名作为 API model id，和单模型列表显示一致
- 默认 `n-gpu-layers = auto`
- 默认 `load-on-startup = false`

最终启动命令类似：

```powershell
.\llama-server.exe --models-preset .\bin\router-models.auto.ini --models-max 1 --models-autoload --host 127.0.0.1 --port 29856 --no-ui
```

`--models-max 1` 适合 16GB 显存环境，避免多个大模型同时占用显存。

### 手动路由配置

选择第 4 项会扫描 `models/*.gguf` 并创建/刷新 `bin/router-models.ini`。如果文件已经存在，脚本会先询问是否覆盖。

生成的手动配置会包含：

- 每个模型一个 section，section 名就是 GGUF 文件名
- 主模型路径 `model = ...`
- 常用配置模板：`n-gpu-layers`、`load-on-startup`、`ctx-size`、`threads`
- 可手动启用的多模态配置：`mmproj`、`image-min-tokens`
- 当前 `mmproj/*.gguf` 文件列表，方便复制到对应模型段落

多模型路由启动时，如果 `bin/router-models.ini` 存在，会优先使用这个手动配置；否则使用自动生成的 `bin/router-models.auto.ini`。

## API 示例

查看所有模型：

```http
GET http://127.0.0.1:29856/models
```

手动加载模型：

```http
POST http://127.0.0.1:29856/models/load
Content-Type: application/json

{"model":"Qwen3.5-9B-UD-Q4_K_XL.gguf"}
```

OpenAI 兼容接口选择模型：

```http
POST http://127.0.0.1:29856/v1/chat/completions
Content-Type: application/json

{
  "model": "Qwen3.5-9B-UD-Q4_K_XL.gguf",
  "messages": [
    {"role": "user", "content": "你好"}
  ]
}
```

## 说明

`mmproj` 文件不是单独模型，它是多模态/视觉模型使用的投影器。单模型 API 服务模式会让你手动选择或跳过 `mmproj`；多模型路由的自动配置只生成主模型 preset，如需在路由模式中使用多模态模型，请用第 4 项生成 `router-models.ini` 后手动配置对应模型的 `mmproj`。

多模型路由基于 `llama.cpp` 官方 `llama-server` router mode。官方 server README 说明 `--models-preset` 可使用 INI 文件定义模型预设，每个 section 是一个模型 preset，API 可通过 `/models`、`/models/load` 和请求里的 `model` 字段管理与路由模型。
