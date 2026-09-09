#!/usr/bin/env bash
# gah 便携启动:数据目录 = 本脚本同级 gah-data(自动发现,无需 env);
# 启动环境变量(如 GAH_MCP_COMMANDS 外部 MCP server)统一在 gah-data/env.sh 管理。
# 换机/升级:只需携带 gah + gah-data(含 env.sh)+ start.sh。
set -euo pipefail
cd "$(dirname "$0")"

# 加载便携环境(不存在则忽略——数据目录内可选文件)
if [ -f gah-data/env.sh ]; then
  # shellcheck disable=SC1091
  source gah-data/env.sh
fi

exec ./gah "$@"
