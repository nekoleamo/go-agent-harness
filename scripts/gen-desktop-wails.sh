#!/usr/bin/env bash
# Go 壳(Wails v3)构建:sidecar `gah` + 壳本体。
#
# 与 scripts/gen-desktop.sh(Tauri/Rust 壳)并存:迁移期内两条链都能跑,
# 主干合并后再删旧的。Tauri 链的产物布局是 desktop/src-tauri/binaries/gah-<triple>,
# 这里沿用同一套 triple 命名,只是落在 desktop/wails/binaries/。
#
# 用法:
#   bash scripts/gen-desktop-wails.sh              # debug 构建(CGO=1,快)
#   bash scripts/gen-desktop-wails.sh --release    # 发行构建(-s -w + 版本号)
#
# 前置:Go 工具链 + Xcode CLT(clang;Wails 的 mac/win 实现都要 cgo 链接系统 webview)。
set -euo pipefail
cd "$(dirname "$0")/.."

MODE=debug
for arg in "$@"; do
  case "$arg" in
    --release) MODE=release ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数:$arg"; exit 2 ;;
  esac
done

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)   triple=aarch64-apple-darwin ;;
  Darwin-x86_64)  triple=x86_64-apple-darwin ;;
  MINGW*|MSYS*|CYGWIN*) triple=x86_64-pc-windows-msvc ;;
  *) echo "gen-desktop-wails: 未支持平台 $(uname -s)-$(uname -m)(桌面壳只发 mac/win)"; exit 1 ;;
esac

SHELL_MOD="desktop/wails"
BIN="$SHELL_MOD/binaries"
mkdir -p "$BIN"

echo "[1/2] sidecar: gah-$triple"
CGO_ENABLED=0 go build -trimpath -o "$BIN/gah-$triple" ./cmd/gah

echo "[2/2] 壳: $SHELL_MOD(CGO=1,$MODE)"
VERSION="$(git describe --tags 2>/dev/null || echo dev)"
LDFLAGS="-X main.version=$VERSION"
[ "$MODE" = "release" ] && LDFLAGS="-s -w $LDFLAGS"
OUT="$SHELL_MOD/build/gah-desktop"
mkdir -p "$SHELL_MOD/build"
(cd "$SHELL_MOD" && CGO_ENABLED=1 go build -trimpath -ldflags "$LDFLAGS" -o "build/gah-desktop" .)

echo "产物: $OUT ($(du -h "$OUT" | cut -f1)) version=$VERSION"
echo "运行: GAH_SIDECAR=$PWD/$BIN/gah-$triple $OUT"
echo "注:发行包(.app/.dmg/NSIS)与更新器迁移见 docs/DESKTOP_FEASIBILITY.md §11.6"
