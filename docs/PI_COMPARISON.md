# gah 对比基线:pi 与 dsh(体验基线 + 架构来源)

> 对比对象与分工:
> - **[pi coding agent](https://www.npmjs.com/package/@earendil-works/pi-coding-agent)**(TS 终端 harness;含 `pi-tui`)——**交互/体验/会话拓扑基线**(2025-10 起)。读 pi 官方 docs 与 gah 逐项对照,改进点按"价值 × 成本"评级,实施落 DESIGN §14.1。
> - **[DeepSeek Harness (dsh)](https://github.com/deepseek-ai/deepseek-harness)**(TS/Cordis,一切皆插件)——**架构哲学来源**(本项目「一切皆插件 / 微内核 / 配置层」直接对齐,见 DESIGN 引言);同时是 web-first workbench 与权限/子代理语义的对照。developer preview 阶段。
> - **gah**(Go 微内核 harness,本仓库):单二进制交付,内置 pi 明示不做、dsh 经插件组合的能力交集。
> 结论原则:交互缺口补向 pi;架构/能力语义对齐 dsh;差异作为差异化优势,不反向补齐。

---

# Part A · pi vs gah(交互体验基线)

## A0. 架构哲学差异(决定交互改进取舍)

| 维度 | pi | gah | 含义 |
|---|---|---|---|
| 内核 | 薄壳 + TS 扩展/包生态(工作流全走扩展,内置 MCP/subagent/权限弹层/plan/todo/后台 bash **故意不做**) | Go 微内核 + 插件仓库**内置**工作流(host-jobs/fanout/todo/memory/auto-plan 已交付) | gah 已覆盖 pi 明说"不做"的多项;改进重心不在补功能而在**体验与生态** |
| 会话模型 | **树状分支**(/tree//fork//clone+分支摘要) | 线性 + 多会话切换 + 分支树(/fork /clone /tree 已交付) | 分支能力已对齐;pi 仍有分支摘要/树 UI 差异 |
| 上下文管理 | 多级压缩(auto compact + 手动 /compact 自定义)+ 分支摘要 | token-compress 滚动摘要 + usage-stats + 手动 /compact(已交付) | 方向已对齐;差异在压缩策略细项 |
| 主题 | 完整主题系统 + 运行时 /settings | **主题外部化已交付**(`$GAH_HOME/config/themes/` + `/theme`,M13) | 已对齐(此行为 2025-10 后迭代,更新状态) |
| 扩展 UI | `ctx.ui.custom` 组件系统(overlay/widget/footer/editor 可替换) | TUI 内建 + widget 槽位(输入区,/widgets);Web 侧 M7.2 槽位插件化 | 终端 UI seam 仍窄;Web 槽位已先行 |
| 端模式 | regular(主屏 scrollback,默认)+ fullscreen(实验) | 仅 alt-screen(经评估维持) | 已决策方案 A,不追 |

> A 部分各行状态随 gah 迭代刷新;以下改进点中已交付者标 ✅(2025-10 基线后落地)。

## A1. TUI 界面改进点(按价值排序)

### A1.1 高价值(多数已交付)

| # | 改进点 | pi 参照 | gah 现状 | 状态 |
|---|---|---|---|---|
| T1 | 消息队列(steering/follow-up) | Enter 排队转向;Alt+Enter 排队整轮后;Esc 恢复;Alt+Up 取回 | 回合中 Enter 无排队(Esc 取消) | ✅ P4-1 已交付(消息队列) |
| T2 | 输入框增强 | `@` 文件引用;Tab 补全;Shift+Enter 多行;Ctrl+G 外部编辑器 | — | ✅ P4-2(@+Tab 补全)/ P4-6(多行+外部编辑器)已交付 |
| T3 | 工具调用展示增强 | 工具标题/输出分色、折叠、状态色 | 折叠去重 + md 轻渲染 + 工具行分组 | ✅ P4-7/S2.1 已交付 |
| T4 | md 渲染补全 | 完整 Markdown 组件(高亮/链接/引用…) | 轻渲染 | ✅ P4-3 代码块高亮已交付 |

### A1.2 中价值 / 决策

| # | 改进点 | 说明 | 状态 |
|---|---|---|---|
| T5 | widget 槽位 | 输入区上方宿主注册信息行 | ✅ P4-12 已交付(/widgets + TokWidget) |
| T6 | 消息分组视觉 | 轮次内边界色/竖线 | ✅ S2.1/turnDivider 已交付 |
| T7 | 图片/富媒体(终端内联) | pi fullscreen Kitty 协议 | 不做(终端内联远期;Web 附件已承载多模态) |
| T8 | 快捷键可配置化 | pi 全量 keybindings.json | 低优先(键位硬编码,用户面小) |
| S3.1 | 主屏 scrollback 双形态 | pi regular 模式 | 已决策方案 A 维持(TUI_OPTIMIZE S3) |

## A2. 会话与上下文改进点

| # | 改进点 | 状态 |
|---|---|---|
| C1 | 会话树/分支(/tree /fork /clone + 命名) | ✅ P4-10 已交付(任意点派生/克隆/树视图/自动命名) |
| C2 | 手动 /compact(带自定义指示) | ✅ P4-8 已交付 |
| C3 | 会话命名 /name + 显示名 | ✅ 已交付(/name;状态栏/列表名优先) |
| C4 | 分支摘要(弃支一句话总结) | 未做(树 UI 摘要标注;成本随树完善) |
| C5 | 会话导出扩展(HTML/回读) | ✅ /export jsonl + HTML 已交付(P5.4) |
| C6 | 多级上下文文件(父目录 AGENTS.md 逐级) | ✅ P4-5 已交付(根→近覆盖 + override) |

## A3. 工程/生态改进点

| # | 改进点 | 状态 |
|---|---|---|
| E1 | UI 扩展 seam | ✅ Web 侧 M7.2 槽位插件化(registry v2 三扩展点);TUI widget 槽位(P4-12)部分覆盖 |
| E2 | 主题系统(语义 token + 文件 + /theme) | ✅ M13 主题外部化 + P4-9 token 化(2026-09) |
| E3 | /settings 运行时设置 UI | ✅ Web 设置面板 + TUI /settings(history 等) |
| E4 | 运行时热配置 | ✅ P4-11 /reload 指令热更;配置层热更仍重启生效 |
| E5 | 错误/状态提示 | ✅ 已有 error banner + 状态栏;toast 语义未做 |
| E6 | cost/用量显示 | 部分(上下文/缓存使用率已显;cost 计价表未做,低优先) |

> **A 部分结论(更新)**:2025-10 基线的高/中价值改进点**绝大部分已交付**(P4 12 项 + P5.x);pi 对比现在主要指向**剩余细项**(分支摘要、toast、配置热更、键位自定义)与终端 UI 生态,不再是能力缺口。

---

# Part B · dsh vs gah(架构与能力)

## B0. 架构哲学对照

| 维度 | dsh | gah | 含义 |
|---|---|---|---|
| 内核 | TS/Cordis 插件框架:核心只保留 **AgentLoop + ctx seam**;"一切皆插件"(模型适配/工具/沙箱/会话/**agent loop 本身**均可换) | Go 微内核:core/ 零业务(ctx/event/plugin/config);能力全为插件(bundle 按序装配) | **同源哲学**:gah 直接继承 dsh 设计(见 DESIGN 引言/§4);差异在语言与分发 |
| 分发 | npm/Node(`npx @deepseek-ai/dsh`;Node ≥ 版本 + 依赖树) | 单一静态二进制(CGO=0,~37MB),零运行时依赖 | **gah 定位出发点**:消除 dsh 的 Node 依赖树(DEISGN 引言);这是差异而非缺口 |
| 形态 | **web-first workbench**(`dsh web` 127.0.0.1:3080)+ headless 一次性(`dsh --profile headless <task>`) | TUI + Web(`gah web` 2233)+ headless 同源 | gah 多一个 TUI;web workbench 形态对齐 |
| 插件模型 | TS 模块 `apply` + `inject` 依赖声明;`dsh plugin --profile add <source>`;热重载/卸载清理 | Go sdk.Plugin(Start→Disposer)+ ctx 服务容器注入;catalogue 登记;外部进程/UI 槽位两类 | gah 插件协议为 Go;UI 插件(v2 槽位)与 dsh "interface components 插件"思路同源 |
| 现状 | **Developer Preview**(生态演进中) | 已交付 M1–P5.7,全库测试绿 | — |

## B1. 能力对照

| 能力 | dsh | gah | 差异/注 |
|---|---|---|---|
| 工具 | tool-fs / tool-bash / session_search + 插件扩展 | shell / file_* / web_fetch / web_search / workflow / subagent / todo / memory / auto_plan / job_* / mcp_* | gah 工具面更宽;dsh `session_search`(会话检索工具)在 gah 侧为 TUI `/search`,**工具面无 session_search——改进点候选** |
| 子代理/工作流 | 子代理并行分工(边界 pin 审批);workflow | fanout agent/parallel/pipeline + tool-subagent(spawn/send_message/fork)+ starlark workflow | 语义对齐(独立上下文、父级仅结论);gah 无 workflow(编排)缺?有 tool-workflow(starlark)。dsh 无对应 starlark?差异:gah 有受限脚本 |
| 审批/权限 | **fail-closed**:ask 无审批通道自动拒绝 + 审计事件对;delegation 边界 **pin** 策略(如 approval: never) | 审批三档 open/smart/strict(fail-closed:smart 无确认通道安全拒绝)+ 沙箱三档 + 凭据隔离 | fail-closed 语义一致;gah **未实现 delegation 边界 pin**(子代理继承/自身审批语义)——改进点候选 |
| 会话/上下文 | ~/.dsh/sessions;独立进程会话/权限/沙箱 | $GAH_HOME(便携 gah-data)/sessions + 分支树 + token 压缩 + 多 provider | 同域;gah 增加便携单根与分支 |
| 模型 | 多模型路由 | 多 provider 并存(/provider)+ 前缀路由 | 对齐 |
| UI 扩展 | 插件可扩展 **UI 面板**(web workbench) | M7.2 UI 槽位插件化(Web)+ TUI widget | 思路同源,已落地 |
| 配置 | profile/bundle 层 + `--dump-config` | profile→bundle→patch + `--dump-config`(对齐) | 直接对齐(DESIGN §3) |

## B2. dsh→gah 已采纳的设计对齐点(清单)

- 「一切皆插件」微内核(核心零业务,AgentLoop 可替换插件)
- 配置层 profile/bundle(+patch)+ `--dump-config` 任一条目可 patch
- 事件 veto 语义(pre-step / pre-execute 扩展点)→ tools/pre-execute 等
- ctx seam 依赖注入(ctx.llm / ctx.tools / ctx.jobs / ctx.sessions…)与插件卸载即撤销
- LLM streaming 域词汇(Message/Chunk/Stream)与 `ctx.llm` seam
- 工具 schema MCP 兼容(name/description/inputSchema)
- 沙箱三档与审批 fail-closed 语义
- 本机 web workbench 形态与 headless one-shot profile
- Web 输入一体外壳/设计语言(DeepSeek Harness 观感,见 AGENTS UI 规范)

## B3. 差异与待评估(不自动反向补齐)

| 差异点 | 说明 | 评估 |
|---|---|---|
| `session_search` 工具面 | dsh 有会话检索工具;gah 只有 TUI /search 与 memory | 中:模型可检索历史会话有独立价值;成本 M(跨会话索引) |
| delegation 边界审批 pin | dsh 子代理在委派边界 pin 审批策略(不继承父权限) | 低中:gah 子代理独立 ctx;审批继承语义需明确定义;成本 S–M |
| UI 面板扩展(web workbench) | dsh 插件可直接扩展 web 面板 | gah 已 M7.2 槽位覆盖(设置区段/侧栏动作/附加面板);余量小 |
| Node 分发与 Cordis 生态 | dsh 可复用 Cordis/JS 插件生态 | gah 刻意排除 Node(硬红线);生态走 Go 插件 + 外部进程,MCP 兼容互操作 |
| developer preview 稳定性 | dsh 仍 preview,功能/API 演进快 | 以 DESIGN 已对齐的稳定 seam 为准,不追 API 变动 |

## B4. dsh 部分结论

gah 与 dsh **同源同哲学**(Go 重写 Cordis 式微内核),核心架构/配置/事件/审批/沙箱语义已对齐;**结构性差异 = 分发形态(单二进制 vs Node)+ 界面矩阵(TUI+Web vs Web-first)+ 语言生态(Go 插件 vs TS 插件)**,均属有意选择而非缺口。可评估补齐项集中在 B3 两处(B 部分随迭代刷新)。

---

# Part C · 优先级建议(合并)

- **pi 线剩余(细项/生态)**:C4 分支摘要、E5 toast 提示、E4 配置热更、T8 键位自定义(均低成本,按需)。
- **dsh 线(能力语义)**:B3-1 session_search 工具面(中)、B3-2 delegation 审批 pin(低中)。
- **已决策不做/远期**:pi S3.1 scrollback 双形态;T7 终端内联图片;dsh Node 分发/TS 插件生态(硬红线)。

> 总体结论:**交互缺口补向 pi(已基本收敛),架构语义对齐 dsh(已收敛),能力层 gah 已超出两者交集**;后续改进重心 = 会话拓扑细节 + 终端 UI 生态 + dsh 两处可评估能力,不再是大能力缺口。
