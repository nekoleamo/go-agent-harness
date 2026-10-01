#!/bin/bash
# 本地提交前门禁(gofmt / go vet / staticcheck)。
#
# 存在理由(2026-10-02,第二项待办「门禁固化」):
#   这三步原先只以「习惯」的形式活在开发者脑子里 —— 批次记录里反复写「护栏:
#   gofmt/vet/staticcheck 干净」,但**没有任何脚本**把它们固化,CI 也只跑 vet 与
#   staticcheck(**gofmt 一步都没有**)。代价是实测过的:SA4005(staticcheck 抓到
#   `_ = x` 的无效赋值)只在 CI 被逮到,那一轮的本地检查等于没做。习惯不是门禁。
#
# 单一事实源:本脚本。CI 的「本地门禁同源」步骤直接调它,避免两边各写一份命令
# 之后漂移(与覆盖率门/体积门同一条纪律)。
#
# 用法:
#   bash scripts/gate.sh                # 全量三步(提交前跑这个)
#   bash scripts/gate.sh --fast         # 只跑 gofmt + go vet(staticcheck 最慢,迭代中用)
#   GAH_GATE_SKIP_STATICCHECK=1 bash scripts/gate.sh   # 明确跳过(会打印一行明示,不静默)
#
# 环境变量:
#   GAH_STATICCHECK_BIN   staticcheck 可执行文件路径(默认 PATH 或 $(go env GOPATH)/bin)
#   GAH_GATE_SKIP_STATICCHECK=1  跳过 staticcheck(**明示**跳过,不假装跑过)
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# staticcheck 版本与 CI 固定一致(2026.2.1);本地版本不同会得到不同的检查结论。
STATICCHECK_VERSION="2026.2.1"
FAST=0
for arg in "$@"; do
  case "$arg" in
    --fast) FAST=1 ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "未知参数:$arg(见 --help)"; exit 2 ;;
  esac
done

FAILED=0
step() { printf '\n== %s ==\n' "$1"; }
fail() { echo "FAIL: $1"; FAILED=1; }

resolve_staticcheck() {
  if [ -n "${GAH_STATICCHECK_BIN:-}" ] && [ -x "$GAH_STATICCHECK_BIN" ]; then
    echo "$GAH_STATICCHECK_BIN"; return 0
  fi
  local cand
  cand="$(command -v staticcheck 2>/dev/null)" && [ -n "$cand" ] && { echo "$cand"; return 0; }
  cand="$(go env GOPATH)/bin/staticcheck"
  [ -x "$cand" ] && { echo "$cand"; return 0; }
  return 1
}

# —— 1) gofmt:只查 **git 跟踪的** .go 文件 ——
# 不查未跟踪/忽略文件(构建产物、生成代码、本地草稿都不该被门禁拦下),
# 也因此不需要 `gofmt -w` 全树改写(那会把无关文件卷进 diff)。
step "gofmt(仅 git 跟踪的 .go 文件)"
UNFORMATTED="$(git ls-files '*.go' | xargs gofmt -l 2>/dev/null)"
if [ -n "$UNFORMATTED" ]; then
  echo "$UNFORMATTED" | sed 's/^/  ✗ /'
  fail "以上文件未 gofmt(逐个 gofmt -w,或直接: git ls-files '*.go' | xargs gofmt -w)"
else
  echo "OK"
fi

# —— 2) go vet:sdk 是独立 module(go.work 下根目录 ./... 不含它),两处都跑 ——
step "go vet(根模块)"
if go vet ./...; then echo "OK"; else fail "go vet 根模块"; fi
step "go vet(sdk module)"
if (cd sdk && go vet ./...); then echo "OK"; else fail "go vet sdk module"; fi

# —— 3) staticcheck(双模块;缺失即失败,不静默跳过)——
if [ "$FAST" = "1" ]; then
  step "staticcheck(--fast:已跳过,不是通过)"
  echo "SKIP(--fast)"
elif [ "${GAH_GATE_SKIP_STATICCHECK:-0}" = "1" ]; then
  step "staticcheck"
  echo "SKIP(GAH_GATE_SKIP_STATICCHECK=1 显式跳过 —— 本次不构成通过)"
else
  SC="$(resolve_staticcheck)" || {
    echo "  ✗ 找不到 staticcheck。装与 CI 同版本:"
    echo "      go install honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}"
    fail "staticcheck 缺失(不静默跳过)"
    SC=""
  }
  if [ -n "$SC" ]; then
    step "staticcheck(根模块 · $($SC -version 2>/dev/null))"
    if "$SC" ./...; then echo "OK"; else fail "staticcheck 根模块"; fi
    step "staticcheck(sdk module)"
    if (cd sdk && "$SC" ./...); then echo "OK"; else fail "staticcheck sdk module"; fi
    # 版本漂移会让人误判「本地过了就是过了」——CI 固定 2026.2.1。
    if ! "$SC" -version 2>/dev/null | grep -q "$STATICCHECK_VERSION"; then
      echo "  ! 本地 staticcheck 版本与 CI 固定版本($STATICCHECK_VERSION)不同,结论可能不一致"
    fi
  fi
fi

echo
if [ "$FAILED" = "0" ]; then
  if [ "$FAST" = "1" ]; then echo "GATE_OK(gofmt + vet;staticcheck 未跑,不算通过)"; else echo "GATE_OK(gofmt + vet + staticcheck)"; fi
  exit 0
fi
echo "GATE_FAIL(先修上面各项;不要靠「CI 会报」——门禁的价值就在于提交前就红)"
exit 1
