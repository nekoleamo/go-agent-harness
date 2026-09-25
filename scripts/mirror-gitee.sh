#!/usr/bin/env bash
# 把发布产物镜像到 Gitee Release(国内加速;零成本自控镜像,不依赖公益代理)。
#
# 用法:
#   GITEE_TOKEN=xxx bash scripts/mirror-gitee.sh upload <tag> <产物目录>
#   bash scripts/mirror-gitee.sh verify <tag> <产物目录>     # 只回查匿名直链(不需令牌)
#   GITEE_TOKEN=xxx bash scripts/mirror-gitee.sh list <tag>
#
# 做什么(upload):
#   1. 创建(或复用)Gitee 上同名 tag 的 Release;
#   2. 上传桌面产物(dmg / *-setup.exe / *.app.tar.gz);同名附件**先删后传**,所以可重复跑;
#   3. 回查匿名直链(HEAD 跟随重定向后 200)并打印 —— 这是「镜像真的能下」的唯一自动出口。
#
# 之后由调用方决定是否把 latest.json 的下载地址换成 Gitee(GitHub 与 Gitee 的直链同形:
# 同 tag、同文件名,只差 host 与仓库路径):
#   bash scripts/publish-desktop.sh rewrite-url gitee:<owner/repo>
#
# 令牌:Gitee → 设置 → 私人令牌,勾选 projects 权限。只经 env 传入,不落盘、不入库
# (Gitee API 用 `access_token` 查询参数,故会出现在本机进程列表里;CI 上由 GitHub 打码)。
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="${GITEE_REPO:-${GAH_GITEE_REPO:-null_593_5354/go-agent-harness}}"
OUT_DIR="${GAH_DIST_DIR:-dist-desktop}"
API="https://gitee.com/api/v5/repos/$REPO"
SITE="https://gitee.com/$REPO"
TOKEN="${GITEE_TOKEN:-}"

usage() {
  cat >&2 <<EOF
用法:
  GITEE_TOKEN=xxx bash scripts/mirror-gitee.sh upload <tag> <产物目录>
  bash scripts/mirror-gitee.sh verify <tag> <产物目录>
  GITEE_TOKEN=xxx bash scripts/mirror-gitee.sh list <tag>
环境:GAH_GITEE_REPO 或 GITEE_REPO(缺省 $REPO)、GITEE_TOKEN
EOF
  exit 1
}

cmd="${1:-}"; tag="${2:-}"; dir="${3:-}"
[ -n "$cmd" ] && [ -n "$tag" ] || usage
command -v curl >/dev/null || { echo "需 curl"; exit 1; }
command -v jq >/dev/null || { echo "需 jq"; exit 1; }

api_json() { curl -fsS --max-time 60 "$@"; }
need_token() {
  [ -n "$TOKEN" ] || { echo "缺 GITEE_TOKEN(Gitee → 设置 → 私人令牌,勾选 projects)" >&2; exit 1; }
}

# 产物清单:只镜像「给人装的包」与「updater 要的包」三类
collect() {
  find "$dir" -type f \( -name '*.dmg' -o -name '*-setup.exe' -o -name '*.app.tar.gz' \) 2>/dev/null | sort
}

# 找 Release id(tag 不存在则创建)
release_id() {
  local id
  id="$(api_json "$API/releases/tags/$tag?access_token=$TOKEN" | jq -r '.id' 2>/dev/null || true)"
  if [ -z "$id" ] || [ "$id" = "null" ]; then
    id="$(api_json -X POST "$API/releases" \
            -d "access_token=$TOKEN" -d "tag_name=$tag" -d "name=gah $tag" \
            -d "body=自动镜像自 GitHub Release $tag(GitHub 仍是唯一事实源)。" \
            -d "target_commitish=master" | jq -r '.id')"
    echo "已在 Gitee 创建 Release $tag(id=$id)" >&2
  fi
  [ -n "$id" ] && [ "$id" != "null" ] || { echo "取不到 Release id(令牌权限不足?需 projects)" >&2; exit 1; }
  printf '%s' "$id"
}

attachment_id_of() { # <release_id> <文件名>
  # 注意:匹配串要作为 jq 变量传入 —— 直接写 $2 是 shell 位置参数,jq 会报 compile error
  # (2026-09-25 首发实测:删除同名附件那步静默失败)。
  api_json "$API/releases/$1/attach_files?access_token=$TOKEN" \
    | jq -r --arg n "$2" '.[] | select(.name == $n or (.name | contains($n))) | .id' | head -1
}

link_of() { printf '%s/releases/download/%s/%s' "$SITE" "$tag" "$1"; }

verify() {
  local f name code fail=0
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    name="$(basename "$f")"
    # 只取首字节(-r 0-0 ⇒ 206):回查要回答的问题是「匿名能不能下到」,
    # 不是「把这个 25MB 全下回来」。CDN 不支持 Range 时会退成 200,两种都算通过。
    code="$(curl -sS -L -r 0-0 -o /dev/null -w '%{http_code}' --max-time 60 "$(link_of "$name")" || echo 000)"
    printf '  %s  %s\n' "$code" "$(link_of "$name")"
    case "$code" in 200|206) ;; *) fail=1 ;; esac
  done <<< "$(collect)"
  [ "$fail" = 0 ] || { echo "有附件匿名直链拿不到 200/206 —— 检查仓库是否公开、附件是否上传成功" >&2; return 1; }
}

case "$cmd" in
  upload)
    need_token
    [ -n "$dir" ] && [ -d "$dir" ] || usage
    id="$(release_id)"
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      name="$(basename "$f")"
      old="$(attachment_id_of "$id" "$name" || true)"
      if [ -n "$old" ]; then
        api_json -X DELETE "$API/releases/$id/attach_files/$old?access_token=$TOKEN" >/dev/null
        echo "  旧附件已删除:$name"
      fi
      echo "  上传 $name($(wc -c < "$f" | tr -d ' ') 字节)…"
      # 上传大文件(25MB 级)不能用 api_json 的 60 秒上限:家里上行与 CI 海外链路都可能更久。
      # 2026-09-25 实测:GitHub runner(海外)传到 Gitee 60 秒 0 字节超时 ⇒ 这里放宽到 15 分钟
      # 并重试三次。注意该链路天生不利,正式镜像建议在国内机器上跑。
      ok=0
      for attempt in 1 2 3; do
        if curl -fsS --max-time 900 --connect-timeout 30 -X POST \
             "$API/releases/$id/attach_files?access_token=$TOKEN" -F "file=@$f" >/dev/null; then
          ok=1; break
        fi
        echo "  第 $attempt 次上传失败,重试…" >&2
        sleep 5
      done
      [ "$ok" = 1 ] || { echo "上传失败:$name(三次都未成功)" >&2; exit 1; }
    done <<< "$(collect)"
    echo "上传完成,回查匿名直链:"
    verify || exit 1
    # 生成 Gitee 版 latest.json:把每个平台的下载地址换到 Gitee 直链(取原 URL 最后两段
    # tag/文件名 ⇒ 与源无关,输入是不是已被加速前缀包过都成)。壳从 raw 通道读这份表。
    if [ -f "$dir/latest.json" ]; then
      mkdir -p "$OUT_DIR/gitee"
      jq --arg g "https://gitee.com/$REPO/releases/download/" \
        '.platforms |= with_entries(.value |= (.url = ($g + (.url | split("/") | .[-2:] | join("/")))))' \
        "$dir/latest.json" > "$OUT_DIR/gitee/latest.json"
      # 自检:平台集合与签名必须与原表逐平台一致(换源可以,改签名不行)
      diff <(jq -S '.platforms | map_values(.signature)' "$dir/latest.json") \
           <(jq -S '.platforms | map_values(.signature)' "$OUT_DIR/gitee/latest.json") \
        || { echo "自检失败:Gitee 版表的签名与原表不一致" >&2; rm -rf "$OUT_DIR/gitee"; exit 1; }
      jq -e --arg b "https://gitee.com/$REPO/releases/download/" \
        '[.platforms[].url | startswith($b)] | all' "$OUT_DIR/gitee/latest.json" >/dev/null \
        || { echo "自检失败:仍有下载地址未指向 Gitee" >&2; rm -rf "$OUT_DIR/gitee"; exit 1; }
      echo "已生成 Gitee 版表:$OUT_DIR/gitee/latest.json"
      echo "→ 随代码快照提交:GAH_EXTRA_FILES=$OUT_DIR/gitee/latest.json:latest.json bash scripts/sync-gitee.sh"
    else
      echo "警告:$dir/latest.json 不存在,跳过 Gitee 版表生成(客户端将只能走 GitHub 表)" >&2
    fi
    echo "→ 客户端取表地址:https://gitee.com/$REPO/raw/master/latest.json"
    ;;
  verify)
    [ -n "$dir" ] && [ -d "$dir" ] || usage
    verify
    ;;
  list)
    need_token
    id="$(api_json "$API/releases/tags/$tag?access_token=$TOKEN" | jq -r '.id')"
    api_json "$API/releases/$id/attach_files?access_token=$TOKEN" | jq -r '.[] | "\(.id)\t\(.name)"'
    ;;
  *) usage ;;
esac
