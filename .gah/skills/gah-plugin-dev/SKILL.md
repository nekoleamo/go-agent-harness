---
name: gah-plugin-dev
description: gah 插件开发规范:微内核/插件 SDK 契约/七步开发流程/装配与测试要求/提交检查清单。任何开发、注册、修改 gah 插件时必须先读本技能。
trigger:
  - 插件开发
  - 注册插件
  - 插件装配
  - catalogue
  - sdk.Plugin
  - Disposer
  - Manifest
  - 热重载
  - 外部插件桥
---

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
| `ctx.systemPrompt` | host-system-prompt | 提示词片段 + 指令文件(AGENTS.md) + schema 组装 |
| `ctx.agentLoop` | host-agent-loop | 默认 ReAct 循环(可替换) |
| `ctx.sandbox` | policy-sandbox | 三档沙箱(ro/ws/full) |
| `ctx.pluginManager` | host-plugin-manager | 运行期插拔(Load/Unload) |
| `ctx.confirm` | ui-tui-app | 危险操作 y/n 确认(无实现 = 安全拒绝) |
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
`go build ./... && go vet ./... && go test ./... -race` 全绿后提交;catalogue 变更必须提交。

## 4. 生命周期与热重载
- 卸载 = Disposer 链逆序执行(后注册先撤销)。
- 运行期插拔:`host-plugin-manager` 提供 Load/Unload;TUI `/plugins on|off`。
- 外部进程插件(崩溃隔离):host-bridge(自建 stdio + net/rpc;握手行 `GAH-PLUGIN|2|stdio`,stdout 只能写这一行)/ mcp-bridge(MCP stdio),见各自包注释与测试。

## 5. 检查清单(提交前)
- [ ] 只 import sdk;无 core/tui/其它插件 import
- [ ] Start 返回的 Disposer 可逆且幂等(注册的每个副作用都有撤销)
- [ ] 缺依赖显式报错,不静默
- [ ] catalogue 已登记(provides/requires/bundle 正确)
- [ ] config 条目已加(含 enabled/data)
- [ ] 单测通过;-race 全绿
- [ ] 错误回传模型(结构化 error),不 panic
