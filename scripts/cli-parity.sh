#!/bin/bash
# CLI 输出**逐字节比对**:把任意一个 commit/分支的 `gah` 与当前工作树的 `gah` 都构建出来,
# 跑同一批命令,逐字节比较 stdout / stderr / 退出码,把差异原样列出来。
#
# 为什么需要它(2026-10-11 立):M0 那次"入口行为保持重构"承诺的是"CLI 行为逐字不变",
# 而当时**只能靠读代码 + 人工跑几条命令**来相信它。真正让我发现东西的不是读 diff,而是
# 这个比对:它当场揪出「纯查询/管理类 flag 在管道里被 TUI 护栏拦死」这个一直存在的缺陷
# (一个"意外的零差异"case 逼我去查,因为管道里 `-dump-config` 两侧都没输出)。
# 手工跑一次就扔掉的脚本,下次改 CLI 还得重写一遍 —— 故收在这里。
#
# 用法:
#   bash scripts/cli-parity.sh <ref>            # 与 <ref> 比对;有差异 **exit 1**
#   bash scripts/cli-parity.sh <ref> --report   # 只报告不判失败(已知会有意的差异时用)
#   bash scripts/cli-parity.sh <ref> --case "-dump-config"   # 只跑一条(可重复)
#   bash scripts/cli-parity.sh <ref> --list     # 只列用例矩阵
#
# 两个刻意的设计:
#   1. **两侧都用同一个假版本号**(-X main.version=parity):版本号由构建注入,不归一化就
#      永远有一行差异,把真差异淹掉。
#   2. **日志时间戳归一化**(time=… → time=TS):否则每条带日志的用例都"有差异"。
# 判据因此只剩"输出文本与退出码本身"——这正是要守的东西。
#
# 前提:被测 ref 的构建需要前端产物与外部插件产物(`web/dist`、`internal/embed/extplugins`),
# 它们是生成物、不入库 ⇒ 本脚本把**当前工作树**的那两份拷进 ref 的 worktree。
# 口径提醒:这意味着"非 CLI 输入"是按当前树固定的;比 CLI 行为时这正是想要的(变量只剩 CLI)。
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

REF=""
MODE="gate"   # gate | report
CASES_FILTER=()
while [ $# -gt 0 ]; do
  case "$1" in
    --report) MODE="report"; shift ;;
    --list) MODE="list"; shift ;;
    --case) CASES_FILTER+=("$2"); shift 2 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) REF="$1"; shift ;;
  esac
done

# ── 用例矩阵:全部是**不需要网络、不需要终端**的纯 CLI 面 ──────────────
# 每条都跑两次(两份二进制),故矩阵越短越省时间;覆盖的是"分支与早退"最密的地方。
CASES=(
  "-version"
  "-ephemeral -dump-config"
  "-ephemeral -profile web -dump-config"
  "-ephemeral -profile headless -dump-config"
  "-ephemeral -profile acp -dump-config"
  "-ephemeral -list-plugins"
  "-ephemeral -list-ui-plugins"
  "-ephemeral -list-trusted-plugins"
  "-ephemeral -check-plugin-updates"
  "-ephemeral -uninstall nope"
  "-ephemeral -untrust-plugin nope"
  "-ephemeral -untrust-ui-plugin nope"
  "-ephemeral -trust-plugin nope"
  "-install-artifact http://x/y"
  "-install-artifact http://x/y -id foo -name bar"
  # 刻意**不含** `-install <repo>`:它会真的 git ls-remote(网络)⇒ 比对矩阵必须离线可跑,
  # 否则脚本在无网/慢网环境里失败或变慢,而它要守的是"输出文本"不是"网络行为"。
  "doc"
  "-badflag"
  "-profile tui"
  "web -version"
  "acp -version"
)

if [ "$MODE" = "list" ]; then
  printf '%s\n' "${CASES[@]}"
  exit 0
fi

if [ -z "$REF" ]; then
  echo "用法: bash scripts/cli-parity.sh <ref> [--report|--list|--case \"<args>\"]" >&2
  exit 2
fi
for dep in web/dist internal/embed/extplugins; do
  [ -d "$dep" ] || { echo "缺少生成物 $dep —— 先跑 bash scripts/gen-web.sh / scripts/gen-extplugins.sh" >&2; exit 2; }
done

WORK="$(mktemp -d /tmp/gah-parity-XXXXXX)"
cleanup() {
  git worktree remove --force "$WORK/src" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> 构建当前工作树 ==="
go build -trimpath -ldflags "-X main.version=parity" -o "$WORK/gah-head" ./cmd/gah || exit 2

echo "==> 检出并构建 $REF ==="
git worktree add -q --detach "$WORK/src" "$REF" || { echo "无法检出 $REF" >&2; exit 2; }
# 生成物按当前树固定(见头注:比 CLI 时变量只剩 CLI)
cp -R web/dist "$WORK/src/web/dist"
cp -R internal/embed/extplugins "$WORK/src/internal/embed/extplugins"
( cd "$WORK/src" && go build -trimpath -ldflags "-X main.version=parity" -o "$WORK/gah-ref" ./cmd/gah ) || exit 2

# run_case <bin> <dir> <args...>:在独立目录里跑一次(ephemeral 也在独立 GAH_HOME 下)
run_case() {
  local bin="$1" dir="$2"; shift 2
  mkdir -p "$dir/gah-data"
  ( cd "$dir" && cp "$bin" ./gah && printf '' | ./gah "$@" > out.txt 2> err.txt; echo $? > rc.txt )
}

# normalize <file>:抹掉"必然不同"的东西(临时路径、日志时间戳)
normalize() {
  sed -E -e 's#/tmp/gah-parity-[A-Za-z0-9]+/(in|ref|head)#/TMP#g' \
         -e 's#time=[0-9T:.+-]+#time=TS#g' \
         -e 's#/private/var/folders/[^ ]+#/TMPVAR#g' \
         -e 's#/var/folders/[^ ]+#/TMPVAR#g' "$1"
}

DIFFS=0
TOTAL=0
for spec in "${CASES[@]}"; do
  if [ ${#CASES_FILTER[@]} -gt 0 ]; then
    keep=0
    for f in "${CASES_FILTER[@]}"; do [ "$f" = "$spec" ] && keep=1; done
    [ $keep -eq 1 ] || continue
  fi
  TOTAL=$((TOTAL + 1))
  rm -rf "$WORK/in" "$WORK/ref"
  # shellcheck disable=SC2086
  run_case "$WORK/gah-head" "$WORK/in" $spec
  # shellcheck disable=SC2086
  run_case "$WORK/gah-ref" "$WORK/ref" $spec
  case_diffs=0
  for f in out err rc; do
    normalize "$WORK/in/$f.txt" > "$WORK/a.norm"
    normalize "$WORK/ref/$f.txt" > "$WORK/b.norm"
    if ! diff -q "$WORK/a.norm" "$WORK/b.norm" >/dev/null; then
      case_diffs=1
      DIFFS=$((DIFFS + 1))
      echo "--- 差异 [$spec] $f ---"
      diff -u "$WORK/b.norm" "$WORK/a.norm" | sed -n '3,14p'
    fi
  done
  if [ $case_diffs -eq 0 ]; then
    printf '  same  %s\n' "$spec"
  fi
done

echo
echo "==> 比对完毕:用例 $TOTAL 条,差异 $DIFFS 处(基准 = $REF,当前 = 工作树)"
if [ "$DIFFS" -eq 0 ]; then
  echo "PARITY_OK"
  exit 0
fi
if [ "$MODE" = "report" ]; then
  echo "PARITY_DIFFS(仅报告,不判失败)"
  exit 0
fi
echo "PARITY_DIFFS(逐条核对上面每一处:是有意变更就写进提交信息,不是就修)"
exit 1
