# 插件开发规范(gah)

> 目标:后续 AI / 开发者依此文档即可开发、装配、测试、提交一个插件。
> 对应实现:微内核(`core/`)、插件 SDK(`sdk/`)、单一事实源(`plugins/catalogue`)、装配层(`bundles/`)。

## 1. 架构定位(30 秒版)

```
配置文件(profile.yaml → bundles → patch)
        │（boot: cmd/gah 按 profile 声明顺序装配）
        ▼
plugins/catalogue(全插件声明:provides/requires/bundle 归属)
        ▼ 装配层 bundles/register.RegisterInto(注入配置 data)
plugin.Registry(拓扑排序 → StartSubset(enabled) → Disposer 链)
        ▼
插件 Start(ctx, manifest) → (Disposer, error)
```

- 插件**只依赖 `sdk/`**(接口与域模型);不 import `core/`、`tui/`、其它插件。
- 注册即副作用:Start 里做的任何注册,必须由返回的 Disposer 可逆撤销;幂等。

## 2. SDK 契约(sdk/ 包)

### 2.1 必须实现的接口

```go
type Plugin interface {
    Name() string
    Start(c Ctx, m *Manifest) (Disposer, error) // 注册副作用;返回撤销器
}
```

### 2.2 Ctx 视图(插件可见的全部能力)

| 方法 | 用途 |
|---|---|
| `Provide(key, svc)` | 注册具名服务(如 `ctx.sessions`);重复注册报错 |
| `Inject(key, out *T)` | 取回服务;缺失/类型不符显式报错 |
| `Subscribe(name, fn)` → Disposer | 订阅事件(返回撤销器) |
| `Emit(ctx, name, payload, mode)` | 发事件;mode: Emit/Waterfall/Serial/Bail/Parallel |
| `Logger()` | slog 日志(宿主 fanout 到 TUI) |

### 2.3 服务键与职责(对齐 dsh `ctx.*`)

| 服务键 | 提供者 | 职责 |
|---|---|---|
| `ctx.sessions` | host-session-log | 追加式会话事件流 + 历史投影(不变量:模型可见即已记录) |
| `ctx.llm` | host-llm | 适配器注册 + 默认路由 |
| `ctx.tools` | host-tools | 工具注册 + 执行流水线(pre/execute/post/result) |
| `ctx.systemPrompt` | host-system-prompt | 提示词片段 + schema 组装 |
| `ctx.agentLoop` | host-agent-loop | 默认 ReAct 循环(可替换) |
| `ctx.sandbox` | policy-guard | 三档沙箱(ro/ws/full);**实际生效档**会再被 sync 联动与当前角色收紧 (`sdk.EffectiveSandbox`) |
| `ctx.pluginManager` | host-plugin-manager | 运行期插拔(Load/Unload) |
| `ctx.confirm` | ui-tui-app | 危险操作 y/n 确认(无实现 = 安全拒绝)
| `ctx.notices` | host-notices | 提示通道(见 §2.9):发布/回填用户提示,不进会话记录 |
| `ctx.commands` | host-commands | 斜杠命令注册表:Register/List/Get;TUI 提示/分发/help 动态来自本表 |
| `ctx.turnControl` | host-agent-loop | 回合控制:取消(Esc / `/api/control` 共用入口)+ **转向**。转向走**可选能力接口** `sdk.TurnSteerer`(`Steer(text) bool`):实现了才能注入,未实现则调用方回落「排队待发」—— 与 `SandboxSync` / `MultiProviderService` 同一种能力探测模式,插件不应 import 提供方 |

**工具可见性(可选能力,第九十一批)**:`ctx.tools` 的提供方(host-tools)额外实现 `sdk.ToolCatalogue` —— `SetFilter(visible func(sdk.ToolDefinition) bool) sdk.Disposer` + `ListAll() []sdk.ToolDefinition`。`host-roles` 在 Start 里按**当前角色**的排除清单装判定函数(每次求值现算 ⇒ 切角色即生效),卸载时经 Disposer 撤销(不留残留过滤)。契约要点:① `List()` = **模型可见**表(已过滤),`ListAll()` = **注册状态**面(给安装列表/管理面板用 —— "注册了什么"与"模型看得见什么"是两件事);② `Execute()` 也必须过滤(**只过 `List` 会让人凭历史上下文再叫一次就绕过**),拒绝文案要与"工具不存在"区分;③ `Get()` **不过滤** —— 它是裁决面(policy-guard 取真实目标工具定义做路径/审批裁决)与插件自查面;④ 未实现该接口的注册表(测试替身/极简宿主)⇒ 调用方跳过,这是"能力不存在",不是静默降级。判定用 `sdk.ToolVisible(role, name)`(运行期与展示端共用同一判据,别各写一份)。

**角色级权限收紧(可选能力,第九十二批)**:`ctx.approval` / `ctx.sandbox` 的提供方额外实现 `sdk.EffectiveApproval`(`EffectiveMode() sdk.ApprovalMode`)与既有的 `sdk.EffectiveSandbox`,并可实现窄接口 `sdk.EffectiveSource{EffectiveFrom() string}`(值 `sdk.TierSourceRole` / `sdk.TierSourceApproval`)。契约要点:① `Mode()` = **声明档**(用户设的那个),`EffectiveMode()` = **实际生效档** = 「声明档 → 审批↔沙箱 sync 联动 → 当前角色收紧」里**更严**的那个 —— 判据用 sdk 纯函数 `TightenApproval`/`TightenSandbox`,**别自己写比较**:沙箱档的松紧序与自然序**相反**(read-only 最严、full-access 最松),写反就是静默放宽权限;② 角色档**只能收紧**(`sdk.NormalizeRoleApproval`/`NormalizeRoleSandbox` 拒绝 `open`/`full-access`),所以合成只需"取更严",任何路径都不得让结果比角色声明的更松;③ **每次现算,不许缓存**:`c.Inject("ctx.roles", &rs)` → `rs.Current()` → `rs.Get(id)`,并且审批与沙箱必须用**同一个闭包**(否则会出现"审批按角色拒了、沙箱按全局放行"的裂缝)—— 缓存会重犯第八十七批 P1-3(`/plugins off|on host-roles` 后策略冻结到重启);④ 裁决要用有效档:角色收紧到 `strict` 时**直接拒绝、不弹确认框**(无人值守下确认框无人应答),路径裁决同样走 `effectiveMode()`;⑤ 展示端(TUI 状态栏 / `/approval` `/sandbox` 回显 / Web `/api/state`)读 `EffectiveMode()` + `EffectiveFrom()` 判断**来源**,**不要自己重算合成链**(必然与实际拦截行为漂),也不要写死成"联动所致"(角色收紧时那是假归因);⑥ 未实现这些可选接口的替身(旧实现/单测桩)⇒ 调用方按"无偏离"处理,文案与接口出现前**逐字一致**。

**给插件加斜杠命令**(如 host-jobs 注册 `/jobs`):Start 内**可选注入** `_ = c.Inject("ctx.commands", &cmds)`(未装配=无 TUI 场景,跳过不报错——与沙箱可选注入同模式),随后 `cmds.Register(CommandSpec{Name, Usage, Desc, Run})`;Run 返回**输出文本 + error**(输出由 TUI 显示为 meta 行);返回 Disposer 随插件卸载撤销命令;**同名冲突被拒绝**(先到先得,非静默)。插件命令自动进入 `/` 选项列表与 `/help`。

**交互式选择器**(可选增强):`Args []sdk.ArgLevel` 声明参数级联——每级要么是**枚举级**(`Options`,可动态求值:任务/插件列表,`picked` 为前几级已选值,选完进下一级),要么是**自由级**(`FreeArgs` 参数名列表,选择器断点回输入框、提示继续输入,如 `/model` 需模型名、`/provider set` 需 baseUrl/apiKey);两级皆空 = 该路径无参数,直接执行(`/help`、`/provider show`)。示例见 host-jobs 的 `/jobs`(一级 list/output/kill,二级动态任务 ID)。TUI 中:输入 `/` 自动激活列表,↑/↓ 移动、Enter 确定、Esc 退出选择。 |
| `system.registry` / `system.catalogue` | boot 注入 | 宿主内部服务(插件不可自行 import) |

### 2.4 事件命名与语义

- 名称点分:`agent/pre-step`、`tools/pre-execute`、`llm/pre-request`、`session/event`(会话事件广播)。
- 扩展点优先 **Waterfall**(返回 error = veto/拦截)。
- 持久事实用 `ctx.sessions.Append`(可回放);实时通知用 `Emit`。

**`llm/pre-request`(sdk.EventLLMPreRequest,waterfall)**:宿主 `host-llm` 在**每次模型请求的公共必经点**(`Service.Complete` 开头)发出,载荷 `*sdk.LLMRequest` —— 监听器**就地改写**即可生效(改 `Model`/`Thinking`/`Messages`/任何请求字段均在适配器调用之前);返回 error = **veto**,整个请求不发出。

```go
c.Subscribe("llm/pre-request", func(e *sdk.Event) error {
    req := e.Payload.(*sdk.LLMRequest) // 就是宿主接下来要发的那一个指针,就地改写
    return nil
})
```

与 `tools/pre-execute` 的差别与纪律:
- 它是**热路径**(每回合、每次重试之外的每个逻辑请求一次;`retry` 直调适配器,不重复发)且**无人交互** —— 只该做“填空/标注”这类确定性动作。 **长时间阻塞、网络调用、抛错都要慎用**:抛错会直接掐掉本回合(可用但要想清楚)。
- 事件在**取 `Service` 内部锁之前**发出 ⇒ 监听器可安全读宿主状态(如 `ctx.roles`);但不要在监听器里回头调 `ctx.llm.Complete`(会递归发事件)。
- **发生在模型解析之前**:若会话未配模型、而监听器填了一个,请求照样能跑(这是“角色携带模型”依赖的性质)。
- 现有订阅者:`host-roles`(角色声明的 model/thinking 在此注入,只填空不掠夺,且**永不返回 error**)。
- 想按要求来源区分路径(如只主回合生效)**本批没有 Origin 包装** —— 载荷就是裸 `*sdk.LLMRequest`;需要时先加字段再订阅,不要靠调用方自己约定。

### 2.5 结构化错误契约

工具返回 `map[string]any{"error": "..."}` 会被 host-tools 提升为 `ToolResult.Error` 字段回传模型(不中断 turn)。错误必须回传模型,不得 panic。

### 2.6 路径参数声明(能力化沙箱,**涉及文件路径的工具必读**)

宿主 `policy-guard` 在 `tools/pre-execute` 统一做路径沙箱裁决(read-only / workspace-write / full-access)。
裁决依据 = 工具**自述**的路径参数声明;未声明时依次回退:内置工具名表 → 按 `inputSchema` 参数名与工具名动词**推断** → **值级兜底**(参数值一眼是路径就按工具名的读写意图裁决);四级全空才放行(2026-09-27 审计后从 fail-open 改成这样)。

```go
func (t *myTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "save_note",
		// 声明:哪些参数是路径、读写意图如何(顶层 JSON 字段名)
		PathParams: []sdk.PathParam{
			{Arg: "target", Access: sdk.PathWrite},                 // 越界写被拒
			{Arg: "attachments", Access: sdk.PathRead, Many: true}, // 字符串数组:逐元素裁决
			{Arg: "dir", Access: sdk.PathRead, Optional: true},     // 缺省跳过(如"默认工作区根")
		},
		InputSchema: map[string]any{/* ... */},
	}
}
```

规则:
- **建议显式声明**(涉及文件路径的工具尤甚):未声明不再是"不受约束",而是进入推断/兜底档 —— 推断可能猜错(参数名 `file_id` 这类会命中、工具名没有读词时按写处理),猜错**宁可拒**,你只能看到一条带"未声明路径参数"的 veto。
- 声明非空时**以声明为准**;声明为空/缺失 → 内置名表 → 推断 → 值级兜底 —— 内置名工具无须改动,"声明空"也**无法**绕过已知工具的裁决。
- 确无路径参数的工具:设 `PathParamsDeclared: true`(显式声明位)—— 与"没写"在 JSON 里不可区分,故单列一个布尔;设了就跳过推断与兜底。
- **代理工具**(一次调用代表对另一个工具的调用,如检索模式的 `mcp_call`)还要声明 `ProxyArgsParam`(内层参数对象字段名,如 `"arguments"`):宿主据**真实目标工具**(`ApprovalTargetParam` 指向的)的声明/推断裁决内层参数 —— 不声明则内层路径不经任何裁决。
- **值级兜底的两条豁免**(避免把正常调用拒掉):命令/脚本执行器类工具(`shell`/`run_code`/`lisp_eval`/`bash`)不兜底 —— 命令体里出现绝对路径是正常写法,而命令面已由 shell 写目标扫描负责;内容/指令类参数名(`command`/`script`/`code`/`input`/`query`/`url`/`prompt`/`text`/`content`/`pattern`/…)跳过 —— `web_search{query:"/etc/hosts"}` 里的绝对路径形态是**搜索词**。两者都是“宁可少拦、不误拦”的一侧。
- 参数类型不符(路径给成数字)、必填路径参数缺失 → 显式报错 veto(非静默放行)。
- 凭据类路径(`.env`/`id_rsa`/`provider.yaml`/`~/.ssh/**` 等)任何档位、任何工具都不放行。
- 声明只走宿主↔插件协议(桥 `defDTO`/`serve.go` 已透传),**不下发模型**(适配层只取 Name/Description/InputSchema),零 token 成本。
- 该裁决是**宿主侧兜底**:工具侧自己装配了沙箱(`sdk.Sandbox`)时两层都会校验,不冲突。

**shell 命令的路径裁决(与声明无关,宿主统一施加)**:`shell` 工具的命令行会按词法扫描提取**显式写目标**——重定向(`>`/`>>`/`&>`/`<>`,含 fd 前缀)、写命令表(`rm/mv/cp/mkdir/touch/truncate/sed -i/tee/dd of=/chmod/chown/ln/tar -x/rsync` 等)、`sudo`/`env`/`nohup`/`time`/`xargs`/`exec` 前缀剥离、`eval`/`bash -c` 递归一层、`~` 展开,以及**输出型 flag**(`curl -o|--output`、`wget -O`、`gcc/clang/cc -o`、`go build|install -o`、`tar -C`、`unzip -d`、`pip install -t|--target`、`npm install --prefix`、`git clone <repo> <dir>` 的位置目标):越界写被拒;含变量/命令替换/glob 的不可裁决写目标直接拒绝(提示改写为确定路径或切 `full-access`)。重要语义:**审批通过 ≠ 放开档位** —— 用户点「允许」不会让 workspace-write 档接受越界写(与 `file_*` 一致)。
**环境 jail(档位无关,恒定生效)**:`shell` 执行把「缓存/临时根」重定向进数据根 —— `TMPDIR`/`TMP`/`TEMP`、`XDG_CACHE_HOME`、`GOCACHE`、`GOMODCACHE`、`npm_config_cache`、`PIP_CACHE_DIR` → `$GAH_HOME/jail/**`(`jail/cache/*` 复用、`jail/tmp` 每次顺带清 24h 前条目);`HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` **刻意不动**(git/ssh/gpg 要能读配置与凭据)。目录 0700,创建失败**显式报错**(安全机制不可用不静默放行);`GAH_SHELL_JAIL=0` 可整体关闭。
**档位联动与运行期开关(可选能力)**:审批档 `open`/`strict` 会覆盖沙箱**有效**档(`full-access`/`read-only`);共享沙箱实现若额外实现可选接口 `sdk.SandboxSync`(`SyncEnabled() bool` / `SetSyncEnabled(bool)`,与 `sdk.EffectiveSandbox` 同风格的**能力探测**模式),用户就能用 `/sandbox sync on|off`(或 Web 设置面板的「档位联动」勾选框)关掉这个覆盖 —— 关掉后 `EffectiveMode()` 等于声明档,拦截行为跟着变(item 与 UI 都只在实现该接口时才出现,未实现则显式回「不支持联动开关」,不假装成功)。用户选择经 `internal/prefs`(`$GAH_HOME/config/gah-state.json` 的 `sandbox_sync`,三态:nil=用 config 默认)持久化,并由**插件自己的 Start** 读回 —— 恢复点放在插件里而不是各端 UI 启动钩子,无人值守(定时任务/headless)才不会静默回退。
**审批等待(2026-09-26 起默认不限时)**:smart 档需要人裁决时,`ctx.confirm` 的等待**默认没有上限** —— 只有用户应答或 ctx 取消(回合被停止/进程退出)才结束;无应答时该动作不执行,回合也不会继续往下跑(此前是硬编 2 分钟,到点按安全默认拒绝后**继续执行后续步骤**)。要恢复「无人守候也不卡住」的老行为,配 `data.confirm_timeout_sec: <秒>`。配套(web 单 profile,不经 host-confirm-fusion):`web/confirm.go` 在超时/取消时同样推 `confirmdone` 裁决帧(否则弹层残留)、新连接建立时补推未决弹层(`ConfirmService.Pending()`)、输入区在 `busy` 时提供「停止」按钮(`POST /api/control {cancel:true}`)。`sdk.UnattendedOf` 下需审批动作仍**一律直拒且绝不弹确认**,与本默认值无关。
未覆盖(**协作层**的诚实边界):命令包装器与构建系统内部的写(`ccache`/`make`/`cmake` 自选的落点)、解释器内部写(`python3 -c "open('/x','w')"`)、`cmake --install` 不给 `--prefix` 时的默认落点、`go install` 无 `-o` 时装进 `GOPATH/bin`(`GOPATH` 刻意不重定向)、`curl -O`(按 URL 落 cwd,不越界)、变量拼出的命令文本(`CMD='rm …'; $CMD`),以及**外部进程**(MCP server 子进程、host-bridge 外部插件)的写。这些靠危险模式 + 审批档兜底。
**内核级沙箱(第 3 组,进程树层面)** 给上述边界兜底:宿主把**有效**档位经 `sdk.SandboxHint` 下发到执行入口,`shell` 在进程树级施加平台限制 —— macOS `/usr/bin/sandbox-exec`(seatbelt profile)、Linux **Landlock**(内核 ≥5.13;因 Landlock 对进程不可撤销,经**自举 helper 重新 exec** 自身后再 `syscall.Exec` 真实命令)。语义:只约束**文件写**(读与网络不限制,与协作层范围一致;唯一例外是**凭据目录的读** —— macOS 侧与协作层同一份名单在 seatbelt 里再拒一次,`GAH_SHELL_CRED_READ_KERNEL=0` 可关,Linux 无等价能力);白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**`(+ `/dev/null`、`/dev/tty`、pty 等必要设备节点);路径先 `filepath.EvalSymlinks` 解析(macOS `/tmp`→`/private/tmp`,不解析则白名单会静默失效);read-only 档**保留 jail 可写**(否则 `TMPDIR`/`GOCACHE` 断裂会让命令大面积失败);能力缺失(无 `sandbox-exec`、无 Landlock、Windows 等)→ **一次性 stderr 告警 + 不施加包装**(明示降级,不静默);`GAH_SHELL_KERNEL_SANDBOX=0` 可关闭。
**结论**:macOS/Linux 上「表判不出的写」已被内核层兜住(最坏只是错误信息不如协作层精确),**Windows 仍是纯协作式控制**。所以**工具自己拼 shell 命令时,请把路径显式传给 `shell` 而不是塞进变量**。

### 2.7 工具执行唯一入口(安全不变式,**所有调用工具的插件必读**)

工具执行只有一条合法路径:**`ctx.tools`(`host-tools` registry)**。该 registry 是 `tools/pre-execute` 的**唯一发出点**,而 `policy-guard`(审批档 + 路径沙箱)只订阅它 —— 绕过 registry 就等于绕过全部策略。

规则:
- 插件不得私接工具实现、不得自建执行路径(禁止把别的插件的工具函数直接拿来调;插件间也不允许相互 import)。
- 需要执行工具(子代理、workflow 嵌套调用、MCP server 暴露、外部插件回调等)一律经 `ctx.tools.Execute(ctx, name, args)`。
- **新增任何「能触发工具执行的入口」必须在 `tests/policy_entries_e2e_test.go` 的入口矩阵中登记**(断言:该入口委派给注入的 registry、registry 必发 `tools/pre-execute`、veto 时工具**不产生副作用**)。当前已登记:agent-loop、host-fanout、tool-workflow、mcp-server、host-bridge 宿主侧 `toolsCall`、web `/api/tools/{name}`。
  - **NOND-M1 第 2/3 步 MCP 配置端点与检索代理工具**:不新增执行路径 —— `mcp_search`/`mcp_call` 是外部插件进程内的工具实现,`mcp_call` 经插件内 `Conn.Execute` 转发 MCP JSON-RPC(不回调宿主执行其它工具);`GET /api/mcp` 的状态视图会经注入的 `ctx.tools` 调一次 `mcp_search`(**复用 web 入口**,`web/mcp_test.go` 断言它只经注入 registry;被 veto 时仅表现为工具计数缺失,无副作用);`POST /api/mcp` 只写 `$GAH_HOME/config/mcp.yaml` 并重启外部插件,不执行工具。
  - **NOND-W4 定时任务(host-schedule)**:不新增执行路径 —— 到点经 `ctx.agentLoop.Run` 提交一轮(即复用上表 agent-loop 入口),工具仍只经 `ctx.tools`。它的专属 e2e 在 `tests/schedule_e2e_test.go`(触发落会话记录 + 前缀 / **无人值守三档一律拒**需审批动作,含「有人值守必放行」灵敏度对照 / 重启保留计划 / 卸载无残留)。**新增定时/无人值守类入口时照此办理**:走 agent-loop + 在 `tests/schedule_e2e_test.go` 或同型文件里加对照用例,并补 `sdk.WithUnattended` 语义验证。
- veto 语义:订阅者返回错误即「不执行」,由 registry 转成结构化 `blocked:` 结果回传模型(不中断回合)。
- **宿主会把有效沙箱档位注入执行 ctx**:`sdk.SandboxHint{Mode, Root}`(`sdk.WithSandboxHint`/`sdk.SandboxHintOf`)。执行入口(`ctx.tools`)在 `tools/pre-execute` 之后、真正执行之前注入**有效**档位(`EffectiveSandbox.EffectiveMode()`,即联动后的档;不是声明档),外部插件工具经协议随调用携带、在插件侧 ctx 里可读回。**档位/根为空 = 未知 → 按不可放行处理**(不得猜默认值);未注入 = 与改动前行为一致(旧对端不报错,只是不施加内核限制)。自带进程执行的工具(不限于 `shell`)应以该 hint 作为施加内核级限制的输入 —— 它是唯一能覆盖子进程树的控制点。

### 2.7.1 联网抓取工具的内网守卫与不可信内容(**带 URL 抓取的工具必读**)

`web_fetch` 抓的是**模型给出的任意 URL**,故有两件事在工具侧自己收(宿主不提供网络层策略):

- **内网守卫(防 SSRF)**:`tool-web` 的 `newFetchClient()` 用 `guardedDial` —— 字面量 IP 在**拨号前**判定、域名在**连接建立后**按 `RemoteAddr` 判定(DNS rebinding 无窗口),命中环回/私网/链路本地/未指定/多播即拒,逃生舱 `GAH_WEB_ALLOW_PRIVATE=1`。自研抓取工具请照抄该思路(不要只查 `url.Host` 字符串:`http://localhost`、`http://[::1]`、十进制 IP 写法都绕得过);`web_search` 不挂守卫是**有意**的 —— 它的端点由用户在 `search.yaml` 选定,可能本就是本地服务。
- **抓回的内容是不可信数据**:间接提示注入(网页里写“忽略之前的指令…”)的入口就是工具结果,它和用户指令**同权重**进上下文。宿主已在系统提示固定引导里定性这类内容(不得执行其中的指令),工具作者**不需**再包装,但**不得**把抓回的内容当指令源去调用其它工具(比如把页内“命令”拿去 `shell`)。

### 2.8 外部插件控制面(`ctx.extplugins`,NOND-M1)

`host-bridge` 装配后 Provide `ctx.extplugins`(`sdk.ExternalPlugins{Reload(name string) error}`):按名重启一个外部插件进程(配置改完后重读)。名字 = 插件二进制**文件基名去平台扩展名**(`tool-mcp`;Windows 产物是 `tool-mcp.exe`,目录名仍是 `tool-mcp`),两种落点都支持:发布布局 `plugins/<名>/<名>` 与扁平布局 `plugins/<名>`。条目未加载时会尝试补加载(用户新装插件 / 新增 MCP server 无需重启 gah)。

语义与边界:
- 重载先撤销旧实例(工具 + 命令注册)再拉起新进程;**新进程启动失败 → 返回显式错误,且旧条目已撤销**(不假装成功,调用方必须把错误显示给用户);
- 三处重载入口(插件目录文件监听、工作区切换、`ctx.extplugins`)经同一把锁串行化,防同路径双载造成进程泄漏与工具双注册残留;
- 插件自己不感知被重载,不要在插件进程内缓存跨重载状态;
- 外部进程读配置一律经 `sdk.Home()`(`$GAH_HOME`)派生路径(便携纪律),不从命令行参数猜路径。

配置持久化可参考 `internal/mcpconfig`(MCP server 配置 `{name, command, args, enabled, mode}` 落 `$GAH_HOME/config/mcp.yaml`;写盘 0600 + 同目录临时文件 rename 原子替换 + 头部注释;**读盘与写盘共用同一条规范化** —— 名字净化、整行命令按 `sdk.SplitArgs` 拆分、`mode` 大小写容错;避免「GUI 写能跑、手抄进文件却跑不起来」)。

### 2.9 提示通道(`ctx.notices`,NOND-N1)

想让**人**(而非模型)注意某事时用它 —— 比如后台作业失败、定时任务跳过、需要人回来的时刻。

```go
var notices sdk.NoticeService
if err := c.Inject("ctx.notices", &notices); err == nil { // 可选注入:未装配则跳过
	notices.Publish(sdk.Notice{
		Level:  sdk.NoticeError, // info|warn|error(非法值归一 info)
		Title:  "备份失败",       // 一句话(折叠单行;空则回落正文首行)
		Body:   err.Error(),    // 细节(可空)
		Source: "host-backup",  // 发布者(缺失填 unknown)
		Key:    "backup:" + id, // 可选去重键:同 Key 60s 内只出一条
	})
}
```

纪律(不是风格偏好,是这套通道能成立的前提):
- **不进会话记录**:不落 `jsonl`、不进模型上下文、不计 token。它与 `ctx.sessions.Append` 是两个正交的概念(持久事实 vs 瞬时信号)。
- **`Publish` 返回 `0` = 被去重丢弃**(不是失败);被去重的条数经 `NoticePage.Suppressed` 与 debug 日志可见 —— 不静默压掉。
- **同一件事用同一个 `Key`**,不同事件必须不同 `Key`(否则会被误当重复丢掉);`Key` 取事件 id/错误首行,不要取时间戳(那样永远不去重)。
- **不要用它当第二条命令通道**:端只订阅 `sdk.EventNotice` 事件 / 拉 `GET /api/notices`,不要各端各写一份旁路逻辑。
- **噪音纪律**:自动化高频路径上只在「人需要回来」的时刻发(如 `schedule/run` 只报 failed/skipped —— 每次成功都发会退化成每日噪音)。
- **呈现强度由端各自决定**:同一份载荷在 Web 是右下 toast、在 TUI 是状态栏项(`notice`)/`/notice` 浮层;不要假设接收端会怎么显示,也不要在这里做端特定格式化(标题请自带一句人话)。
- 提示是**进程内环形缓冲**(200 条),重启即空;**不保证回放**,端按 `id` 去重与续传。

## 3. 开发步骤(七步)

### 3.1 决策:是否插件?
业务能力一律走插件(微内核红线)。类型:llm / tool / policy / agent / host / ui。

### 3.2 建目录
`plugins/<type>-<name>/`,包名可简写(如 tool-shell → toolshell)。

### 3.3 实现 Plugin

```go
// plugins/tool-demo/demo.go
package tooldemo

type Plugin struct{}

func (p *Plugin) Name() string { return "tool-demo" }

func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
    var tools sdk.ToolRegistry
    if err := c.Inject("ctx.tools", &tools); err != nil {
        return nil, err // 缺依赖显式失败
    }
    d := tools.Register(&demoTool{})
    return d, nil // 注册即副作用;dispose 撤销注册
}
```

工具实现 `sdk.Tool`:`Definition() sdk.ToolDefinition`(MCP 兼容:name/description/inputSchema JSON Schema)+ `Execute(ctx, argsJSON) (any, error)`。

### 3.4 登记 catalogue
`plugins/catalogue/catalogue.go` 加 Def:

```go
"tool-demo": {Factory: func() sdk.Plugin { return &tooldemo.Plugin{} }, Manifest: &sdk.Manifest{
    ID: "tool-demo", Type: "tool", APIVersion: ">=1.0,<2.0",
    Requires: []string{"ctx.tools"}}, Bundle: "base"},
```

- Provides = 它提供的服务键;Requires = 它依赖的服务键(拓扑排序依据)。
- Bundle 归属决定由哪个 profile 装配(base 默认;tui 界面类)。

### 3.5 配置条目(启停/参数)

> **样板演进规则**:新增/修改 **base 能力条目**(host-* 等)时,必须同步:① `config/bundle-*.yaml` 顶部 `# seed-version: N` **+1**(老用户自动升级,备份后覆盖);② 同步 `internal/embed/seed/bundle-*.yaml`(guard 测试强制一致);③ 登记 catalogue。(profile/patch 属用户配置偏好,不改版本号,不自动覆盖。)
`config/bundle-base.yaml` 加条目(可 `enabled: false` 默认关闭;参数进 `data:`):

```yaml
- id: tool-demo
  data:
    retries: 3
```

### 3.6 单测
- 同包单测 + 标准库断言;装配其它插件时用**黑盒测试包**(`package xxx_test`)防 import 环。
- 端到端加在 `tests/`(装配 base + `system.*` 注入服务,见 AGENTS.md 测试惯例)。

### 3.7 验证与提交
`go build ./... && go vet ./... && go test ./... -race` 全绿后提交;更新 AGENTS.md/会话状态不必要,但 catalogue 变更必须提交。

## 4. 生命周期与热重载
- 卸载 = Disposer 链逆序执行(后注册先撤销)。
- 运行期插拔:`host-plugin-manager`(plugins/manager.go)提供 Load/Unload;TUI `/plugins on|off`。
- **结构性防护**:卸载被已加载插件依赖的插件会被拒绝(`BlockedByLoaded`,显式报错:先卸依赖者或走配置切换)——结构性服务(host-tools 等)建议配置层切换(重启生效)。
- **工具同名注册**被忽略并记录警告(不静默):替换同名工具 = 先关闭旧提供者插件,再启用新插件。
- 外部进程插件(崩溃隔离):host-bridge(自建 stdio + net/rpc,见 §4.1)/ mcp-bridge(MCP stdio),见各自包注释与测试。

### 4.1 外部插件开发(外部化形态)

形态定位:独立二进制(崩溃隔离/独立升级),随包 embed 释放、host-bridge 扫描加载(M6.9 起工具类全部走此形态)。最小参照实现:`extplugins/tool-echo`(约 50 行)。

**入口与握手**(M14 起可带命令)
```go
func main() {
    hostbridge.ServeTools(map[string]sdk.Tool{
        "echo": &echoTool{},
    }, map[string]sdk.CommandSpec{ // M14:可选(不传 = 纯工具插件,旧行为不变)
        "echo": {Name: "echo", Usage: "/echo <文本>", Desc: "...",
            Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"文本"} }}},
            Run: func(args []string) (string, error) { return strings.Join(args, " "), nil }},
    })
}
```
- 唯一入口 `ServeTools(tools, commands...)`(外部进程服务端;宿主同仓库编译,import `plugins/host/host-bridge` 的 serve.go 符号或按 extplugins 现有写法)。
- **传输层自建(SZ-1,2026-09-18)**:stdio + net/rpc(gob),不再依赖 hashicorp/go-plugin(旧版把 gRPC/protobuf/yamux/hclog 整栈拉进每个插件)。协议方法面不变,只有两点要遵守:
  - 握手:宿主注入 `GAH_PLUGIN=gah-external-tool`,插件启动后**先向 stdout 写一行** `GAH-PLUGIN|2|stdio`(由 `ServeTools`/`ServeRPC` 自动完成),随后 stdin/stdout 即 RPC 流。
  - **除握手行外不得往 stdout 写任何东西**(会污染 gob 流);插件日志一律走 stderr(宿主的加载失败错误会把它带出来)。
  - 自定义 RPC 服务实现(非 `sdk.Tool` 表,如 `extplugins/tool-echo`)直接 `hostbridge.ServeRPC(&myServer{})`,服务名固定 `Plugin`(宿主调用 `Plugin.ExecuteNamed` 等)。
  - **旧版(go-plugin 时代)插件产物无法握手**,宿主会报明确错误(含升级指引):随包产物由首启 sha256 覆盖升级,自装插件需按本节重新编译。

**外部命令桥(M14,免编译加命令)**:外部插件声明的命令经 host-bridge 自动转注册进 `ctx.commands`(与进程内插件命令同表),**新命令插件丢进 `$GAH_HOME/plugins/` 即生效,无需重编译 gah**(目录 fsnotify 热重载、崩溃自动拉起复用既有机制)。语义:
- 声明即注册:Args 级联照常(枚举级 `Options` 运行期经桥 RPC 求值、自由级 `FreeArgs` 触发断点向导);执行 `Run` 在外部进程内完成,输出文本+error 回宿主 TUI meta 行。
- 同名命令冲突:先到先得,拒绝并记警告(被跳过命令不注册,插件继续加载);插件卸载/热重载随 Disposer 撤销。
- TUI 侧零改动:`/` 提示、`/help`、选择器断点全部自动来自 `ctx.commands` 注册表。
- 参照实现:`extplugins/tool-echo`(工具 + `/echo` 命令共存,约 80 行)。
- **纯命令插件**:外部插件可只提供命令(无工具)——宿主扫描识别 `tool-*` 与 `cmd-*` 前缀(文件名 `cmd-<名>`),`ServeTools(nil, commands)` 直接可用;loadOne 工具或命令任一满足即加载。
- **命令超时**(可选):`sdk.CommandSpec.TimeoutMs`(毫秒)声明执行/枚举选项 RPC 超时,0 = 宿主全局默认 3s——死进程/慢命令不会阻塞 TUI 线程(超时即显式报错,连接类错误仍触发自动拉起)。

**桥协议**
- 多工具(新协议):`Definitions` 枚举 + `ExecuteNamed` 按名执行;旧单工具协议(`Definition`/`Execute`)宿主自动回退兼容。
- 工具级超时:定义声明 `TimeoutMs`(毫秒),覆写宿主全局默认 3s;宿主同时把 ~80% 的该值下发插件侧作执行超时(插件先自行结束并回明确错误)。
- **执行可中断(必读)**:宿主为每次调用生成 `CallID` 并下发(`ExecNamedArgs.CallID/TimeoutMs`);用户中断回合(Esc / Web 停止)或宿主超时时,宿主经 `Plugin.Cancel(CallID)` RPC 通知插件。插件侧由 `ServeTools` 自动登记可取消 ctx —— 因此
  **`Execute(ctx, args)` 必须尊重 ctx**(长耗时/阻塞操作要 `select ctx.Done()`、把 ctx 传给子进程/网络调用),否则用户的取消只能等宿主超时兜底。
  旧插件无 `Cancel` 方法:宿主忽略方法缺失,行为退化为"宿主侧超时"(不报错、不崩)。

**宿主回调通道**(仅服务调用,不桥事件 veto)
- 环境注入:`GAH_CB_ADDR`(宿主回调地址)+ `GAH_CB_TOKEN`(鉴权,回传校验)。
- 可用:tools.execute/list、jobs.run/output、fanout.agent/parallel/pipeline;宿主未装配对应服务时返回显式错误(不静默)。

**沙箱上下文注入(内核级写限制,2026-09-27 审计 A3 / A3b)**
- 环境注入:`GAH_EXT_SANDBOX_MODE`(宿主**有效**档位)+ `GAH_EXT_SANDBOX_ROOT`(工作区根);宿主未装配沙箱或无档位时**不注入**(插件必须按"未注入"处理,不得假定档位)。宿主也会给**被包装**的插件进程注入 `GAH_KERNEL_SANDBOXED=1` 作为"已在内核沙箱内"标记(后代不得重复施加)。
- **宿主对插件进程本身施加内核包装(默认开,A3b)**:按当前有效档位把插件进程起在 seatbelt(macOS)/Landlock(Linux)里,白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**` + 包管理器缓存与系统临时目录 + **插件自报**的数据目录;读与网络不限。开关:`GAH_EXT_PLUGIN_SANDBOX=0` 全局关、`GAH_EXT_PLUGIN_RW_PATHS` 追加写路径、`GAH_EXT_PLUGIN_CRED_READ_DENY=1` 开凭据读拒(默认关)。
- **两种自报能力(`bridge.ServeToolsWith`)**(2026-09-27 审计 A6 修订:原来的 `SandboxProvider` 已**撤除**,见下方说明):
  - `CredentialReadDeny: true` —— "本插件进程内会执行用户 shell 命令"(典型:shell 提供者)。宿主据此默认给该插件进程开凭据目录读拒绝,与 `tool-shell` 自己的 `GAH_SHELL_CRED_READ_KERNEL` 同口径。**为何需要这条**:进程被外壳包装后,插件自己那份读拒绝会因 `GAH_KERNEL_SANDBOXED` 标记而跳过(见下方"不可嵌套"),不声明就等于把 F1 的凭据读拒静默丢掉。不声明也没有安全洞:读拒绝仍可由 `GAH_EXT_PLUGIN_CRED_READ_DENY=1` 打开。
  - `DataWrites: ["memory", "todos"]` —— 需要**直写**的数据根**直接子目录名**(相对 `$GAH_HOME`)。宿主校验后并入白名单:只接受直接子目录名,`config`/`plugins`/`ui-plugins`/绝对路径/`..`/嵌套一律**丢弃并记 ERROR**(声明来自被约束方,不能让它自行申请 `provider.yaml` 的写权;保留目录名按**大小写折叠**判定 —— macOS/Windows 默认卷上 `Config` 就是 `config`)。不声明就只能写缓存/临时区/workspace;需要别的目录时用户用 `GAH_EXT_PLUGIN_RW_PATHS` 点名。
  - 声明怎么被读到:宿主在 `exec` **之前**跑一次 `插件 --gah-caps`(短命探测,打印能力 JSON 后即退,不建 RPC/不调回调)—— 包装 argv 必须在 exec 前定下来,而 RPC 只能在进程起来后。**不实现 `--gah-caps` 也没关系**:探测失败 = 未声明 = 按普通插件包装(安全侧默认;旧插件不受影响)。
- **档位/工作根变更 = 先重建再执行**:被包装插件的 profile 是启动时的静态串,所以宿主在每次工具/命令调用前比较启动快照与当前档位/根;不一致就**同步重建插件**(持重载锁,失败才拒)并在新 profile 下执行本次调用。插件作者无需处理,切档/切工作区后的第一次调用只是稍慢一点(不报错)。
  - 为何不是"先拒一次再异步重载"(A3b 原设计,已改):把 `tool-basic` 也纳入包装后,`/ws` 切根会让第一次 `shell`/`file_*` 调用必失败(e2e `TestExternalFileChangeLandsInLedger` 实测报出),而安全收益为零 —— 执行的始终是新 profile,不存在"拿旧档跑完"的窗口。
- 纪律:裁决是**写**侧(读与网络不限),档位语义与 `sdk.SandboxMode` 一致;未声明能力的**第三方插件**需要白名单外的写时,只能由用户经 `GAH_EXT_PLUGIN_RW_PATHS` 点名;不提供 deny-list 型弱沙箱(与 A3 立意相反)。
- 不可嵌套:被施加包装的进程会拿到 `GAH_KERNEL_SANDBOXED=1`,后代**不得**重复施加(seatbelt/Landlock 均不可嵌套,实测 `sandbox_apply: Operation not permitted`)——`internal/kernelsandbox.Wrap` 已内置该判定。**这条也是 `SandboxProvider` 被撤除的原因**:外层包装与插件自施加可安全叠加(外壳被标记跳过),所以无需为"自己会套"的插件开豁免口;不撤则其进程内直写(数据根、`file_*` 工具)只能靠协作层裁决,与 A3/A3b 的立意相左。

**五件查过、完好的承重属性**(2026-10-03 补;A 组。它们**没有**出现在下面的「开发步骤」里,
所以插件作者常以为自己做错了什么 —— 其实宿主早就在替你兜着。写在这里是为了别再花时间怀疑):

1. **`tools.execute` / `jobs.run` 回调**必过 `tools/pre-execute`。也就是说经回调通道起的
   shell 一样要过**危险命令审批与路径裁决** —— 你不需要在插件里自己实现那套。
2. **工具同名 first-wins,且三面可见**:后注册的同名工具被跳过,但冲突通过 `ToolConflicts`
   暴露(TUI / 面板 / 日志都能看到)。不要指望"覆盖掉宿主内置工具"。
3. **命令同名冲突记 ERROR 并跳过,内置永远优先** —— 与工具相反(工具是 first-wins)。
   两边规则不同不是笔误:命令表由用户在 `/` 里按名字选,静默覆盖会让用户敲错名字时
   得到另一个命令的结果。
4. **`DataWrites` 走 `validDataWrites` 校验**;**不声明能力 = 按普通插件包装**(只能写
   缓存/临时区/workspace)。想自己申请 `provider.yaml` 的写权是不可能的(见上文)。
5. **「未声明路径参数」≠「不裁决」**:宿主按 schema 与实际取值**推断**哪些参数是路径,
   仍然裁决。你不声明,只是拿不到"用户明确声明过这个参数"的加固,不是"绕过路径裁决"。

**进程型插件拿不到确认通道(P7,2026-10-03 查实)**:刻意**不**把 `ctx.confirm` 桥接给外部进程插件 ——
确认是一个**跨进程的人工交互**,桥过去要么阻塞整个宿主等一个看不见的弹窗,要么静默超时。
能力缺口,不是漏洞。想触发审批只有一条路:**经 `tools.execute` 间接走 `tools/pre-execute`** ——
也就是说:你的插件里起 shell,不要自己去判断"这条命令危不危险"。

**生命周期:随时可停用(2026-10-03,批二)**:`ctx.extplugins` 多了 `Disable/Enable/List`。
**停用**按**二进制**而不是按角色(一个 `tool-kit` 提供四个角色,用户的心智单位是那一件);
停用之后**重开不需要重新 trust**(哈希仍匹配),但**重新启用会重验漂移**(你当初记的 tag
若已被 force-push 过,启用会被拒并给出路)。回合执行中停用 ⇒ 正在跑的工具调用会因 RPC 断连
给一个可读错误且可重试,**宿主不崩** —— 你不需要为"被中途停掉"写任何善后逻辑。

**错误与退出语义(P3 软降级)**
- 业务失败回 `reply.Error`(结构化),回传模型、不中断宿主 turn。
- 插件加载/启动失败 = 自身被跳过(宿主 ERROR 日志,继续 boot);**exit code 不向宿主传语义**——缺配置必须在 stderr 显式说明后 `exit 1`(防静默空转,参照 extplugins/tool-mcp 的 GAH_MCP_COMMAND 模式)。
- 运行期崩溃:调用转结构化错误,宿主 60s 节流自动拉起。

**平台与构建(P4)**
- 插件二进制必须与宿主同平台;`scripts/gen-extplugins.sh` 按发行矩阵(darwin/linux × amd64/arm64 + windows/amd64)构建,embed 分平台打包(主包每目标只嵌本平台产物)。
- 需要限制子进程写入的插件:用根模块的 `internal/kernelsandbox`(**仅进程内插件可用**;外部插件只 import `sdk`,故只能经上文的沙箱上下文变量 + 自己实现——`tool-basic` 就是这样给 shell 施加的;进程被宿主包装时,它内部那次 `Wrap` 会因 `GAH_KERNEL_SANDBOXED` 标记返回 nil,由外层 profile 统一生效)。
- 新增外部插件:加进脚本的 NAMES 列表 + catalogue 登记;构建链产物缺失时主包构建失败(防漏,勿手动删除 embed 产物目录)。

**验收路径**
- 单测参照 `plugins/host/host-bridge/bridge_test.go` 的 `buildExternalPlugin` 模式(测试内 `go build` 产物再装配断言崩溃隔离/软降级)。
- 集成端到端见 `tests/external_test.go`(`releaseExt` 释放 + 真实回合走回调通道)。

### 4.2 UI 插件(web 槽位)的信任模型

UI 插件以 `$GAH_HOME/ui-plugins/<id>/` 落盘,manifest 声明槽位覆盖(stream/input/statusbar/confirm/settings-section/sidebar-action/extra-panel),前端经 `web-src/src/plugins.ts` 动态 `import()` 载入。

**关键事实:UI 插件与主应用同源同 realm,因此拥有与主应用相同的权限** —— 可读取页面上的全部会话内容、可用 cookie 调全部 `/api/*`(含 `/api/input`,即向模型投喂 prompt → 经工具执行等同本地代码执行)。这与外部插件二进制「与宿主进程同权限」等价。

- 安装 = `gah -install-ui <repo|本地目录>`(**自动登记进完整性闸**)。
- **完整性闸默认强制(2026-10-03,批四)**:`ui-plugins/SHA256SUMS` 由 `ui-web-app` 在 boot 时
  **无条件**创建。手工拷一个目录进 `ui-plugins/` **不再直接可用** —— 这是本项目最宽的面上
  唯一一处被主动断掉的老路。放行:`gah -trust-ui-plugin <id>`(只登记**当前那一份**的
  entry-scope 摘要;清单里已有**不同**摘要 ⇒ 显式拒绝,不自动洗白)。
- 合格线 = **manifest + 槽位声明会被 `import()` 的模块**,不是整目录:改 sourcemap 或
  无关 chunk 不该被拒,但改 `manifest.json` 的槽位指向会(它必须在内 —— 它定义指向)。
- 停用 = **不下发**(前端根本不加载);文件与闸条目留着,重新启用不需要重新登记。
- 客户端开发时若发现"插件不加载",看日志与设置面板的说明行 —— 那是闸在起作用,不是坏了。
- 纵深:SPA 响应带严格 CSP(`default-src 'none'` + 显式白名单,`connect-src 'self'`),切断「纯外发」通道(要绕过需注入,成本陡增);`/api/ui-plugins` 响应带 `trusted`/说明字段,设置页有同款提示。
- 规划中(未实施):`iframe sandbox` + postMessage 能力桥 + manifest `permissions` 声明 —— 仅当 UI 插件成为**网络分发**面才值得做。
- 开发约束不变:渲染层禁 `v-html`,组件不得散写裸色值(见 AGENTS.md「UI 规范」)。

## 5. 检查清单(提交前)
- [ ] 只 import sdk;无 core/tui/其它插件 import
- [ ] Start 返回的 Disposer 可逆且幂等(注册的每个副作用都有撤销)
- [ ] 缺依赖显式报错,不静默
- [ ] catalogue 已登记(provides/requires/bundle 正确)
- [ ] config 条目已加(含 enabled/data)
- [ ] **便携纪律**(见 AGENTS.md「便携纪律」):任何写盘路径以 `$GAH_HOME` 为根(注意 GAH_HOME 是 boot 内部贯通变量,**数据根唯一 = 二进制同级 gah-data/**,用户不可经 env 指定);禁用硬编码 ~/.gah、cwd 相对写、系统根/散目录;密钥入 config/、env 入 gah-data/env.sh;新增路径 helper 可审计
- [ ] 涉及文件路径的工具:已声明 `ToolDefinition.PathParams`(§2.6)
- [ ] 工具执行只经 `ctx.tools`(§2.7);新增「可触发工具执行的入口」已登记入口矩阵测试
- [ ] 联网抓取类工具:已考虑内网守卫(SSRF)与「抓回内容不可信」(§2.7.1)
- [ ] UI 插件型:未把「同源同权限」当作安全边界(§4.2)
- [ ] 单测通过;-race 全绿
- [ ] 错误回传模型(结构化 error),不 panic
- [ ] 外部插件型:握手/协议/回调/退出语义(§4.1)已符合;产物已编入 scripts/gen-extplugins.sh 的 NAMES
