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
#
#   本地跑请按 CI 同源口径带上子进程覆盖变量(否则 cmd/gah 会假红,45.9% vs 棘轮 69):
#     mkdir -p /tmp/gah-cover-merge
#     GAH_COVER_MERGE_DIR=/tmp/gah-cover-merge bash scripts/coverage-check.sh
#   2026-10-05:未设该变量且 cmd/gah 跌破棘轮时,脚本会**在红字里明示这一点**
#   (此前只印覆盖率数字,很容易被当成代码退化去追 —— 实测 45.9% → 加变量即 70.7)。
#
# —— 结构性稀释口径(2026-10-02 补齐,原先只有「不降门」三个字)——
#   背景:棘轮是**逐包绝对百分比**,而一批新功能往往给某个包一次加几百行新代码
#   (例:第一百零九批给 host-internal-commands 加约 700 行命令面)。此时该包的
#   百分比会因**分母变大**而下滑 —— 与「旧代码的测试被删/被改坏」是两回事,
#   但棘轮看数字 indistinguishable,于是只剩两个坏选择:要么降门(纪律不允许),
#   要么在别处凑测试(第九十四/一百零九批实际做的:补既有低覆盖路径)。
#   两种做法都会掩盖真问题:凑出来的测试与新代码无关;「稀释不算回退」若只写
#   在心里,则等于门禁可以随时被解释掉。所以口径**写成可判定规则**:
#
#   规则(三条,顺序即处置顺序):
#     1. 棘轮数值**只许上调**。包覆盖率下滑一律先当回退处理(现行行为不变)。
#     2. 本批给某包新增了**超过 200 行语句**(用两份 profile 的语句数差算),
#        且下滑 ≥ GAH_DILUTION_PP(默认 3.0)pp ⇒ 判为「结构性稀释」,此时该包
#        **必须补测本批新增的那部分**,补测后覆盖率应回到棘轮之上;
#        若补不满,唯一合法出口是把它写进下面的 DILUTION 表并写明理由 ——
#        理由必须指向**为什么本批新增的行现在还测不到**(需子进程/需真机/需注入
#        失败分支),不接受「时间不够」「下批补」。
#     3. 判定入口 = `bash scripts/coverage-check.sh --dilution <基线> <当前>`,
#        它只对**棘轮包**报 Δ 并在越线时红;不改动普通模式的任何行为。
#   基线 profile 从哪来:开工那批之前跑一次普通模式,把两份 profile 存到别处
#   (`go test ./... -coverprofile=/tmp/base.out` + `cd sdk && … -coverprofile=…`)。
#
#   已知的一次真实触发:第一百零九批(约 700 行命令面)→ 补 13 条既有低覆盖路径,
#   包回到棘轮之上,本表留空(有据可查的处置不需要豁免条目)。
DILUTION_MAX_NEW_STMT="${GAH_DILUTION_MAX_STMT:-200}"   # 认定为「大批量新增」的语句数门
DILUTION_PP="${GAH_DILUTION_PP:-3.0}"                   # 稀释判定的下滑幅度门(pp)
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

FLOOR_MIN="${GAH_COVER_FLOOR_MIN:-65}"   # 全局总覆盖率下限(实测 69.7%)
SKIP="${GAH_COVER_SKIP:-0}"             # 1 = 只测(不校验),用于基线测量

# —— 棘轮表:关键包逐包下限(实测值 - 5~8pp)。提升覆盖后请把数字上调。——
# 更新规则:新增关键包 → 补一行;包改名/拆分 → 同步本表(未登记的棘轮包会被报缺失)。
# 第九十四批(2026-09-30)按实测上调三行(补测:命令补全/显示分支、偏好落盘失败路径、
# CLI 进程级行为):internal/prefs 66→82(实测 86.4)、plugins/host/host-roles 78→87(实测 90.4)、
# cmd/gah 50→56(实测 57.9;当时 main() 只能由**子进程**跑到,子进程不写 cover 计数器 ⇒ 上限在此)
#   → 2026-10-02 子进程覆盖接通后重测:darwin/arm64 **75.0**(含 main()),棘轮 56 → **68**。
#   取 68 而非 75:棘轮取**各平台实测的较小值**,本地只有 darwin 一档,linux/windows 由 CI 首跑实测;
#   CI 实测若低于 68,按跨平台差处理(平台专用 @GOOS 行),不要直接把这里往上抬。
#   2026-10-02(外部插件四合一:host-bridge 加了角色寻址层、tool-shell 加了 pty 编排层)后:
#   这两个包的**新增代码已被用例钉住**(多角色夹具 + pty 编排/真 pty 两组),但它们本来
#   就靠真进程/真 pty,基数小、新增多 ⇒ 百分比被结构性稀释。按纪律**先补测本批新增部分**
#   (补完仍低于旧棘轮),再按实测重定基:host-bridge 86→85、tool-shell 90→88。
#   这两行是**下调**,理由写在这里:补测已做完(多角色反向验证两处都真钉住),剩下的
#   未覆盖是「需要真进程/真 pty」的既有部分,不是本批的新缺口。
#   2026-10-02(MCP 传输面:http 传输 + 凭据打码)后再上调:plugins/mcp/mcp-bridge 72 → **75**
#   (实测 75.7)、internal/mcpconfig 80 → **88**(实测 88.8;新增的 ValidateEndpoint 与
#   headers 规范化/打码都有用例)。
#   2026-10-02(子进程覆盖接通 + zstd 换代)后再上调:internal/embed 75.2 → **78**
#   (实测 78.4;该包换 zstd 后新增了 SHA256SUMS 解析与流式落盘,补了坏清单/写失败两条用例)。
# 方向只允许往上:降门必须换成补测(见 AGENTS.md「覆盖率门」)。
#   2026-10-02(计划计划段 UI 可用化:解析层 cronexpr/crontxt/resolver + 一次性触发链路)后上调:
#   plugins/host/host-schedule 81 → **82**(实测 83.7;新增 L 字段月末语义、十档控件态映射、
#   中文排期解析、once 触发链路四组用例)、plugins/tool/tool-schedule 80 → **82**(实测 83.1;
#   补 once/once_date 的参数校验与改回循环的清理用例)、web 80.6 → **81**(实测 81.6;
#   补 resolve 端点的十档/中文/失败语义与一次性 REST 用例)。跨平台留约 1pp 余量给 linux CI。
#   2026-10-08(动画加强 + 性能优化)后上调:internal/prefs 83 → **84**(实测 84.3;新增的
#   mtime 缓存带来七条护栏用例:命中/同进程写失效/跨进程写可见/文件删除/坏 JSON/按路径分桶/
#   写路径不经缓存)、web 81 → **81.5**(实测 81.5;数据根可写性探针的 TTL 复用六条用例)。
#   2026-10-09(第一百三十六批 · 桌面端系统代理)新增 internal/sysproxy,登记 **65**
#   (darwin 实测 72.1;Apply 里的 scutil/平台分支不在单测里跑,余量留足)。

# 跨平台差(踩过的坑):棘轮取**各平台实测的较小值**。第一百零四批按 macOS 实测把
# host-schedule 调到 81.5,而 CI(linux)实测 81.2 ⇒ 门红。带平台差异的代码路径要用
# @GOOS 平台行单独登记,别拿单一平台的读数当通用值。
#
# 平台覆盖:<包>@<GOOS> 行 = 该平台专用下限(覆盖同包的基础行)。仅当某个包的覆盖率
# **因平台语义而不可比**时才用,且必须写清原因 —— 目前两处,都是同一条:
#   plugins/tool/tool-shell@linux 与 internal/kernelsandbox@linux:Landlock 自举 helper
#   (linux.go 的 landlockSelfAndExec/llAddPathRule)是**重新 exec 自身**去跑目标命令的。
#   被 exec 替换掉的进程**永远不会写出覆盖计数器**(Go 运行时在正常退出时写盘,而 exec
#   替换了进程镜像 ⇒ 退出处理根本不执行)。已用最小实验验证:插桩程序 exec 掉自己后,
#   那段代码在 profile 里计数恒为 0,而父进程写出的文件里**只有父进程那段**。
#   ⇒ 这一类**不是「测试没跑到」,也修不了**:GOCOVERDIR / covdata 合并对它无效(2026-10-02
#   实测确认),要真弥合得让 helper 不 exec 自己(改成 fork 出子进程、由父进程等待),
#   那是动**内核沙箱的施加路径**、只能在 Linux 上验 —— 已登记待议,不假装已解决。
#   darwin 无此问题(seatbelt 走 argv 包装,profile 在父进程构建)。
#
# cmd/gah 曾是第三处同类盲区(main() 只能由**子进程**跑到),2026-10-02 **已弥合**:
# 测试用 `go build -cover` 插桩 + `GOCOVERDIR` 让子进程自己落盘,再经 `scripts/covermerge`
# 按区块计数相加并进主 profile(实现与边界见 internal/testutil/coverchild.go)。
#   驱动方式:设 `GAH_COVER_MERGE_DIR=<目录>` 后跑测试,覆盖率门会自动并入该目录下的
#   `*.child.out`;不设 = 不启用(拿不到子进程那份 ≠ 那部分覆盖率为 0,两件事必须能分开)。
#
#   internal/kernelsandbox 同样只登记了一个保守下限(darwin 实测 95.1):Linux 的
#   Landlock 自举 helper 与上面的 tool-shell@linux 是**同一个盲区**(重新 exec 自身后
#   子进程不写 cover 计数器),且 darwin 专属用例(seatbelt profile 组装)在 Linux 上不跑
#   → 两平台不可比。下限取 50(远低于实测值),逾期若在 Linux CI 报低,按上面的
#   模式补了 `internal/kernelsandbox@linux 43`(Linux CI 实测 43.4%,2026-09-27)。
#   注意这个下限不能按 darwin 的数字(实测 95.1)要求 —— 两平台可跑到的行集不同。
read -r -d '' MINS <<'EOF'
core/event 90
core/ctx 60
core/plugin 72
core/config 65
sdk 85.6
internal/prefs 84
internal/xlock 70
internal/roles 86.4
internal/rolepack 85
internal/skills 81.7
internal/searchfile 66
internal/instructions 76
internal/providerfile 72
internal/kernelsandbox 50
internal/kernelsandbox@linux 43
internal/mcpconfig 88
internal/sysproxy 65
internal/embed 78
internal/install 77
internal/sessionevents 92
internal/sessionhtml 92
cmd/gah 69
plugins/catalogue 70
plugins/ui/ui-web-app 70
web 81.5
tui 74
plugins/policy/policy-guard 91
plugins/host/host-agent-loop 91.5
plugins/host/host-bridge 85
plugins/host/host-backup 80
plugins/host/host-commands 76
plugins/host/host-confirm-fusion 76
plugins/host/host-cwd-sessions 87.4
plugins/host/host-docview 72
plugins/host/host-docview/pdfium 20
plugins/host/host-fanout 80
plugins/host/host-internal-commands 93
plugins/host/host-jobs 88
plugins/host/host-notices 70
plugins/host/host-schedule 82
plugins/host/host-llm 68
plugins/host/host-plugin-manager 76
plugins/host/host-session-log 90.2
plugins/host/host-session-summary 74
plugins/host/host-skills 85
plugins/host/host-roles 87
plugins/host/host-system-prompt 88
plugins/host/host-tools 91
plugins/host/host-worktrees 81
plugins/host/host-usage-stats 60
plugins/host/token-compress 84
plugins/mcp/mcp-bridge 75
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
plugins/tool/tool-shell 88
plugins/tool/tool-schedule 82
plugins/tool/tool-session-search 88
plugins/tool/tool-shell@linux 58
plugins/tool/tool-subagent 56
plugins/tool/tool-todo 75
plugins/tool/tool-web 85
plugins/tool/tool-workflow 72
EOF

# —— 稀释豁免表:仅「结构性稀释且本批补不满」时用(包名|理由)。前缀不匹配,精确包名。——
# 理由必须指向**为什么本批新增的行现在还测不到**(需子进程 / 需真机 / 需注入失败
# 分支),不接受「时间不够」「下批补」。留空是正常状态 —— 有据可查的处置请直接补测。
read -r -d '' DILUTION <<'EOF'
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
printf '%s\n' "$DILUTION" >"$TMP/dilution.txt"

# —— 模式二:结构性稀释判定(只报棘轮包的 Δ;不跑测试,吃外部 profile)——
# 用法:bash scripts/coverage-check.sh --dilution <基线根> <基线sdk> <当前根> <当前sdk>
# 四个 profile 按固定顺序传入(awk 靠 FNR==1 切阶段,避免依赖 gawk 的 ARGIND ——
# macOS 自带的是 mawk)。
if [ "${1:-}" = "--dilution" ]; then
  shift
  if [ "$#" -ne 4 ]; then
    echo "用法:bash scripts/coverage-check.sh --dilution <基线根.out> <基线sdk.out> <当前根.out> <当前sdk.out>"
    exit 2
  fi
  for p in "$@"; do
    [ -s "$p" ] || { echo "FAIL 覆盖率 profile 缺失或为空:$p"; exit 1; }
  done
  awk -v maxnew="$DILUTION_MAX_NEW_STMT" -v maxpp="$DILUTION_PP" '
    FILENAME == ARGV[1] {
      if (NF >= 2 && index($1, "@") == 0) min[$1] = $2 + 0
      next
    }
    FILENAME == ARGV[2] {
      n = split($0, f, "|"); if (n >= 2 && f[1] != "") why[f[1]] = f[2]
      next
    }
    FNR == 1 { phase++ }
    !/^mode:/ {
      split($1, a, ":"); path = a[1]
      sub(/\/[^\/]*$/, "", path)
      sub(/^github\.com\/nekoleamo\/go-agent-harness\/?/, "", path)
      if (path == "") path = "."
      k = (phase <= 2) ? "b" : "c"
      if (phase == 2 || phase == 4) k = k "s"        # sdk module 单独计一份
      stmts[k SUBSEP path] += $2
      if ($3+0 > 0) cov[k SUBSEP path] += $2
    }
    # 基线/当前各自优先用本 module 的 profile,取不到才回退到另一个(不跨 module 混算)
    function pick(p) {
      if (stmts["b" SUBSEP p] + 0 > 0) return pct("b", p)
      if (stmts["bs" SUBSEP p] + 0 > 0) return pct("bs", p)
      return -1
    }
    function pickc(p) {
      if (stmts["c" SUBSEP p] + 0 > 0) return pct("c", p)
      if (stmts["cs" SUBSEP p] + 0 > 0) return pct("cs", p)
      return -1
    }
    function pct(k, p) { return 100 * cov[k SUBSEP p] / stmts[k SUBSEP p] }
    function stmtsOf(p) { return stmts["c" SUBSEP p] + stmts["cs" SUBSEP p] }
    function stmtsB(p) { return stmts["b" SUBSEP p] + stmts["bs" SUBSEP p] }
    END {
      printf "%-46s %8s %8s %8s %9s  %s\n", "包", "基线", "当前", "Δpp", "新增语句", "判定"
      bad = 0
      for (p in min) {
        b = pick(p); c = pickc(p)
        if (b < 0) continue
        if (c < 0) { printf "%-46s %7.1f%% %7s %8s %9s  %s\n", p, b, "-", "-", "-", "✗ 当前 profile 缺该包(改名/移除请同步棘轮表,否则等于把测试删了而不报错)"; bad = 1; continue }
        d = c - b
        new = stmtsOf(p) - stmtsB(p)
        if (d > -0.05) { continue }                    # 只报下滑的包(持平/上升不列)
        verdict = "—"
        if (d <= -maxpp && new > maxnew) {
          if (p in why) { verdict = "稀释·已登记豁免" }
          else { verdict = "✗ 结构性稀释未处置"; bad = 1 }
        } else if (d <= -maxpp) {
          if (p in min && c + 0.05 < min[p]) { verdict = "✗ 跌破棘轮(按回退处理)"; bad = 1 }
          else verdict = "小幅下滑(仍在棘轮上)"
        } else if (c + 0.05 < min[p]) {
          verdict = "✗ 跌破棘轮"; bad = 1
        } else verdict = "小幅下滑(仍在棘轮上)"
        printf "%-46s %7.1f%% %7.1f%% %+7.1f %9d  %s\n", p, b, c, d, new, verdict
      }
      if (bad) { print "\nDILUTION_FAIL(处置见脚本头「结构性稀释口径」三条规则)"; exit 1 }
      print "\nDILUTION_OK"
    }
  ' "$TMP/mins.txt" "$TMP/dilution.txt" "$1" "$2" "$3" "$4"
  exit $?
fi

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

# 子进程覆盖合并(2026-10-02):测试侧按 GAH_COVER_MERGE_DIR 落 *.child.out
# (见 internal/testutil/coverchild.go —— 子进程必须带 -cover 插桩才会写 GOCOVERDIR,
#  文件已按包筛过,不把别的包的测量混进来)。这里做「按区块计数相加」的并入:
# 没设该变量 = 不启用,普通模式与 --dilution 的行为与从前**逐字相同**。
CHILD_DIR="${GAH_COVER_MERGE_DIR:-}"
if [ -n "$CHILD_DIR" ] && [ -d "$CHILD_DIR" ]; then
  shopt -s nullglob
  child_files=("$CHILD_DIR"/*.child.out)
  shopt -u nullglob
  if [ "${#child_files[@]}" -gt 0 ]; then
    echo "== 合并子进程覆盖:${#child_files[@]} 份 =="
    for f in "${child_files[@]}"; do echo "   $(basename "$f")"; done
    # 逐份并入**拥有该包的那个 profile**(root 或 sdk)。两份都并会把同一个包计两次 ——
    # 分母翻倍、覆盖率被拉回中间值(实测 75.0% 被算成 59.0%)。
    # 归属判据用「该子进程 profile 里的文件是否已出现在某个 profile 中」,不引入模块路径知识。
    for f in "${child_files[@]}"; do
      # 取第一行数据块的文件部分(纯 bash 切:profile 行首字段就是 "文件:起.止",
      # 去掉最后一个冒号之后的内容即文件路径)。不用 sed/awk 的正则 —— BSD sed 与
      # awk 字面量里的 `/` 转义各有一版怪癖,为这一行不值得踩两种坑。
      line="$(grep -m1 -v '^mode:' "$f" || true)"
      tok="${line%% *}"
      pkg="${tok%:*}"
      [ -n "$pkg" ] && [ "$pkg" != "$tok" ] || { echo "FAIL 子进程 profile 形状不对:$f" >&2; exit 1; }
      target=""
      for p in "${PROFILES[@]}"; do
        if grep -q "^${pkg}:" "$p" 2>/dev/null; then target="$p"; break; fi
      done
      if [ -z "$target" ]; then
        # 既不在 root 也不在 sdk ⇒ 要么这个包的测试压根没跑(该 profile 里没有它),
        # 要么子进程插桩的是别的 module。显式报错,不静默丢数据。
        echo "FAIL 子进程覆盖 $f 的包(文件前缀 $pkg)不属于任何已有 profile —— 并进去会重复计数" >&2
        exit 1
      fi
      m="$TMP/merged-$(basename "$target")"
      if go run ./scripts/covermerge "$target" "$f" > "$m"; then
        cp "$m" "$target"
        echo "   $(basename "$f") → $(basename "$target")"
      else
        echo "FAIL 子进程覆盖合并失败(不静默跳过:那会让盲区重新变成假的 0%)"
        exit 1
      fi
    done
  else
    echo "== 子进程覆盖:无数据(测试未启用插桩,或本平台无子进程路径) =="
  fi
fi

GOOS_NOW="$(go env GOOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')"
# 子进程覆盖是否启用(未启用 ⇒ cmd/gah 的 main() 那段永远写不到计数器,必假红)
CHILD_ON=0; [ -n "${GAH_COVER_MERGE_DIR:-}" ] && CHILD_ON=1
echo "== 逐包覆盖率(棘轮;平台 $GOOS_NOW;子进程覆盖 $([ "$CHILD_ON" = 1 ] && echo 已启用 || echo 未启用)) =="
awk -v mins="$TMP/mins.txt" -v exempt="$TMP/exempt.txt" -v floor_min="$FLOOR_MIN" -v skip="$SKIP" -v goos="$GOOS_NOW" -v child="$CHILD_ON" '
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
      # cmd/gah 的 main() 只能由子进程跑到,子进程不写 cover 计数器 ⇒ 未启用插桩时
      # 它必然远低于棘轮。那是**测量缺失**,不是回退,但只印数字会被当成代码退化
      # 去追(实测裸跑 45.9% / 棘轮 69,加 GAH_COVER_MERGE_DIR 后 70.7)。
      # 这里只作**明示**:不跳过判定、不降门 —— 静默跳过正是本门要消灭的东西。
      if (p == "cmd/gah" && m != "" && pct + 0.05 < m && child == "0") {
        bads[++nb] = "      ↳ 本次未启用子进程覆盖(未设 GAH_COVER_MERGE_DIR),该数字必偏低、不是回退;按脚本头部用法带上该变量重跑才是真结论"
      }
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
