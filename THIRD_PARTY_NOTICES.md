# 第三方组件与许可声明

## ParseHub

平台适配器及提取器部分逻辑改写自 https://github.com/z-mio/ParseHub ，本次迁移参考发行版为 2.3.15。相关源文件与 `internal/extractor/LICENSE.parsehub` 保留原 MIT 声明；哔哩哔哩保持既有原生实现。

MIT License

Copyright (c) 2024 梓澪

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Xhshow

小红书激活签名的 Go 实现改写自 https://github.com/cloxl/xhshow 的 0.2.0 发行包；不在运行时安装该 Python 库。

MIT License

Copyright (c) 2024 Cloxl

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.


## SecuritySm

小黑盒设备配置实现参考了 ParseHub 对 SecuritySm 的适配。上游代码与许可固定于：
https://github.com/YueHen14/skyland-auto-sign/tree/6e7115b5580377c842f50f05b0fa39ab079c17b1

MIT License

Copyright (c) 2023 xxyz30

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Cap 与解压组件

固定版本的前端资源位于 `internal/web/assets/`：

- Cap widget 0.1.56。
- @cap.js/wasm 0.0.8。
- Pako 2.1.0。

来源、版权与许可见同目录 `NOTICE`、`cap-widget.LICENSE`、`cap-wasm.LICENSE`、`pako.LICENSE`。Cap Standalone 容器保留官方运行时及其许可，不将其声明为本项目自有代码。

## Go 与系统依赖

Go 模块的精确版本见 `go.mod`、`go.sum`，包括 goquery、Gorilla WebSocket、html-to-markdown、tls-client/fhttp、modernc SQLite、golang.org/x 系列及传递依赖。各模块许可随原始模块分发，并由构建工具收集到镜像文档目录。

`fhttp v0.7.3` 的源码声明使用 Go BSD 许可，但发行模块与该版本仓库均缺少独立 `LICENSE`。构建器从 `scripts/licenses/supplemental/` 随附其源码明确引用的 Go 许可原文及来源记录；仅匹配该精确模块版本，其他缺失许可证的模块仍使构建失败。这是对既有声明的补发，不为未声明的新贡献推定额外授权。

FFmpeg、Debian、Valkey 及镜像内依赖分别适用其自身许可证；原生 Go 或静态构建不代表整个 Docker 镜像采用单一许可证。重新分发二进制或镜像时，应随附相关声明并满足对应组件的源代码提供等义务。

## yt-dlp、EJS、Deno 与 Python 运行时

用户选择的三个平台后端使用 yt-dlp `2026.10.26.220202.dev0`、yt-dlp-ejs `0.8.0`、Deno `2.9.8`。固定附件、SHA-256、上游地址和默认依赖见 `scripts/ytdlp-runtime.lock.json`；构建时核验，运行时不自动下载组件。

应用镜像的 `/usr/local/share/doc/multiparse/ytdlp-runtime/` 保存 Python 包清单、原始 wheel 许可元数据、EJS 内嵌 meriyah ISC/astring MIT 许可、Deno 及其原生和 Rust 依赖许可集合与来源记录。该集合覆盖锁文件依赖的保守超集，不宣称是精确链接二进制 SBOM。

新增 Python 组件分别遵循自身许可证，包括 Mutagen 的 GPL、certifi 的 MPL 以及 requests、urllib3、websockets、Brotli、pycryptodomex 等原始许可。Mutagen 同版本对应源码随镜像存放于该目录的 `python-sources/`。Python 解释器属于 Debian 软件包清单及对应源码分发范围；不能将整个运行时统一声明为本项目自有代码或 MIT。
