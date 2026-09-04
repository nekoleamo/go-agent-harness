#!/bin/bash
# 生成随包分发的基础工具插件二进制(P1 方案 B):tool-basic(三件套)随主二进制 embed。
# 发布流水线(goreleaser before hook)与本地开发前置调用;产物缺失时主包构建失败。
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p internal/embed/extplugins
go build -trimpath -ldflags "-s -w" -o internal/embed/extplugins/tool-basic ./extplugins/tool-basic
ls -la internal/embed/extplugins/
