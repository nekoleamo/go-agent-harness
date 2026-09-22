#!/bin/bash
# 覆盖率门(单一事实源:阈值只在本脚本;CI 与本地共用)。E-C 治理件。
#
# 背景(2026-09-12 建立):
#   项目已有一批高价值单测与端到端不变量测试,但**没有覆盖率门**:新增零测试包、
#   重构把已覆盖分支改成死代码、或某包覆盖率大幅回退都不会被发现(staticcheck 只查
#   静态问题,`-race` 只看跑到的用例是否绿)。本脚本把「当前水位」固化成棘轮:
#     - 棘轮表(ratchet):关键包逐包下限 = 实测值留 5~8pp 余量;回退即失败;
#       新增包未登记 → 失败(逼你显式登记,不静默);
#     - 全局下限:FLOOR_MIN,兜住未被棘轮覆盖的包;
#     - 豁免表(exempt):声明/装配/外部二进制/测试辅助等无语句覆盖意义的包,
#       必须写理由,且**不允许**非豁免包覆盖率为 0(防新增无测试包)。
#
#   sdk 是独立 Go module(go.work),根模块 `go test ./...` **不含**它;
#   本脚本同时测两个 module(CI 的 go test/vet 步骤同步补齐 —— 见 .github/workflows/ci.yml)。
#
# 用法:
#   bash scripts/coverage-check.sh                 # 自行跑两模块覆盖率(本地,较慢)
#   bash scripts/coverage-check.sh a.out b.out     # 复用已有 profile(CI 用)
#   GAH_COVER_FLOOR_MIN=60 GAH_COVER_SKIP=1 bash scripts/coverage-check.sh
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

FLOOR_MIN="${GAH_COVER_FLOOR_MIN:-65}"   # 全局总覆盖率下限(实测 69.7%)
SKIP="${GAH_COVER_SKIP:-0}"             # 1 = 只测(不校验),用于基线测量

# —— 棘轮表:关键包逐包下限(实测值 - 5~8pp)。提升覆盖后请把数字上调。——
# 更新规则:新增关键包 → 补一行;包改名/拆分 → 同步本表(未登记的棘轮包会被报缺失)。
#
# 平台覆盖:<包>@<GOOS> 行 = 该平台专用下限(覆盖同包的基础行)。仅当某个包的覆盖率
# **因平台语义而不可比**时才用,且必须写清原因 —— 目前只有一处:
#   plugins/tool/tool-shell@linux:Landlock 自举 helper(kernel_linux.go 的
#   landlockSelfAndExec/llAddPathRule)是**重新 exec 自身**后在子进程里跑完的,
#   子进程不写 cover 计数器 → Linux 上这部分永远显示 0.0%(代码其实被真跑到了,
#   Linux CI 的端到端与 tool-shell 用例都验证了「越界写被拦」)。darwin 无此问题
#   (seatbelt 走 argv 包装,profile 在父进程构建),故 darwin 沿用 90。
#   若要弥合两个数字,需给测试开 GOCOVERDIR + `go tool covdata textfmt` 合并,
#   属另一个量级的改动,暂不做(登记在此,不假装平台无关)。
read -r -d '' MINS <<'EOF'
core/event 90
core/ctx 60
core/plugin 65
core/config 65
sdk 60
internal/prefs 66
internal/providerfile 72
internal/mcpconfig 80
internal/embed 70
internal/install 60
internal/sessionevents 92
internal/sessionhtml 92
cmd/gah 50
plugins/catalogue 70
plugins/ui/ui-web-app 70
web 68
tui 71
plugins/policy/policy-guard 89
plugins/host/host-agent-loop 72
plugins/host/host-bridge 86
plugins/host/host-backup 80
plugins/host/host-commands 76
plugins/host/host-confirm-fusion 76
plugins/host/host-cwd-sessions 76
plugins/host/host-docview 72
plugins/host/host-docview/pdfium 20
plugins/host/host-fanout 77
plugins/host/host-internal-commands 93
plugins/host/host-jobs 88
plugins/host/host-notices 70
plugins/host/host-schedule 76
plugins/host/host-llm 60
plugins/host/host-plugin-manager 76
plugins/host/host-session-log 76
plugins/host/host-session-summary 74
plugins/host/host-skills 76
plugins/host/host-system-prompt 88
plugins/host/host-tools 88
plugins/host/host-worktrees 74
plugins/host/host-usage-stats 60
plugins/host/token-compress 84
plugins/mcp/mcp-bridge 72
plugins/mcp/mcp-server 85
plugins/mcp/acp-server 86
plugins/adapter/llm-anthropic-compat 58
plugins/adapter/llm-mock 55
plugins/adapter/llm-openai-compat 66
plugins/tool/tool-ask 80
plugins/tool/tool-auto-plan 58
plugins/tool/tool-doc 72
plugins/tool/tool-files 70
plugins/tool/tool-memory 73
plugins/tool/tool-shell 90
plugins/tool/tool-schedule 80
plugins/tool/tool-session-search 88
plugins/tool/tool-shell@linux 58
plugins/tool/tool-subagent 56
plugins/tool/tool-todo 75
plugins/tool/tool-web 76
plugins/tool/tool-workflow 72
EOF

# —— 豁免表:无「语句覆盖」意义的包(必须写理由)。前缀匹配。——
read -r -d '' EXEMPT <<'EOF'
bundles|bundle 装配声明层(仅注册表,catalogue 为单一事实源)
extplugins|独立外部插件二进制(由 tests/ 端到端覆盖,不在宿主进程内执行)
scripts|构建/治理脚本
tests|测试支撑(mcpserver 夹具等)
plugins/ui/ui-tui-app|TUI 启动胶水(需真实 TTY)
EOF

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
printf '%s\n' "$MINS" >"$TMP/mins.txt"
printf '%s\n' "$EXEMPT" >"$TMP/exempt.txt"

if [ "$#" -gt 0 ]; then
  PROFILES=("$@")
else
  PROFILES=("$TMP/root.out" "$TMP/sdk.out")
  echo "== 采集覆盖率:根模块 =="
  if ! CGO_ENABLED=0 go test ./... -coverprofile="${PROFILES[0]}" -covermode=atomic -count=1 >"$TMP/root.log" 2>&1; then
    echo "FAIL 根模块测试未通过(先修测试再看覆盖率):"
    grep -E "^(FAIL|---|ok.*FAIL)" "$TMP/root.log" | head -20
    exit 1
  fi
  echo "== 采集覆盖率:sdk 模块(独立 module)=="
  if ! (cd "$ROOT/sdk" && CGO_ENABLED=0 go test ./... -coverprofile="${PROFILES[1]}" -covermode=atomic -count=1) >"$TMP/sdk.log" 2>&1; then
    echo "FAIL sdk 模块测试未通过:"
    grep -E "^(FAIL|---)" "$TMP/sdk.log" | head -20
    exit 1
  fi
fi

for p in "${PROFILES[@]}"; do
  [ -s "$p" ] || { echo "FAIL 覆盖率 profile 缺失或为空:$p"; exit 1; }
done

GOOS_NOW="$(go env GOOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')"
echo "== 逐包覆盖率(棘轮;平台 $GOOS_NOW) =="
awk -v mins="$TMP/mins.txt" -v exempt="$TMP/exempt.txt" -v floor_min="$FLOOR_MIN" -v skip="$SKIP" -v goos="$GOOS_NOW" '
  FILENAME==mins   {
    if (NF>=2) {
      k=$1; v=$2+0
      if (index(k, "@") > 0) { split(k, ka, "@"); osmin[ka[1] SUBSEP ka[2]]=v }  # 平台专用下限
      else min[k]=v
    }
    next
  }
  FILENAME==exempt { n=split($0, f, "|"); if (n>=2) { why[f[1]]=f[2]; pfx[++np]=f[1] } ; next }
  !/^mode:/ {
    split($1, a, ":"); path = a[1]
    sub(/\/[^\/]*$/, "", path)                          # 去掉文件名
    sub(/^github\.com\/nekoleamo\/go-agent-harness\/?/, "", path)
    if (path == "") path = "."
    stmts[path] += $2
    if ($3+0 > 0) cov[path] += $2
    alls += $2; if ($3+0 > 0) allc += $2
  }
  function isExempt(p,   i) { for (i=1;i<=np;i++) if (index(p, pfx[i]) == 1) return 1; return 0 }
  # 生效下限:平台专用优先;返回 "" 表示该包无棘轮(仅全局下限/豁免)
  function effMin(p) {
    if ((p SUBSEP goos) in osmin) return osmin[p SUBSEP goos]
    if (p in min) return min[p]
    return ""
  }
  function minTag(p) {
    if ((p SUBSEP goos) in osmin) return "棘轮 " osmin[p SUBSEP goos] "(" goos ")"
    return "棘轮 " min[p]
  }
  END {
    bad = 0
    # 逐包判定 + 插入排序(按覆盖率升序,便于一眼看到最薄弱的包)
    n = 0
    for (p in stmts) {
      pct = 100 * cov[p] / stmts[p]
      m = effMin(p)
      tag = "仅全局下限"
      if (m != "") tag = minTag(p)
      else if (isExempt(p)) tag = "豁免"
      ord[++n] = pct "\t" tag "\t" p
      if (m != "" && pct + 0.05 < m) { bads[++nb] = "  ✗ " p " 覆盖率 " int(pct*10)/10 "% < 下限 " m "%(" goos ")"; bad = 1 }
      if (!(p in min) && !isExempt(p) && pct <= 0.0) { bads[++nb] = "  ✗ " p " 覆盖率为 0(非豁免包必须带测试)"; bad = 1 }
    }
    for (i = 2; i <= n; i++) { key = ord[i]; j = i - 1
      while (j >= 1 && (ord[j] + 0) > (key + 0)) { ord[j+1] = ord[j]; j-- }
      ord[j+1] = key }
    for (i = 1; i <= n; i++) { split(ord[i], f, "\t"); printf "%7.1f  %-44s %s\n", f[1], f[3], f[2] }
    for (p in min) if (!(p in stmts)) { bads[++nb] = "  ✗ 棘轮包 " p " 未出现在覆盖率 profile(改名/移除请同步脚本棘轮表)"; bad = 1 }
    # 平台专用下限同样要校验:写了 @GOOS 行却当平台跑不到该包 = 表里留着无效覆盖
    for (k in osmin) { split(k, kk, SUBSEP); if (kk[2] == goos && !(kk[1] in stmts)) { bads[++nb] = "  ✗ 棘轮包 " kk[1] "@" kk[2] " 未出现在覆盖率 profile"; bad = 1 } }
    total = 100 * allc / alls
    printf "\n总覆盖率 %.1f%%(全局下限 %s%%)\n", total, floor_min
    if (!skip && total + 0.05 < floor_min) { bads[++nb] = "  ✗ 总覆盖率 " int(total*10)/10 "% < 下限 " floor_min "%"; bad = 1 }
    if (!skip) { print "\n豁免(无语句覆盖意义,需理由):"; for (i=1;i<=np;i++) printf "  %-26s %s\n", pfx[i], why[pfx[i]] }
    for (i = 1; i <= nb; i++) print bads[i]
    if (skip) exit 0
    if (bad) { print "\nCOVERAGE_FAIL"; exit 1 }
    print "\nCOVERAGE_OK"
  }
' "$TMP/mins.txt" "$TMP/exempt.txt" "${PROFILES[@]}"
