#!/usr/bin/env bash
# 打包命令行版（Linux / macOS / *nix）。
#
# 托盘版只在 Windows 上有意义，用 build\build.bat 打包。
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
OUT=build
mkdir -p "$OUT"

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
    -o "$OUT/GXU_Net_AutoLogin" .

echo "完成（版本 $VERSION）：$OUT/GXU_Net_AutoLogin"
