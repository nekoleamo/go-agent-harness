#!/usr/bin/env bash
# 发布编排层(单一入口):preflight → wait → tag → watch → verify → mirror → endpoint
#
# 为什么需要它(2026-10-04 v0.5.0 → v0.5.1 → v0.5.2 连发三版、两版带 P0 的直接教训):
#   ① **tag 之前没有等 master 的 CI**。v0.5.0 与 v0.5.1 都是在「本地全绿」后**约一分钟**
#      就打了 tag,而那两版的 CI 都是红的 —— 且 v0.5.0 的缺陷**已进产物**。
#      `wait` 这一步就是为它存在的,也是整个脚本里性价比最高的一条。
#   ② **本地的「绿」可能跑的是旧产物**。布局护栏跑的是 `web/dist` 而不是源码;dist 陈旧时
#      「绿」是上一版代码的绿。`preflight` 里那一步就是为它存在的。
#
# 形态:**分阶段 + 红灯即停**,不是「一键全做」。发布是唯一做错了就公开出去、且难以撤回的事;
# 每个阶段能单独跑(事故复盘时常常只跑其中一段),任何一步红就停在那,不带未核验的状态往前走
# —— 这正是镜像链路那两道护栏已经证明有效的形态。
#
# 本层**不发明逻辑**:所有实质能力都已存在(gate.sh / coverage-check.sh / size-check.sh /
# verify-release.mjs / mirror-gitee.sh / sync-gitee.sh),这里只做**顺序、门、停**。
# 出现新的构建/校验逻辑就意味着第二份事实源,而本项目吃过两次亏(gate.sh 之前的三步只活在
# 开发者习惯里;assert_arch 两份实现会漂)。
#
# 用法:
#   bash scripts/release.sh preflight          # tag 之前的本地闸(不联网)
#   bash scripts/release.sh wait               # 等 master 的 CI 五 job 全绿
#   bash scripts/release.sh tag v0.6.0 --yes    # 打 tag 并推(默认只打印将执行的命令)
#   bash scripts/release.sh watch v0.6.0       # 盯 release-cli + release-desktop 到终态
#   bash scripts/release.sh verify v0.6.0      # 产物校验门(逐平台验签)
#   bash scripts/release.sh mirror v0.6.0 --mirror      # Gitee 镜像(需 env 的 GITEE_TOKEN)
#   bash scripts/release.sh endpoint v0.6.0    # 回查壳的第一顺位端点
#   bash scripts/release.sh all v0.6.0 --yes --mirror
#   bash scripts/release.sh preflight --fast   # 跳过重活(交叉编译/desktop),只跑快的
#
# 环境:
#   GAH_RELEASE_TIMEOUT   wait/watch 的超时秒数(缺省 1800;超时按**红**处理,不是继续等)
#   GAH_GITHUB_REPO       缺省 nekoleamo/go-agent-harness
#   GITEE_REPO            缺省 null_593_5354/go-agent-harness(与 mirror-gitee.sh 同口径)
#   GITEE_TOKEN           mirror 阶段**只认 env**。本脚本**不读任何 secrets 文件** ——
#                         凭据引入留在仓库外(`~/.pi/agent/local/gitee-release.sh` 只做这一件事)
#
# 明确不做(红线,见方案 §5):不自动删 tag;镜像失败**不自动重试**(重试 = 「先删后传」,
# 半途失败会留下缺件);不提供 `--force`(有例外就手工跑叶子脚本 —— 它们本来就能单独跑)。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

GH_REPO="${GAH_GITHUB_REPO:-nekoleamo/go-agent-harness}"
GITEE_REPO="${GITEE_REPO:-null_593_5354/go-agent-harness}"
TIMEOUT="${GAH_RELEASE_TIMEOUT:-1800}"

# ci.yml 的五个 job。**逐个判绿**而不是「workflow 结束」—— 后者在有 job 被 skip 时也会是绿的。
CI_JOBS=(test test-macos test-windows desktop-shell desktop-shell-macos)
RELEASE_WORKFLOWS=(release-cli release-desktop)

# 先取子命令并 shift —— **再**扫旗标/位置参数。
# (顺序反了的话子命令会被当成位置参数,报「多余的位置参数」——第一版就栽在这。)
sub="${1:-}"
if [ -z "$sub" ]; then grep '^#' "$0" | tail -n +2 | sed 's/^# \{0,1\}//'; exit 0; fi
shift || true

FAST=0; YES=0; MIRROR=0; SKIP_VERIFY=0
TAG=""; ARGS=()
for a in "$@"; do
  case "$a" in
    --fast) FAST=1 ;;
    --yes) YES=1 ;;
    --mirror) MIRROR=1 ;;
    --skip-artifacts) SKIP_VERIFY=1 ;;
    -h|--help) grep '^#' "$0" | tail -n +2 | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "未知旗标:$a(--help 看用法)" >&2; exit 2 ;;
    *) ARGS+=("$a") ;;
  esac
done
[ "${#ARGS[@]}" -le 1 ] || { echo "多余的位置参数:${ARGS[*]:1}" >&2; exit 2; }
if [ "${#ARGS[@]}" -ge 1 ]; then TAG="${ARGS[0]}"; fi

# —— 输出小工具(不用颜色:日志会被 CI 抓走,转义序列只会更难读)——
say()  { printf '%s\n' "$*"; }
step() { say ""; say "== $* =="; }
die()  { say ""; say "!! $* " >&2; exit 1; }

# need_cmd 缺依赖 ⇒ **显式拒绝**,不静默跳过。
# 为什么:静默跳过会让发版链「看起来跑完了」,而某一步其实没做 —— 那比红更危险。
need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺 $1 —— 这一阶段做不了。装它,或先用其它子命令(缺依赖不静默跳过)"
}

need_repo_scripts() {
  [ -d "$REPO_ROOT/scripts" ] || die "找不到仓库:$REPO_ROOT"
}

ver_of() { echo "${1#v}"; }

# file_mtime / newest_mtime:跨 BSD(macOS)与 GNU(Linux)的 mtime 取值。
# 不用 `-newer` 做判据是因为它在两边语义一致但错误信息更难读;这里显式取秒数。
file_mtime() {
  stat -f %m "$1" 2>/dev/null || stat -c %Y "$1" 2>/dev/null || echo 0
}
newest_mtime() {
  find "$1" -type f -print0 2>/dev/null | xargs -0 stat -f %m 2>/dev/null \
    | sort -rn | head -1 || echo 0
}

# ───────────────────────────── preflight ─────────────────────────────
# tag 之前的本地闸。这一步拦的是「本地绿是假的 / 本地没绿」。
preflight() {
  step "preflight · 工作树与产物新鲜度"
  if [ -n "$(git status --porcelain)" ]; then
    git status --short | sed 's/^/    /'
    die "工作树不干净。发行要求干净工作树(goreleaser 的 before hooks 会重建入库产物,脏树直接失败)"
  fi
  # web/dist 与 web-src 的新鲜度 —— v0.5.0 的 P0 就是躲在这里。
  #
  # 布局护栏与 go:embed 跑的都是 `web/dist`;若它比源码旧,本地「绿」指的是**上一版代码**的绿。
  # 判据用 mtime(最便宜且够用):源码里最新的一个文件 vs 产物入口的 mtime。
  [ -f web/dist/index.html ] || die "缺 web/dist/index.html —— 先跑:bash scripts/gen-web.sh"
  local src_mtime dist_mtime
  src_mtime="$(newest_mtime web-src/src)"
  dist_mtime="$(file_mtime web/dist/index.html)"
  say "    web/dist=$dist_mtime  web-src/src 最新=$src_mtime"
  [ "$dist_mtime" -ge "$src_mtime" ] || die "web/dist 比 web-src/src 旧 ——
  本地护栏跑的是**上一版代码**的产物(v0.5.0 的 P0 正是这样溜过去的)。先跑:bash scripts/gen-web.sh"

  step "preflight · 静态检查(gate.sh)"
  bash scripts/gate.sh || die "gate.sh 红(gofmt / go vet ×2 / staticcheck ×2)"

  step "preflight · 覆盖率棘轮"
  local covdir; covdir="$(mktemp -d)"
  GAH_COVER_MERGE_DIR="$covdir" bash scripts/coverage-check.sh || die "覆盖率门红(棘轮只许上调)"
  rm -rf "$covdir"

  if [ "$FAST" = 1 ]; then
    say ""
    say "preflight(--fast):跳过交叉编译 / desktop / web 全套 / 体积门"
    return 0
  fi

  step "preflight · 前端(vue-tsc + 单测 + 布局护栏)"
  ( cd web-src
    npm run typecheck || die "vue-tsc 有错"
    npm test           || die "前端单测红"
    GAH_LAYOUT_REQUIRE=1 npm run test:layout || die "布局护栏红(本地产物;它跑的是 web/dist)"
  )

  step "preflight · 桌面壳(cargo fmt --check + cargo test)"
  ( cd desktop/src-tauri
    cargo fmt --check || die "cargo fmt --check 红(桌面壳)"
    cargo test --offline --locked || die "桌面壳单测红"
  )

  step "preflight · 体积门"
  bash scripts/size-check.sh || die "体积门红"

  step "preflight · 六目标交叉编译"
  local t
  for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
    GOOS="${t%/*}" GOARCH="${t#*/}" CGO_ENABLED=0 go build ./... || die "交叉编译红:$t"
    say "    OK $t"
  done
  step "preflight 全绿"
}

# ───────────────────────────── wait ─────────────────────────────
# 等 master 上**当前提交**的 CI 五 job 全绿。v0.5.0/v0.5.1 就是漏了这一步。
wait_ci() {
  need_cmd gh
  local sha; sha="$(git rev-parse HEAD)"
  step "wait · 等 master 的 CI($sha)"
  say "    超时 ${TIMEOUT}s(超时按**红**处理,不当「还在跑」)"

  # 推表可能比 run 列表快(旧 run 先返回);等本次提交的 run 出现。
  local run="" i
  for i in $(seq 1 20); do
    run="$(gh run list --repo "$GH_REPO" --commit "$sha" --json databaseId,workflowName \
             -q '[.[] | select(.workflowName == "ci")][0].databaseId' 2>/dev/null || true)"
    [ -n "$run" ] && break
    sleep 5
  done
  [ -n "$run" ] || die "等了 100s 还没出现本次提交的 CI run(确认已 push master:git log -1 --oneline)"

  local deadline=$(( $(date +%s) + TIMEOUT ))
  while :; do
    [ "$(date +%s)" -lt "$deadline" ] || die "等 CI 超时(${TIMEOUT}s)。别急着打 tag —— 本次已有两版是把红 CI 发出去的"
    local status conclusion
    status="$(gh run view "$run" --repo "$GH_REPO" --json status -q .status 2>/dev/null || echo unknown)"
    [ "$status" = completed ] && break
    sleep 20
  done
  conclusion="$(gh run view "$run" --repo "$GH_REPO" --json conclusion -q .conclusion 2>/dev/null || echo unknown)"

  # 逐 job 判绿 —— 「workflow 成功」在有 job 被 skip 时也会为真。
  local j jc bad=0
  for j in "${CI_JOBS[@]}"; do
    jc="$(gh run view "$run" --repo "$GH_REPO" --json jobs \
           -q ".jobs[] | select(.name == \"$j\") | .conclusion" 2>/dev/null || echo missing)"
    if [ "$jc" = success ]; then
      say "    OK   $j"
    else
      say "    红   $j = ${jc:-<无此 job>}" >&2; bad=1
    fi
  done
  [ "$bad" = 0 ] || die "CI 有 job 非 success(run $run,conclusion=$conclusion)。红了不要打 tag"
  [ "$conclusion" = success ] || die "CI run $run 的 conclusion=$conclusion"
  say "    run $run 五 job 全绿"
}

# ───────────────────────────── tag ─────────────────────────────
# 默认**只打印将执行的命令**;`--yes` 才真推。tag 一旦推出,产物即公开。
do_tag() {
  [ -n "$TAG" ] || die "用法:release.sh tag <vX.Y.Z> --yes"
  git rev-parse -q --verify "refs/tags/$TAG" >/dev/null 2>&1 && die "tag $TAG 已存在"
  [ -z "$(git status --porcelain)" ] || die "工作树不干净,不打 tag"
  git fetch origin master --quiet
  [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/master)" ] \
    || die "本地 HEAD 不等于 origin/master —— 先 push 再打 tag"

  # 不依赖会话记忆:重新判一次「本次提交的 master CI 是否已绿」。
  need_cmd gh
  local sha run status conclusion
  sha="$(git rev-parse HEAD)"
  run="$(gh run list --repo "$GH_REPO" --commit "$sha" --json databaseId,workflowName \
         -q '[.[] | select(.workflowName == "ci")][0].databaseId' 2>/dev/null || true)"
  [ -n "$run" ] || die "本次提交还没有 CI run —— 先 push master 并跑完 CI(release.sh wait)"
  status="$(gh run view "$run" --repo "$GH_REPO" --json status -q .status 2>/dev/null || echo unknown)"
  conclusion="$(gh run view "$run" --repo "$GH_REPO" --json conclusion -q .conclusion 2>/dev/null || echo unknown)"
  [ "$status" = completed ] && [ "$conclusion" = success ] \
    || die "master 的 CI 还没绿(status=$status conclusion=$conclusion)。**tag 一推产物就公开** —— 先跑 release.sh wait"

  if [ "$YES" != 1 ]; then
    say "将执行:"
    say "  git tag $TAG"
    say "  git push origin $TAG"
    say "没带 --yes,到此为止(这是有意的:tag 不可撤回)"
    return 0
  fi
  git tag "$TAG"
  git push origin "$TAG"
  step "tag $TAG 已推 —— release-cli / release-desktop 会自动开跑"
}

# ───────────────────────────── watch ─────────────────────────────
watch_release() {
  need_cmd gh
  [ -n "$TAG" ] || die "用法:release.sh watch <vX.Y.Z>"
  step "watch · 盯 ${RELEASE_WORKFLOWS[*]} 到终态"
  local deadline=$(( $(date +%s) + TIMEOUT ))
  local w run status conclusion bad=0
  for w in "${RELEASE_WORKFLOWS[@]}"; do
    run=""
    local i
    for i in $(seq 1 20); do
      run="$(gh run list --repo "$GH_REPO" --workflow "$w" --json databaseId,headBranch \
             -q "[.[] | select(.headBranch == \"$TAG\")][0].databaseId" 2>/dev/null || true)"
      [ -n "$run" ] && break
      sleep 5
    done
    [ -n "$run" ] || die "$w 没找到 tag $TAG 的 run"
    while :; do
      [ "$(date +%s)" -lt "$deadline" ] || die "等 $w 超时(${TIMEOUT}s)"
      status="$(gh run view "$run" --repo "$GH_REPO" --json status -q .status 2>/dev/null || echo unknown)"
      [ "$status" = completed ] && break
      sleep 20
    done
    conclusion="$(gh run view "$run" --repo "$GH_REPO" --json conclusion -q .conclusion 2>/dev/null || echo unknown)"
    if [ "$conclusion" = success ]; then say "    OK   $w (run $run)"; else say "    红   $w = $conclusion" >&2; bad=1; fi
  done
  [ "$bad" = 0 ] || die "有 workflow 红。修好可用 workflow_dispatch 补发,或按方案 §5 的红线人工判断"

  # latest.json 只由 release-desktop 的 merge job 生成;缺了它 updater 端点会 404。
  local assets
  assets="$(gh release view "$TAG" --repo "$GH_REPO" --json assets -q '.assets[].name' 2>/dev/null || true)"
  printf '%s\n' "$assets" | grep -qx latest.json \
    || die "Release $TAG 没有 latest.json —— release-desktop 的 merge job 没成功。updater 端点会 404(用户端显示「暂无可用更新」)"
  say "    Release 资产 $(printf '%s\n' "$assets" | grep -c . ) 件,含 latest.json"
}

# ───────────────────────────── verify ─────────────────────────────
do_verify() {
  need_cmd node
  [ -n "$TAG" ] || die "用法:release.sh verify <vX.Y.Z>"
  step "verify · 产物校验门(verify-release.mjs --all)"
  local args=(--tag "$TAG")
  [ "$SKIP_VERIFY" = 1 ] && args+=(--skip-artifacts)
  node scripts/verify-release.mjs "${args[@]}" || die "产物校验不过(逐平台验签 / updater 端点一致性)"
}

# ───────────────────────────── mirror ─────────────────────────────
# Gitee 国内镜像。**只认 env 的 GITEE_TOKEN** —— 本脚本不读任何 secrets 文件。
#
# 为什么要求显式 --mirror:这是整条链里**唯一写外部平台**的一步。默认发生太容易被顺手带过。
do_mirror() {
  need_cmd curl; need_cmd jq; need_cmd gh
  [ -n "$TAG" ] || die "用法:release.sh mirror <vX.Y.Z> --mirror"
  [ "$MIRROR" = 1 ] || die "镜像会写 Gitee(建 Release / 传附件 / 推快照 / 改用户端的第一顺位端点),必须显式 --mirror"
  [ -n "${GITEE_TOKEN:-}" ] || die "缺 env 的 GITEE_TOKEN。
  本脚本**刻意不读 secrets 文件** —— 凭据引入在仓库外:
    bash ~/.pi/agent/local/gitee-release.sh $TAG   (它只做「引凭据 + 调本脚本」这一件事)"

  local dir="${GAH_MIRROR_DIR:-$(mktemp -d)}"
  mkdir -p "$dir"; dir="$(cd "$dir" && pwd)"

  # 1) 拉齐 GitHub Release 上的桌面产物(经 gh-proxy;本机直连拉 25MB 级附件会长时间 0 字节停滞)
  step "mirror 1/4 · 按 Release 资产清单拉齐产物 → $dir"
  local ver names f
  ver="$(ver_of "$TAG")"
  names="$(gh release view "$TAG" --repo "$GH_REPO" --json assets -q '.assets[].name' 2>/dev/null \
          | grep -E '\.app\.tar\.gz$|-setup\.exe$|\.dmg$|-portable\.zip$|^latest\.json$' || true)"
  if [ -z "$names" ]; then
    say "    取不到资产清单(gh 缺失/未登录/无此 tag)→ 退回按平台名猜"
    names="$(printf '%s\n' "gah_${ver}_aarch64.dmg" "gah_${ver}_x64.dmg" \
             "gah_${ver}_x64-setup.exe" "gah_${ver}_x64-portable.zip" "gah.app.tar.gz" "latest.json")"
  fi
  # 目录**先清空**:它是复用的,上一次发版的包还躺在里面 —— 旧脚本会把它们一并传进本 tag
  # (v0.4.1 首次镜像时,v0.1.7 的两个安装包就被传到了 0.4.1 页面上)。
  find "$dir" -maxdepth 1 -type f \( -name '*.dmg' -o -name '*-setup.exe' -o -name '*.app.tar.gz' \
       -o -name '*-portable.zip' -o -name 'latest.json' \) -delete
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    say "    拉取 $f …"
    curl -fsSL --connect-timeout 20 --max-time 900 -o "$dir/$f" \
      "${GH_PROXY:-https://gh-proxy.com/}https://github.com/${GH_REPO}/releases/download/${TAG}/${f}" \
      || die "拉取 $f 失败"
  done <<< "$names"

  # 2) 上传 + 回查匿名直链
  step "mirror 2/4 · 上传桌面附件 + 回查匿名直链"
  bash scripts/mirror-gitee.sh upload "$TAG" "$dir" || die "上传/回查失败"

  # 3) 逐字节核对。同名先删后传若中途失败/截断,updater 只会发现「下到的包签名不对」,
  #    而人眼看到的是「附件在、URL 200」—— 这一步把它变成机器判定。
  step "mirror 3/4 · 逐字节核对(本地 sha256 == Gitee 直链 sha256)"
  local bad=0 url tmp a b
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    local name; name="$(basename "$f")"
    url="https://gitee.com/${GITEE_REPO}/releases/download/${TAG}/${name}"
    tmp="$(mktemp)"
    if ! curl -fsSL --connect-timeout 20 --max-time 900 -o "$tmp" "$url"; then
      say "    拉取失败:$name" >&2; rm -f "$tmp"; bad=1; continue
    fi
    a="$(sha256_of "$f")"; b="$(sha256_of "$tmp")"; rm -f "$tmp"
    if [ "$a" = "$b" ]; then say "    OK   $name  $(printf '%s' "$a" | cut -c1-16)…"
    else say "    不一致 $name:本地 ${a:0:16}… vs Gitee ${b:0:16}…" >&2; bad=1; fi
  done <<< "$(attach_list "$dir")"
  [ "$bad" = 0 ] || die "附件内容与本地副本不一致 —— 先别推更新表(否则线上指向一个换过的包)。
  **不要盲目重跑**:重跑 = 「先删后传」,半途失败会留下缺件。先用 curl -sIL 确认现状。"

  # 4) 推快照 + 注表
  local snap="$REPO_ROOT/dist-desktop/gitee/latest.json"
  [ -f "$snap" ] || die "缺 Gitee 版更新表:$snap"
  step "mirror 4/4 · 推代码快照 + 注入 latest.json"
  GAH_EXTRA_FILES="$snap:latest.json" bash scripts/sync-gitee.sh || die "推快照失败"
}

attach_list() {
  # 便携包一并上传:它不进 latest.json(updater 只认安装包),但分发靠人工链接,
  # 不镜像等于国内用户拿不到(见 mirror-gitee.sh collect 的注释)。
  find "$1" -maxdepth 1 -type f \( -name '*.dmg' -o -name '*-setup.exe' -o -name '*.app.tar.gz' \
       -o -name '*-portable.zip' \) 2>/dev/null | sort
}

sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# ───────────────────────────── endpoint ─────────────────────────────
# 桌面壳的**第一顺位**更新端点。端点停在旧版是**可接受的安全态** —— 要如实说,不要粉饰。
do_endpoint() {
  need_cmd curl; need_cmd jq
  [ -n "$TAG" ] || die "用法:release.sh endpoint <vX.Y.Z>"
  step "endpoint · 回查壳的第一顺位端点"
  local url="https://gitee.com/${GITEE_REPO}/raw/master/latest.json"
  # Gitee raw 是 302 → raw.giteeusercontent.com,必须 -L(少它只拿到 448 字节跳转页,
  # jq 报 `Invalid numeric literal` —— 2026-09-27 踩过,别当「回源延迟」)。
  local got="" i
  for i in $(seq 1 5); do
    got="$(curl -fsSL "$url" 2>/dev/null | jq -r '.version' 2>/dev/null || true)"
    [ "$got" = "$(ver_of "$TAG")" ] && break
    say "    第 $i 次回查得到 '${got:-<不可解析>}',等 10s(刚推完可能有缓存)"
    sleep 10
  done
  curl -fsSL "$url" | jq -r '"  version=\(.version)",
    (.platforms | to_entries[] | "  \(.key) → \(.value.url)")'
  say "    表地址:$url"
  [ "$got" = "$(ver_of "$TAG")" ] \
    || die "端点未对齐到 $TAG(当前 $got)。**这是可接受的安全态**:升级器不会指向未核验的字节"
}

# ───────────────────────────── all ─────────────────────────────
closeout() {
  step "收尾 · 待同步的文档(**脚本不代劳**,见方案 §4 决策 C)"
  cat <<EOF
  三份按 2026-10-01 决策**不入库**,且内容是判断不是机械替换,所以这里只打印清单:

  docs/VERIFY.md    §剩余任务快照:tag=$TAG · CI run 号 · 本轮真机**未做**的项
  docs/TODO_OVERVIEW.md  执行总览加一行;未完成项盘点按需
  DESIGN.md         §14.1 追加交付记录(批次号 + 结论 + 教训)

  本次可用的事实:
    tag          $TAG
    master sha   $(git rev-parse --short HEAD)
EOF
}

need_repo_scripts
# 位置参数已在解析时收进 ARGS;此处按子命令分派。
if [ "${#ARGS[@]}" -gt 0 ]; then TAG="${ARGS[0]}"; fi
set -- "${ARGS[@]:-}"
case "$sub" in
  preflight) preflight "$@" ;;
  wait)      wait_ci "$@" ;;
  tag)       do_tag "$@" ;;
  watch)     watch_release "$@" ;;
  verify)    do_verify "$@" ;;
  mirror)    do_mirror "$@" ;;
  endpoint)  do_endpoint "$@" ;;
  all)
    TAG="${TAG:-${1:-}}"
    [ -n "$TAG" ] || die "用法:release.sh all <vX.Y.Z> --yes --mirror"
    preflight; wait_ci; do_tag "$TAG"; watch_release "$TAG"; do_verify "$TAG"
    [ "$MIRROR" = 1 ] && { do_mirror "$TAG"; do_endpoint "$TAG"; } \
      || say "未带 --mirror:跳过 Gitee 镜像与端点回查(那是唯一写外部平台的一步)"
    closeout ;;
  *) die "未知子命令:$sub(--help 看用法)" ;;
esac