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
echo "kernel-sandbox-probe: OK(真实内核约束已验,无 SKIP)。"
