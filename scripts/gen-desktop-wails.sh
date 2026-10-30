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

# 版本号在**构建 sidecar 之前**就要有:壳与 sidecar 必须报同一个版本
# (2026-10-30 v0.8.0 事故:sidecar 拼命令漏了 -X main.version,壳是 0.8.0、
#  内嵌的 gah 是 dev ⇒ 托盘「关于」显示 0.8.0,Web 底栏显示 vdev)。
VERSION="$(git describe --tags 2>/dev/null || echo dev)"
SIDE_LD="-X main.version=$VERSION"
[ "$MODE" = "release" ] && SIDE_LD="-s -w $SIDE_LD"

# 版本核验:本机 build 一定能真跑一次(不扫二进制找版本串 —— 版本号可能恰好出现在
# 依赖或嵌入数据里,漏注入也能 grep 到,那种护栏会假通过;2026-10-30 实测)。
assert_sidecar_version() { # <bin> <version>
  local got
  got="$("$1" --version 2>/dev/null | awk '{print $2}')"
  [ "${got#v}" = "${2#v}" ] || {
    echo "!! sidecar 版本不对(期望 $2,实际 ${got:-<空>}):$1" >&2
    echo "   壳会报对、sidecar 报 dev —— 用户看到的是「版本号不对」,别把它发出去。" >&2
    exit 1
  }
}

echo "[1/2] sidecar: gah-$triple($VERSION)"
CGO_ENABLED=0 go build -trimpath -ldflags "$SIDE_LD" -o "$BIN/gah-$triple" ./cmd/gah || exit 1
assert_sidecar_version "$BIN/gah-$triple" "$VERSION"

echo "[2/2] 壳: $SHELL_MOD(CGO=1,$MODE)"
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
# Windows 壳必须是 GUI 子系统(否则双击会多弹一个黑窗口),且只能对 windows 目标加。
case "$triple" in
  *windows*) [ "$MODE" = release ] && LDFLAGS="$LDFLAGS -H windowsgui" ;;
esac
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
