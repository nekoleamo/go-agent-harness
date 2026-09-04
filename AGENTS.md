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
- M6.9 工具类全外部化:tool-workflow/tool-mcp 移出宿主(extplugins/ 独立二进制,随包 embed 释放);host-bridge 增宿主回调通道(GAH_CB_ADDR,外部进程请求宿主 tools/jobs/fanout);`gah --profile mcp-serve` 独立 serve 形态;外部化后全库依赖 host-bridge(卸载矩阵需先卸 bridge 再卸 jobs/fanout)。
- 新增能力:Anthropic 适配器(llm-anthropic-compat,`claude-*` 模型前缀路由)、**MCP server 端**(plugins/mcp-server,stdio JSON-RPC,与 mcp-bridge 对称,见 DESIGN.md §14.1 M6.7)、AGENTS.md 指令注入(全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md`,顺序即覆盖)、技能机制(host-skills:全局 `$GAH_HOME/skills` + 项目 `.gah/skills` 扫描 SKILL.md,list_skills/read_skill,prompt 注入索引)、自注册技能 `gah-plugin-dev`(仓库 `.gah/skills/`)。
- P4 发行平台匹配:gen-extplugins.sh 按发行矩阵五目标构建外部插件(内含 assert_arch 魔数/架构断言,build 后立即校验)→embed 按平台拆包(build-tag 同名变量,主包只嵌本平台产物,体积门不变);CI 交叉编译矩阵(五目标+每目标体积门+TestExtPluginsMatchPlatform)+ 发行流水线冒烟(goreleaser snapshot 全链路:before hook→五目标归档→产物抽查);tests 经 embed.OpenExtPlugin 读本平台产物。产物用 `gzip -9 -n`(确定性,重跑无 diff)。修改 embed 布局后需重跑 scripts/gen-extplugins.sh(旧平铺 *.gz 会被清,embed 目录缺失主包构建失败)。
- 样板版本化:config/bundle-*.yaml 头部 `# seed-version: N`(seed 与 repo 两份同步,guard 测试强制);EnsureSeed 落盘版本低于 seed → 备份(.bak-时间戳)后覆盖——**新增 base 能力条目必须 bump 版本号**(否则老用户不自动升级)。仅 bundle 系列参与;profile/patch 不覆盖。
- 命令注册表:新增 host-commands(base,Provides ctx.commands):插件在 Start 可选注入注册命令(Register 同名冲突拒绝,Disposer 撤销);命令 Run 返回(输出文本,error);新插件命令自动进提示/help,不改 TUI 代码。新增插件须登记 catalogue + 同步 config 与 seed 两份 bundle 样板。
- LLM 提供商运行时配置:host-llm.SetProvider/ProviderInfo(命中通用适配器,claude-* 路由不受影响)+ 适配器可选 sdk.ProviderAdapter(Configure/ProviderInfo);TUI `/provider show|set|clear` 写 `~/.gah/config/provider.yaml`(0600,env 显式优先,坏 yaml 显式报错)——任意 OpenAI 兼容端点一行配置即用(SiliconFlow 等),零重启。
- TUI 思考状态:回合中状态栏显示思考动画(⠋ 旋转,tea.Every tick)+ Esc 取消提示,工具执行时切换"执行工具: <name>",含当前工作区目录名;空闲停 tick。
- 选择器自由级:CommandSpec.Args 改 ArgLevel(枚举级 Options / 自由级 FreeArgs):命令需手动输入参数(如 /model 模型名、/provider set baseUrl/apiKey)选中后断点保留文本回输入框+提示继续输入,不直接执行;无可定义级命令(/help、/provider show)仍直接执行。
- 思考等级:切换仅 **Tab=前进 / Shift+Tab=后退** 快捷键(off→low→medium→high→off,从 off 开始;无 /thinking 命令),状态栏"思维: <等级>"(off 隐藏);host-llm 会话级注入(请求未显式时),openai→reasoning_effort / anthropic→thinking budget_tokens(1024/4096/16384),off 均不发送(安全默认)。
- 交互式命令选择器:CommandSpec.Args 参数级联选项(每级动态枚举器,nil=自由级断点,最后级无选项直接执行);TUI 输入 / 自动激活列表、↑/↓ 移动、Enter 级联确定、Esc 退出;`/sandbox` → ro|ws|full 示例,host-jobs 的 /jobs 二级动态枚举任务 ID。
- P3 首启健壮性(main 设 `GAH_HOME=home` 统一 home 事实源,ephemeral 彻底隔离外部插件目录)+ host-bridge 加载软降级(单插件加载失败 ERROR 跳过,不拖垮 boot)+ CI 裸机冒烟新增 headless 真实回合护栏;见 DESIGN.md §14.1 P3。
- 交付门已通过:单二进制 31.9MB(<40MB,M7 gzip embed 修复,M6.9 峰值 83.5MB 曾击穿)、`CGO_ENABLED=0` 静态、六目标交叉编译、裸机 `env -i` 启动成功 + 体积门(CI);实测见 DESIGN.md §7.6,发行配置 `.goreleaser.yaml`,CI 见 `.github/workflows/ci.yml`(vet + -race + 体积门 + 裸机冒烟)。
- 测试注意:tests 卸载矩阵需关闭 llm-anthropic-compat(防打真实 API)且按依赖序卸载(先 host-bridge 再 host-jobs/host-fanout);新插件必须登记 catalogue 并同步 config 与 internal/embed/seed 两份 bundle 样板;外部化二进制经 scripts/gen-extplugins.sh 重新生成(embed 缺失主包构建失败)。
