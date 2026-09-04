#!/bin/bash
# 生成随包分发的外部工具插件二进制(M6.8 工具类全外部化):tool-basic(三件套)、
# tool-workflow(starlark 引擎,经宿主回调通道)、tool-mcp(MCP client 桥)。
# 发布流水线(goreleaser before hook)与本地开发前置调用;产物缺失时主包构建失败。
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p internal/embed/extplugins
flags="-trimpath"
go build $flags -o internal/embed/extplugins/tool-basic ./extplugins/tool-basic
go build $flags -o internal/embed/extplugins/tool-workflow ./extplugins/tool-workflow
go build $flags -o internal/embed/extplugins/tool-mcp ./extplugins/tool-mcp
ls -la internal/embed/extplugins/
