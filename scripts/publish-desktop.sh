#!/usr/bin/env bash
# 桌面壳零成本发行(无 Apple/Windows 代码签名,updater ed25519 自持签名):
#   单平台:bash scripts/publish-desktop.sh darwin-aarch64      # 或 darwin-x86_64 / windows-x86_64
#   合并:   bash scripts/publish-desktop.sh merge               # 读 dist-desktop/latest.<平台>.json 合成 latest.json
# 产物:dist-desktop/<平台>/(安装包 + latest.<平台>.json);merge 后 latest.json 上传 GitHub Release,
#       updater endpoint 固定取 https://github.com/<repo>/releases/latest/download/latest.json
# 版本:RELEASE_VERSION=vX.Y.Z(缺省读 tauri.conf version;发布时由 git tag 驱动)
# 密钥:TAURI_SIGNING_PRIVATE_KEY_PATH(缺省 ~/.tauri/gah.key)或 TAURI_SIGNING_PRIVATE_KEY 字符串
# 前置:Go + Rust(target triple 已 rustup add)+ Xcode CLT(mac)/无(win runner);node(经 npx 调 tauri-cli,免 cargo install)
# 无签名分发说明:mac 首启需「右键→打开」或 xattr 去隔离;win 首启 SmartScreen「仍要运行」——见 docs/RELEASE.md
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="${GAH_REPO:-nekoleamo/go-agent-harness}"
VERSION="$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)"
[ -z "$VERSION" ] && VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' desktop/src-tauri/tauri.conf.json | head -1)"
OUT="dist-desktop"
mkdir -p "$OUT"

# 平台 → Rust triple / updater 键 / bundle 子目录
platform="$1"
case "$platform" in
  darwin-aarch64)  triple=aarch64-apple-darwin;   updkey=darwin-aarch64;  bdir=macos ;;
  darwin-x86_64)   triple=x86_64-apple-darwin;    updkey=darwin-x86_64;   bdir=macos ;;
  windows-x86_64)  triple=x86_64-pc-windows-msvc; updkey=windows-x86_64;  bdir=nsis ;;
  merge) merge=1 ;;
  *) echo "用法: $0 {darwin-aarch64|darwin-x86_64|windows-x86_64|merge}"; exit 1 ;;
esac

if [ "${merge:-0}" = 1 ]; then
  # 合并多平台 latest.<平台>.json → latest.json(updater 单端点含全平台)
  command -v jq >/dev/null || { echo "需 jq"; exit 1; }
  files=("$OUT"/latest.*.json)
  [ -e "${files[0]}" ] || { echo "无 latest.<平台>.json(先跑单平台构建)"; exit 1; }
  jq -s '{
    version: (.[-1].version // .[0].version),
    pub_date: (.[-1].pub_date // .[0].pub_date),
    platforms: (reduce .[].platforms as $p ({}; . + $p))
  }' "${files[@]}" > "$OUT/latest.json"
  echo "merged → $OUT/latest.json"
  jq . "$OUT/latest.json"
  exit 0
fi

# 1. sidecar(仓库源码 go build;改动 extplugins 需先 bash scripts/gen-extplugins.sh 重生成 embed)
echo "[1/4] sidecar build: gah-$triple"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "desktop/src-tauri/binaries/gah-$triple" ./cmd/gah

# 2. 壳 release bundle(cargo tauri 优先,无则 npx 拉 @tauri-apps/cli)
echo "[2/4] tauri build --release --target $triple (version=$VERSION)"
cd desktop/src-tauri
TAURI_BUILD=""
if command -v cargo-tauri >/dev/null 2>&1 || command -v tauri >/dev/null 2>&1; then
  TAURI_BUILD="tauri"
elif command -v npx >/dev/null 2>&1; then
  TAURI_BUILD="npx -y @tauri-apps/cli@2 tauri"
else
  echo "缺 tauri-cli: 安装 cargo-tauri 或用 npx(@tauri-apps/cli)"; exit 1
fi
# shellcheck disable=SC2086
$TAURI_BUILD build --release --target "$triple" --config "{\"version\":\"$VERSION\"}"
cd ../..

# 3. 提取安装包产物
echo "[3/4] 收集产物"
BUNDLE="desktop/src-tauri/target/$triple/release/bundle/$bdir"
mkdir -p "$OUT/$platform"
artifacts=()
case "$platform" in
  darwin-*) artifacts=("$BUNDLE"/*.app.tar.gz "$BUNDLE"/*.dmg) ;; # updater 用 .app.tar.gz;分发另附 .dmg
  windows-x86_64) artifacts=("$BUNDLE"/*-setup.exe) ;;
esac
cp ${artifacts[*]} "$OUT/$platform/" 2>/dev/null || true
ls -la "$OUT/$platform"

# 4. updater 签名 + latest.<平台>.json(ed25519 自持密钥;signer 经 npx cli)
echo "[4/4] updater 签名"
KEY=""
KEY_TMP=0
if [ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then
  KEY="$(mktemp)"; KEY_TMP=1; printf '%s' "$TAURI_SIGNING_PRIVATE_KEY" > "$KEY"
else
  KEY="${TAURI_SIGNING_PRIVATE_KEY_PATH:-$HOME/.tauri/gah.key}"
fi
[ -f "$KEY" ] || { echo "缺签名私钥:$KEY(tauri signer generate -w ~/.tauri/gah.key 生成)"; exit 1; }

url="https://github.com/$REPO/releases/download/v$VERSION"
json="{}"
for a in "$OUT/$platform"/*; do
  [ -e "$a" ] || continue
  name="$(basename "$a")"
  sig="$(npx -y @tauri-apps/cli@2 signer sign -k "$KEY" "$a" 2>/dev/null | tail -1)"
  json="$(jq --arg k "$updkey" --arg s "$sig" --arg u "$url/$name" \
    '.platforms[$k] = {url: $u, signature: $s}' <<<"$json")"
done
[ "$KEY_TMP" = 1 ] && rm -f "$KEY"
cat "$OUT/latest.$platform.json" | jq .
echo "完成。多平台发布:各平台跑本脚本后执行 bash scripts/publish-desktop.sh merge,上传 dist-desktop/latest.json 与安装包到 GitHub Release(v$VERSION)"
