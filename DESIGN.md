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
> 目录布局:插件按类别分组于 plugins/ 的 host/ adapter/ policy/ tool/ mcp/ ui/ 子目录(catalogue/ 为汇总事实源,总览见 plugins/README.md)。分组为纯目录整理,不改包名/接口/装配语义;外部化目标不受影响(工具类保持 extplugins/ 独立二进制)。

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
├── plugins/                 # 全量功能插件(按类别分组:<type>-<name>/ 位于 host/adapter/policy/tool/mcp/ui 子目录,
│   │                         # 源内嵌编译,见 §7;总览见 plugins/README.md)
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
| 样板版本升级 | `bundle-*.yaml` 头部 `seed-version`;落盘版本低于 seed(或无版本旧样板)→ **备份(.bak-时间戳)后覆盖**——新增 base 能力条目(host-* 等)老用户自动补齐,零人工干预;版本一致/非 bundle 样板(profile/patch)不覆盖(用户自定义保留,应走 patch 层) |
| 会话日志 | `~/.gah/sessions/<project-key>.jsonl`(主会话,跨期共享);切换会话 `<project-key>-<id>.jsonl`(id=创建时间戳);重启经 Load 恢复历史 |
| `--ephemeral` | 一次性模式:全部落 temp、退出即焚(适用于容器/CI) |
| 外部插件目录 | `~/.gah/plugins/`(M5 桥加载;不存在 = 仅内置插件,正常降级);插件二进制**自动升级**:每次启动 sha256 比对 embed 产物与落盘内容,不同则覆盖(旧版能力缺失,如缺 web_search;不保留备份——plugins 扫描会加载 tool-* 前缀文件,二进制随包可再生;同版/用户自装产物跳过,幂等) |

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
- **取消链**:用户中断(Esc)→ turn ctx cancel → step ctx → LLM 流中断(适配器透传)+ kill 工具进程组(`SIGKILL` on `process.Group`)→ 会话日志记录 `turn/cancelled`。(退出与取消分离:`Ctrl+C` 是**双按退出**(防误触),`Esc` 是回合取消——见 §12。)
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
- **多会话切换**:同一项目可开多个会话(`/session switch|new|current`,switch 二级动态枚举会话列表)。主会话(`<key>.jsonl`)为跨期共享默认入口;新建/切换会话落 `<key>-<id>.jsonl`(id=时间戳)。切换后经 sessionlog.Load 恢复该会话历史(seq 接续、坏行容忍),模型上下文即所见历史;重启后主会话历史自动恢复(启动 Load),不再从空开始。
- **会话 token 统计**(host-usage-stats,独立插件):订阅 session/usage 事件(agent-loop 每轮 LLM 完成后记录 UsageEvent=模型名+Usage),累计 prompt/completion/cached token 与请求数;状态栏显示上下文使用率/缓存命中率;切换会话经 Reset 归零。
- **上下文窗口按模型动态解析**(不用单一默认值):agent-loop 记录时携带请求模型名,解析链 = `data.context_window`(统一覆盖)> `data.model_windows`(按模型名前缀的配置层覆盖)> **错误驱动学习**(新模型首次超限,agent/error 错误文本携带窗口数字,自动解析记忆——无需改表即"优先获取新模型窗口")> 内置窗口表(发行版本基线,随版本补新条目,如 glm-5.3 → 1M)> 0(未知)。**未知/空模型窗口=0:状态栏只显示使用量**(`上下文 1.2K`),不显示总量/百分比(不假精确);模型切换后随下一轮 usage 事件自动更新。
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

> 命令体系经 **ctx.commands 注册表**(host-commands)动态装配:内部命令(宿主级)与插件命令共表(如 host-jobs 注册 `/jobs`);输入 `/` 实时显示全部命令、`/s` 前缀过滤——**插件新增命令自动进入提示**,卸载随 Disposer 撤销,TUI 分发/`/help` 均查表(见 PLUGIN_DEV.md 命令注册小节)。
> **交互式选择器**:输入 `/` 自动激活选项列表(高亮首项),**↑/↓ 移动、Enter 确定、Esc 退出**;命令声明的参数级(ArgLevel)→ 逐级选择——枚举级(`Options`)选完进下一级(如 `/sandbox` → `ro|ws|full` → 选完即执行);**自由级(`FreeArgs`)自动断点回输入框、提示继续输入**(如 `/model <名>`、`/provider set <baseUrl> <apiKey>`——保留命令文本不直接执行);无可定义级直接执行(`/help`)。动态枚举实时反映运行时状态(`/jobs output` 列任务 ID、`/plugins on` 列插件)。
> **状态栏**:回合运行中显示思考动画(`思考中 ⠋`)+ `(Esc 取消)`;工具执行时切换 `执行工具: <name>`;显示当前工作区目录名(`工作区: <dir>`)、会话 id(`会话: <id>`,主会话隐藏)、token 统计(`上下文 12.3K/64K (19%) 缓存 45%`——上下文使用率与缓存命中率来自 host-usage-stats,无请求时显示 `上下文 -`)。
> **退出(防误触)**:`Ctrl+C` 输入中单次仅清空输入(不退出);输入为空时**连按两次**(`quitConfirmWindow`=2s 窗口内,首次高亮提示 `⚠ 再按一次 Ctrl+C 彻底退出`)才退出——超时或按其他任意键自动解除;`Esc` 取消进行中的回合(TUI 退出 → `system/shutdown` 事件 → 主进程关停;外部 `kill -INT` 为运维硬退出通道,不经双按)。

| 命令 | 作用 |
|---|---|
| `/sandbox <ro\|ws\|full>` | 运行期切沙箱档 |
| `/session` `/export` | 会话管理统一入口(`/session list` 列出会话文件;`switch` 二级选择器选会话进入,切换后重放历史继续;`new` 新建空会话;`current` 查看当前;原 `/sessions` 并入 list,避免与 `/session` 混淆)+ `/export` 导出 |
| `/settings ...` | 设置(含 `history N\|off\|unlimited`) |
| `/jobs list\|output\|kill` | 后台任务 |
| `/plugins on\|off\|default <id>` | 插件运行期插拔 + 持久开关(`patch-runtime.yaml`,重启仍生效;`default` 恢复配置树默认) |
| `/model` | 切换模型/提供商 |
| `/provider show\|set\|clear` | LLM 端点/凭据运行时配置:`set <baseUrl> <apiKey> [model]` 立即生效并写 `provider.yaml`(0600,env 显式优先);show 打码展示;clear 回退 env/样板 |

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
| **M6**(已交付) | M6.1–M6.17 已交付:host-jobs/fanout/外部化(M6.8/6.9)/MCP server/指令与技能/插件安装/命令注册表/会话与统计(M6.10)/目录分组(M6.11)/配置修复(M6.12)/伪调用兜底(M6.13)/web_search(M6.14)/TUI 滚动与会话启动(M6.15)/滚轮风暴根因(M6.16)/输入键语义与滚动条拖动(M6.17)——细表见 §14.1 交付行,交付总览以 AGENTS.md「会话状态」为准;M7+ Web/工具类见下方未实施清单 | 全库 -race 绿 |
| **交付门**(已通过,P1 更新) | 单二进制 <40MB(P1 起附包插件)/ `CGO_ENABLED=0` / 六目标交叉编译 / 裸机 scp 启动(首启释放插件后可用) | ✅ 实测数据见 §7.6 |

### 14.1 未交付规划清单(M6,按需逐个实现)

> **状态图例**:标题 `✅` = 已交付实施;标题 `⏳ 未实施` = 规划待执行、尚未开工(规划条目正文为完整方案,按切片实施)。
>
> **当前未实施清单(5 项)**:M7 Web UI · M7.2 UI 槽位插件化 · M7.3 WebSocket 通道 · M8-T2 展示联动(依赖 M7.2)· M9 send_message/fork(依赖后台引擎会话化)——详见下方对应 ⏳ 行。已交付 ✅:M6.1–M6.21、M8-T1、M10、M9.1(one-shot)、M11-T1、M11-T2、M9.2(后台 delegate 子集)、M12 多 provider 并存(2026-09,见下交付表)。TUI 线全交付 ✅(S1.1–S2.2 + 折叠交互;S3.1 决策方案 A 记录在案;S3.2 框架覆盖)。
>
> **体验改进(对比 pi 基线)**:P4 12 项**全部交付** ✅(2026-09 收口:P4-1 消息队列 · P4-2 @引用+Tab 补全 · P4-3 代码块高亮 · P4-4 会话命名 · P4-5 C6 多级上下文 · P4-6 多行输入/外部编辑器 · P4-7 工具视觉增强 · P4-8 /compact · P4-9 语义色 token 化 · P4-10 C1 会话树/分支(树可视化 UI 归 M7 后)· P4-11 /reload · P4-12 widget 槽位;细见 `docs/ROADMAP.md` P4 节)。
>
> **TUI 界面优化**(非里程碑,详见 `docs/TUI_OPTIMIZE.md`):S1.1–S1.6 全交付 ✅;S1.4 Markdown 轻渲染、S2.1 消息分组/工具行去重、S2.2 渲染组件化(render/session/chrome/markdown 四层)+ 折叠交互 已交付 ✅(2025-09/10 迭代);S3.1 主屏 scrollback 决策=方案 A 维持现状(2025-10,理由与 B/C 备选记录于 docs/TUI_OPTIMIZE.md S3),S3.2 框架覆盖 ✅(S3.2 synchronized output 由 bubbletea v2 框架自动启用 ✅)。
>
> **实施排期**:P0 ✅、P1 ✅、P2 ✅ 均已交付(S1.1–S2.2 折叠交互 + S3.1 决策方案 A + S3.2 框架 + M8-T1 + M9.1/M9.2 + M10 + M11-T1/T2 + /workspace;P1/P2 完成于 2025-09/10)→ **剩余 = P3 远期**(M7 Web UI 全家桶 + M7.2 槽位 + M7.3 WS + M8-T2 展示联动)+ subagent send_message/fork —— 详见 `docs/ROADMAP.md` 与下方未实施清单。

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
| M6.7 MCP server(二期)✅ | plugins/mcp/mcp-server:本仓工具暴露为 MCP server(stdio JSON-RPC,initialize/tools/list/tools/call;错误码 -32700/-32600/-32601/-32602/-32603;业务失败 isError;工具列表实时反映 ctx.tools 插拔);catalogue 登记 + base bundle 默认关闭(serve/headless 场景启用) | 外部 MCP client 经 stdio 发现并调用本仓全部工具;与 mcp-bridge 对称组成双向桥 |
| M6.8 拆分重构 ✅ | **token-compress**:token 滚动摘要压缩从 host-session-log 拆出(独立插件,requires ctx.sessions,经 RegisterCompressor 注入;host-session-log 回归纯持久化/投影);**host-fanout**:子代理编排(agent/parallel/pipeline)从 tool-workflow 拆出(宿主服务 ctx.fanout,复用 ctx.llm/tools/systemPrompt;tool-workflow 回归纯 starlark 执行器 + 薄适配) | 压缩独立开关/独立测试;子代理编排宿主化,任意入口可复用(未来 fanout 完善的扩展点) |
| M6.9 工具类全外部化 ✅ | **tool-workflow/tool-mcp 移出宿主**(extplugins/ 独立二进制,随包 embed 释放);host-bridge 增**宿主回调通道**(GAH_CB_ADDR,net/rpc:tools.execute/list、jobs.run/output、fanout.agent/parallel/pipeline——仅桥纯函数服务,不桥事件 veto);内嵌实现停用(与 P1 tool-basic 同模式);**mcp-serve profile**(gah 独立 serve 形态,仅工具宿主 + mcp-server,不跑 agent) | 工具类能力 100% 外部进程(崩溃隔离/独立升级);脚本工具/背景任务/子代理经回调通道回宿主持有全流水线;`gah --profile mcp-serve` 可被外部 MCP client 拉起 |
| P4 发行平台匹配 ✅ | **gen-extplugins.sh 按发行矩阵构建**(darwin/linux × amd64/arm64 + windows/amd64,与 .goreleaser.yaml 对齐);**embed 按平台拆包**(extplugins_<os>_<arch>.go,build-tag 限定同名 extPlugins/extPluginDir,主包每目标只嵌本平台产物,体积门不变);**CI 交叉编译矩阵**(五目标 go build + 每目标体积门 <40MB + TestExtPluginsMatchPlatform 魔数/架构校验);tests 经新公开 API embed.OpenExtPlugin 读本平台产物 | 发行产物外部插件平台匹配(此前发布机平台产物会嵌进全部目标包,linux/darwin 包首启产物实际不可执行);交叉编译矩阵回归入 CI(体积门单目标升级为五目标) |
| P3 首启健壮性 ✅ | **统一 home 事实源**:main 确定 home 后设 `GAH_HOME`(ephemeral 彻底隔离,外部插件目录一并入临时 home,退出即焚);**host-bridge 加载软降级**:单个外部插件加载失败(缺配置/崩溃/不兼容)记 ERROR 日志跳过继续装配(目录级错误仍显式失败),热重载/崩溃拉起失败同款日志;tool-mcp 缺 `GAH_MCP_COMMAND` 不再拖垮整个 boot;**CI 裸机冒烟升级**:--dump-config 之外新增 headless 真实回合(`env -i` + `--ephemeral -input`,默认 llm-mock 无外网),全装配首启路径进回归护栏 | 干净环境首启零配置 boot 成功;未装上的工具对模型不可见、调用侧显式提示(不静默);回归由 CI 真实回合护栏 |
| M6.10 会话与统计 ✅ | **host-usage-stats 独立统计插件**(订阅 session/usage 事件,agent-loop 每轮记录 UsageEvent=模型+Usage,累计 token/请求数)+ **上下文窗口解析链**:data.context_window(统一覆盖)> data.model_windows(配置层前缀)> 错误驱动学习(agent-loop 失败包装 sdk.LLMError,订阅 agent/error 从超限错误文本解析窗口数字自动记忆,新模型免改表)> 内置窗口表(发行基线,如 glm-5.3→1M 已补)> 0(未知:状态栏只显示用量不假精确);**TUI 状态栏**显示 `上下文 12.3K/64K (19%) 缓存 45%`;**多会话切换**:主会话 `<key>.jsonl` 跨期共享 + 切换会话 `<key>-<id>.jsonl`(id=时间戳),`/session switch|new|current`(switch 二级选择器枚举),sessionlog.Load 恢复历史+seq 接续(坏行容忍)+每次启动即新开会话(空历史,旧会话经 switch 回溯);适配器解析缓存 token(openai prompt_tokens_details.cached_tokens/anthropic cache_read_input_tokens) | 状态栏使用率/命中率实时;切会话重放历史继续;启动首屏干净(不自动重放主会话);新模型超限即得窗口 |
| M6.11 目录分组 ✅ | plugins/ 按类别子目录(host/adapter/policy/tool/mcp/ui,总览 plugins/README.md;catalogue/ 仍为单一事实源);全仓 import 批量同步(含 extplugins main.go 对内置 export 包引用同映射);catalogue 守卫改递归(类别子目录逐一验登记);测试相对路径上移修正(bridge/mcp/skills 三处) | 分组后 -race 全绿;目录可读性提升;外部化目标/装配语义零变化(纯路径整理) |
| M6.12 配置持久化修复与启动显示 ✅ | **openai 适配器配置解析链重写**(抽 resolveConfig 纯函数):base_url/api_key/model **每字段独立恢复,model 不再被 apiKey gate**——旧实现 provider.yaml 的 model/base_url 只在 env key 为空时才读,env 提供 key(常见 export)时重启丢失持久化模型,且兜底 deepseek-chat 在 provider 读取前赋值顺序颠倒;新链 env 显式 > provider.yaml(/provider set、/model)> data 样板 > 内置默认;坏 provider.yaml 显式报错(不静默回退样板);anthropic/mock 无需改(model 经 openai resolveConfig + host-llm 前缀路由覆盖 claude 恢复);**TUI 启动即拉实际生效值**:Model=llm.Model()/Sandbox=ctx.sandbox.Mode()/Thinking=llm.Thinking()(不再显示"模型: 未设置"假默认);**history 设置补持久化**(DESIGN §10 声称"实时生效并持久化"此前仅内存):SetHistory 落盘会话目录 sidecar(<path>.history),Load 恢复(重启/切换会话一致;旧用户无 sidecar → 0 = 全部,向后兼容) | 重启不丢持久化模型/base_url;状态栏首帧即实际配置;history N 重启/切会话保持;`-race` 全绿(新增 resolveConfig/history 单测) |
| M6.13 工具调用纪律与伪调用提示 ✅ | 模型把工具调用格式写进回复正文(如 <tool_calls>/<invoke>/<antml:invoke>/<DSML>,未走 API 结构化 tool_calls)时 gah 不执行——此前该场景被当作普通文本回答静默结束,用户看到"模型说要调工具但无结果";本次双层防护:**① 系统提示纪律**(host-system-prompt 规则加:工具调用必须经结构化 tool_calls 发起、禁止正文伪调用标记、无可用工具不得假装已调用);**② 回合兜底检测**(agent-loop:无真实 tool_call 但正文含伪调用强信号标记 → 注入一条 user 提醒(含如何修正),再给一轮;每回合至多提醒 1 次防无限修正,maxSteps 兜底;提醒仅进程内注入不入会话历史,重建自确定性代码+上轮日志(日志已含上轮文本));host-tools 未注册工具名的结构化错误回传为既有行为(不变) | 伪调用不再被静默当作终答:模型收到提醒修正,或直接如实回答;二次仍伪调用即结束不空转;`-race` 全绿(新增 containsFakeToolCall/提醒上限/送达断言/纪律文字单测) |
| **M6.14 联网搜索 web_search** ✅ | **落点:同包扩展不新开插件**(plugins/tool/tool-web/ 内加第二工具 web_search——fetch/search 共享 http client/超时/错误结构;tool-web 已登记 catalogue/bundle,免全套新增)。**结构**:httpclient.go(抽共享 client:30s 超时/UA/响应上限)+ search.go(SearchTool:Definition/Execute)+ extplugins/tool-basic 注册 `web_search`(内置 Plugin 同注册,双轨现状不变,无 seed bump)。**工具面**:`web_search {query: string 必填, num_results?: int 默认 5 上限 10}`;返回归一 `{query, results:[{title,url,snippet(截断~300 字),published_date?}], truncated?}`(错误走 error 键,风格同 web_fetch);Description 提示模型:需原文正文再调 web_fetch(两工具协作零编排代码)。**provider 兼容性(用户要求:默认 Exa 且可换)**:包内极简 Provider seam——`type SearchProvider interface{ Search(ctx, query string, n int)([]Result, error) }` + provider 注册表(map,启动读 `data.provider` 选默认,缺省 exa);默认 `exaProvider` 直连 `POST https://api.exa.ai/search`(Authorization: Bearer <key>;**key 解析链:env EXA_API_KEY > `$GAH_HOME/config/search.yaml` 的 `api_key` > 空**——配置文件兜底不依赖 shell env(会话固化问题),GAH_HOME 宿主与 tool-basic 外部进程一致;文件还可配 `provider`/`endpoint` 兜底;坏 yaml 显式失败;**凭据通道已核实**:host-bridge 起外部工具进程用 os.Environ 全量,白名单化仅作用于 shell 再 spawn 孙进程,故 key 在 tool-basic 进程可读且模型经 shell 不可见);后续第二 provider 实现接口注册即换,consumer/schema 零改动(对齐 dsh-web provider 拆分思想,收敛单包防过度抽象)。**错误归一**:401(换 key)/429(限流→结构化提示)/5xx(可重试语义同 llm 适配器)/超时(共享 client)。**不做**:正文全文(web_fetch 承担)、结果 rerank(第二 provider 出现再说)。**切片**:T1 httpclient+exaProvider+SearchTool+装配+httptest fixture(成功/缺字段/401/429/截断/env 隔离);T2 打磨(snippet 质量/文档/plugins README tool 行补 web_search) | T1:模型可 web_search 检索并可选 web_fetch 抓正文;换 provider 经 data.provider 一行配置;EXA_API_KEY 凭据隔离(模型不可见);`-race` 全绿 |
| **M7 Web UI**(⏳ 未实施 · S1–S3 切片) | **ui-web-app 插件**:web 端操作,对齐 dsh Web UI 双进程思想(能力全在宿主,浏览器只订阅事件流);入口糖命令 `gah web` ≡ `--profile web`;`data.addr: 127.0.0.1:2233`(默认仅本机);双轨形态:① `web/` Go 运行时包(对称 tui/ 先例,插件不 import 之外的约束同 tui 薄壳例外):server.go(handler/SSE/confirm)+ events.go(session/event→SSE 帧,断线按 Seq 差集续传→Last-Event-ID 重放,sessionlog 天然支持)+ confirm.go(web 版 ConfirmService) ② `web-src/` 独立前端工程:Vue3 + Vite + TS(SFC,`<script setup lang="ts">`),`scripts/gen-web.sh`(对齐 gen-extplugins.sh 先例,产物落地 web/dist 入 gitignore,缺失主包构建失败)③ 插件 `plugins/ui/ui-web-app`(薄壳读 data:addr/auth_token/static_dir)④ `config/bundle-web.yaml` + `profile-web.yaml`(不含 ui-tui-app,ctx.confirm 唯一性靠 profile 互斥免代码);**使用时严格零构建**:运行时预编译 render、无 Node、CSP 天然安全,开发态 Vite HMR 经 `data.static_dir`/`GAH_WEB_STATIC` 覆写(dist 直读)可两全;**协议**(SSE 优先,WS 归 P3 通道 seam 升级,消息 JSON 冻结兼容):SSE 下行 `session/status/confirm` + REST 上行 `/api/input`(含 cancel)/`+api/confirm`/`/api/state`(model/sandbox/thinking/stats/session/running)/`/api/sessions`(列表/switch/new);**核心功能范围**:会话流式渲染、状态栏(含 host-usage-stats 上下文/缓存)/思中/执行工具、命令复用 ctx.commands(`/` 提示+分发,`/session` 会话切换)、审批弹层;**UI 槽位化预留**(P2 基础):四槽位 `data-ui-slot="stream/input/statusbar/confirm"` + `web-src/src/registry.ts` 类型化注册表(**冻结签名版本化 v1**,kebab-case 契约,`v-html` 禁令渲染层转义,多槽位绑定最小化+核心交互纯 REST 不绕模板);CI 加 `vue-tsc --noEmit` 与 go vet 对称 | S1 web/包(server/SSE/静态/槽位骨架)+ ui-web-app 薄壳 + catalogue/bundle/profile/seed 全套 + `gah web` + 端口 2233;S2 交互闭环(input 409 互斥/状态栏 state/confirm/session 切换);S3 Seq 断线重放 + auth_token + 文档与体积门回归;`gah web` 起 http://127.0.0.1:2233,web 上完整跑通 TUI 同集能力(会话/命令/审批/状态栏统计) |
| **M6.15 TUI 滚动与会话启动修复** ✅ | **滚动修复(根因)**:会话流渲染未对逻辑行文本折行——一条 Line 含 `\n`/超长文本(assistant 多段回复/新闻列表)时当单行渲染,输出行数撑爆终端窗口(97 行 vs 11 行窗口),输入行/状态栏被挤出屏幕、滚动视觉无效(offset 已变但屏幕内容错乱)。**改动(tui/render.go + state.go)**:`flattenLines` 把 Lines 展平为物理显示行(`\n` 分段 + 终端列宽折行,双宽字符不跨行;用户消息首行挂 `❯ ` 前缀且前缀宽度计入折行),滚动窗口/滚动条/`ScrollOffset`/`ScrollBy` 上限全部改按物理行(State.flatN 缓存展平行数),错误横幅同规则折行;`kitty 真机 + pty` 双验证(滚轮均生效)。**启动新会话**:host-cwd-sessions Start 改为 `svc.New()`(每次启动生成时间戳会话、空历史、首屏干净),不再自动 Load 主会话;旧会话保留在 `<key>.jsonl`/历史文件,经 `/session switch` 回溯;TUI 状态栏显示当前会话 id。测试:多行渲染受控/物理行滚动/前缀单次回归测试;全库 `-race` 绿 | 启动首屏干净且可滚;多行长回复不撑爆窗口;滚动按物理行精确 |
| **M6.16 滚轮风暴根因修复** ✅ | **bubbletea v2 鼠标转发死循环**:View.OnMouse 原样返回 MouseMsg(MouseWheelMsg 实现 MouseMsg 接口)→ tea 系统层 `case MouseMsg:` 再捕获同一消息无限重发(实测:kitty 注入 30 滚轮后死循环产 ~24 万/秒,CPU 180%;事件间隔 0ms 铁证内部循环;界面一直滚/抖动/卡死/Ctrl+C 无效)。minwheel 最小复现(AltScreen+OnMouse 原样转发=风暴;包装自定义类型=消失)定位。**修复(tui/model.go)**:OnMouse 转发包装为 `mouseEventMsg`(非 MouseMsg 接口绕过系统层捕获);滚动逻辑抽 handleWheel:50ms 节流 + 手势上限 gestureCap=24(>350ms 未滚或方向反转 = 新手势,换向立即响应) + skipView 缓存快速消化丢弃事件。验证:kitty 真机注入 30 滚轮后 CPU 0.0%、静置不自滚、反向可滚、双击 Ctrl+C 退出 | 滚轮可控;无自激风暴;方向可反;输入不排队 |
| **M6.17 输入键语义与滚动条拖动** ✅ | **↑/↓ 恢复为输入框光标头/尾**(历史浏览走滚轮/滚动条/PgUp/PgDn;
选择器激活时箭头仍移动选项);**滚动条鼠标可拖**(tui/model.go handleBarMouse):bar 列左键按下吸附跳转
(y→offset 比例,顶=最早/底=最新),按住 motion 拖动随鼠标垂直位移 1:1 更新 offset,释放结束;
滚轮每格滚 `wheelStep`=3 行提速。真机(kitty)验证 ↑↓ 光标、bar 点击/拖动;单测:↑↓ 光标头尾、
bar 吸附跳转/拖动位移/非 bar 不触发 | 方向键编辑;滚动条点击即跳、拖动跟手 |
| **M6.18 TUI 鼠标划选复制** ✅ | alt-screen+mouse tracking 下终端本地选择被应用占用——会话流内容区左键拖选形成选区(反色高亮、跨行),释放经 OSC52 写系统剪贴板;单击/Esc 清除;与滚动条拖动按列分流(bar 列=滚滚动条,内容区=划选);坐标映射按展平物理行+rune 列(双宽按列宽)。实现:tui/model.go(selRows 缓存/colAt/mouseRowAt/Press/Motion/Release/selectedText/OSC52)+ tui/render.go(styleForKind/selRange/renderRowSel)+ sel_test.go;kitty 真机拖选即进剪贴板 | 划选复制;无死循环;滚动条/划选互不干扰 |
| **M6.19 滚动条增强(TUI S1.2)** ✅ | 回底指示(offset>0 时滚动条底行显示 ▼,点击回最新)+ hover 加亮(bar 列悬停,auto-hide 期间保持)+ auto-hide(交互后静止 ~1.5s 且非悬停隐藏,滚轮/拖动/翻页重置计时,tea.Tick 驱动重绘)。实现:tui/state.go(BarShownAt/HoverBar)+ render.go(显示判定/指示 ▼/悬停样式)+ model.go(markBar/回底点击/hover 跟踪);单测 TestBarAutoHide/EndIndicator/HoverAndMark + kitty 真机(▼ 出现/静置隐藏/点击回底) | 浏览历史时 ▼ 回底;静止隐藏;悬停保持 |
| **M6.20 会话内搜索(TUI S1.1)** ✅ | `/search <词>`:匹配会话流 Lines 原文(大小写不敏感子串,命中逻辑行记录),渲染命中行整行背景高亮(当前命中更亮,renderSessionRow 与选区反色叠加);n/N/F3 循环跳转(Shift+F3 上一条),定位=命中行展平首物理行置顶;Esc 退出;输入中 n 不跳转;无命中自动退。实现:state.go(SearchQuery/Hits/Idx)+ model.go(searchRun/Goto/Jump)+ app.go(命令注册)+ render.go(高亮);单测 search_test.go + kitty 真机 | 搜索循环定位;高亮可见;Esc 恢复 |
| **M6.21 /search 交互修复(选择器自由级断点)** ✅ | `/search` 注册缺 Args → 选中即提交(空参报错),随后输入词走了普通消息通道发给大模型(表现为"搜索无效")。修复:① `/search` 补自由参数级 Args(FreeArgs "搜索词",同 /model 语义);② 自由级断点 Input 补尾随空格(picker.go advanceInto)——用户断点后直接打字即拼成 "/cmd <词>",否则粘连成 "/cmd<词>" 误拼进命令名(此缺陷影响全部自由级命令)。回归:search_cmd_test.go(Args 声明断言 + 完整用户流 断点→输入词→进入搜索态命中 2 行),picker_test 断点断言同步;全库 -race 34 包 ok(TestTUIProbeScrollbarAndArrow 为 pre-existing pty 首屏失败,与本次无关) | 选中 /search 断点等词;输入词回车进入搜索;直接打字不再粘连 |
| **M7.2 UI 槽位插件化**(⏳ 未实施 · P2;依赖 M7) | 加载器 + manifest + 安装命令三件纯增量(方案已预付一半):① 加载器:`index.html` 装配期扫 `~/.gah/ui-plugins/<id>/manifest.json`(声明覆盖槽位),`defineAsyncComponent` 动态导入按声明顺序覆盖;;② 插件形态:自带 Vite 小工程,`peerDependencies` 引宿主 `registry` 类型(TS 纯类型分发形态 P2 立项时三选一:workspace/npm local pkg/独立 ui-plugin-sdk 包——不提前抽象);;③ `gah -install-ui` 安装命令复用 host-plugin-manager 模式;HMR 边界写文档(插件 HMR 属各插件工程自身 dev server);;④ 契约校验:多插件覆盖同一槽位策略(顺序/显式)P2 定;`v-html` manifest 静态扫描拒装 | 换皮=整目录替换 static(已有);部分槽位覆盖零改协议;registry v1 兼容已发布 UI 插件 |
| **M7.3 WebSocket 通道 + 多浏览器会话**(⏳ 未实施 · P3) | 通道 seam(`web/eventsink.go` 抽订阅+写帧接口,本期 SSE 为第一实现;WS 第二实现同 payload 不同载体,前端 transport.js EventSource→WebSocket 自动降级);多浏览器会话鉴权(cookie session);Playwright 冒烟入 CI | 同一进程两通道可切;前端代码零返工 |
| **M8 工具类 todo** ✅ | **已交付 T1**:plugins/tool/tool-todo(todo 单工具 8 action:create/start/complete/pend/delete/update/list/get;4 状态机 pending→in_progress→completed + deleted 墓碑,状态变更仅专用 action;单 in_progress 约束(start 前置检查并提示当前项);blockedBy 依赖:成环/悬空/自依赖拒绝,start 前依赖须全 completed,被依赖任务 delete 拒绝;update 不改状态/id,blockedBy 经 add/removeBlockedBy 增删)+ extplugins/tool-basic 注册 + catalogue 登记 + config/seed 两份同步(seed 5→6)+ gen-extplugins.sh 重跑;sdk.ProjectKey 纯函数收口(memory/todo 共用,host-cwd-sessions 保留本地同步);全库 -race 36 包 ok。规划要点: | **纯工具外部化(与 tool-shell/files/web 全同构,宿主零感知)**:`plugins/tool/tool-todo/` 工具实现包 + `extplugins/tool-basic` 注册第 4 组工具(共享运行时单进程,合并零体积增量;工具名 `todo`,多 action 单 schema)。模型向任务清单:复杂多步任务(3+ 步/并行修改/评审校验)先建单、执行中推进、完成立即销单(状态变更专用 action,禁直接 update status——防“攒批”与跨态错乱)。**任务模型(4 状态机)**:`pending → in_progress → completed` + `deleted` 坟墓(状态变更仅 start/complete/pend/delete 四专用 action);字段:subject(祈使句短行)/description(长描述)/activeForm(进行时标签,供未来 TUI/Web spinner)/owner/metadata。**依赖**:blockedBy 数组(create 可带、update 可增删),环拒绝;并发:**同一时刻仅一个 in_progress**(start 前置检查,违反拒绝并提示先 start/pend 当前的)。**存储**:`$GAH_HOME/todos/<project-key>.jsonl`(ProjectKey 复用 host-cwd-sessions 同款派生——外部进程 import plugins/host/host-cwd-sessions 纯函数(P0 桥 extplugins 先例:tool-mcp import mcp-bridge export 同型)或复制小函数,实施时定;按项目隔离对齐会话);追加 jsonl + 全量快照末行(墓表同款:坏行容忍,sessionlog 同型);文件锁(回合串行下自然安全,工具并发多路调用时带锁)。**展示联动**:宿主/TUI/Web 展示 todo 面板归 M7.2 槽位注册表演示插件(宿主经 GAH_CB_ADDR 回调或查询工具读状态,零额外 seam);`-list-plugins` 可见;**切片**:T1 工具协议与存储(action 全集/状态机校验/锁/坏行容忍)+ tool-basic 注册第 4 组 + 单测(全生命周期/依赖环/并发锁/墓表优先);T2 展示联动(归 M7.2,S2 后实施) | T1:模型可全生命周期管理任务且状态机/依赖约束生效;坏行容忍;并行多调用不坏文件;T2:M7 面板实时反映任务推进 |
| **M9 tool-subagent 子代理委派** ✅ T1(one-shot)+ T2 后台控制(后台 delegate 子集)已交付 · send_message/fork ⏳ | **交付(T1)**:`plugins/tool/tool-subagent` 实现库(subagent 单 action delegate,窄接口 agentCaller 注入;进程内 Plugin 经 ctx.fanout,extplugins/tool-subagent 独立入口经 GAH_CB_ADDR 回调 fanout.agent,CbFanout 代理复用 NewTool)+ extplugins 独立二进制(崩溃隔离,gen-extplugins.sh NAMES 增 tool-subagent,embed 5 平台产物已重建)+ catalogue 登记 + bundle 样板同步(seed 6→7→8)+ tests/external_test 端到端(TestExternalSubagent:外部进程注册 → delegate → 宿主 fanout 子代理 mock 回结论)。 **交付(T2 后台控制)**:sdk.FanoutService 增 SpawnAgent/ListAgents/AgentStatus/KillAgent;host-fanout 引擎后台会话表(独立 ctx+cancel,运行/完成/失败/killed 状态机);host-bridge callback fanout.spawn/list/status/kill 分发 + cbFanout 代理转发;tool-subagent 工具扩 spawn/agents/agent_status/agent_kill(后台带句柄不阻塞,轮询取结论);单测(host-fanout TestSpawnAgent*、subagent stub 全 action、外部 e2e TestExternalSubagentBackground);gen 重建产物。send_message 需子代理消息循环、fork 需父事件流种入 → 归后续。

**规划(原文保留,供 T2/T3)**:引擎复用声明:**host-fanout(M6.8,ctx.fanout:agent/parallel/pipeline,独立子代理上下文、子会话隔离、仅结论回流父级)已存在,GAH_CB_ADDR 回调通道 fanout.agent 亦已具备(M6.9)**——零新引擎,零宿主改动;本次新增仅为**模型面向工具面**。两消费面共存:tool-workflow(starlark 编排,程序化批量)与 tool-subagent(自然语言委派,自主型)共享同一 fanout seam。**工具面**(单 schema 多 action)·`delegate`:task(委派目标/验收口径)+ 可选 tools 白名单/深度;·执行策略:**默认 one-shot 同步等子代理**(dsh 缺省);· 子代理上下文隔离。**切片**:T1 工具面+GAH_CB_ADDR 回调 fanout 桥+one-shot ✅;T2(M9.2)控制组(对齐 dsh tool-subagent-control:send_message/interrupt/list_agents)+ continuable 背景带手柄(复用 host-jobs 管道)+ fork;T3 展示联动(归 M7.2) | T1:模型可 autonomous 委派多步任务,仅结论入父上下文;starlark 与 subagent 两面共存不干扰;外部进程崩溃不拖垮父回合(T1 已达成,见 TestExternalSubagent) |
| **M10 tool-memory 跨会话操作记忆** ✅ | **已交付**:plugins/tool/tool-memory(memory 单工具 4 action + jsonl 追加/坏行容忍/人工可编辑,$GAH_HOME/memory/<project-key>.jsonl,项目 key 与 host-cwd-sessions 同款派生,只读工具零宿主感知)+ extplugins/tool-basic 注册 + catalogue 登记 + config/seed 两份同步(seed 4→5)+ gen-extplugins.sh 重跑;全库 -race 35 包 ok(TestTUIProbeScrollbarAndArrow 为 pre-existing pty 失败)。实现要点(**与 AGENTS.md 两层分工**:AGENTS.md(M5.5)已承担用户手写静态偏好层;本插件补**模型写操作层**(决策/踩坑/偏好会话内沉淀→跨会话可取)。参考裁剪至最小集(Hermes“自发在末尾留存+按需检索自身历史”+ mindspace“小规模人工可编辑”),**非目标显式拒绝**:分层体系(如 5 层)/闲时 LLM 自动提取/分块压缩(dsh-plugin-memory 类过度工程);**注入默认不做**(P1 纯工具零宿主感知,任务描述引导模型自主调用)。**工具**(单 schema 多 action,与 todo 同形态):remember(带 tags/后续检索)/list(近 N 条,预算限)/recall(需关键词或日期检索)/forget(标记废弃,物理保留可参照 sessionlog 坏行墓表)。**存储**:$GAH_HOME/memory/<project-key>.jsonl(ProjectKey 同 host-session-log 派生;追加 jsonl+坏行容忍+人工可编辑,mindspace 同三特性);跨会话:会话切换(M6.10)后 recall 可用(跨会话就是它的意义) | 模型可沉淀与召回跨期操作事实；人工可直接编辑 jsonl；与 AGENTS.md 两层不冲突(注入顺序 AGENTS.md 先,过预算条目不进)|
| **M11 tool-auto-plan 规划模式** ✅ T1+T2 已交付(参考 pi auto-plan extension;与 M8 tool-todo 分工:plan=规划期产物(先探索→结构化规划→等确认),todo=执行期任务追踪) | **交付(T1)**:`plugins/tool/tool-auto-plan`(auto_plan 6 action:create/get/list/step/confirm/complete + 生命周期 proposed→confirmed→completed + 步骤线性推进;存储 $GAH_HOME/plans/<key>.jsonl 追加/坏行容忍/锁)+ **规则注入**:Plugin Start 经 ctx.systemPrompt.AddSection(enable_rule data 开关默认开)。**装配形态偏差(实施记录)**:规则注入需宿主 ctx.systemPrompt,外部进程(tool-basic)无法注入 → 按 host-skills 先例改为**进程内装配插件**(catalogue Requires ctx.tools+ctx.systemPrompt,bundle 默认启用,不进 tool-basic——若进外部进程,AddSection 在子进程不可达且同 host-tools 重名注册冲突);seed 6→7。**交付(T2 联动)**:规则文本/Description 增补与 tool-todo 分工边界——auto_plan 管规划期产物与确认门,确认后执行期任务追踪交由 todo 承接(检查清单转 todo.create 按 todo 状态机推进,执行完回 auto_plan.step/complete 归档);新增 TestRuleTodoHandoff/TestLifecycleWithTodoHandoff 回归;plugins README tool 行同步**

**动机**:pi 同名 extension 语义 = 检测“计划/规划/方案/步骤/roadmap/plan”等规划意图 → 进入规划模式(**先只读探索,不动手修改 → 输出结构化规划(目标/关键约束与风险/实施步骤检查清单)→ 等待用户确认 → 再执行**)。gah 落为**工具类插件**(模型面承接,宿主零感知):**纯工具外部化(与 tool-files/web 全同构)**。**落点**:`plugins/tool/tool-auto-plan/` 工具实现包 + `extplugins/tool-basic` 注册第 5 组(共享运行时单进程;内置 Plugin 双轨注册同 tool-files)。**工具 `auto_plan`**(单 schema 多 action):create(request:探索后生成 目标/关键约束与风险/检查清单步骤 并落盘)/get(id)/list(当前项目全部)/step(id,index,status:待→进行中→完成,线性检查清单,**不上依赖图/blockedBy 防过度工程**)/confirm(id:用户“确认/执行/开始”后标记已确认,允许进入执行阶段)/complete(id:归档)。**规则注入**:插件 Start 经 `ctx.systemPrompt.AddSection`(Disposer 撤销)注入“规划模式”系统提示片段——用户请求**要求先规划后执行(意图判定而非裸关键词包含:消息本身已含明确执行指令(如“将规划写入文档”“仅/只 <动词>”的命令式)时不视为规划请求,按其指令执行——参考 pi auto-plan extension 误判教训,见本节)**或复杂任务(3+ 步)时,必须**先只读探索并 auto_plan.create 输出结构化规划,确认前不执行任何有副作用操作**(纯查询可做),确认后**按步骤逐项推进**(step 标记状态);`data.enable_rule` 开关(默认开;与 AGENTS.md 全局“操作确认”规则重复冗余时可关)。**存储**:`$GAH_HOME/plans/<project-key>.jsonl`(ProjectKey 复用 host-cwd-sessions 同款纯函数,外部进程 import(M8/M10 同款先例);追加 jsonl + 全量快照末行,坏行容忍,人工可编辑——session/memory/todo 同三特性);文件锁(回合串行下自然安全,工具并发多路调用时带锁)。**切片**:T1 工具协议与存储(create/get/list/step/confirm/complete 全集/步骤状态机/存储三特性/锁)+ 规则注入(AddSection+enable_rule 开关)+ catalogue 登记与 config/seed 两份 bundle 同步(seed 3→4,新增 base 条目必须 bump)+ tool-basic 第 5 组 + 单测(全生命周期/坏行容忍/并发锁);T2 打磨(与 todo 联动边界(规划确认后执行期由 todo 承接)文档/plugins README tool 行) | 模型面对规划意图先输出结构化规划并**等待用户确认(确认前零副作用工具调用)**,确认后逐步骤推进;规划落盘跨会话可取、人工可编辑;`-race` 全绿 |
| **M12 多 provider 并存** ✅ (2026-09) | **providerfile v2**:provider.yaml 改 `{active, providers[]}`(name/base_url/api_key/model,0600 原子写);旧单对象自动迁移视图(name=域短名),下次写盘落 v2;API:Add(upsert,首自动活跃)/SetFields/SetActive/Remove/UpdateModel(活跃)/Unset(活跃,删空即移除该 provider)/Clear——Load() 仍返回活跃 provider(adapter resolveConfig 不经改)。**sdk**:MultiProviderService 可选接口(类型断言,LLMService 接口不变,mock 零破坏)+ OpenAIFetchModels(直拉 /models,聚合非活跃端点用)。**host-llm**:Providers/AddProvider(同名 upsert 激活)/SetActiveProvider(Configure+SetModel 即切)/ListAllModels(TTL 10min;活跃走适配器缓存、非活跃直拉,单条失败记 Err 不整体失败)。**TUI**:/provider show(全列★)|add(名自动=域短名,首个自动活跃)|use(二级枚举切换)|set(编辑活跃=旧单 provider 流程不变)|unset|clear;/model 枚举=全部 provider 模型聚合(Value=`provider|model`,选中自动切所属 provider+SetModel;手动 /model 无分隔符走当前活跃向后兼容);状态栏来源=活跃域短名。**范围边界**:anthropic 适配器不参与 openai 聚合(单端点,claude 手输);/provider 暂不提供单条删除(upsert 覆盖;全清走 unset 删空/clear),记 TODO | /model 一次聚合列出所有 provider 端点模型(带来源),选中即切端点与模型;多 provider 并存可 add/use;旧 provider.yaml 与 /provider set 流程零干预迁移;新增 providerfile/multi_provider/multiprovider 三层单测;-race 全绿(pre-existing pty 探针除外) |

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
| Web UI 形态(已定 §14.1 M7) | web/ 与 web-src/ 双轨(对称 tui 先例);前端 Vue3 + Vite + TS(SFC);**开发态可构建,使用时严格零构建**(运行时预编译,无 Node,二进制内 embed 纯静态);零运行依赖红线不变 |
| Web UI 协议 | SSE 下行 + REST 上行(端口 2233,默认 127.0.0.1);WS 入 P3 通道 seam;消息 JSON 结构本期冻结(事件/状态/confirm/state/sessions)使其后兼容 |
| UI 插件化基底 | 四槽位 DOM 契约 + `registry.ts` 类型化槽位注册表(签名 v1,kebab-case 跨档契约,渲染层 v-html 禁令) |

---

> 待确认后从 M1 微内核开始实施。
