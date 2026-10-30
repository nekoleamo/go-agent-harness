#!/usr/bin/env bash
# 组装 macOS 发行包:`gah.app` + 更新器要的 `.app.zip`(+ 可选 dmg)。
#
# 为什么手写而不是用打包框架:壳只有一个二进制 + 一个 sidecar,Info.plist 就十几行;
# 用框架会再拉一套工具链(与「省磁盘」的初衷相反)。Windows 侧是 NSIS(见 desktop/wails/installer/),
# 首次安装走 dmg/NSIS,升级走 updater 的 zip。
#
# 用法:
#   bash scripts/package-desktop-wails.sh            # 构建 .app 与 .app.zip
#   bash scripts/package-desktop-wails.sh --dmg      # 额外出 dmg
#   RELEASE_VERSION=0.8.0-preview bash scripts/package-desktop-wails.sh --no-build --dmg
#     ↑ 发版路径**必须显式给版本**:缺省取 `git describe`,在非 tag 提交上会变成
#       "0.7.4-26-gabcd" —— Info.plist 就与清单/壳里的版本不一致了(2026-10-30 实测)。
#
# 关键点:
#   - **bundle identifier 必须有**:macOS 的系统通知服务在这个上面直接失败
#     (notifications require a valid bundle identifier,本轮实测)
#   - sidecar 与壳同目录(Contents/MacOS/),与发行布局一致
#   - 版本号取 git describe,写进 CFBundleShortVersionString 与 updater 清单
set -euo pipefail
cd "$(dirname "$0")/.."

DO_DMG=0
NO_BUILD=0
for arg in "$@"; do
  case "$arg" in
    --dmg) DO_DMG=1 ;;
    --no-build) NO_BUILD=1 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "未知参数:$arg"; exit 2 ;;
  esac
done

[ "$(uname -s)" = "Darwin" ] || { echo "packaging: 只支持 macOS(Windows 走 NSIS)"; exit 1; }

VERSION="${RELEASE_VERSION:-$(git describe --tags 2>/dev/null | sed 's/^v//')}"
[ -n "$VERSION" ] || VERSION="0.0.0-dev"
ARCH="$(uname -m)"
case "$ARCH" in
  arm64) TRIPLE=aarch64-apple-darwin ;;
  x86_64) TRIPLE=x86_64-apple-darwin ;;
  *) echo "packaging: 未知架构 $ARCH"; exit 1 ;;
esac

# --no-build:发行脚本已经构建过(带公钥/版本注入),这里只组装,免得再编一遍覆盖掉。
[ "$NO_BUILD" = "1" ] || bash scripts/gen-desktop-wails.sh --release >/dev/null

APP="desktop/wails/build/gah.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# 只放壳:sidecar 已 gzip 内嵌在壳里(见 desktop/wails/embed_sidecar.go),
# 于是 .app 只有一个可执行文件,更新器换这一个文件就等于整包升级。
cp desktop/wails/build/gah-desktop "$APP/Contents/MacOS/gah-desktop"
chmod +x "$APP/Contents/MacOS/gah-desktop"

# 图标:壳自己的 assets(不再依赖旧 Tauri 工程目录 —— 那条链删掉后这里会静默丢图标)。
ICNS=desktop/wails/assets/gah.icns
[ -f "$ICNS" ] && cp "$ICNS" "$APP/Contents/Resources/icon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>gah</string>
  <key>CFBundleDisplayName</key><string>gah</string>
  <key>CFBundleIdentifier</key><string>dev.gah.desktop</string>
  <key>CFBundleExecutable</key><string>gah-desktop</string>
  <key>CFBundleIconFile</key><string>icon.icns</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>10.15</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
  <!-- 窗口直连 http://127.0.0.1:<随机端口>(gah 自己提供页面)。ATS 默认只放行 HTTPS,
       所以显式允许任意加载(仅本机回环,不出网);不加这条时表现为窗口白屏。 -->
  <key>NSAppTransportSecurity</key>
  <dict><key>NSAllowsArbitraryLoads</key><true/></dict>
</dict>
</plist>
PLIST

OUT_DIR="desktop/wails/build"
ZIP="$OUT_DIR/gah-${VERSION}-${TRIPLE}.app.zip"

# 代码签名:macOS 按「签名标识 + bundle 封章」认应用,而 Go 链接器只给**可执行文件**
# 一层 adhoc 签名(flags=adhoc,linker-signed:`codesign -dv` 显示 Identifier=a.out、
# Info.plist=not bound)。2026-10-30 真机现场:这样组装的 .app 一发送通知就是
# UNErrorDomain error 1,通知中心里也查不到 gah 这一项。wails 的 updater 同样要求
# 「打包并签名」(validate.go 对本情形是宽容不改判,但那是容错不是正确)。
# 有开发者身份就用它(发行版),否则 adhoc(本地/内测;系统接受,codesign --verify 也过)。
# 签名必须在打 zip **之前**:包内容再变(换二进制)就得重签。
SIGN_ID="${GAH_CODESIGN_ID:--}"
codesign --force --sign "$SIGN_ID" "$APP" >/dev/null
codesign --verify --strict --verbose=2 "$APP" >/dev/null 2>&1 || {
  echo "package: 签名后校验失败(bundle: $APP)" >&2
  exit 1
}
echo "签名: $SIGN_ID($(codesign -dv "$APP" 2>&1 | awk -F= '/^Identifier=/{print $2}'))"

rm -f "$ZIP"
# ditto 保资源叉与符号链接;--keepParent 让 zip 里只有一个顶层条目(.app),
# 这是 updater 的硬要求:多于一个顶层条目会被拒绝(staging 无法确定换哪个)。
# 不加 --sequesterRsrc:ditto 会额外塞一个 `__MACOSX/` 顶层条目,而 wails updater 要求
# zip **恰好一个顶层条目**,否则拒绝(实测过:__MACOSX 也不算例外)。
ditto -c -k --keepParent "$APP" "$ZIP"

# 硬闸:顶层条目必须恰好一个(这一条错了,发行出去的包在用户机器上装不上,
# 而 CI 里看不出来 —— 只能靠这里拦住)。
python3 - "$ZIP" <<'PYZ'
import sys, zipfile
tops = {n.split('/', 1)[0] for n in zipfile.ZipFile(sys.argv[1]).namelist()}
if len(tops) != 1:
    sys.exit(f"package: zip 顶层条目必须恰好 1 个,实际 {sorted(tops)}(updater 会拒绝)")
print("zip 顶层条目:", tops.pop())
PYZ
echo "APP:  $APP"
echo "ZIP:  $ZIP ($(du -h "$ZIP" | cut -f1))"
shasum_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi; }
shasum_of "$ZIP" | awk '{print "SHA256:", $1}'

if [ "$DO_DMG" = "1" ]; then
  DMG="$OUT_DIR/gah-${VERSION}-${TRIPLE}.dmg"
  rm -f "$DMG"
  # dmg 只是首装的便利包(**不进**更新清单):hdiutil 失败(磁盘满/权限)时告警继续,
  # 不要让一个可选产物把整版发行卡死。
  if hdiutil create -volname "gah" -srcfolder "$APP" -ov -format UDZO "$DMG" >/dev/null 2>&1; then
    echo "DMG:  $DMG ($(du -h "$DMG" | cut -f1))"
  else
    echo "package: ！生成 dmg 失败(可选产物,继续;首装可用 .app.zip)" >&2
  fi
fi

echo "下一步:desktopman sign --artifact $ZIP --platform darwin-$( [ "$ARCH" = arm64 ] && echo aarch64 || echo x86_64 ) ..."
