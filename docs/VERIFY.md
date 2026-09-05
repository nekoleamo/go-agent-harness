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
