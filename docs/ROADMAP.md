# gah 未完成功能实施排期

> 覆盖:DESIGN §14.1 未实施清单(历史:原 M7–M11,**现已全部交付清零**,本表兼作交付记录)+ `docs/TUI_OPTIMIZE.md`(TUI 优化 S1–S3)。
> 排期原则:**依赖约束 > 用户价值 > 风险/成本**;TUI 与宿主工具两类可并行推进(两者无强依赖,
> 仅 M9.2/8-T2 等少量交叉);每项独立验收(单测 -race + kitty 真机 + 全库 -race)。
> 工作量:XS <1d,S 1–3d,M 3–8d,L 2–4周,XL 1–3月。标注 ⚠️ 依赖前项。

## 总览

| 阶段 | 窗口 | 内容 | 里程碑判定 |
|---|---|---|---|
| P0 近期 ✅ | 已完成 | TUI S1.5/S1.1/S1.2/S1.3 + M8-T1 + M10 + 6a /workspace | ✅ 全部交付:划选/搜索/滚动条/输入增强;todo/memory 工具;工作区切换 |
| P1 中期 ✅(已完成) | 已完成 | TUI S1.4/S2.1 + M9.1 + M11-T1 | ✅ 已交付:Markdown 轻渲染(S1.4);消息分组/工具行去重(S2.1);subagent one-shot 委派(M9.1);auto-plan 规划+确认门(M11-T1) |
| P2 后期 ✅(已完成/决策) | 已完成 | S3.2(框架)+ M9.2(后台子集)+ M11-T2 + S2.2 | ✅ M9.2 后台委派;M11-T2 plan→todo;S2.2 渲染四层;S3.2 框架覆盖;S3.1 决策=方案 A 维持现状(记录在案) |
| P3 ✅ 已交付(2026-09) | 独立里程碑 | M7 Web UI + M7.2 槽位插件化 + M7.3 WS 通道 + M8-T2 展示联动(+M13/M14/M16/M16.7–16.9:Web 会话工作台·设置面板·UI 收敛·便携数据根,见下方) | Web UI 全能力闭环、槽位插件化、可视化设置、完全便携均 ✅;二期待办( jobs 面板/export/命令下沉/mcp 看护)见 DESIGN §14.1 未实施清单 |
| P4 体验改进(对比 pi 基线,见 docs/PI_COMPARISON.md) | 独立迭代 | **12 项全部交付 ✅(2026-09)**:P4-1 消息队列 · P4-2 @引用+Tab 补全 · P4-3 代码块高亮 · P4-4 会话命名 · P4-5 C6 多级上下文 · P4-6 多行输入/外部编辑器 · P4-7 工具视觉 · P4-8 /compact · P4-9 语义色 · P4-10 C1 会话树/分支 · P4-11 /reload · P4-12 widget 槽位(细见 P4 节);会话树可视化 UI 与 E1 UI seam 仍绑 M7.2 | 交互体验对齐 pi;Agent 能力面无缺口 |
| P5 TUI 视觉升级 ✅(2026-09-08,对照本地 pi gruvbox-dark) | 独立迭代 | **6 项全部交付**:V1 主题 token 扩 16(背景/md-link-quote/thinking 边框/syntax 细化)· V2 用户消息整块背景 · V3 工具框感(▍ 竖线+三态背景) · V4 md 链接/引用/语法高亮 · V5 输入思考色左缘竖线 · V6 Ctrl+O 折叠/回合耗时/Ctrl+↑↓ 跳转(细见 DESIGN §14.1 P5 / TUI_OPTIMIZE 执行记录 6) | 对照 gruvbox-dark 的界面层次补齐(消息/工具背景块、框感、md 完整度、输入聚焦);主题外部化对新 token 天然生效 |

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
| 13 ✅ | **M9.2 控制组+背景** | ⚠️ M9.1 | M | ✅ 已交付:后台 delegate 带手柄(spawn/agents/agent_status/agent_kill)+ **M9.3 send_message/fork(2026-09)**:接口扩展(Fork/SendMessage/AgentHandle.Messages)+ fanout 引擎 inbox/dialog/seed(运行循环每步 drain 注入,回复经 agent_status.messages 可读)+ 回调协议 send/fork + 工具面 send_message/fork action;全库 -race 绿 | ✅ 可后台委派/列表/查状态/终止/消息引导;fork 继承父上下文 |
| 14 ✅ | **M11-T2 plan→todo 承接** | ⚠️ M11-T1 + M8-T1 | M | ✅ 已交付:规则文本/Description 增补联动边界(auto_plan 管确认门,确认后执行期由 todo 承接,检查清单转 todo.create,执行完回归档);TestRuleTodoHandoff 等回归 | ✅ 确认门→todo 承接语义清晰 |
| 15 ⏸️ | **TUI S3.1 主屏 scrollback 模式** | ⚠️ S2.2 组件化 | L | 决策:方案 A 维持现状(暂不实施)。B/C 备选记录在案(docs/TUI_OPTIMIZE.md S3 决策记录),后续再研究 | —(暂缓) |
| 16 ⏳ | **UI 槽位 v2:设置等新扩展点插件化** | ⚠️ M7.2(v1 契约)+ M16.8 设置面板 | L | v1 仅四槽位(stream/input/statusbar/confirm)可覆盖;v2 将 **设置面板区段、侧栏扩展区、附加面板入口**开放为插件扩展点(registry v2 + manifest 扩展点声明 + 插件可在 App 内注册自定义入口/面板容器;App 骨架中可插拔面收敛为显式槽位)。切片:① 契约扩展(registry v2,向后兼容 v1 插件)② 新槽位落点(设置面板 sections / 侧栏 actions / 附加面板 host)③ 示例插件+文档 | ✅ 第三方 UI 插件可向设置面板/侧栏注入扩展,附加面板与 v1 并存;v1 插件零改动 |

## P3(已交付 2026-09:Web 全家桶)

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| 16 ✅ | **M7 Web UI** | — | XL | ✅ 已交付(2026-09):S1–S3 一次完工——web/ 包(server/events/confirm:SSE 下行 + REST 上行,Last-Event-ID 断线重放经会话 Seq)+ ui-web-app 插件(data.addr/auth_token/static_dir)+ bundle-web/profile-web/seed 全套 + `gah web` 入口糖;web-src/ 前端(Vue3+Vite+TS:SFC,会话流/状态栏/`/` 命令提示/审批弹层/会话切换)+ registry.ts 四槽位契约 v1(M7.2 预留)+ scripts/gen-web.sh(embed web/dist,漏则主包编译失败)+ CI vue-tsc + tests 端到端;TUI 内部命令(会话/模型类)不下沉,web 侧以等价 REST(/api/sessions、/api/control)承接(命令经 ctx.commands 的宿主命令仍可用) | ✅ web 上跑通 TUI 同集能力;断线续传;auth_token 鉴权;-race 绿 |
| 17 ✅ | **M7.2 UI 槽位插件化** | ⚠️ M7 | L | ✅ 已交付(2026-09):加载器(plugins.ts 动态导入+priority 覆盖)+ manifest.json 契约 v1 + `gah -install-ui/-uninstall-ui/-list-ui-plugins`(v-html 指令扫描拒装;npm 构建落 $GAH_HOME/ui-plugins/<id>/)+ server 托管(/ui-plugins/ + /api/ui-plugins 聚合)+ 示例插件 web-src/examples/statusbar-demo | ✅ 第三方 UI 插件装完即覆盖槽位(重载页面生效);registry v1 兼容;-race 绿 |
| 18 ✅ | **M7.3 WebSocket** | ⚠️ M7 | M | ✅ 已交付(2026-09):web/ws.go 零依赖 RFC6455(握手/Origin 同源/文本帧)+ server 共享 consumeStream(EventHub=seam)+ 前端 transport.ts WS 优先降级 ES + cookie 鉴权 + CI ws-smoke.go 冒烟 | ✅ 双通道同 payload 可切;前端零返工;-race 绿 |
| 19 ✅ | **M8-T2 展示联动** | ⚠️ M7.2 | M | ✅ 已交付(2026-09):/api/todo(经 ctx.tools 代理 todo list)+ 演示插件 web-src/examples/todo-panel(槽位插件:statusbar 覆盖 + 浮动面板,5s 轮询/刷新,状态徽标;priority 120);tests e2e 含 tool-todo 验证 200 | ✅ 模型任务进展 web 面板实时可见;同槽位 priority 抢占语义实测;-race 绿 |
| 20 ✅ | **M9.3 控制面完整** | ⚠️ M9.2 | L | ✅ 已交付(2026-09):send_message(运行中注入,回复经 messages 可读)/ fork(父会话历史种入);交互语义=输入注入非暂停式,暂停-恢复归展示联动线评估 | ✅ 子代理运行中可消息引导;fork 上下文继承 |
| 21 ✅ | **M13 TUI 主题外部化** | — | S–M | ✅ 已交付(2026-09):palette 覆盖层(ApplyTheme/colorVal,默认表兜底)+ 启动链(data.palette<theme.yaml)+ /theme 切换(themes/*.yaml)+ 防穿越/显式报错;palette_test/theme_test/theme_cmd_test | ✅ 改配置/命令即整体换肤(零重编译);基线守卫不破;-race 绿 |
| 22 | **M14 外部命令桥** ✅ | ⚠️ host-bridge | M | ✅ 已交付(2026-09):协议加 Commands/CommandOptions/RunCommand(ServeTools 变参);host-bridge 转注册 ctx.commands(同名字冲突跳过/Disposer 撤销);**命令 RPC 超时保护**(TimeoutMs 覆写,默认 3s)+ **纯命令插件**(cmd-* 前缀识别,无工具可载);tool-echo 参照实现;command_bridge_test 端到端 | ✅ 新命令插件零重编译 gah、丢目录即生效;`/` 提示自动可见;慢命令不阻塞 TUI;旧插件零改动兼容;-race 绿 |
| 23 ✅ | **M16 测试提速专项** | — | M | ✅ 已交付(2026-09):pty 探针窗口收紧 + drainUntil 条件提前退出、external 独立 e2e t.Parallel、CI 计时护栏;全库首跑 74s(基线 ~100s);≤60s 目标未完全达成(host-bridge RPC/embed 校验记录后续优化)。详见 DESIGN §14.1 M16 | ✅ 首跑 74s、重复 ≤5s、覆盖不减;-race 绿 |
| 24 ✅ | **M16.7 Web 会话工作台与交互收敛**(2026-09) | ⚠️ M7.2 | L | ✅ 已交付(taste skill 纪律全 web):侧栏重构(工作区固定区/会话摘要/改名/删除,二次确认居中弹层)+ 消息类型分色 + 连接状态入状态栏 + 工作区切换**真正切目录**(SwitchDir + cwd/workspace-switched 事件 → host-bridge 外部进程重启继承新 cwd、policy-sandbox root 同步)+ 删除语义(删当前会话自动新建空承接)+ tool-mcp 多 server(GAH_MCP_COMMANDS)+ 命令/接口 details 见 DESIGN §14.1 M16.7 | ✅ web 会话工作台可视化完备;工具真实在新工作区目录执行;-race 全绿 |
| 25 ✅ | **M16.8 Web 可视化设置面板与 UI 收敛**(2026-09) | ⚠️ M16.7 | L | ✅ 已交付:设置抽屉(模型/思考/沙箱/Provider/插件/历史/压缩)+ 后端 compact/history 端点 + 输入一体外壳(DeepSeek 式,空态居中/对话态沉底)+ 左右分割布局 + 克制动效层(reduced-motion)+ 偏好持久化 internal/prefs(TUI/Web 共享)+ 插件管理域(manage: host/web/external/scenario,外部化只读)。细节见 DESIGN §14.1 M16.8 | ✅ 命令能力转可视化;升级替换单文件;外部化插件 web 防误启;-race 全绿 |
| 26 ✅ | **M16.9 便携数据根与纪律**(2026-09) | ⚠️ main | S–M | ✅ 已交付:homeDir 便携自动发现(gah 同级 gah-data,GAH_HOME>便携>~/.gah>TempDir)+ 迁移(旧 ~/.gah→gah-data,env 入 gah-data/env.sh,start.sh 启动)+ 便携纪律入 AGENTS/PLUGIN_DEV 规范。细节见 DESIGN §14.1 M16.9。**后续变更(56a0267)**:~/.gah/TempDir 兑底已弃用,不可便携时 boot 显式报错退出 | ✅ 升级只替换 gah 单文件;数据/密钥/env 全随 gah-data;-race 全绿 |
| 27 ✅ | **M17 审批等级三档**(2026-09) | — | M | ✅ 已交付:policy-approval 由单档扩三档(open 放行 / smart 默认弹确认 / strict 直接拒绝)+ sdk.ApprovalMode/ApprovalService(Provide ctx.approval)+ TUI `/approval`(tui + host-internal-commands 双轨,判重跳过)+ Web 设置面板「审批」分段(/api/control approval + state.approval)+ 偏好持久化 internal/prefs(重启恢复)+ bundle `data.mode: smart`(seed 9→10)。细节见 DESIGN §14.1 M17 | ✅ 危险操作按档处理;TUI/Web 双端可切、重启恢复;-race 全绿 |
| 28 ✅ | **M18 整体备份/恢复**(2026-09) | ⚠️ M16.9 | M | ✅ 已交付:新插件 plugins/host/host-backup(Provide ctx.backup)——`/backup` 一键打包 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好,**排除 backups/ 自身**)+ 确定性 tar.gz(gzip 头置零,同源字节一致)+ 时间戳命名防覆盖;默认存 `$GAH_HOME/backups/` + 外部 dest;`/backup list|restore`(恢复前**自动先备份当前态**,`../` 越界/坏归档拒绝)+ web `/api/backup`(未装配 503)+ 设置面板「数据备份」区段(恢复二次确认)+ catalogue/bundle(seed 10→11)。细节见 DESIGN §14.1 M18 | ✅ 一键整体备份含密钥、随 gah-data 迁移、恢复前自备份+二次确认;-race 全绿 |

## 并行性与约束
- **两条并行线**:TUI 线(1–4→7–9→12/15)与宿主工具线(5/6→10/11→13/14),可在同迭代交替交付。
- **强制依赖链**:15←9←1–8;17/18←16;13←10;14←11+5;19←17+5;P4:C1/P4-10←M7.2 后(或独立树视图),其余 P4 项独立。
- **P4 备注**:P4-1/2/3/4/6/7/8/9/11/12 均不依赖 M7,可在 P3 Web 之外独立迭代;P4-10(会话树)与 P3 相关但可作独立 TUI 树先做。
- **登记同步**:每个新插件(5/6/10/11)都需 catalogue 登记 + config 与 seed 两份 bundle 同步 + seed bump;外部化工具需重跑 scripts/gen-extplugins.sh。
- **回归红线**:始终保持物理行滚动/滚轮节流手势/滚动条拖动/启动新会话/双按退出的不回归单测。

## P4 体验改进(对齐 pi 基线;来源 docs/PI_COMPARISON.md)

> 状态:**P4 12 项全部交付** ✅(P4-1 消息队列 · P4-2 @引用+Tab 补全 · P4-3 代码块高亮 · P4-4 会话命名 · P4-5 C6 多级上下文 · P4-6 多行输入/外部编辑器 · P4-7 工具视觉增强 · P4-8 /compact · P4-9 语义色 token 化 · P4-10 C1 会话树/分支(可用闭环,树可视化归 M7 后)· P4-11 /reload 热更 · P4-12 widget 槽位)。Agent 能力面无缺口;P4 内不再有未实施项。
> (pi 明示不内置的 MCP/subagent/权限/plan/todo/后台 bash,gah 均已交付),本阶段只补交互与会话体验。

| # | 功能 | 依赖 | 工作量 | 切片/要点 | 验收 |
|---|---|---|---|---|---|
| P4-1 ✅ | **T1 消息队列**(回合串行) | 独立 | M | ✅ 已交付(范围收敛:回合级排队;轮内"转向打断"需 agent-loop 注入 seam,记入未实施):回合运行中 Enter 普通消息不入新回合 → 入队(状态栏 "· 待发 N",命令不入队即时执行);回合**成功结束自动逐条续发**(每次一条,保证会话串行;取消/失败暂停续发,队列保留);Esc(空闲)逐条取回队列到编辑区、Alt+Up 随时取回队尾;会话/工作区切换清空队列防错发 | agent 工作时可提交后续意图;回合串行(修复此前运行中 Enter 会并发开新回合的隐患);取消不丢队列可取回;-race 全绿 |
| P4-2 ✅ | **T2a @文件引用 + Tab 路径补全** | 独立(sandbox/tool-files 路径面) | M | ✅ 已交付:输入中以空白/行首开头的 "@词"(光标在词内/尾)自动激活项目文件候选(URL/邮箱段内 @ 不触发);↑/↓ 移动、Tab/Enter 应用替换整段 @token(相对路径插入,光标跟到路径尾)、Esc 关闭保留文本;继续输入子串过滤、退格恢复;候选=App 侧项目文件索引(indexProjectFiles 纯函数,排除依赖/生成/隐藏目录、上限 4K、cwd 缓存,workspace 切换失效);与命令选择器/自由向导互斥(命令输入不启用);路径不越 cwd(索引本身限定工作区,沙箱外无新增通道) | @ 唤起候选、选中插入路径;Tab 直接应用高亮项;过滤跟随输入;文件列表受目录黑名单与上限约束(新增 mention/files 单测,-race 全绿) |
| P4-3 ✅ | **T4 代码块高亮** | S2.2 引擎就绪 | S–M | ✅ 已交付:render 层展平后 annotateCodeFences 跨行状态标注(仅 assistant 参与切换,工具结果含 ``` 不干扰);围栏行/块内行统一代码色 + 单遍极简语法着色(字符串/注释/关键字,字符无损),块内禁 md token 误伤;折叠/搜索/选区回落路径不变 | ✅ 代码块可读、与正文区分;无 ANSI 泄漏/宽度不变(新增围栏/高亮单测,全库 -race 38 包绿) |
| P4-4 ✅ | **C3 会话命名(/name)** | host-cwd-sessions | S | ✅ 已交付:tui/app.go 注册 /name <名>(- 清除)+ host-cwd-sessions names.json 索引(按会话文件独立持久,坏文件容忍)+ 状态栏/切换选择器/session current 显示名优先 | ✅ 状态栏显示名;重开/切会话名保留;无名称回退 id/主会话;全库 -race 38 包绿(pty 探针 pre-existing 除外) |
| P4-5 ✅ | **C6 多级上下文文件加载** | host-system-prompt | M | ✅ 已交付:从 cwd 逐级向上收集 AGENTS.md(根→近,近者放在后覆盖远者),每级标题标明来源目录;同级 AGENTS.override.md 存在时替换该级 AGENTS.md(override 语义);全缺跳过不注入;单级/兼容路径保留(projectInstr 回退);data.instructions.project 开关控整体 | 上级目录约定自动生效;层级近者覆盖远者;override 同级替换;来源可辨(新增 levels_test,-race 绿) |
| P4-6 ✅ | **T2b 多行输入 + 外部编辑器** | TUI 输入 | S–M | ✅ 已交付:Shift+Enter 插入换行(仅普通输入态;选择器/自由向导仍与 Enter 同语义)、↑/↓ 行间移动(列意图记忆 bash/readline 语义,单行退化原首/尾)、输入区多行渲染(续行对齐、主区自动扣减);Ctrl+G 外部编辑器整段编辑($VISUAL>$EDITOR>nano,tea.ExecProcess 自动 releaseTerminal 交还终端,保存退出回填,undo 一步;命令参数支持如 "code -w");多行普通消息可提交,命令(/ 前缀)保持单行语义——含换行拒绝保留现场提示,纯空白不发起回合;Ctrl+C 清空/双按退出语义不回归 | 多行输入可提交;外部编辑回填;↑↓ 行间光标;Ctrl+C 语义不回归(新增 multiline_test,全库 -race 绿,pre-existing pty 探针除外) |
| P4-7 ✅ | **T3 工具结果视觉增强** | S2.2 就绪 | M | ✅ 已交付:工具三态语义色(调用 ⚙ 琥珀 / 成功 ✓ 绿 styleToolOK / 失败 ✗ error 红);展开结果 diff 轻染色(+++/---/@@ 头灰、+ 行淡绿、- 行暗红,字符无损);搜索命中/选区回落基础样式 | ✅ 工具执行状态一眼可分;失败醒目;diff 染色可读;单测 toolrow_test + 全库 -race 38 包绿(pty 探针 pre-existing 除外) |
| P4-8 ✅ | **C2 手动 /compact [prompt]** | token-compress | M | ✅ 已交付:sdk.CompactService 可选接口(host-session-log Log 实现,类型断言发现,不改 ctx.sessions);/compact 立即以注册预算折叠滚动摘要(不等投影超限,自动压缩不变),回读累计摘要回显(截断单行);无可折叠/压缩器未注册/预算关闭均明确提示不静默;prompt 指示词仅记录(token-compress 为抽取式引擎,不消费其内容);空会话不重复压缩 | 手动压缩即时生效;摘要可读;错误显式;短会话/关闭预算有明确提示(新增 compact_test 用真实 Engine,-race 全绿) |
| P4-9 ✅ | **E2 语义色 token 化** | S2.2 | S–M | ✅ 已交付:tui/palette.go 新增 Token+DefaultPalette(21 token)+ fg() 唯一取色入口;render.go/markdown.go/session.go 全部色值字面量收口为 token 派生(零裸色值);palette_test.go 基线守卫(逐 token 对原 256 索引 + 无空色值) | ✅ 渲染逐字等价(全 tui 单测 SGR 精确断言绿);换肤仅改 DefaultPalette 本表 |
| P4-10 ✅ | **C1 会话树/分支**(可用闭环;树可视化导航归 M7 后) | 独立 TUI 树 | L | ✅ 已交付:sdk.ForkableSessions 可选接口(host-cwd-sessions ForkAt/CloneCurrent/ForkPoints);/fork [seq] 从历史任意点派生分支会话(继承到该点,切换续聊,新轮次只写新文件;缺省=最近提问)、/clone 复制当前独立演进;分支自动命名标记(fork@seq ← 源 / clone ← 源);/tree 会话分支树(各会话 + 可 fork 提问点 seq+摘要);导航复用 /session switch;坏行容忍 | 回退/多方案并行;/tree 定位点 + /fork 派生 + /session 切换(新增 fork/forkcmd 单测,-race 绿;树形可视化导航 UI 与弃支摘要归 M7.2 后) |
| P4-11 ✅ | **E4 配置热更(/reload 等效)** | 独立 | M | ✅ 已交付:sdk.ReloadableInstructions 可选接口(断言发现)+ host-system-prompt ReloadInstructions(按配置文件重读全局/多级项目/附加 AGENTS.md;NotFound=清除旧值,其它读取失败保留旧值返回错误=错误回滚);TUI /reload 命令(显式提示失败保留旧值);下次回合 SystemPrompt 生效,免重启。外部进程插件热重载既有(host-bridge watch)。键位目前无用户配置文件(硬编码);主题外部化已交付 **M13**(DESIGN §14.1:注入通道=theme.yaml/data.palette + /theme 切换,默认表兜底) | 外部编辑 AGENTS.md 后 /reload 免重启生效;失败保留旧值显式提示(新增 reload_test,-race 绿) |
| P4-12 ✅ | **T5 输入区 widget 槽位** | S2.2 | S–M | ✅ 已交付:State.Widgets+WidgetOn;App.AddWidget(id,text func) 宿主注册(每次渲染求值,空/nil 跳过,单行化截断;渲染帧经 onWidgets 拉取);输入行上方 ◇ 前缀渲染,主区高度自动扣减(不挤输入);/widgets on|off 开关(新增 TokWidget 语义色)| widget 展示;不挤输入;可开关(新增 widgets_test,-race 绿) |

> **明确不做/远期**(已决策):S3.1 scrollback 双形态(方案 A)、全量键位自定义。~~图片富媒体~~ **已撤销(2026-09)**:改为 Web 附件(图片/文件)一期含多模态注入,见 docs/WEB_ATTACHMENTS_PLAN.md 与 docs/TODO_OVERVIEW.md A 组。
> **绑 M7**:C1 会话树 UI、E1 UI 扩展 seam(Web 侧 registry 先行,M7.2 槽位概念 TUI 后接)。

## 变更记录
- 2026-09-06:tests/TestTUIProbeScrollbarAndArrow 探针修复:对齐现行语义(M6.15 启动首屏干净不强制重放历史/滚动条、M6.17 ↑↓ 归输入框光标,滚动键用 PgUp /\x1b[5~/、选择器分步推进——命令级前缀过滤,继续输入超命令名即脱选择态);进入历史会话(/session 分步选中 switch → 二级会话列表)后验滚动条轨道 ░ + PgUp 两次滚动 diff + 退出链。**全库 -race 全绿,不再有 pre-existing 失败**。
- 2026-09-06:M14 补丁:外部命令 RPC 超时保护(rpcCall 复用,命令可声明 TimeoutMs 覆写)sdk.CommandSpec.TimeoutMs 新字段 + 纯命令插件支持(host-bridge 扫描 tool-*/cmd-* 前缀,loadOne 工具或命令任一满足即载,ServeTools(nil, commands) 可纯命令);TestExternalCmdOnlyPluginWithTimeout 端到端;-race 绿(除 pre-existing pty 探针)。
- 2026-09-06:M14 外部命令桥交付(桥协议加 Commands/CommandOptions/RunCommand(CommandDTO 级联声明,旧插件无方法自动按"无命令"处理,零改动兼容);ServeTools(tools, commands...);host-bridge 可选注入 ctx.commands+转注册(同名冲突显式跳过/Disposer 撤销)+ commandRPCClient RPC 代理(枚举级经 CommandOptions 求值,连接错误自动拉起);extplugins/tool-echo 参照实现;command_bridge_test 装配真进程端到端+-race 绿(除 pre-existing pty 探针))。
- 2026-09-06:M13 TUI 主题外部化交付(tui/palette.go 覆盖层 ApplyTheme/colorVal(默认表兜底,未知 token/非法色值显式报错)+ NewApp 变参 palette 启动链(data.palette<theme.yaml)+ tui/theme.go 文件层(theme.yaml/themes/<名>.yaml,防穿越,对齐 search.yaml 先例)+ /theme 命令(枚举主题列表+default 哨兵,运行期切换/恢复);palette_test/theme_test/theme_cmd_test 配套;-race 绿(除 pre-existing pty 探针))。
- 2026-09-06:M14 外部命令桥入未实施规划(DESIGN §14.1 未实施清单 6→7 项 + ROADMAP P3 表 22 号;背景:外部进程协议仅工具,命令注册通道 ctx.commands 在进程内;方案=桥协议加 CommandSpec 声明→host-bridge 转注册→GAH_CB_ADDR 回调 commands.run,复用热重载/崩溃拉起/软降级,新命令插件零重编译 gah。)
- 2026-09-06:M13 TUI 主题外部化入未实施规划(DESIGN §14.1 未实施清单 5→6 项 + ROADMAP P3 表 21 号 + TUI_OPTIMIZE 不做/远期同步;背景:P4-9 token 化后换肤表仍编译期硬编码,注入通道=ui-tui-app data.palette / theme.yaml / /theme,默认表兜底,零重编译换肤)。
- 2026-09-06:P4-10 C1 会话树/分支交付(sdk.ForkableSessions 断言 + host-cwd-sessions ForkAt/CloneCurrent/ForkPoints;/fork [seq] 任意点派生(缺省最近提问)/clone 复制;/tree 列出各会话可 fork 提问点;自动命名标记;导航复用 /session switch;树形可视化 UI 归 M7 后)——**P4 12 项全部交付**。
- 2026-09-06:P4-11 E4 配置热更交付(sdk.ReloadableInstructions 断言 + host-system-prompt ReloadInstructions 重读全局/层级/附加指令文件,NotFound=清除、其它失败保留旧值;/reload 命令免重启生效);剩余 P4 1 项。
- 2026-09-06:P4-12 T5 输入区 widget 槽位交付(State.Widgets+WidgetOn / App.AddWidget 宿主注册渲染帧求值 / 输入行上方渲染主区扣减 / /widgets on|off / TokWidget 语义色);剩余 P4 2 项。
- 2026-09-06:P4-5 C6 多级上下文文件加载交付(从 cwd 逐级向上收集 AGENTS.md 近者覆盖远者,来源标注;AGENTS.override.md 同级替换;缺失跳过);剩余 P4 3 项。
- 2026-09-06:M12 多 provider 并存交付(独立需求:多 provider 配置可并存、/model 聚合所有 provider 模型——存储 provider.yaml v2{active,providers[]}+旧格式迁移;sdk.MultiProviderService 可选接口+OpenAIFetchModels;host-llm Add/SetActive/ListAllModels(活跃走适配器缓存,非活跃直拉,TTP 缓存,单条失败不整体崩);TUI /provider show|add|use|set|unset|clear 与 /model 聚合(选中自动切所属 provider,手动 /model 向后兼容),见 DESIGN §14.1 M12)。
- 2026-09-06:P4-2 T2a @文件引用 + Tab 路径补全交付(@token 自动激活项目文件候选(URL 内不触发),↑↓/Tab/Enter/Esc 完整交互;App 项目文件索引含 cwd 缓存与 workspace 切换失效);剩余 P4 4 项。
- 2026-09-06:P4-8 C2 手动 /compact 交付(/compact [指示词]:sdk.CompactService 断言 + Log.Compact 立即折叠,回显单行摘要;未启用/无可压缩显式提示;自动超限压缩不变;指示词仅记录——抽取式引擎不消费);剩余 P4 5 项。
- 2026-09-06:P4-1 T1 消息队列交付(回合级排队:运行中 Enter 入队、命令不入队即时执行;成功回合自动逐条续发、取消/失败暂停;Alt+Up/Esc 取回;会话切换清空;范围收敛——轮内"转向打断"需 agent-loop 注入 seam 记入未实施);剩余 P4 6 项。
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
- 2026-09-06:M9.3 send_message/fork 交付(sdk.FanoutService 增 Fork/SendMessage + AgentHandle.Messages;sdk 子代理运行循环 inbox 注入/dialog 记录/fork 父会话历史种入;回调协议 send/fork 分支;工具面 send_message/fork action;host-fanout/tool-subagent/host-bridge 单测 + 外部 e2e TestExternalSubagentFork;gen 重建产物;全库 -race 39 包绿)——**M9 全交付**,未实施清单 5→4 项,剩余全为 M7 Web 线。
- 2026-09-06:M15 全交付(M13 主题外部化上收口:TUI π 式默认样式——状态栏精简去 gah、思考动画 shade 光条滚动、指标行最底、底部区空隙行;F15.1–F15.5 迭代,全库 -race 39 包绿,详见 DESIGN §14.1 M15)。
- 2026-09-06:M16 测试提速专项入规划(DESIGN §14.1 未实施清单 4→5 项 + ROADMAP P3 表 23 号,排期待 M7 Web 后;背景:改代码后首次全库 -race ~1m40s(tests pty 探针 84s/embed 27s/bridge 22s),日常缓存命中 3s(去 -count=1 即用 Go 测试缓存);目标:首跑 ≤60s + 计时回归护栏)。
- 2026-09-06:M16.7/M16.8/M16.9 交付入 ROADMAP P3 表(24–26 号,与 DESIGN §14.1 交付行对应):web 会话工作台/taste UI 收敛/设置面板/输入一体外壳/克制动效/偏好持久化(内嵌 prefs TUI 共享)/工作区真实切目录/插件管理域/manage 只读/tool-mcp 多 server/便携数据根(gah-data)+ 便携纪律规范。二期待办:jobs 面板、会话 export、TUI 命令下沉、mcp server 看护(见 DESIGN §14.1 未实施清单 2026-09 二期)。
- 2026-09-16:R5 三端复查 + R6 观察项完善交付(DESIGN §14.1 登记):旧解析链(~/.gah)收敛、SSE 重放/订阅 gap 修复(先订阅后重放+Seq 去重,consume_test 回归)、WS 指数退避重连(transport.ts)、桌面壳数据根恒传(appDataHome)与失败提示;全库 44 包 -race 绿 + cargo check 通过。policy-guard 融合(policy-approval+sandbox→单插件,seed 12,并行会话)同期入库。
