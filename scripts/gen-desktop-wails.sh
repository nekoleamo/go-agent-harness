#!/usr/bin/env bash
# Go 壳(Wails v3)构建:sidecar `gah` + 壳本体。
#
# 旧的 Tauri 链已于 2026-10-29 删除;产物布局沿用同一套 triple 命名(落 desktop/wails/binaries/),
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
TAGS=""
if [ "$MODE" = "release" ]; then
  LDFLAGS="-s -w $LDFLAGS"
  # 发行:sidecar 以 gzip 内嵌进壳(见 desktop/wails/embed_sidecar.go 的三条理由)。
  # 内嵌后**更新器只需换一个文件**,Windows 上才不会出现「新壳配旧 sidecar」。
  mkdir -p "$SHELL_MOD/embed"
  gzip -9 -n -c "$BIN/gah-$triple" > "$SHELL_MOD/embed/gah.bin.gz"
  TAGS="-tags embed_sidecar"
  # 验签公钥烧进壳(信任根不能来自发行渠道);缺省空 = 只校验摘要,并留一条警告。
  if [ -n "${GAH_DESKTOP_UPDATE_PUBKEY:-}" ]; then
    LDFLAGS="$LDFLAGS -X main.updatePubKeyHex=$GAH_DESKTOP_UPDATE_PUBKEY"
  else
    echo "警告: 未设 GAH_DESKTOP_UPDATE_PUBKEY,发行包只校验摘要"
  fi
  echo "内嵌 sidecar: $SHELL_MOD/embed/gah.bin.gz($(du -h "$SHELL_MOD/embed/gah.bin.gz" | cut -f1))"
fi
OUT="$SHELL_MOD/build/gah-desktop"
mkdir -p "$SHELL_MOD/build"
# shellcheck disable=SC2086  # TAGS 需要按空格拆开(空时不能留空参数)
(cd "$SHELL_MOD" && CGO_ENABLED=1 go build -trimpath $TAGS -ldflags "$LDFLAGS" -o "build/gah-desktop" .)

echo "产物: $OUT ($(du -h "$OUT" | cut -f1)) version=$VERSION"
if [ "$MODE" = "release" ]; then
  echo "发行包内置 sidecar;调试时可 GAH_SIDECAR=<gah> 覆盖成任意产物(就地跑)"
else
  echo "运行: GAH_SIDECAR=$PWD/$BIN/gah-$triple $OUT"
fi
echo "注:发行包(.app/.dmg/NSIS)与更新器迁移见 docs/DESKTOP_FEASIBILITY.md §11.6"
