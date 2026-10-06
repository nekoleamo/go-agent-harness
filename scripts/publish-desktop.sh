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
# 输出目录**用绝对路径**:脚本中途会 cd 进 desktop/src-tauri 再 cd 回来,而便携包那段还用了
# $OLDPWD(那是**调用者**的旧目录,不是脚本 CWD)⇒ run 37002443809 的两个 Windows job 把
# 便携包写到了仓库外,紧接着自检读 $OUT/... 就 FileNotFoundError。绝对路径一次消掉这整类坑。
OUT="$PWD/dist-desktop"
mkdir -p "$OUT"

# 便携版的 bundle identifier(与安装版 dev.gah.desktop **必须不同**)。
#
# 为什么:桌面壳注册了 tauri-plugin-single-instance,单实例锁按 bundle identifier 走。
# 两形态共用一个 identifier 时,安装版与便携版会被当成同一个产品 —— 后开的那个只做
# 「show + set_focus」已有窗口,真机表现是「打开便携版却看到安装版的界面」,且与谁先开无关。
# 两形态数据根本来就不同(安装版在应用数据目录,便携版在用户解压目录),本就不该互顶。
# 详见 3b 步骤的注释。
PORTABLE_IDENTIFIER="dev.gah.desktop.portable"

# 平台 → Rust triple / updater 键 / bundle 子目录
platform="$1"
case "$platform" in
  darwin-aarch64)  triple=aarch64-apple-darwin;   updkey=darwin-aarch64;  bdir=macos ;;
  darwin-x86_64)   triple=x86_64-apple-darwin;    updkey=darwin-x86_64;   bdir=macos ;;
  windows-x86_64)  triple=x86_64-pc-windows-msvc; updkey=windows-x86_64;  bdir=nsis ;;
  windows-arm64)   triple=aarch64-pc-windows-msvc; updkey=windows-arm64;  bdir=nsis ;;
  merge) merge=1 ;;
  rewrite-url) rewrite=1 ;;
  *) echo "用法: $0 {darwin-aarch64|darwin-x86_64|windows-x86_64|windows-arm64|merge|rewrite-url}"; exit 1 ;;
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
    platforms: (reduce .[].platforms as $p ({}; . + $p)),
    # 逐平台合并便携资产。**必须给 // {} 兵底**:只有 Windows 平台产便携包,macOS 那份
    # 没有这个键 ⇒ `.[].portable` 对它求值为 null,而 `{} + null` 直接报错 —— 没有兵底时
    # merge 必崩(不是“少一项”,是整个发版停在这里)。已用混合输入实测过。
    portable: (reduce (.[].portable // {}) as $p ({}; . + $p))
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
  windows-x86_64 | windows-arm64)
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

# 3b. Windows 便携包(一个 zip 解压即用;不进 updater 端点 —— updater 只认安装包)
#
# 为什么是 zip 而不是「真单文件 exe」:壳与 sidecar 天然是两个 exe。真单文件要么自解压
# (多一层、启动变慢、杀软更敏感),要么把 sidecar 塞进壳内资源每次落盘(= 现有外置复制),
# 两者都不比 zip 干净。**zip 就是这个需求的真正形态**。
#
# ⚠ 便携版必须**单独构建一次**(identifier 覆盖),不能直接拿主构建的 exe:
#   桌面壳注册了 tauri-plugin-single-instance,而单实例锁是按 **bundle identifier** 走的
#   (壳侧 main.rs 的 plugin 注册处)。两种形态共用 dev.gah.desktop 时,安装版与便携版
#   会被当成同一个产品 —— 先开的那个把窗口留住,后开的只做「show + set_focus」,
#   真机表现就是「打开便携版却看到安装版的界面」,而且与谁先打开无关(双向都错)。
#   两形态的**数据根本就不同**(安装版 %LOCALAPPDATA%\dev.gah.desktop\bin\、
#   便携版用户解压目录),锁不该互顶。分 identifier 之后:
#     同一形态双击 → 照旧聚焦已有窗口(手滑双击不会开出两个实例);
#     不同形态     → 各自独立跑,各挑各的空闲端口。
#   代价:多跑一次 tauri build(只发生在 Windows;macOS 不发便携包)。
#   updater artifacts 关掉:便携版不做自动更新(见下),不产 updater 产物也不用签名。
#
# 包内四个条目(判定 = 同目录有 portable.marker,壳侧 stage.rs::is_portable):
#   gah/                     顶层目录(避免解压时把一堆文件倒进当前目录)
#   gah/gah-desktop.exe      壳(主程序;名字来自 Cargo 包名 gah-desktop)
#   gah/gah.exe              sidecar(壳按固定名找它,改了就起不来)
#   gah/portable.marker      便携标记(空文件)
#   gah/README-portable.txt  三行说明:数据在哪 / 怎么升级 / 为什么没有自动更新
# ⚠ 便携包**不进 updater 端点**:Windows 不允许覆盖正在运行的 exe,让 updater 去装
#   NSIS 包只会半途失败(壳里已在 checkForUpdatesInner 拦掉并说明原因)。
if [ "$platform" = "windows-x86_64" ] || [ "$platform" = "windows-arm64" ]; then
  # 便携构建会重建 bundle/ 目录(主构建的 .sig 可能随之消失)⇒ 先把签名备份到输出目录,
  # 第 4 步优先读它,省掉一次重新签名(npx 拉 tauri signer)。
  [ -f "$upd_file.sig" ] && cp "$upd_file.sig" "$OUT/$platform/"
  echo "[3b] 便携版单独构建(identifier=$PORTABLE_IDENTIFIER)"
  (cd desktop/src-tauri && \
   # shellcheck disable=SC2086
   $TAURI_BUILD build --target "$triple" \
     --config "{\"version\":\"$VERSION\",\"identifier\":\"$PORTABLE_IDENTIFIER\",\"bundle\":{\"createUpdaterArtifacts\":false}}" \
     $DEBUG_FLAG)
  SHELL_EXE="desktop/src-tauri/target/$triple/$PROFILE_DIR/gah-desktop.exe"
  [ -f "$SHELL_EXE" ] || { echo "缺壳产物:$SHELL_EXE" >&2; exit 1; }
  [ -f "$SIDECAR" ] || { echo "缺 sidecar 产物:$SIDECAR" >&2; exit 1; }
  PSTAGE="$OUT/$platform/portable"
  rm -rf "$PSTAGE"
  mkdir -p "$PSTAGE/gah"
  cp "$SHELL_EXE" "$PSTAGE/gah/gah-desktop.exe"
  cp "$SIDECAR" "$PSTAGE/gah/gah.exe"
  : > "$PSTAGE/gah/portable.marker"   # 空文件即标记
  cat > "$PSTAGE/gah/README-portable.txt" <<TXT
gah 便携版 $VERSION(Windows x64)
================================

怎么用:解压整个 gah 目录到任意可写位置(推荐 D 盘或移动硬盘的专用目录),双击 gah-desktop.exe。
        不用安装,没有开始菜单项,卸载 = 删掉这个目录。

数据在哪:本目录的 gah-data/ 子目录(配置、会话、记忆、凭据都在里面)。
        换机器请把**整个目录**一起拷过去;只拷 gah-desktop.exe 和 gah.exe 是空的。
        重要数据建议定期用对话里的 /backup 导出一份到目录之外。

怎么升级:到发布页下载新的便携包,解压后用新文件覆盖本目录里的同名文件。
        **不要删 gah-data/**(数据在里面),也不要删 portable.marker(删了就变回安装版行为)。

为什么没有自动更新:Windows 不允许覆盖正在运行的程序文件。便携版的升级动作就是
        「下载新包覆盖本目录」。不过托盘里的「检查更新…」**仍可点**:它会查一次版本,
        有新版就直接为你打开便携包的下载页(国内走 Gitee 镜像,取不到时回落 GitHub),
        下载后你自己解压覆盖即可。
        安装版(NSIS)才有真正的自动更新(它能替换自己);两条路互不影响。

注意:未签名分发,首启可能弹 SmartScreen —— 点「更多信息」→「仍要运行」即可。
      数据目录请放在有写权限的位置(桌面/程序目录等受保护位置会写不进去)。
TXT
  # 包名里的架构要跟平台走(写死 x64 会让 arm64 构建产出一个自称 x64 的包 ——
  # run 37000288306 就是这么红的:产物叫 gah_0.4.0_x64-portable.zip,而校验按架构找它,
  # 结果 FileNotFoundError,arm64 首跑因此挂了)。
  case "$platform" in
    windows-arm64) parch=arm64 ;;
    *)            parch=x64 ;;
  esac
  PORTABLE_ZIP="$OUT/gah_${VERSION}_${parch}-portable.zip"
  rm -f "$PORTABLE_ZIP"
  # zip 目录内不放平台元数据(时间戳全固定 ⇒ 同样输入打出同样字节,重跑无 diff)
  if command -v zip >/dev/null 2>&1; then
    (cd "$PSTAGE" && find gah -exec touch -t 200001010000 {} + && TZ=UTC zip -X -q -9 -r "$PORTABLE_ZIP" gah)
  else
    python3 - "$PSTAGE" "$PORTABLE_ZIP" <<'PY'
# 无 zip 命令时(最小 Windows runner 镜像)的等价实现:同样固定时间戳,同样顶层 gah/
import os, sys, zipfile
stage, out = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for root, _dirs, files in os.walk(os.path.join(stage, 'gah')):
        for f in sorted(files):
            p = os.path.join(root, f)
            arc = os.path.relpath(p, stage).replace(os.sep, '/')
            zi = zipfile.ZipInfo(arc, date_time=(2000, 1, 1, 0, 0, 0))
            zi.compress_type = zipfile.ZIP_DEFLATED
            zi.external_attr = 0o755 << 16 if f.endswith('.exe') else 0o644 << 16
            with open(p, 'rb') as fh:
                z.writestr(zi, fh.read())
PY
  fi
  rm -rf "$PSTAGE"
  [ -f "$PORTABLE_ZIP" ] || { echo "便携包未生成:$PORTABLE_ZIP" >&2; exit 1; }
  echo "便携包 → $PORTABLE_ZIP($(du -h "$PORTABLE_ZIP" | cut -f1))"
  echo "  条目:$(unzip -Z1 "$PORTABLE_ZIP" 2>/dev/null | tr '\n' ' ' || python3 -c "import zipfile,sys;print(' '.join(zipfile.ZipFile(sys.argv[1]).namelist()))" "$PORTABLE_ZIP")"
fi

# 4. latest.<平台>.json(签名优先取 tauri-bundler 自己产出的 .sig;缺失时回退 tauri signer)
echo "[4/4] updater 签名"
name="$(basename "$upd_file")"
# 签名来源优先级:输出目录里的备份(便携构建前拷的,最可靠)→ 主构建 bundle 里的 .sig →
# 现场签一次。便携包那次 tauri build 会重建 bundle/,所以第二档可能已经没了。
sig_src=""
[ -f "$OUT/$platform/$name.sig" ] && sig_src="$OUT/$platform/$name.sig"
[ -z "$sig_src" ] && [ -f "$upd_file.sig" ] && sig_src="$upd_file.sig"
if [ -n "$sig_src" ] && [ -s "$sig_src" ]; then
  sig="$(tr -d '\n' < "$sig_src")"
  echo "签名来源:$sig_src"
else
  sig="$(npx -y -p @tauri-apps/cli@2 tauri signer sign -k "$KEY" "$OUT/$platform/$name" 2>/dev/null | tail -1)"
  echo "签名来源:tauri signer sign(回退)"
fi
[ "$KEY_TMP" = 1 ] && rm -f "$KEY"
if [ -z "$sig" ]; then echo "updater 签名失败(无 .sig 且 tauri signer 失败)"; exit 1; fi

url="https://github.com/$REPO/releases/download/v$VERSION/$name"
# 便携资产(**不进 platforms**):updater 只会把它当安装包下载,而便携包不能被 updater 装
# (Windows 不允许覆盖正在运行的 exe)。放顶层 portable 键 = 版本清单里能看到便携包在哪,
# 而 platforms 的结构与 updater 契约**一字不动**(tauri-plugin-updater 2.11 的 manifest
# 解析没有 deny_unknown_fields,且 Static 形状靠 platforms 字段识别,顶层多一个键无害)。
# 壳侧当前**不读**这个键(没有 HTTPS 客户端依赖,不为一次检查更新加依赖):它按
# 「{站点}/releases/download/v{版本}/gah_{版本}_{架构}-portable.zip」这个形态开下载页 ——
# 形态依据见 scripts/mirror-gitee.sh 的 link_of(两站同形,已逐条验证)。登记的价值是
# 「可盘点 + 可被发布校验断言」,而不是给壳省一步。
portable_json=""
case "$platform" in
  windows-arm64) pkey=windows-arm64; parch=arm64 ;;
  windows-x86_64) pkey=windows-x86_64; parch=x64 ;;
  *) pkey="" ; parch="" ;;
esac
if [ -n "$pkey" ] && [ -f "$OUT/gah_${VERSION}_${parch}-portable.zip" ]; then
  portable_json="$(jq -n --arg v "$VERSION" --arg p "$pkey" --arg u "https://github.com/$REPO/releases/download/v$VERSION/gah_${VERSION}_${parch}-portable.zip" \
    '{($p): {url: $u, note: "便携包:解压后覆盖本目录,不要删 gah-data/"}}')"
else
  portable_json='{}'
fi
jq -n --arg v "$VERSION" --arg d "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --arg u "$url" --arg s "$sig" --arg k "$updkey" --argjson portable "$portable_json" \
      '{version: $v, pub_date: $d, platforms: {($k): {url: $u, signature: $s}}, portable: $portable}' \
  > "$OUT/latest.$platform.json"
cat "$OUT/latest.$platform.json"
echo "完成。多平台发布:各平台跑本脚本后执行 bash scripts/publish-desktop.sh merge,上传 dist-desktop/latest.json 与安装包到 GitHub Release(v$VERSION)"
