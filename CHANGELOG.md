# 变更记录

## 2.2.0

- **24 平台零追踪标准直链深度净化**：
  - **小红书默认 100% 零参数直链**：默认剥离 `xsec_source` 与 `xsec_token`，统一输出 `https://www.xiaohongshu.com/explore/{id}`（兼容 `/user/profile/{uid}/{id}` 路径归一，同时保留解析器内部上游请求的 `xsec_token` 确保匿名解析零风控）；后台新增可选开关 `xhs_keep_token`（默认关闭，开启时仅保留 `xsec_token` 供桌面网页免登录直开，仍强制剔除 `xsec_source`）。
  - **小黑盒 API 分享外壳解包**：将 `https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id={id}` 与移动端域名统一重写为 `https://www.xiaoheihe.cn/app/bbs/link/{id}`。
  - **全平台域名与路由归一**：覆盖微信视频号（`channels.weixin.qq.com` / `exportkey` → `https://weixin.qq.com/sph/{id}`）、知乎（解析 `zhihu.link` 短链重定向、`/appview/p/{id}` → `https://zhuanlan.zhihu.com/p/{id}`）、百度贴吧（剔除 `see_lz=0` 与 `pn=1`）、微博（`video.weibo.com/show?fid=...` → `weibo.com/tv/show/...` 及标准博文直链）、豆瓣（解包 `/doubanapp/dispatch?uri=...`）、酷安（`coolapk1s.com` 归一）、今日头条、抖音、TikTok、B 站（剔除 `t=0` / `start_progress`，动态归一 `/opus/{id}`）、YouTube（`youtu.be` 归一并剔除 `&list=RD...` 电台追踪）等全部 24 个注册平台，并同步清洗 `{content}` 正文内嵌链接与扩充 30+ 追踪参数黑名单。
- **媒体处理画质与吞吐全面升级**：
  - **默认视频画质升级至 `1080p`**：`CurrentConfigSchema` 升级至 `3`，全新安装默认目标画质由 `720p` 升级至 `1080p`（同时保留旧版已显式保存的自定义画质设定），符合规范的 1080p H.264 (`yuv420p`) 原画流直接 `-c:v copy` 毫秒级无损直传，免除 720p 重编码画质折损与 CPU 开销。
  - **纯 Go 原生 WebP 解码与超长截图无损保真**：引入 `golang.org/x/image/webp` 原生解码静态 WebP（并支持动图 WebP `ANMF` 首帧提取），在 50MP 安全边界内支持最高 `16384px` 超长截图/条漫无损直出（修复原 FFmpeg `4096px` 缩放导致的文字模糊，且不再占用 `TranscodeConcurrency` 视频转码槽位）。
  - **多平台流优选与容灾增强**：皮皮虾优先提取无水印 `origin_video_download` / `video_high`；B 站自动降权 `*.mcdn.bilivideo.*` / `*.szbdyd.com` 慢速 PCDN 节点并新增 `AudioBackupURLs` 音轨多源容灾回退；兜底视频抽帧自动跳过片头黑场（`0.8s–1.0s` 智能寻帧 + `0s` 回退）并提升至 `1280px` 高清封面。
- **产品体验与模板能力迭代**：
  - **超长单图/无图文章自动合并转发全文阅读**：正文超出摘要字数上限（`SummaryLength`）的单图或纯文本长文自动补发「消息 2 · 长文合并转发阅读卡」，按自然段落智能切分阅读节点（单节点 `<= 1500` 字符）。
  - **新增时长变量与全平台标签/作者补全**：新增 `{duration}` / `{raw_duration}` 模板变量；补全小黑盒、知乎、快手、贴吧、最右、Medium 原生 `{tags}` 话题提取及贴吧、皮皮虾、Medium `{author}` 作者提取；支持 B 站图文动态与微博 `MixMediaInfo` 实况图（`livephoto`）配对；自动剔除标题与正文中的零宽不可见字符并折叠多余空行。
- **管理控制台视觉与诊断升级（v2.2.0）**：沙盒新增「原始链接 → 纯净直链」剥离参数可视化对比器与媒体流明细胶囊；升级高保真 QQ NT 聊天气泡与可点击展开的合并转发内部节点预览器；补齐 24 平台品牌色徽标、缓存配额三色预警进度条、任务耗时与状态计数、任务/缓存一键沙盒重放及全表单密码显隐切换。

## 2.1.0

- **小红书视频与图片平台无损直出**：优先读取 `originVideoKey` 直连 `sns-video-bd.xhscdn.com` 无水印原始上传流（多节点容灾回退，无源片时优选最高分辨率流），图片与封面切换至 `ci.xiaohongshu.com/{key}?imageView2/2/w/format/png` 无水印原图管道并支持最高 50MP 相机直出画幅；所有原生平台视频优先采用官方无水印高清封面替代首帧截图。
- **全平台标准直链重构与严格参数白名单**：升级链接净化策略，解析短链（如 `b23.tv`、`v.douyin.com`、`xhslink.com`、`v.kuaishou.com`、`t.co`）后直接还原为各平台纯净官方标准直链，并采用域名级严格参数白名单（B 站仅保留非 1 的 `p` 与 `t`，小红书仅保留防 404 的 `xsec_token` / `xsec_source` 并剥离 `appuid` 等一切追踪与账号指纹参数）；修复旧配置升级时布尔零值覆盖问题。
- **重构双消息投递架构与多媒体合并转发**：统一为「消息 1：高清无水印封面 + 标题 + Tag（`{tags}` / `{raw_tags}`）+ 正文摘要 + 纯净链接」与「消息 2：单视频直发 / 多图·多视频·实况图 OneBot 合并转发（`send_group_forward_msg` / `send_private_forward_msg`）」；单张图片作品仅发送消息 1 避免重复刷图，图视混排合并转发失败时自动兜底为图片合并转发加视频独立补发。
- **全平台话题标签提取与正文智能摘要**：新增全平台标签提取并自动剥离正文末尾重复堆砌的话题标签串；支持消息 1 正文摘要字数上限配置，超长原文自动完整收录进消息 2 合并转发首节点；B 站多 P 视频标题自动补充 `P{n}/{total}` 分集标识。
- **全链路业务体验优化**：新增同作品异链的「作品级标准键缓存复用与防刷屏冷却」、单条消息最大解析链接数限制、免解析指令（`-np` / `不解析` / `/` 前缀）与聊天记录卡片防套娃过滤、超长/超大视频优雅降级发送导览名片卡、处理中轻量贴表情回应（`set_msg_emoji_like`）、群聊失败静默降噪开关，以及后台平台 Cookie 失效主动预警与双消息仿真预览。

## 2.0.0

- **平台级消息区块变量与自定义模板引擎**：将解析文案拆分为带前缀的区块变量（`{title}`、`{author}`、`{content}`、`{url}`、`{clean_url}`、`{platform}`、`{type}`、`{media_stats}`）与纯值原子变量（`{raw_title}`、`{raw_author}`、`{raw_content}`、`{raw_url}`、`{raw_clean_url}`、`{raw_platform}`、`{raw_type}`）。支持全局默认消息模板，并允许每个平台独立设置模板模式（跟随全局 / 自定义模板 / 不发送文字）与封面图策略（跟随全局 / 强制显示 / 强制关闭）。
- **智能空行折叠与文案优化**：当模板某一行引用的所有变量均为空值时自动折叠整行（不残留自定义前缀或多余空行）；当标题与正文完全重复时自动去重正文；新增链接追踪参数清洗开关（自动剔除 `utm_*`、`spm_id_from`、`vd_source`、`share_*`、`igsh` 等分享追踪参数）与群聊引用回复原消息开关（OneBot `reply` 消息段）。
- **彻底移除 Cap 验证码与附属容器**：移除 Cap widget/WASM 前端资源、`cap-run` 子命令、`/captcha/` 路由、`cap_replays` 数据表、`compose.no-cap.yaml` 及 `-cap` 镜像，部署精简为无需 Valkey 的单容器架构。保留 Argon2id 密码散列、首次随机密码强制修改、HttpOnly 会话、同源与 CSRF 校验及持久化 IP 登录失败封禁，并将前端 `Content-Security-Policy` 收紧为 `script-src 'self'`（移除 `'unsafe-inline'`、`'unsafe-eval'` 与 `'wasm-unsafe-eval'`）。支持从 v1.x 数据卷平滑升级。
- **控制面板视觉与交互重构**：重构为响应式双栏工作台（支持浅色/深色主题记忆），重整为「运行总览、解析沙盒与队列、平台与模板、OneBot 与会话策略、媒体与出口、缓存与安全」六大模块；新增平台滑出式配置抽屉（内置变量胶囊点击插入与实时聊天气泡预览）、底部未保存变更浮动栏与 `Ctrl+S` 快捷键、非敏感配置免输密码一键保存（仅修改网络监听/安全封禁/代理凭据/TLS 等高危项时弹出密码确认框）。
- **新增管理端沙盒预览、单条缓存删除与配置导入导出**：新增 `POST /api/parse/preview` 同步解析预览接口（仅拉取结构化元信息与实时渲染预览文案，脱敏剥离请求头与提取凭据，不写缓存且不发 OneBot）、`DELETE /api/cache?key=...` 单条缓存删除接口，以及脱敏配置 JSON 导出与安全导入回填功能。
- **容器基底迁移至 Alpine Linux（`alpine:3.24`）**：构建阶段切换至 `golang:1.27.3-alpine`，运行阶段切换至 `alpine:3.24`，为锁定的 yt-dlp Python 运行时补充 `musllinux_1_2_x86_64` 原生 wheel 校验并复用系统 `deno`，大幅缩减单容器镜像体积并实现纯离线镜像归档加载部署。

## 1.2.2

- 校验 HTTP 媒体下载 `Content-Length` 完整性，拦截 CDN 边缘节点提前断开导致的残缺流，避免进入 FFmpeg 全片转码后快速报错。
- 将 `ffprobe` / `ffmpeg` 处理纳入 `BackupURLs` 备用节点回退循环并精确回滚失败尝试的临时文件与磁盘配额；仅在 HLS 输入附加 `aac_adtstoasc`，音频流直拷失败时自动降级重试 AAC 转码，并输出路径与 URL 脱敏的 `stderr` 告警日志；补齐抖音图文与实况照片的备用 CDN 地址。
- 任务层在远端媒体流处理失败时支持 singleflight 锁内单次重新解析（轮换虚拟设备与签名 CDN 节点）自愈重试。
- 为哔哩哔哩播放地址补充 WBI 签名回退（`/x/web-interface/nav` + `/x/player/wbi/playurl`），兼容未登录态 `code == -101` 返回的 `wbi_img` 密钥。

## 1.2.1

- 补齐抖音移动端 App 协议签名（`X-Gorgon` / `X-Khronos` / `X-Ladon` / `X-Argus`）、虚拟 Android 设备池自动注册与日常（Story）高清直链探测，修复无 Cookie 或网页端 `aweme/detail` 返回 HTTP 403 时的解析失败，并修正网页端签名参数顺序；保留哔哩哔哩既有解析逻辑与小红书无水印原图解析。

## 1.2.0

- 以 ParseHub 2.3.15 迁移除哔哩哔哩、抖音外的 22 个平台，保留两者适配器、签名及共用解析辅助代码不变。
- YouTube、Facebook、Snapchat 切换为 Go 管理的固定 yt-dlp/EJS/Deno 后端，保留下载上下文；加入认证回环网关、受控代理、公网校验、隔离进程和资源边界。
- 补齐原生平台的分享入口、签名、API/页面回退、媒体顺序、GIF、配对 LivePhoto 与文章 Markdown；保留既有 OneBot 图文成功回执后再发视频的顺序。
- LivePhoto 伴随文件接入签名 HTTP、文件传输、缓存租约和过期清理；迁移平台独立缓存版本，哔哩哔哩和抖音原键不变。
- Medium 使用受控 Chrome 150 TLS/HTTP2 指纹；Go 最低版本更新为 1.27。镜像与 CI 安装校验固定运行时，并随附新增依赖许可和对应源码材料。

## 1.1.1

- 修复 Bilibili AV 链接播放接口标识：信息接口保留 `aid`，播放接口优先使用返回的 `bvid`，缺少时使用 `avid`；保留 BV、短链、分 P 与 cid 校验。
- 调整 OneBot 为同一条普通消息发送图片或封面与解析文案，收到成功回执后再独立发送视频；任一步失败停止后续媒体，不重复已成功图文，保留展示开关及文件/签名 HTTP 传输。

## 2026-10-31

- 1.1.0 新增可选密码登录模式：默认仍启用 Cap，首次可用 `CAP_ENABLED=false` 明确关闭，已有部署通过面板认证后修改；密码、会话、CSRF 和封禁仍生效。
- 新增独立 `compose.no-cap.yaml` 及 README 完整示例，仅部署 Go 应用，无需 Cap/Valkey；启用验证码前验证 Cap 配置，失败保留原策略。
- 明确使用可移植的 `default.env.example` Release 附件名，安装时保存为本地 `.env.example` 后校验；文档注明两个 GHCR 镜像已公开并支持匿名拉取。
- 发布修订版本 1.0.1：源码归档使用 Debian 描述文件的准确文件名，处理 Snapshot 历史别名，并排除不同架构来源的 Debian Ports 重名文件；保留校验失败时停止发布的行为。
- 重写面向开发者的 README，提供默认 Compose、可选配置、Go/API 使用例和上游引用。
- 将镜像部署与源码构建配置分离；新增 GitHub CI，以及检查通过后构建双镜像、发布 GHCR 和 GitHub Release 的版本标签工作流。
- 为镜像补充 Go 依赖和 SecuritySm 许可材料，记录准确的 Debian 包版本，并随发行版提供经校验的对应源码分卷。
- 默认入口改为 `::` 双栈监听全部 IPv4 / IPv6 接口，同步 Compose、环境变量示例和面板说明；不覆盖已有部署的监听配置。
- 首次创建管理员成功后，将随机初始密码输出到日志，重启不重复输出；提醒限制日志访问并及时修改密码。
- 新增 Go OneBot v11 反向 WebSocket 服务、Bearer 鉴权、机器人允许列表及 TLS。
- 新增中文控制面板、默认 admin 随机密码、强制首次修改密码、账号修改、会话撤销与本地恢复命令。
- 新增官方自托管 Cap 等级 4 配置与读回验证、资源内置、验证码防重放，以及跨重启保留的一小时登录失败封禁；验证码错误与密码失败分别计数。
- 新增 24 个平台的原生 Go 解析适配器，不依赖 Python 或 yt-dlp。
- 新增 Bilibili 裸 AV/BV、长链接、分享卡片、短链和分 P 识别。
- 新增平台独立开关、平台级 NSFW 拒绝、自定义拒绝文案与会话权限策略。
- 新增会话成功发送冷却和重复请求抑制；重启取消中断任务，不自动重放不确定的消息发送。
- 新增全局代理、全部直连、仅海外平台代理三模式，以及 HTTP/SOCKS5 支持。
- 新增受限媒体下载、原生音视频合并、HLS 本地化、必要转码和同机/签名 HTTP 媒体交付。
- 新增默认 24 小时缓存与关联记录清理、活动文件引用保护及中断目录回收。
- 新增默认 host 网络的 Compose、面板与 OneBot 共用端口、10492 起端口冲突递增和健康检查。
- 补齐 Compose、真实浏览器登录、HTTP/SOCKS5 代理、图片/视频 OneBot 闭环、HTTPS/WSS 和缓存过期清理的运行核验说明。
