# 开发规范(gah 仓库)

> 供 AI 与开发者共同遵守;插件开发细读 [docs/PLUGIN_DEV.md](docs/PLUGIN_DEV.md)。

## 项目定位
- 微内核 harness:core/ 仅含 ctx/event/plugin/config/boot,零 Agent 能力、零 UI。
- 一切能力以插件形式存在(plugins/ 下),经 bundle 装配,配置层(profile→bundle→patch)可随时插拔开关。
- 交付形态:单一静态二进制(`CGO_ENABLED=0`),运行时依赖 = 0(见 DESIGN.md §7)。

## 红线
- **插件只 import sdk/**,不 import core/、tui/ 或其它插件包(防 import 环;依赖经 Ctx 注入,宿主内部服务经 `system.registry`/`system.catalogue` 注入)。
- **注册即副作用,卸载即撤销**:任何 Provide/Subscribe/工具注册必须随 Disposer 撤销,disposers 幂等。
- **插拔安全**:运行期卸载不得破坏依赖者(`BlockedByLoaded` 拒绝被依赖插件的卸载);工具同名注册非静默(记警告)。
- **插件声明单一事实源是 `plugins/catalogue`**(provides/requires/bundle 归属);新增插件必须登记,装配层按配置树 enabled 启停。
- 缺失依赖显式失败,不静默降级。
- core/ 包必须带单测;新增分发模式/生命周期语义需测试覆盖。

## 语言与工具
- Go 1.27+;golangci-lint;testify 可选,多数用例标准库断言即可。
- 发布编译:`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)"`。
- CI:go vet + go test ./... -race;全库测试必须保持 -race 全绿。

## 事件与配置约定
- 插件事件名用点分(`agent/pre-step`、`tools/pre-execute`),扩展点优先 waterfall(=veto 语义)。
- 配置条目 id = `<type>-<name>`,与 plugins/ 目录同名;config/bundle-*.yaml 为样板数据源。

## 测试惯例
- 插件单测:同包 + 标准库断言;涉及跨插件装配用黑盒包(如 `host-plugin-manager` 的 `_test` 包)防 import 环。
- 端到端:tests/ 包直接装配 base bundle + 注入 `system.*` 服务,验 round 流程与"模型可见即已记录"不变量。
- 外部进程插件(host-bridge/mcp-bridge):测试内 `go build` 产物到临时目录再加载。

## 会话状态
- 交付里程碑:M1–M5.6 + 完善 A/B 组 + M6.1–M6.6 + P0–P2 全部交付(见 DESIGN.md §14),均已提交;-race 全绿(`go test -race ./...`)。
- M6 已交付:host-jobs 后台任务、pty 交互(tool-shell data.pty)、tool-files/tool-web(沙箱联动)、插件安装 `gah -install/-uninstall/-list-plugins`;P0–P2:外部化+崩溃拉起+工具级超时、tool-basic 随包释放、插拔解耦矩阵。
- M6.8 拆分重构:token-compress(滚动摘要压缩从 host-session-log 拆出,独立开关/测试)+ host-fanout(子代理编排从 tool-workflow 拆出,ctx.fanout 宿主服务);tool-workflow 回归纯 starlark 执行器。
- 新增能力:Anthropic 适配器(llm-anthropic-compat,`claude-*` 模型前缀路由)、**MCP server 端**(plugins/mcp-server,stdio JSON-RPC,与 mcp-bridge 对称,见 DESIGN.md §14.1 M6.7)、AGENTS.md 指令注入(全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md`,顺序即覆盖)、技能机制(host-skills:全局 `$GAH_HOME/skills` + 项目 `.gah/skills` 扫描 SKILL.md,list_skills/read_skill,prompt 注入索引)、自注册技能 `gah-plugin-dev`(仓库 `.gah/skills/`)。
- 交付门已通过:单二进制 15.8MB(<25MB)、`CGO_ENABLED=0` 静态、六目标交叉编译、裸机 `env -i` 启动成功;实测见 DESIGN.md §7.6,发行配置 `.goreleaser.yaml`,CI 见 `.github/workflows/ci.yml`(vet + -race + 裸机冒烟)。
- 测试注意:tests 卸载矩阵需关闭 llm-anthropic-compat(防打真实 API),补丁已入仓;新插件必须登记 catalogue 并同步 config 与 internal/embed/seed 两份 bundle 样板。
