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
#   2. 上传桌面产物(dmg / *-setup.exe / *.app.tar.gz / 便携包 *-portable.zip);同名附件**先删后传**,所以可重复跑;
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
# 上传失败时的响应体暂存(Gitee 的错误信息全在里面;不读就只能猜,2026-10-06 的教训)
TMP_UPLOAD_BODY="$(mktemp)"
trap 'rm -f "$TMP_UPLOAD_BODY"' EXIT
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

# 产物清单:镜像「给人装的包」与「updater 要的包」——
# dmg / *-setup.exe / *.app.tar.gz **外加便携包**。
# 便携包为什么也要镜像:它**不进 latest.json**(updater 只认安装包),分发全靠人工拿链接;
# 而 GitHub 在国内往往拉不动 ⇒ 不镜像等于国内用户没有便携包可用 —— 而便携包恰恰是
# 「不想安装、解压即用」那批人的唯一入口(v0.4.0 起才有)。对 updater 无影响:它不读这两件。
# 产物文件名必须**属于本 tag** 才允许进这个 Release。
#
# 为何要有这道闸(2026-10-03 实测):产物目录是**复用**的(dist-desktop/up),上一次发版
# 的包还躺在里面。上一轮跑 v0.4.1 时,目录里残留的 gah_0.1.7_*.dmg / _x64-setup.exe
# 被原样传进了 **v0.4.1** 的 Gitee Release —— 页面上出现「0.4.1 的 release 里挂着
# 0.1.7 的安装包」,用户点下去装的是三个月前的版本。旧脚本只做「同名先删后传」,
# 对**不同名**的脏文件一无所知。
#
# 形状:gah_<版本>_<平台>.<ext> / gah.app.tar.gz(无版本号,永远放行)。
# 判据取「文件名里含 _<版本>_ 」而不是逐个平台写死 —— 与 --prepare 那侧从 Release
# 资产清单派生名字的做法同源。
belongs_to_tag() {
  local name="$1" ver
  case "$name" in
    gah.app.tar.gz) return 0 ;;                       # 名字里不带版本,属所有 tag
    *.dmg|*setup.exe|*portable.zip) ;;
    *) return 1 ;;                                    # 不认识的形态:不放行(宁可漏传)
  esac
  ver="${tag#v}"
  case "$name" in
    *_"$ver"_*) return 0 ;;
    *) return 1 ;;
  esac
}

collect() {
  find "$dir" -type f \( -name '*.dmg' -o -name '*-setup.exe' -o -name '*.app.tar.gz' -o -name '*-portable.zip' \) 2>/dev/null | sort
}

# collect_checked = collect + 版本闸:不属于本 tag 的**显式报错退出**,不静默跳过。
#
# 为何是 exit 而不是跳过:目录被复用是常态,而「漏传一件便携包」与「多传一件旧版安装包」
# 在发布这一步都同样静默 —— 前者让国内用户拿不到包,后者让他们装到旧版本。宁可停下来。
collect_checked() {
  local f name bad=0
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    name="$(basename "$f")"
    if belongs_to_tag "$name"; then
      echo "$f"
    else
      echo "  拒绝(不属于 $tag):$name  ← $f" >&2
      bad=1
    fi
  done <<< "$(collect)"
  [ "$bad" = 0 ] || { echo "产物目录里有其它版本的残留包 —— 清掉再跑(它们会被传进本 tag 的 Release)" >&2; exit 1; }
}

# prune_foreign 删掉该 Release 上**已存在但不属于本 tag**的附件。
#
# 自愈用:上一轮如果已经传脏(见 belongs_to_tag 的注释),光加闸不够 —— 脏文件已经在
# 线上了,得由这里清掉。判据同上;gah.app.tar.gz 保留。
prune_foreign() {
  local rid="$1" name
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    belongs_to_tag "$name" && continue
    local aid
    aid="$(api_json "$API/releases/$rid/attach_files?access_token=$TOKEN" \
      | jq -r --arg n "$name" '.[] | select(.name == $n) | .id' | head -1)"
    if [ -n "$aid" ]; then
      api_json -X DELETE "$API/releases/$rid/attach_files/$aid?access_token=$TOKEN" >/dev/null
      echo "  已清理不属于本 tag 的旧附件:$name"
    fi
  done < <(api_json "$API/releases/$rid/attach_files?access_token=$TOKEN" | jq -r '.[].name')
}

# prune_old_releases 跨 Release 清理旧版本附件,**只保留最近 N 个版本**(默认 2,可用
# GAH_GITEE_KEEP_RELEASES 覆盖;设 0 = 不清理)。
#
# 为什么必须有这一步(2026-10-06 实测):Gitee 的仓库附件有**总配额 1 GiB**,而每次发布
# 要传 ~100 MiB ⇒ 大约十来个版本就把仓库填满,之后上传直接被拒:
#   HTTP 400 {"message":"验证失败：文件大小已超出仓库附件配额：1 GB"}
# 那个响应体原先**根本没被读出来**,脚本只会「重试三次 → 上传失败」,让人对着一句无信息量
# 的报错猜。(现在会读,见上传失败分支。)
#
# 为什么保留「最近两个」而不是全部:Gitee 镜像是**国内加速**用途,GitHub 才是唯一事实源
# (历史版本在 GitHub 上一件不少)。用户绝大多数只跨一两个版本升级;为它们把仓库附件撑爆,
# 代价是**当前版本谁也发不上去**。
#
# 删的是**附件**,不删 Release 本身:留着空壳 Release,至少 tag 与说明页还在,
# 有人从旧版本点进来时能看到「这个版本的附件已被清理,请到 GitHub 下载」而不是 404 到莫名其妙。
prune_old_releases() {
  local keep="${GAH_GITEE_KEEP_RELEASES:-2}"
  [ "$keep" -gt 0 ] 2>/dev/null || { echo "  GAH_GITEE_KEEP_RELEASES=$keep(非法,跳过清理)" >&2; return 0; }
  # 按 tag 的语义版本排序取最新的 N 个(含本 tag);比字符串序可靠(v0.5.10 > v0.5.9)
  local keep_tags
  keep_tags="$(api_json "$API/releases?access_token=$TOKEN&per_page=100" \
    | jq -r --arg t "$tag" '[.[] | .tag_name]
             | map(select(test("^v?[0-9]+\\.[0-9]+\\.[0-9]+$")))
             | sort_by(. | sub("^v"; "") | split(".") | map(tonumber))
             | reverse
             | .[0:'"$keep"'] | join("\n")')"
  [ -n "$keep_tags" ] || { echo "  取不到可解析的版本列表(跳过清理)" >&2; return 0; }
  local old_rid old_tag
  while IFS= read -r old_rid; do
    old_tag="$(api_json "$API/releases/$old_rid?access_token=$TOKEN" | jq -r '.tag_name')"
    if printf '%s\n' "$keep_tags" | grep -qx -- "$old_tag"; then
      continue
    fi
    local n
    n="$(api_json "$API/releases/$old_rid/attach_files?access_token=$TOKEN" | jq -r 'length')"
    [ "${n:-0}" -gt 0 ] || continue
    # **只删附件,不删 Release**:留着空壳页面,至少有人从旧版本点进来时能看到说明,
    # 而不是点到一个不知道为什么的空页面。Release 元数据几乎不占配额。
    local aid
    while IFS= read -r aid; do
      [ -n "$aid" ] || continue
      api_json -X DELETE "$API/releases/$old_rid/attach_files/$aid?access_token=$TOKEN" >/dev/null 2>&1 || true
    done < <(api_json "$API/releases/$old_rid/attach_files?access_token=$TOKEN" | jq -r '.[].id')
    echo "  已清理旧版本附件:$old_tag($n 件,Release 页保留)"
  done < <(api_json "$API/releases?access_token=$TOKEN&per_page=100" \
            | jq -r --arg t "$tag" '.[] | select(.tag_name != $t)
                     | select(.tag_name | test("^v?[0-9]+\\.[0-9]+\\.[0-9]+$"))
                     | .id')
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
    # 先清旧版本附件(必须**在上传之前**:仓库附件总配额 1 GiB,而一次发布 ~100 MiB,
    # 配额满了当前版本就传不上去)。再自愈本 Release 上的脏附件,最后校验本地产物 ——
    # 顺序反过来时,脏文件会留在线上没人管。
    prune_old_releases
    prune_foreign "$id"
    files="$(collect_checked)"
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
        # 失败时把**响应体**读出来:2026-10-06 排查「配额满了」时,脚本只会说「上传失败」,
        # 而 Gitee 真正回的是 400 + {"message":"...超出仓库附件配额：1 GB"} —— 响应体
        # 不读出来就只能猜。
        if code="$(curl -sS --max-time 900 --connect-timeout 30 -o "$TMP_UPLOAD_BODY" \
             -w '%{http_code}' -X POST \
             "$API/releases/$id/attach_files?access_token=$TOKEN" -F "file=@$f")" \
           && [ "${code:-0}" -ge 200 ] && [ "${code:-0}" -lt 300 ]; then
          ok=1; break
        fi
        echo "  第 $attempt 次上传失败(HTTP ${code:-?})" >&2
        if grep -q '配额' "$TMP_UPLOAD_BODY" 2>/dev/null; then
          echo "  ⇒ Gitee 仓库附件配额已满。GitHub 侧不受影响;清理旧版本附件后重跑即可。" >&2
          echo "     (脚本默认只保留最近 ${GAH_GITEE_KEEP_RELEASES:-2} 个版本的附件;若已手动清过,跳过此步)" >&2
          break   # 配额满时重试三次没有意义,只会白等 15 分钟
        fi
        head -c 200 "$TMP_UPLOAD_BODY" >&2 || true
        echo >&2
        sleep 5
      done
      [ "$ok" = 1 ] || { echo "上传失败:$name(三次都未成功)" >&2; exit 1; }
    done <<< "$files"
    echo "上传完成,回查匿名直链:"
    verify || exit 1
    # 生成 Gitee 版 latest.json:把每个平台的下载地址换到 Gitee 直链(取原 URL 最后两段
    # tag/文件名 ⇒ 与源无关,输入是不是已被加速前缀包过都成)。壳从 raw 通道读这份表。
    if [ -f "$dir/latest.json" ]; then
      mkdir -p "$OUT_DIR/gitee"
      jq --arg g "https://gitee.com/$REPO/releases/download/" \
        '(.platforms |= with_entries(.value |= (.url = ($g + (.url | split("/") | .[-2:] | join("/"))))))
         | (if .portable then (.portable |= with_entries(.value |= (.url = ($g + (.url | split("/") | .[-2:] | join("/")))))) else . end)' \
        "$dir/latest.json" > "$OUT_DIR/gitee/latest.json"
      # 自检:平台集合与签名必须与原表逐平台一致(换源可以,改签名不行)
      diff <(jq -S '.platforms | map_values(.signature)' "$dir/latest.json") \
           <(jq -S '.platforms | map_values(.signature)' "$OUT_DIR/gitee/latest.json") \
        || { echo "自检失败:Gitee 版表的签名与原表不一致" >&2; rm -rf "$OUT_DIR/gitee"; exit 1; }
      jq -e --arg b "https://gitee.com/$REPO/releases/download/" \
        '[.platforms[].url | startswith($b)] | all' "$OUT_DIR/gitee/latest.json" >/dev/null \
        || { echo "自检失败:仍有下载地址未指向 Gitee" >&2; rm -rf "$OUT_DIR/gitee"; exit 1; }
      # 便携资产同样要换源(它在顶层 portable 键下,不参与 updater,但下载页要指着 Gitee)
      if jq -e 'has("portable") and (.portable | length > 0)' "$dir/latest.json" >/dev/null; then
        jq -e --arg b "https://gitee.com/$REPO/releases/download/" \
          '[.portable[].url | startswith($b)] | all' "$OUT_DIR/gitee/latest.json" >/dev/null \
          || { echo "自检失败:便携包地址未指向 Gitee" >&2; rm -rf "$OUT_DIR/gitee"; exit 1; }
        diff <(jq -S '.portable | map_values(.note)' "$dir/latest.json") \
             <(jq -S '.portable | map_values(.note)' "$OUT_DIR/gitee/latest.json") >/dev/null \
          || { echo "自检失败:便携包的 note 被改写了" >&2; rm -rf "$OUT_DIR/gitee"; exit 1; }
      fi
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
