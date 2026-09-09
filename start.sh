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

exec ./gah "$@"
