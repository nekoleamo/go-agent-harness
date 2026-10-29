#!/usr/bin/env bash
# 组装 macOS 发行包:`gah.app` + 更新器要的 `.app.zip`(+ 可选 dmg)。
#
# 为什么手写而不是用打包框架:壳只有一个二进制 + 一个 sidecar,Info.plist 就十几行;
# 用框架会再拉一套工具链(与「省磁盘」的初衷相反)。Windows 侧仍是 NSIS(见
# scripts/publish-desktop.sh),首次安装走 dmg/NSIS,升级走 updater 的 zip。
#
# 用法:
#   bash scripts/package-desktop-wails.sh            # 构建 .app 与 .app.zip
#   bash scripts/package-desktop-wails.sh --dmg      # 额外出 dmg
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

VERSION="$(git describe --tags 2>/dev/null | sed 's/^v//')"
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

# 图标:沿用桌面壳工程里已有的 icns(不重新生成,免得引入 icon 工具链)。
ICNS=desktop/src-tauri/icons/icon.icns
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
shasum -a 256 "$ZIP" | awk '{print "SHA256:", $1}'

if [ "$DO_DMG" = "1" ]; then
  DMG="$OUT_DIR/gah-${VERSION}-${TRIPLE}.dmg"
  rm -f "$DMG"
  hdiutil create -volname "gah" -srcfolder "$APP" -ov -format UDZO "$DMG" >/dev/null
  echo "DMG:  $DMG ($(du -h "$DMG" | cut -f1))"
fi

echo "下一步:desktopman sign --artifact $ZIP --platform darwin-$( [ "$ARCH" = arm64 ] && echo aarch64 || echo x86_64 ) ..."
