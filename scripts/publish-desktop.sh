#!/usr/bin/env bash
# 桌面壳零成本发行(无 Apple/Windows 代码签名,updater ed25519 自持签名):
#   单平台:bash scripts/publish-desktop.sh darwin-aarch64      # 或 darwin-x86_64 / windows-x86_64
#   合并:   bash scripts/publish-desktop.sh merge               # 读 dist-desktop/latest.<平台>.json 合成 latest.json
#   国内加速:bash scripts/publish-desktop.sh rewrite-url https://ghproxy.net/
#           # 把 latest.json 里各平台的下载 URL 前置镜像前缀(updater 签名只对文件内容验签,
#           # URL 不参与 ⇒ 换源不降低安全强度);`rewrite-url none` 还原成 GitHub 直链。
#           首次运行会把原表另存为 latest.github.json,之后一律以它为改写源(幂等)。
# 产物:dist-desktop/<平台>/(安装包 + updater 产物 + latest.<平台>.json);merge 后 latest.json 上传 GitHub Release,
#       updater endpoint 固定取 https://github.com/<repo>/releases/latest/download/latest.json
# 版本:RELEASE_VERSION=vX.Y.Z(缺省取最近 git tag;发布时由 git tag 驱动)
# 调试/升级冒烟:GAH_DESKTOP_DEBUG=1 走 cargo 的 debug profile(只编壳,不做发行),
#   产物落 target/<triple>/debug/bundle —— 给「旧版 → 线上新版」的真机升级冒烟当旧版用
#   (见 README/docs/RELEASE.md「升级冒烟」)。
# 密钥:TAURI_SIGNING_PRIVATE_KEY_PATH(缺省 ~/.tauri/gah.key)或 TAURI_SIGNING_PRIVATE_KEY 字符串
# 前置:Go + Rust(target triple 已 rustup add)+ Xcode CLT(mac)/无(win runner);node(经 npx 调 tauri-cli,免 cargo install)
# 无签名分发说明:mac 首启需「右键→打开」或 xattr 去隔离;win 首启 SmartScreen「仍要运行」——见 docs/RELEASE.md
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="${GAH_REPO:-nekoleamo/go-agent-harness}"
VERSION="${RELEASE_VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || true)}"
VERSION="${VERSION#v}"   # 允许带 v 前缀(RELEASE_VERSION=v0.1.3)
[ -z "$VERSION" ] && VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' desktop/src-tauri/tauri.conf.json | head -1)"
PROFILE_DIR=release
[ "${GAH_DESKTOP_DEBUG:-0}" = "1" ] && PROFILE_DIR=debug
OUT="dist-desktop"
mkdir -p "$OUT"

# 平台 → Rust triple / updater 键 / bundle 子目录
platform="$1"
case "$platform" in
  darwin-aarch64)  triple=aarch64-apple-darwin;   updkey=darwin-aarch64;  bdir=macos ;;
  darwin-x86_64)   triple=x86_64-apple-darwin;    updkey=darwin-x86_64;   bdir=macos ;;
  windows-x86_64)  triple=x86_64-pc-windows-msvc; updkey=windows-x86_64;  bdir=nsis ;;
  merge) merge=1 ;;
  rewrite-url) rewrite=1 ;;
  *) echo "用法: $0 {darwin-aarch64|darwin-x86_64|windows-x86_64|merge|rewrite-url}"; exit 1 ;;
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

if [ "${rewrite:-0}" = 1 ]; then
  # 改写 latest.json 的下载 URL 前缀(国内网络加速)。
  # 为什么安全:updater 的 ed25519 签名只对**文件内容**验签,URL 不参与 ⇒ 换源不降低校验强度;
  #   镜像/代理换包会被签名直接拒掉。也因此对存量客户端立即生效(不需重编/重发)。
  # 幂等:第一次运行把原表另存为 latest.github.json,之后一律以它为改写源。
  command -v jq >/dev/null || { echo "需 jq"; exit 1; }
  base="${2:-}"
  [ -n "$base" ] || { echo "用法: $0 rewrite-url <URL 前缀|gitee:owner/repo|none>"; exit 1; }
  [ -f "$OUT/latest.json" ] || { echo "无 $OUT/latest.json(先跑 merge,或从 Release 取回 latest.json)"; exit 1; }
  src="$OUT/latest.github.json"
  # 归一化:从 URL 里抽出最后的 GitHub 直链(幂等 —— 即使输入是**已被前缀过的**表也能正确留档,
  # 否则 CI 侧重新拉线上的表再改写会变成双重前缀)
  norm='.platforms |= with_entries(.value |= (.url = ((.url | capture("(?<gh>https://github\\.com/.*)$") | .gh) // .url)))'
  # 目标基址先解析(供幂等短路与自检使用):
  #   none|github ⇒ 还原;gitee:owner/repo ⇒ 换基址(Gitee 直链与 GitHub 同 tag、同文件名,
  #   只差 host 与仓库路径 ⇒ 正则换基址,而不是前置拼接);其它 ⇒ 视为前缀,强制补尾斜杠
  #   (否则会拼出 host**https://** 这种非法 URL)
  case "$base" in
    none|github) ;;
    gitee:*)
      gpath="${base#gitee:}"; gpath="${gpath#/}"; gpath="${gpath%/}"
      case "$gpath" in */*) ;; *) echo "gitee: 后需给 owner/repo(如 gitee:null_593_5354/go-agent-harness)" >&2; exit 1 ;; esac
      base="https://gitee.com/$gpath/releases/download/"
      rebase=1 ;;
    */) ;;
    *) base="$base/" ;;
  esac
  # 幂等短路:当前表已经全部指向目标基址 ⇒ 无事可做(补发/重跑时线上表可能已换过源)。
  # 没有这一步,「拿已换源的表再改一次」会在下面被当成非法输入拒掉。
  if [ "$base" != "none" ] && [ "$base" != "github" ] \
     && jq -e --arg b "$base" '[.platforms[].url | startswith($b)] | all' "$OUT/latest.json" >/dev/null 2>&1; then
    echo "已是目标下载地址,无需改写:$OUT/latest.json"
    exit 0
  fi
  if [ ! -f "$src" ]; then
    jq "$norm" "$OUT/latest.json" > "$src"
    echo "已留档原始表(GitHub 直链)→ $src"
  fi
  # 留档表必须含 GitHub 直链(否则改写无从谈起)。若留档不可用但**当前表**是干净的 GitHub 直链
  # (补发场景:线上表已换成镜像源),就用当前表刷新留档。
  if ! jq -e '[.platforms[].url | startswith("https://github.com/")] | all' "$src" >/dev/null 2>&1; then
    if jq -e '[.platforms[].url | startswith("https://github.com/")] | all' "$OUT/latest.json" >/dev/null 2>&1; then
      cp "$OUT/latest.json" "$src"
      echo "留档不可用,已用当前表的 GitHub 直链刷新:$src"
    else
      echo "拒绝改写:既没有可用的留档表,当前表也不是 GitHub 直链" >&2
      exit 1
    fi
  fi
  if [ "$base" = "none" ] || [ "$base" = "github" ]; then
    cp "$src" "$OUT/latest.json"
    echo "已还原为 GitHub 直链 → $OUT/latest.json"
  else
    if [ "${rebase:-0}" = 1 ]; then
      jq --arg b "$base" '.platforms |= with_entries(.value |= (.url = (.url | sub("^https://github\\.com/[^/]+/[^/]+/releases/download/"; $b))))' \
        "$src" > "$OUT/latest.json.tmp"
    else
      jq --arg b "$base" '.platforms |= with_entries(.value |= (.url = ($b + .url)))' "$src" > "$OUT/latest.json.tmp"
    fi
    mv "$OUT/latest.json.tmp" "$OUT/latest.json"
    echo "已改写下载地址(基址 → $base)→ $OUT/latest.json"
  fi
  # 自检 1:平台集合与 signature 必须与原始表**逐平台一致**(换源不许动签名)
  if ! jq -e --slurpfile a "$src" \
      '.platforms as $p | ($a[0].platforms) as $q | ($p | keys) == ($q | keys)
       and ([$p | keys[] as $k | $p[$k].signature == $q[$k].signature] | all)' \
      "$OUT/latest.json" >/dev/null; then
    echo "自检失败:平台集合或签名被改写破坏 —— 已回滚" >&2
    cp "$src" "$OUT/latest.json"
    exit 1
  fi
  # 自检 2:每个 URL 都以给的前缀开头(还原模式跳过);gitee 模式则要求全部指向 gitee
  if [ "$base" != "none" ] && [ "$base" != "github" ]; then
    jq -e --arg b "$base" '[.platforms[].url | startswith($b)] | all' "$OUT/latest.json" >/dev/null \
      || { echo "自检失败:URL 前缀不符 —— 已回滚" >&2; cp "$src" "$OUT/latest.json"; exit 1; }
  fi
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

# 0b. 图标前置检查(Windows 侧 tauri-build 需要 icons/icon.ico 生成资源文件,缺了报错很隐晦:
#     「icons/icon.ico not found; required for generating a Windows Resource file」)
if [ ! -f desktop/src-tauri/icons/icon.ico ]; then
  echo "缺 desktop/src-tauri/icons/icon.ico" >&2
  echo "  → cd desktop/src-tauri && npx -p @tauri-apps/cli@2 tauri icon icons/icon.png" >&2
  exit 1
fi

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
echo "[2/4] tauri build --target $triple ($PROFILE_DIR,version=$VERSION)"
cd desktop/src-tauri
DEBUG_FLAG=""
[ "$PROFILE_DIR" = debug ] && DEBUG_FLAG="--debug"   # tauri-cli v2:release 是默认,debug 才要显式给
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
$TAURI_BUILD build --target "$triple" --config "{\"version\":\"$VERSION\"}" $DEBUG_FLAG
cd ../..

# 3. 提取产物(updater 产物与「给人双击的安装包」分开:latest.json 只能指向 updater 产物)
#    - macOS:updater 要 *.app.tar.gz;人用 *.dmg(在 bundle/dmg/,注意排除 bundle_dmg.sh 的
#      中间产物 rw.*.dmg,失败/中断的构建会留下 ~90 MB 读写镜像)
#    - Windows:tauri 直接签 `*-setup.exe`(updater 的 extract_exe 分支接受裸 exe;若某版本改产
#      zip,`infer::archive::is_zip` 分支同样接受,故优先 zip、回退 exe),人用同一个 exe
echo "[3/4] 收集产物"
BUNDLE="desktop/src-tauri/target/$triple/$PROFILE_DIR/bundle"
mkdir -p "$OUT/$platform"
case "$platform" in
  darwin-*)
    upd_candidates=("$BUNDLE/macos"/*.app.tar.gz)
    dist_glob=()
    for f in "$BUNDLE"/dmg/*.dmg; do
      case "$(basename "$f")" in rw.*) ;; *) dist_glob+=("$f") ;; esac
    done
    ;;
  windows-x86_64)
    dist_glob=("$BUNDLE/nsis"/*-setup.exe)
    upd_candidates=("$BUNDLE/nsis"/*.zip "${dist_glob[@]}")
    ;;
esac
if [ "${#dist_glob[@]}" -eq 0 ]; then
  echo "缺分发安装包(dmg / -setup.exe);检查 tauri.conf.json 的 bundle.targets" >&2
  exit 1
fi
upd_file=""
for c in "${upd_candidates[@]}"; do
  if [ -f "$c" ]; then upd_file="$c"; break; fi
done
if [ -z "$upd_file" ]; then
  echo "缺 updater 产物(候选:${upd_candidates[*]})" >&2
  echo "  → 检查 tauri.conf.json 的 bundle.createUpdaterArtifacts(=true)与签名私钥是否就位" >&2
  exit 1
fi
cp "${dist_glob[@]}" "$OUT/$platform/"
need_copy=1
for f in "${dist_glob[@]}"; do
  [ "$f" = "$upd_file" ] && need_copy=0
done
if [ "$need_copy" = 1 ]; then cp "$upd_file" "$OUT/$platform/"; fi
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
