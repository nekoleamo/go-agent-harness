# gah 未完成功能实施排期

> 覆盖:DESIGN §14.1 未实施清单(M7–M11)+ `docs/TUI_OPTIMIZE.md`(TUI 优化 S1–S3)。
> 排期原则:**依赖约束 > 用户价值 > 风险/成本**;TUI 与宿主工具两类可并行推进(两者无强依赖,
> 仅 M9.2/8-T2 等少量交叉);每项独立验收(单测 -race + kitty 真机 + 全库 -race)。
> 工作量:XS <1d,S 1–3d,M 3–8d,L 2–4周,XL 1–3月。标注 ⚠️ 依赖前项。

## 总览

| 阶段 | 窗口 | 内容 | 里程碑判定 |
|---|---|---|---|
| P0 近期 ✅ | 已完成 | TUI S1.5/S1.1/S1.2/S1.3 + M8-T1 + M10 + 6a /workspace | ✅ 全部交付:划选/搜索/滚动条/输入增强;todo/memory 工具;工作区切换 |
| P1 中期 ✅(已完成) | 已完成 | TUI S1.4/S2.1 + M9.1 + M11-T1 | ✅ 已交付:Markdown 轻渲染(S1.4);消息分组/工具行去重(S2.1);subagent one-shot 委派(M9.1);auto-plan 规划+确认门(M11-T1) |
| P2 后期 ✅(已完成/决策) | 已完成 | S3.2(框架)+ M9.2(后台子集)+ M11-T2 + S2.2 | ✅ M9.2 后台委派;M11-T2 plan→todo;S2.2 渲染四层;S3.2 框架覆盖;S3.1 决策=方案 A 维持现状(记录在案) |
| P3 远期(季度+) | 独立里程碑 | M7(M7.1 Web UI)+ M7.2 槽位 + M7.3 WS + M8-T2(展示联动) | Web UI 全能力闭环;槽位插件化;todo 面板 |
| P4 体验改进(对比 pi 基线,见 docs/PI_COMPARISON.md) | 独立迭代 | T1 消息队列 · T2a @引用+Tab 补全 · **T4 代码块高亮 ✅** · **C3 会话命名 ✅** · **T3 工具视觉 ✅** · **E2 语义色 ✅** · C6 多级上下文文件;T2b/T3/C2 次之;会话树 C1 与 UI seam E1 绑 M7.2 后 | 交互体验对齐 pi;Agent 能力面无缺口 |

## P0 近期(1–2 周:优先用户痛点、低风险、依赖已就绪)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 1 ✅ | **TUI S1.5 鼠标划选复制**(OSC52,已交付) | 物理行/滚动条几何已就绪 | S | model.go 选区状态+鼠标分支+OSC52;render.go 反色高亮;单测 | kitty 拖选→高亮→释放进剪贴板;与滚动条拖动互不干扰 |
| 2 ✅ | **TUI S1.2 滚动条完善**(回底/hover/auto-hide,已交付) | 滚动条可拖已就绪 | S | offset>0 显示"↓回底"点击=0;悬停高亮;静止~1.5s 淡出 | 浏览历史可一键回底;静止后滚动条隐藏,操作重现 |
| 3 ✅ | **TUI S1.1 会话内搜索**(已交付) | 物理行展平已就绪 | M | /search+高亮+循环跳转(n/N)+命中计数;offset 定位命中行 | 长会话循环定位;Esc 恢复 |
| 4 ✅ | **TUI S1.3 输入增强** | 输入框已有 | M | 已交付:历史 Ctrl+P/N(源=会话 user+已提交命令,含草稿还原/去重);undo Ctrl+Z / redo Ctrl+Shift+Z(相邻同型 800ms 合并,提交/清空清栈);Ctrl+K 删至行尾、Ctrl+U 删至行首;Alt+←/→ 按词移动(右=下一词首)。实现:state.go(histRev/histCur + undo/redo 栈 + kill/word 状态机)+ model.go(组合键分派)+ 单测 input_edit_test.go/update_test | 状态机单测 + 真机可用 |
| 5 ✅ | **M8 tool-todo T1**(协议与存储) | 独立;与 M7 无依赖 | M | 已交付:plugins/tool/tool-todo(todo 8 action;4 状态机 + blockedBy 环/悬空拒绝 + 单 in_progress;jsonl 追加/墓碑/坏行容忍)+ tool-basic 注册 + catalogue/bundle(seed 5→6)+ gen 重跑;sdk.ProjectKey 收口 | 模型可全生命周期管理;坏行容忍;并发锁;-race 绿 |
| 6a ✅ | **工作区快速切换**(`/workspace`,升级:最近使用列表 + 新目录输入) | 独立 TUI 层;需 host-cwd-sessions key 联动 | M | 已交付:两级 Args——一级最近工作区(时间倒序,`workspaces.json` 记录)+ 哨兵「输入新目录」;选历史直接执行/哨兵→断点输入;chdir → SwitchProject key 重绑 + 自动新建空会话 → afterSessionSwitch + 状态栏刷新;同项目仅刷新最近时间 | 选择器选历史即切新会话;输入新目录可行;可经 /session switch 回溯旧项目 |
| 6 ✅ | **M10 tool-memory** | 独立 | S | 已交付:plugins/tool/tool-memory(memory 单工具 remember/list/recall/forget;`$GAH_HOME/memory/<project-key>.jsonl` 追加/坏行容忍/人工可编辑)+ tool-basic 注册 + catalogue/bundle(seed 4→5)+ gen 重跑 | 跨会话 recall;人工可编辑;显式拒绝过度分层 |

> P0 状态:全部交付 ✅(1 S1.5 划选 / 2 S1.2 滚动条 / 3 S1.1 搜索 / 4 S1.3 输入增强 / 5 M8 todo / 6 M10 memory / 6a /workspace);M6.15–M6.21 修复链随 TUI 线交付(细见 DESIGN §14.1);全库 `-race` 绿(36 包,pre-existing pty 首屏失败除外)。

## P1 中期(2–6 周:内容质量、引擎已有承接层)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 7 ✅ | **TUI S1.4 Markdown 轻渲染** | 渲染文本层;建议在 S2.2 前以最小侵入做 | M | ✅ 已交付(tui/markdown.go):粗体/行内 code/标题/列表缩进/分隔线;折行后逐物理行 token 着色;每段独立 SGR(字符无损、宽度不变);assistant 行无搜索/选区时启用 | ✅ 单测 12 项 + 全库 -race 绿 |
| 8 ✅ | **TUI S2.1 消息分组/工具行去重** | 独立 | S | ✅ 已交付:工具行双写去重(EventAssistantMessage 不再铺 ToolCalls 行,仅 EventToolCall+Result 单写)+ 重放跨轮细分隔线(turnDivider);⚙ 折叠可展开交互归 S2.2 组件化后接入 | ✅ 多轮结构可分;工具行不翻倍占屏 |
| 9 ✅ | **TUI S2.2 渲染组件化 + 折叠交互** | — | L | ✅ 已交付:render.go(装配)/session.go(引擎)/chrome.go(装饰)/markdown.go(样式)四层分离;折叠交互(Full 全文 + 单击展开) | ✅ 模块单测;Render 输出与现行为一致 |
| 10 ✅ | **M9 tool-subagent M9.1**(one-shot) | 引擎 host-fanout 已存在 | M | ✅ 已交付:plugins/tool/tool-subagent(subagent delegate)+ extplugins/tool-subagent(GAH_CB_ADDR 回调 fanout.agent)+ gen 矩阵重建 | ✅ 模型可委派子 agent 取回结果(TestExternalSubagent);崩溃隔离 |
| 11 ✅ | **M11 tool-auto-plan T1**(规划输出+确认门) | 独立(与 M8 分工) | M | ✅ 已交付:plugins/tool/tool-auto-plan(auto_plan 6 action + 规则注入 enable_rule,进程内装配同 host-skills,不进 tool-basic) | ✅ 规划落盘跨会话可取;确认前零副作用(规则注入) |

## P2 后期(6 周–3 月:形态演进、高级交互)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 12 ✅ | **TUI S3.2 synchronized output** | 独立小项 | XS | ✅ 已确认由 bubbletea v2 框架自动启用(tea.go 终端能力查询 2026) | ✅ 框架覆盖,无需自实现 |
| 13 ✅ | **M9.2 控制组+背景**(后台子集) | ⚠️ M9.1 | M | ✅ 已交付:后台 delegate 带手柄(spawn/agents/agent_status/agent_kill)——sdk.FanoutService 后台会话 + host-fanout 引擎 + 回调桥 + extplugins;send_message/fork 未实施(需消息循环/事件流种入,记录后续) | ✅ 可后台委派/列表/查状态/终止 |
| 14 ✅ | **M11-T2 plan→todo 承接** | ⚠️ M11-T1 + M8-T1 | M | ✅ 已交付:规则文本/Description 增补联动边界(auto_plan 管确认门,确认后执行期由 todo 承接,检查清单转 todo.create,执行完回归档);TestRuleTodoHandoff 等回归 | ✅ 确认门→todo 承接语义清晰 |
| 15 ⏸️ | **TUI S3.1 主屏 scrollback 模式** | ⚠️ S2.2 组件化 | L | 决策:方案 A 维持现状(暂不实施)。B/C 备选记录在案(docs/TUI_OPTIMIZE.md S3 决策记录),后续再研究 | —(暂缓) |

## P3 远期(季度+:Web 全家桶)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 16 | **M7 Web UI** | — | XL | S1 web/包(server/SSE/静态/槽位骨架)+ui-web-app 插件+全套配置+`gah web` 2233;S2 交互闭环;S3 Seq 断线重放/auth_token | web 上跑通 TUI 同集能力 |
| 17 | **M7.2 UI 槽位插件化** | ⚠️ M7 | L | 加载器+manifest+安装命令;defineAsyncComponent 覆盖槽位 | 第三方 UI 插件可安装/覆盖槽位 |
| 18 | **M7.3 WebSocket** | ⚠️ M7 | M | 通道 seam(SSE 为第一实现,WS 同 payload) | 双通道统一订阅 |
| 19 | **M8-T2 展示联动** | ⚠️ M7.2 | M | todo 面板实时反馈任务推进(web 侧) | 模型任务进展 web 可见 |
| 20 | **M9.3 控制面完整** | ⚠️ M9.2 | L | send_message(子代理消息循环)/ fork(父事件流种入子会话) | 子代理运行中可消息引导/中断;fork 上下文继承 |

## 并行性与约束
- **两条并行线**:TUI 线(1–4→7–9→12/15)与宿主工具线(5/6→10/11→13/14),可在同迭代交替交付。
- **强制依赖链**:15←9←1–8;17/18←16;13←10;14←11+5;19←17+5;P4:C1/P4-10←M7.2 后(或独立树视图),其余 P4 项独立。
- **P4 备注**:P4-1/2/3/4/6/7/8/9/11/12 均不依赖 M7,可在 P3 Web 之外独立迭代;P4-10(会话树)与 P3 相关但可作独立 TUI 树先做。
- **登记同步**:每个新插件(5/6/10/11)都需 catalogue 登记 + config 与 seed 两份 bundle 同步 + seed bump;外部化工具需重跑 scripts/gen-extplugins.sh。
- **回归红线**:始终保持物理行滚动/滚轮节流手势/滚动条拖动/启动新会话/双按退出的不回归单测。

## P4 体验改进(对齐 pi 基线;来源 docs/PI_COMPARISON.md)

> 状态:P4-3(代码块高亮)、P4-4(会话命名)、P4-6(多行输入/外部编辑器)、P4-7(工具视觉增强)、P4-9(语义色 token 化)✅ 已交付,其余 ⏳ 未实施。排期原则:用户可感 > 依赖 M7 > 成本。Agent 能力面无缺口
> (pi 明示不内置的 MCP/subagent/权限/plan/todo/后台 bash,gah 均已交付),本阶段只补交互与会话体验。

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| P4-1 | **T1 消息队列**(转向/后续) | 独立 | M | Enter 排队转向消息(当前工具完成后送达)、Alt+Enter 排队后续(整轮后);Esc 恢复队列到编辑器、Alt+Up 取回队列;与运行中状态/取消互斥 | agent 工作时可提交后续意图;取消/恢复不丢队列;回合串行安全 |
| P4-2 | **T2a @文件引用 + Tab 路径补全** | 独立(sandbox/tool-files 路径面) | M | 编辑器输入 @ → fuzzy 搜项目文件(路径沙箱约束);Tab 补全路径(文件/目录) | @ 唤起候选、选中插入引用路径;Tab 路径补全;路径不越沙箱 |
| P4-3 ✅ | **T4 代码块高亮** | S2.2 引擎就绪 | S–M | ✅ 已交付:render 层展平后 annotateCodeFences 跨行状态标注(仅 assistant 参与切换,工具结果含 ``` 不干扰);围栏行/块内行统一代码色 + 单遍极简语法着色(字符串/注释/关键字,字符无损),块内禁 md token 误伤;折叠/搜索/选区回落路径不变 | ✅ 代码块可读、与正文区分;无 ANSI 泄漏/宽度不变(新增围栏/高亮单测,全库 -race 38 包绿) |
| P4-4 ✅ | **C3 会话命名(/name)** | host-cwd-sessions | S | ✅ 已交付:tui/app.go 注册 /name <名>(- 清除)+ host-cwd-sessions names.json 索引(按会话文件独立持久,坏文件容忍)+ 状态栏/切换选择器/session current 显示名优先 | ✅ 状态栏显示名;重开/切会话名保留;无名称回退 id/主会话;全库 -race 38 包绿(pty 探针 pre-existing 除外) |
| P4-5 | **C6 多级上下文文件加载** | host-system-prompt | M | 从 cwd 逐级向上读 AGENTS.md 并入 project instructions(近者覆盖远者);AGENTS.override.md 语义;不涉 core | 上级目录约定自动生效;层级覆盖正确;有单测 |
| P4-6 ✅ | **T2b 多行输入 + 外部编辑器** | TUI 输入 | S–M | ✅ 已交付:Shift+Enter 插入换行(仅普通输入态;选择器/自由向导仍与 Enter 同语义)、↑/↓ 行间移动(列意图记忆 bash/readline 语义,单行退化原首/尾)、输入区多行渲染(续行对齐、主区自动扣减);Ctrl+G 外部编辑器整段编辑($VISUAL>$EDITOR>nano,tea.ExecProcess 自动 releaseTerminal 交还终端,保存退出回填,undo 一步;命令参数支持如 "code -w");多行普通消息可提交,命令(/ 前缀)保持单行语义——含换行拒绝保留现场提示,纯空白不发起回合;Ctrl+C 清空/双按退出语义不回归 | 多行输入可提交;外部编辑回填;↑↓ 行间光标;Ctrl+C 语义不回归(新增 multiline_test,全库 -race 绿,pre-existing pty 探针除外) |
| P4-7 ✅ | **T3 工具结果视觉增强** | S2.2 就绪 | M | ✅ 已交付:工具三态语义色(调用 ⚙ 琥珀 / 成功 ✓ 绿 styleToolOK / 失败 ✗ error 红);展开结果 diff 轻染色(+++/---/@@ 头灰、+ 行淡绿、- 行暗红,字符无损);搜索命中/选区回落基础样式 | ✅ 工具执行状态一眼可分;失败醒目;diff 染色可读;单测 toolrow_test + 全库 -race 38 包绿(pty 探针 pre-existing 除外) |
| P4-8 | **C2 手动 /compact [prompt]** | token-compress | M | /compact 主动触发压缩(带可选自定义指示);完成后回显摘要;超限自动压缩不变 | 手动压缩即时生效;摘要可读;不重复压缩空会话 |
| P4-9 ✅ | **E2 语义色 token 化** | S2.2 | S–M | ✅ 已交付:tui/palette.go 新增 Token+DefaultPalette(21 token)+ fg() 唯一取色入口;render.go/markdown.go/session.go 全部色值字面量收口为 token 派生(零裸色值);palette_test.go 基线守卫(逐 token 对原 256 索引 + 无空色值) | ✅ 渲染逐字等价(全 tui 单测 SGR 精确断言绿);换肤仅改 DefaultPalette 本表 |
| P4-10 | **C1 会话树/分支** | ⚠️ M7.2 后(或独立 TUI 树) | L | /tree 跳任意点续聊;/fork 从旧消息派生;/clone 复制当前分支;分支摘要;基于 sessionlog 树索引 | 回退/多方案并行;树导航 UI;弃支可摘要 |
| P4-11 | **E4 配置热更(/reload 等效)** | 独立 | M | 运行期重载 keybinding/主题/上下文文件/命令(外部插件热重载已有) | 免重启生效;错误回滚 |
| P4-12 | **T5 输入区 widget 槽位** | S2.2 | S–M | 输入行上方 widget 区(如 todo 进展/子代理活动),宿主可注册 | widget 展示;不挤输入;可开关 |

> **明确不做/远期**(已决策):S3.1 scrollback 双形态(方案 A)、图片富媒体、全量键位自定义。
> **绑 M7**:C1 会话树 UI、E1 UI 扩展 seam(Web 侧 registry 先行,M7.2 槽位概念 TUI 后接)。

## 变更记录
- 2026-09-06:P4-6 T2b 多行输入 + 外部编辑器交付(Shift+Enter 换行/↑↓ 行间移动列意图/输入区多行渲染;Ctrl+G $VISUAL>$EDITOR>nano 整段编辑,ExecProcess 自动 releaseTerminal 保存回填 undo 一步;命令单行语义含换行拒绝保留现场、纯空白不发起回合);剩余 P4 7 项。
- 2026-09-06:P4-4 C3 会话命名交付(/name <名> 设置、- 清除;host-cwd-sessions names.json 按会话文件独立持久、坏文件容忍;状态栏/切换选择器//session current 显示名优先,无名称回退 id);P4 首个交付项,剩余 11 项。
- 2026-09-06:P4-9 E2 语义色 token 化交付(tui/palette.go:21 语义 token + DefaultPalette 单一事实源 + fg() 唯一入口;render/markdown/session 色值字面量清零;渲染逐字等价,换肤只改表);剩余 P4 10 项。
- 2026-09-06:P4-3 T4 代码块高亮交付(render 层 annotateCodeFences 跨行围栏标注(仅 assistant 翻转,工具结果 ``` 不干扰)+ 块内统一代码色与单遍极简语法着色(字符串/注释/关键字)、块内禁 md token;palette 增 md-key/md-str/md-cmt 三 token;围栏/高亮/字符无损单测);剩余 P4 9 项。
- 2026-09-06:P4-7 T3 工具结果视觉增强交付(工具三态语义色:调用 ⚙ 琥珀 / 成功 ✓ 绿 / 失败 ✗ 红;展开结果 diff 轻染色 +行绿/-行红/头灰,搜索命中与选区回落基础样式;palette 增 tool-ok/diff-add/diff-del/diff-hdr 四 token;toolrow_test 单测);剩余 P4 8 项。
- 2026-09-06:TUI 选择/提示列表滚动窗口修复(选项超 6 行截断丢弃 → pickWindow 随光标滚动展示剩余项,上/下箭头双向滚动;静态 / 命令提示超窗改余量提示;见 docs/TUI_OPTIMIZE.md §1.7)。
- 2026-09-06:TUI 选择器内过滤(参数级直接打字=子串即时过滤 Value/Desc,退格/Esc 恢复,无匹配回车不提交;命令名前缀过滤不变;见 docs/TUI_OPTIMIZE.md §1.8)。
- 2026-09-06:TUI 多值自由参数逐步向导(框架级 A:FreeArgs 多参数=逐级 Enter 输入,尾 '?' 可选可跳过,旧单参/一次多词语义兼容;provider set 即获逐级体验;见 docs/TUI_OPTIMIZE.md §1.9)。
- 2026-09-06:TUI 状态栏模型来源标注(选中/启动后显示 模型: id(来源缩写如 siliconflow),随 provider set/unset/clear、/model 与启动经 syncDisplay 刷新,未配置不显示)。
- 2026-09-05:初版(覆盖 M7–M11 与 TUI S1–S3,依据 DESIGN §14.1 切片与 docs/TUI_OPTIMIZE.md)。
- 2025-10:P4 体验改进排期新增(来源 docs/PI_COMPARISON.md;P0–P2 已交付,P3 M7 待排;见总览与 P4 节)。
- 2026-09-05:TUI 线 P0 三件(S1.5/S1.2/S1.1)与 M6.15–M6.21 修复链全部交付(含 /search 自由级断点交互修复);宿主工具线(M8/M10)仍未开工;剩余 P0 = 4(S1.3 输入增强)→5(M8-todo T1)→6(M10-memory)→6a(/workspace)。
- 2026-09-05:M10 tool-memory 交付(见 DESIGN §14.1 M10 ✅):剩余 P0 = 4(S1.3 输入增强)→5(M8-todo T1)→6a(/workspace)。
- 2026-09-05:TUI S1.3 输入增强交付(Ctrl+P/N 历史、Ctrl+Z/Shift+Z undo/redo、Ctrl+K/U kill、Alt+←/→ 按词移动;历史源=会话 user+已提交命令);剩余 P0 = 5(M8-todo T1)→6a(/workspace)。
- 2026-09-05:M8 tool-todo T1 交付(见 DESIGN §14.1 M8 ✅):剩余 P0 = 6a(/workspace 切换)。
- 2026-09-05:P0 收口:/workspace 切换交付(sdk.CwdSessions.SwitchProject + host 重绑 + TUI 命令,会话/上下文/展示即时切换;工具进程 cwd 重启后生效,见 TUI_OPTIMIZE §1.6)。P0 全部完成,下一阶段转 P1(M9/M11/S1.4/S2)与 P2+/P3(M7 等,见总览)。
- 2026-09-05:/workspace 升级为选择器两级交互(最近使用工作区按时间倒序 + 「输入新目录路径」入口;sdk.RecentProjects + host workspaces.json 记录;选历史直接切、哨兵断点输路径;同项目仅刷新最近时间)。
