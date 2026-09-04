#!/bin/bash
# 生成随包分发的外部工具插件二进制(M6.9 工具类全外部化):tool-basic(三件套)、
# tool-workflow(starlark 引擎,经宿主回调通道)、tool-mcp(MCP client 桥)。
# P0 体积门回归(M7):strip(-s -w)+ gzip(Go 二进制压缩率 ~50%);embed 存 .gz,
# 宿主首启 EnsurePlugins 解压落盘;产物缺失时主包构建失败(goreleaser before hook 调用)。
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p internal/embed/extplugins
for name in tool-basic tool-workflow tool-mcp; do
  go build -trimpath -ldflags "-s -w" -o /tmp/ext-${name} ./extplugins/${name}
  gzip -9 -f /tmp/ext-${name}
  mv /tmp/ext-${name}.gz internal/embed/extplugins/${name}.gz
done
ls -la internal/embed/extplugins/
