#!/usr/bin/env bash
# 统一构建入口:一条命令出 gah 全部形态(TUI/Web 单二进制、六目标发行、桌面壳 .app/.dmg)。
# 三端融合原理:gah 单二进制内嵌 web/dist(TUI/Web/headless 同进程三 profile,零打包差异);
# 桌面壳的 sidecar 复用同一 cli 产物,Tauri 仅做窗口/托盘/打包。
#
# 用法:
#   ./scripts/gen.sh cli           本平台单二进制(TUI+Web+headless 全形态;前端已内嵌)
#   ./scripts/gen.sh release       goreleaser 六目标发行(tar.gz/zip;before hooks 已含 web/ext 构建)
#   ./scripts/gen.sh desktop       桌面壳:sidecar(=cli 产物)→ tauri build(.app/.dmg,未签名)
#   ./scripts/gen.sh web-dev       前端热更开发(vite watch;后端 GAH_WEB_STATIC 指向产物免重启)
set -euo pipefail
cd "$(dirname "$0")/.."

# —— 前端产物保障:gah 内嵌 web/dist;仅当缺失或 web-src 有更新时重构建(平时跳过)——
ensure_web() {
  if [ ! -f web/dist/index.html ]; then
    echo "[web] web/dist 缺失(embed 护栏)→ 构建前端"
    bash scripts/gen-web.sh
    return
  fi
  if find web-src/src -type f -newer web/dist/index.html -print -quit | grep -q .; then
    echo "[web] web-src 有新变更 → 重建前端"
    bash scripts/gen-web.sh
  else
    echo "[web] web/dist 已最新,跳过(前端未改动)"
  fi
}

cmd="${1:-help}"
case "$cmd" in
  cli)
    ensure_web
    version="${VERSION:-dev}"
    echo "[cli] go build(本平台单二进制;dev 默认,发行写 VERSION=v0.x.y)"
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o gah ./cmd/gah
    echo '产出: ./gah(31.9MB级;TUI: ./gah / Web: ./gah web / headless: ./gah --profile headless)'
    ;;
  release)
    ensure_web
    if ! command -v goreleaser >/dev/null; then
      echo "[release] 缺 goreleaser:安装 → https://goreleaser.com/install/"
      exit 1
    fi
    echo "[release] goreleaser(before hooks 已含 gen-extplugins + gen-web;六目标 tar.gz/zip)"
    goreleaser release "${@:2}"
    ;;
  desktop)
    # 旧 Tauri 链已移除;桌面壳构建见 scripts/gen-desktop-wails.sh(壳在 desktop/wails/)。
    bash scripts/gen-desktop-wails.sh "${@:2}"
    ;;
  web-dev)
    echo "[web-dev] vite watch(vite dev 产物落盘需 build --watch;前端改动自动重建)"
    echo "         后端免重启:另终端 GAH_WEB_STATIC=$PWD/web/dist gah web"
    ( cd web-src && npx vite build --watch )
    ;;
  help|*)
    sed -n '2,12p' "$0"
    exit 0
    ;;
esac

