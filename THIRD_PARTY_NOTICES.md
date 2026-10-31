# 第三方组件与许可声明

## ParseHub

原生平台适配器部分逻辑改写自 https://github.com/z-mio/ParseHub 。相关源文件保留原 MIT 声明。

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

Go 模块的精确版本见 `go.mod`、`go.sum`，包括 goquery、Gorilla WebSocket、原生 YouTube 库、modernc SQLite、golang.org/x 系列及传递依赖。各模块许可随原始模块分发。

FFmpeg、Debian、Valkey 及镜像内依赖分别适用其自身许可证；原生 Go 或静态构建不代表整个 Docker 镜像采用单一许可证。重新分发二进制或镜像时，应随附相关声明并满足对应组件的源代码提供等义务。
