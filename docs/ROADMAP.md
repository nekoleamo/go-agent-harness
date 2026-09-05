# gah 未完成功能实施排期

> 覆盖:DESIGN §14.1 未实施清单(M7–M11)+ `docs/TUI_OPTIMIZE.md`(TUI 优化 S1–S3)。
> 排期原则:**依赖约束 > 用户价值 > 风险/成本**;TUI 与宿主工具两类可并行推进(两者无强依赖,
> 仅 M9.2/8-T2 等少量交叉);每项独立验收(单测 -race + kitty 真机 + 全库 -race)。
> 工作量:XS <1d,S 1–3d,M 3–8d,L 2–4周,XL 1–3月。标注 ⚠️ 依赖前项。

## 总览

| 阶段 | 窗口 | 内容 | 里程碑判定 |
|---|---|---|---|
| P0 近期 ✅ | 已完成 | TUI S1.5/S1.1/S1.2/S1.3 + M8-T1 + M10 + 6a /workspace | ✅ 全部交付:划选/搜索/滚动条/输入增强;todo/memory 工具;工作区切换 |
| P1 中期(当前) | 下一迭代 | TUI S1.4/S2.1/S2.2 + M9(M9.1)+ M11-T1 | Markdown 可读、渲染组件化;subagent 委派 one-shot;auto-plan 输出+确认门 |
| P2 后期(6 周–3月) | 三~四期 | TUI S3.1/S3.2 + M9.2 + M11-T2 | 主屏 scrollback 模式;subagent 控制组/背景;plan→todo 承接 |
| P3 远期(季度+) | 独立里程碑 | M7(M7.1 Web UI)+ M7.2 槽位 + M7.3 WS + M8-T2(展示联动) | Web UI 全能力闭环;槽位插件化;todo 面板 |

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
| 7 | **TUI S1.4 Markdown 轻渲染** | 渲染文本层;建议在 S2.2 前以最小侵入做 | M | 粗体/行内 code/标题/列表缩进;先折行后按 token 着色 | 常见回复可读;无 ANSI 泄漏 |
| 8 | **TUI S2.1 消息分组/工具行折叠** | 独立 | S | user/assistant 细分隔线;⚙ 行默认折叠可展开(折叠态纯展示) | 多轮结构一眼可分;工具调用不占屏 |
| 9 | **TUI S2.2 渲染组件化** | ⚠️ P1 内 TUI 各项完成后再做(重构基座) | L | render.go 拆:会话流引擎/输入/状态栏/提示/markdown 样式层;各持 state 子视图 | 每模块单测;Render 输出基线一致 |
| 10 | **M9 tool-subagent M9.1**(one-shot) | 引擎 host-fanout 已存在 | M | 工具面+GAH_CB_ADDR 回调 fanout 桥+one-shot 委派;extplugins 独立入口 | 模型可委派子 agent 并取回结果;崩溃隔离 |
| 11 | **M11 tool-auto-plan T1**(规划输出+确认门) | 独立(与 M8 分工) | M | 探索→结构化规划→等确认(确认前零副作用工具);enable_rule 开关;catalogue+bundle | 规划落盘跨会话可取;确认门生效 |

## P2 后期(6 周–3 月:形态演进、高级交互)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 12 | **TUI S3.2 synchronized output** | 独立小项,可提前 | XS | DECSET 2026 合并大刷新 | 滚动/长输出无撕裂;不支持的终端忽略 |
| 13 | **M9.2 控制组+背景** | ⚠️ M9.1 | M | send_message/interrupt/list_agents;continuable 背景(复用 host-jobs 管道);fork | 可中断/消息/后台续跑 |
| 14 | **M11-T2 plan→todo 承接** | ⚠️ M11-T1 + M8-T1 | M | 确认后逐步骤推进,执行期由 todo 承接 | 规划确认后自动转执行 |
| 15 | **TUI S3.1 主屏 scrollback 模式** | ⚠️ S2.2 组件化 | L | `--view regular`/`/view regular`;输出流进 scrollback、底部固定输入;滚动交终端 | 长输出不占屏;模式切换无残留 |

## P3 远期(季度+:Web 全家桶)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 16 | **M7 Web UI** | — | XL | S1 web/包(server/SSE/静态/槽位骨架)+ui-web-app 插件+全套配置+`gah web` 2233;S2 交互闭环;S3 Seq 断线重放/auth_token | web 上跑通 TUI 同集能力 |
| 17 | **M7.2 UI 槽位插件化** | ⚠️ M7 | L | 加载器+manifest+安装命令;defineAsyncComponent 覆盖槽位 | 第三方 UI 插件可安装/覆盖槽位 |
| 18 | **M7.3 WebSocket** | ⚠️ M7 | M | 通道 seam(SSE 为第一实现,WS 同 payload) | 双通道统一订阅 |
| 19 | **M8-T2 展示联动** | ⚠️ M7.2 | M | todo 面板实时反馈任务推进(web 侧) | 模型任务进展 web 可见 |

## 并行性与约束
- **两条并行线**:TUI 线(1–4→7–9→12/15)与宿主工具线(5/6→10/11→13/14),可在同迭代交替交付。
- **强制依赖链**:15←9←1–8;17/18←16;13←10;14←11+5;19←17+5。
- **登记同步**:每个新插件(5/6/10/11)都需 catalogue 登记 + config 与 seed 两份 bundle 同步 + seed bump;外部化工具需重跑 scripts/gen-extplugins.sh。
- **回归红线**:始终保持物理行滚动/滚轮节流手势/滚动条拖动/启动新会话/双按退出的不回归单测。

## 变更记录
- 2026-09-05:初版(覆盖 M7–M11 与 TUI S1–S3,依据 DESIGN §14.1 切片与 docs/TUI_OPTIMIZE.md)。
- 2026-09-05:TUI 线 P0 三件(S1.5/S1.2/S1.1)与 M6.15–M6.21 修复链全部交付(含 /search 自由级断点交互修复);宿主工具线(M8/M10)仍未开工;剩余 P0 = 4(S1.3 输入增强)→5(M8-todo T1)→6(M10-memory)→6a(/workspace)。
- 2026-09-05:M10 tool-memory 交付(见 DESIGN §14.1 M10 ✅):剩余 P0 = 4(S1.3 输入增强)→5(M8-todo T1)→6a(/workspace)。
- 2026-09-05:TUI S1.3 输入增强交付(Ctrl+P/N 历史、Ctrl+Z/Shift+Z undo/redo、Ctrl+K/U kill、Alt+←/→ 按词移动;历史源=会话 user+已提交命令);剩余 P0 = 5(M8-todo T1)→6a(/workspace)。
- 2026-09-05:M8 tool-todo T1 交付(见 DESIGN §14.1 M8 ✅):剩余 P0 = 6a(/workspace 切换)。
- 2026-09-05:P0 收口:/workspace 切换交付(sdk.CwdSessions.SwitchProject + host 重绑 + TUI 命令,会话/上下文/展示即时切换;工具进程 cwd 重启后生效,见 TUI_OPTIMIZE §1.6)。P0 全部完成,下一阶段转 P1(M9/M11/S1.4/S2)与 P2+/P3(M7 等,见总览)。
- 2026-09-05:/workspace 升级为选择器两级交互(最近使用工作区按时间倒序 + 「输入新目录路径」入口;sdk.RecentProjects + host workspaces.json 记录;选历史直接切、哨兵断点输路径;同项目仅刷新最近时间)。
