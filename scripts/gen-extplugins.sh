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

# assert_arch <bin> <expect-os> <expect-arch>:build 后(压缩前)校验产物头部魔数
# 与架构字段 == 目标平台(P4 漂移护栏:脚本/工具链改动漏平台立即失败)。
# ELF e_machine@18(amd64=0x3e,arm64=0xb7);Mach-O LE64 cputype@4(amd64=0x01000007,
# arm64=0x0100000c);PE e_lfanew@0x3C→sig "PE\0\0"→machine@+4(amd64=0x8664)。
assert_arch() {
  local bin="$1" eos="$2" earch="$3" actual=""
  local magic
  magic=$(head -c 4 "$bin" | od -An -tx1 | tr -d ' \n')
  case "$magic" in
    7f454c46) # ELF
      local m
      m=$(dd if="$bin" bs=1 skip=18 count=2 2>/dev/null | od -An -tx1 | tr -d ' \n')
      case "$m" in
        3e00) actual="linux/amd64" ;;
        b700) actual="linux/arm64" ;;
        *) actual="linux/?" ;;
      esac
      ;;
    cffaedfe) # Mach-O little-endian 64
      local c
      c=$(dd if="$bin" bs=1 skip=4 count=4 2>/dev/null | od -An -tx1 | tr -d ' \n')
      case "$c" in
        07000001) actual="darwin/amd64" ;;
        0c000001) actual="darwin/arm64" ;;
        *) actual="darwin/?" ;;
      esac
      ;;
    4d5a*) # PE(前 2 字节 MZ,后 2 字节为 DOS stub 内容)
      local lfhex sighex mhex lf
      lfhex=$(dd if="$bin" bs=1 skip=60 count=4 2>/dev/null | od -An -tx1 | tr -d ' \n')
      lf=$((0x${lfhex:6:2}${lfhex:4:2}${lfhex:2:2}${lfhex:0:2})) # LE 反转字组
      sighex=$(dd if="$bin" bs=1 skip="$lf" count=4 2>/dev/null | od -An -tx1 | tr -d ' \n')
      if [ "$sighex" = 50450000 ]; then
        mhex=$(dd if="$bin" bs=1 skip=$((lf + 4)) count=2 2>/dev/null | od -An -tx1 | tr -d ' \n')
        [ "$mhex" = 6486 ] && actual="windows/amd64" || actual="windows/?"
      else
        actual="windows/?"
      fi
      ;;
    *) actual="?/?" ;;
  esac
  if [ "$actual" != "$eos/$earch" ]; then
    echo "assert_arch FAIL: $bin = $actual, want $eos/$earch" >&2
    exit 1
  fi
  echo "assert_arch OK: $bin = $actual"
}

# 清旧产物(平铺 *.gz 与旧平台目录),防跨平台残留
rm -rf "$EMBED_DIR"/*
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  dir="$EMBED_DIR/$os-$arch"
  mkdir -p "$dir"
  for name in $NAMES; do
    echo "build $t/$name"
    GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$dir/$name" "./extplugins/$name"
    assert_arch "$dir/$name" "$os" "$arch"
    gzip -9 -n -f "$dir/$name" # -n:不存 mtime/文件名,产物幂等(重跑无 diff)
  done
done
echo "--- 产物清单 ---"
ls -la "$EMBED_DIR"/*/