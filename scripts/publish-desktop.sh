#!/usr/bin/env bash
# 桌面壳零成本发行(无 Apple/Windows 代码签名,updater ed25519 自持签名):
#   单平台:bash scripts/publish-desktop.sh darwin-aarch64      # 或 darwin-x86_64 / windows-x86_64
#   合并:   bash scripts/publish-desktop.sh merge               # 读 dist-desktop/latest.<平台>.json 合成 latest.json
# 产物:dist-desktop/<平台>/(安装包 + updater 产物 + latest.<平台>.json);merge 后 latest.json 上传 GitHub Release,
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
  nver="$(jq -r '.version' "${files[@]}" | sort -u | wc -l | tr -d ' ')"
  if [ "$nver" != 1 ]; then
    echo "多平台 version 不一致(检查 git tag 与 tauri.conf.json):" >&2
    for f in "${files[@]}"; do echo "  $(jq -r .version "$f")  $f" >&2; done
    exit 1
  fi
  jq -s '{
    version: .[0].version,
    pub_date: (map(.pub_date) | max),
    platforms: (reduce .[].platforms as $p ({}; . + $p))
  }' "${files[@]}" > "$OUT/latest.json"
  echo "merged → $OUT/latest.json"
  jq . "$OUT/latest.json"
  exit 0
fi

# 0. updater 签名私钥(必须在 tauri build 之前就位:bundle.createUpdaterArtifacts=true 时
#    tauri-bundler 自己会签名,缺私钥会直接报「A public key has been found, but no private key」)
KEY=""
KEY_TMP=0
if [ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then
  KEY="$(mktemp)"; KEY_TMP=1; printf '%s' "$TAURI_SIGNING_PRIVATE_KEY" > "$KEY"
else
  KEY="${TAURI_SIGNING_PRIVATE_KEY_PATH:-$HOME/.tauri/gah.key}"
fi
[ -f "$KEY" ] || { echo "缺签名私钥:$KEY(tauri signer generate -w ~/.tauri/gah.key 生成)"; exit 1; }
export TAURI_SIGNING_PRIVATE_KEY="$(cat "$KEY")"   # 供 tauri-bundler 生成 updater 产物与 .sig
export TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"

# 1. sidecar(仓库源码 go build;改动 extplugins 需先 bash scripts/gen-extplugins.sh 重生成 embed)
# Windows 侧 sidecar 必须带 .exe:tauri-bundler 找的是 binaries/gah-<triple>.exe
# (CI 实测报 resource path `binaries\gah-x86_64-pc-windows-msvc.exe` doesn't exist),
# 同 gen-extplugins.sh 对 windows 外部插件产物的处理(无扩展名的 PE 也无法 exec)。
SIDECAR="desktop/src-tauri/binaries/gah-$triple"
case "$platform" in windows-*) SIDECAR="$SIDECAR.exe" ;; esac
echo "[1/4] sidecar build: $(basename "$SIDECAR")"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$SIDECAR" ./cmd/gah

# 2. 壳 release bundle(cargo tauri 优先,无则 npx 拉 @tauri-apps/cli)
# 注:tauri-cli v2 的 build **默认就是 release**(只有 -d/--debug),没有 --release 参数 ——
#     v1 语义的 `build --release` 在 v2 会直接报 unexpected argument(本地实测踩到)。
echo "[2/4] tauri build --target $triple (release,version=$VERSION)"
cd desktop/src-tauri
TAURI_BUILD=""
if command -v cargo-tauri >/dev/null 2>&1 || command -v tauri >/dev/null 2>&1; then
  TAURI_BUILD="tauri"
elif command -v npx >/dev/null 2>&1; then
  # -p 指定包、显式给 bin 名:`npx -p <pkg> <bin>`;不可写 `npx <pkg> tauri …`
  # (那样 argv 首参变成 "tauri",CLI 会报 unrecognized subcommand 'tauri' —— 本地实测踩到,
  #  而 cargo-tauri 分支的 `tauri build` 形态是对的,故两条分支形状本就不同)。
  TAURI_BUILD="npx -y -p @tauri-apps/cli@2 tauri"
else
  echo "缺 tauri-cli: 安装 cargo-tauri 或用 npx(@tauri-apps/cli)"; exit 1
fi
# shellcheck disable=SC2086
$TAURI_BUILD build --target "$triple" --config "{\"version\":\"$VERSION\"}"
cd ../..

# 3. 提取产物(updater 产物与「给人双击的安装包」必须分开:latest.json 只能指向 updater 产物,
#    否则 updater 下到 .dmg/-setup.exe 会装不上;两者都随 Release 上传,人下载安装包)
echo "[3/4] 收集产物"
# 注意子目录:.app.tar.gz 在 bundle/macos/,dmg 在 bundle/dmg/,nsis 两件都在 bundle/nsis/
BUNDLE="desktop/src-tauri/target/$triple/release/bundle"
mkdir -p "$OUT/$platform"
case "$platform" in
  darwin-*)
    upd_glob=("$BUNDLE/macos"/*.app.tar.gz) # updater 要的是整个 .app 的 tar.gz
    # 人:双击 dmg 拖入 Applications;必须排除 bundle_dmg.sh 的中间产物 rw.*.dmg
    # (失败/中断的构建会在同目录留下 ~90 MB 的读写镜像,裸 *.dmg 会把它们一起收走)
    dist_glob=()
    for f in "$BUNDLE"/dmg/*.dmg; do
      case "$(basename "$f")" in rw.*) ;; *) dist_glob+=("$f") ;; esac
    done
    ;;
  windows-x86_64)
    upd_glob=("$BUNDLE/nsis"/*-setup.exe.zip) # updater 要的是 NSIS 安装器的 zip(tauri-plugin-updater 期望形态)
    dist_glob=("$BUNDLE/nsis"/*-setup.exe)    # 人:双击 NSIS 安装器
    ;;
esac
upd_file="${upd_glob[0]}"
if [ ! -f "$upd_file" ]; then
  echo "缺 updater 产物:${upd_glob[*]}" >&2
  echo "  → 检查 tauri.conf.json 的 bundle.createUpdaterArtifacts(=true)与 bundle.targets" >&2
  exit 1
fi
if [ "${#dist_glob[@]}" -eq 0 ]; then
  echo "缺分发安装包(dmg / -setup.exe);检查 tauri.conf.json 的 bundle.targets" >&2
  exit 1
fi
cp "${dist_glob[@]}" "$OUT/$platform/"
cp "$upd_file" "$OUT/$platform/"
ls -la "$OUT/$platform"

# 4. latest.<平台>.json(签名优先取 tauri-bundler 自己产出的 .sig;缺失时回退 tauri signer)
echo "[4/4] updater 签名"
if [ -f "$upd_file.sig" ] && [ -s "$upd_file.sig" ]; then
  sig="$(tr -d '\n' < "$upd_file.sig")"
  echo "签名来源:$upd_file.sig"
else
  sig="$(npx -y -p @tauri-apps/cli@2 tauri signer sign -k "$KEY" "$OUT/$platform/$(basename "$upd_file")" 2>/dev/null | tail -1)"
  echo "签名来源:tauri signer sign(回退)"
fi
[ "$KEY_TMP" = 1 ] && rm -f "$KEY"
if [ -z "$sig" ]; then echo "updater 签名失败(无 .sig 且 tauri signer 失败)"; exit 1; fi

name="$(basename "$upd_file")"
url="https://github.com/$REPO/releases/download/v$VERSION/$name"
jq -n --arg v "$VERSION" --arg d "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --arg u "$url" --arg s "$sig" --arg k "$updkey" \
      '{version: $v, pub_date: $d, platforms: {($k): {url: $u, signature: $s}}}' \
  > "$OUT/latest.$platform.json"
cat "$OUT/latest.$platform.json"
echo "完成。多平台发布:各平台跑本脚本后执行 bash scripts/publish-desktop.sh merge,上传 dist-desktop/latest.json 与安装包到 GitHub Release(v$VERSION)"
