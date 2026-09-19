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
| `ctx.sandbox` | policy-sandbox | 三档沙箱(ro/ws/full) |
| `ctx.pluginManager` | host-plugin-manager | 运行期插拔(Load/Unload) |
| `ctx.confirm` | ui-tui-app | 危险操作 y/n 确认(无实现 = 安全拒绝)
| `ctx.notices` | host-notices | 提示通道(见 §2.9):发布/回填用户提示,不进会话记录 |
| `ctx.commands` | host-commands | 斜杠命令注册表:Register/List/Get;TUI 提示/分发/help 动态来自本表 |

**给插件加斜杠命令**(如 host-jobs 注册 `/jobs`):Start 内**可选注入** `_ = c.Inject("ctx.commands", &cmds)`(未装配=无 TUI 场景,跳过不报错——与沙箱可选注入同模式),随后 `cmds.Register(CommandSpec{Name, Usage, Desc, Run})`;Run 返回**输出文本 + error**(输出由 TUI 显示为 meta 行);返回 Disposer 随插件卸载撤销命令;**同名冲突被拒绝**(先到先得,非静默)。插件命令自动进入 `/` 选项列表与 `/help`。

**交互式选择器**(可选增强):`Args []sdk.ArgLevel` 声明参数级联——每级要么是**枚举级**(`Options`,可动态求值:任务/插件列表,`picked` 为前几级已选值,选完进下一级),要么是**自由级**(`FreeArgs` 参数名列表,选择器断点回输入框、提示继续输入,如 `/model` 需模型名、`/provider set` 需 baseUrl/apiKey);两级皆空 = 该路径无参数,直接执行(`/help`、`/provider show`)。示例见 host-jobs 的 `/jobs`(一级 list/output/kill,二级动态任务 ID)。TUI 中:输入 `/` 自动激活列表,↑/↓ 移动、Enter 确定、Esc 退出选择。 |
| `system.registry` / `system.catalogue` | boot 注入 | 宿主内部服务(插件不可自行 import) |

### 2.4 事件命名与语义

- 名称点分:`agent/pre-step`、`tools/pre-execute`、`session/event`(会话事件广播)。
- 扩展点优先 **Waterfall**(返回 error = veto/拦截)。
- 持久事实用 `ctx.sessions.Append`(可回放);实时通知用 `Emit`。

### 2.5 结构化错误契约

工具返回 `map[string]any{"error": "..."}` 会被 host-tools 提升为 `ToolResult.Error` 字段回传模型(不中断 turn)。错误必须回传模型,不得 panic。

### 2.6 路径参数声明(能力化沙箱,**涉及文件路径的工具必读**)

宿主 `policy-guard` 在 `tools/pre-execute` 统一做路径沙箱裁决(read-only / workspace-write / full-access)。
裁决依据 = 工具**自述**的路径参数声明;未声明时回退内置工具名表(`file_read/file_write/file_append/file_edit` 等)。

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
- **工具名不在内置表中(自定义名如 `save_note`)必须声明**,否则路径参数不受沙箱约束(未声明工具按"非路径工具"处理,不拦)。
- 声明非空时**以声明为准**;声明为空/缺失才回退内置名表 —— 内置名工具无需改动,"声明空"也**无法**绕过已知工具的裁决。
- 参数类型不符(路径给成数字)、必填路径参数缺失 → 显式报错 veto(非静默放行)。
- 凭据类路径(`.env`/`id_rsa`/`provider.yaml`/`~/.ssh/**` 等)任何档位、任何工具都不放行。
- 声明只走宿主↔插件协议(桥 `defDTO`/`serve.go` 已透传),**不下发模型**(适配层只取 Name/Description/InputSchema),零 token 成本。
- 该裁决是**宿主侧兜底**:工具侧自己装配了沙箱(`sdk.Sandbox`)时两层都会校验,不冲突。

**shell 命令的路径裁决(与声明无关,宿主统一施加)**:`shell` 工具的命令行会按词法扫描提取**显式写目标**——重定向(`>`/`>>`/`&>`/`<>`,含 fd 前缀)、写命令表(`rm/mv/cp/mkdir/touch/truncate/sed -i/tee/dd of=/chmod/chown/ln/tar -x/rsync` 等)、`sudo`/`env`/`nohup`/`time`/`xargs`/`exec` 前缀剥离、`eval`/`bash -c` 递归一层、`~` 展开,以及**输出型 flag**(`curl -o|--output`、`wget -O`、`gcc/clang/cc -o`、`go build|install -o`、`tar -C`、`unzip -d`、`pip install -t|--target`、`npm install --prefix`、`git clone <repo> <dir>` 的位置目标):越界写被拒;含变量/命令替换/glob 的不可裁决写目标直接拒绝(提示改写为确定路径或切 `full-access`)。重要语义:**审批通过 ≠ 放开档位** —— 用户点「允许」不会让 workspace-write 档接受越界写(与 `file_*` 一致)。
**环境 jail(档位无关,恒定生效)**:`shell` 执行把「缓存/临时根」重定向进数据根 —— `TMPDIR`/`TMP`/`TEMP`、`XDG_CACHE_HOME`、`GOCACHE`、`GOMODCACHE`、`npm_config_cache`、`PIP_CACHE_DIR` → `$GAH_HOME/jail/**`(`jail/cache/*` 复用、`jail/tmp` 每次顺带清 24h 前条目);`HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` **刻意不动**(git/ssh/gpg 要能读配置与凭据)。目录 0700,创建失败**显式报错**(安全机制不可用不静默放行);`GAH_SHELL_JAIL=0` 可整体关闭。
**档位联动与运行期开关(可选能力)**:审批档 `open`/`strict` 会覆盖沙箱**有效**档(`full-access`/`read-only`);共享沙箱实现若额外实现可选接口 `sdk.SandboxSync`(`SyncEnabled() bool` / `SetSyncEnabled(bool)`,与 `sdk.EffectiveSandbox` 同风格的**能力探测**模式),用户就能用 `/sandbox sync on|off`(或 Web 设置面板的「档位联动」勾选框)关掉这个覆盖 —— 关掉后 `EffectiveMode()` 等于声明档,拦截行为跟着变(item 与 UI 都只在实现该接口时才出现,未实现则显式回「不支持联动开关」,不假装成功)。用户选择经 `internal/prefs`(`$GAH_HOME/config/gah-state.json` 的 `sandbox_sync`,三态:nil=用 config 默认)持久化,并由**插件自己的 Start** 读回 —— 恢复点放在插件里而不是各端 UI 启动钩子,无人值守(定时任务/headless)才不会静默回退。
未覆盖(**协作层**的诚实边界):命令包装器与构建系统内部的写(`ccache`/`make`/`cmake` 自选的落点)、解释器内部写(`python3 -c "open('/x','w')"`)、`cmake --install` 不给 `--prefix` 时的默认落点、`go install` 无 `-o` 时装进 `GOPATH/bin`(`GOPATH` 刻意不重定向)、`curl -O`(按 URL 落 cwd,不越界)、变量拼出的命令文本(`CMD='rm …'; $CMD`),以及**外部进程**(MCP server 子进程、host-bridge 外部插件)的写。这些靠危险模式 + 审批档兜底。
**内核级沙箱(第 3 组,进程树层面)** 给上述边界兜底:宿主把**有效**档位经 `sdk.SandboxHint` 下发到执行入口,`shell` 在进程树级施加平台限制 —— macOS `/usr/bin/sandbox-exec`(seatbelt profile)、Linux **Landlock**(内核 ≥5.13;因 Landlock 对进程不可撤销,经**自举 helper 重新 exec** 自身后再 `syscall.Exec` 真实命令)。语义:只约束**文件写**(读与网络不限制,与协作层范围一致);白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**`(+ `/dev/null`、`/dev/tty`、pty 等必要设备节点);路径先 `filepath.EvalSymlinks` 解析(macOS `/tmp`→`/private/tmp`,不解析则白名单会静默失效);read-only 档**保留 jail 可写**(否则 `TMPDIR`/`GOCACHE` 断裂会让命令大面积失败);能力缺失(无 `sandbox-exec`、无 Landlock、Windows 等)→ **一次性 stderr 告警 + 不施加包装**(明示降级,不静默);`GAH_SHELL_KERNEL_SANDBOX=0` 可关闭。
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

**错误与退出语义(P3 软降级)**
- 业务失败回 `reply.Error`(结构化),回传模型、不中断宿主 turn。
- 插件加载/启动失败 = 自身被跳过(宿主 ERROR 日志,继续 boot);**exit code 不向宿主传语义**——缺配置必须在 stderr 显式说明后 `exit 1`(防静默空转,参照 extplugins/tool-mcp 的 GAH_MCP_COMMAND 模式)。
- 运行期崩溃:调用转结构化错误,宿主 60s 节流自动拉起。

**平台与构建(P4)**
- 插件二进制必须与宿主同平台;`scripts/gen-extplugins.sh` 按发行矩阵(darwin/linux × amd64/arm64 + windows/amd64)构建,embed 分平台打包(主包每目标只嵌本平台产物)。
- 新增外部插件:加进脚本的 NAMES 列表 + catalogue 登记;构建链产物缺失时主包构建失败(防漏,勿手动删除 embed 产物目录)。

**验收路径**
- 单测参照 `plugins/host/host-bridge/bridge_test.go` 的 `buildExternalPlugin` 模式(测试内 `go build` 产物再装配断言崩溃隔离/软降级)。
- 集成端到端见 `tests/external_test.go`(`releaseExt` 释放 + 真实回合走回调通道)。

### 4.2 UI 插件(web 槽位)的信任模型

UI 插件以 `$GAH_HOME/ui-plugins/<id>/` 落盘,manifest 声明槽位覆盖(stream/input/statusbar/confirm/settings-section/sidebar-action/extra-panel),前端经 `web-src/src/plugins.ts` 动态 `import()` 载入。

**关键事实:UI 插件与主应用同源同 realm,因此拥有与主应用相同的权限** —— 可读取页面上的全部会话内容、可用 cookie 调全部 `/api/*`(含 `/api/input`,即向模型投喂 prompt → 经工具执行等同本地代码执行)。这与外部插件二进制「与宿主进程同权限」等价。

- 安装 = 用户手工放产物;**只安装你信任的插件**(与「插件目录可写 = 用户可替换插件」同一信任域,设计如此)。
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
- [ ] UI 插件型:未把「同源同权限」当作安全边界(§4.2)
- [ ] 单测通过;-race 全绿
- [ ] 错误回传模型(结构化 error),不 panic
- [ ] 外部插件型:握手/协议/回调/退出语义(§4.1)已符合;产物已编入 scripts/gen-extplugins.sh 的 NAMES
