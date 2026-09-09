#!/usr/bin/env bash
# 全局安装(macOS/Linux,方案 A):一次部署,任意目录敲 `gah` 启动(pi 式)。
#   - 构建 gah → 安装到 INSTALL_DIR(默认 ~/.local/gah)→ 符号链接进 ~/.local/bin
#   - 数据根(R8)= 二进制同级 gah-data/(即 ~/.local/gah/gah-data,首次运行自动创建);
#     GAH_HOME env 不作输入源;卸载会删除数据,需要保留请先 /backup。
# 用法:
#   bash scripts/install.sh                      # 构建并安装(已有则覆盖升级)
#   INSTALL_DIR=~/custom bash scripts/install.sh # 自定义安装目录
#   bash scripts/install.sh --uninstall          # 卸载(删符号链接 + 安装目录含数据)
# 前置:Go 工具链(本地构建);Windows 见 scripts/install.ps1。
set -euo pipefail
cd "$(dirname "$0")/.."

PROG=gah
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/gah}"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"

if [ "${1:-}" = "--uninstall" ]; then
  rm -f "$BIN_DIR/$PROG"
  if [ -d "$INSTALL_DIR" ]; then
    rm -rf "$INSTALL_DIR"
    echo "已卸载:$INSTALL_DIR(数据目录已删除;如需保留请先 /backup)"
  else
    echo "已卸载(未找到 $INSTALL_DIR)"
  fi
  exit 0
fi

echo "[1/3] 准备目录:$INSTALL_DIR / $BIN_DIR"
mkdir -p "$INSTALL_DIR" "$BIN_DIR"

echo "[2/3] 构建 gah(当前源码)"
ver="dev"
if tag="$(git describe --tags --exact-match 2>/dev/null)"; then ver="${tag#v}"; fi
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$INSTALL_DIR/$PROG" ./cmd/gah

echo "[3/3] 符号链接:$BIN_DIR/$PROG -> $INSTALL_DIR/$PROG"
ln -sf "$INSTALL_DIR/$PROG" "$BIN_DIR/$PROG"

if ! printf '%s' "$PATH" | tr ':' '\n' | grep -qx "$BIN_DIR"; then
  echo
  echo "提示:$BIN_DIR 不在 PATH。加入后新终端即可用 gah:"
  case "$SHELL" in
    *zsh)  echo '  echo '\''export PATH="$HOME/.local/bin:$PATH"'\'' >> ~/.zshrc' ;;
    *bash) echo '  echo '\''export PATH="$HOME/.local/bin:$PATH"'\'' >> ~/.bashrc' ;;
    *)     echo "  将 $BIN_DIR 加入你的 shell PATH" ;;
  esac
fi

echo
echo "安装完成 ✓"
echo "  命令    : gah(TUI)/ gah web / gah --profile headless -input ..."
echo "  数据根  : $INSTALL_DIR/gah-data(首次运行自动创建;会话按项目 cwd 自动隔离)"
echo "  升级    : 重跑本脚本即可(替换二进制,数据不动)"
echo "  卸载    : bash scripts/install.sh --uninstall"
echo "  便携单飞: 也可直接 cp gah 到任意目录即用(该目录自动建独立 gah-data,与全局隔离)"
