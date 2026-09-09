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
- 外部进程插件(崩溃隔离):host-bridge(go-plugin net/rpc)/ mcp-bridge(MCP stdio),见各自包注释与测试。

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
- 握手标识 `GAH_PLUGIN=gah-external-tool` 缺失即拒启(防误跑)。

**外部命令桥(M14,免编译加命令)**:外部插件声明的命令经 host-bridge 自动转注册进 `ctx.commands`(与进程内插件命令同表),**新命令插件丢进 `~/.gah/plugins/` 即生效,无需重编译 gah**(目录 fsnotify 热重载、崩溃自动拉起复用既有机制)。语义:
- 声明即注册:Args 级联照常(枚举级 `Options` 运行期经桥 RPC 求值、自由级 `FreeArgs` 触发断点向导);执行 `Run` 在外部进程内完成,输出文本+error 回宿主 TUI meta 行。
- 同名命令冲突:先到先得,拒绝并记警告(被跳过命令不注册,插件继续加载);插件卸载/热重载随 Disposer 撤销。
- TUI 侧零改动:`/` 提示、`/help`、选择器断点全部自动来自 `ctx.commands` 注册表。
- 参照实现:`extplugins/tool-echo`(工具 + `/echo` 命令共存,约 80 行)。
- **纯命令插件**:外部插件可只提供命令(无工具)——宿主扫描识别 `tool-*` 与 `cmd-*` 前缀(文件名 `cmd-<名>`),`ServeTools(nil, commands)` 直接可用;loadOne 工具或命令任一满足即加载。
- **命令超时**(可选):`sdk.CommandSpec.TimeoutMs`(毫秒)声明执行/枚举选项 RPC 超时,0 = 宿主全局默认 3s——死进程/慢命令不会阻塞 TUI 线程(超时即显式报错,连接类错误仍触发自动拉起)。

**桥协议**
- 多工具(新协议):`Definitions` 枚举 + `ExecuteNamed` 按名执行;旧单工具协议(`Definition`/`Execute`)宿主自动回退兼容。
- 工具级超时:定义声明 `TimeoutMs`(毫秒),覆写宿主全局默认 3s。

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

## 5. 检查清单(提交前)
- [ ] 只 import sdk;无 core/tui/其它插件 import
- [ ] Start 返回的 Disposer 可逆且幂等(注册的每个副作用都有撤销)
- [ ] 缺依赖显式报错,不静默
- [ ] catalogue 已登记(provides/requires/bundle 正确)
- [ ] config 条目已加(含 enabled/data)
- [ ] **便携纪律**(见 AGENTS.md「便携纪律」):任何写盘路径以 GAH_HOME 为根(禁用硬编码 ~/.gah、cwd 相对写、系统根/散目录);密钥入 config/、env 入 gah-data/env.sh;新增路径 helper 可审计
- [ ] 单测通过;-race 全绿
- [ ] 错误回传模型(结构化 error),不 panic
- [ ] 外部插件型:握手/协议/回调/退出语义(§4.1)已符合;产物已编入 scripts/gen-extplugins.sh 的 NAMES
