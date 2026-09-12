#!/usr/bin/env bash
# 生成 web/dist(前端产物;主包 go:embed web/dist 依赖,缺失时主包构建失败 = 天然护栏)。
# 对齐 scripts/gen-extplugins.sh 先例:开发态可构建,使用时严格零构建(运行时预编译静态资源)。
# 产物确定性:vue-tsc --noEmit(类型检查,CI 与 gen 同步)+ vite build(版本锁定于 package-lock)。
set -euo pipefail
cd "$(dirname "$0")/../web-src"
if ! command -v npm >/dev/null 2>&1; then
  echo "gen-web: 需要 npm(node) 才能构建前端产物(CI/开发机需安装 Node)" >&2
  exit 1
fi
# npm ci(不是 install):严格按 lockfile 安装且**绝不改写** package-lock.json ——
# 发行路径(goreleaser release)要求工作树干净,npm install 在不同 npm 版本下会重排 lockfile,
# 让 release 直接以「git is in a dirty state」失败(首个 tag 实测踩到)。
npm ci --no-audit --no-fund --silent
npm run build
if [ ! -f ../web/dist/index.html ]; then
  echo "gen-web: 构建产物缺失 web/dist/index.html" >&2
  exit 1
fi
echo "gen-web: 产物已落地 web/dist/$(ls ../web/dist | tr '\n' ' ')"
