#!/usr/bin/env bash
# gah 便携启动:数据根 = gah 二进制同级的 gah-data(由 gah 自解析自动创建,无需 env,
# GAH_HOME env 不作输入源——2026-09-16 收紧);启动环境变量(如 GAH_MCP_COMMANDS
# 外部 MCP server)统一在 gah-data/env.sh 管理。
# 换机/升级:只需携带 gah + gah-data(含 env.sh)+ start.sh。
set -euo pipefail
cd "$(dirname "$0")"

# 加载便携环境(不存在则忽略——数据目录内可选文件)
if [ -f gah-data/env.sh ]; then
  # shellcheck disable=SC1091
  source gah-data/env.sh
fi

# 孤儿外部插件进程清理:插件是本进程的子进程,宿主一死(PPID 变 1)就无主 —— 它们占着
# 内存与端口,还会让 web 监听失败(用户只看到 bind: address already in use)。
# 新版二进制靠 stdin EOF 自退(宿主被 kill -9 也会退),残留主要来自旧版或被强杀时卡住的进程。
# 只清插件,且按 **argv[0] 精确匹配** —— 命令行里只是出现过该路径的无关进程(编辑器、tail、
# 甚至本次排障用的 shell)不该被杀。PPID=1 的 **gah 主进程**正是用户有意后台常驻的实例,不动它。
# GAH_NO_REAP=1 跳过。
if [ "${GAH_NO_REAP:-0}" != "1" ]; then
  orph=$(ps -eo pid=,ppid=,command= 2>/dev/null | awk '$2==1 && $3 ~ /gah-data\/plugins\/[^\/]+\/[^\/]+$/ {print $1}' || true)
  if [ -n "$orph" ]; then
    # shellcheck disable=SC2086
    kill $orph 2>/dev/null || true
    echo "[start.sh] 清理孤儿插件进程: $(echo $orph | tr '\n' ' ')" >&2
  fi
fi

exec ./gah "$@"
