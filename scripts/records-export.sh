#!/usr/bin/env bash
# 决策记录导出(2026-10-02,补「DESIGN.md 只在本机」的迁出缺口)。
#
# 背景与要解决的问题:
#   2026-10-01 拍板 DESIGN.md **不入库**(它是本项目最大的单文件、只对本地开发有意义)。
#   但它是**唯一的决策登记册**:一百多批的交付记录、未实施清单、踩过的坑全在里面。
#   不入库 = 这些内容**只存在于这台机器的一个文件里**。机器坏了 / 换电脑 / 清盘
#   ⇒ 决策历史没了,而「为什么当初这么定」会重新被推导一遍(这正是有登记册的意义)。
#   `.gitignore` 里留了「想恢复跟踪:git add -f」,但那等于推翻当天的决定(它太大、
#   对外分发不需要),不是本脚本要做的。
#
# 本脚本做什么:把**不入库的决策资料**快照到仓库外的一个目录,并生成一份索引,
#   让新机器(或失而复得的旧机器)能在一分钟内知道「有哪些记录、在哪、现在什么状态」。
#
# 导什么(都是不入库、但丢了就没了的):
#   DESIGN.md            决策登记册(交付表 + 未实施清单 + 踩坑记录)
#   AGENTS.md            开发规范(在库,但一并导出便于整体恢复)
#   docs/*.md            内部文档(整个 docs/ 已在 .gitignore 里,只有 PLUGIN_DEV.md 入库)
#   ~/Documents/Plan/gah-*.md   仓库外调研与方案文档(更早的决策依据,同样只在本地)
#
# 不做什么(边界,别误以为它能做):
#   **不加密、不上传、不推到任何远端** —— 那需要凭据与隐私判断,不在本脚本范围。
#   它只保证「数据在**两处**且有一份可读索引」;真正的异地备份由用户决定(网盘/移动盘/私有仓库)。
#
# 用法:
#   bash scripts/records-export.sh              # 导出到 $GAH_RECORDS_DIR(缺省 ~/Documents/gah-records)
#   bash scripts/records-export.sh --dir <路径> # 指定目录
#   bash scripts/records-export.sh --verify     # 只跑自检(不写盘):核对导出清单与索引非空
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DEST="${GAH_RECORDS_DIR:-$HOME/Documents/gah-records}"
VERIFY=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dir) DEST="$2"; shift 2 ;;
    --verify) VERIFY=1; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "未知参数:$1"; exit 2 ;;
  esac
done

# —— 导出清单(单一事实源就在这里;加东西改这一处)——
# 相对路径 : 来源在仓库内;~ : 来源在仓库外的本地文档目录。
SOURCES_REPO=(
  "DESIGN.md"
  "AGENTS.md"
  "README.md"
  "README_EN.md"
  "docs"
)

# 生成索引:让新机器不用打开几千行就能定位。
# 取两段:① 顶部状态块(到第一个 `## 0.` 之前 = 最近发生过什么);
#        ② §14.1 的全部标题(交付/未实施清单在哪一节)。
write_index() {
  local out="$1" src="$2" srcdir="$3"
  {
    printf '# gah 决策记录索引\n\n'
    printf -- '- 导出时间:%s\n' "$(date '+%Y-%m-%d %H:%M:%S %z')"
    printf -- '- 源仓库:%s\n' "$ROOT"
    printf -- '- 本文件由 `scripts/records-export.sh` 生成,勿手改(重跑会覆盖)\n\n'
    printf '## 顶部状态块(最近这些批次做了什么)\n\n```\n'
    awk '/^## 0\. /{exit} {print}' "$src"
    printf '```\n\n'
    printf '## §14.1 章节目录(交付表 / 未实施清单按批记录)\n\n'
    grep -nE '^### ' "$src" | sed 's/^\([0-9]*\):/行 \1 · /' | head -200
    printf '\n## 本次导出的文件\n\n'
    (cd "$srcdir" && find . -type f | sort | sed 's|^\./|- |')
  } > "$out"
}

copy_all() {
  local outdir="$1"
  mkdir -p "$outdir"
  for rel in "${SOURCES_REPO[@]}"; do
    [ -e "$rel" ] || { echo "跳过(不存在):$rel" >&2; continue; }
    mkdir -p "$outdir/$(dirname "$rel")"
    cp -R "$rel" "$outdir/$rel"
  done
  # 仓库外的调研/方案文档(只在 ~/Documents/Plan;目录不在就不导,不为它报错)
  local plan="$HOME/Documents/Plan"
  if [ -d "$plan" ]; then
    mkdir -p "$outdir/plan"
    local n=0
    for f in "$plan"/gah-*.md; do
      [ -e "$f" ] || continue
      cp "$f" "$outdir/plan/$(basename "$f")"
      n=$((n + 1))
    done
    echo "仓库外方案文档:$n 份"
  else
    echo "跳过(不存在):~/Documents/Plan(gah-*.md)"
  fi
}

# —— 自检:导出物必须齐,且索引必须真的有内容 ——
# 只用「文件存在 + 索引非空」这种能机械判的判据;「记录写得全不全」不是脚本能判的。
verify() {
  local d="$1" bad=0
  for f in DESIGN.md AGENTS.md INDEX.md; do
    if [ ! -s "$d/$f" ]; then echo "缺或为空:$f"; bad=1; fi
  done
  if [ ! -s "$d/INDEX.md" ]; then
    echo "INDEX.md 为空"
    bad=1
  elif ! grep -q "§14.1" "$d/INDEX.md"; then
    echo "INDEX.md 里没有 §14.1 目录(抽取逻辑坏了)"
    bad=1
  fi
  local n
  n="$(find "$d" -type f | wc -l | tr -d ' ')"
  echo "导出文件数:$n"
  if [ "$n" -lt 5 ]; then echo "文件数异常少(预期至少 5)"; bad=1; fi
  [ "$bad" = 0 ] && echo "RECORDS_OK" || { echo "RECORDS_FAIL"; return 1; }
}

if [ "$VERIFY" = 1 ]; then
  # 自检跑在临时目录:不碰用户的导出目录
  T="$(mktemp -d)"
  trap 'rm -rf "$T"' EXIT
  copy_all "$T" >/dev/null
  write_index "$T/INDEX.md" "$ROOT/DESIGN.md" "$T"
  verify "$T"
  exit $?
fi

STAMP="$(date '+%Y%m%d-%H%M%S')"
OUT="$DEST/$STAMP"
mkdir -p "$OUT"
copy_all "$OUT"
write_index "$OUT/INDEX.md" "$ROOT/DESIGN.md" "$OUT"
echo "已导出 → $OUT"
verify "$OUT"
echo
echo "提醒:这只是**第二处副本**。异地备份(网盘/移动盘/私有仓库)由你决定 ——"
echo "本脚本不读不打印任何凭据,也不往任何远端传。"
