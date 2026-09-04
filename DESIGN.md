# Go Agent Harness 设计方案

> 基于 DeepSeek Harness(dsh)"一切皆插件"设计哲学 + Cordis 可逆插件框架理念,使用 Go 实现。Cordis 插件系统 Go 化。暂不使用 Web 端,以 TUI 为界面。
> 参考项目:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)。
> 状态:评审通过,待实施。

## 0. 项目目的

**排除 dsh 因 Node.js 带来的依赖:以单一静态二进制交付全部 harness 能力,仅通过二进制部署即可启动,不依赖其余环境。**

- 无 node 运行时、无 node_modules 分发链、无版本管理器(nvm/volta);scp 一个文件到目标机即开箱可用。
- 消除 dsh 的数十 MB 依赖树与安装步骤(dsh 需 Node ≥ 指定版本 + npm 剖析安装各包)。零运行时环境依赖是**硬性红线**;一切其余设计(微内核、插件化、go:embed)均服务于此目的。

---

## 1. 定位与设计哲学

对齐 dsh 的 **"一切皆插件"** 理念,**微内核化**:内核不承载任何 Agent 能力,仅负责插件的**加载、卸载与依赖管理**(与 Cordis 内核定位一致)。Agent 循环、会话日志、工具注册表、模型适配器、沙箱、审批策略、乃至 TUI 本身,全部是插件,由 bundle 按序组装,经配置层(patch/profile)自由替换,**随时插拔开关**。

| Cordis 概念 | Go 实现 |
|---|---|
| Context(共享服务容器) | `Ctx` 结构体,持有服务注册表 + 事件总线 |
| Service 依赖注入 | 类型化接口注册:`ctx.Provide(name, impl)`;消费方 `ctx.Inject("ctx.tools")` |
| 可逆副作用(disposer) | 注册动作返回 `Disposer`,插件 `Unload()` 时按序执行撤销;reload 先 dispose 再重挂 |
| 事件系统 | EventBus 五种分发:emit(广播)/ waterfall(洋葱拦截)/ serial(顺序)/ bail(短路)/ parallel(并发) |
| 插件树 + profile patch | YAML 配置层:profile 组合包按序叠加 → patch 按 id 定位条目替换/插入/启停 |
| 生命周期 | `Start(ctx, manifest) (Disposer, error)` + `Dispose()` + hot reload |

**核心原则:注册即副作用,卸载即撤销** —— 不存在需要打补丁的特权内核。插件可独立订阅/发布事件、注册服务与工具、挂载 UI 视图,不修改宿主代码。

---

## 2. 微内核边界(最核心的部分,仅此而已)

### 2.1 内核(`core/`,不可插拔)

| 模块 | 职责 |
|---|---|
| `ctx` | `Ctx` 服务容器:Provide/Inject/Require |
| `event` | EventBus:5 种分发模式 |
| `plugin` | PluginRegistry:拓扑排序加载、依赖管理、热重载(RCU 式引用切换) |
| `config` | 配置层:profile → bundle → patch 按序合并;`--dump-config` 导出生效树 |
| `boot` | `cmd/gah` 仅做挂载编排(非应用),按 profile 声明顺序拉起插件 |

### 2.2 内核之外的全部(bundle 组装,MVP 起即插件化)

`base` bundle(共享第一层,对齐 dsh-base)——以下皆为插件:
`host-agent-loop`、`host-session-log`、`host-tools`、`host-system-prompt`、`host-llm`(域模型 + 路由)、`llm-openai-compat`、`policy-sandbox`、`policy-approval`、`host-plugin-manager`、`host-cwd-sessions`(项目级会话隔离)、`host-jobs`(后台任务)、`tool-*` 系。
叠加 bundle:`tui`(加 `ui-tui-app` 全套界面插件)、`headless`(无 UI 一次性运行器)。

**插件加载来源两级**(详见 §7):
1. **内置插件**:`go:embed` 随二进制编译,boot 直接注册——零外部文件依赖;
2. **外部插件**:`~/.gah/plugins/` 目录,经 M5 gRPC 桥加载(可选,缺失即降级为仅内置)。

**插拔保证**:
- 任一能力的启停 = patch 一条 entry(`enabled: true/false` 或移除条目),支持 profile 级与运行期两级;
- 运行期+持久开关:`host-plugin-manager` 提供 load/unload 工具(模型可经工具调用,TUI 可经 `/plugins on|off`),dispose 后副作用即时撤销;TUI 的 `/plugins on|off|default` 同时写入 `patch-runtime.yaml`(幂等合并 + profile 自动引用),重启仍生效,`default` 清除持久覆盖恢复配置树默认;
- `--dump-config` 打印的任何条目都可被自己的 patch 替换(对齐 dsh)。

### 2.3 插件形态决策(区别于 dsc)

- **核心:进程内 Go 接口插件**(`Plugin` interface + 服务注入 + 事件订阅)。Cordis 语义(可逆副作用、同步回调、服务引用)只在进程内成立。
- **外部插件桥(可选,M5)**:gRPC + go-plugin,面向崩溃隔离/多语言扩展。
- **热重载**:进程内 `fsnotify` + dispose/reload;外部插件 reattach。
- 权衡:进程内插件崩溃拖垮宿主 → MVP 期接受,M5 桥缓解。

---

## 3. 系统架构

```
┌──────────── 微内核 core/(零 Agent 能力,零 UI) ────────────┐
│  ctx(服务容器)  event(5 分发)  plugin(拓扑/热重载)         │
│  config(profile→bundle→patch)  boot(挂载编排)               │
└───────────────────────────┬──────────────────────────────────┘
                            │ 挂载(按 profile 声明顺序)
┌───────────────────────────┴──────────────────────────────────┐
│ base bundle(全部功能皆插件)                                  │
│  host-agent-loop   host-session-log    host-tools(waterfall)│
│  host-system-prompt host-llm(域模型)  llm-openai-compat       │
│  policy-sandbox    policy-approval    host-plugin-manager     │
│  host-cwd-sessions host-jobs          tool-shell/files/web     │
└───────────────────────────┬──────────────────────────────────┘
                ┌────────────┴────────────┐
        tui bundle(ui-tui-app)      headless bundle(一次性)
```

**轮次流程(对齐 dsh;AgentLoop 本身是可替换插件)**:

```
turn/start
  claim next-step input
  assemble prompt sections + tool schemas(host-system-prompt)
  -> agent/pre-step(waterfall:可改写/拒绝)
  step/start
  -> llm/stream -> assistant/chunk* -> assistant/message
  tool/call* -> tools/pre-execute(veto) -> tools/execute -> tools/post-execute -> tool/result*
  step/end
turn/end
```

`turn/*`、`step/*`、`user/message`、`assistant/*`、`tool/*` 为持久会话事件;`agent/*`、`llm/stream`、`tools/*` 为实时扩展点。

---

## 4. 插件生态设计

### 4.1 插件类型(对齐 dsh 包角色;命名沿用 dsc 目录约定 `plugins/<type>-<name>/`)

| 类型 | 职责 | ctx 键示例 |
|---|---|---|
| `llm` | 模型适配器 | `ctx.llm` |
| `tool` | 工具(schema 进 system prompt) | `ctx.tools` |
| `policy` | 审批/拦截/沙箱 | 监听 `tools/pre-execute`, veto |
| `agent` | 自定义循环, 实现 `Agent` 接口 | `ctx.agentLoop` |
| `host` | 宿主级:会话/命令/任务/插件管理 | `ctx.sessions` / `ctx.commands` / `ctx.jobs` |
| `ui` | TUI 应用/视图渲染器 | UI 注册表(card/table/plain) |

### 4.2 Plugin Manifest(必要字段)

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | `plugins/` 下目录名(`<type>-<name>`) |
| `type` | enum | llm/tool/policy/agent/host/ui |
| `apiVersion` | semver 范围 | 兼容性校验,对齐 dsc `>=1.0,<2.0` |
| `provides` | []string | 提供的 ctx 服务键(拓扑排序依据) |
| `requires` | []string | 依赖的 ctx 服务键(缺失时显式失败,不静默降级) |
| `capabilities` | []string | 声明能力(如 `tools:execute`、`fs:write`) |

### 4.3 插件 SDK

```go
// 插件仅依赖 sdk/,不依赖 core/(防循环依赖,保证版本兼容与热重载安全)
type Plugin interface {
    Name() string
    Start(ctx *Ctx, m *Manifest) (Disposer, error) // 注册即副作用,dispose 自动撤销
}

// 示例:工具注册
ctx.Provide("ctx.tools", tools)
ctx.Emit("agent/request", &ev, Waterfall)
```

---

## 5. 技术选型

### 5.1 核心选型(与 dsc 同款,已验证;依赖链全纯 Go,无 CGO → 静态编译可行)

| 领域 | 选择 | 理由 |
|---|---|---|
| TUI | `charm.land/bubbletea/v2` + `bubbles/v2` + `lipgloss/v2` | v2 异步模型,专为流式 LLM 输出设计 |
| 事件系统 | 自研 EventBus(5 种分发) | 无现成库 |
| 配置 | `yaml.v3` + 自研 profile→patch 层 | 对齐 cordis.patch.yml |
| 热重载 | `fsnotify` + dispose/reload | 进程内 |
| 外部插件桥 | `hashicorp/go-plugin` + gRPC(**M5 启用**) | 崩溃隔离/多语言 |

### 5.2 LLM 统一域模型(替代捆绑官方 SDK)

- 自研 `Message/Chunk/Stream` 域类型,对齐 dsh `llm-streaming` 词汇表与 `ctx.llm` seam(dsc fork 三个 SDK 库是反面案例)。
- **主适配器 = OpenAI 兼容协议**(`/v1/chat/completions` + SSE):通吃 DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp;纯 HTTP + SSE,零 SDK 依赖。
- Anthropic 独立适配器✅(llm-anthropic-compat,Messages API + SSE;模型前缀路由:模型名 claude-* 经 ModelRouter 命中本适配器,默认回退首个无前缀声明的通用适配器)。

### 5.3 starlark-go(替代 go-lua,编排/workflow/PTC)

- 无标准库、无系统调用 → 天然沙箱,宿主只暴露 SDK 函数(`mytool{...}` / `agent` / `parallel` / `pipeline` / `job_*`)。
- Python 语法对模型生成成功率更高(dsc 的 go-lua 需自研协程调度器,弃)。

### 5.4 日志:`log/slog` + 自研 fanout handler(宿主/TUI/外部插件进程三路扇出)。

### 5.5 工具定义:MCP 兼容 JSON Schema(`name/description/inputSchema`),零成本接 MCP 桥:client 已交付(mcp-bridge),server 已交付(mcp-server,§14.1 M6.7)。

### 5.6 补充(低优先,按需引入)

| 项 | 选择 | 用途 |
|---|---|---|
| diff | `hexops/gotextdiff` | editor diff 着色 |
| 高亮 | `chroma` | 会话流渲染 |
| CLI | `cobra`(headless 子命令) | 多 profile |
| 校验 | `go-playground/validator` | manifest/config 校验 |
| pty | `creack/pty`(M5) | shell 交互场景 |
| 配置 | `koanf`(M5) | env/flags 覆盖层 |

---

## 6. 目录结构

```
go-agent-harness/            # module: github.com/nekoleamo/go-agent-harness,二进制 gah
├── cmd/gah/                 # boot:仅挂载编排
├── core/                    # 微内核:ctx/event/plugin/config/boot(见 §2.1)
├── sdk/                     # 插件 SDK(core 对外稳定接口,插件唯一依赖)
├── bundles/                 # bundle 定义:base / tui / headless
├── plugins/                 # 全量功能插件(<type>-<name>/,源内嵌编译,见 §7)
├── internal/embed/          # go:embed 资源(profile/patch 样板、system prompt 模板)见 §7
├── tui/                     # ui-tui-app 插件的界面包(bubbletea)
├── config/                  # 开发用样板源(→ go:embed)
├── tests/                   # 集成测试(插件往返、热重载 e2e)
└── docs/
```

> 约束:插件只依赖 `sdk/`;`core/` 不 import 任何业务包。

---

## 7. 二进制交付与部署(项目目的的落地章)

### 7.1 零运行时依赖红线

**运行时依赖 = 0**(硬性):gah 二进制在任何目标机上免安装、免解释器、免包管理器直接启动。审查口径:
- 全依赖链纯 Go、`CGO_ENABLED=0` 交叉编译,`net` 用纯 Go resolver;
- **增强工具与运行时依赖严格区分**:git/chromium 等属"环境本就有的增强工具",gah 调用时缺失则**优雅降级**(提示可用性),**不做任何安装**,不违背交付目的。

### 7.2 go:embed 嵌入体系

单一二进制携带全部资源:
- 内置插件代码随二进制编译(boot 直接注册,无外部 so/dll);
- base/tui/headless bundle 定义、profile/patch 样板、system prompt 模板 → `internal/embed` 经 `go:embed` 打入。

### 7.3 首启释放与 Home

| 项 | 行为 |
|---|---|
| home 目录 | 默认 `~/.gah/`,env `GAH_HOME` 覆盖;无写权限降级 temp home |
| 首次启动 | 释放可编辑层(配置样板、sessions/)到 home;embed 资源只读共享,不重复释放 |
| 会话日志 | `~/.gah/sessions/<project-key>.jsonl` |
| `--ephemeral` | 一次性模式:全部落 temp、退出即焚(适用于容器/CI) |
| 外部插件目录 | `~/.gah/plugins/`(M5 桥加载;不存在 = 仅内置插件,正常降级) |

### 7.4 构建交付规范

| 项 | 规范 |
|---|---|
| 编译 | `CGO_ENABLED=0 go build -trimpath`;`-ldflags "-s -w -X main.version=..."` 注入版本 |
| 矩阵 | goreleaser:linux/macOS × amd64/arm64(Windows 随 pty 进度) |
| 体积验收 | 单二进制 **< 40MB**(M6.9 工具类全外部化 + M7 gzip embed:外部插件 .gz 压缩率 ~50%,三件套 ~15MB;基线 ~18MB);超限则 upx(可选)与 embed 资源压缩审查 |
| 校验 | 发布附 sha256;`gah version` 输出版本与构建信息 |

### 7.5 升级

替换二进制即升级(配置/会话在 home,与二进制解耦);跨版本 schema 迁移由 host-cwd-sessions 在首启时执行,失败回滚。

### 7.6 交付实测(已通过)

| 验收项 | 实测结果 |
|---|---|
| `CGO_ENABLED=0` 静态编译 | ✅ `-trimpath -ldflags="-s -w -X main.version=v0.1.0"`;otool 仅系统库 |
| 体积 <40MB | ✅ 31.9MB(darwin/arm64,M7 gzip embed 回归;M6.9 峰值曾达 83.5MB 已修复);基线(无插件)~18MB |
| 版本注入 | ✅ `gah -version` → `gah v0.1.0 (github.com/nekoleamo/go-agent-harness)` |
| 交叉编译六目标 | ✅ darwin/linux/windows × amd64/arm64(除 windows/arm64 视 pty) |
| 裸机启动 | ✅ `env -i PATH=/usr/bin:/bin HOME=<tmp>` 下 headless 一轮成功 |
| 冒烟 | ✅ headless 完整轮次 / `--dump-config` / 非 TTY 降级 |
| 校验 | ✅ sha256 附档;`.goreleaser.yaml` 已配置(tar.gz/zip + checksums) |

---

## 8. 并发与取消语义(Go 实现核心细节)

- **goroutine 所有权**:每个 turn 一个 owner goroutine;工具调用各自短生命周期 goroutine(`context.Context` 派生自 step);LLM 流单 goroutine 读 SSE,chunk 同时 emit 事件 + append 会话日志。
- **取消链**:用户中断(Esc/`Ctrl-C`)→ turn ctx cancel → step ctx → LLM 流中断(适配器透传)+ kill 工具进程组(`SIGKILL` on `process.Group`)→ 会话日志记录 `turn/cancelled`。
- **工具超时**:活跃续命(基准 10min,`GAH_SHELL_TIMEOUT` 覆盖;有新 stdout/stderr 输出即续命,全静默才判超时)。
- **热重载与取消协调**:插件 reload 前必须等待/取消其进行中的 turn(RCU 式:新实例先挂载收新请求,旧实例排空后 dispose)。
- **EventBus 监听器 panic**:recover + 标记该监听器故障;waterfall 中断委托链。

## 9. 安全:沙箱、审批、凭据隔离

- **沙箱三档**(policy-sandbox 插件):`read-only`(拒绝一切写)/ `workspace-write`(仅 workspace 根内写,相对路径以 workspace 为根、防 `../` 穿越)/ `full-access`;TUI `/sandbox` 运行期切换,状态栏显示当前范围;`read-only` 同时禁用"无法从参数判定只读"的解释器类工具。
- **审批策略**(policy-approval 插件):危险操作经 TUI 模态 `y/n/always`;`always` 记入会话级 allowlist;与 waterfall veto 组合(policy 先决,用户后决)。
- **工具依赖界定**(呼应 §7.1):`tool-web` = 纯 Go HTTP fetch(零外部依赖);浏览器自动化为**可选插件**(显式依赖 chromium,缺失优雅降级);git 类调用仅使用环境已有的 git,缺失只降级提示。
- **凭据隔离**:工具子进程 spawn 时 env 白名单化——滤除 `*_API_KEY` / `*_TOKEN` / `*_SECRET`(保留 `GAH_*`),防模型经 shell 读 key 进会话历史;外部插件桥(M5)子进程同理。

## 10. 上下文与会话管理

- **会话日志**:jsonl 追加;不变量 **"模型可见即已记录"** —— 模型请求中的一切必须可从日志重建(fork/恢复/transcript/遥测均派生于事件流)。
- **项目级隔离**(host-cwd-sessions):会话按 cwd 项目路径命名,同项目跨期共享历史,不同项目隔离;无硬编码会话名。
- **history injection**:`/settings history N|off|unlimited`,实时生效并持久化。
- **token 预算/压缩**(M4):超限滚动摘要压缩,完整日志保留在磁盘。

## 11. 错误处理矩阵

| 层 | 错误 | 处理 |
|---|---|---|
| LLM 网络/断流 | 可重试 | 指数退避 ×3,仍失败 → 关闭 turn + 用户提示 |
| LLM 4xx(auth/quota) | 不可重试 | 立即失败 turn,提示检查 key |
| 工具执行失败 | — | **结构化错误回传模型**(JSON error 作为 tool result),不中断 turn |
| 插件 Start 失败 | — | 条目级隔离失败,不影响其他插件,仅日志 |
| 日志写失败 | — | 内存降级 + 警告 |

## 12. TUI 用户命令(对照验收)

| 命令 | 作用 |
|---|---|
| `/sandbox <ro\|ws\|full>` | 运行期切沙箱档 |
| `/sessions` `/session` `/export` | 会话列表/切换/导出 |
| `/settings ...` | 设置(含 `history N\|off\|unlimited`) |
| `/jobs list\|output\|kill` | 后台任务 |
| `/plugins on\|off\|default <id>` | 插件运行期插拔 + 持久开关(`patch-runtime.yaml`,重启仍生效;`default` 恢复配置树默认) |
| `/model` | 切换模型/提供商 |

## 13. 开发规范

- **Go 1.27+**;`golangci-lint` + `testify`;CI = lint + test + race(`-race` 走 CGO 仅用于 CI,发布编译仍 `CGO_ENABLED=0`)。
- 构建/发布规范见 §7.4(goreleaser/静态编译/体积验收)。
- 插件 SDK 语义化版本范围校验;`AGENTS.md` 约定插件开发规范。

## 14. 实施里程碑

| 阶段 | 内容 | 验收 |
|---|---|---|
| **M1 微内核** | ctx/event/plugin/config/boot + sdk + 热重载框架 | 单测:dispose 可逆、5 种分发、patch 合并;`--dump-config` |
| **M2 base bundle** | host-session-log/host-tools/host-llm(域模型+OpenAI 兼容适配器)/host-agent-loop/host-system-prompt | headless 跑通完整轮次 |
| **M3 tui bundle** | ui-tui-app:会话流/工具面板/状态栏/命令托盘/流式渲染 | §12 命令全可用 |
| **M4 生态完善**(已交付) | policy-sandbox(三档)/policy-approval(危险命令确认)/凭据隔离(env 白名单)/host-plugin-manager(运行期插拔)/history 注入(settings) | 插件运行时装卸不影响会话;沙箱三档生效 |
| ~~M4.5~~ | starlark workflow 已随 M5 交付(tool-workflow 插件,天然沙箱/组合多步/background) | — |
| **M5 进阶**(已交付) | ✅ external 插件桥(崩溃隔离验证)+ ✅ starlark workflow + ✅ MCP client 桥 + ✅ 配置自愈 | 外部崩溃→结构化错误/宿主存活;MCP 互通;坏配置可回滚 |
| **M5.5**(已交付) | ✅ 指令文件注入(全局/项目 AGENTS.md,顺序即覆盖)+ ✅ 技能机制(host-skills)+ ✅ 技能自注册(gah-plugin-dev) | 指令与技能按需加载 |
| **完善 A 组**(已交付) | ✅ 会话持久化+项目隔离(host-cwd-sessions)+ ✅ LLM 断流指数退避重试(§11)+ ✅ TUI 回合取消(Esc→取消链) | 跨期共享隔离;断流自愈;可中断 |
| **完善 B 组**(已交付) | ✅ apiVersion 语义化校验(SDK 兼容红线,go-version)+ ✅ /export 真导出(jsonl)+ ✅ 外部插件热重载接线(host-bridge watch→自动重载)+ ✅ go:embed 配置样板+home 首启释放+`--ephemeral` 落地(空目录发布实测通过) | 发布形态自包含;插件版本兼容强制;外部插件更新自动生效 |
| **M6 规划**(待交付) | 见下(§14.1 未交付规划清单;按需求逐个实现,无硬性交付线) | 每项独立验收 |
| **交付门**(已通过,P1 更新) | 单二进制 <40MB(P1 起附包插件)/ `CGO_ENABLED=0` / 六目标交叉编译 / 裸机 scp 启动(首启释放插件后可用) | ✅ 实测数据见 §7.6 |

### 14.1 未交付规划清单(M6,按需逐个实现)

| 项 | 内容 | 验收 |
|---|---|---|
| M6.1 host-jobs 后台任务 ✅ | `ctx.jobs` 服务(提交/列表/输出/终止)+ 模型工具(job_list/job_output/job_kill)+ TUI `/jobs list\|output\|kill`;对接 workflow background | 后台任务可提交/取回/终止,不阻塞回合 |
| M6.2 子代理 fanout ✅ | workflow 增加 agent/parallel/pipeline 编排(独立 agent 上下文,复用现有 agent-loop / 会话隔离) | 脚本可扇出多个子代理并行执行并聚合 |
| M6.3 pty 交互 ✅ | tool-shell 引入 `creack/pty`(data.pty 开关):交互式命令(REPL/git 编辑器等) | shell 工具可驱动交互式进程 |
| M6.4 tool-files / tool-web ✅ | 文件工具(读写/编辑,经 `ctx.sandbox.ValidatePath` 联动)+ 纯 Go HTTP fetch 工具 | 文件操作受沙箱三档约束;fetch 零外部依赖 |
| M6.5 token 压缩 ✅ | 会话超限时滚动摘要压缩(完整日志仍留盘)(§9) | 长会话注入 token 受限可用 |
| M6.6 插件安装与线上索引 ✅ | `gah -install <repo>[@version]` / `-uninstall <id>` / `-list-plugins`:git 拉取 → 构建 → 落 `~/.gah/plugins/<id>/` → 幂等登记 `patch-installed.yaml` + profile 自动引用(装完即启用);manifest(仓库根 `plugin.yaml`:{id, protocol: bridge\|mcp, binary, build});MCP 插件直接登记 mcp-bridge 配置(`mcp:<id>:<command>`) | 一条命令装完即启用;卸载撤销干净;MCP server 与自有桥插件经同一入口发现 |
| P0 外部化基础 ✅ | sdk 独立 module(monorepo,替换本地);桥协议多工具化(Definitions/ExecuteNamed,旧单工具回退)+ 工具级超时(TimeoutMs)+ 进程崩溃自动拉起(60s 节流) | 第三方独立开发;长命令不被 3s 截断;崩溃自动恢复 |
| P1 方案B 随包释放 ✅ | tool-shell/files/web 合一批二进制 tool-basic(多工具),embed 随主包;首启释放 `~/.gah/plugins/`(已有跳过);base bundle 内置 tool-* 停用、host-bridge 默认启用(dir 缺省 home/plugins) | 开箱即用(运行时全为外部进程插件);`-list-plugins` 可见;体积 <40MB |
| P2 插拔解耦边界 ✅ | 宿主服务类(skills/jobs/workflow 等进程内插件)不外部化——外部化需宿主服务桥(IPC 双向通道),与微内核/事件 veto 语义冲突,收益低;工具能力全外部化已达成;插件卸载解耦由集成矩阵保障(hook:被依赖者拒卸/叶子可卸/卸后回合继续/缺失显式提示) | 关闭/卸载插件不报错;无法继续的回合给出可操作提示 |
| M6.7 MCP server(二期)✅ | plugins/mcp-server:本仓工具暴露为 MCP server(stdio JSON-RPC,initialize/tools/list/tools/call;错误码 -32700/-32600/-32601/-32602/-32603;业务失败 isError;工具列表实时反映 ctx.tools 插拔);catalogue 登记 + base bundle 默认关闭(serve/headless 场景启用) | 外部 MCP client 经 stdio 发现并调用本仓全部工具;与 mcp-bridge 对称组成双向桥 |
| M6.8 拆分重构 ✅ | **token-compress**:token 滚动摘要压缩从 host-session-log 拆出(独立插件,requires ctx.sessions,经 RegisterCompressor 注入;host-session-log 回归纯持久化/投影);**host-fanout**:子代理编排(agent/parallel/pipeline)从 tool-workflow 拆出(宿主服务 ctx.fanout,复用 ctx.llm/tools/systemPrompt;tool-workflow 回归纯 starlark 执行器 + 薄适配) | 压缩独立开关/独立测试;子代理编排宿主化,任意入口可复用(未来 fanout 完善的扩展点) |
| M6.9 工具类全外部化 ✅ | **tool-workflow/tool-mcp 移出宿主**(extplugins/ 独立二进制,随包 embed 释放);host-bridge 增**宿主回调通道**(GAH_CB_ADDR,net/rpc:tools.execute/list、jobs.run/output、fanout.agent/parallel/pipeline——仅桥纯函数服务,不桥事件 veto);内嵌实现停用(与 P1 tool-basic 同模式);**mcp-serve profile**(gah 独立 serve 形态,仅工具宿主 + mcp-server,不跑 agent) | 工具类能力 100% 外部进程(崩溃隔离/独立升级);脚本工具/背景任务/子代理经回调通道回宿主持有全流水线;`gah --profile mcp-serve` 可被外部 MCP client 拉起 |
| P3 首启健壮性 ✅ | **统一 home 事实源**:main 确定 home 后设 `GAH_HOME`(ephemeral 彻底隔离,外部插件目录一并入临时 home,退出即焚);**host-bridge 加载软降级**:单个外部插件加载失败(缺配置/崩溃/不兼容)记 ERROR 日志跳过继续装配(目录级错误仍显式失败),热重载/崩溃拉起失败同款日志;tool-mcp 缺 `GAH_MCP_COMMAND` 不再拖垮整个 boot;**CI 裸机冒烟升级**:--dump-config 之外新增 headless 真实回合(`env -i` + `--ephemeral -input`,默认 llm-mock 无外网),全装配首启路径进回归护栏 | 干净环境首启零配置 boot 成功;未装上的工具对模型不可见、调用侧显式提示(不静默);回归由 CI 真实回合护栏 |

## 15. 风险与权衡

| 风险 | 缓解 |
|---|---|
| 进程内插件崩溃拖垮宿主 | M5 gRPC 桥;MVP 接受 |
| 热重载引用泄漏/竞态 | disposer 强制清理 + `-race` CI + RCU 式切换 |
| 微内核边界漂移(功能悄悄进 core) | 评审红线:`core/` 不 import 业务包;`--dump-config` 可见性 |
| 二进制体积超 40MB | upx(可选);embed 资源压缩;chroma/goldmark 类大库按需侧载进 tui bundle(不进 base);随包插件按需裁剪 |
| 增强工具被误当运行时依赖 | §7.1 红线:CI 增加裸机(无 git/chromium)启动冒烟测试 |
| 流式渲染复杂度 | M3 集中投入 |
| starlark 表达力不足 | 编排场景已涵盖,缺失再加 go-lua 适配器 |
| 兼容协议提供商差异 | 差异映射单测 + Anthropic 独立适配 |

## 16. 参考来源

- DeepSeek Harness 架构文档(profile/bundle、事件域、能力 seam、`--dump-config`)
- Cordis《A Programming Paradigm for Spatiotemporal Composability》(dsc 仓库含 PDF)
- naamfung/dsc(go.mod、README 功能清单、sandbox/续命超时/凭据隔离/项目级会话设计)

## 17. 决策点(已定)

| 决策点 | 结论 |
|---|---|
| 项目目录 | `/Users/nekoleamo/Documents/Working/go-agent-harness/` |
| module 名 | `github.com/nekoleamo/go-agent-harness`,二进制 `gah` |
| Go 版本 | **1.27+** |
| 架构形态 | **微内核**——core 仅加载/卸载/依赖管理,其余全部插件化,profile/patch + 运行期两级插拔 |
| 交付形态 | **单一静态二进制**(go:embed 全资源,`CGO_ENABLED=0`,运行时依赖 = 0) |
| MVP LLM | OpenAI 兼容适配器先行(DeepSeek/Ollama 同吃),Anthropic 第二 |
| MCP 桥 ✅ | client(mcp-bridge)+ server(mcp-server)均已交付(§14.1) |

---

> 待确认后从 M1 微内核开始实施。
