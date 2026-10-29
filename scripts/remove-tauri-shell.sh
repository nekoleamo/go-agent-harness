#!/usr/bin/env bash
# 删掉旧 Tauri 桌面壳(Phase:迁移收尾)。**默认干跑**,`--apply` 才真删。
#
# 为什么要脚本而不是手删:旧链散在 6 个地方 —— 目录、三个脚本、一个 CI workflow、
# 两处断言文件(.gitattributes/.gitignore)、AGENTS.md 一行纪律;手删必漏,而漏掉的那处
# 会在某个 CI job 上红(比如 .gitattributes 里给 src-tauri/fixtures 钉的行尾)。
#
# 用法:
#   bash scripts/remove-tauri-shell.sh              # 干跑:只说会做什么
#   bash scripts/remove-tauri-shell.sh --apply      # 真删(会 git rm + 改文件,不提交)
#   bash scripts/remove-tauri-shell.sh --apply --force   # 跳过分支/工作树保护
#
# 前置:新链必须齐备(缺一个就中止 —— 删完没有替代品比留着更糟)。
# 回滚:apply 之后先别提交,`git checkout -- .` 可全额还原;已提交则 `git revert <sha>`。
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

APPLY=0
FORCE=0
for arg in "$@"; do
  case "$arg" in
    --apply) APPLY=1 ;;
    --force) FORCE=1 ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "未知参数:$arg"; exit 2 ;;
  esac
done

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s ==\n' "$*"; }
die()  { printf '中止: %s\n' "$*" >&2; exit 1; }

# ---------- 0) 前置检查 ----------
step "前置检查"
need_files=(
  desktop/wails/main.go
  desktop/wails/embed_sidecar.go
  desktop/wails/installer/installer.nsi
  scripts/gen-desktop-wails.sh
  scripts/publish-desktop-wails.sh
  .github/workflows/release-desktop-wails.yml
)
missing=0
for f in "${need_files[@]}"; do
  [ -e "$f" ] || { say "  ✗ 缺 $f"; missing=1; }
done
[ "$missing" = "0" ] || die "Go+Wails 链不齐,不能删旧链"
say "  ✓ Go+Wails 链齐备(6 个关键文件都在)"

branch="$(git branch --show-current)"
head_sha="$(git rev-parse --short HEAD)"
say "  当前分支:${branch:-<detached>} @ ${head_sha}"
if [ "$APPLY" = "1" ] && [ "$FORCE" != "1" ]; then
  case "$branch" in
    feat/wails-v3-desktop) ;;
    *) die "不是迁移分支(当前 ${branch:-<detached>});确认无误可加 --force" ;;
  esac
  if [ -n "$(git status --porcelain)" ]; then
    die "工作树不干净:先提交或 stash,免得删改与未提交改动混在一起"
  fi
fi
say "  回滚:apply 后未提交 → git checkout -- . ;已提交 → git revert <sha>"

# ---------- 1) 待删除的路径 ----------
step "待删除"
DEL_PATHS=(
  desktop/src-tauri          # Tauri 壳工程(含 Cargo.toml/target/fixtures)
  desktop/ui                 # Tauri 的 frontendDist(只有一个占位 index.html)
  desktop/.cargo             # 项目级 cargo 镜像配置
  scripts/gen-desktop.sh     # 旧:sidecar + cargo build
  scripts/publish-desktop.sh # 旧:tauri signer + latest.json
  scripts/verify-release.mjs # 旧:latest.json 发布校验门
  .github/workflows/release-desktop.yml # 旧:tauri CLI 矩阵
  desktop/wails/cmd/desktopman/desktopman # 误提交的编译产物(顺手清)
  desktop/icons/gen-icon.mjs             # 旧:调 @tauri-apps/cli 生成图标集的 node 脚本
)
for p in "${DEL_PATHS[@]}"; do
  if [ -e "$p" ]; then
    tracked="未跟踪/仅本地"
    git ls-files --error-unmatch "$p" >/dev/null 2>&1 && tracked="git 跟踪"
    n=$(git ls-files "$p" | wc -l | tr -d ' ')
    say "  - $p(${tracked},${n} 个跟踪文件)"
  else
    say "  · $p(不存在,跳过)"
  fi
done
say "  - dist-desktop/(旧产物目录,gitignore;仅本地)"

# ---------- 2) 待改的文件 ----------
step "待改动的文件"
plan_edits() { :; }

# .gitattributes:删掉给 tauri fixtures 钉行尾的那行
GITATTR_HIT=$(grep -n "desktop/src-tauri" .gitattributes 2>/dev/null || true)
[ -n "$GITATTR_HIT" ] && say "  ~ .gitattributes: 删除 ${GITATTR_HIT%%:*} 行(src-tauri fixtures 行尾钉死)"

# .gitignore:删 tauri 条目,加一条防再次误提交二进制
say "  ~ .gitignore: 删 /desktop/src-tauri/{target,binaries}/ 与 /dist-desktop/,并忽略 desktopman 产物"

# ci.yml:删 desktop-shell job
CI_RANGE=$(awk '
  /^  desktop-shell:$/ { start=NR; next }
  start && /^  [a-z0-9-]+:$/ { print start"-"(NR-1); found=1; exit }
  END { if (start && !found) print start"-"NR }
' .github/workflows/ci.yml)
if [ -n "$CI_RANGE" ]; then
  say "  ~ .github/workflows/ci.yml: 删除 desktop-shell job(第 ${CI_RANGE} 行)"
else
  say "  · .github/workflows/ci.yml: 找不到 desktop-shell job(可能已删)"
fi

# AGENTS.md:删桌面壳(SQL 无关)那一行纪律
AG_LINE=$(grep -n '^- 桌面壳(`desktop/src-tauri/`)' AGENTS.md 2>/dev/null | head -1)
[ -n "$AG_LINE" ] && say "  ~ AGENTS.md: 删除第 ${AG_LINE%%:*} 行(桌面壳 cargo 纪律)"

say "  · scripts/mirror-gitee.sh:**保留**(它还管 CLI 归档与 Gitee 代码快照);其桌面段是旧口径,"
say "    等要用 Gitee 镜像桌面产物时改用 desktopman rewrite-url"
say "  · desktop/icons/:**保留**(gah-icon-1024.png 是壳图标生成的唯一源)"

# ---------- 3) 干跑到此结束 ----------
if [ "$APPLY" != "1" ]; then
  step "干跑结束"
  say "什么都没改。确认无误后:bash scripts/remove-tauri-shell.sh --apply"
  exit 0
fi

# ---------- 4) 执行 ----------
step "执行删除"
for p in "${DEL_PATHS[@]}"; do
  [ -e "$p" ] || continue
  if git ls-files --error-unmatch "$p" >/dev/null 2>&1; then
    git rm -r -q --ignore-unmatch "$p" || die "git rm 失败:$p"
  else
    rm -rf "$p" || die "rm 失败:$p"
  fi
  say "  ✓ 删除 $p"
done
rm -rf dist-desktop && say "  ✓ 删除 dist-desktop/"

step "执行文件改动"
python3 - "$(pwd)" <<'PYEDIT'
import re, sys, pathlib

root = pathlib.Path('.')

def read(p):
    return pathlib.Path(p).read_text(encoding='utf-8')

def write(p, s):
    pathlib.Path(p).write_text(s, encoding='utf-8')

# ---- 1) .gitattributes:删 src-tauri 行 ----
p = '.gitattributes'
lines = read(p).split('\n')
kept = [l for l in lines if 'desktop/src-tauri' not in l]
write(p, '\n'.join(kept))
print(f'  ✓ .gitattributes: {len(lines)-len(kept)} 行')

# ---- 2) .gitignore:删 src-tauri 条目及其两段注释;补 desktopman 产物忽略 ----
p = '.gitignore'
lines = read(p).split('\n')
drop_markers = ('src-tauri', '# ② Tauri 编译期生成的 schema', '# 恢复跟踪:git add -f internal/embed/extplugins')
kept = [l for l in lines if not any(m in l for m in drop_markers)]
s2 = '\n'.join(kept)
if 'desktop/wails/cmd/*/desktopman' not in s2:
    # 插在第一个桌面壳产物条目附近(找不到就追加到末尾)
    anchor = next((i for i, l in enumerate(kept) if '/desktop/wails/binaries/' in l), len(kept))
    kept[anchor:anchor] = ['# 工具产物:别再把编译出来的 desktopman 提交进来', '/desktop/wails/cmd/*/desktopman']
    s2 = '\n'.join(kept)
write(p, s2)
print(f'  ✓ .gitignore: 清 {len(lines)-len(kept)} 行 + 忽略 desktopman 产物')

# ---- 3) 工作流:删 desktop-shell* job 与含 Rust 标记的步骤 ----
MARKERS = ('desktop/src-tauri', 'dtolnay/rust-toolchain', 'Swatinem/rust-cache', 'cargo ')
for wf in pathlib.Path('.github/workflows').glob('*.yml'):
    s = read(wf)
    lines = s.split('\n')
    out = []
    i = 0
    removed_jobs = 0
    while i < len(lines):
        l = lines[i]
        # job 头:两空格缩进 + 名字 + 冒号
        m = re.match(r'^  (desktop-shell[a-z-]*):\s*$', l)
        if m:
            removed_jobs += 1
            i += 1
            while i < len(lines) and not re.match(r'^  [a-z0-9-]+:\s*$', lines[i]) and not re.match(r'^\S', lines[i]):
                i += 1
            continue
        # 步骤:六空格缩进 "- "
        if re.match(r'^      - ', l):
            j = i + 1
            while j < len(lines) and not re.match(r'^      - ', lines[j]) and not re.match(r'^  [a-z0-9-]+:\s*$', lines[j]) and not re.match(r'^\S', lines[j]):
                j += 1
            block = '\n'.join(lines[i:j])
            if any(k in block for k in MARKERS):
                i = j
                continue
            out.extend(lines[i:j])
            i = j
            continue
        out.append(l)
        i += 1
    if removed_jobs or out != lines:
        write(wf, '\n'.join(out))
        print(f'  ✓ {wf}: 删 {removed_jobs} 个 desktop-shell job / 清 Rust 步骤')

# ---- 4) AGENTS.md:删掉引用 tauri fixtures 的那条纪律 ----
p = 'AGENTS.md'
lines = read(p).split('\n')
kept = [l for l in lines if 'desktop/src-tauri' not in l]
write(p, '\n'.join(kept))
print(f'  ✓ AGENTS.md: 删 {len(lines)-len(kept)} 行(桌面壳 cargo 纪律)')

# ---- 5) scripts/gen.sh:desktop 子命令改指新链 ----
p = 'scripts/gen.sh'
s = read(p)
m = re.search(r'  desktop\)\n(?:.*\n)*?    ;;\n', s)
if m:
    new_block = (
        '  desktop)\n'
        '    # 旧 Tauri 链已移除;桌面壳构建见 scripts/gen-desktop-wails.sh(壳在 desktop/wails/)。\n'
        '    bash scripts/gen-desktop-wails.sh "${@:2}"\n'
        '    ;;\n'
    )
    s = s[:m.start()] + new_block + s[m.end():]
    write(p, s)
    print('  ✓ scripts/gen.sh: desktop 子命令改指 gen-desktop-wails.sh')
else:
    print('  · scripts/gen.sh: 未找到 desktop 分支(可能已改)')

# ---- 6) scripts/release.sh:兜底检查(本分支已改成 Go 链;若还是 cargo 就报错让人工处理)----
p = 'scripts/release.sh'
s = read(p)
if 'desktop/src-tauri' in s:
    print('  ✗ scripts/release.sh 仍引用 desktop/src-tauri —— 手工把它改成 go vet/test/build')
    sys.exit(1)
print('  ✓ scripts/release.sh: 桌面 preflight 已是 Go 链')
PYEDIT
[ $? -eq 0 ] || die "文件改动失败(可能已部分执行,git checkout -- . 可还原)"

# ---------- 5) 后置校验 ----------
step "后置校验"
left=$(git grep -n -E "src-tauri|tauri\.conf\.json|gen-desktop\.sh|publish-desktop\.sh|release-desktop\.yml" -- \
        ':!docs/' ':!desktop/wails/' ':!scripts/remove-tauri-shell.sh' ':!README.md' ':!README_EN.md' 2>/dev/null | head -20)
if [ -n "$left" ]; then
  say "  仍在引用旧链的文件(人工确认是否只剩历史说明):"
  printf '%s\n' "$left" | sed 's/^/    /'
else
  say "  ✓ 没有残留引用"
fi
say "  下一步:bash scripts/gate.sh（三 module 全门禁）→ 提交"
say "  回滚未提交: git checkout -- . && git clean -fd desktop/src-tauri"
