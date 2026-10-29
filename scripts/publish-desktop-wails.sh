#!/usr/bin/env bash
# 桌面壳(Go + Wails v3)发行:构建 → 打包 → 签名 → 清单。
#
# 与旧链(scripts/publish-desktop.sh,Tauri)并列存在,主干合并后删旧的。差异:
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
  darwin-aarch64|darwin-x86_64|windows-x86_64|windows-arm64|merge) ;;
  -h|--help) sed -n '2,27p' "$0"; exit 0 ;;
  *) echo "用法: $0 {darwin-aarch64|darwin-x86_64|windows-x86_64|windows-arm64|merge}"; exit 2 ;;
esac

ROOT="$PWD"
OUT_ROOT="$ROOT/dist-desktop-wails"
VERSION="${RELEASE_VERSION:-$(git describe --tags 2>/dev/null | sed 's/^v//')}"
[ -n "$VERSION" ] || { echo "publish: 取不到版本号(设 RELEASE_VERSION)"; exit 1; }
KEY="${GAH_DESKTOP_UPDATE_KEY:-$HOME/.gah-desktop/update.key}"

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
  # 校验和(独立文件名:不与 goreleaser 的 checksums.txt 争)
  ( cd "$OUT_ROOT" && find . -maxdepth 2 -name '*.zip' -o -maxdepth 2 -name '*.dmg' | sort | while read -r f; do
      [ -f "$f" ] && shasum -a 256 "$f"
    done ) > "$OUT_ROOT/checksums-desktop-wails.txt"
  echo "checksums → $OUT_ROOT/checksums-desktop-wails.txt"
  exit 0
fi

# ---------- 单平台 ----------
case "$PLATFORM" in
  darwin-aarch64)  GOOS=darwin;  GOARCH=arm64; TRIPLE=aarch64-apple-darwin ;;
  darwin-x86_64)   GOOS=darwin;  GOARCH=amd64; TRIPLE=x86_64-apple-darwin ;;
  windows-x86_64)  GOOS=windows; GOARCH=amd64; TRIPLE=x86_64-pc-windows-msvc ;;
  windows-arm64)   GOOS=windows; GOARCH=arm64; TRIPLE=aarch64-pc-windows-msvc ;;
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
    bash scripts/package-desktop-wails.sh --no-build >/dev/null
    ART="$OUT/gah-$VERSION-$TRIPLE.app.zip"
    cp "$SHELL_MOD/build/gah-$VERSION-$TRIPLE.app.zip" "$ART" 2>/dev/null || \
      cp "$(ls -t "$SHELL_MOD"/build/*.app.zip | head -1)" "$ART"
    ;;
  windows)
    # ① 升级产物:单 exe 的 zip(updater 只换这一个文件;sidecar 已内嵌)
    ART="$OUT/gah-$VERSION-$TRIPLE.zip"
    go run ./desktop/wails/cmd/desktopman pack --in "$EXE" --entry gah-desktop.exe --out "$ART"

    # ② 首次安装:NSIS setup.exe(按用户安装,不弹 UAC)。makensis 只在 Windows/CI 上有。
    if command -v makensis >/dev/null 2>&1; then
      SETUP="$OUT/gah-$VERSION-setup.exe"
      makensis -V2 -DVERSION="$VERSION" -DAPPEXE="$EXE" -DOUTFILE="$SETUP" \
        desktop/wails/installer/installer.nsi
      echo "SETUP: $SETUP"
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
shasum -a 256 "$ART" | awk '{print "SHA256:", $1}'
echo "清单: $OUT/desktop-latest.$PLATFORM.json"
