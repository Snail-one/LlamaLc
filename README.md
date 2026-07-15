# llama.cpp Windows/Linux Go 启动器

一个不依赖 PowerShell 的 `llama.cpp` 启动器。部署根目录必须字面命名为 `llama.cpp`；Windows 用户只从 `llama.cpp/bin/llama-launcher.exe`、Linux 用户只从 `llama.cpp/bin/llama-launcher` 执行命令。启动器可从官方 GitHub Releases 安装和手动更新受管的 llama.cpp 运行时。

实现只使用 Go 标准库。启动参数以 [llama.cpp 官方 Server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md) 为准。

## 构建

需要 Go 1.22 或更高版本。

### Linux 原生构建

`build-only.sh` 默认构建当前 Linux 架构的 launcher 和最小 updater，自动读取 `go.mod` 模块路径、Git 版本和提交，并将两个程序写入同一个可部署目录：

```sh
./build-only.sh
./dist/linux-amd64/llama.cpp/bin/llama-launcher --version
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
dist/linux-amd64/llama.cpp/bin/llama-launcher
dist/linux-amd64/llama.cpp/bin/llama-updater
dist/windows-amd64/llama.cpp/bin/llama-launcher.exe
dist/windows-amd64/llama.cpp/bin/llama-updater.exe
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

`scripts/build-linux.sh` 与 `scripts/build-windows.cmd` 会生成可直接部署的 `dist/windows-<arch>/llama.cpp/bin/llama-launcher.exe` 和 `llama-updater.exe`。未提供位置参数时架构默认为 `amd64`；未提供版本时自动使用 Git 描述，Git 不可用时使用 `dev`。Linux 脚本会明确设置 `GOOS=windows`，避免生成无法在 Windows 运行的 ELF 文件。项目不提交编译产物。

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

版本查询直接输出并以状态码 `0` 退出，不会创建配置、扫描模型或启动服务器，但和其他用户命令一样要求启动器位于字面名为 `llama.cpp` 的根目录下的 `bin/`。交互菜单标题显示其中的版本号。

## 目录布局

```text
llama.cpp/
├─ data/
│  └─ llama.cpp/
│     └─ <tag>-<backend>/          # 受管 server、cli 和运行库
├─ models/                        # 自动创建；生成/聊天模型：gguf、bin、ggml
├─ embeddings/                    # 自动创建；Embedding 模型：gguf
├─ rerank/                        # 自动创建；Rerank 模型：gguf
├─ mmproj/                        # 自动创建；多模态投影文件：gguf
├─ config/                        # 验证安装位置后自动创建
│  ├─ launcher.json              # 首次运行自动生成
│  ├─ launcher.api-key           # 传给 llama-server 的私有 key 文件
│  ├─ update-state.json          # 私有的活动版本、后端和摘要状态
│  ├─ router-models.ini           # 手动 Router 配置
│  └─ router-models.auto.ini      # 自动 Router 配置
└─ bin/
   ├─ llama-launcher.exe / llama-launcher
   └─ llama-updater.exe / llama-updater
```

根目录旧版平铺的 `llama-server`、`llama-cli`（以及 `.exe` 版本）会被完全忽略，既不迁移也不删除。更新默认删除上一个受管版本；Windows 文件占用导致暂时无法删除时，会写入 `pending_cleanup`，下次管理命令重试。模型文件不属于更新范围。

模型目录会递归扫描并稳定排序。`llama-server`、`llama-cli`、模型文件、四个模型目录、`config/` 和受管理配置文件均必须是真实的普通文件或目录，不允许使用符号链接或 Windows 重解析点。Router 只接收 GGUF，API model id 使用完整 GGUF 文件名；如果三个模型目录中存在同名文件，启动器会列出所有冲突并拒绝生成配置，避免静默覆盖。

除无副作用的版本查询外，启动器会先解析自身真实路径（包括符号链接），检查直接父目录必须名为 `bin`，且根目录必须名为 `llama.cpp`。随后只读取 `config/update-state.json` 指向的 `data/llama.cpp/<tag>-<backend>/`，并执行其中的 server `--version` 探测。绝对路径、越界路径、符号链接、重解析点及损坏状态都会被拒绝。

缺失或损坏运行时时，无参数启动进入维护菜单，可安装 llama.cpp、更新启动器或退出；服务子命令会明确提示先执行 `install`。安装和检查更新均由当前 launcher 直接完成，不会运行 updater。launcher 更新 archive 固定同时包含新版 launcher 和新版 updater，并在完成双重 SHA-256 校验、严格结构检查及两个程序的版本探测后开始交接。Windows 会把当前 `bin/llama-updater.exe` 复制为 `bin/.llama-updater-run-*.exe`；临时副本等待 launcher 退出，先替换正式 updater，再替换 launcher，随后直接退出。新版 launcher 下次启动清理临时副本；如果文件仍被占用则警告并在后续启动重试。普通启动不会联网，也不会自动检查更新。

## 安装与手动更新

```sh
# 后端必须手动选择；可用值由当前官方 Release 动态列出
bin/llama-launcher install --backend cpu
bin/llama-launcher install --version b9637 --backend vulkan

# 只读检查，默认检查两个组件
bin/llama-launcher check-update
bin/llama-launcher check-update --component llama --json

# 默认依次更新 llama.cpp 和启动器
bin/llama-launcher update --yes
bin/llama-launcher update --component llama --backend cpu --yes
bin/llama-launcher update --component launcher --launcher-version v1.2.3 --yes
```

CLI 写入默认要求终端确认；stdin 不是交互终端时必须提供 `--yes`。同版本默认不操作，`--force` 可重装；降级默认拒绝，只有 `--allow-downgrade` 才允许。后续 llama.cpp 更新沿用已保存后端；新版不再提供该后端时不会自动切换，交互模式要求重新选择，非交互模式列出可用值并报错。

元数据来自 `api.github.com` 上的 `Snail-one/LlamaLc` 和 `ggml-org/llama.cpp` Releases。可用 `LLAMALC_GITHUB_TOKEN` 提高 API 限额，认证头只发送给 `api.github.com`。网络优先采用 `HTTP_PROXY`、`HTTPS_PROXY` 与 `NO_PROXY` 环境配置；Windows 未设置对应环境代理时，会读取当前用户在 Windows 网络设置中的手动代理、绕过列表和 PAC/WPAD 自动代理。URL 命中代理时先通过代理访问，API 请求或下载失败后自动直连重试一次；没有代理或命中绕过规则时直接访问。下载时仅在终端显示开始信息、访问方式、百分比、字节数、实时速度、回退信息和完成摘要，不写入持久化下载日志。所有下载必须是 HTTPS 且带 GitHub API SHA-256 digest；启动器还会和 Release 的 `SHA256SUMS.txt` 交叉校验。CUDA 后端会把匹配的运行库资产作为同一选择整体下载。

根目录固定为 `bin` 的上一级；模型目录、配置文件和 Router 文件位置也全部固定。`--root` 与 `--config` 已移除，传入时会直接报错，防止意外把文件写到其他位置。顶层帮助、子命令帮助、未知命令和版本查询都不会执行 server 探测或初始化磁盘。

探测成功后会同时打印实际执行的 server 文件与识别到的版本，例如：

```text
实际探测文件: E:\llama.cpp\data\llama.cpp\b9637-cpu\llama-b9637\llama-server.exe
已识别 llama.cpp: version: 10002 (a7312ae94)
```

## 交互菜单

双击 `bin/llama-launcher.exe`，或不带参数运行，即可进入中文菜单：

```text
llama.cpp Go 启动器
启动器版本: v1.0.0
llama.cpp: b10015 / cuda-13.3 — version: 10015 (abc123)

1. 单模型 API 服务
2. Embedding API
3. Rerank API
4. 生成手动 Router 配置
5. 多模型 Router
6. CLI 命令行聊天
7. 检查并更新 llama.cpp
8. 检查并更新启动器
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

配置优先级固定为：命令行 flags > `config/launcher.json` > 内置默认值。模型或可执行文件不存在、端口越界、GPU 层数或 pooling 非法时，启动器会在创建子进程前用中文报错。启动器会把 `server.api_key` 原子同步到私有的 `config/launcher.api-key`，再通过 `--api-key-file` 注入单模型、Embedding、Rerank 和 Router 服务，避免密钥出现在进程命令行；监听地址不是 localhost、loopback IP 或 Unix socket 时仍会检查有效认证参数，否则拒绝启动。

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

`server.api_key` 在示例配置中可留空；启动器会用密码学安全随机源生成并保存 128 位 key。手工配置必须至少 32 位、最长 8167 位，并且只能包含 ASCII 字母、数字、连字符和下划线。逗号和空白会被拒绝，避免上游将单个 key 重新解释为多个 key 或空 key 列表。长度上限依据[当前上游 HTTP 头限制](https://github.com/ggml-org/llama.cpp/blob/master/vendor/cpp-httplib/httplib.h)和[认证头处理实现](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/server-http.cpp)推导。

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

- `config/launcher.json`、`config/launcher.api-key`、手动 Router preset 和自动 Router preset 都固定在 `config/` 中；符号链接、junction 和重解析点会被拒绝，避免写入根目录外文件。
- `config/launcher.json` 和派生的 `config/launcher.api-key` 都包含明文 API key。Unix 上会把 `config/` 强制修正为 `0700`、两个文件修正为 `0600`；Windows 上会设置受保护 DACL，仅授予当前用户和 LocalSystem 完全控制。请勿提交或共享这些文件。
- 配置与 Router preset 先写入同目录临时文件并同步，再替换目标文件。覆盖失败不会先清空原文件；不带 `--force` 时仍拒绝覆盖手动配置。
- Router 会拒绝包含控制字符、换行或非法 section 分隔符的模型文件名和路径，防止 preset 注入。
- 启动器使用 `--api-key-file` 传递托管密钥，密钥不会出现在操作系统进程列表中；用户自行转发的 `--api-key` 仍会在命令预览中脱敏。
- 非本机监听必须配置认证，并会额外显示安全警告。上游的 `/models`、`/v1/models`、健康检查和 UI 静态资源不受 API key 保护；需要隐藏时请使用反向代理拦截。默认仍为 `127.0.0.1` 且关闭 Web UI。
- 自定义参数拥有 llama.cpp 的完整能力。不要在不可信环境中启用 `--tools`、MCP proxy 等高权限功能。
- 不要直接加载来源不明的模型。llama.cpp 官方建议在容器、虚拟机等隔离环境中处理不可信模型，参见 [llama.cpp Security](https://github.com/ggml-org/llama.cpp/security)。

## 进程与退出码

启动器通过参数数组直接创建进程，不经过 shell。子进程连接当前终端的 stdin/stdout/stderr，因此 CLI 交互、日志和 Ctrl+C 保持原生行为。以子命令方式运行时，`llama-server` 或 `llama-cli` 的退出码会由启动器原样返回。Windows 自更新时，最小 updater 只接受 `bin` 下由 launcher 创建的 `.llama-updater-new-*.exe` 和 `.llama-launcher-new-*.exe` 文件名，替换目标固定为同目录的正式 updater 和 launcher，不能通过参数指定任意目标路径。launcher 的残留清理也只删除 `bin` 下 `.llama-updater-run-*` 前缀的普通文件，拒绝目录、符号链接和重解析点。

## 旧布局说明

新版本不兼容旧配置位置，不会读取根目录的 `launcher.json`，也不会读取 `bin/router-models.ini` 或 `bin/router-models.auto.ini`。所有运行配置和 Router 文件统一位于根目录的 `config/` 中。

## 自动发版

推送形如 `v1.0.0` 的 Git tag 会触发 GitHub Actions：先运行测试和 `go vet`，再交叉构建 Windows/Linux 的 amd64、arm64 四个平台。Archive 固定命名为 `llama-launcher-<version>-<os>-<arch>.zip|tar.gz`，每个 archive 必须且只能包含 `llama.cpp/bin/llama-launcher[.exe]` 与 `llama.cpp/bin/llama-updater[.exe]`。`SHA256SUMS.txt` 覆盖四个平台 archive，工作流会校验 archive 结构和两个二进制中的嵌入版本后才发布。

```sh
git tag v1.0.0
git push origin v1.0.0
```

Dependabot 每周检查 Go Modules 与 GitHub Actions 更新。
