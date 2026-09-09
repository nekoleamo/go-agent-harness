# Web 附件(图片/文件)入口与输入快捷键规划

> 状态:**已确认待执行**(2026-09-06 用户确认 4 项决策;规划定稿,未开工)。
> 决策记录见 §决策记录;执行登记照旧落 DESIGN §14.1 交付表与 docs/VERIFY.md。

## 0. 目标

1. **附件入口**:web 输入框(InputBar 一体外壳)内可传图片/文件——按钮选择 + 拖放 + 粘贴图片;附件以 chip 展示(图片缩略图预览),随消息提交;**一期即打通图片进 LLM 上下文(多模态注入)**。
2. **快捷键**:输入框内可一键全选/一次删除选中;统一各平台(Cmd/Ctrl)语义;Esc 清空输入(web)+ TUI 输入区补全选/清空。
3. **兼容审计**:web 与 TUI 全部快捷键在 mac/win × 常用终端(WebView/Chrome/Safari/kitty/iTerm2/Windows Terminal/WezTerm/ConHost)的兼容矩阵;缺口给修复/fallback 键。

## 1. 现状与硬约束(代码证据)

| 项 | 现状 | 约束 |
|---|---|---|
| 消息模型 | `sdk` 层 `LLMMessage.Content string` 纯文本;openai/anthropic 适配器 wireMsg 为 `content *string`(compat.go L237/L301) | **多模态需升级消息模型 + 两适配器**,向后兼容现有 Content |
| 上传能力 | 后端无上传端点;前端无 file input | 新建端点 + 落盘 `$GAH_HOME/attachments/`(便携纪律:数据单根随目录迁移,需 Path() helper 可审计) |
| 前端输入区 | InputBar 仅 `Enter` 提交 / `Shift+Enter` 换行;全选/删除现为浏览器原生(Cmd/Ctrl+A + Backspace,无拦截,天然跨平台) | 新增键需平台归一 `(e.metaKey \|\| e.ctrlKey)` |
| TUI 输入键 | Ctrl+P/N 历史、Ctrl+Z/Shift+Z undo/redo、Ctrl+K/U kill、Alt+←/→ 按词、Home/End/PgUp/PgDn/Delete;**无全选/清空** | bubbletea Ctrl+X 跨平台归一 ✓;风险面 = Alt(meta)与 Ctrl+Shift 组合在部分终端 |
| tool-files | 存在(file 按路径读文本) | 文本附件可经工具读取;图片必须走多模态注入(本期) |
| 沙箱 | policy-sandbox 三档;read-only 拒写 | 附件落 GAH_HOME(非 workspace),"只读"沙箱下读附件应可用——需验证 ValidatePath 对 GAH_HOME 的判定 |
| 设计纪律 | taste skill:单强调色 #4176e6、圆角 token、禁 emoji、动效克制;AGENTS UI 规范(v-html 禁、二次确认、状态显眼) | 附件按钮 SVG 图标;chip/缩略图样式走 style.css token;图片消息渲染走消息分色规范 |

## 2. 决策记录(用户确认,2026-09-06)

1. **看图并入一期**:附件机制 + 多模态注入(图片进 LLM 上下文)一次交付;不做两期拆分。
2. **落盘 `$GAH_HOME/attachments/`**:数据单根,随目录迁移;`Path()` helper 可审计。
3. **Esc 清空输入**(web)+ **TUI 输入区补全选/清空**:纳入本期。
4. **兼容审计含 TUI 全量矩阵**:输出矩阵表 + fallback 键。

## 3. 一期范围(定稿)

- 后端:附件上传端点(multipart,流式,大小/类型限制,路径穿越防护)+ 静态预览 + input 注入附件引用
- 前端:附件按钮/拖放/粘贴图片/chip(缩略图+名+大小+删除);提交时先传附件再送消息;消息流渲染图片附件
- **多模态注入**:sdk 消息模型支持图片 parts;openai 适配器 `content` 数组(image_url,base64 data URI)、anthropic 适配器 `images`(base64);会话 jsonl 持久化附件;模型视觉能力开关/检测
- 快捷键:web Esc 清空、Cmd/Ctrl+Enter 等效发送;TUI Ctrl+A 全选 + 清空
- 兼容审计:F4 矩阵 + fallback 键(Alt 组合依赖终端的补 ASCII 控制键)

## 4. 实施步骤(检查清单,按依赖排序)

### F1 后端:附件上传与托管
- [ ] `web/server.go` 新增 `POST /api/attachments`(multipart 字段 `file`;流式;上限 ~20MB;类型白名单:image/*、text/*、常见文档)
- [ ] 落盘 `$GAH_HOME/attachments/<会话key或时间戳>/`(复用 home 派生 helper;`filepath.Base` 防穿越;重名加后缀)
- [ ] 附件静态托管 + 预览:`GET /attachments/…`(仅本机绑定,可经 authMiddleware 白名单;图片可内联)
- [ ] `POST /api/input` 增 `attachments?: []`(校验存在、注入引用)
- [ ] 单测:上传成功/类型拒绝/超限/路径穿越/落盘审计/input 注入/鉴权

### F2 前端:附件入口(InputBar 一体外壳内)
- [ ] 工具条左侧「附件」SVG 按钮(非 emoji)→ 隐藏 `<input type="file" multiple>`
- [ ] 拖放:文件拖入 shell 高亮收集;粘贴图片(Ctrl/Cmd+V 剪贴板含图)→ 收集
- [ ] 附件 chip 区(位于 textarea 与 bar 之间):图片缩略图(本地 URL)+ 名称 + 大小 + × 删除;token 化样式
- [ ] 提交流程:先 POST attachments 逐个上传(失败提示/移除)→ 成功后 submit 带附件
- [ ] 会话流渲染图片消息(StreamView 按消息类型分色 + 图片展示)
- [ ] taste pre-flight:无 em-dash、单强调色、图标对比度、禁用态;空态/常规态形态一致

### F3 多模态注入(看图一期,依赖 F1/模型层)
- [ ] `sdk` 消息模型升级:`LLMMessage` 增 `Attachments []LLMAttachment`(`{Kind: image, MimeType, Data([]byte|base64)}`);`Content` 保持兼容(纯文本路径零破坏)
- [ ] 附件 → 消息组装:agent-loop 注入 user 消息时携带附件(options/Input 通道扩展或 server 层组装)
- [ ] openai 适配器:`wireMsg.Content` 支持 `[]any`(文本 parts + `{"type":"image_url","image_url":{"url":"data:<mime>;base64,..."}}`);仅当消息含附件
- [ ] anthropic 适配器:`images` 数组(base64 + media_type);Content 文本保持 string
- [ ] 会话 jsonl:`Attachments` 序列化/反序列化(坏行容忍、版本兼容)
- [ ] 视觉能力:配置开关 `data.vision`(默认开)+ 模型名/能力提示(未知模型回退纯文本,frontend 显示"不支持看图"提示)
- [ ] 单测:两适配器 payload 构造(含/不含附件)、模型兼容旧字段、jsonl roundtrip、注入链路

### F4 快捷键(web)
- [ ] 保留/确认原生 `Cmd/Ctrl+A` 全选 + Backspace 一次删除;文档/placeholder 提示(⌘/Ctrl+A)
- [ ] `Esc` 清空输入:优先关浮层(命令提示/会话抽屉),再按清空输入(有内容才清)
- [ ] `Cmd/Ctrl+Enter` 等效裸 Enter 发送(mac 习惯)
- [ ] keydown 统一平台归一 `(e.metaKey || e.ctrlKey)`;与 `/` 提示、disabled 态互不干扰

### F5 TUI 输入键补全与对齐
- [ ] TUI 输入区补 `Ctrl+A` 全选(输入态)、`Ctrl+U` 已有(kill 至行首)、清空输入语义确认(Ctrl+U 已覆盖→发布说明/help 文案)"
- [ ] 与 web Esc 语义对齐:确认 TUI Esc 现有"取消进行中回合"不被清空输入覆盖;输入态 Esc 是否清空(决策:输入态 Esc = 清空输入一次,已有时不变)

### F6 快捷键兼容审计(矩阵 + fallback)
- [ ] 建矩阵表:`mac(WebView/kitty/iTerm2/Terminal.app) × win(Edge WebView2/Windows Terminal/WezTerm/ConHost)` 覆盖全部自定义键
- [ ] 高风险点:Alt+←/→(meta 序列,win ConHost 不转发/需 Option 配置)→ 补 `Ctrl+B/F`(ASCII 控制键,终端无关)fallback;`Ctrl+Shift+Z`(部分终端占用)→ 确认或补 `Ctrl+Y`
- [ ] 输出版本化结论:README/VERIFY 增「支持的终端与键位矩阵」
- [ ] kitty/真机回归(TUI 输入键 + 鼠标/OSC52 划选不受影响)

### F7 文档与纪律
- [ ] DESIGN §14.1 交付登记(附件机制 + 多模态 + 快捷键 + 兼容矩阵)
- [ ] VERIFY.md 增交互验收(上传/拖放/chip/删除/Esc/Cmd+Enter/图片渲染/视觉降级提示;矩阵引用)
- [ ] docs/README.md 登记本规划

## 5. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 多模态注入改模型层,波及面大 | Content 向后兼容 + 适配器旧字段保持;全量 -race 回归;仅含附件时切换结构化 content |
| 模型视觉能力差异(deepseek 部分模型/自定义端点可能无视觉) | `data.vision` 开关 + 能力提示;无视觉模型回退纯文本引用 |
| 大文件/多文件撑爆请求 | 大小上限 + 流式 multipart + 前端预检 |
| 附件路径注入后沙箱读取受限 | 附件落 GAH_HOME;`read-only` 下读附件属只读——F1 阶段验证 ValidatePath;必要时附件引用走只读白名单 |
| 拖放/粘贴在 Safari/WebView2 差异 | 点击选择为基线,粘贴/拖放按实测支持(掩码写明) |
| TUI Alt 组合在部分终端失效 | F6 补 ASCII 控制键 fallback,矩阵文档化 |

## 6. 工作量与提交拆分

- F1+F3(后端+多模态):L;F2(前端):M;F4+F5(快捷键):S;F6(审计):M(含真机);F7:S。
- 提交拆分:① F1(后端附件+单测)② F3(多模态注入+单测)③ F2(前端附件 UI+渲染)④ F4/F5(快捷键)⑤ F6/F7(审计+文档)。