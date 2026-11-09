#!/usr/bin/env bash
# kernel-sandbox-probe.sh:Linux 内核沙箱(Landlock)能力探针 —— 只在 Linux CI 上有意义。
#
# 为什么需要它(2026-11-09「自动化优先」第③批):
#   `internal/kernelsandbox` 的 Linux 用例在内核不支持 Landlock 时会**跳过**,于是
#   「内核层真的拦住了看不见的写」这条最硬的判据长期只有 SKIP 记录 —— 看起来"跑过了",
#   其实一次没验。本脚本把这件事变成可判定的:
#     - 内核支持 ⇒ **必须真跑通过**(出现 SKIP 即失败,不允许悄悄退化);
#     - 内核不支持 ⇒ 明确打印原因与出口(不拦人,但留痕,免得下次又以为验过)。
#
# 用法:bash scripts/kernel-sandbox-probe.sh
# 退出码:0 = 通过或环境不具备(已留痕);1 = 本该能跑却 SKIP/失败。
set -uo pipefail
cd "$(dirname "$0")/.."

# 平台与 LSM 可经 env 覆盖:只是为了能在 macOS 上验证本脚本的两条分支
# (判定逻辑最容易写错,而 Linux CI 是唯一实跑场合 —— 不给它本地可测性就只能盲改)。
OS_NAME=${GAH_PROBE_OS:-$(uname -s)}
if [ "$OS_NAME" != "Linux" ]; then
  echo "kernel-sandbox-probe: 非 Linux($OS_NAME),本探针不适用;跳过。"
  exit 0
fi

KERNEL=$(uname -r)
LSM_FILE=/sys/kernel/security/lsm
LSMS=${GAH_PROBE_LSM:-}
if [ -z "$LSMS" ] && [ -r "$LSM_FILE" ]; then
  LSMS=$(cat "$LSM_FILE")
fi
echo "kernel: $KERNEL"
echo "lsm  : ${LSMS:-(读不到 $LSM_FILE)}"

# landlock 支持的两个前提:①内核 ≥5.13 ②LSM 列表里有 landlock。
# 只看第一个会误判(内核新但 LSM 未启用 ⇒ 系统调用恒 EOPNOTSUPP)。
if ! printf '%s' "$LSMS" | grep -q landlock; then
  echo "kernel-sandbox-probe: 本机 LSM 未启用 Landlock ⇒ 内核沙箱用例会 SKIP。"
  echo "  这不是 gah 的缺陷:Landlock 需内核启动参数启用(不能运行期打开)。"
  echo "  要用真内核验,需自托管 runner / VM(见 docs/VERIFY.md「自动化优先」节的转化路线)。"
  exit 0
fi

echo "kernel-sandbox-probe: 检测到 Landlock ⇒ 内核沙箱用例**必须真跑**,出现 SKIP 即失败。"
set -o pipefail
OUT=$(go test ./internal/kernelsandbox/ -run 'TestKernelSandbox|TestBorderHelper' -count=1 -v 2>&1)
RC=$?
echo "$OUT" | tail -30
if [ $RC -ne 0 ]; then
  echo "kernel-sandbox-probe: 内核沙箱用例失败(见上)。"
  exit 1
fi
# 关键断言:能力在场就不再接受 SKIP —— 否则"环境具备但用例自己跳过"会被当成通过。
if echo "$OUT" | grep -q -- "--- SKIP"; then
  echo "kernel-sandbox-probe: Landlock 在场却出现 SKIP ⇒ 该验的没验。"
  exit 1
fi
# **还要断言"至少真跑过一条"**:`-run` 一个匹配不到的模式会得到 `no tests to run` + PASS,
# 退出码 0 —— 那是一道看着绿、实际什么都没跑的门(本地在 macOS 上模拟 Landlock 时当场踩到)。
PASSED=$(echo "$OUT" | grep -c -- "--- PASS:")
if [ "${PASSED:-0}" -eq 0 ]; then
  echo "kernel-sandbox-probe: 一条用例都没真跑(no tests to run)⇒ 不能算通过。"
  exit 1
fi
echo "kernel-sandbox-probe: 真跑通过 $PASSED 条用例。"

# 端到端那一层:真实进程**真的**被内核约束(模型看不见的写被拒 / 有效档跟到内核)。
# 包内单测只验"包装参数对不对",而这两条是"包装之后真的挡住了"—— 真机清单 L1 的核心判据。
echo "kernel-sandbox-probe: 端到端用例(真实进程被内核约束)也必须在跑。"
E2E=$(go test ./tests/ -run 'TestKernelSandbox' -v -count=1 -timeout=10m 2>&1)
E2E_RC=$?
echo "$E2E" | grep -E '^(=== RUN|--- (PASS|SKIP|FAIL)|PASS|FAIL|ok)' | tail -20
if [ $E2E_RC -ne 0 ]; then
  echo "kernel-sandbox-probe: 端到端内核用例失败。"
  exit 1
fi
if echo "$E2E" | grep -q -- "--- SKIP"; then
  echo "kernel-sandbox-probe: Landlock 在场却出现 SKIP ⇒ 端到端那层没验(容器 seccomp 挡了 landlock 系统调用?)"
  exit 1
fi
E2E_PASSED=$(echo "$E2E" | grep -c -- "--- PASS:")
if [ "${E2E_PASSED:-0}" -lt 2 ]; then
  echo "kernel-sandbox-probe: 端到端内核用例应至少 2 条真跑通过,实际 ${E2E_PASSED:-0} 条。"
  exit 1
fi
echo "kernel-sandbox-probe: 端到端真跑通过 $E2E_PASSED 条(看不见的写被拒 / 有效档跟到内核)。"

# L2:子进程覆盖记账。helper 会 **exec 掉自己**去施加限制,进程镜像被替换 ⇒ 退出处理不执行;
# 唯一的硬证据是它在 exec 之前把计数器写进 GOCOVERDIR,再由覆盖率门合并进主 profile。
# 这条以前被记为「只有 Linux 真机 + BuildCovered 二进制能验」,实测 CI 早就在记 ——
# 现在把它从"日志里碰巧看到"变成断言:带了 GAH_COVER_MERGE_DIR 就必须真的有 child.out。
if [ -n "${GAH_COVER_MERGE_DIR:-}" ]; then
  if ls "$GAH_COVER_MERGE_DIR"/kernelsandbox-*.child.out >/dev/null 2>&1; then
    N=$(ls "$GAH_COVER_MERGE_DIR"/kernelsandbox-*.child.out | wc -l | tr -d ' ')
    echo "kernel-sandbox-probe: 子进程覆盖记账在场($N 个 kernelsandbox child.out)。"
  else
    echo "kernel-sandbox-probe: 未发现 kernelsandbox child.out ⇒ L2(exec 前的覆盖记账)没生效,"
    echo "  或覆盖率门不在本 job 里跑(需要 GAH_COVER_MERGE_DIR 指向它产出的目录)。"
    exit 1
  fi
else
  echo "kernel-sandbox-probe: 未设 GAH_COVER_MERGE_DIR,跳过 L2 记账断言。"
fi
echo "kernel-sandbox-probe: OK(真实内核约束已验,无 SKIP)。"
