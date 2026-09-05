# pi vs gah 对比分析与改进点(2025-10)

> 对比对象:pi coding agent(Node/TS,`@earendil-works/pi-coding-agent`)+ `@earendil-works/pi-tui`;
> gah(Go 微内核 harness,本仓库)。方法:读 pi 官方 docs(tui/usage/keybindings/sessions/
> compaction/settings)与 gah 代码现状逐项对照。结论按"改进点价值 × 成本"给建议等级。
> 记录存放遵循 AGENTS 纪律;本文档作为对比基线,实施项落 DESIGN §14.1 未实施清单。

## 0. 架构哲学差异(决定改进取舍)

| 维度 | pi | gah | 含义 |
|---|---|---|---|
| 内核 | 薄壳 + TS 扩展/包生态(工作流全走扩展,内置 MCP/subagent/权限弹层/plan/todo/后台 bash **故意不做**) | Go 微内核 + 插件仓库**内置**工作流(host-jobs/fanout/todo/memory/auto-plan 已交付) | gah 已覆盖 pi 明说"不做"的多项;改进重心不在补功能而在**体验与生态** |
| 会话模型 | **树状分支**(/tree//fork//clone+分支摘要) | 线性 + 多会话切换(/session switch) | 分支/摘要为 pi 独有强项 |
| 上下文管理 | 多级压缩(auto compact + 手动 /compact 自定义)+ 分支摘要 | token-compress 滚动摘要 + usage-stats | 方向一致,gah 缺手动压缩与分支摘要 |
| 主题 | 完整主题系统 + 运行时 /settings | 硬编码 lipgloss 色 | gah 无主题化 |
| 扩展 UI | `ctx.ui.custom` 组件系统(overlay/widget/footer/editor 可替换) | 无(TUI 内建,sdk 无 UI seam) | **最大能力差距**,直接堵死生态型 UI |
| 端模式 | regular(主屏 scrollback,默认)+ fullscreen(实验) | 仅 alt-screen(经评估维持) | 已决策方案 A,不追 |

## 1. TUI 界面改进点(按价值排序)

### 1.1 高价值(建议做)

| # | 改进点 | pi 参照 | gah 现状 | 建议 |
|---|---|---|---|---|
| T1 | **消息队列(steering/follow-up)** | Enter 排队转向消息(当前工具完成后送达);Alt+Enter 排队后续消息(整轮后);Esc 恢复队列;Alt+Up 取回队列 | 回合中 Enter 无排队语义(Esc 只能取消) | 高:不打断 agent 又可控,交互质变。成本 M。注意与 TUI 输入互斥/取消逻辑集成 |
| T2 | **输入框增强补齐** | `@` 文件 fuzzy 引用;Tab 路径补全;Shift+Enter 多行;Ctrl+G 外部编辑器;图片粘贴 | 单行输入,无 @ 引用/路径补全/外部编辑器 | 中高:`@` 引用+Tab 补全为日频;多行输入次之。分片 T2a(@/补全)T2b(多行+外部编辑器) |
| T3 | **工具调用展示增强** | 工具标题/输出分色、可折叠/展开(工具状态色),renderCall/renderResult 渲染钩子 | 已折叠去重 + md 轻渲染;结果行仅 tool 色 | 中:工具输出区(状态图标+标题行+结果可展开)已具雏形;补"失败色块/diff 染色"即可达"一眼状态" |
| T4 | **md 渲染补全** | 完整 Markdown 组件(标题/列表/引用/hr/代码块边框+语法高亮/链接) | 轻渲染:粗体/行内 code/标题/列表(无代码块高亮/链接/引用) | 中:**代码块语法高亮**最常见长文本,最值得补;链接 OSC8 可后置 |

### 1.2 中价值(视迭代节奏)

| # | 改进点 | 说明 | 成本 |
|---|---|---|---|
| T5 | 自定义 footer/widget 槽位 | pi 扩展可置 UI;gah 可做"内建 widget 区(todo 面板/进度)在输入区上"而不做开放 seam | S–M |
| T6 | 消息分组视觉(轮次内 user/assistant 边界色) | 部分已交付(turnDivider);可加"assistant 块左侧竖线/缩进"增强结构 | S |
| T7 | 图片/富媒体 | pi fullscreen Kitty 协议内联图(regular 走 iTerm2) | gah 已记录"远期不做";维持 |
| T8 | 快捷键可配置化 | pi 全量 keybindings.json | gah 快捷键硬编码;低优先(用户面小) |

### 1.3 明确不做(维持决策)

- S3.1 主屏 scrollback 双形态(已决策方案 A,理由与备选见 TUI_OPTIMIZE S3)。

## 2. 会话与上下文改进点

### 2.1 高价值(建议做)

| # | 改进点 | pi 参照 | gah 现状 | 建议 |
|---|---|---|---|---|
| C1 | **会话树/分支** | /tree 跳到任意点续聊;/fork 从旧消息派生新会话;/clone 复制当前分支;分支摘要 | 多会话平铺(switch 列表) | 高:复杂任务"走错回溯/多方案并行"核心能力。gah jsonl 会话日志天然可支 fork/树索引。成本 L(会话模型+UI)。建议拆 T1 树导航(TUI)、T2 派生语义 |
| C2 | **手动 /compact [prompt]** | 超限自动压缩 + 用户主动按需压缩(带自定义指示) | 仅超限自动压缩(token-compress) | 高但不急:自动压缩已覆盖主路径;手动触发为省 token 主动行为。成本 M |
| C3 | 会话命名(/name)与标题 | 每会话 display name | 会话 id=时间戳 | 低值低成本;切会话可读性提升。成本 S |

### 2.2 中价值

| # | 改进点 | 说明 | 成本 |
|---|---|---|---|
| C4 | 分支摘要(弃支一句话总结) | /tree 树 UI 上标注;gah 线性日志无弃支概念(与 C1 绑定) | 随 C1 |
| C5 | 会话导入/导出扩展 | pi /export HTML、/import JSONL、/share gist;gah 已有 /export jsonl(见 tui/app.go),可补 HTML/回读 | M |
| C6 | 会话上下文文件加载(父目录 AGENTS.md 逐级) | pi 从 cwd 向上逐级加载 + override;gah 仅当前目录 AGENTS.md + 全局 | M(工程:S2.2 后 host-system-prompt 装配器) |

## 3. 工程/架构/生态改进点

### 3.1 生态与扩展性(结构性差距,决定项目天花板)

| # | 改进点 | pi 参照 | gah 现状 | 建议 |
|---|---|---|---|---|
| E1 | **UI 扩展 seam(ctx.ui)** | ctx.ui.custom/overlay/widget/setStatus/setWorkingIndicator/footer 替换 | sdk 无 UI 注入点;TUI 状态与渲染全部内建 | **最大缺口**。gah 的 M7 Web UI 若按 M7.2 槽位化,可先于 TUI 提供"槽位注册表";TUI 内做插件化 UI 成本高。建议:Web 侧先落地,M7.2 槽位 registry 亦覆盖未来 TUI |
| E2 | 主题系统 | 语义色 token + /settings 切主题 + 主题文件加载 | 硬编码 256 色 | 中:**语义色 token 抽离**(success/error/tool/md 类)成本 S–M,为换肤与深/浅色兜底打基础;主题文件/reload 后置 |
| E3 | `/settings` 运行时设置 UI | SettingsList 组件 | 配置走 yaml 文件(无 UI) | 低优先:yaml+`--dump-config` 已可;设置 UI 依赖 E2/组件化 |

### 3.2 中价值工程改进

| # | 改进点 | 说明 | 成本 |
|---|---|---|---|
| E4 | 运行时热配置 | pi /reload 应用 keybinding/主题/扩展变更 | gah 外部插件热重载已有(host-bridge watch);缺配置树热更(重启才生效)。成本 M |
| E5 | 错误/状态提示 | pi loader + 通知(成功/错误 toast);gah 有 spinner/error banner | 中:补"操作成功通知"与运行时长展示。成本 S–M |
| E6 | 成本/用量显示增强 | pi footer 显 cost/token/cache/model 总计 | gah 已有上下文/缓存;补 cost 估算需 provider 计价表。低优先 |

## 4. 与 gah 现有规划的承接关系

| 改进点 | 承接 |
|---|---|
| C1 会话树/分支 | 依赖 M7.2 之后 UI;或独立 TUI 树视图(新工作) |
| T2a @ 引用/Tab 补全 | 独立小工程(需文件索引,可复用 tool-files/sandbox 路径) |
| T4 代码块高亮 | session.go/markdown.go 增量(轻渲染层加 fence 解析+着色) |
| E1 UI seam | 与 M7/M7.2 强绑定:Web 端 registry 先行,TUI 后续接同一"槽位"概念 |
| C6 多级 AGENTS.md | host-system-prompt 装配器扩展(不涉 core) |

## 5. 优先级建议

- **第一梯队(用户可感、中等成本、不依赖 M7)**:T1 消息队列、T2a `@`/Tab 补全、T4 代码块高亮、C3 会话命名、C6 多级上下文文件。
- **第二梯队(结构/生态)**:C1 会话树(大,可与 M7 后 UI 结合)、E1 Web UI seam 先行、C2 手动压缩。
- **已决策不做/远期**:S3.1 scrollback 双形态(方案 A);图片富媒体(远期);T8 全量键位自定义。

> 备注:pi 明示不内置的 MCP/subagent/权限/plan/todo/后台 bash,gah 均已作为插件交付,属差异化优势,无需反向补齐。对比结论:**gah 的缺口集中在"交互体验细节"与"会话拓扑/UI 生态",而非 Agent 能力**。
