#!/bin/bash
# 生成随包分发的外部工具插件二进制(M6.9 工具类全外部化):**一个** tool-kit 二进制,
# 按角色分派 tool-basic(三件套)/ tool-workflow(starlark)/ tool-mcp(MCP client)/
# tool-subagent —— 2026-10-02 由四个独立二进制合并而来(原始 30.8 MiB → 9.6 MiB)。
# P4 平台匹配:按发行矩阵(darwin/linux/windows × amd64/arm64,与
# .goreleaser.yaml 对齐)每目标构建一份,落 internal/embed/extplugins/<os>-<arch>/;
# 主包交叉编译时经 build-tag 只嵌本平台产物(体积门不变);
# 产物缺失时主包构建失败(goreleaser before hook 调用,防漏)。
# P0 体积门回归(M7):strip(-s -w)+ 压缩;embed 存压缩产物,宿主首启 EnsurePlugins 解压落盘。
# 压缩格式 2026-10-02 由 gzip -9 换 **zstd -19**(体积债路径 ③,size-check.sh 头注登记):
# 实测同一批产物 28.67 MiB 原始 → gzip -9 11.53 / zstd -19 9.75 / xz -9 8.60 MiB。
# 选 zstd 不选 xz 的理由不是压缩率,是**解码成本落在每次启动上**:xz 还能再省 ~1.15 MiB,
# 但 28 MiB 的单线程 xz 解码约 0.5–1s,而 zstd 约 0.05s —— 启动时间比 1 MiB 体积更值钱。
# 编码侧同样不引外部工具:`scripts/zstdpack`(仓内几十行的 Go 程序,纯 Go 编码器)——
# 五个平台的 CI runner 里 macOS/Windows 镜像都不保证装了 `zstd` 命令。
# Windows 产物带 .exe(os/exec 在 Windows 上按 PATHEXT 补扩展名,无扩展名的 PE 无法
# exec;见 internal/embed/embed.go ExtPluginBinary 注释)。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
EMBED_DIR=internal/embed/extplugins
TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"
# 2026-10-02 瘦身:四个独立二进制合成一个 tool-kit(按角色分派,见 extplugins/toolkit)。
# 合成的是二进制不是进程 —— 宿主仍逐角色起进程,配置文件里的插件 id 一个都没变。
NAMES="tool-kit"

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
        # PE machine 字段是**小端字节序**的十六进制文本:0x8664 → "6486"、0xAA64 → "64aa"。
        # 写十进制会永远匹配不上(初版就这么错了,表现为 assert_arch 报 windows/?
        # 而产物其实是对的 —— 平台护栏自己抓的)。
        case "$mhex" in
          6486)  actual="windows/amd64" ;;
          64aa)  actual="windows/arm64" ;;
          c401)  actual="windows/arm" ;;   # 0x01C4 = arm32,不在矩阵内(报出来,不混过去)
          *)     actual="windows/?" ;;
        esac
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

# 清旧产物(平铺 *.gz/*.zst 与旧平台目录),防跨平台残留与格式换代残留
rm -rf "$EMBED_DIR"/*
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  dir="$EMBED_DIR/$os-$arch"
  mkdir -p "$dir"
  built=()
  for name in $NAMES; do
    bin="$name"
    if [ "$os" = windows ]; then bin="$name.exe"; fi
    echo "build $t/$bin"
    # -buildvcs=false:默认会写入 vcs.revision/vcs.time/vcs.modified → 产物随 **git 状态**变化,
    # "重跑无 diff" 只在 HEAD+脏标记完全一致时成立(实测:同一源码、同一工具链,脏树重生成
    # 与提交版字节不同但大小相同,只是这几个字段)。剥掉 VCS 戳后产物只由源码决定。
    GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w" -o "$dir/$bin" "./extplugins/$name"
    assert_arch "$dir/$bin" "$os" "$arch"
    built+=("$dir/$bin")
  done
  # 用仓内的 Go 打包器而不是系统 zstd:五个平台的 CI runner 里 macOS/Windows
  # 镜像都不保证有 zstd(为一个压缩动作引入外部工具依赖不值)。
  # zstd 帧**不存 mtime/文件名**,同一份二进制 ⇒ 同一份压缩产物(重跑无 diff)。
  # 一次调用传全部四件:打包器据此写出**完整**的 SHA256SUMS(逐件调用会互相覆盖)。
  go run ./scripts/zstdpack "${built[@]}"
done
echo "--- 产物清单 ---"
ls -la "$EMBED_DIR"/*/