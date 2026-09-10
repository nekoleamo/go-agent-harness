#!/bin/bash
# 体积门(单一事实源:阈值只在本脚本;CI 与本地共用)。E-C 治理件。
#
# 背景(2026-10-11 复测,见 DESIGN §14.1「G 组剩余项评测分析」E-C):
#   旧门「单二进制 <40MB」已被突破(darwin/amd64 43.47 MiB、windows/amd64 43.63 MiB、
#   linux/amd64 42.38 MiB、darwin/arm64 40.59 MiB;仅 linux/arm64 38.88 MiB 达标)。
#   实测构成 = 基线二进制(net/http+gopdf+goldmark+TUI+web embed,19.3–22.3 MiB)
#            + 本平台 extplugins gz embed(19.6–21.6 MiB,4 个外部工具插件,
#              每个 ~5 MiB gz:走 hashicorp/go-plugin → gRPC/protobuf 栈)。
#   决策:按实测重定基(二进制 ≤46 MiB / 下载产物 ≤30 MiB),并把**增长归因**打在输出里;
#   若后续 D6-1/D6-3 需要空间,先执行降体路径(优先:外部插件协议去 gRPC 化 ≈-14 MiB/插件
#   或 extplugins 附包化 ≈-20 MiB),而不是再抬门。
#
# 用法:
#   bash scripts/size-check.sh              # 本平台(darwin/arm64 等)
#   bash scripts/size-check.sh --all        # 发行矩阵五目标(交叉编译,离线可跑)
#   GAH_SIZE_MAX_BIN_MIB=46 GAH_SIZE_MAX_GZ_MIB=30 bash scripts/size-check.sh --all
set -euo pipefail

MAX_BIN_MIB="${GAH_SIZE_MAX_BIN_MIB:-46}"
MAX_GZ_MIB="${GAH_SIZE_MAX_GZ_MIB:-30}"
ALL=0
[ "${1:-}" = "--all" ] && ALL=1

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

TARGETS="$(go env GOOS)/$(go env GOARCH)"
[ "$ALL" -eq 1 ] && TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64"

MINOR="$(git describe --tags 2>/dev/null || echo dev)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

filesize() { wc -c <"$1" | tr -d ' '; }
mib() { awk -v b="$1" 'BEGIN{printf "%.2f", b/1048576}'; }

echo "体积门:二进制 ≤ ${MAX_BIN_MIB} MiB / 下载产物(gz) ≤ ${MAX_GZ_MIB} MiB;版本=$MINOR"
printf "%-16s %10s %10s %10s %10s %8s\n" "目标" "二进制MiB" "embedMiB" "基线MiB" "gzMiB" "判定"
fail=0
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  bin="$TMP/gah-${os}-${arch}"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$MINOR" -o "$bin" ./cmd/gah
  bsize=$(filesize "$bin")
  embed=0
  if [ -d "internal/embed/extplugins/${os}-${arch}" ]; then
    embed=$(du -sk "internal/embed/extplugins/${os}-${arch}" | awk '{print $1*1024}')
  fi
  gzip -9 -c "$bin" > "$bin.gz"
  gz=$(filesize "$bin.gz")
  bmib=$(mib "$bsize"); gzmib=$(mib "$gz"); emib=$(mib "$embed")
  basemib=$(awk -v a="$bsize" -v b="$embed" 'BEGIN{printf "%.2f",(a-b)/1048576}')
  verdict="OK"
  awk -v a="$bmib" -v b="$MAX_BIN_MIB" 'BEGIN{exit !(a>b)}' && { verdict="FAIL(bin)"; fail=1; }
  awk -v a="$gzmib" -v b="$MAX_GZ_MIB" 'BEGIN{exit !(a>b)}' && { verdict="FAIL(gz)"; fail=1; }
  printf "%-16s %10s %10s %10s %10s %8s\n" "$t" "$bmib" "$emib" "$basemib" "$gzmib" "$verdict"
done

echo
echo "--- 体积归因(本平台 embed 明细 TOP;增长先看这里) ---"
host_os="$(go env GOOS)"; host_arch="$(go env GOARCH)"
if [ -d "internal/embed/extplugins/${host_os}-${host_arch}" ]; then
  du -h "internal/embed/extplugins/${host_os}-${host_arch}"/* | sort -h | tail -6
else
  echo "(本平台无 embed 目录)"
fi
echo "提示:归因项固定为「基线依赖」与「extplugins 外部插件(每件 ~5 MiB gz,源自 go-plugin/gRPC 栈)」;"
echo "      降体路径见本脚本头部注释(协议去 gRPC 化 / 附包化),不在 CI 自动执行。"

if [ "$fail" -ne 0 ]; then
  echo "体积门失败:超出阈值(调阈值须同步 DESIGN §14.1 交付门行并说明归因)" >&2
  exit 1
fi
echo "体积门通过。"
