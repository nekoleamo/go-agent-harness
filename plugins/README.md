# plugins/ 插件目录总览

一切能力皆插件:每个插件一个独立包(仅 import `sdk/`),经 `catalogue/` 单一事实源登记,装配层按配置树 enabled 启停。分组仅为可读性整理,不改包名/接口/装配语义。

```
plugins/
├── catalogue/   # 汇总事实源:每个插件的工厂 + Manifest(provides/requires)+ bundle 归属
├── host/        # 宿主级服务(Agent 能力宿主,内置)
├── adapter/     # LLM 提供商适配器
├── policy/      # 策略(沙箱/审批)
├── tool/        # 内置工具(注意:已在 M6.9 外部化为 extplugins/,默认 enabled:false)
├── mcp/         # MCP 桥/服务端
└── ui/          # 界面
```

| 类别 | 插件 | 职责 | 依赖(requires) |
|---|---|---|---|
| host | host-session-log | 会话日志:追加式事件流 + 模型历史投影(ctx.sessions) | — |
| host | token-compress | 滚动摘要压缩(超预算回调压缩器) | ctx.sessions |
| host | host-tools | 工具注册表(ctx.tools) | — |
| host | host-system-prompt | 系统提示组装(ctx.systemPrompt) | — |
| host | host-llm | LLM 注册表 + 路由 + 重试(ctx.llm) | — |
| host | host-commands | 斜杠命令注册表(ctx.commands) | — |
| host | host-agent-loop | 默认 ReAct 回合循环(ctx.agentLoop) | ctx.sessions/ctx.llm/ctx.tools/ctx.systemPrompt |
| host | host-cwd-sessions | 项目级会话隔离 + 多会话切换(ctx.cwdSessions) | ctx.sessions |
| host | host-session-summary | 会话概述(F 组 F3):ctx.sessionSummary——LLM 生成「标题+一句话+主题词」,落 meta.json 缓存;回合后自动生成(默认开) + 全局单飞 + 每会话节流 | ctx.cwdSessions(;ctx.llm 可选) |
| host | host-usage-stats | 会话 token 统计 + 模型窗口解析(ctx.usageStats) | — |
| host | host-skills | 技能机制(SKILL.md 扫描) | ctx.tools/ctx.systemPrompt |
| host | host-jobs | 后台任务(ctx.jobs) | ctx.tools |
| host | host-fanout | 子代理编排(ctx.fanout) | ctx.llm/ctx.tools/ctx.systemPrompt |
| host | host-plugin-manager | 运行期插拔(ctx.pluginManager) | — |
| host | host-bridge | 外部插件桥(加载 home/plugins 外部进程,GAH_CB_ADDR 回调) | ctx.tools/ctx.jobs/ctx.fanout |
| host | host-backup | 整体备份/恢复(M18):ctx.backup + /backup 命令;备份目录 $GAH_HOME/backups(排除自身) | — |
| host | host-docview | 文档预览(D 组):ctx.doc(sdk.DocService)——统一块模型 + 解析器/预算/缓存/格式探测 + markdown/CSV/notebook/PDF 抽取器;四端(TUI pager/Web 工作台/`gah doc`/IM)同源渲染;`/preview` 命令发 `doc/open` 事件 | ctx.commands;ctx.sandbox(可选) |
| adapter | llm-openai-compat | OpenAI 兼容适配器(SSE + usage/缓存解析) | ctx.llm |
| adapter | llm-anthropic-compat | Anthropic 适配器(claude-* 前缀路由) | ctx.llm |
| adapter | llm-mock | 假适配器(dev/CI 无外网) | ctx.llm |
| policy | policy-sandbox | 沙箱三档(ctx.sandbox) | — |
| policy | policy-approval | 危险操作审批三档(M17):open 放行 / smart 弹确认(默认)/ strict 直接拒绝;ctx.approval 运行期切档,偏好持久化 | — |
| tool | tool-shell / tool-files / tool-web / tool-workflow / tool-memory / tool-todo | 工具实现(默认关闭,已外部化);tool-web 含 web_fetch/web_search(M6.14,默认 Exa,data.provider 可换);tool-memory 含 memory(M10,remember/list/recall/forget);tool-todo 含 todo(M8,4 状态机 + blockedBy;T2 面板联动经 web /api/todo 演示插件 todo-panel) | ctx.tools(workflow 另需 ctx.fanout) |
| tool | tool-doc | 文档阅读(D 组 D5):read_document(行号化+预算分页)/doc_open(发 `doc/open` 三端弹预览)/doc_list;经 ctx.doc 统一 resolver | ctx.tools + ctx.doc |
| tool | tool-auto-plan | 规划模式(M11):auto_plan(create/get/list/step/confirm/complete)+ 规则注入(enable_rule,进程内装配同 host-skills,需 ctx.systemPrompt);M11-T2 联动:确认后执行期由 todo 承接(检查清单转 todo.create,执行完回 complete 归档) | ctx.tools + ctx.systemPrompt |
| tool | tool-subagent | 子代理委派(M9.1+9.2):subagent delegate/spawn/agents/agent_status/agent_kill;extplugins/tool-subagent 独立进程(回调宿主 ctx.fanout);spawn=后台带句柄不阻塞 | ctx.tools + ctx.fanout |
| mcp | mcp-bridge / mcp-server | MCP 客户端桥 / MCP server 端 | ctx.tools |
| ui | ui-tui-app | TUI 挂载 | ctx.agentLoop/ctx.llm |
| ui | ui-web-app | Web UI 挂载(M7):SSE 下行 + REST 上行;会话/状态栏/命令/审批/会话切换;addr 默认 127.0.0.1:2233,auth_token 可选,static_dir 开发态(HMR);与 tui bundle 互斥 | ctx.agentLoop/ctx.sessions/ctx.llm + ctx.confirm(提供) |

## 维护约定

- **新增插件**:在对应类别下建包 → `catalogue/` 登记(provides/requires/bundle)→ config/ 与 internal/embed/seed/ 两份 bundle 样板同步(新增条目 bump seed-version)→ 插件只 import `sdk/`(红线:不 import core/tui/其它插件包)。
- **管理域声明**:外部化/场景专用插件在 `catalogue/` 登记时同步声明 `Manage`(external=已外部化界面勿启停 / scenario=场景专用勿启;空=常规),web/TUI 展示层透传该声明 — **勿在 web/server.go 等展示层新增硬编码名单**(2026 已清理,声明为单一事实源)。
- **外部化目标**(M6.9/P4):工具类保持外部二进制(`extplugins/` + embed 按平台拆包),内置二进制只承载 host/adapter/policy/ui。分组不影响外部化:路径变化随映射同步,编译期兜底。
- **窗口表**维护:新模型上下文窗口见 `host/host-usage-stats/modelwindows.go`(host/adapter 类插件展示时 manage 由运行态派生为 host,无需声明)。
