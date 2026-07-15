# llama.cpp Windows/Linux Go 启动器

一个不依赖 PowerShell 的 `llama.cpp` 启动器。Windows 用户运行 `bin/llama-launcher.exe`，Linux 用户运行 `bin/llama-launcher`，即可启动生成模型、Embedding、Rerank、多模型 Router 或命令行聊天。

实现只使用 Go 标准库。启动参数以 [llama.cpp 官方 Server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md) 为准。

## 构建

需要 Go 1.22 或更高版本。

### Linux 原生构建

`build-only.sh` 默认构建当前 Linux 架构，自动读取 `go.mod` 模块路径、Git 版本和提交，并将产物写入 `dist/`：

```sh
./build-only.sh
./dist/llama-launcher_linux_amd64 --version
```

可通过环境变量覆盖目标平台和所有版本信息：

```sh
VERSION=v1.0.0 \
COMMIT=abc1234 \
BUILD_DATE=2026-07-14T12:00:00Z \
./build-only.sh
```

交叉编译也使用同一个脚本：

```sh
GOOS=windows GOARCH=amd64 ./build-only.sh
```

输出文件会根据入口程序、目标系统和架构自动命名，例如：

```text
dist/llama-launcher_linux_amd64
dist/llama-launcher_windows_amd64.exe
```

### Windows 启动器一键构建

Linux 下一键交叉编译 Windows amd64 版本：

```sh
./scripts/build-linux.sh
```

Windows 下双击 `scripts/build-windows.cmd`，或在终端运行：

```powershell
.\scripts\build-windows.cmd
```

两个脚本都支持可选的版本号和目标架构：

```sh
# Linux
./scripts/build-linux.sh v1.2.3 arm64
```

```powershell
# Windows
.\scripts\build-windows.cmd v1.2.3 arm64
```

`scripts/build-linux.sh` 与 `scripts/build-windows.cmd` 会生成可直接部署的 `bin/llama-launcher.exe`。未提供位置参数时架构默认为 `amd64`；未提供版本时自动使用 Git 描述，Git 不可用时使用 `dev`。Linux 脚本会明确设置 `GOOS=windows`，避免生成无法在 Windows 运行的 ELF 文件。项目不提交编译产物。

`llama-launcher.exe` 固定放在 llama.cpp 根目录的 `bin/` 下；启动器会自动以上一级目录作为根目录。

查看完整版本信息可使用 `-v`、标准长参数 `--version` 或 `version` 命令：

```powershell
.\bin\llama-launcher.exe -v
.\bin\llama-launcher.exe --version
.\bin\llama-launcher.exe version
```

输出包含构建版本、Git 提交和 UTC 构建时间：

```text
Version:   v1.0.0
Commit:    abc1234
BuildDate: 2026-07-14T12:00:00Z
```

版本查询直接输出并以状态码 `0` 退出，不要求位于 `bin/`，不会创建配置、扫描模型或启动服务器。其他命令仍会强制检查启动器必须位于 `bin/`。交互菜单标题显示其中的版本号。

## 目录布局

```text
llama.cpp/
├─ llama-server.exe / llama-server  # Windows / Linux
├─ llama-cli.exe / llama-cli        # Windows / Linux
├─ models/                        # 自动创建；生成/聊天模型：gguf、bin、ggml
├─ embeddings/                    # 自动创建；Embedding 模型：gguf
├─ rerank/                        # 自动创建；Rerank 模型：gguf
├─ mmproj/                        # 自动创建；多模态投影文件：gguf
├─ config/                        # 验证安装位置后自动创建
│  ├─ launcher.json              # 首次运行自动生成
│  ├─ router-models.ini           # 手动 Router 配置
│  └─ router-models.auto.ini      # 自动 Router 配置
└─ bin/
   └─ llama-launcher.exe / llama-launcher
```

模型目录会递归扫描并稳定排序。`llama-server`、`llama-cli`、模型文件、四个模型目录、`config/` 和受管理配置文件均必须是真实的普通文件或目录，不允许使用符号链接或 Windows 重解析点。Router 只接收 GGUF，API model id 使用完整 GGUF 文件名；如果三个模型目录中存在同名文件，启动器会列出所有冲突并拒绝生成配置，避免静默覆盖。

除无副作用的版本查询外，启动器会先解析自身真实路径（包括符号链接），并检查直接父目录必须名为 `bin`。随后按当前系统检查根目录中的 `llama-server.exe`（Windows）或 `llama-server`（Linux），在根目录执行 `--version`，只有命令在 30 秒内成功退出、输出不超过 1 MiB 且内容可识别时，才读取配置和创建目录。任何位置、平台、文件、运行库或版本探测错误都不会创建配置或模型目录。

根目录固定为 `bin` 的上一级；模型目录、配置文件和 Router 文件位置也全部固定。`--root` 与 `--config` 已移除，传入时会直接报错，防止意外把文件写到其他位置。顶层帮助、子命令帮助和未知命令不会执行 server 探测或初始化磁盘；`-v`、`--version`、`version` 则可以在任意位置查询启动器版本。

探测成功后会同时打印实际执行的 server 文件与识别到的版本，例如：

```text
实际探测文件: E:\llama.cpp\llama-server.exe
已识别 llama.cpp: version: 10002 (a7312ae94)
```

## 交互菜单

双击 `bin/llama-launcher.exe`，或不带参数运行，即可进入中文菜单：

```text
1. 单模型 API 服务
2. Embedding API
3. Rerank API
4. 生成手动 Router 配置
5. 多模型 Router
6. CLI 命令行聊天
q. 退出
```

首次正常启动会自动生成 128 位 URL-safe API key 并写入 `config/launcher.json`。启动器会明确提示配置文件路径以及用于查看 key 的 `server.api_key` 字段。后续每次启动都会先询问 `是否重置 API key [y/N]`；直接回车或输入 `n` 会继续使用已保存的 key，输入 `y` 会用新的随机 key 原子更新配置。版本和帮助命令不读取或修改 key。

除 API key 的首次生成和主动重置外，菜单不会把其他交互选择写回配置。交互流程中任意询问输入 `q`（大小写均可）会立即返回主菜单，主菜单输入 `q` 退出程序；`0` 保留给参数和选项本身使用。交互终端在进入功能和返回主菜单时会自动清屏，重定向到文件或管道的输出不会包含清屏控制码。选择模型后会逐项询问上下文、GPU 层数、CPU 线程、batch/ubatch、Flash Attention、服务并发、监听地址、端口和 Web UI。直接回车使用配置默认值，Web UI 默认不启用。

生成模型可从 `mmproj/` 中选择自动匹配项，也可输入其他 mmproj 路径；选择投影文件后还会询问图片最小和最大 token。Embedding 会额外询问 pooling 与向量归一化，Router 会询问模型加载上限、autoload 和 Embedding preset 参数。每种启动模式最后都可填写多个自定义 llama.cpp 参数。启动器会先显示完整最终命令，再请求确认；进程结束后等待按 Enter 返回菜单。

通用默认值采用 llama.cpp 官方的自动或保守默认行为：

| 参数 | 默认值 | 含义 |
| --- | ---: | --- |
| `--ctx-size` | `0` | 从模型元数据读取上下文 |
| `--n-gpu-layers` | `auto` | 自动按可用设备内存适配 |
| `--threads` | `-1` | 自动选择 CPU 线程数 |
| `--batch-size` | `2048` | 官方逻辑 batch 默认值 |
| `--ubatch-size` | `512` | 官方物理 batch 默认值 |
| `--flash-attn` | `auto` | 由后端自动决定 |
| `--parallel` | `-1` | 服务槽位数自动决定 |
| `--ui` | `false` | 启动器明确关闭 Web UI |

Embedding 按前述专用设置使用 `--pooling last --batch-size 8192 --ubatch-size 8192 --embd-normalize 2`。启动器会校验 `batch-size >= ubatch-size`；较大 batch 会增加设备内存需求，显存不足时可在菜单中同时调低这两个值。

## 子命令

子命令适合脚本和自动化调用。用 `--` 分隔启动器参数和需要原样传给 llama.cpp 的额外参数。

```powershell
# 单模型 API
.\bin\llama-launcher.exe serve --model Qwen.gguf --ctx-size 8192 --gpu-layers auto

# 多模态模型
.\bin\llama-launcher.exe serve --model Qwen-VL.gguf --mmproj mmproj-Qwen-VL-F16.gguf

# Embedding；不指定时默认 pooling/逻辑 batch/物理 batch 为 last/8192/8192
.\bin\llama-launcher.exe embedding --model bge-m3.gguf

# 显式覆盖 Embedding 参数
.\bin\llama-launcher.exe embedding --model bge-m3.gguf --pooling mean --batch-size 4096 --ubatch-size 4096

# Rerank 专用模式
.\bin\llama-launcher.exe rerank --model bge-reranker-v2-m3.gguf

# 创建手动 preset；已有文件时必须显式 --force 才会覆盖
.\bin\llama-launcher.exe router-config
.\bin\llama-launcher.exe router-config --force

# 启动 Router；仍会刷新 auto preset，但手动 preset 存在时优先使用
.\bin\llama-launcher.exe router --models-max 1 --autoload=true

# 命令行聊天
.\bin\llama-launcher.exe chat --model Qwen.gguf --ctx-size 8192

# 额外参数原样转发
.\bin\llama-launcher.exe serve --model Qwen.gguf -- --threads 8 --flash-attn on

# 自动生成的 API key 会注入服务参数，因此可以安全通过认证校验
.\bin\llama-launcher.exe serve --model Qwen.gguf --host 0.0.0.0
```

通用服务选项包括 `--model`、`--host`、`--port`、`--gpu-layers`（也接受 `--n-gpu-layers`）、`--ctx-size`、`--threads`、`--batch-size`、`--ubatch-size`、`--flash-attn`、`--parallel` 和 `--ui`。Embedding 还支持 `--pooling` 与 `--embd-normalize`；Router preset 使用 `--embedding-batch-size` 和 `--embedding-ubatch-size`。布尔选项可写成 `--ui=false`、`--autoload=false`。运行 `<子命令> --help` 可查看该模式的完整选项。

配置优先级固定为：命令行 flags > `config/launcher.json` > 内置默认值。模型或可执行文件不存在、端口越界、GPU 层数或 pooling 非法时，启动器会在创建子进程前用中文报错。启动器会把 `server.api_key` 作为 `--api-key` 自动注入单模型、Embedding、Rerank 和 Router 服务；监听地址不是 localhost、loopback IP 或 Unix socket 时仍会检查有效认证参数，否则拒绝启动。

## config/launcher.json

首次运行会自动生成默认配置，也可复制 [launcher.example.json](launcher.example.json) 后自行修改：

```json
{
  "server": {
    "host": "127.0.0.1",
    "port": 29856,
    "api_key": "",
    "n_gpu_layers": "auto",
    "ctx_size": 0,
    "threads": -1,
    "batch_size": 2048,
    "ubatch_size": 512,
    "flash_attention": "auto",
    "parallel": -1,
    "ui": false
  },
  "embedding": {
    "pooling": "last",
    "batch_size": 8192,
    "ubatch_size": 8192,
    "normalize": 2
  },
  "router": {
    "models_max": 1,
    "autoload": true
  }
}
```

`server.api_key` 在示例配置中可留空；启动器会用密码学安全随机源生成并保存 128 位 key。手工配置仍允许最长 8167 位；该上限依据[当前上游 HTTP 头限制](https://github.com/ggml-org/llama.cpp/blob/master/vendor/cpp-httplib/httplib.h)和[认证头处理实现](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/server-http.cpp)推导。

`embedding.pooling` 默认为 `last`，也可设为 `none`、`mean`、`cls` 或 `rank`；`embedding.batch_size` 与 `embedding.ubatch_size` 默认为 `8192`，必须是正整数且逻辑 batch 不能小于物理 batch；`embedding.normalize` 使用 llama.cpp 官方默认值 `2`。配置文件最大为 1 MiB。配置不再包含 `paths`；若现有 `config/launcher.json` 仍有该字段，启动器会拒绝读取，请删除该字段或删除配置后让启动器重新生成。

## API 示例

Embedding 服务启动后使用 OpenAI 兼容接口：

```powershell
curl.exe http://127.0.0.1:29856/v1/embeddings `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer <config/launcher.json 中的 api_key>" `
  -d '{"input":["你好","世界"],"model":"bge-m3.gguf","encoding_format":"float"}'
```

Rerank 服务：

```powershell
curl.exe http://127.0.0.1:29856/v1/rerank `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer <config/launcher.json 中的 api_key>" `
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

自动 Router preset 同时收录三类目录：普通模型可自动写入匹配的 `mmproj`，Embedding preset 写入 `embedding = true`、pooling、batch-size 和 ubatch-size，Rerank preset 写入 `reranking = true`。生成手动 preset 时可在菜单中关闭 mmproj 自动匹配。

## 安全说明

- `config/launcher.json`、手动 Router preset 和自动 Router preset 都固定在 `config/` 中；符号链接、junction 和重解析点会被拒绝，避免写入根目录外文件。
- `config/launcher.json` 包含明文 API key；启动器在支持权限位的平台上以仅当前用户可读写的 `0600` 模式创建或更新它。请勿提交或共享真实配置。
- 配置与 Router preset 先写入同目录临时文件并同步，再替换目标文件。覆盖失败不会先清空原文件；不带 `--force` 时仍拒绝覆盖手动配置。
- Router 会拒绝包含控制字符、换行或非法 section 分隔符的模型文件名和路径，防止 preset 注入。
- 完整命令预览会把自动注入的 `--api-key VALUE` 以及用户提供的 `--api-key=VALUE` 脱敏为 `******`。密钥仍可能出现在操作系统进程列表中。
- 非本机监听必须配置认证，并会额外显示安全警告。默认仍为 `127.0.0.1` 且关闭 Web UI。
- 自定义参数拥有 llama.cpp 的完整能力。不要在不可信环境中启用 `--tools`、MCP proxy 等高权限功能。
- 不要直接加载来源不明的模型。llama.cpp 官方建议在容器、虚拟机等隔离环境中处理不可信模型，参见 [llama.cpp Security](https://github.com/ggml-org/llama.cpp/security)。

## 进程与退出码

启动器通过参数数组直接创建进程，不经过 shell。子进程连接当前终端的 stdin/stdout/stderr，因此 CLI 交互、日志和 Ctrl+C 保持原生行为。以子命令方式运行时，`llama-server` 或 `llama-cli` 的退出码会由启动器原样返回。

## 旧布局说明

新版本不兼容旧配置位置，不会读取根目录的 `launcher.json`，也不会读取 `bin/router-models.ini` 或 `bin/router-models.auto.ini`。所有运行配置和 Router 文件统一位于根目录的 `config/` 中。

## 自动发版

推送形如 `v1.0.0` 的 Git tag 会触发 GitHub Actions：使用当前稳定 Go 工具链运行测试与 `go vet`，交叉编译 Windows amd64 版本，将 tag、提交哈希和 UTC 构建时间注入 exe，随后上传 ZIP 和 `SHA256SUMS.txt` 到 GitHub Release。普通 push 和 Pull Request 不运行工作流。

```sh
git tag v1.0.0
git push origin v1.0.0
```

Dependabot 每周检查 Go Modules 与 GitHub Actions 更新。
