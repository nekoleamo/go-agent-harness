#!/usr/bin/env bash
# 把当前提交作为**单次快照**推到 Gitee 镜像仓库(GitHub 仍是唯一事实源)。
#
# 为什么不是全量推送:Gitee 免费版单仓库上限 500MB、单文件 50MB,而本仓库 .git 约 1.8GB
# (历史里含产物),全量推必然触发「仓库锁定」。所以只推一份工作树快照:
#   快照 = `git archive HEAD`(仅已跟踪文件,约 66MB / 705 个,最大单文件 3.9MB)
#   ⇒ Gitee 上是**单个提交**的最新代码,供浏览/下载,不带历史。
#
# 用法:
#   GITEE_TOKEN=xxx bash scripts/sync-gitee.sh                  # 推 HEAD 快照到 master
#   GITEE_TOKEN=xxx GITEE_USER=xxx bash scripts/sync-gitee.sh
#   GITEE_URL=git@gitee.com:null_593_5354/go-agent-harness.git bash scripts/sync-gitee.sh
#     ↑ 本机已把 SSH 公钥加到 Gitee 时用这条(不需要令牌)
#
# 环境:GITEE_REPO(缺省 null_593_5354/go-agent-harness)/ GITEE_TOKEN / GITEE_URL / GITEE_BRANCH
#       GAH_EXTRA_FILES(逗号分隔的 `源:目标` 对,一并写进快照,例如把 latest.json 放仓库根)
#
# 注意:每次快照都会在 Gitee 侧新增 git 对象(约 66MB 未压缩),长期累积可能触及 500MB 上限;
# 触线时删除并重建 Gitee 仓库、再跑一次本脚本即可(它不依赖远端已有历史)。
set -euo pipefail
cd "$(dirname "$0")/.."

REPO="${GITEE_REPO:-${GAH_GITEE_REPO:-null_593_5354/go-agent-harness}}"
TOKEN="${GITEE_TOKEN:-}"
USER_="${GITEE_USER:-${REPO%%/*}}"
BRANCH="${GITEE_BRANCH:-master}"

if [ -n "${GITEE_URL:-}" ]; then
  URL="$GITEE_URL"
elif [ -n "$TOKEN" ]; then
  URL="https://${USER_}:${TOKEN}@gitee.com/${REPO}.git"
else
  echo "缺凭据:设 GITEE_TOKEN(Gitee 私人令牌,projects 权限)或 GITEE_URL(如 git@gitee.com:$REPO.git)" >&2
  exit 1
fi

sha="$(git rev-parse --short HEAD)"
subject="$(git log -1 --pretty=%s)"
tag="$(git describe --tags --exact-match 2>/dev/null || true)"
msg="snapshot${tag:+ $tag}: $sha $subject

GitHub 是唯一事实源(nekoleamo/go-agent-harness);本仓库是最新代码快照,不带历史。"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git archive HEAD | tar -x -C "$tmp"
# 额外文件注入(逗号分隔的 `源:目标` 对)。用途:把 dist-desktop/gitee/latest.json 作为
# latest.json 一起提交 —— 客户端从 Gitee raw 通道读的就是它。
if [ -n "${GAH_EXTRA_FILES:-}" ]; then
  IFS=',' read -r -a pairs <<< "$GAH_EXTRA_FILES"
  for pair in "${pairs[@]}"; do
    extra_src="${pair%%:*}"
    extra_dst="${pair#*:}"
    [ -f "$extra_src" ] || { echo "GAH_EXTRA_FILES 里的源文件不存在:$extra_src" >&2; exit 1; }
    mkdir -p "$tmp/$(dirname "$extra_dst")"
    cp "$extra_src" "$tmp/$extra_dst"
    echo "注入 $extra_src → $extra_dst"
  done
fi
(
  cd "$tmp"
  git init -q -b "$BRANCH"
  git add -A
  git -c user.name="gah mirror" -c user.email="mirror@gah.invalid" \
      -c commit.gpgsign=false commit -q -m "$msg"
  files="$(git ls-files | wc -l | tr -d ' ')"
  git push -q --force "$URL" "refs/heads/$BRANCH"
  echo "已推 Gitee 快照:$REPO#$BRANCH($files 个文件,$(du -sh .git | cut -f1) 对象)"
)
