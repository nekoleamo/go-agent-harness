#!/usr/bin/env bash
# 桌面壳构建(desktop/):sidecar gah 打包 + Tauri 壳编译。
# 用法:
#   bash scripts/gen-desktop.sh                 # dev 构建(cargo build)
#   bash scripts/gen-desktop.sh --release       # release(cargo build --release)
# 完整发行(.app/.dmg + 签名公证)见 P2(desktop/README 与 DESKTOP_FEASIBILITY §7)。
# 前置:Go 工具链 + Rust(cargo);依赖镜像见 desktop/.cargo/config.toml(tuna https)。
set -euo pipefail
cd "$(dirname "$0")/.."

# 1. sidecar 产物(本平台 triple;跨平台矩阵归 CI/P2)
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)   triple=aarch64-apple-darwin ;;
  Darwin-x86_64|Darwin-amd64) triple=x86_64-apple-darwin ;;
  Linux-x86_64|Linux-amd64)   triple=x86_64-unknown-linux-gnu ;;
  Linux-aarch64|Linux-arm64) triple=aarch64-unknown-linux-gnu ;;
  MINGW*|MSYS*|CYGWIN*)       triple=x86_64-pc-windows-msvc ;;
  *) echo "gen-desktop: 未知平台 $(uname -s)-$(uname -m)"; exit 1 ;;
esac
echo "[1/2] sidecar build: gah-$triple"
CGO_ENABLED=0 go build -trimpath -o "desktop/src-tauri/binaries/gah-$triple" ./cmd/gah

# 2. 桌面壳编译(dev/release;tauri.conf externalBin 引用同一命名)
echo "[2/2] cargo build $*"
cd desktop/src-tauri
cargo build "$@"
echo "桌面壳产物: desktop/src-tauri/target/debug/gah-desktop(release 用 target/release)"
echo "运行: 该二进制,或 tauri 打包 .app(dmg,见 P2)"
