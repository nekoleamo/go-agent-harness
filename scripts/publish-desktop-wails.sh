#!/usr/bin/env bash
# 桌面壳(Go + Wails v3)发行:构建 → 打包 → 签名 → 清单。
#
# 旧的 Tauri 链已于 2026-10-29 删除。与它的差异:
#   - 产物形态:wails updater 只吃「单文件 / 唯一顶层条目的 zip」
#       mac  → gah-<ver>-<triple>.app.zip(整个 .app 打包,sidecar 在内嵌壳里)
#       win  → gah-<ver>-<triple>.zip(单个 exe,sidecar 同样内嵌)
#   - 清单换格式:desktop-latest.json {version,notes,pub_date,artifacts{平台键:{url,size,sha256,sig}}}
#     签名对象 = 产物的 **sha256 摘要**(旧链是 minisign over 整包,口径不同,不能混用)
#   - 首次安装仍是 dmg / NSIS;updater 只负责后续升级
#
# 用法:
#   bash scripts/publish-desktop-wails.sh darwin-aarch64    # 在 mac 上
#   bash scripts/publish-desktop-wails.sh windows-x86_64    # 在 win 上(或 mac 交叉编,见下)
#   bash scripts/publish-desktop-wails.sh merge             # 合并各平台清单 + 生成校验和
#
# 平台键 = **GOOS-<归一化 arch>**(darwin-aarch64 / windows-x86_64 / windows-aarch64):
# 壳侧 platformKey() 用同一形状查清单,两边写不一致的后果是「这个平台永远查不到更新」
# (2026-10-30 发现 windows-arm64 这种 Go 拼法曾被当作清单键写入 —— 见下面的拒绝分支)。
#
# 环境变量:
#   GAH_DESKTOP_UPDATE_KEY  签名私钥(缺省 ~/.gah-desktop/update.key;**勿入库**)
#   RELEASE_VERSION         版本号(缺省 git describe 去 v)
#   RELEASE_NOTES           更新说明(缺省取空)
#   GAH_DESKTOP_URL_BASE    产物下载前缀(缺省 GitHub release 直链;Gitee 镜像用它覆盖)
#
# 说明:Windows 目标可以在 mac 上交叉编译(Wails v3 的 windows 实现不需要 cgo,
# 已实测 `GOOS=windows go build` 通过);但**运行验证**必须真机。
set -euo pipefail
cd "$(dirname "$0")/.."

PLATFORM="${1:-}"
case "$PLATFORM" in
  darwin-aarch64|darwin-x86_64|windows-x86_64|windows-aarch64|merge) ;;
  windows-arm64)
    # 曾经这么拼:列表键写成 Go 的 arch,而壳查的是 windows-aarch64 ⇒ 永远查不到更新。
    echo "publish: 清单键是 windows-aarch64(不是 windows-arm64);产物 triple 仍在脚本内部映射为 aarch64-pc-windows-msvc" >&2
    exit 2 ;;
  -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
  *) echo "用法: $0 {darwin-aarch64|darwin-x86_64|windows-x86_64|windows-aarch64|merge}"; exit 2 ;;
esac

ROOT="$PWD"
OUT_ROOT="$ROOT/dist-desktop-wails"
VERSION="${RELEASE_VERSION:-$(git describe --tags 2>/dev/null | sed 's/^v//')}"
[ -n "$VERSION" ] || { echo "publish: 取不到版本号(设 RELEASE_VERSION)"; exit 1; }
KEY="${GAH_DESKTOP_UPDATE_KEY:-$HOME/.gah-desktop/update.key}"

# winpath:把 git-bash(MSYS)路径转成 Windows 原生形态,交给 Windows 程序(windows 上无 cygpath 时原样返回)。
# 为什么要它:同一个 makensis 命令,`C:/a/...` 在 x86_64 runner 上能认,在 arm64 runner 上报
# `no files found`(2026-10-30 实测);反过来原生 `C:\a\...` 两边都认。
winpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
}

# sha256_of:sha256sum(Linux)优先,否则 shasum(mac)。输出「摘要  文件」两列。
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}


# ---------- 合并 ----------
if [ "$PLATFORM" = "merge" ]; then
  files=()
  for d in "$OUT_ROOT"/*/; do
    [ -d "$d" ] || continue
    for f in "$d"desktop-latest.*.json; do
      [ -f "$f" ] && files+=("$f")
    done
  done
  [ ${#files[@]} -gt 0 ] || { echo "publish: $OUT_ROOT 下没有平台清单"; exit 1; }
  python3 - "$OUT_ROOT/desktop-latest.json" "$VERSION" "${files[@]}" <<'PY'
import json, sys, pathlib
out, version, files = sys.argv[1], sys.argv[2], sys.argv[3:]
merged = {"version": "", "notes": "", "pub_date": "", "artifacts": {}}
for f in files:
    d = json.load(open(f))
    if d.get("version") != version:
        sys.exit(f"publish: {f} 版本 {d.get('version')} 与本次 {version} 不一致(拒绝合并)")
    merged["version"] = version
    merged["notes"] = merged["notes"] or d.get("notes", "")
    merged["pub_date"] = merged["pub_date"] or d.get("pub_date", "")
    merged["artifacts"].update(d.get("artifacts", {}))
merged["artifacts"] = {k: merged["artifacts"][k] for k in sorted(merged["artifacts"])}
if not merged["artifacts"]:
    sys.exit("publish: 合并后没有任何产物")
pathlib.Path(out).write_text(json.dumps(merged, ensure_ascii=False, indent=2) + "\n")
print(f"merged → {out}: {version} {sorted(merged['artifacts'])}")
PY
  # 校验和(独立文件名:不与 goreleaser 的 checksums.txt 争)。
  # sha256_of 走 sha256sum/shasum 双路径:merge job 跑在 ubuntu 上,只有 sha256sum 是标配。
  (
    cd "$OUT_ROOT"
    find . -type f \( -name '*.zip' -o -name '*.dmg' \) | sort | while read -r f; do sha256_of "$f"; done
  ) > "$OUT_ROOT/checksums-desktop-wails.txt"
  echo "checksums → $OUT_ROOT/checksums-desktop-wails.txt"
  exit 0
fi

# num_version:NSIS 的 VIProductVersion 只接受**纯数字四段**(x.x.x.x),
# 而预告版 tag 是 0.8.0-preview ⇒ 直接拼成 "0.8.0-preview.0" 会让 makensis 报
# 「invalid VIProductVersion」直接失败(Windows 侧整版发不出去)。
# 非数字段一律当 0;不追求语义正确,
# 只要求「能编过且单调」—— 真正的版本号在 DisplayVersion/清单/壳里。
num_version() {
  local core="${1%%-*}" a b c
  core="${core%%+*}"
  IFS=. read -r a b c <<< "$core"
  for p in "$a" "$b" "$c"; do
    case "$p" in ''|*[!0-9]*) echo "publish: 版本号 $1 的前三段必须是数字(NumVersion 用)" >&2; return 1 ;; esac
  done
  printf '%s.%s.%s.0' "$a" "$b" "${c:-0}"
}

# ---------- 单平台 ----------
case "$PLATFORM" in
  darwin-aarch64)  GOOS=darwin;  GOARCH=arm64; TRIPLE=aarch64-apple-darwin ;;
  darwin-x86_64)   GOOS=darwin;  GOARCH=amd64; TRIPLE=x86_64-apple-darwin ;;
  windows-x86_64)  GOOS=windows; GOARCH=amd64; TRIPLE=x86_64-pc-windows-msvc ;;
  windows-aarch64) GOOS=windows; GOARCH=arm64; TRIPLE=aarch64-pc-windows-msvc ;;
esac

[ -f "$KEY" ] || { echo "publish: 找不到签名私钥 $KEY(用 desktopman keygen 生成,勿入库)"; exit 1; }

OUT="$OUT_ROOT/$PLATFORM"
rm -rf "$OUT"; mkdir -p "$OUT"

SHELL_MOD=desktop/wails
BIN_DIR="$SHELL_MOD/binaries"
EXE="$SHELL_MOD/build/gah-desktop"
[ "$GOOS" = "windows" ] && EXE="$SHELL_MOD/build/gah-desktop.exe"

# 前置:web/dist 与 extplugins 必须现成(go:embed 在编译期就要它们);缺了报一句人话,
# 而不是让 go build 抛一堆 embed 路径错误。
[ -d web/dist ] || { echo "publish: 缺 web/dist —— 先跑 bash scripts/gen-web.sh"; exit 1; }

echo "[1/4] sidecar: $GOOS/$GOARCH"
mkdir -p "$BIN_DIR"
SIDECAR="$BIN_DIR/gah-$TRIPLE"
[ "$GOOS" = "windows" ] && SIDECAR="$SIDECAR.exe"
CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -trimpath -ldflags "-s -w" -o "$SIDECAR" ./cmd/gah

echo "[2/4] 壳 + 内嵌 sidecar"
mkdir -p "$SHELL_MOD/embed"
gzip -9 -n -c "$SIDECAR" > "$SHELL_MOD/embed/gah.bin.gz"
# darwin 的壳必须 cgo(objc 绑定);windows 实现是纯 Go,可 CGO=0(于是能在 mac 上交叉编)。
CGO=0; [ "$GOOS" = "darwin" ] && CGO=1
# 验签公钥必须烧进壳(信任根不能来自发行渠道);没给就明确警告 —— 那种包只能校验摘要。
PUBKEY="${GAH_DESKTOP_UPDATE_PUBKEY:-}"
LD="-s -w -X main.version=$VERSION"
if [ -n "$PUBKEY" ]; then
  LD="$LD -X main.updatePubKeyHex=$PUBKEY"
else
  echo "publish: ！未提供 GAH_DESKTOP_UPDATE_PUBKEY —— 本次产物只校验摘要,不应作为正式发行"
fi
(cd "$SHELL_MOD" && CGO_ENABLED=$CGO GOOS=$GOOS GOARCH=$GOARCH go build -trimpath -tags embed_sidecar \
   -ldflags "$LD" -o "build/$(basename "$EXE")" .)

echo "[3/4] 打包(唯一顶层条目)"
case "$GOOS" in
  darwin)
    [ "$(uname -s)" = "Darwin" ] || { echo "publish: darwin 产物只能在 mac 上组装(.app 需要 ditto)"; exit 1; }
    # 版本必须**显式传进去**:package 脚本自己的缺省是 `git describe`,在非 tag 提交上会得到
    # "0.7.4-26-gxxxx" 这种字符串 —— 那样 Info.plist 里的版本与清单/壳里的版本就对不上了
    # (2026-10-30 实测)。--dmg 顺带出首装用的 dmg;hdiutil 失败只告警(它不进清单)。
    RELEASE_VERSION="$VERSION" bash scripts/package-desktop-wails.sh --no-build --dmg >/dev/null \
      || { echo "publish: 组装 .app/.app.zip 失败"; exit 1; }
    ART="$OUT/gah-$VERSION-$TRIPLE.app.zip"
    cp "$SHELL_MOD/build/gah-$VERSION-$TRIPLE.app.zip" "$ART"
    if [ -f "$SHELL_MOD/build/gah-$VERSION-$TRIPLE.dmg" ]; then
      cp "$SHELL_MOD/build/gah-$VERSION-$TRIPLE.dmg" "$OUT/gah-$VERSION-$TRIPLE.dmg"
    fi
    ;;
  windows)
    # ① 升级产物:单 exe 的 zip(updater 只换这一个文件;sidecar 已内嵌)
    ART="$OUT/gah-$VERSION-$TRIPLE.zip"
    go run ./desktop/wails/cmd/desktopman pack --in "$EXE" --entry gah-desktop.exe --out "$ART"

    # ② 首次安装:NSIS setup.exe(按用户安装,不弹 UAC)。
    #    **只在 x86_64 目标上产**:安装器本身就是 x64 的(arm64 机器靠系统模拟运行),而在
    #    arm64 runner 上的 makensis 是另一份发行(2026-10-30 实测:它对 `C:/a/...` 形式的绝对
    #    路径报 `no files found`,同一条命令在 x86_64 runner 上正常)—— 给不存在的产物多一次
    #    失败机会没有意义。arm64 用户走便携 zip 或模拟安装 x64 安装器(README 已写明)。
    if [ "$PLATFORM" = "windows-x86_64" ] && command -v makensis >/dev/null 2>&1; then
      SETUP="$OUT/gah-$VERSION-setup.exe"
      NUMVER="$(num_version "$VERSION")" || exit 1
      [ -f "$ROOT/$EXE" ] || { echo "publish: 壳产物缺失 $ROOT/$EXE(NSIS 无从打包)"; exit 1; }
      makensis -V2 -DVERSION="$VERSION" -DNUMVERSION="$NUMVER" \
        -DAPPEXE="$(winpath "$ROOT/$EXE")" -DOUTFILE="$(winpath "$SETUP")" \
        -DICONDIR="$(winpath "$ROOT/desktop/wails/installer")" \
        desktop/wails/installer/installer.nsi
      echo "SETUP: $SETUP"
    elif [ "$PLATFORM" != "windows-x86_64" ]; then
      echo "publish: 非 x86_64 目标不产 NSIS 安装包(arm64 用便携 zip / 系统模拟安装)"
    elif [ "${GAH_DESKTOP_REQUIRE_NSIS:-}" = "1" ]; then
      # 发行路径上缺 makensis = 发出去的版本没有首装包(用户只能靠 zip 解压),
      # 静默跳过会让这件事直到 merge job 的 fail_on_unmatched_files 才暴露,甚至更晚。
      echo "publish: 缺 makensis 而本次要求产 NSIS 安装包(GAH_DESKTOP_REQUIRE_NSIS=1)" >&2
      exit 1
    else
      echo "publish: 本机无 makensis —— 跳过 NSIS 安装包(CI 的 windows runner 会产出)"
    fi

    # ③ 便携包:解压即用(同目录放 portable.marker ⇒ 数据根 = 程序目录/gah-data,
    #    删目录即删数据,与安装版把数据放 %LOCALAPPDATA% 的形态区分开)
    PORT_DIR="$OUT/portable/gah"
    mkdir -p "$PORT_DIR"
    cp "$EXE" "$PORT_DIR/gah-desktop.exe"
    : > "$PORT_DIR/portable.marker"
    cat > "$PORT_DIR/README.txt" <<'TXT'
gah 便携版

- 双击 gah-desktop.exe 运行;所有数据在本目录的 gah-data/(换机器请整个目录一起拷)
- 便携版不自动更新(Windows 不允许覆盖正在运行的程序):请下载新包覆盖本目录
- 想彻底清理:退出 gah 后删除整个目录即可
TXT
    go run ./desktop/wails/cmd/desktopman pack --in "$PORT_DIR" --entry gah \
      --out "$OUT/gah-$VERSION-portable-$TRIPLE.zip"
    rm -rf "$OUT/portable"
    ;;
esac
[ -f "$ART" ] || { echo "publish: 产物缺失 $ART"; exit 1; }

echo "[4/4] 签名 + 清单"
URL_BASE="${GAH_DESKTOP_URL_BASE:-https://github.com/nekoleamo/go-agent-harness/releases/latest/download}"
go run ./desktop/wails/cmd/desktopman sign \
  --key "$KEY" --artifact "$ART" --platform "$PLATFORM" \
  --version "$VERSION" --url "$URL_BASE/$(basename "$ART")" \
  --notes "${RELEASE_NOTES:-}" --pub-date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --out "$OUT/desktop-latest.$PLATFORM.json"

# 上传前自检:清单里的摘要/大小必须与产物对得上(签名与产物脱节是最难查的一类错)。
go run ./desktop/wails/cmd/desktopman verify \
  --manifest "$OUT/desktop-latest.$PLATFORM.json" --platform "$PLATFORM" --artifact "$ART" 2>&1 | sed 's/^/  /'

ls -lh "$ART" | awk '{print "ART:", $9, $5}'
sha256_of "$ART" | awk '{print "SHA256:", $1}'
echo "清单: $OUT/desktop-latest.$PLATFORM.json"
