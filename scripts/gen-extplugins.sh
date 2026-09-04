#!/bin/bash
# 生成随包分发的外部工具插件二进制(M6.9 工具类全外部化):tool-basic(三件套)、
# tool-workflow(starlark 引擎,经宿主回调通道)、tool-mcp(MCP client 桥)。
# P4 平台匹配:按发行矩阵(darwin/linux × amd64/arm64 + windows/amd64,与
# .goreleaser.yaml 对齐)每目标构建一份,落 internal/embed/extplugins/<os>-<arch>/;
# 主包交叉编译时经 build-tag 只嵌本平台产物(体积门不变);
# 产物缺失时主包构建失败(goreleaser before hook 调用,防漏)。
# P0 体积门回归(M7):strip(-s -w)+ gzip(Go 二进制压缩率 ~50%);embed 存 .gz,
# 宿主首启 EnsurePlugins 解压落盘。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
EMBED_DIR=internal/embed/extplugins
TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64"
NAMES="tool-basic tool-workflow tool-mcp"

# 清旧产物(平铺 *.gz 与旧平台目录),防跨平台残留
rm -rf "$EMBED_DIR"/*
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  dir="$EMBED_DIR/$os-$arch"
  mkdir -p "$dir"
  for name in $NAMES; do
    echo "build $t/$name"
    GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$dir/$name" "./extplugins/$name"
    gzip -9 -f "$dir/$name"
  done
done
echo "--- 产物清单 ---"
ls -la "$EMBED_DIR"/*/