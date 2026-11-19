# Multi-platform Parse

[![CI and release](https://github.com/IwasakiYouko/Multi-platform-Parse/actions/workflows/ci.yml/badge.svg)](https://github.com/IwasakiYouko/Multi-platform-Parse/actions/workflows/ci.yml)
[GitHub Releases](https://github.com/IwasakiYouko/Multi-platform-Parse/releases) · [Actions](https://github.com/IwasakiYouko/Multi-platform-Parse/actions) · [第三方声明](THIRD_PARTY_NOTICES.md)

Go 主程序的多平台链接解析工具。接收 NapCat 的 OneBot v11 反向 Universal WebSocket 消息，解析分享链接、AV/BV 编号和分享卡片，按可自定义的平台消息区块模板下载并发送图片、封面或视频。内置全新重构的响应式中文控制面板，支持平台级模板配置、在线解析预览沙盒、群聊引用回复、代理分流、登录封禁保护与缓存管理。

除保留原生实现的哔哩哔哩（含 WBI 签名回退）外，其余平台（含抖音移动端设备池与日常回退）以 ParseHub **2.3.15** 为迁移基线。YouTube、Facebook、Snapchat 由 Go 调用固定版本的 yt-dlp/EJS/Deno；其他平台使用原生 Go 适配器，不需要部署外部服务。FFmpeg/ffprobe 负责本地媒体处理；运行镜像基于 **Alpine Linux (`alpine:3.24`)** 单容器构建，面向 **Linux amd64**，默认使用 Docker Engine 的 **host 网络**。

**v2.2.0 在双消息投递架构基础上实现全链路 24 平台零追踪标准直链净化（小红书默认输出 100% 零参数 `/explore/{id}`、小黑盒 API 分享外壳解包、知乎/视频号/贴吧/微博/豆瓣/YouTube 等全量归一）、默认 1080p 原画无损直传（`-c:v copy`）、纯 Go WebP 超长截图原尺寸保真、超长单图/无图文章自动追加消息 2 长文合并转发阅读卡，以及全新升级的 QQ NT 高保真仿真与直链净化对比控制台。从旧版升级只需保留 `app-data` 数据卷并更新镜像标签，配置将自动平滑迁移。**

## 快速开始：使用 GitHub Release

Release 提供由 GitHub Actions 构建的单容器 Alpine 镜像归档，无需登录 GHCR，也无需额外拉取任何第三方数据库容器，支持完整离线导入运行：

```bash
mkdir -p multiparse/certs
cd multiparse

RELEASE=https://github.com/IwasakiYouko/Multi-platform-Parse/releases/download/v2.2.0
for file in compose.yaml alpine-packages.txt multi-platform-parse-v2.2.0-linux-amd64.tar.gz SHA256SUMS; do
  curl --fail --location --remote-name "$RELEASE/$file"
done
curl --fail --location --output .env.example "$RELEASE/default.env.example"
sha256sum --check SHA256SUMS
docker load --input multi-platform-parse-v2.2.0-linux-amd64.tar.gz
cp .env.example .env
docker compose up -d --wait
```

查看首次随机密码与实际入口：

```bash
docker compose logs app
docker compose exec app cat /data/runtime.json
# 备用读取方式；首次修改密码后该文件删除
docker compose exec app cat /data/initial-admin.txt
```

- 默认用户名 **`admin`**。随机密码仅在管理员首次创建成功时输出一次日志，重启不重复输出。
- 首次登录必须修改密码，可以同时修改用户名；修改账号后撤销旧会话，需要重新登录。
- 默认通过 `::` 同时监听全部 IPv4 / IPv6 接口，需系统支持 IPv6。面板为 `http://服务器IP:10492`；IPv6 地址使用方括号，例如 `http://[::1]:10492`。
- 10492 被占用时依次尝试 10493、10494，直到找到可用端口。仅处理端口占用，不掩盖权限、地址或证书错误。最终地址以 `runtime.json` 为准。
- **默认全接口开放，日志包含初始密码。** 公网部署请启用 HTTPS/WSS、设置防火墙或管理来源限制，并保护日志。修改密码不会擦除 Docker 或外部日志系统已经保存的内容。

### 默认 Docker Compose 示例

以下是默认单容器配置，可保存为 `compose.yaml`，也可直接使用 Release 附件或仓库中的同名文件：

```yaml
name: multiparse

services:
  app:
    image: ghcr.io/iwasakiyouko/multi-platform-parse:${IMAGE_TAG:-2.2.0}
    network_mode: host
    restart: unless-stopped
    init: true
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    pids_limit: 256
    environment:
      DATA_DIR: /data
      CACHE_DIR: /media
      LISTEN_ADDR: ${LISTEN_ADDR:-::}
      START_PORT: ${START_PORT:-10492}
      EXTERNAL_URL: ${EXTERNAL_URL:-}
      TZ: ${TZ:-Asia/Shanghai}
    volumes:
      - app-data:/data
      - media:/media
      - ./certs:/certs:ro
    tmpfs:
      - /tmp:size=268435456,mode=1777
    healthcheck:
      test: ["CMD", "/usr/local/bin/multiparse", "health", "--data", "/data"]
      interval: 30s
      timeout: 8s
      start_period: 30s
      retries: 3
    stop_grace_period: 45s

volumes:
  app-data:
  media:
```

**从旧版升级至 v2.2.0**：先停止旧版服务（`docker compose down --remove-orphans`，**切勿加 `-v`**），使用新版 `compose.yaml` 与 `IMAGE_TAG=2.2.0` 执行 `docker compose up -d --wait`。启动时程序会自动迁移配置架构版本（含默认画质平滑升级至 `1080p`）并保留既有自定义策略。

### 可选添加项

以下配置按需要添加，不要求全部启用。

**1. 环境变量：版本与监听范围**

写入与 Compose 同目录的 `.env`：

```dotenv
IMAGE_TAG=2.2.0
LISTEN_ADDR=::
START_PORT=10492
EXTERNAL_URL=
TZ=Asia/Shanghai
```

仅允许本机 IPv4 时将 `LISTEN_ADDR` 改为 `127.0.0.1`；只监听 IPv4 全接口可设为 `0.0.0.0`。`LISTEN_ADDR`、`START_PORT`、`EXTERNAL_URL` 仅在首次初始化业务配置时导入；已有数据卷请通过面板修改，升级不会覆盖已保存的监听设置。

**2. HTTPS / WSS 证书**

默认已预留 `./certs:/certs:ro`。把 PEM 证书链和私钥放在 `certs/`，确保容器 UID 1000 可读，限制私钥权限。在面板启用 TLS，填写 `/certs/server.crt`、`/certs/server.key` 和对应的 `https://` 对外地址。保存前校验资源；失败保留旧配置。成功后面板和 OneBot 在同一端口改用 HTTPS/WSS，客户端需重新连接。

也可由可信反向代理终止 TLS；此时建议 Go 仅监听回环地址，只信任实际代理的来源 IP，不要信任全部转发头。

**3. 同机 NapCat 共享媒体卷**

默认 `file` 传输要求 NapCat 能读取相同绝对路径。在已有 NapCat Compose 中合并以下卷配置，不要照抄覆盖其其他设置：

```yaml
services:
  napcat:
    volumes:
      - multiparse-media:/media:ro
volumes:
  multiparse-media:
    external: true
    name: multiparse_media
```

默认项目名为 `multiparse`，对应卷名 `multiparse_media`。使用 `-p` 改过项目名时，应填写真实卷名。NapCat 还需具备文件读取权限；只有 host 网络而没有共享文件路径，不能发送本地文件。

**4. 跨机器 NapCat：签名 HTTP 媒体**

不增加容器或公开目录。在面板将媒体传输改为 `http`，配置 NapCat 可访问的 HTTPS 对外地址。媒体地址使用短期 HMAC 签名，最长 15 分钟且不超过缓存期限，支持范围读取；重启后旧临时地址失效，读取时重新检查平台和会话策略。

**5. 直接从 GHCR 拉取**

镜像地址：

```text
ghcr.io/iwasakiyouko/multi-platform-parse:2.2.0
```

本仓库镜像已公开，可匿名拉取。Release 的环境变量附件使用 `default.env.example` 名称，快速开始命令将其保存为本地 `.env.example` 后校验和使用。

## 消息区块变量与双层投递架构

v2.2.0 采用**「消息 1：作品导览名片 + 消息 2：单媒体直发 / 多媒体或长文合并转发」**双层投递结构：
- **消息 1**：发送高清无水印封面图（或单图原图）及模板渲染文本（默认模板为 `{title}\n{tags}\n{content}\n{url}`）。
- **消息 2**：若仅含 1 张图片且正文未超摘要上限则免重复发送；若为单个视频则直接独立发送（默认最高 `1080p` H.264 免重编码直传）；若包含多张图片、多个视频、实况图（静图+视频），或单图/无图长文超过摘要上限，自动通过 OneBot `send_group_forward_msg` / `send_private_forward_msg` 整合进 1 条**合并转发**卡片发出（超长原文按自然段智能切分节点，单节点 `<= 1500` 字符）。

可在面板「平台与消息模板」设置全局默认模板，或点击任意平台的「配置模板与策略」单独指定：

- **模板模式（`template_mode`）**：`inherit`（跟随全局模板）、`custom`（使用该平台独立模板）、`none`（不发送任何文字，仅发送图片/视频媒体）。
- **封面策略（`cover_mode`）**：`inherit`（跟随全局封面开关）、`on`（强制附带封面）、`off`（强制关闭封面）。

### 支持的模板变量

| 区块变量（自带格式前缀或括号） | 纯值变量（不含前缀，便于自定义拼接） | 展开说明 |
|---|---|---|
| `{title}` → `【...】` | `{raw_title}` | 作品标题（截断至 300 字符，B 站多 P 自动附分集标识） |
| `{tags}` → `#标签1 #标签2` | `{raw_tags}` | 作品话题标签（提取结构化标签与 `#话题` 并自动剥离正文尾部重复标签串；`{raw_tags}` 以 ` · ` 连接） |
| `{author}` → `作者：...` | `{raw_author}` | 作者昵称（截断至 100 字符） |
| `{content}` → 正文无前缀 | `{raw_content}` | 作品正文摘要（默认截断至 300 字符可配，自动净化零宽字符与内联追踪链接，与标题重复或尾部纯标签串自动去重） |
| `{url}` → `链接：...` | `{raw_url}` | 作品标准纯净直链（全平台解包分享外壳并剥离追踪指纹；小红书默认零参数 `/explore/{id}`，可选开启保留 `xsec_token`） |
| `{clean_url}` → `链接：...` | `{raw_clean_url}` | 始终输出 100% 零追踪参数的官方标准纯净直链 |
| `{duration}` → `时长：04:32` | `{raw_duration}` | 视频或音频作品时长（图片/纯文本作品自动为空并折叠整行） |
| `{platform}` → `[...]` | `{raw_platform}` | 带方括号的平台中文名称（如 `[哔哩哔哩]`、`[抖音]`；纯值变量输出 `哔哩哔哩`） |
| `{type}` → `[...]` | `{raw_type}` | 带方括号的内容类型（如 `[视频]`、`[图集]`、`[混合媒体]`、`[文章]`、`[音频]`、`[实况图]`、`[动态]`；纯值变量不含方括号） |
| `{media_stats}` → `共 ...` | — | 媒体数量摘要（如 `共 1 个视频`、`共 6 张图片`、`共 3 张实况图`） |

**智能空行折叠规则**：支持在模板中混合任意自定义文字、符号或 Emoji（例如 `🎬 【{raw_platform}】{raw_title}`）。若模板中某一行包含变量占位符，且该行引用的**所有**变量在当前作品中均为空（例如无作者信息，或正文因与标题重复而被去重），引擎会自动折叠整行，不会残留突兀的前缀文字或连续多余空行；纯静态装饰行（不含任何 `{...}` 变量）则原样保留。

例如，若希望某平台**仅输出标题**，只需将该平台的模板模式设为「自定义模板」，内容填写 `{title}`（或 `{raw_title}`）即可。面板内置「变量胶囊一键插入」与「实时聊天气泡预览」，也可在「解析沙盒与队列」中输入真实分享链接调用同步预览测试效果。

## 开发者使用例

### 从源码启动完整环境

需要 Git、Docker Engine 和 Docker Compose v2；本机运行 Go 检查需要 Go 1.27 或更新版本、FFmpeg/ffprobe。CI 和 Docker 构建固定 Go 1.27.3。三个 yt-dlp 平台另需固定的隔离 Python 运行时与 Deno，Alpine 镜像会自动安装；不需要安装 Python 机器人或 ParseHub。

```bash
git clone https://github.com/IwasakiYouko/Multi-platform-Parse.git
cd Multi-platform-Parse
mkdir -p certs
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
```

`compose.yaml` 是仅镜像部署配置，`compose.build.yaml` 才添加本地构建目标。修改源码后使用同一条带 `--build` 的命令重新构建。

本机需要执行这三个平台的实际后端验证时，使用 CPython 3.11–3.14 的 Linux amd64 环境（兼容 glibc 与 Alpine musl；若在 Alpine musl 本机运行请先执行 `apk add deno` 安装系统 Deno），在临时目录安装同版本工具。安装器会核对锁定附件的大小和 SHA-256，不修改系统 Python：

```bash
TOOLS_DIR="$(mktemp -d)"
python3 -m venv "$TOOLS_DIR/installer"
"$TOOLS_DIR/installer/bin/python" scripts/install_ytdlp_runtime.py \
  --venv "$TOOLS_DIR/yt-dlp" \
  --deno "$TOOLS_DIR/deno" \
  --docs "$TOOLS_DIR/licenses"
export YTDLP_PYTHON="$TOOLS_DIR/yt-dlp/bin/python"
export YTDLP_JS_RUNTIME="$TOOLS_DIR/deno"
"$YTDLP_PYTHON" -I scripts/check_ytdlp_runtime.py
```

锁定版本为 yt-dlp `2026.10.26.220202.dev0`、EJS `0.8.0`；glibc 环境下载锁定的 Deno `2.9.8`，Alpine musl 环境自动链接系统 `/usr/bin/deno`（或容器构建时使用 `--skip-deno` 直接指向 `/usr/bin/deno`）并核验内嵌 `.dep-v0` 依赖清单与随附许可归档 `deno-2.7.4-alpine-third-party.tar.gz`，附件和默认依赖见 `scripts/ytdlp-runtime.lock.json`。运行服务时不会自动下载或升级这些工具；缺少运行时会明确报错，不返回伪造的解析结果。

```bash
go test -race ./...
go vet ./...
go build -o multiparse ./cmd/multiparse
./multiparse version
docker compose -f compose.yaml -f compose.build.yaml config --quiet
```

### Go 调用原生解析器

本项目使用 Go `internal` 包。将下面的例子放在仓库内的 `cmd/resolve-example/main.go`，不能直接从其他 Go 模块导入这些包。此例只解析元信息，不下载媒体，也不发送 QQ 消息：

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "multiparse/internal/model"
    "multiparse/internal/parser"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
    defer cancel()
    result, err := parser.Resolve(ctx, model.DefaultConfig(), "https://bgm.tv/blog/1")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("%s | %s | %s\n", result.Platform, result.Title, result.SourceURL)
}
```

```bash
go run ./cmd/resolve-example
```

使用其他平台时替换输入链接。Bilibili 可直接传入 `av170001`、`AV170001`、BV 号、长链接、`b23.tv` 短链或带 `?p=2` 的分 P 链接；不存在的分 P 返回错误，不静默降级到 P1。平台策略、Cookie、代理和请求上下文仍然生效。`model.DefaultConfig()` 不会自动加载面板中保存的账号或代理设置。

### 在已登录面板中调用管理 API

完成密码登录和首次改密后，可在该面板同源浏览器上下文中调用管理 API。会话 Cookie 为 HttpOnly，不需要也不应读取它：

```javascript
const meResponse = await fetch('/api/me');
if (!meResponse.ok) throw new Error(`登录状态无效：${meResponse.status}`);
const me = await meResponse.json();
const response = await fetch('/api/parse/preview', {
  method: 'POST',
  headers: {'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf_token},
  body: JSON.stringify({url: 'https://bgm.tv/blog/1'})
});
if (!response.ok) throw new Error(await response.text());
console.log(await response.json());
```

`POST /api/parse` 提交异步下载转码任务并返回 **202** 和 `{id}`；`POST /api/parse/preview` 则同步返回脱敏元信息与根据当前生效模板渲染的文案预览，不下载落盘、不写缓存且不触发 OneBot 发送。以下接口均属于面板管理接口，不使用 OneBot Token 代替会话登录：

| 接口 | 用途 / 约束 |
|---|---|
| `GET /api/status` | 实际监听地址、OneBot 状态、任务与缓存统计 |
| `GET /api/config` | 读取脱敏完整配置 |
| `PUT /api/config` | 提交完整配置；仅修改监听、安全封禁、代理凭据、TLS 等高危字段时要求 `X-Current-Password` |
| `GET /api/platforms` | 平台 ID、域名与媒体类型 |
| `POST /api/parse/preview` | 同步解析元信息与模板渲染预览（自动剥离请求头与凭据，需要 CSRF） |
| `POST /api/parse` | 提交异步解析与媒体下载任务（需要 CSRF） |
| `GET /api/tasks` | 查询异步任务状态 |
| `GET /api/cache` | 查询当前有效缓存条目列表（含平台、标题、链接、文件数、体积与到期时间） |
| `DELETE /api/cache?key=...` | 按缓存键精确删除单条缓存及关联媒体文件（需要 CSRF） |
| `POST /api/cache/clear` | `{all:false}` 清过期，`{all:true}` 清全部（需要 CSRF） |
| `POST /api/proxy/test` | `{platform:"youtube"}` 测试选定平台的实际出口（需要 CSRF） |

配置中的 `********` 表示保持已保存的秘密，显式空值才清除。面板支持导出脱敏配置 JSON 并在新环境安全导入回填（自动保留目标实例现有的加密凭据，不覆盖掩码字段），但导出文件不能替代包含密钥的完整数据卷备份。

### 扩展平台解析器

- `internal/model`：平台 ID、域名、配置模型、模板模式和媒体结果结构。
- `internal/policy`：平台/群聊准入策略、URL 追踪参数清洗及消息区块模板渲染引擎。
- `internal/parser`：`Resolver`、`Register` 和现有原生适配器；新平台沿用 `context.Context`、受控 `http.Client` 和结构化媒体结果。
- `internal/netx`：代理选择、公网 IP 校验、DNS 和重定向约束。不要另建不受策略控制的下载客户端。
- `internal/media`：下载、完整性校验、备用 CDN 节点回退、FFmpeg/ffprobe、音轨合并和处理产物。
- `internal/extractor`：三个 yt-dlp 平台的元数据、保留下载上下文、隔离进程与受控网络网关；不得把作品网页当成视频文件下载。
- `internal/jobs`：策略、队列、并发、同步预览、缓存租约、OneBot 交付和过期清理。
- `internal/onebot`：反向 WebSocket、事件提取（含 `message_id`）、API 调用与消息段。
- `internal/web`、`internal/ui`：Argon2id 认证、IP 封禁、管理 API 与内置响应式前端资源。

## NapCat 接入与消息示例

1. 在面板「OneBot 与会话策略」页面生成 Token，保存本次显示的值。
2. 在 NapCat 添加反向 WebSocket **客户端**，使用事件和 API 共用的 **Universal** 连接。
3. 同机地址填写 `ws://127.0.0.1:10492/onebot/v11/ws`；如自动换端口，以面板和 `runtime.json` 为准。TLS 使用 `wss://`。
4. 客户端设置 `Authorization: Bearer <token>`，可在面板限制机器人 QQ 号。
5. 配置共享媒体卷或签名 HTTP 传输后，在允许的群聊发送 `av170001`、平台链接或分享卡片。

本工具是 WebSocket **服务端**，NapCat 是主动连接方。桥接容器的 `127.0.0.1` 指它自身，不能直接照抄宿主机地址。自动换端口不会自动修改 NapCat、书签或反向代理配置。

开启「群聊引用回复原消息」后，群内触发的图文或首条媒体消息会自动前置 `[CQ:reply,id=...]`（OneBot `reply` 消息段），在多人高频聊天群中清晰定位来源消息。解析结果先发送一条普通图文消息（`image` + 模板渲染后的 `text` 消息段），收到 OneBot 成功回执后再逐个发送独立的视频消息；若平台模板设为「不发送文字」且关闭封面、无其他图片，则直接发送视频，不发送空消息。

## 平台、策略与代理

当前注册 24 个平台：Bilibili、抖音、TikTok、小红书、微博、快手、Twitter/X、Instagram、YouTube、Facebook、Threads、贴吧、微信公众号、微信视频号、酷安、皮皮虾、最右、小黑盒、Snapchat、知乎、豆瓣、Bangumi、今日头条、Medium。其中 YouTube、Facebook、Snapchat 使用 yt-dlp 后端，其余使用 Go 适配器。准确的平台 ID、域名和内容类型见 `GET /api/platforms` 与 `internal/model/model.go`。

| 代理模式 | 中国大陆平台 | 海外平台 |
|---|---|---|
| `all` 全局走代理 | 代理 | 代理 |
| `direct` 全部不走代理（默认） | 直连 | 直连 |
| `overseas` 仅海外走代理 | 直连 | 代理 |

在面板配置 HTTP/HTTPS 类型的 HTTP 代理或 SOCKS5，支持认证。解析、短链、封面、视频和音轨沿用一致出口。直连不继承 `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY`；指定代理失败不回退直连。代理模式经代理访问固定 IP 的 Cloudflare DoH，验证公网地址后再连接目标，来源链接和重定向不能访问宿主机内网或云元数据。

## 缓存、安全与运维

默认缓存 **24 小时**，自写入完成起算，命中不续期。到期立即失效，后台每 30 秒及启动时补清理图片、视频、音轨、封面、缩略图、解析信息和关联任务记录；面板支持按条目精确删除单条缓存或一键清理过期/全部缓存。活动文件由租约保护，释放后删除。

默认同一来源 IP 在 15 分钟内密码错误五次后封禁一小时，跨重启保留。移除外部验证码组件后，面板执行严格的 `Content-Security-Policy`（`script-src 'self'`，禁止内联脚本、动态 `eval` 与 WASM 执行），配合 Argon2id 口令散列、HttpOnly 会话与同源 CSRF 令牌保护管理端。公网部署仍建议配置强密码、开启 TLS 并限制管理端口来源。

```bash
# 本地解封，不删除业务数据
docker compose exec app multiparse unban --data /data --ip 127.0.0.1

# 通过标准输入重置密码，避免密码出现在进程参数中
read -rs -p '新密码: ' PASSWORD; echo
printf '%s\n' "$PASSWORD" | docker compose exec -T app multiparse admin-reset --data /data --username admin
unset PASSWORD
```

备份时停止写入，完整保存 `app-data` 卷及权限；`app-data` 中的 `encryption.key` 不能遗漏，否则无法解密配置。SQLite 中的敏感配置使用 AES-GCM，密码使用 Argon2id，会话只存摘要。媒体卷可丢弃；不要使用 `docker compose down -v` 作为普通升级步骤。

## CI 与镜像发布

工作流定义：[`ci.yml`](.github/workflows/ci.yml)。

- PR 到 `main` 或推送 `main`：安装 FFmpeg 和固定提取运行时，运行离线 EJS 检查、全量竞态测试（`-race`）、`go vet`、编译、许可证收集、运行时与源码打包脚本单测、Compose 校验及 Alpine 非 root 离线容器冒烟检查；不发布镜像。
- 推送 `vMAJOR.MINOR.PATCH`：检查成功后构建单容器 Alpine 镜像，导出 `alpine-packages.txt` 与 `alpine-packages.tsv` 软件包清单，自动拉取并按 `APKBUILD` SHA-512 校验镜像中全部 GPL/LGPL/AGPL 软件包（含 `ffmpeg`、`busybox`、`gcc` 运行库、`x264`、`x265` 等）的精确对应源码包与补丁，推送版本标签、`latest` 和 `sha-<完整提交SHA>` 至 GHCR，并创建公开 GitHub Release（附离线镜像归档、`compose.yaml`、`default.env.example`、`alpine-packages.txt`、`SHA256SUMS`，以及对应源码资产 `alpine-packages.tsv`、`alpine-source-manifest.json`、`multi-platform-parse-v*-alpine-gpl-sources.tar.gz*` 与 `ALPINE-SOURCES-SHA256SUMS`）。

维护者发布示例：

```bash
git tag v2.1.0
git push origin v2.1.0
```

## 许可与引用

自有代码尚未声明统一开源许可证，公开仓库不等于为全部代码授予任意再分发许可。下列第三方代码和组件仍遵循各自原始许可证；完整声明见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。镜像在 `/usr/local/share/doc/multiparse/` 随附 Go 模块许可、`alpine-packages.txt` 及 Python 运行时许可与对应源码材料，重新分发时还需履行 FFmpeg、系统组件等对应的许可与源代码提供义务。

1. [BestBcz/BiliURL](https://github.com/BestBcz/BiliURL)：参考 OneBot 接入和消息展示行为；未直接复制其 AGPL 代码。
2. [z-mio/ParseHub](https://github.com/z-mio/ParseHub) 与 [参考克隆 parse_hub_bot](https://github.com/IwasakiYouko/parse_hub_bot)：原生解析适配器部分逻辑移植来源，保留 MIT 版权与许可声明。
3. [OneBot v11 标准](https://11.onebot.dev/) 与 [NapCatQQ](https://github.com/NapNeko/NapCatQQ)：反向 WebSocket、事件和消息段协议参考。
4. [goquery](https://github.com/PuerkitoBio/goquery)、[Gorilla WebSocket](https://github.com/gorilla/websocket)、[modernc SQLite](https://pkg.go.dev/modernc.org/sqlite)、[html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown)、[tls-client](https://github.com/bogdanfinn/tls-client)：Go 依赖，精确版本见 `go.mod` / `go.sum`。
5. [FFmpeg](https://ffmpeg.org/legal.html) 与 [Alpine Linux](https://alpinelinux.org/)：媒体与容器系统运行时及相关许可。
6. [Docker Compose 文档](https://docs.docker.com/compose/) 与 [GitHub 包权限和可见性说明](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)：部署、镜像访问与发布流程参考。
7. [SecuritySm 上游及 MIT 许可](https://github.com/YueHen14/skyland-auto-sign/tree/6e7115b5580377c842f50f05b0fa39ab079c17b1)：小黑盒设备配置相关适配的上游来源，保留 `Copyright (c) 2023 xxyz30` 与许可全文。
8. [yt-dlp](https://github.com/yt-dlp/yt-dlp)、[yt-dlp-ejs](https://github.com/yt-dlp/ejs)、[Deno](https://github.com/denoland/deno)：三个平台的固定提取与 JavaScript 运行时；分发版本、哈希和来源见运行时锁文件。
9. [xhshow](https://github.com/cloxl/xhshow)：小红书激活签名的 Go 移植来源，固定参考 `0.2.0`，保留 `Copyright (c) 2024 Cloxl` 与 MIT 许可。
