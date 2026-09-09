# 真机验证清单(交互类,kitty 终端)

> 单测/-race 已覆盖的状态机与布局之外,下列交互依赖真实终端(键盘协议/渲染/tty 释放),
> 需在 kitty 逐项验证。自动化冒烟(CI headless 全装配回合 + 迁移 boot)已另行通过。

## P4-6 多行输入与外部编辑器(commit 4fee608)
- [ ] Shift+Enter 换行不提交(输入区出现续行);普通 Enter 仍提交整段多行
- [ ] ↑/↓ 在多行内逐行移动(列意图记忆:先右移后 ↑/↓ 停在原列)
- [ ] 单行输入时 ↑/↓ 仍为光标头/尾(不回归)
- [ ] Ctrl+G 打开 $EDITOR 编辑器整段编辑,保存退出回填;Esc/取消不丢原文
- [ ] 命令输入(/)含换行提交被拒并提示;纯空白回车不发起回合

## P4-1 消息队列(commit a43b98d)
- [ ] 回合运行中输入内容回车 → 状态栏"· 待发 N";回合结束自动逐条续发
- [ ] Esc(回合取消)后队列保留不自动发;Alt+Up 逐条取回编辑区再改
- [ ] /session switch 切换会话后队列清空(不串到新会话)

## P4-2 @文件引用 + Tab 补全(commit 3c46b0e)
- [ ] 输入 `@` + 字符 → 弹项目文件候选;↑↓ 选择、Tab/Enter 应用路径、Esc 关闭
- [ ] URL/邮箱内 @ 不触发(如 "mail@x.com")
- [ ] /workspace 切项目后候选刷新为新目录文件

## P4-8 /compact(commit 0e7a27d)
- [ ] 长会话(超预算)中 /compact → 折叠旧历史并回显摘要;短会话提示"无可压缩"
- [ ] 压缩后继续对话,模型上下文含滚动摘要(前置)

## M12 多 provider 并存(commit e866b04)
- [ ] /provider add 两次(不同域名)→ /provider show 列出两条且仅一条 ★活跃
- [ ] /provider use <名> 切换 → 状态栏模型来源/端点跟随;直接对话走新端点
- [ ] /model 枚举 → 聚合两个 provider 的全部模型(带来源);选中自动切所属 provider
- [ ] 重启后活跃 provider 与模型恢复(provider.yaml v2)

## P4-5 多级上下文(commit d62c610)
- [ ] 在上层目录放 AGENTS.md → 当前目录回合 system prompt 含"来自 <dir>"层级块
- [ ] 某级放 AGENTS.override.md → 该级替换生效

## P4-10 会话树/分支(commit 4c6b950)
- [ ] /tree 显示各会话与可 fork 提问点(seq+摘要)
- [ ] /fork <seq> → 新分支会话(自动命名 fork@seq);继续对话;回源 /session switch
- [ ] /clone → 当前会话副本独立演进
- [ ] 分支文件为 <key>-<id>.jsonl,重启后经 /session switch 可见

## P4-11 /reload(commit 53b46a4)
- [ ] 外部编辑 AGENTS.md 后 /reload → 下回合 system prompt 用新内容;删文件 → 清除
- [ ] /reload 在无改动时正常返回不报错

## P4-12 widget 槽位(commit 81fed61)
- [ ] /widgets on|off 开关;开启时输入行上方有 ◇ 行(宿主注册内容)
- [ ] widget 存在时输入行/主区布局不挤(总行数恒定)

## P3/P4 已有回归(改动波及,抽查)
- [ ] 滚轮滚动/搜索/划选/选择器过滤/多值向导 @ 共存无冲突(新键分支回归)
- [ ] P4-7 工具视觉/P4-3 代码块高亮正常(渲染改动后抽查)

## M17 审批等级三档(2026-09;`/approval open|smart|strict`)
- [ ] TUI `/approval` 无参 → 报用法;`/approval strict|open|smart` 切换成功(命令提示有三级枚举选项)
- [ ] **smart(默认)**:命中危险命令(rm -rf / git push -f / sudo / chmod 777)→ 弹确认,允许执行/拒绝拦截;无确认通道(如 headless 无 UI)时安全拒绝
- [ ] **open**:命中危险命令直接放行,不弹确认(web profile 下状态栏/设置面板审批档显示 open 且执行不弹层)
- [ ] **strict**:命中危险命令直接拒绝(结构化错误回传),不弹确认
- [ ] 归档:重启后审批档恢复上次值(偏好持久化);TUI `/approval` 与 Web 设置面板「审批」分段控件共享同一持久化(`$GAH_HOME/config/gah-state.json`)
- [ ] agent/status 状态栏或设置面板显示当前审批档(未装配 ctx.approval 时省略)

## M18 整体备份/恢复(2026-09;`/backup`)
- [ ] TUI `/backup` 一键备份 → 归档落 `$GAH_HOME/backups/gah-backup-<时间戳>.tar.gz`;`/backup list` 列出(时间倒序)
- [ ] 归档内容完整:config(含 provider.yaml 密钥)/plugins/sessions/env.sh/偏好均在;**不含 backups/ 自身**(无递归膨胀)
- [ ] `/backup <外部路径>` 备份到外部目录(不进 GAH_HOME backups/)
- [ ] 确定性:相同数据两次备份归档字节一致(gzip -n 头无时间戳)
- [ ] `/backup restore <name>`:先自动备份当前态(backups/ 新增一份快照)> 覆盖恢复;`../` 越界条目拒绝中止;坏归档显式报错
- [ ] Web 设置面板「数据备份」区段:立即备份 / 恢复最新备份(**二次确认**,恢复前自动先备份);未装配 host-backup 时 `/api/backup` 503
- [ ] 便携:整个 backups/ 随 gah-data 目录迁移,升级只替换 gah 单文件不丢备份

## M7 Web UI(commit 待定;`gah web` 起 http://127.0.0.1:2233)
- [ ] `gah web` 启动成功后**自动打开浏览器**并即见界面,首帧状态栏显示实际模型/沙箱/会话 id(非假默认)
- [ ] 端口被占用时监听失败显式报错(不再打印 listening、不弹浏览器);`GAH_WEB_OPEN=0` 不自动开
- [ ] 输入普通消息回车 → 会话流实时增量渲染(assistant 分块;工具行 ✓/✗ + 结果折叠展开)
- [ ] 回合运行中输入被禁用(禁用态 placeholder);状态栏显示"思考/执行"
- [ ] `/` 输入弹出命令提示(名称+说明);选 /jobs list 执行输出回 meta 行
- [ ] ⇄ 按钮打开会话抽屉;切换/新建后流清空并全量重放新会话历史;状态栏会话标识更新
- [ ] 审批:触发危险操作(如 sandbox read-only 下的写类工具)→ 弹层出现,允许/拒绝即时生效(智能档默认)
- [ ] 🧠 循环切换思考等级、🛡️ 循环切换沙箱档位 → 状态栏即时更新
- [ ] 断线重连(停服务 5s 再起)→ 事件流自动续传(Last-Event-ID 差集,不丢消息)
- [ ] `data.auth_token` 设置后:无 token 访问 /api/* 返回 401;带 Bearer 正常;页面静态照常
- [ ] 开发态:data.static_dir 指向 vite dev/server 产物目录 → 改前端源码即时生效(零重编译 gah)
- [ ] `POST /api/shutdown` → 200 `{"ok":true,"shutting_down":true}`;宿主优雅退出、外部插件进程(tool-*)随 DisposeAll 回收、端口释放(跨平台停机通道:Windows 无 SIGTERM 亦可用;桌面壳/运维复用)
- [ ] 附件:点击「附件」选文件/拖入外壳/粘贴图片 → chip(图片缩略图+名+大小);提交后后端 200、会话流用户消息内显示图片缩略图;`POST /api/attachments` 类型白名单拒绝(未知类型 415)、超限 413
- [ ] 输入快捷键:Enter 发送、Shift+Enter 换行、Esc 清空输入(会话抽屉开时先关抽屉);Cmd/Ctrl+Enter 亦可发送(mac/win 一致)
- [ ] TUI 输入:Ctrl+A 全选后 Backspace/Delete 一次清空/输入替换;Crtl+Y=redo;Crtl+B/F=左右移动

## 输入快捷键兼容矩阵(附件/快捷键一期)

| 环境 | ASCII 控制键(Ctrl+P/N/Z/Y/K/U/A/B/F/G)| Alt 组合(Alt+←/→ 按词)| Shift 组合(Ctrl+Shift+Z)| 全选/粘贴(浏览器原生)|
|---|---|---|---|---|
| mac WebView/Chrome/Safari | ✅ 终端标准 | ✅(Option 作 Meta,kitty/iTerm2/Terminal.app 默认)| ✅ | ✅ Cmd+A / Cmd+V |
| win WebView2/Edge | ✅ | ✅(Windows Terminal/WezTerm)| ⚠️ 部分终端不转发→Ctrl+Y 兜底 | ✅ Ctrl+A / Ctrl+V |
| win ConHost(旧 cmd)| ✅ | ❌(不转发 esc 前缀)→Ctrl+B/F 兜底 | ❌→Ctrl+Y | —(浏览器侧不存在)|
| TUI(任意终端)| ✅ | ⚠️ 见上(win ConHost 兜底 Ctrl+B/F)| ⚠️→Ctrl+Y | —(TUI 划选=OSC52)|

> 原则:Ctrl+* 使用 ASCII 控制键(所有终端通吃);Alt/Shift 组合依赖终端配置,失败场景均有 ASCII 兜底键。web 端由浏览器统一 Cmd/Ctrl 语义。

## M7.2 UI 槽位插件化
- [ ] `gah -install-ui <本地示例插件>`(web-src/examples/statusbar-demo)后重载页面 → 状态栏出现插件徽标(替换默认 statusbar)
- [ ] -list-ui-plugins 显示 id/版本/覆盖槽位;-uninstall-ui 后重载回默认;插件带 v-html 指令源码的仓库被拒装(注释提及不误伤)
- [ ] 多插件覆盖同槽位:priority 降序生效;同优先级后注册者胜

## M7.3 WebSocket 通道
- [ ] 浏览器(现代 Chrome/Firefox/Safari)打开界面 → 网络面板可见 /api/events/ws 101;消息经 WS 到达(非 SSE)
- [ ] 停 web 服务再起 → WS 关闭自动降级 EventSource(角标"重连中"→ 恢复);会话事件不丢(after 续传)
- [ ] `data.auth_token` 配置 + 前端带 token(URL ?token=)→ cookie 写入后刷新/WS 自动携带,无 401

## B5 UI 槽位 v2(扩展点)
- [ ] `gah -install-ui <extension-demo 目录>` → 重载页面:设置抽屉底部出现「插件区段」;侧栏出现「插件动作」与「附加面板」区;点「示例面板」→ 右侧抽屉打开(标题=manifest.title)
- [ ] `/api/ui-plugins` 聚合显示 extension-demo 的 3 个 v2 槽位(settings-section/sidebar-action/extra-panel);未知槽位名在 -install-ui 时被拒装;v1 插件(statusbar-demo)行为不变(向后兼容)

## M8-T2 展示联动(todo-panel 演示插件)
- [ ] `gah -install-ui ./web-src/examples/todo-panel` 后重载 → 右下角 todo 面板出现(状态栏含 ✓▶ 计数)
- [ ] 模型回合中经 todo 工具建单/推进 → 面板 5s 内自动反映(或点 ↻ 刷新);activeForm 随 in_progress 显示
- [ ] 同槽位冲突:同时装 statusbar-demo(100)与 todo-panel(120)→ todo-panel 生效(priority 降序);卸载后回退下一高优或默认

## P5.5 pty 探针自包含化修复(2026-09-08)
- [x] TestTUIProbeScrollbarAndArrow 修复:自构造 400 行超窗会话(sdk.ProjectKey 推导 key)+ 直接 `/session switch probebig` + buildGahCurrent(当前源码构建);不再依赖真实 ~/.gah/旧仓库根二进制/列表项序假设
- [x] 全部 TUI 探针(Probe/NonTTY/RealHome/ScrollSettle/ScrollbarAndArrow)统一当前源码二进制;tests 全量 -count=1 绿(51s);-race 单跑绿;全库 -race -p 2 绿——预存失败清零

## M16 测试提速专项
- [ ] 全库 `go test ./... -count=1 -race` 首跑计时 ≈74s(基线 ~100s);重复运行(带缓存)≤5s
- [ ] pty 探针冷启:机器响应快时探针测试提前通过(不再空等固定窗口)

## P5.1 剩余体验优化(2026-09-08)
- [x] 单测:markdown_test TestMdLinkInline 增 OSC8 断言(ESC]8;;url 包裹)+ stripANSI 扩展剥 OSC;input_edit_test 增 TestYankKillBuf(kill→yank→undo 全链);`go test ./tui/` 全绿
- [x] 真机 pty:`/theme` 参数级列表含 gruvbox-dark(样板文件复制到 GAH_HOME/config/themes 后生效)
- [ ] kitty 人工:md 链接下划线文字 cmd/ctrl+点击打开浏览器;Alt+P 粘贴最近 Ctrl+K/U 删除文本;
      `cp config/themes/gruvbox-dark.yaml $GAH_HOME/config/themes/` 后 `/theme gruvbox-dark` 整屏按 gruvbox 换肤

## P5.4 B2/B3 HTML 导出与分支树(2026-09-08)
- [x] 单测:host fork_tree_test(ForkAt/Clone 后 ForkTree 记录父系/主根)+ tui tree_test(树线/单次渲染/平铺回退/环保护)+ render_html_test(user/think/assistant 拼合/tool ok·err/转义/空流)
- [x] 真机:`/export /tmp/x.html` → 文件生成 DOCTYPE 完整(1057 bytes)
- [ ] 真机:`/tree` 在真实 fork/clone 会话后显示 `└─` 树形(fork@seq 会话缩进于父会话下);浏览器打开导出的 .html 观感(gruvbox 底/用户块/思考灰/工具块)

## P5.3 B1 思维块显示(2026-09-08)
- [x] 单测:compat_test TestCompleteStreamsThinking(reasoning_content → Thinking 拼合/content 互斥/聚合仅正文)+ tui thinking_test(累积独立行/折叠截断 40+/展开拼接全文/灰斜体 242+3;)
- [x] 投影零改动验证:projectLocked 仅 UserMessage/AssistantMessage 最终消息,thinking 不进入模型历史(既有测试覆盖投影语义)
- [ ] kitty 真机:deepseek 模型 + thinking high 回合 → 思维块灰斜体出现于正文前;Ctrl+T 折叠为一段+提示、再按展开全文;搜索命中思维行高亮定位
- [ ] anthropic thinking blocks 适配后置(单端点,对齐 M12 先例);web 端思维块显示未排期

## P5 TUI 视觉升级(2026-09-08)
> 对照本地 pi gruvbox-dark 的视觉整轮(见 DESIGN §14.1 P5 / TUI_OPTIMIZE 执行记录 6)。
- [x] 单测:rowbg_test(背景标注/渲染/竖线/折叠保持)+ markdown 链接引用语法增强 + turn_test(回合耗时/Ctrl+O/消息跳转)+ palette/multiline 基线同步;`go test ./tui/ -count=1` 全绿
- [x] 真机 pty(ANSI 断言,100×30,历史会话重放):user 背景 `48;5;236`、调用行背景 `48;5;235`、结果行背景 `48;5;237`、输入左缘竖线 `│` 命中
- [ ] 真机视觉确认:kitty 里 user 消息整块深灰底、工具调用带 ▍ 竖线+灰底、结果 ✓ 行带灰底(折叠 ▲ 可点)、md 链接蓝字下划线/引用灰块、输入行思考色竖线(high=紫)、状态栏空闲显示 `上一回合 X.Xs`、Ctrl+O 展开/收起结果、Ctrl+↑ 跳最早用户
- [x] 全库 `go test ./... -race -p 2` 绿(2026-09-08 探针自包含化修复后无失败;降并行防 pty 时序超窗)
