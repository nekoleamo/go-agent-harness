# gah TUI 界面优化规划(参考 pi agent / pi-tui)

> 状态:✅ S1.1/S1.2/S1.3/S1.5/S1.6 已实施;✅ S1.4 Markdown 轻渲染已实施
> (assistant 行 token 级粗体/行内 code/标题/列表/分隔线,折叠后逐 token 着色,字符无损);
> ✅ S2.1 消息分组已实施(工具行双写去重 + 重放跨轮细分隔线 + 结果单行截断);
> ✅ S2.2 组件化已实施:render.go(整屏装配 149 行)/ session.go(会话流引擎 333 行:
> 展平/折行/样式/行渲染/选区/滚动度量)/ chrome.go(装饰层:输入/提示/状态栏)/
> markdown.go(md 样式层)四层分离,行为逐字等价(30+ Render 基线测试锁输出);
> 折叠交互已实施(tool 结果行存 Full 全文(foldFullLimit 4KB 保护),State.FoldOpen +
> 鼠标单击 toggle 展开/收起,flattenViewLines 视图几何一致;fold_test.go 6 项);
> ✅ S2.2 全部分层完成;S3 决策:方案 A 维持现状(记录在案,见 S3 决策记录),S3.2 由框架启用 ✅。
> 参考对象:`@earendil-works/pi-tui`(0.85.0,差分渲染 TUI 库)与 pi agent 会话界面;
> 本规划只借鉴**交互/布局/结构理念**,实现仍在 gah 的 bubbletea v2 + lipgloss 技术栈内(Go),
> 不引入 JS/pi-tui 代码。排期见 [ROADMAP.md](ROADMAP.md)。

## 背景:pi-tui 设计要点(对比基准)

| 维度 | pi-tui 做法 | gah 现状(tui/) |
|---|---|---|
| 渲染形态 | 双形态:`TuiMainScreen`(输出流进主屏+scrollback,滚动交终端原生)+ `TuiAltScreen`(自研 viewport/ScrollView) | 仅 alt-screen + 自研物理行滚动(物理行引擎已就绪) |
| 会话内搜索 | `AltScreenSearch`(索引 + searchNext/Previous) | ✅ `/search` 已交付 |
| 滚动条 | thumb/track 样式、可拖、**hover 高亮、auto-hide、回到最新指示器** | ✅ 回底/hover/auto-hide 已交付 |
| 输入框 | Editor:autocomplete/history/kill-ring/undo/word 导航 | ✅ 历史/undo/kill/词导航已交付 |
| 内容渲染 | markdown/text/truncated-text、box、组件布局(v/h-stack/spacer) | S1.4 md 轻渲染已交付(tui/markdown.go);render.go 拆模归 S2.2 |
| 交互基建 | mouse-region(组件绑鼠标)、loader、select-list、状态行 | 部分:spinner/banner/picker/滚动条拖动✓ |
| 渲染稳度 | 差分渲染 + synchronized output(DECSET 2026) | bubbletea 差分✓;synchronized output 由框架自动启用(tea.go 终端能力查询,kitty/wezterm 等自动,2026 已支持) |

## S1 交互补全(当前架构内,低风险,优先)

### 1.1 会话内搜索(对标 pi AltScreenSearch)✅ 已实施
- 入口:`/search <词>`(自由级断点交互,选中后输入词回车执行;历史可用 Ctrl+P/N 翻到);
  再次搜索更新词;Esc 退出。
- 匹配对象:会话流已展平文本(State.Lines 逻辑行),大小写不敏感、子串匹配。
- 跳转:`n`/`N` 或 F3/Shift+F3 循环 next/prev;跳转后会话流滚动到命中行、
  命中行展平首物理行置顶;当前命中整行背景高亮(更亮),其余命中行弱高亮。
- 命中计数显示在搜索提示行(`搜索 "x":命中 2 行`);无命中自动退出搜索态;输入框有内容时 n 仍为输入。
- **已落地**:`/search <词>`;State.SearchQuery/SearchHits/SearchIdx + searchRun/Goto/Jump;
  renderSessionRow 命中高亮(与选区反色叠加);Esc 退出。
  单测 search_test.go + kitty 真机(命中 43 行/跳转定位)。
  M6.21 修复:注册缺 Args 曾导致"选中即提交、再输入误走普通消息",补自由级断点 + 输入框尾随空格。
- 验收:长会话中搜索词能循环命中并定位;Esc 退出恢复原视图。

### 1.2 滚动条完善(回底指示/hover/auto-hide)✅ 已实施
- **回到最新指示**:offset>0(浏览历史)时滚动条底端显示"▼"(点击回底 offset=0),回底后消失。
- **hover 高亮**:鼠标悬停滚动条列时轨道/滑块加亮(auto-hide 期间保持)。
- **auto-hide**:滚动停止 ~1.5s 且无 hover 时滚动条隐藏;任何滚轮/拖拽/翻页后重新显示。
- **已落地**:State.BarShownAt/HoverBar + markBar(交互重置计时并排 auto-hide tick)+ 渲染判定隐藏;
  回底指示与点击回底;hover 样式;PgUp/PgDn 同样重置计时。
  单测 TestBarAutoHide/EndIndicator/HoverAndMark + kitty 真机(▼ 出现/静置隐藏/点击回底)。
- 进阶(⏳ 规划):滚动平滑动画(当前 30ms/2 行已丝滑;像素级插值成本高,按需评估)。
- 验收:浏览历史时可见回底指示并点击回底;静止后滚动条隐藏,操作即重现。

### 1.3 输入编辑增强(对标 pi editor)✅ 已实施
- **输入历史**:↑/↓ 已交还光标(行首/尾),历史由 **Ctrl+P / Ctrl+N**(上/下一条)提供;
  历史源 = 本项目会话 user 消息(Lines,含切会话重放)+ 本进程已提交斜杠命令(CmdHistory);
  相邻重复去重;首次翻页记录草稿,越过最新一条自动还原草稿;填入 `/` 开头历史不自动激活选择器。
- **undo/redo**:Ctrl+Z / Ctrl+Shift+Z(输入框内文本撤销+光标恢复,提交/清空即清栈;
  相邻同型编辑 800ms 合并为一个 undo 步防逐字符爆栈;新编辑使 redo 失效)。
- **kill/word 导航**:Ctrl+K 删至行尾、Ctrl+U 删至行首、Alt+←/→ 按词移动
  (左=前词首,右=下一词首;均可用 undo 恢复;参考 kill-ring 语义,不做 ring)。
- **已落地**:state.go(histRev/histCur/草稿 + undo/redo 栈 + kill/word 状态机)+
  model.go(组合键分派,选择器激活时不拦截)+ submit 记录命令。
  单测 input_edit_test.go(历史循环/去重/合并/序列/kill/word/清栈)+ update_test(键分派)+ kitty 真机。
- 验收:单测覆盖光标/编辑/undo 状态机;真机可用。

### 1.4 Markdown 轻渲染(assistant 内容)⏳ 待实施
- 不做完整引擎;文本级增强:粗体/斜体行内标记、行首 `#/##` 标题加粗、
  `` `code` `` 行内代码与代码块用等宽+底色调、`-`/`1.` 列表保留缩进。
- 实现位置:render.go 渲染管线的文本样式层(与物理行折行解耦,先折行后按 token 着色)。
- 验收:常见 markdown 回复在会话流可读;无 ANSI 泄漏、折行宽度不破。

### 1.5 鼠标划选复制(应用内选择 + OSC52)✅ 已实施
- 背景:gah 处于 alt-screen + mouse tracking,终端本地选择被应用占用——用户无法直接拖选复制。
  kitty 等按住 Shift+拖选可走终端本地(零代码),但跨终端不一致,故自实现划选。
- **已落地**:会话流内容区左键按下→拖动形成选区(跨行,反色高亮),释放若有位移 → 选中文本
  经 **OSC52**(`ESC ] 52 ; c ; <base64> BEL`)写剪贴板,状态栏短显"已复制 N 字符";
  单击/Esc 清除;与滚动条拖动按列分流(bar 列=滚动条,内容区=划选)。
  文本映射:鼠标 (x,y) → 展平物理行 + rune 列(双宽按列宽);选区排除 pad/前缀空格。
  实现:tui/model.go(选区状态/坐标/OSC52)+ tui/render.go(反色高亮)+ sel_test.go;kitty 真机拖选即复制。
- 增强(可选,⏳):双击选词、Shift 修饰转终端本地选择(有 clash 再评估)。
- 验收:kitty 拖选 → 选区高亮 → 释放即剪贴板可粘贴;单测覆盖边界。

### 1.6 工作区快速切换(对标 pi /workspace)✅ 已实施
- 入口:`/workspace`——选择器两级交互:一级 = **最近使用工作区列表**(按最近使用时间倒序,
  宿主记录于 `$GAH_HOME/sessions/workspaces.json`)+ 哨兵「输入新目录路径…」;选历史目录回车
  直接执行,选哨兵 → 二级断点输入路径;无历史记录时一级直接回退输入框。每次切换 = 新建会话。
- 语义:os.Chdir → 会话重绑(sdk.CwdSessions.SwitchProject 新 key + 自动新建空会话)→
  界面同步(清流、重放新项目空历史、统计重置)+ 状态栏工作区名/会话 id 刷新;
  旧项目历史经 `/session switch` 回溯;同项目切换仅刷新最近使用时间(no-op 不建新会话)。
- **已落地**:sdk.CwdSessions.SwitchProject/RecentProjects + host-cwd-sessions(workspaces.json
  upsert 记录 + 时间倒序,单测)+ tui /workspace 两级 Args(workspaceOptions + 哨兵)+ cmdWorkspace
  (去哨兵解析路径)+ workspace_test(切换/级联/哨兵/相对/降级)。
- 边界(收敛版,⏳ 后续):沙箱 root 与已运行外部工具进程 cwd 仍按启动工作区——工具进程重启
  (host-bridge 重载)后按新 cwd;会话/上下文/展示即时切换。
- 验收:切换后会话/上下文/展示按新项目;可切回。

### 1.7 选择/提示列表滚动窗口(超窗随光标滚动)✅ 已实施
- 问题:选择器/提示区最多 maxHintRows(6) 行,选项(如会话列表/插件/最近工作区)超窗时
  直接截断丢弃——下箭头把高亮项移出窗口后看不到剩余选项(旧:仅显示前 6 项,高亮不可见)。
- 方案:render.go 增纯函数 `pickWindow(n, cursor, visible)`——以高亮 cursor 为锚取窗口:
  cursor 触底后窗口随之下滚一行、触顶后随之上滚,光标下移时列表表现为滚动展示余项(上/下
  箭头即滚动,无额外交互);选项不足窗口时全量显示。静态命令提示(/)超窗时截前段 + 末行
  “… 还有 N 项(继续输入过滤)”,不再静默丢弃。
- 已落地:tui/render.go(pickWindow + Render 窗口化装配)+ hintwin_test(窗口数学/尾部对齐/
  高亮唯一/静态提示余量)。
- 验收:长会话列表下箭头逐项滚动到尾;上箭头回滚到首;提示余量可见。

## S2 内容与结构质量

### 2.1 消息视觉分组
- user 消息与后续 assistant 间加细分隔线(弱化 meta);长回复内部工具调用行(⚙)
  默认折叠为一行,聚焦可展开查看完整参数/结果(折叠态存 state,纯展示)。
- 验收:多轮对话结构一眼可分;工具调用不占整屏。

### 2.2 渲染代码组件化
- 把 render.go 单函数拆模块:会话流引擎(展平/折行/窗口/滚动条)、输入区、状态栏、
  提示区/选择器、markdown 样式层——各持 state 子视图,单测独立。
- 目标:后续增删 UI 特性不再"改一处动全局"(滚动问题即源于单文件耦合)。
- 验收:tui 单测覆盖每模块;Render 输出与现行为一致(基线用例)。

## S3 形态演进(决策:暂定方案 A 维持现状,其余记录在案)

### 决策记录(2025-10)
> **结论**:S3.1 主屏 scrollback 模式**暂不实施**,维持方案 A(alt-screen + 自研物理行滚动)。
> 理由(技术核查):bubbletea v2 无 append-only renderer——非 AltScreen 模式仍是 cellbuf 差分重绘
> (cursed_renderer.go Erase+Touched 固定区更新),历史不会推进终端 scrollback;"输出进 scrollback +
> 底部固定输入"需放弃 bubbletea 新写渲染器,与现有全部交互(搜索/滚轮/划选/滚动条)双轨维护。
> 与既有能力重叠(alt-screen 已可物理行滚动 + /search 回看;滚轮风暴根因 M6.16 已修)。
>
> **备选方案(记录在案,后续再研究)**:
> - **方案 B regular 双形态**:`gah --view regular`;非 bubbletea 渲染,stdout 追加输出 + 底部固定输入行,
>   滚动交终端原生。功能降级(无划选/搜索高亮/折叠);成本 ~ 与现有 TUI 等量,风险高(两套 UI 维护)。
> - **方案 C 转 M7 Web UI**:浏览器天然 scrollback/滚动,可复用会话日志;独立 XL 里程碑。
> - 若出现具体场景(长日志跟随、脚本管道、终端原生滚动习惯)再评估。

### 3.1(留档)主屏 scrollback 模式(对标 pi TuiMainScreen,暂缓)
- 入口:`gah --view regular` 或 `/view regular`(运行时切换);默认仍 alt 专注模式。
- 行为:输出流式追加到主屏 scrollback,输入行/状态行固定在视口底部;
  上滚查历史 = 终端原生 scrollback(零滚轮代码),下滚=回输入。
- 收益:长输出(日志/大回复)不占屏、天然可回看;绕开自研滚轮整类问题。
- 约束:"流式输出+底部固定输入"的追加渲染(非全屏重绘);S2.2 组件化是前提。
- 验收:regular 模式输出进 scrollback、命令交互不受影响;切换无残留。
- 状态:暂缓(见上决策记录)。

### 3.2 渲染稳度:synchronized output
- 窗口/终端支持时启用同步输出(DECSET 2026),大刷新合并减少闪烁。
- 验收:滚动/长输出重绘无撕裂闪烁;不支持的终端自动忽略。

## 不做/远期
- kitty 图形协议富媒体(图像直接上屏)、多面板布局、主题系统:
  留给 M7 Web UI(M7.2 槽位插件化)之后的统一展示层评估。

## 阶段推进建议(执行记录)
1. ✅ S1.2 + S1.1(滚动条/搜索)+ S1.5(划选)+ S1.3(输入增强)+ S1.6(/workspace)已实施。
2. ✅ S1.4(Markdown 轻渲染)已实施(markdown.go 单测 12 项;kitty 真机见渲染)。
3. ✅ S2.1(消息分组)已实施:工具行去重(EventAssistantMessage 不再双写 ToolCalls 行,
   工具行仅 EventToolCall+Result 各一条,一轮 N 工具 = 2N 行而非 3N+)、重放跨轮插细分隔线
   (turnDivider,弱化 meta 非全宽“轮次结束”),结果行已单行截断(160)。
   ⏳ 可展开折叠交互留 S2.2 组件化(需 state 折叠集合 + 鼠标单击命中折叠行)。
4. → S2.2(渲染组件化):S2.1 折叠交互在此接入。
5. → S3(形态):S2.2 完成后评估,作为独立里程碑。

## 验收总则
- 每项:tui 单测(-race)+ kitty 真机(键盘/鼠标/滚轮/拖动/搜索/历史)+ 全库 -race 绿。
- 不回归既有:物理行滚动、滚轮节流/手势、滚动条拖动、启动新会话、双按退出、输入历史/undo/编辑键。
