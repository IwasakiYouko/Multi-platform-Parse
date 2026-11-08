# Multi-platform Parse

[![CI and release](https://github.com/IwasakiYouko/Multi-platform-Parse/actions/workflows/ci.yml/badge.svg)](https://github.com/IwasakiYouko/Multi-platform-Parse/actions/workflows/ci.yml)
[GitHub Releases](https://github.com/IwasakiYouko/Multi-platform-Parse/releases) · [Actions](https://github.com/IwasakiYouko/Multi-platform-Parse/actions) · [第三方声明](THIRD_PARTY_NOTICES.md)

Go 主程序的多平台链接解析工具。接收 NapCat 的 OneBot v11 反向 Universal WebSocket 消息，解析分享链接、AV/BV 编号和分享卡片，下载并发送图片、封面或视频。内置中文控制面板，支持平台策略、代理、登录保护和缓存管理。

除保持既有实现的哔哩哔哩外，其余平台（含抖音移动端设备池与日常回退）以 ParseHub **2.3.15** 为迁移基线。YouTube、Facebook、Snapchat 由 Go 调用固定版本的 yt-dlp/EJS/Deno；其他平台使用 Go 适配器，不需要部署 ParseHub 服务。FFmpeg/ffprobe 负责本地媒体处理；默认使用官方自托管 Cap 与 Valkey 提供登录验证码，也可明确关闭验证码。当前镜像面向 **Linux amd64**，使用 Docker Engine 的 **host 网络**。

**v1.2.1 包含本次抖音移动端协议补齐与解析迁移。已有部署请同时更新应用与 Cap 镜像标签，并保留原数据卷；需要平台登录态时，请在控制面板配置自己的 Cookie。**

## 快速开始：使用 GitHub Release

Release 提供由 GitHub Actions 构建的应用与 Cap 镜像归档。此安装方式不要求登录 GHCR；第三方 Valkey 镜像仍需要访问其镜像仓库，因此不是完整离线安装包。

```bash
mkdir -p multiparse/certs
cd multiparse

RELEASE=https://github.com/IwasakiYouko/Multi-platform-Parse/releases/download/v1.2.1
for file in compose.yaml compose.no-cap.yaml multi-platform-parse-v1.2.1-linux-amd64.tar.gz SHA256SUMS; do
  curl --fail --location --remote-name "$RELEASE/$file"
done
curl --fail --location --output .env.example "$RELEASE/default.env.example"
sha256sum --check SHA256SUMS
docker load --input multi-platform-parse-v1.2.1-linux-amd64.tar.gz
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
- 默认启用 Cap，插桩等级为 **4**；启用时 Cap 未就绪则禁止登录，不会因服务故障自动绕过验证。完全不使用验证码时，使用下方独立示例显式关闭。
- **默认全接口开放，日志包含初始密码。** 公网部署请启用 HTTPS/WSS、设置防火墙或管理来源限制，并保护日志。修改密码不会擦除 Docker 或外部日志系统已经保存的内容。

### 默认 Docker Compose 示例

以下是默认配置，可保存为 `compose.yaml`。也可直接使用 Release 附件或仓库中的同名文件。应用与 Cap 必须使用相同的发行版本。

```yaml
name: multiparse

services:
  app:
    image: ghcr.io/iwasakiyouko/multi-platform-parse:${IMAGE_TAG:-1.2.1}
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
      CAP_ADMIN_KEY_FILE: /secrets/cap-admin.key
      LISTEN_ADDR: ${LISTEN_ADDR:-::}
      START_PORT: ${START_PORT:-10492}
      EXTERNAL_URL: ${EXTERNAL_URL:-}
      CAP_ENABLED: ${CAP_ENABLED:-true}
      CAP_URL: http://127.0.0.1:${CAP_PORT:-30192}
      TZ: ${TZ:-Asia/Shanghai}
    volumes:
      - app-data:/data
      - media:/media
      - secrets:/secrets
      - ./certs:/certs:ro
    tmpfs:
      - /tmp:size=268435456,mode=1777
    depends_on:
      valkey:
        condition: service_healthy
      cap:
        condition: service_started
    healthcheck:
      test: ["CMD", "/usr/local/bin/multiparse", "health", "--data", "/data"]
      interval: 30s
      timeout: 8s
      start_period: 90s
      retries: 3
    stop_grace_period: 45s

  cap:
    image: ghcr.io/iwasakiyouko/multi-platform-parse-cap:${IMAGE_TAG:-1.2.1}
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
      CAP_ADMIN_KEY_FILE: /secrets/cap-admin.key
      SERVER_HOSTNAME: 127.0.0.1
      SERVER_PORT: ${CAP_PORT:-30192}
      REDIS_URL: redis://127.0.0.1:${VALKEY_PORT:-30292}
      ENABLE_ASSETS_SERVER: "false"
    volumes:
      - secrets:/secrets:ro
    tmpfs:
      - /tmp:size=134217728,mode=1777
    depends_on:
      valkey:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "bun", "-e", "fetch('http://127.0.0.1:'+process.env.SERVER_PORT+'/health').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"]
      interval: 30s
      timeout: 8s
      start_period: 90s
      retries: 3
    stop_grace_period: 15s

  valkey:
    image: valkey/valkey:8.1.3-alpine@sha256:d827e7f7552cdee40cc7482dbae9da020f42bc47669af6f71182a4ef76a22773
    network_mode: host
    restart: unless-stopped
    command: ["valkey-server", "--bind", "127.0.0.1", "--port", "${VALKEY_PORT:-30292}", "--protected-mode", "yes", "--appendonly", "yes", "--maxmemory", "256mb", "--maxmemory-policy", "noeviction"]
    volumes:
      - cap-data:/data
    healthcheck:
      test: ["CMD", "valkey-cli", "-p", "${VALKEY_PORT:-30292}", "ping"]
      interval: 10s
      timeout: 3s
      retries: 5
    stop_grace_period: 15s

volumes:
  app-data:
  media:
  secrets:
  cap-data:
```

### 不带 Cap / Valkey 的 Docker Compose 示例

需要 **1.1.0 或更新版本**。以下配置只有 `app`，不会启动、拉取或访问 Cap/Valkey；使用用户名和密码登录。首次强制修改密码、会话与 CSRF 保护、失败计数及一小时封禁仍生效。关闭验证码会降低登录保护，不建议直接暴露到公网。

保存为独立的 `compose.no-cap.yaml`，**与默认 Compose 二选一，不要合并加载**。仓库和 Release 也提供同名文件：

```yaml
name: multiparse

services:
  app:
    image: ghcr.io/iwasakiyouko/multi-platform-parse:${IMAGE_TAG:-1.2.1}
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
      CAP_ADMIN_KEY_FILE: /secrets/cap-admin.key
      CAP_ENABLED: "false"
      CAP_URL: http://127.0.0.1:30192
      LISTEN_ADDR: ${LISTEN_ADDR:-::}
      START_PORT: ${START_PORT:-10492}
      EXTERNAL_URL: ${EXTERNAL_URL:-}
      TZ: ${TZ:-Asia/Shanghai}
    volumes:
      - app-data:/data
      - media:/media
      - secrets:/secrets
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
  secrets:
```

全新部署：

```bash
mkdir -p multiparse/certs
cd multiparse
curl --fail --location --output compose.no-cap.yaml \
  https://github.com/IwasakiYouko/Multi-platform-Parse/releases/download/v1.2.1/compose.no-cap.yaml
docker compose -f compose.no-cap.yaml up -d --wait
docker compose -f compose.no-cap.yaml logs app
```

`CAP_ENABLED: "false"` 只在首次初始化导入，存为 `security.cap_disabled=true`。`secrets` 卷只保留本地初始化密钥，以便以后恢复默认部署；保留该卷或 `CAP_URL` 配置不会启动或连接 Cap。

**已有部署切换**：先升级至 1.2.1，在面板「登录安全」勾选「关闭 Cap CAPTCHA」并输入当前密码保存，再停止原 Compose（`docker compose down`，不要加 `-v`），使用相同项目名启动本例。仅删除 Cap 容器或修改环境变量，不会覆盖已有配置，也不会让启用验证码的旧实例自动改成密码登录。

**重新启用验证码**：保留原数据卷，先启动默认三服务 Compose，确认 Cap/Valkey 可用，再在面板取消关闭选项并保存。后端先完成 Cap 初始化与等级校验才提交启用；失败时保留原配置，不假装已经启用保护。使用自定义内部端口时，保持面板 Cap 地址与部署一致。

### 可选添加项

以下配置位于默认示例下方，按需要添加，不要求全部启用。

**1. 环境变量：版本、监听范围和内部端口**

写入与 Compose 同目录的 `.env`：

```dotenv
IMAGE_TAG=1.2.1
LISTEN_ADDR=::
START_PORT=10492
EXTERNAL_URL=
CAP_ENABLED=true
CAP_PORT=30192
VALKEY_PORT=30292
TZ=Asia/Shanghai
```

仅允许本机 IPv4 时将 `LISTEN_ADDR` 改为 `127.0.0.1`；只监听 IPv4 全接口可设为 `0.0.0.0`。Cap/Valkey 内部端口冲突需修改各自变量，10492 的自动递增不会替它们换端口。

`LISTEN_ADDR`、`START_PORT`、`EXTERNAL_URL`、`CAP_ENABLED` 仅在首次初始化业务配置时导入。已有数据卷请通过面板修改，升级不会覆盖已保存的监听或验证码设置。默认 Compose 中仅设置 `CAP_ENABLED=false` 不会删除 Cap/Valkey 服务；需要单容器部署时，使用上方独立配置。`IMAGE_TAG` 和辅助服务端口属于 Compose 配置，修改辅助端口后还需保持面板中的 Cap 服务地址一致。

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
ghcr.io/iwasakiyouko/multi-platform-parse:1.2.1
ghcr.io/iwasakiyouko/multi-platform-parse-cap:1.2.1
```

本仓库的两个镜像已公开，可匿名拉取。若自行 fork 后新建 GHCR 包，默认可能为私有，公开源码不会自动公开包；仓库所有者可在 **Package settings → Change visibility → Public** 开放匿名拉取。私有包的授权用户需使用具有 `read:packages` 权限的令牌执行 `docker login ghcr.io`。不要把令牌写入 Compose、README 或提交记录。Release 的环境变量附件使用 `default.env.example` 名称，快速开始命令将其保存为本地 `.env.example` 后校验和使用。

## 开发者使用例

### 从源码启动完整环境

需要 Git、Docker Engine 和 Docker Compose v2；本机运行 Go 检查需要 Go 1.27 或更新版本、FFmpeg/ffprobe。CI 和 Docker 构建固定 Go 1.27.3。三个 yt-dlp 平台另需固定的隔离 Python 运行时与 Deno，镜像会自动安装；不需要安装 Python 机器人或 ParseHub。

```bash
git clone https://github.com/IwasakiYouko/Multi-platform-Parse.git
cd Multi-platform-Parse
mkdir -p certs
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
```

`compose.yaml` 是仅镜像部署配置，`compose.build.yaml` 才添加本地构建目标。修改源码后使用同一条带 `--build` 的命令重新构建，不要误以为仅执行默认 Compose 就会使用工作区代码。

本机需要执行这三个平台的实际后端验证时，使用 CPython 3.11–3.14 的 Linux amd64 环境，在临时目录安装同版本工具。安装器会核对锁定附件的大小和 SHA-256，不修改系统 Python：

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

锁定版本为 yt-dlp `2026.10.26.220202.dev0`、EJS `0.8.0`、Deno `2.9.8`，附件和默认依赖见 `scripts/ytdlp-runtime.lock.json`。运行服务时不会自动下载或升级这些工具；缺少运行时会明确报错，不返回伪造的解析结果。

```bash
go test -race ./...
go vet ./...
go build -o multiparse ./cmd/multiparse
./multiparse version
docker compose -f compose.yaml -f compose.build.yaml config --quiet
```

默认完整服务需要正确配置自托管 Cap、Valkey 和 `CAP_ADMIN_KEY_FILE`；启用时不会因调试或故障跳过验证。明确选择无验证码模式时，使用上方单容器配置。不论哪种模式，都不要让两个实例共用同一管理数据目录。

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

先正常完成 Cap 登录和首次改密，再在该面板同源浏览器上下文中执行以下示例。会话 Cookie 为 HttpOnly，不需要也不应读取它：

```javascript
const meResponse = await fetch('/api/me');
if (!meResponse.ok) throw new Error(`登录状态无效：${meResponse.status}`);
const me = await meResponse.json();
const response = await fetch('/api/parse', {
  method: 'POST',
  headers: {'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf_token},
  body: JSON.stringify({url: 'https://bgm.tv/blog/1'})
});
if (!response.ok) throw new Error(await response.text());
const {id} = await response.json();
console.log('已提交任务：', id);
const tasks = await fetch('/api/tasks').then(r => r.json());
console.log(tasks.find(task => task.id === id));
```

`POST /api/parse` 成功返回 **202** 和 `{id}`，不代表媒体处理已经完成。管理入口提交的任务没有 QQ 发送目标；需要 QQ 交付时由 OneBot 消息触发。以下接口均属于面板管理接口，不使用 OneBot Token 代替会话登录。

| 接口 | 用途 / 约束 |
|---|---|
| `GET /api/status` | 实际监听地址、OneBot 状态、任务与缓存状态 |
| `GET /api/config` | 读取脱敏完整配置 |
| `PUT /api/config` | 提交完整配置，不是局部 PATCH；敏感修改另需 `X-Current-Password` |
| `GET /api/platforms` | 平台 ID、域名与媒体类型 |
| `GET /api/tasks` | 查询异步任务状态 |
| `POST /api/cache/clear` | `{all:false}` 清过期，`{all:true}` 清全部；需要 CSRF |
| `POST /api/proxy/test` | `{platform:"youtube"}` 测试选定平台的实际出口；需要 CSRF |

配置中的 `********` 表示保持已保存的秘密，显式空值才清除。导出 JSON 为脱敏配置，不能替代完整备份。不要将真实 Cookie、Token、日志或解析后的短期媒体地址作为测试夹具提交。

### 扩展平台解析器

- `internal/model`：平台 ID、域名、配置和媒体结果结构。
- `internal/parser`：`Resolver`、`Register` 和现有原生适配器；新平台沿用 `context.Context`、受控 `http.Client` 和结构化媒体结果。
- `internal/netx`：代理选择、公网 IP 校验、DNS 和重定向约束。不要另建不受策略控制的下载客户端。
- `internal/media`：下载、FFmpeg/ffprobe、音轨合并和处理产物。
- `internal/extractor`：三个 yt-dlp 平台的元数据、保留下载上下文、隔离进程与受控网络网关；不得把作品网页当成视频文件下载。
- `internal/jobs`：策略、队列、并发、缓存租约、发送和清理。
- `internal/onebot`：反向 WebSocket、事件提取、API 调用与消息段。
- `internal/web`、`internal/ui`：认证、管理 API、内置前端和官方 Cap 资源。

新增适配器需要同时维护平台目录、注册入口、配置、媒体类型和确定性的行为回归。只取得标题不等于成功取得视频。原生 HTML 适配器不执行网页 JavaScript；播放器挑战仅由固定 EJS/Deno 受限执行，不启用远程组件或用户插件。FFmpeg 只处理已经下载的本地文件，不直接访问来源 URL。实网验证应单独运行，不能要求 CI 持有真实平台账号。

## NapCat 接入与消息示例

1. 在面板 OneBot 页面生成 Token，保存本次显示的值。
2. 在 NapCat 添加反向 WebSocket **客户端**，使用事件和 API 共用的 **Universal** 连接。
3. 同机地址填写 `ws://127.0.0.1:10492/onebot/v11/ws`；如自动换端口，以面板和 `runtime.json` 为准。TLS 使用 `wss://`。
4. 客户端设置 `Authorization: Bearer <token>`，可在面板限制机器人 QQ 号。
5. 配置共享媒体卷或签名 HTTP 传输后，在允许的群聊发送 `av170001`、平台链接或分享卡片。

本工具是 WebSocket **服务端**，NapCat 是主动连接方。桥接容器的 `127.0.0.1` 指它自身，不能直接照抄宿主机地址。自动换端口不会自动修改 NapCat、书签或反向代理配置。

支持文字、CQ、JSON/XML 分享卡片、share 消息段及混合消息。没有来源链接的普通上传视频不能反向恢复原平台地址。默认同一机器人、同一群或私聊会话在成功发送后冷却 30 秒，可设为 0 关闭；重复处理中请求被抑制，失败不占用成功冷却。进程重启会取消中断任务，不自动重发可能已经送达的消息；Token 轮换使旧连接失效。

解析结果先发送一条普通图文消息：可用图片或封面使用 `image` 消息段，原有解析文案使用 `text` 消息段，放在同一个 `message` 数组中。收到 OneBot 成功回执后，再逐个发送独立的视频消息，不使用合并转发或并发发送。没有图片时先发文案，没有视频时只发图文；关闭文案与封面且没有其他图片时直接发视频，不发送空消息。展示开关及视频原生缩略图行为保持不变。

图文发送失败会停止后续视频；视频发送失败会报告对应步骤，不自动重发已经成功的图文。HTTP 模式的图片、封面、视频与缩略图均沿用带签名的媒体地址；文件模式仍要求 NapCat 可以访问共享媒体文件。

## 平台、策略与代理

当前注册 24 个平台：Bilibili、抖音、TikTok、小红书、微博、快手、Twitter/X、Instagram、YouTube、Facebook、Threads、贴吧、微信公众号、微信视频号、酷安、皮皮虾、最右、小黑盒、Snapchat、知乎、豆瓣、Bangumi、今日头条、Medium。其中 YouTube、Facebook、Snapchat 使用 yt-dlp 后端，其余使用 Go 适配器。准确的平台 ID、域名和内容类型见 `GET /api/platforms` 与 `internal/model/model.go`。

平台可独立开启、关闭。平台级 NSFW 拒绝独立保存，批量开启平台不会取消它；这不是 AI 逐条审核。支持自定义全局/平台拒绝文案、`{platform}` / `{reason}` 占位符或静默拒绝。平台关闭、NSFW 拒绝和会话权限任一不通过即拒绝；缓存读取及发送前仍重查策略。已提交给 NapCat 的消息无法保证拦截或撤回。群白名单非空时优先使用白名单，否则应用黑名单；可单独关闭私聊或全局自动解析。

| 代理模式 | 中国大陆平台 | 海外平台 |
|---|---|---|
| `all` 全局走代理 | 代理 | 代理 |
| `direct` 全部不走代理（默认） | 直连 | 直连 |
| `overseas` 仅海外走代理 | 直连 | 代理 |

在面板配置 HTTP/HTTPS 类型的 HTTP 代理或 SOCKS5，支持认证。平台归属可调整，依据平台而非 CDN 地理位置；抖音和 TikTok 分开分类。解析、短链、封面、视频和音轨沿用一致出口。直连不继承 `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY`；指定代理失败不回退直连。代理模式经代理访问固定 IP 的 Cloudflare DoH，验证公网地址后再连接目标，因此代理必须能访问该 DNS 服务。来源链接和重定向不能访问宿主机内网或云元数据。

平台可能要求有效 Cookie、区域访问或授权。小红书分享参数不能随意删除；知乎可能需要含 `d_c0` 的 Cookie；视频号依赖元宝授权 Cookie，该 Cookie 仅发往元宝域名。DRM、付费墙、私密内容和平台风控不通过伪造成功或权限绕过处理。

迁移保留了平台专属的链接清理、签名、回退、动图和文章结构。`markdown_content` 保存富文本语义，`content` 保存纯文本；LivePhoto 使用一个 `livephoto` 媒体对象关联图片 URL 与 `video_url`，不会在解析阶段丢失配对。OneBot 仍先发图片与文案并等待成功回执，再发伴随视频；实际 GIF 作为图片发送，MP4 动画作为视频发送，文案沿用既有显示长度限制。

新实现不会复制参考仓库内置的共享登录 Cookie。需要登录的平台应配置自己的有效凭据。迁移平台使用独立缓存版本，避免旧解析结果掩盖切换；哔哩哔哩、抖音沿用原缓存键和解析路径。

yt-dlp 的网络访问经过每次调用独立的认证回环网关，沿用同一平台代理、公网 IP 校验与目标策略；隔离启动器拒绝直接外连、任意子进程和联网 FFmpeg。超时、文件与 CPU 限制之外，工作目录大小及 RSS 使用采样检查，并非完整操作系统沙箱。Medium 的 Chrome 150 TLS/HTTP2 指纹请求同样通过受控网关，不等同于仅修改 User-Agent。

## 缓存、安全与运维

默认缓存 **24 小时**，自写入完成起算，命中不续期。到期立即失效，后台每 30 秒及启动时补清理图片、视频、音轨、封面、缩略图、解析信息和关联任务记录。活动文件由租约保护，释放后删除；缩短期限会重新判断已有记录，延长不复活过期缓存。账号、配置和封禁不属于媒体缓存，也不保证清除 NapCat/QQ 自行保存的副本。

媒体默认单文件 80 MiB、最长 1500 秒、最多 30 张图片、下载并发 2、转码并发 1、磁盘上限 2 GiB，可在面板调整。兼容媒体直接处理，必要时转为 H.264/AAC；HLS 在受限下载器中本地化，FFmpeg 不直接联网。

默认同一来源 IP 在 15 分钟内密码错误五次后封禁一小时，重启保留，无验证码模式也不例外。开启 Cap 时，验证码错误单独限速，解出的令牌不可重放；等级 4 对应官方 `obfuscationLevel`，不是通用计算难度。官方插桩要求内联脚本、动态 JavaScript 和 WASM；CSP 因而保留相关执行能力，应用不把用户内容当作 HTML 或脚本执行。关闭 Cap 时不加载验证码组件，但不应因此降低密码强度或扩大管理入口访问范围。

```bash
# 本地解封，不删除业务数据
docker compose exec app multiparse unban --data /data --ip 127.0.0.1

# 通过标准输入重置密码，避免密码出现在进程参数中
read -rs -p '新密码: ' PASSWORD; echo
printf '%s\n' "$PASSWORD" | docker compose exec -T app multiparse admin-reset --data /data --username admin
unset PASSWORD
```

重置密码至少 12 字节，撤销旧会话，并要求下次登录再次修改。备份时停止写入，完整保存 `app-data`、`secrets`、`cap-data` 卷及权限；`app-data` 中的 `encryption.key` 不能遗漏，否则无法解密配置。SQLite 中的敏感配置使用 AES-GCM，密码使用 Argon2id，会话只存摘要。媒体卷可丢弃；不要使用 `docker compose down -v` 作为普通升级步骤。

升级应先备份，再选择相同版本的应用/Cap 镜像；使用 Release 归档时加载新归档、更新 `IMAGE_TAG`，然后 `docker compose up -d --wait`。只覆盖配置文件不会自动迁移所有外部 NapCat 地址或证书。

## CI 与镜像发布

工作流定义：[`ci.yml`](.github/workflows/ci.yml)。

- PR 到 `main` 或推送 `main`：安装 FFmpeg 和固定提取运行时，运行离线 EJS 检查、全量竞态测试、`go vet`、编译、许可证收集、发布工具测试、两种 Compose 校验及 Debian 非 root 镜像运行检查；不发布镜像。
- 推送 `vMAJOR.MINOR.PATCH`：检查成功后构建镜像、收集对应 Debian 源码，再推送去掉 `v` 的版本标签、`latest` 和 `sha-<完整提交SHA>`；最后创建公开 GitHub Release。
- Release 附镜像归档、Compose、环境变量示例、校验和、准确的 Debian 软件包清单及对应源码分卷；每次镜像发布均有同版本源码材料，不以旧版材料代替新版。
- 仅使用仓库的 `GITHUB_TOKEN`。包写权限只给镜像作业，Release 写权限只给发布作业；第三方 Actions 固定到提交 SHA。

维护者发布示例：

```bash
git tag v1.2.1
git push origin v1.2.1
```

版本标签须与 `cmd/multiparse/main.go` 中的应用版本同步；新发行版应同时更新仓库默认 `IMAGE_TAG` 和文档示例。不要覆盖已发布版本来代替新版本发布。

验证边界：CI 的受控回归不是所有平台实网成功的保证。开发阶段已验证真实 Bangumi 图片、YouTube 视频、外部 OneBot 客户端交付、Cap 登录、双栈、代理和缓存清理；没有真实 QQ 账号验收最终收件。当前测试出口访问 Bilibili 曾返回 HTTP 412，AV/BV 和分 P 回归通过不等于该出口已成功下载视频。

### 镜像对应源码与许可证

两个运行镜像在 `/usr/local/share/doc/multiparse/` 附带 Go 模块、Go 工具链、浏览器资源和适配代码的许可材料。应用镜像中的 `debian-packages.tsv` 记录实际安装的 Debian 二进制及源包版本。

同版本 Release 另附 `debian-source-manifest.json`、`debian-packages.tsv`、`DEBIAN-SOURCES-SHA256SUMS` 和 `multi-platform-parse-v版本-debian-sources.tar.gz.partNNN`。下载全部源码分卷及清单后，可在独立目录校验并解包：

```bash
sha256sum --check DEBIAN-SOURCES-SHA256SUMS
cat multi-platform-parse-v1.2.1-debian-sources.tar.gz.part* | tar -xz
```

Debian 源码取自 Snapshot 的准确源包版本，下载时核对大小、Snapshot 哈希和 Debian 描述文件的 SHA-256。应用镜像还在 `/usr/local/share/doc/multiparse/ytdlp-runtime/` 附带 Python 包清单、许可证、Mutagen 对应源码及 Deno 第三方许可材料。Deno 清单是锁定依赖的保守集合，不是精确链接二进制 SBOM。上述材料不构成对镜像所有组件的法律保证。

## 许可与引用

自有代码尚未声明统一开源许可证，公开仓库不等于为全部代码授予任意再分发许可。下列第三方代码和组件仍遵循各自原始许可证；完整声明见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) 与 [`internal/web/assets/NOTICE`](internal/web/assets/NOTICE)。镜像随附相关声明，重新分发时还需履行 FFmpeg、系统组件等对应的许可与源代码提供义务。

1. [BestBcz/BiliURL](https://github.com/BestBcz/BiliURL)：参考 OneBot 接入和消息展示行为；未直接复制其 AGPL 代码。
2. [z-mio/ParseHub](https://github.com/z-mio/ParseHub) 与 [参考克隆 parse_hub_bot](https://github.com/IwasakiYouko/parse_hub_bot)：原生解析适配器部分逻辑移植来源，保留 MIT 版权与许可声明。
3. [OneBot v11 标准](https://11.onebot.dev/) 与 [NapCatQQ](https://github.com/NapNeko/NapCatQQ)：反向 WebSocket、事件和消息段协议参考。
4. [Cap](https://github.com/tiagorangel1/cap) 与 [Cap 文档](https://capjs.js.org/)：官方自托管验证码、插桩及内置浏览器资源。
5. [goquery](https://github.com/PuerkitoBio/goquery)、[Gorilla WebSocket](https://github.com/gorilla/websocket)、[modernc SQLite](https://pkg.go.dev/modernc.org/sqlite)、[html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown)、[tls-client](https://github.com/bogdanfinn/tls-client)：Go 依赖，精确版本见 `go.mod` / `go.sum`。
6. [FFmpeg](https://ffmpeg.org/legal.html)、[Debian 软件包源代码](https://sources.debian.org/)、[Valkey](https://valkey.io/)：媒体与系统运行时及相关许可。
7. [Docker Compose 文档](https://docs.docker.com/compose/) 与 [GitHub 包权限和可见性说明](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)：部署、镜像访问与发布流程参考。
8. [SecuritySm 上游及 MIT 许可](https://github.com/YueHen14/skyland-auto-sign/tree/6e7115b5580377c842f50f05b0fa39ab079c17b1)：小黑盒设备配置相关适配的上游来源，保留 `Copyright (c) 2023 xxyz30` 与许可全文。
9. [yt-dlp](https://github.com/yt-dlp/yt-dlp)、[yt-dlp-ejs](https://github.com/yt-dlp/ejs)、[Deno](https://github.com/denoland/deno)：三个平台的固定提取与 JavaScript 运行时；分发版本、哈希和来源见运行时锁文件。
10. [xhshow](https://github.com/cloxl/xhshow)：小红书激活签名的 Go 移植来源，固定参考 `0.2.0`，保留 `Copyright (c) 2024 Cloxl` 与 MIT 许可。
