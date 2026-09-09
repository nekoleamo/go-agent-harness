# Go Agent Harness 设计方案

> 基于 DeepSeek Harness(dsh)"一切皆插件"设计哲学 + Cordis 可逆插件框架理念,使用 Go 实现。Cordis 插件系统 Go 化。暂不使用 Web 端,以 TUI 为界面。
> 参考项目:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)。
> 状态:已实施。M1–M6、P0–P4、M7 Web 线 + M16.7(Web 会话工作台/taste UI 收敛/工作区真实切目录/删除语义/tool-mcp 多 server)+ M16.8(可视化设置面板/输入一体外壳/左右分割/动效层/偏好持久化 TUI 共享/插件管理域)+ M16.9(便携数据根 gah-data + 便携纪律)全部交付(M7 起 Web 端已启用,与 TUI 双界面并存);**2026-09 二期五项全部交付**:Web jobs 面板(B1)/会话 export(B2)/命令下沉宿主(B3)/mcp-bridge 看护(B4)/UI 槽位 v2(B5);另交付:优雅停机端点 POST /api/shutdown、Web 附件(图片/文件)+多模态、快捷键(与兼容矩阵)、GAH_WEB_ADDR。**M17 审批等级三档(开放/智能/严格)+ M18 整体备份/恢复 已交付(见 §14.1 交付表)**:审批档 open/smart/strict 运行期切换(TUI /approval + Web 设置面板,偏好持久化);/backup 一键打包 GAH_HOME(确定性 tar.gz,排除 backups/ 自身,恢复前自动先备份当前态)。**policy-guard 融合 + seed-version 12**(policy-approval/policy-sandbox 合并单插件统一裁决,并行会话交付)。**R5 三端复查 + R6 观察项完善已交付(2026-09-16)**:~/.gah 旧解析链残留收敛(57739b1 弃用同步)、SSE 重放/订阅 gap 修复(先订阅后重放+Seq 去重)、WS 指数退避重连、桌面壳数据根恒传应用数据目录 + 启动失败窗口提示;DESIGN §14.1 未实施清单清零。**R7 桌面零成本发行工程(C2/C3 变体,2026-09-16)**:updater 接入(ed25519 自持签名 + GitHub Releases 端点 + 托盘检查更新)+ publish-desktop.sh + release-desktop CI 矩阵 + docs/RELEASE.md 无签名分发指引(见 §14.1 R7;待 repo 公开 + 首 tag 启用)。**R8 数据根收紧(2026-09-16)**:排除 GAH_HOME env 输入,数据根唯一 = 二进制同级 gah-data/(无则自动创建,禁 ~/.gah;桌面壳同步去应用数据目录,见 §14.1 R8)。交付总览以 AGENTS.md「便携纪律」与 DESIGN §14.1 交付表、docs/ROADMAP.md P3 表、docs/TODO_OVERVIEW.md 为准。**IM 远程控制线(P0–P3,2026-10-03)**:P0-1 宿主 seam(ctx.turnControl 回合取消 + job/done 事件 + web cancel 端点)、P0-2a im/ 运行时(入站管线/gate 访问控制/IM ConfirmService/mock 全链路 e2e;含 policy-guard confirm 注入时序修复)与 P0-2b 微信 ilink 通道 + 插件壳(扫码登录/长轮询/文本收发闭环;媒体 CDN 归 P0-2c)已交付,见 §14.1 IM 行与 docs/IM_REMOTE.md。

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
2. **外部插件**:`$GAH_HOME/plugins/` 目录(缺省便携根 gah-data/plugins,见 §7.3),经外部进程桥(host-bridge)加载(可选,缺失即降级为仅内置)。

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
| home 目录 | 数据根(**唯一 = 二进制同级 `gah-data/`**,2026-09-16 R8 起 `GAH_HOME env` 不再作输入源,设了也忽略并告警;不存在则首启自动新建并释放初始化内容);创建失败(目录只读)= **启动显式报错退出**——~/.gah/TempDir 亦已弃用;运行态由 main Setenv GAH_HOME 贯通插件层 |
| 首次启动 | 释放可编辑层(配置样板、sessions/)到 home;embed 资源只读共享,不重复释放 |
| 样板版本升级 | `bundle-*.yaml` 头部 `seed-version`;落盘版本低于 seed(或无版本旧样板)→ **备份(.bak-时间戳)后覆盖**——新增 base 能力条目(host-* 等)老用户自动补齐,零人工干预;版本一致/非 bundle 样板(profile/patch)不覆盖(用户自定义保留,应走 patch 层) |
| 会话日志 | `$GAH_HOME/sessions/<project-key>.jsonl`(主会话,跨期共享);切换会话 `<project-key>-<id>.jsonl`(id=创建时间戳);重启经 Load 恢复历史 |
| `--ephemeral` | 一次性模式:全部落 temp、退出即焚(适用于容器/CI) |
| 外部插件目录 | `$GAH_HOME/plugins/`(M5 桥加载;不存在 = 仅内置插件,正常降级);插件二进制**自动升级**:每次启动 sha256 比对 embed 产物与落盘内容,不同则覆盖(旧版能力缺失,如缺 web_search;不保留备份——plugins 扫描会加载 tool-* 前缀文件,二进制随包可再生;同版/用户自装产物跳过,幂等) |

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
| **M6**(已交付) | M6.1–M6.21 已交付:host-jobs/fanout/外部化(M6.8/6.9)/MCP server/指令与技能/插件安装/命令注册表/会话与统计(M6.10)/目录分组(M6.11)/配置修复(M6.12)/伪调用兜底(M6.13)/web_search(M6.14)/TUI 滚动与会话启动(M6.15)/滚轮风暴根因(M6.16)/输入键语义与滚动条拖动(M6.17)/鼠标划选复制(M6.18)/滚动条增强(M6.19)/会话内搜索(M6.20)/search 交互修复(M6.21)——细表见 §14.1 交付行,交付总览以 AGENTS.md「会话状态」为准;M7 Web 线(M7/M7.2/M7.3/M8-T2)已交付,未实施清单清零 | 全库 -race 绿 |
| **交付门**(已通过,P1 更新) | 单二进制 <40MB(P1 起附包插件)/ `CGO_ENABLED=0` / 六目标交叉编译 / 裸机 scp 启动(首启释放插件后可用) | ✅ 实测数据见 §7.6 |

### 14.1 未交付规划清单(M6,按需逐个实现)

> **状态图例**:标题 `✅` = 已交付实施;标题 `⏳ 未实施` = 规划待执行、尚未开工(规划条目正文为完整方案,按切片实施)。
>
> **未实施清单:M17 审批等级三档 + M18 整体备份/恢复已交付 ✅(2026-09,见下方交付行)**;2026-09 二期(Web jobs 面板/会话 export/命令下沉宿主/mcp-bridge 看护/UI 槽位 v2)全交付 ✅(→ DESIGN 交付表与 docs/TODO_OVERVIEW.md)。其余远期增量(会话树 Web 可视化 M7.2.1、类型分发等)见 docs/ROADMAP.md。已交付 ✅:M6.1–M6.21、M8-T1、M10、M11-T1、M11-T2、**M9 子代理全交付(one-shot + 后台控制 + send_message/fork,见下交付行)**、M12 多 provider 并存(2026-09)、**M13 TUI 主题外部化(2026-09)**、**M14 外部命令桥(2026-09)**、**M15 TUI π 式默认样式(2026-09,见下交付表)**、**M7 Web UI 全交付(S1–S3,见下交付行)**、**M7.2 UI 槽位插件化(见下交付行)**、**M7.3 WebSocket 通道(见下交付行)**、**M8-T2 展示联动(见下交付行)**。TUI 线全交付 ✅(S1.1–S2.2 + 折叠交互 + M13 主题外部化;S3.1 决策方案 A 记录在案;S3.2 框架覆盖)。外部命令桥交付后,新命令插件不再需要重编译 gah(见 M14 行与 docs/PLUGIN_DEV.md §4.1)。
>
> **体验改进(对比 pi 基线)**:P4 12 项**全部交付** ✅(2026-09 收口:P4-1 消息队列 · P4-2 @引用+Tab 补全 · P4-3 代码块高亮 · P4-4 会话命名 · P4-5 C6 多级上下文 · P4-6 多行输入/外部编辑器 · P4-7 工具视觉增强 · P4-8 /compact · P4-9 语义色 token 化 · P4-10 C1 会话树/分支(树可视化 UI 归 M7 后)· P4-11 /reload · P4-12 widget 槽位;细见 `docs/ROADMAP.md` P4 节)。
>
> **TUI 界面优化**(非里程碑,详见 `docs/TUI_OPTIMIZE.md`):S1.1–S1.6 全交付 ✅;S1.4 Markdown 轻渲染、S2.1 消息分组/工具行去重、S2.2 渲染组件化(render/session/chrome/markdown 四层)+ 折叠交互 已交付 ✅(2025-09/10 迭代);S3.1 主屏 scrollback 决策=方案 A 维持现状(2025-10,理由与 B/C 备选记录于 docs/TUI_OPTIMIZE.md S3),S3.2 框架覆盖 ✅(S3.2 synchronized output 由 bubbletea v2 框架自动启用 ✅)。
>
> **实施排期**:P0 ✅、P1 ✅、P2 ✅、P3 ✅、P4 ✅ 均已交付(P0:划选/搜索/滚动条/输入增强 + M8-T1 + M10 + /workspace;P1:S1.4/S2.1 + M9.1 + M11-T1;P2:S2.2/S3.2 + M9.2/9.3 + M11-T2 + S3.1 决策方案 A;P3:M7 Web UI 全家桶 + M7.2 槽位 + M7.3 WS + M8-T2 展示联动 + M13/M14/M16,完成于 2026-09)。

> **当前未实施清单:IM 远程控制线(P0–P3,2026-10-03 登记,规划正文见 docs/IM_REMOTE.md;P0-1/P0-2a/P0-2b(微信 ilink 文本闭环)已交付 ✅,见下;剩余 P0-2c 媒体 CDN、P0-2b-QQ、P1–P3)**。
> ✅ **IM-P0-1 宿主 seam(2026-10-03,IM 线第一步)已交付**:① **ctx.turnControl 回合控制**(host-agent-loop 提供,Provides 增 ctx.turnControl):Run 内部派生可取消 child ctx 并注册(token),回合结束注销;control 并发安全(注册表/快照后解锁调用/幂等);sdk.TurnControl{Running, Cancel};直接构造 Loop 无 tc 场景 nil 兼容(旧测试不破坏);② **job/done 终态事件**(host-jobs SetNotify → c.Emit;每任务完成恰一次,done/failed/killed;载荷 sdk.JobDoneEvent{ID,State},输出经 ctx.jobs.Output 取回不进载荷);③ **web /api/control 补 {cancel}**(经 ctx.turnControl;未装配 503;对齐其它可选服务)。测试:host-agent-loop TestTurnControlCancel(阻塞 LLM 挂起→Cancel→cancelled 收尾→Running 复位)+ TestTurnControlRegistry(注册/取消/注销/幂等);host-jobs TestJobDoneEventNotify(done/failed/killed 各恰一次);web TestControlCancel(200/503);go vet + 全库 `go test ./... -race -count=1` 全绿。动机:IM 远程控制线(P0)前提——此前回合取消无宿主服务(TUI 私藏 cancelFn、web 用 Background ctx 无法取消)、job 完成无事件(只能轮询);该 seam 任何 UI(TUI/Web/IM)共用受益。Web 前端取消按钮留 P1/Web 迭代(端点已可用)。
> ✅ **IM-P0-2a im 运行时 + mock 全链路(2026-10-03,同批交付)**:新增根下运行时包 `im/`(web/ 先例,只 import sdk;非插件不入 catalogue)——① **域模型与 Transport**:Route(Channel/UserID/ChatID + Key/SenderKey)、Inbound(MsgID/Text)、Transport 接口(SendText 回推,入站逐条 HandleInbound);② **访问控制 Access**(三态 disabled 默认静默 / allowlist / pairing 配对码 1h 过期、同人复用、上限 32 防刷、ApprovePair/Allow/Revoke/List);③ **Bridge 入站管线**(gate → 去重(通道消息 id,5min 窗口)→ 交互归属判定(回合中确认回答先回填,防打断)→ 命令(/stop /im 自处理 + 宿主 ctx.commands 分发)→ 回合驱动(busy 串行回忙提示,queue 留 P1);回合内多步 assistant 经 SessionLog 回放按 seq0 游标聚合**最终一条文本**回推;回合取消经 ctx.turnControl(/stop));④ **IM ConfirmService**(实现 sdk.ConfirmService,plugin 壳 Provide ctx.confirm):确认推给当前回合归属用户 + pending 按会话归属,文字回答 y/批准/n/拒绝 回填,未识别词提示继续等待,ctx 超时安全拒绝;⑤ **RegisterCommands**(/stop、/im status|pair|list 注册进宿主 ctx.commands,Disposer 撤销);⑥ **配套修复:policy-guard ctx.confirm 注入时序 bug**——Start 一次性注入会因 ctx.confirm 由 UI/IM 插件后启动(拓扑无约束)恒为 nil,smart 档永远无通道拒绝;改 tools/pre-execute 每次现取(未装配仍安全拒绝,语义不变)。**测试**:im 单测 9 项(gate 静默/聚合/busy/confirm 批准拒绝与未识别提示/stop 转发/pairing 流程/去重/im status/词表)+ tests/im_e2e_test(真实宿主装配:ApproveFlow(危险命令确认→y→执行→回推)、DenyFlow(拒绝=veto 工具、tool/result Error 入会话、回合由模型继续——gah 语义)、UnknownUserBlocked、HostCommandsRegistered);policy 时序由 e2e 后注入 ctx.confirm 证明生效。**安全语义记录**:smart 档用户拒绝 = 该工具调用被 veto 且错误回传模型(模型可解释修正),不中断回合;硬中断由 strict 档承担。全库 `-race` 绿。真实通道(ilink/qqbot)与插件壳归 P0-2b。
> ✅ **IM-P0-2b 微信 ilink 通道 + 插件壳(2026-10-03,同批交付;文本闭环,媒体 CDN 归 P0-2c)**:① **`ilink/` 协议包**(官方 iLink SDK v2.2.0 wire):客户端(POST 鉴权头 ilink_bot_token/Bearer/X-WECHAT-UIN;业务错误 HTTP200+ret/errcode 显式校验;长轮询 getupdates ~35s 超时空转;sendmessage 含 context_token/client_id 幂等;typing getconfig/sendtyping)+ QR 扫码登录(FetchQR/轮询 LoginQRFromToken)+ 凭证 yaml 存取(0600)+ SessionExpired(ret=-14)识别;媒体项占位(CDN 解密 P0-2c)。② **`plugins/ui/ui-im-wechat` 插件壳**:transport(poll loop 断线退避/会话过期停等重登/入站 token 缓存回显;SendText 先 typing)+ 装配(注入 agentLoop/sessions/turn → im.New → Provide ctx.confirm(与 tui/web profile 互斥)→ /stop /im(桥)+ /wechat login|status(通道命令));已登录自动启动 poll;扫码登录后自动授权扫码者(allowlist 持久入凭证 yaml;配对/手动授权持久化归 P1)。③ 样板:bundle-im-wechat(seed-version 1)+ profile-im-wechat(config+seed 两份同步)+ bundles/im-wechat 装配层 + catalogue 登记(Provides ctx.confirm,Requires agentLoop/sessions,Bundle=im-wechat,Manage=scenario)+ catalogue_test bundle 白名单扩 im-wechat。**测试**:ilink 单测 6 项(mock iLink 服务器:收消息/ExtractText 媒体占位/sendmessage body 与鉴权头/业务错误与会话过期/长轮询超时空转/QR 登录 wait→confirmed/凭证往返 0600)+ tests/im_wechat_e2e_test(真实装配 base+im-wechat + mock iLink:入站→agent 回合(mock llm)→sendmessage 回推断言;未授权用户静默丢弃;凭证/授权持久化未被轮询打扰);全库 46 包 `-race` 绿。真机验收(扫码登录/实收实发/审批 y·n/长回合 /stop)需真实微信账号,记录于 docs/IM_REMOTE.md 待人工。**P0-2b 剩余**:QQ 官方 bot v2 通道(独立插件 ui-im-qq,共享 ilink→im 同型装配)与微信媒体(图片/文件 CDN AES-128-ECB)归后续切片。
> ✅ **IM 真机反馈修复(2026-10):危险删除类模式补齐** —— smart 档对 `rmdir`/`unlink`/`rm -f` 此前漏网直接放行(仅「递归删除」`rm -rf` 命中);approval.go 补两条模式(rmdir;rm -f/unlink),TestMatchDangerous 加对应用例。保守误报说明:`echo rmdir` 等命令文本含删除词的也会触发确认(可拒,安全偏宽)。policy 全库 `-race` 绿。
> ✅ **IM 真机反馈增强(2026-10):长回合 typing 指示** —— 反馈:微信发消息后等待过长,无法判断仍在工作 vs 断联。方案:`im.TypingAware` 可选能力(Transport 实现 ShowTyping/StopTyping;未实现静默跳过),`im.Bridge.runTurn` 回合开始 Show/结束统一 Stop(成功与错误路径);微信 transport 周期刷新(每 ~20s 重发 status=1,iLink typing 状态生命周期短),回合结束发 cancel。单测 TestTurnTypingIndicator(show→stop 时序,成功/错误路径)+ wechat e2e 断言 typing show 到达。跨网络说明:架构为出站长轮询,不需与微信同网/公网入站(gah 能出 443 即可)。
> ✅ **IM 网络反馈调研落地(2026-10,详 docs/IM_REMOTE.md §6)**:① 已实施:长文本分块(splitLongText 2000 字,段落→行→空格→硬切;≤10 块/回合,超限截断+continue 提示;段间 300ms 防短窗口截断——社区头号坑 hermes #46529/cc-connect #1098 实证)+ typing keepalive 5s(iLink 建议)+ 语音转写采纳(voice_item.text);② 调研要点(来源 openclaw-weixin#142/hermes#46529/corespeed#76/qwen-code#3993/QQ 官方文档):微信短窗口 ~10 条截断仅能聚合+分块规避;主动消息 24h/条数冻结无协议内自愈;图片收发灰占位与 CDN token 过期归 P0-2c;QQ 端设计借鉴:被动回复 5min 时效(长任务转 host-jobs + 主动配额记账)、markdown/图片/键盘原生呈现+降级、msg_id/msg_seq 幂等、input_notify 复用 im.TypingAware、群场景(ChatID 路由+@+群 allowlist)为微信没有的差异化能力。⏳ 未实施:出站发送预算层(P1,微信/QQ 共用)与 QQ transport 呈现/时效分层。
> ✅ **IM 真机反馈修复 2(2026-10)**:① smart 档删除操作全形态覆盖——rmdir/rm -f/rm -v 先后漏网后收敛为单条「删除操作」`\brm\b|\brmdir\b|\bunlink\b`(rm 任意 flags/裸 rm 均确认;文本级启发,含词即保守触发);② `bot_agent` 对齐产品名 GoAgentHarness/0.1.0(微信端“openclaw”若属腾讯 ClawBot 产品层标识则协议不可改,需真机验证)。
> ✅ **IM 危险操作全面复查(2026-10)**:① 工具面复核——危险删除仅经 shell 工具(tool-files 无删除动作:只 read/write/append/edit),policy 在 shell 单点拦截即全覆盖、无绕过面;② 模式补齐:删除增强(shred/truncate/find -delete)、git 强推含 --force-with-lease、git 破坏性操作(reset --hard / clean -f,clean flags 组合 -fd/-df 均命中)、chmod 777 覆盖 -R 与参数顺序变体(`\bchmod\b[^\n]*\b777\b`)、特权含 pkexec;测试矩阵 25 例含 false 守卫(git push 无 force/git reset --soft/chmod 644/+x/echo 均不误伤);全库 47 包 -race 绿。
> 🚧 **IM 远程控制线(未实施,P0–P3 分期)**:通过微信(iLink Bot API,个人号)/QQ(官方 Bot v2)远程命令操作 gah。**研究结论(对比 omp-wechat · hermes · dsh-im,证据与对比表见 docs/IM_REMOTE.md)**:① dsh 官方无 IM 通道,社区实现分两极——简单 clone(dsh-weixin 等)与成熟 dsh-im(9+ 渠道,含二维码登录/多机器人/结构化交互/主动投递实测);② gah 现有 seam 可零引擎改造承接(ctx.agentLoop.Run 注入、ctx.confirm 审批、ctx.commands 命令、session 事件流回推),但暴露三个宿主缺口——**回合取消无宿主服务**(TUI 私藏 cancelFn、web 用 Background ctx 无法取消)、**host-jobs 完成不发事件**(只能轮询)、**confirm 仅文本 bool 无结构化交互**(dsh ask_user_question/多选);③ **微信 iLink 主动投递受限**(dsh-im Issue-151 实证:服务端消息额度 ret=-2 prepare failed、约 10 条后拒发、重启不恢复、须接收者先发消息才恢复;context_token 持久化仅部分缓解)→ 微信只做响应式通道,主动投递(后台任务结果等)仅 QQ/飞书承诺;④ hermes delivery ledger(至少一次 + ♻️ 重复标记)与 dsh-im「产物显式登记」优于 omp outbox 目录 diff。分期:P0 = ①宿主回合控制 seam `ctx.turnControl`(取消/忙闲,agent-loop 内部 WithCancel)+ `job/done` 事件(web /api/control 补 cancel,三方 UI 共用)②ilink/qqbot 协议客户端 + im/ 共享运行时(访问控制默认拒绝+配对/chat↔session 映射/IM ConfirmService pending 归属判定不抢占/事件流→IM 回推/typing/分段/seq 去重/凭证与状态按 bot 持久化);P1 = 会话绑定命令面(/new /status /stop /history /sessionlist)+ 忙时 queue 语义 + lastMessageError 脱敏诊断;P2 = delivery ledger 可靠投递 + 后台任务回投(仅 QQ 承诺,微信受限);P3 = agentLoop 增 ask_user_question 多选语义交互 seam,统一 confirm/question 事件化(对齐 dsh Session 事件模型)。进度以交付表 IM 行与 docs/IM_REMOTE.md 为准。
> 🔧 **历史瘦身(2026-09-16)**:仓库 .git 641MB(早期未压缩 embed 裸二进制 ~100MB + .gz 产物 6 代迭代 ~400MB,gzip 高熵不可 delta)→ `git filter-repo --strip-blobs-bigger-than 1M` 清理全部 >1MB 历史 blob 后重建 HEAD 产物,**641MB → 113MB**(pack 99.5MB 几乎全为当前 embed 产物);提交 hash 全部改写,本文件与 docs/ 中引用的 commit hash 已按 filter-repo commit-map 同步替换(残留 0);备份 /tmp/gah-backup-641.bundle。后续 embed 产物每次变更仍会新增 blob(不可压缩),属设计内。
> 🚧 **R7 桌面壳零成本发行工程(C2/C3 变体,2026-09-16 铺,待 repo 公开 + 首 tag 启用)** 已交付工程侧:用户决策①repo 公开②接受无签名③先铺工程。①**updater 接入(C3 零成本)**:Cargo.toml + `tauri-plugin-updater`;main.rs 注册 Builder + 托盘「检查更新…」菜单(checkForUpdates:check → download_and_install → 通知+重启;失败仅通知不打断);tauri.conf `plugins.updater`(active/endpoints=GitHub Releases `releases/latest/download/latest.json`/pubkey);ed25519 密钥 `tauri signer generate`(~/.tauri/gah.key 无密码;公钥入 conf,私钥不入库)。cargo check 过(4m 新 crate)。②**发布流水线(C2 零成本,不购证书)**:`scripts/publish-desktop.sh`(platform darwin-aarch64/x86_64/windows-x86_64:go build sidecar → tauri release bundle(--config 覆写 version)→ 提取 .app.tar.gz/dmg/setup.exe → npx signer sign 每产物 → latest.<平台>.json;merge 模式 jq 合成含全平台 latest.json);`.github/workflows/release-desktop.yml`(tag v* 触发;macos-14 aarch64 + windows-latest x86_64 两 job + merge-upload 合成 latest.json 并经 softprops 上传 Release;secret TAURI_SIGNING_PRIVATE_KEY)。③**无签名分发指引**:docs/RELEASE.md(首次启动右键打开/`xattr -dr com.apple.quarantine`/SmartScreen 放行;密钥管理;本地发布路径)。tauri.conf bundle.targets 增 nsis(currentUser);docs 登记同步(TODO C 组/ROADMAP P2P3/DESKTOP_FEASIBILITY §9§10/README 索引)。**验收待真机**:repo 公开后首 tag v0.2.0 触发 CI → Release 含安装包+latest.json → 旧版托盘检查更新自动升级;win runner NSIS 真机调。
> ✅ **R8 数据根收紧(2026-09-16,取代 R6 的应用数据目录方案)** 已交付:用户决策「排除 GAH_HOME env,只允许 gah-data 存数据,无则自动创建,禁止 ~/.gah」——① `cmd/gah homeDir()` 移除 GAH_HOME env 输入源(仅便携 gah-data 自动新建;用户设不一致 env 启动告警不生效);boot 报错文案同步;main_test 补 `TestHomeDirIgnoresEnv`。② 桌面壳 `desktop/src-tauri/src/main.rs`:spawn 不再传 GAH_HOME、删除 appDataHome()(系统应用数据目录方案作废),数据根 = sidecar 同级 gah-data(Contents/MacOS/gah-data);启动失败提示改 gah-data 可写性指引(并提示升级 .app 前 /backup)。③ 文档同步:README/README_EN(便携节+env 表 GAH_HOME 行)、AGENTS.md「便携纪律」(解析链去 env 输入、写盘路径经 $GAH_HOME 派生语义注)、DESIGN §7.3、docs/DESKTOP_FEASIBILITY(§2/架构/落地变更/§10 R8 决策)、docs/RELEASE.md(数据根 = sidecar 同级 gah-data,升级替换需 /backup)、start.sh(去 export GAH_HOME)。验证:cargo check 通过;pty 探针重构(bin 同级 gah-data 预置数据,去 GAH_HOME env);全库 44 包 go test -race 全绿。副作用记录:.app 升级会替换 Contents/MacOS/gah-data,桌面形态数据保留依赖 /backup(文档已注);桌面壳数据不再存应用数据目录。
> 📌 **远期改进观察(2026-09-16,PI_COMPARISON 整合 dsh 后)**:非排期待办,按需启用——pi 线:C4 分支摘要/E5 toast/E4 配置热更/T8 键位自定义;dsh 线:session_search 工具面(中)/delegation 边界审批 pin(低中)。对比基线见 docs/PI_COMPARISON.md(pi + dsh 双参考)。
> ✅ **R9 全局安装与 symlink 数据根补正(2026-09-16,承接 R8)** 已交付:① **全局安装(方案 A,pi 式,两模式并存)**:`scripts/install.sh`(macOS/Linux:构建 → `~/.local/gah` + symlink `~/.local/bin/gah`,INSTALL_DIR/BIN_DIR 可覆盖、`--uninstall`、PATH 缺失提示)+ `scripts/install.ps1`(Windows:构建 → `%LOCALAPPDATA%\gah` + **用户级** PATH,`-InstallDir`/`-Uninstall`)——装完任意目录 `gah` 启动;**两种模式并存由部署位置决定**(全局共用:统一数据根 + cwd project key 隔离;便携单飞:cp 到任意目录即独立 gah-data),互不干扰。② **symlink 数据根修复**(实测暴露):PATH symlink 启动时 `os.Executable()` 返回**入口链接路径** → 数据根错误落符号链接目录;`cmd/gah homeDir` 增 `execRealPath`(EvalSymlinks 归一)→ 数据根跟随**真实二进制**(全局 symlink 场景与便携拷贝语义一致);补 `TestExecRealPath`(含 mac `/var→/private/var` 归一断言);`TestHomeDirIgnoresEnv` 期望同步归一。③ **文档**:README(中英)Quick start 增「全局安装」小节(跨平台用法/两种模式对照);DESIGN 未实施清单 R8 后登记;scripts 入库(不受 docs 忽略影响)。验证:install.sh 实测(临时目录安装/符号链接启动数据根落真实目录);全库 44 包 `go test -race` 全绿。
> ✅ **R6 观察项分析与完善(2026-09-16,承接 R5 三端复查的 3 个观察项)** 已交付:①**SSE 重放/订阅 gap 产品级 bug**(R5 观察项 3 flake 根因实为 `web/server.go` `consumeStream` 先重放后订阅——重放快照与 `hub.Stream()` 之间广播的帧永久丢失;非测试时序问题)→ 改为**先订阅后重放 + 按 Seq 去重**(seen 游标,非会话帧 ID=0 不参与);新增 `web/consume_test.go` `TestConsumeStreamGapNoLossNoDup`(阻塞式 Replay stub 精确构造窗口;回退旧实现即失败=灵敏度验证);`tests/web_e2e_test.go` 调序为先建 SSE 再提交回合(对齐真实前端时序,实时路径覆盖)。②**WS 掉线永久降级 SSE**→ `web-src/src/transport.ts` WsTransport 增指数退避重连(1s/2s/4s,带 after 游标差集续传,onopen 清零退避),持续失败(约 7s)才降级 EventSource(浏览器自愈);vue-tsc 通过。③**桌面壳数据根**(R5 观察项 2)→ `desktop/src-tauri/src/main.rs` spawn 时**恒传 GAH_HOME**:显式 env > 系统应用数据目录 `appDataHome()`(macOS ~/Library/Application Support/gah,Linux XDG_DATA_HOME|~/.local/share/gah,Windows %APPDATA%\gah)——壳内便携根(.app/Contents/MacOS/gah-data)随升级丢失且可能只读不作桌面默认;启动 12s 未就绪时窗口注入失败提示(含数据根路径,不再永远停在“正在启动”);`cargo check` 通过(仅存量命名 warning)。验收:全库 44 包 `go test -race` 全绿;consumeStream gap 单测灵敏度验证(旧实现 FAIL/新实现 PASS);O3 e2e 连续通过。
> ✅ **R5 三端复查(2026-09-16,承接 57739b1「弃用 ~/.gah」纪律清理)** 已交付:全库基线 44 包 -race 绿(含并行会话 policy-guard 融合);审查确认 `~/.gah` 旧解析链残留 4 处代码 + 2 处规范/注释漂移,统一收敛为「GAH_HOME env > 二进制同级 gah-data/(首启自动新建);不可便携= boot 报错退出,~/.gah/TempDir 兑底弃用;运行态 GAH_HOME 恒设,空(仅嵌入/单测)宁回 TempDir 不落 cwd/根」:① `plugins/ui/ui-web-app/web.go` uiPluginsHome 此前空 GAH_HOME 回退 **$HOME 根**(→ ~/ui-plugins、~/attachments 外泄,最重)改 TempDir;② `tui/theme.go` themeHome 回退 ~/.gah 改 TempDir(注释去失效 exakey.go 引用);③ `tui/app.go` pluginHome 同款;④ `desktop/src-tauri/src/main.rs` 顶部注释同步新链;⑤ AGENTS.md「便携纪律」解析链描述同步。审查确认无确定功能 bug(前端 timer/listener 清理完整、web Go 无 goroutine 泄漏信号、transport WS→SSE 降级设计正确);观察项记录:transport.ts WS 掉线后不重试 WS(永久降级 SSE,设计取舍);desktop .app 未设 GAH_HOME 时 sidecar 便携根落 Contents/MacOS/gah-data 的权限与 GUI 提示待完善(需设计决策)。另:TestWebEndToEndTurn 全量并行下偶发 flake(SSE 断言窗口时序,单跑稳定过,与本次改动无关)。
> ✅ **Q1 三端 bug 修复** 已交付:① `web-src/src/components/JobsPanel.vue` 轮询改 `watch(open)` 启停(immediate 首开即拉,关窗 clearInterval,组件常驻/v-if 控显语义不变,App 5s 徽标轮询未动);② `plugins/host/host-backup/backup.go` `backupCmd` 对 `/backup ~/<名>` 展开 `~`(复用 host-internal-commands 同款逻辑,os.UserHomeDir + TrimPrefix;dest 与展示分支同用展开值)。
> ✅ **Q2 web 端 token 化** 已交付:style.css 补 `--ok-soft/--err-soft/--err-line/--tool-soft/--tool-line/--tool-strong/--overlay/--fg-on-accent` 八枚 token(soft 系浅背景/浅边框/深字、遮罩统一 0.28、accent 钮反白);App/ConfirmDialog/ConfirmBar/InputBar/JobsPanel/SettingsPanel/Sidebar/StreamView 八组件 29 处裸色值(含 rgba 遮罩 4 处)全部 token 化,`grep '#..|rgba'` 组件目录零残留(存量 style.css 内仅剩 token 定义与全局 tooltip 基础样式);vue-tsc + vite build 通过。
> ✅ **Q3 注释与加固** 已交付:① `desktop/src-tauri/src/main.rs` 两处注释同步为便携根解析链(GAH_HOME env > 二进制同级 gah-data/(首启自动新建)> ~/.gah > TempDir),代码行为不变;② `web/server.go` SSE 增 `retry: 3000` 断线重连指令(与 Last-Event-ID 续传配合)+ `X-Content-Type-Options: nosniff`。
> ✅ **Q4 提交纪律** 已交付:Q1–Q3 + M17/M18 + seed-version 11 一并落库(`b98eb17`,216 文件;**纠偏:.gitignore 原只忽略 `/web-src/node_modules/`,示例 UI 插件 `web-src/examples/*/node_modules`(2145 文件)裸入库风险→补 `web-src/examples/**/node_modules/` 规则**,清除 ci.yml.snip 0 字节残留);Q1 补单测 TestBackupCmdTildeExpand(`8be6cf8`);全库 45 包 `go test -race -count=1` 全绿 + go vet 通过,工作区干净。

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
| **M7 Web UI** ✅ 全交付(2026-09,S1–S3 一次完工) | **交付**:① `web/` Go 运行时包(对称 tui/ 先例):server.go(SSE 下行 + REST 上行:input(round 409 互斥)/confirm/state/sessions/commands/control + 静态托管 embed web/dist,`data.static_dir` 覆写=开发态 HMR)+ events.go(订阅 session/event+agent/status+agent/error → SSE 帧,id=会话 Seq,断线 Last-Event-ID/`?after=` 从 sessionlog Replay 重投续传)+ confirm.go(Web 版 ConfirmService:confirm 帧推送 → REST 应答,ctx 取消按安全默认拒绝)。② `web-src/` 前端工程:Vue3+Vite+TS SFC(`<script setup lang="ts">`):App(SSE 消费=历史重放+实时共用消费引擎,chunk 拼 pending/assistant message 落定/工具行折叠)+ StreamView(会话流)/StatusBar(模型·思维·沙箱·会话 + 上下文·缓存使用率)/InputBar(`/` 命令提示自 ctx.commands,会话切换抽屉,思维/沙箱循环)+ ConfirmDialog(审批弹层);registry.ts 四槽位契约 **v1 冻结**(stream/input/statusbar/confirm,`data-ui-slot` DOM 标注,优先级覆盖,M7.2 插件覆盖基点)。③ `plugins/ui/ui-web-app` 薄壳(读 data.addr/auth_token/static_dir → 注入+订阅+Provide ctx.confirm)+ `config/bundle-web.yaml`+`profile-web.yaml`+seed 两份同步(新 bundle,seed-version 1)+ catalogue 登记(Bundle=web)+ `bundles/web` 装配层 + `cmd/gah` 入口糖 `gah web` ≡ `--profile web`。④ `scripts/gen-web.sh`(npm 类型检查 vue-tsc + vite build → web/dist;产物 gitignore,embed 缺失主包编译失败=天然护栏)+ CI(setup-node + gen-web.sh)+ goreleaser before hook。⑤ **启动自动开浏览器**:`data.open_browser` 缺省 true(env `GAH_WEB_OPEN=0/false` 显式关,CI ws-smoke 已置 0);`web.Server.Start` 改为**先实际监听成功**再打 listening 日志并 Serve(监听失败返回错误,不再假 listening),监听成功后 OnReady 回调触发平台命令(darwin `open`/linux `xdg-open`/windows `rundll32`,异步启动,失败仅记 warn 不阻塞)。⑥ **通用 REST 能力面(为前端增删改铺路)**:工具清单/调用(`GET /api/tools`、`POST /api/tools/<name>` 参数 JSON 透传)、后台任务(`GET /api/jobs`、`GET /api/jobs/<id>`、`POST /api/jobs/<id>/kill`)、命令直接执行(`POST /api/commands/<name>` {args},绕过 `/` 前缀)、插件管理(`GET /api/plugins`、`POST /api/plugins/<id>/load|unload`)、模型聚合(`GET /api/models[?all=1]` 多 provider 聚合/单适配器列表)、provider CRUD(`GET/POST /api/providers`、`POST /api/providers/<name>/use`;删除按 M12 决策显式 501)、会话改名(`POST /api/sessions/rename`)、会话分支/克隆(`POST /api/sessions` action `fork{seq}`/`clone`,ForkableSessions 断言)、指令热更(`POST /api/reload`)。可选服务未装配 → 503/501 显式错误不静默;web 增注入 ctx.jobs/ctx.pluginManager/ctx.systemPrompt(可选);ui-web-app 侧栏(会话+工作区历史)依赖 workspaces 端点(M7 后续迭代)。**实施偏差记录**:TUI 内部命令(/session//model 等)注册在 tui/app.go registerInternalCommands,不下沉宿主——web 侧以等价 REST 承接:会话切换=`/api/sessions`(switch/new),模型/思维/沙箱=`/api/control`;ctx.commands 注册的宿主命令(/jobs 等)与外部命令插件(M14)在 web 侧经 `/` 前缀自动分发(命令输出回 command 帧)。**测试**:web 包单测(events 帧/Seq/ReplayAfter、confirm 应答/取消、server 路由/409/命令分发/会话/鉴权/静态/SSE 重放)+ tests/web_e2e_test(真实宿主装配:回合事件流全链路 user→tool→assistant→turn/end、命令分发、会话列表/状态)+ 真机冒烟(`gah web` + mock:回合事件流完整,产物 ~85KB);全库 -race 绿。交互验收清单见 docs/VERIFY.md(Web 节)。| ✅ `gah web` 起 http://127.0.0.1:2233 并**自动打开浏览器**(可关);web 上完整跑通 TUI 同集能力(会话/命令/审批/状态栏统计);断线续传/auth_token/input 409;-race 绿 |
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
| **M7.2 UI 槽位插件化** ✅ 交付(2026-09,依赖 M7) | **交付**① **加载器**(web-src/src/plugins.ts):装配期 fetch /api/ui-plugins 聚合清单,对每槽位覆盖声明动态导入(`import(/* @vite-ignore */ \`/ui-plugins/<id>/<module>\`)` 绝对路径防产物重写)并按 priority 注册(registry.ts 同槽位降序、同优先级后注册者胜;未知槽位忽略)——默认实现保持,失败静默不阻塞。② **插槽契约**:manifest.json {id/version/out/slots[{name,priority,module}]},module 相对插件根指向产物入口(如 ./dist/plugin.js);构建输出 out 目录保留层级落位;槽位名契约 v1 冻结校验(未知拒装)。③ **安装命令**(internal/install/uiinstall.go + cmd/gah `-install-ui`/`-uninstall-ui`/`-list-ui-plugins`):spec=repo[@version] 或本地目录(git clone/拷贝)→ **v-html 指令形态静态扫描拒装**(v-html= 才命中,注释提及不误伤)→ npm 构建 → 产物+manifest 落 $GAH_HOME/ui-plugins/<id>/;卸载删目录,重载页面即回默认。④ **server 托管**:/ui-plugins/ 静态(StripPrefix,DirFS)+ /api/ui-plugins 聚合(读盘即时,坏 manifest 记日志跳过);ui-web-app 传入 UIPluginsDir(默认 $GAH_HOME/ui-plugins)。⑤ **示例插件**(web-src/examples/statusbar-demo):Vite lib 模式单文件产物(vue 打进,浏览器独立运行);覆盖 statusbar 展示模型徽标。实现注:peerDependencies 类型分发未抽象(三选一留 M7.2.1 或生态成熟再定,宿主 registry 类型文档化即可);插件 HMR 属各插件工程 dev server(文档注明)。**测试**:uiinstall_test(v-html 扫描/注释不误伤/manifest 校验/拒装路径/卸载/ListUI/copyDir 排除 node_modules 与隐藏目录)+ 真机链路(install-ui → 聚合 → 静态托管 200);copyDir 相对路径 "./" 前缀被 Walk 清理导致 TrimPrefix 失配的坑已修(src 绝对化)。| ✅ `gah -install-ui <repo|本地目录>` 装完即生效(重载页面);槽位覆盖零改宿主代码;registry v1 兼容;-race 绿 |
| **M7.3 WebSocket 通道** ✅ 交付(2026-09) | **交付**① **通道 seam**:EventHub 即统一订阅源(SSE/WS 消费同一 <-chan Frame,payload/重放语义一致);server 抽共享 consumeStream(历史 Replay 重投 + 实时转发,sink 错误=断连退出)。② **WS 实现**(web/ws.go):零外部依赖 RFC 6455 最小子集——握手(Accept 官方向量验证)+ Origin 同源校验(无 Origin=非浏览器放行)+ 文本帧写(<=125 内联/更大 16 位扩展,服务端无掩码);hijack 后 r.Context() 立即取消的坑已修(停止信号改永不关闭信道,断连由写错误驱动)。③ **前端 transport.ts**:WS 优先,失败/关闭自动降级 EventSource(同接口订阅;断线经 after 游标差集续传,sessionStorage 记 lastSeq;SSE 由 Last-Event-ID 自动)。④ **多浏览器会话鉴权**:authMiddleware 增 cookie( gah_token )承载(token 一次设置,后续请求/WS 自动携带;Bearer/query 旧路径兼容)。⑤ **CI 冒烟**(scripts/ws-smoke.go + ci.yml):起 `gah web`(ephemeral+mock)→ POST input 制造事件 → 手写最小 WS 客户端握手(官方向量校验)+ 断言 user/message 帧。偏差记录:Playwright 完整浏览器冒烟未入 CI(浏览器下载开销与收益权衡),以 Go 冒烟(协议级)替代 + VERIFY 人工清单;HMR/多开浏览器行为见 VERIFY。| ✅ WS/SSE 双通道同 payload 可切(前端自动降级);cookie 鉴权多会话可用;-race 绿 |
| **M16 测试提速专项** ✅ 交付(2026-09) | **交付(T1)**:tests/tui_pty_probe_test.go 新增 drainUntil(条件提前退出,命中即时返回不再空等满窗口);探针窗口收紧(boot 10s→4s、首输入 5s→3s、RealHome 循环 10s→8s、失败补窗 5s→2s);external 六个独立 e2e(各自 TempDir)t.Parallel() 化(TestExternalMCPBridge 因 t.Setenv 排除);tests 首跑 84s→57s(−32%)。**交付(T2 部分)**:无深度改动——embed/host-bridge 耗时占比已随 cache 与并行下降,实测全库首跑 **74s**(基线 ~100s),剩余瓶颈(host-bridge RPC 会话数、embed 平台校验)记录为后续优化项。**交付(T3)**:CI 加全库 `-count=1 -race` 计时回归护栏(≤150s,记录本地基线 74s;CI 机器冷 cache 裕量)。偏差记录:规划口标 ≤60s 未完全达成(本地 74s);剩余 ~14s 主要系 host-bridge 多会话 RPC 与 external 装配,继续压缩收益递减风险增,记 TODO 按需再优化。| ✅ 首跑 74s(基线 ~100s,−26%);重复运行全 cached ≤5s;pty 探针/外部链路覆盖不减;-race 绿 |
| **M8 工具类 todo** ✅ | **已交付 T1**:plugins/tool/tool-todo(todo 单工具 8 action:create/start/complete/pend/delete/update/list/get;4 状态机 pending→in_progress→completed + deleted 墓碑,状态变更仅专用 action;单 in_progress 约束(start 前置检查并提示当前项);blockedBy 依赖:成环/悬空/自依赖拒绝,start 前依赖须全 completed,被依赖任务 delete 拒绝;update 不改状态/id,blockedBy 经 add/removeBlockedBy 增删)+ extplugins/tool-basic 注册 + catalogue 登记 + config/seed 两份同步(seed 5→6)+ gen-extplugins.sh 重跑;sdk.ProjectKey 纯函数收口(memory/todo 共用,host-cwd-sessions 保留本地同步);全库 -race 36 包 ok。规划要点: | **纯工具外部化(与 tool-shell/files/web 全同构,宿主零感知)**:`plugins/tool/tool-todo/` 工具实现包 + `extplugins/tool-basic` 注册第 4 组工具(共享运行时单进程,合并零体积增量;工具名 `todo`,多 action 单 schema)。模型向任务清单:复杂多步任务(3+ 步/并行修改/评审校验)先建单、执行中推进、完成立即销单(状态变更专用 action,禁直接 update status——防“攒批”与跨态错乱)。**任务模型(4 状态机)**:`pending → in_progress → completed` + `deleted` 坟墓(状态变更仅 start/complete/pend/delete 四专用 action);字段:subject(祈使句短行)/description(长描述)/activeForm(进行时标签,供未来 TUI/Web spinner)/owner/metadata。**依赖**:blockedBy 数组(create 可带、update 可增删),环拒绝;并发:**同一时刻仅一个 in_progress**(start 前置检查,违反拒绝并提示先 start/pend 当前的)。**存储**:`$GAH_HOME/todos/<project-key>.jsonl`(ProjectKey 复用 host-cwd-sessions 同款派生——外部进程 import plugins/host/host-cwd-sessions 纯函数(P0 桥 extplugins 先例:tool-mcp import mcp-bridge export 同型)或复制小函数,实施时定;按项目隔离对齐会话);追加 jsonl + 全量快照末行(墓表同款:坏行容忍,sessionlog 同型);文件锁(回合串行下自然安全,工具并发多路调用时带锁)。**展示联动**:宿主/TUI/Web 展示 todo 面板归 M7.2 槽位注册表演示插件(宿主经 GAH_CB_ADDR 回调或查询工具读状态,零额外 seam);`-list-plugins` 可见;**切片**:T1 工具协议与存储(action 全集/状态机校验/锁/坏行容忍)+ tool-basic 注册第 4 组 + 单测(全生命周期/依赖环/并发锁/墓表优先);T2 展示联动 ✅ 交付(2026-09,M8-T2):web server 增 **/api/todo** 数据端点(经 ctx.tools 代理 todo 工具 list,零额外 seam;工具未装配 503);演示插件 **web-src/examples/todo-panel**(M7.2 槽位插件:覆盖 statusbar,保留默认语义 + 右下角浮动面板,5s 轮询/手动刷新,状态徽标/计数/activeForm;priority 120 与 statusbar-demo(100)构成同槽位抢占演示);tests e2e 真实装配含 tool-todo 验证 /api/todo 200;优先级语义实测(降序生效,同优先级后注册者胜) | T1:模型可全生命周期管理任务且状态机/依赖约束生效;坏行容忍;并行多调用不坏文件;T2:M7 面板实时反映任务推进 |
| **M9 tool-subagent 子代理委派** ✅ 全交付(one-shot + 后台控制 + send_message/fork) | **交付(T1)**:`plugins/tool/tool-subagent` 实现库(subagent 单 action delegate,窄接口 agentCaller 注入;进程内 Plugin 经 ctx.fanout,extplugins/tool-subagent 独立入口经 GAH_CB_ADDR 回调 fanout.agent,CbFanout 代理复用 NewTool)+ extplugins 独立二进制(崩溃隔离,gen-extplugins.sh NAMES 增 tool-subagent,embed 5 平台产物已重建)+ catalogue 登记 + bundle 样板同步(seed 6→7→8)+ tests/external_test 端到端(TestExternalSubagent:外部进程注册 → delegate → 宿主 fanout 子代理 mock 回结论)。 **交付(T2 后台控制)**:sdk.FanoutService 增 SpawnAgent/ListAgents/AgentStatus/KillAgent;host-fanout 引擎后台会话表(独立 ctx+cancel,运行/完成/失败/killed 状态机);host-bridge callback fanout.spawn/list/status/kill 分发 + cbFanout 代理转发;tool-subagent 工具扩 spawn/agents/agent_status/agent_kill(后台带句柄不阻塞,轮询取结论);单测(host-fanout TestSpawnAgent*、subagent stub 全 action、外部 e2e TestExternalSubagentBackground);gen 重建产物。 **交付(T2.5:M9.3 send_message/fork,2026-09)**:① **sdk.FanoutService 接口扩展**:增 `Fork(ctx, input)`(种入父上下文后台启动)与 `SendMessage(id, message)`(向运行中的子代理注入);`AgentHandle` 增 `Messages []AgentMessage{From,Content}`(注入与回复的父子对话记录)。② **host-fanout 引擎**:agentSession 增 `inbox chan`(注入队列)+ `seed []LLMMessage`(fork 父历史)+ `dialog`(对话记录);运行循环改造 runAgentLoop:每步开头 drain inbox(消息追加为 user 输入继续执行),注入后的首个文本回复记录 dialog agent 侧;`Fork` 从 ctx.sessions.DeriveMessages() 种入父会话已投影历史(未装配会话服务显式报错);`SendMessage` 仅 running 可注入(完成/失败/终止显式拒绝,队列满显式报错);snapshot 锁内附着 Messages。③ **host-bridge 回调协议**:fanoutCall 增 `send`/`fork` 分发(业务失败回传语义同 kill),cbFanout 代理增两方法 RPC 转发。④ **tool-subagent 工具面**:action 增 `send_message(agent_id, message→注入)`、`fork(task→带父上下文后台启动)`;Description/enum/args/fnOnly 同步。⑤ 测试:host-fanout(TestSendMessageInjectsAndRecords 门控时序验证注入→dialog/回复、TestSendMessageNotRunning、TestForkInheritsParentContext 父历史种入断言、TestForkNoSessions)+ tool-subagent(T estForkAction/TestSendMessageAction/TestNoFanoutBackend 扩)+ host-bridge callback_fanout_test(协议分支分发与代理 RPC 转发)+ 外部 e2e TestExternalSubagentFork(buildExternalEnv 增 host-session-log 在 host-fanout 前装配);gen 重建产物;全库 `-race` 39 包绿。**交互语义**:send_message 为运行中输入注入(子代理继续执行,回复经 agent_status.messages 可读),非挂起等消息式对话——后续如需暂停-恢复语义归 M7 展示联动线评估。

**规划(原文保留,供 T2/T3)**:引擎复用声明:**host-fanout(M6.8,ctx.fanout:agent/parallel/pipeline,独立子代理上下文、子会话隔离、仅结论回流父级)已存在,GAH_CB_ADDR 回调通道 fanout.agent 亦已具备(M6.9)**——零新引擎,零宿主改动;本次新增仅为**模型面向工具面**。两消费面共存:tool-workflow(starlark 编排,程序化批量)与 tool-subagent(自然语言委派,自主型)共享同一 fanout seam。**工具面**(单 schema 多 action)·`delegate`:task(委派目标/验收口径)+ 可选 tools 白名单/深度;·执行策略:**默认 one-shot 同步等子代理**(dsh 缺省);· 子代理上下文隔离。**切片**:T1 工具面+GAH_CB_ADDR 回调 fanout 桥+one-shot ✅;T2(M9.2)控制组(对齐 dsh tool-subagent-control:send_message/interrupt/list_agents)+ continuable 背景带手柄(复用 host-jobs 管道)+ fork;T3 展示联动(归 M7.2) | T1:模型可 autonomous 委派多步任务,仅结论入父上下文;starlark 与 subagent 两面共存不干扰;外部进程崩溃不拖垮父回合(T1 已达成,见 TestExternalSubagent) |
| **M10 tool-memory 跨会话操作记忆** ✅ | **已交付**:plugins/tool/tool-memory(memory 单工具 4 action + jsonl 追加/坏行容忍/人工可编辑,$GAH_HOME/memory/<project-key>.jsonl,项目 key 与 host-cwd-sessions 同款派生,只读工具零宿主感知)+ extplugins/tool-basic 注册 + catalogue 登记 + config/seed 两份同步(seed 4→5)+ gen-extplugins.sh 重跑;全库 -race 35 包 ok(TestTUIProbeScrollbarAndArrow 为 pre-existing pty 失败)。实现要点(**与 AGENTS.md 两层分工**:AGENTS.md(M5.5)已承担用户手写静态偏好层;本插件补**模型写操作层**(决策/踩坑/偏好会话内沉淀→跨会话可取)。参考裁剪至最小集(Hermes“自发在末尾留存+按需检索自身历史”+ mindspace“小规模人工可编辑”),**非目标显式拒绝**:分层体系(如 5 层)/闲时 LLM 自动提取/分块压缩(dsh-plugin-memory 类过度工程);**注入默认不做**(P1 纯工具零宿主感知,任务描述引导模型自主调用)。**工具**(单 schema 多 action,与 todo 同形态):remember(带 tags/后续检索)/list(近 N 条,预算限)/recall(需关键词或日期检索)/forget(标记废弃,物理保留可参照 sessionlog 坏行墓表)。**存储**:$GAH_HOME/memory/<project-key>.jsonl(ProjectKey 同 host-session-log 派生;追加 jsonl+坏行容忍+人工可编辑,mindspace 同三特性);跨会话:会话切换(M6.10)后 recall 可用(跨会话就是它的意义) | 模型可沉淀与召回跨期操作事实；人工可直接编辑 jsonl；与 AGENTS.md 两层不冲突(注入顺序 AGENTS.md 先,过预算条目不进)|
| **M11 tool-auto-plan 规划模式** ✅ T1+T2 已交付(参考 pi auto-plan extension;与 M8 tool-todo 分工:plan=规划期产物(先探索→结构化规划→等确认),todo=执行期任务追踪) | **交付(T1)**:`plugins/tool/tool-auto-plan`(auto_plan 6 action:create/get/list/step/confirm/complete + 生命周期 proposed→confirmed→completed + 步骤线性推进;存储 $GAH_HOME/plans/<key>.jsonl 追加/坏行容忍/锁)+ **规则注入**:Plugin Start 经 ctx.systemPrompt.AddSection(enable_rule data 开关默认开)。**装配形态偏差(实施记录)**:规则注入需宿主 ctx.systemPrompt,外部进程(tool-basic)无法注入 → 按 host-skills 先例改为**进程内装配插件**(catalogue Requires ctx.tools+ctx.systemPrompt,bundle 默认启用,不进 tool-basic——若进外部进程,AddSection 在子进程不可达且同 host-tools 重名注册冲突);seed 6→7。**交付(T2 联动)**:规则文本/Description 增补与 tool-todo 分工边界——auto_plan 管规划期产物与确认门,确认后执行期任务追踪交由 todo 承接(检查清单转 todo.create 按 todo 状态机推进,执行完回 auto_plan.step/complete 归档);新增 TestRuleTodoHandoff/TestLifecycleWithTodoHandoff 回归;plugins README tool 行同步**

**动机**:pi 同名 extension 语义 = 检测“计划/规划/方案/步骤/roadmap/plan”等规划意图 → 进入规划模式(**先只读探索,不动手修改 → 输出结构化规划(目标/关键约束与风险/实施步骤检查清单)→ 等待用户确认 → 再执行**)。gah 落为**工具类插件**(模型面承接,宿主零感知):**纯工具外部化(与 tool-files/web 全同构)**。**落点**:`plugins/tool/tool-auto-plan/` 工具实现包 + `extplugins/tool-basic` 注册第 5 组(共享运行时单进程;内置 Plugin 双轨注册同 tool-files)。**工具 `auto_plan`**(单 schema 多 action):create(request:探索后生成 目标/关键约束与风险/检查清单步骤 并落盘)/get(id)/list(当前项目全部)/step(id,index,status:待→进行中→完成,线性检查清单,**不上依赖图/blockedBy 防过度工程**)/confirm(id:用户“确认/执行/开始”后标记已确认,允许进入执行阶段)/complete(id:归档)。**规则注入**:插件 Start 经 `ctx.systemPrompt.AddSection`(Disposer 撤销)注入“规划模式”系统提示片段——用户请求**要求先规划后执行(意图判定而非裸关键词包含:消息本身已含明确执行指令(如“将规划写入文档”“仅/只 <动词>”的命令式)时不视为规划请求,按其指令执行——参考 pi auto-plan extension 误判教训,见本节)**或复杂任务(3+ 步)时,必须**先只读探索并 auto_plan.create 输出结构化规划,确认前不执行任何有副作用操作**(纯查询可做),确认后**按步骤逐项推进**(step 标记状态);`data.enable_rule` 开关(默认开;与 AGENTS.md 全局“操作确认”规则重复冗余时可关)。**存储**:`$GAH_HOME/plans/<project-key>.jsonl`(ProjectKey 复用 host-cwd-sessions 同款纯函数,外部进程 import(M8/M10 同款先例);追加 jsonl + 全量快照末行,坏行容忍,人工可编辑——session/memory/todo 同三特性);文件锁(回合串行下自然安全,工具并发多路调用时带锁)。**切片**:T1 工具协议与存储(create/get/list/step/confirm/complete 全集/步骤状态机/存储三特性/锁)+ 规则注入(AddSection+enable_rule 开关)+ catalogue 登记与 config/seed 两份 bundle 同步(seed 3→4,新增 base 条目必须 bump)+ tool-basic 第 5 组 + 单测(全生命周期/坏行容忍/并发锁);T2 打磨(与 todo 联动边界(规划确认后执行期由 todo 承接)文档/plugins README tool 行) | 模型面对规划意图先输出结构化规划并**等待用户确认(确认前零副作用工具调用)**,确认后逐步骤推进;规划落盘跨会话可取、人工可编辑;`-race` 全绿 |
| **M13 TUI 主题外部化** ✅ (2026-09) | **已交付**:① **调色板覆盖层**(tui/palette.go):active 当前生效表(默认克隆 DefaultPalette),新增公开 `ApplyTheme(overrides)`(部分覆盖,空值回落默认,未知 token 名/非法色值显式报错不静默,失败零写入原子性)+ `ResetTheme()`/`ThemeSnapshot()`/`colorVal()` 唯一取色入口;渲染层零改动(fg()/colorVal 派生不变;搜索高亮背景改经 colorVal 取当前值,主题切换即时生效)。② **启动加载链**(app.go NewApp 变参 `palette ...map[string]string`):默认表 < `data.palette`(ui-tui-app 装配层样板注入,bundle/patch 可用)< `$GAH_HOME/config/theme.yaml`(用户全局,覆盖前者);坏主题文件启动时记录显式错误行(不静默),渲染回落默认表。③ **/theme 命令**(注册进 ctx.commands,一级枚举= themes/ 目录主题列表 + `default` 哨兵):`/theme <名>` 运行期切换 `$GAH_HOME/config/themes/<名>.yaml`(防路径穿越校验),`/theme default` 恢复启动活动覆盖链;TUI 提示/help 自动纳入。④ 文件层(tui/theme.go):theme.yaml/themes/<名>.yaml 加载(对齐 exakey.go search.yaml 先例,GAH_HOME 未设回退 ~/.gah;缺主文件=无覆盖,缺指定主题/坏 yaml=显式报错)。**测试**:palette_test 扩展(覆盖/部分覆盖/空值回落/未知 token/非法色值/原子性/Reset)+ theme_test(主/命名/列表/Options)+ theme_cmd_test(注册/切换与恢复/缺失与非法名/启动坏文件显式错误行),全库 `-race` 绿 | 改 theme.yaml / data.palette 或 `/theme` 切换即整体重配色(零重编译,免改源码);默认表基线守卫不破;`-race` 全绿 |
| **M14 外部命令桥(免编译加命令)** ✅ (2026-09) | **已交付**(协议扩展一次,插件永久零编译;旧插件零改动兼容=M6.8 工具协议回退同款先例):① **协议**(proto.go):ToolServer 增 Commands(枚举命令定义 JSON,CommandDTO{Name/Usage/Desc/Args 级联 每级 Enum=枚举或 FreeArgs=自由参数名序列,对齐 sdk.ArgLevel 语义})+ CommandOptions(枚举级选项运行期求值,载荷 Name/Level/Picked)+ RunCommand(Name/Args → ExecReply 输出文本+结构化错误);旧外部插件无新方法 → 宿主按"无命令"处理(can't find method 跳过),纯工具行为不变。② **外部入口**(serve.go):`ServeTools(tools, commands...)` 变参(不传命令 = 旧行为);toolServer 实现三方法,枚举选项序列化 sdk.Option 数组。③ **宿主转注册**(bridge.go):Plugin.Start 可选注入 ctx.commands(未装配 = 命令不注册,工具不受影响,对齐 host-jobs 可选注入);loadOne 协议探测后枚举 Commands;registerAll 经 `cmds.Register(cmd.spec())` 注册(同名冲突显式记警告跳过该命令、插件继续加载;Disposer 随插件卸载/热重载/崩溃替换自动撤销);`commandRPCClient` 代理:Run 经 RPC 转发外部进程(输出文本+错误,TUI meta 行),Args 级联构造成 sdk.ArgLevel(Enum→Options 经 CommandOptions RPC,FreeArgs→静态),连接错误触发 onDead 自动拉起;执行/枚举选项 RPC 带 **超时保护**(rpcCall 复用,默认 3s,命令可声明 `TimeoutMs` 覆写(sdk.CommandSpec 新字段)——死进程/慢命令不阻塞 TUI 线程);扫描识别 `tool-*`/`cmd-*` 前缀,**纯命令插件(无工具)可单独加载**(`ServeTools(nil, commands)`,loadOne 工具或命令任一满足即载,皆空才拒)。④ **参照实现**:extplugins/tool-echo 增 Commands/RunCommand(命令与工具共存示例,不进发行矩阵)。**TUI/提示零改动**:`/` 提示、/help、自由级断点向导全部自动来自 ctx.commands。**测试**:command_bridge_test.go(buildExternalPlugin 真进程装配 host-tools+host-commands+host-bridge:注册/执行/自由级 Args/工具共存/无 ctx.commands 时命令跳过/同名冲突跳过(先到先得)/卸载撤销/注册表拒重)+ TestServeCommandsDTOAndEnum(进程内协议核心:DTO 往返/枚举求值/越界与未知命令空/业务错误回传并保留输出/Args 级联构造)+ TestExternalCmdOnlyPluginWithTimeout(纯命令插件+声明超时快速返回),全库 `-race` 绿 | 新命令插件不经重编译 gah、丢 plugins 目录即挂载(运行中零重启,fsnotify 热重载);`/` 提示与 /help 自动可见;同名冲突显式拒绝;卸载随 Disposer 撤销;旧插件零改动兼容;`-race` 全绿 |
| **M15 TUI π 式默认样式** ✅ (2026-09;**F15.1/F15.2 修正**:指标信息最终位于状态栏之下、屏幕最底独立指标行——输入行不携指标、输入多长不挤压;**F15.3**:思考动画改定宽 5 shade 光条滚动(░▒▓█ 家族,左右滚动醒目;初版 ▁▂▃▄▅ 细分块在 Menlo 等字体缺失显示空白,已替换),状态栏去掉 gah 标识;**F15.4**:指标行前导空格与状态栏左缘对齐;**F15.5**:状态栏/指标行与输入区之间增 1 空隙行(底部区几何 mainH=height-6)) | **参考 pi agent TUI 视觉收敛为默认样式**(用户指定;渲染层纯函数改造,零新增 token):① **状态栏精简**(tui/chrome.go renderStatusLine):去掉 profile/模型/思维/上下文,保留 `gah | 状态(思考/执行工具+Esc 取消;待发 N 计数) | 工作区 | 沙箱 | 会话`——信息密度降半,一眼读状态。② **输入行右侧挂载运行指标**(新 renderInputRight):`模型(源)[思维] · 上下文 x/y (n%) 缓存 n%` 右对齐挂输入行首行(输入未用满宽度时显示;超宽自动省略防挤压;无请求显示 上下文 -;窗口未知仅显示量不假精确)。③ **底部区整宽分隔线**(tui/render.go):主区(会话流)与底部区边界淡色 `─` 线(styleMeta),几何统一收纳 `mainH = height-4`(输入1+状态栏1+分隔线1+预留1)。④ **调色板默认表降饱和**(tui/palette.go):assistant 120→253(近白,克制层次由 markdown 局部高亮承担)、tool 220→246(灰)、status 250→252、widget 249→243;默认表即新基线,`/theme`/theme.yaml 覆盖通道不变。**测试**:chrome_test 重构(状态栏断言更新 + 新增 TestInputLineRight 四态:模型/来源/上下文/缓存/超宽省略)、palette/session/toolrow/commands/multiline/state 基线断言同步;全库 `-race` 39 包绿(不含预存 pty 探针)。互动细节见 docs/TUI_OPTIMIZE.md 执行记录 0 | 默认开箱即 π 式克制样式(状态栏轻、输入行右侧指标、主区/底部区明确边界);主题外部化通道(M13)仍可整体换肤;-race 绿 |
| **P5.7 便携根不存在自动新建初始化** ✅ (2026-09-08) | homeDir 同级分支由"存在即用"扩为"**不存在则 MkdirAll 自动新建**(cmd/gah portableRoot 纯函数:已存在复用 / NotExist 创建 / 创建失败=目录只读 → 回落空由 homeDir 继续 ~/.gah);初始化内容(config 样板 bundle/patch/profile + 随包外部插件)由既有首启 EnsureSeed/EnsurePlugins 释放——部署目录内 gah+gah-data 即自包含,无需手工 mkdir。测试:main_test.go portableRoot 三态(缺失新建/已存在复用/只读回落);真机:空目录放 gah 启动 → gah-data 自动生成含 config/plugins/sessions,booted 正常。README 便携节与 AGENTS.md 便携纪律同步(不存在自动建)。全库 -race 绿 | 空目录首次运行即得完整便携数据根(自动创建+初始化);只读目录安全回落;部署仅需 gah 单文件 |
| **P5.6 ~/.gah 迁移完成与删除(数据单根落定)** ✅ (2026-09-08) | M16.9 遗留的"~/.gah 保留待确认删除"闭环:① **核对**:逐目录 diff ~/.gah vs gah-data——plugins/config 主体 gah-data 全覆盖且更新(gah-data 62M 含全部 config/plugins/36 真实会话);~/.gah 仅余 config-backups 1 个备份(已迁)与 22 个 `tests-*` 探针冒烟残留(历史版本探针真实 home 产物,判测试垃圾不迁不污染 gah-data);② **删除** `~/.gah`(51M,含 22 个垃圾会话;rm -rf,已确认无用户数据缺失);③ **配套**:tests 探针 RealHome/ScrollSettle 数据源 `~/.gah` → `../gah-data`(便携根);④ **验证**:无 GAH_HOME 启动自动发现二进制同级 gah-data(booted+状态栏正常);RealHome(gah-data config 真 LLM)/ScrollSettle 自 gah-data 跑通;全库 `-race -p 2` 绿。此后数据仅走 gah-data 单根 | 便携单根最终落定:~/.gah 删除,所有数据/密钥/插件/会话只从 gah-data(GAH_HOME/同级自动发现)走;升级替换 gah 单文件即完整迁移;全库 -race 绿 |
| **P5.5 pty 探针自包含化修复(预存失败清零)** ✅ (2026-09-08) | 背景:TestTUIProbeScrollbarAndArrow 长期为"预存失败"(历史交付行多次标注)——根因三:① 依赖真实 ~/.gah 拷贝(便携迁移后数据陈旧,主会话/项序不可控,选择器第一项=main 且内容不超窗 → 无滚动条 ░);② 跑仓库根 gah 旧二进制(与源码漂移,测的是旧代码);③ 流程假设"列表第一项=超窗会话"。**修复**(tests/tui_pty_probe_test.go):① **自包含构造**:测试自写 400 行 user 事件超窗会话(key 经 sdk.ProjectKey(cwd) 推导,与运行时一致),不再依赖真实数据/skip;② **直接命令切换** `/session switch probebig`(绕开选择器项序脆弱假设);③ **buildGahCurrent helper**:全部 TUI 探针(Probe/NonTTY/RealHome/ScrollSettle/ScrollbarAndArrow)改跑**当前源码构建**的二进制(消除旧二进制漂移);④ 滚动条断言保留(轨道 ░ 命中即提前)。验证:tests 全量 -count=1 绿(51s,含 RealHome 真实 LLM);`-race` 单跑绿;全库 `-race -p 2`(降并行防 pty 时序超窗)全绿——**预存失败清零**。此前 DESIGN 各交付行"预存 pty 探针失败/旧二进制"表述均属该问题历史,以本行修复为准 | pty 探针自包含+当前源码构建,预存失败清零;全库 -race 绿 |
| **P5.4 B2/B3 HTML 导出 + 分支树可视化** ✅ (2026-09-08) | ① **B2 /export html**(plugins/host/host-internal-commands/render_html.go):path 以 .html 结尾 → 会话事件渲染为自包含 HTML(内嵌 gruvbox 系 CSS,零外部依赖;user/think 灰/assistant 增量拼合/工具块含 ok·err 类/error/hr;html.EscapeString 全转义;AssistantMessage 跳防与 chunk 重复;cmdExport 按后缀分支,jsonl 既有语义不变)。真机:/export /tmp/x.html 生成完整文档。② **B3 /tree 分支树可视化**(sdk.ForkNode + ForkableSessions.ForkTree;host-cwd-sessions fork_tree.go:fork-tree.json(workspaces 同型)记录派生 id→{Parent,ParentSeq},ForkAt/CloneCurrent 写溯源(seq0=克隆,parent 在 Open 切换前取);tui treeRenderNodes 纯函数渲染:主会话恒根、fork-自主挂主下、孤儿独立根、无记录平铺回退、深度≤12+环保护、缩进 `└─`+★ 当前标记)。**测试**:host fork_tree_test(记录/克隆 seq0/主根)+ tui tree_test(单次渲染/树线/平铺/环)+ web stubCS 补 ForkTree;全库 -race 绿(不含预存旧二进制 pty 探针)。互动细节见 docs/TUI_OPTIMIZE.md 执行记录 11 | 会话可导出自包含 HTML(分享/归档);/tree 树形展示 fork 派生关系(同 pi /tree 形态);-race 绿 |
| **P5.3 B1 思维块显示(OpenAI 兼容链 + TUI)** ✅ (2026-09-08) | ① **事件字段**(sdk/llm.go):LLMStreamEvent 增 `Thinking`(思维增量,与 Delta 互斥发送;聚合消息不携带)。② **adapter 解析**(llm-openai-compat compat.go):wireChunk delta 增 `reasoning_content`(deepseek 推理模型),逐 chunk 映射到 ev.Thinking。③ **TUI 渲染**(tui/state.go/session.go/render.go/model.go):thinking 增量独立行累积(appendThinking,kind=thinking,与正文不混行);新 token `thinking`(灰斜体 242,styleThink);**折叠**:Ctrl+T 切换 ThinkingFull,flattenViewLines 展平层折叠态截首段 40 rune+「…(Ctrl+T 展开)」(宽字符 2 列,80+13<100 列单行;搜索/选区命中完整),展开态全文拼接;④ **投影纪律**:projectLocked 只用 UserMessage/AssistantMessage 最终消息,**thinking 天然不进模型历史**(零改动);重放重建 thinking 行(随事件流)。**测试**:compat_test TestCompleteStreamsThinking(reasoning 拼合/content 正常/聚合仅正文)+ tui thinking_test(累积独立行/折叠截断与展开拼接/灰斜体样式);全库 -race 绿(不含预存旧二进制 pty 探针)。anthropic thinking blocks 后置(单端点手输,对齐 M12 先例)。互动细节见 docs/TUI_OPTIMIZE.md 执行记录 10 | 推理过程可见(灰斜体弱化+Ctrl+T 折叠展开);思维不污染模型历史;OpenAI 兼容链(deepseek reasoning_content)单测+真机构造验证;-race 绿 |
| **P5.2 A 组剩余体验优化(工具耗时/会话预览)** ✅ (2026-09-08) | ① **A1 工具耗时显示**(tui/state.go):State 增 toolStart,EventToolCall 计时起点 → EventToolResult 结算,耗时段 `(X.Xs)` 挂结果行尾(`fmtDur` 复用;<100ms 不显示=重放毫秒级自然抑制;toolStart 零值保护防无前置调用挂虚构时长)。② **A2 会话列表内容预览**(tui/app.go sessionDesc):/session switch 选择器每项带首条用户消息预览(截 20 rune,对齐 web Sidebar Preview,SessionInfo.Preview 既有字段复用零协议改)。测试:duration_test.go(耗时:真实1.2s挂/重放<100ms 抑制/失败结果也挂/零值保护 + 预览:截断/无预览/主会话);真机 pty:选择器项含 ─ 预览。全库 -race 绿(不含预存旧二进制 pty 探针)。互动细节见 docs/TUI_OPTIMIZE.md 执行记录 8 | 每次工具调用一眼看耗时;会话选择器按内容可识别;零协议改纯展示层;-race 绿 |
| **P5.1 剩余体验优化(主题样板/OSC8 链接/yank)** ✅ (2026-09-08) | ① **gruvbox-dark 主题样板**(config/themes/gruvbox-dark.yaml):44 token 全表 hex 覆盖(对照用户本地 pi 的 @firstpick/pi-themes-bundle 同名主题),复制到 $GAH_HOME/config/themes 后 `/theme gruvbox-dark` 一键切换(真机 pty 验证列表出现);theme.go 机制零改动。② **md 链接 OSC8 超链接**(tui/markdown.go mdLink):经 lipgloss Hyperlink 原生输出(`ESC]8;;url` 包裹,终端 cmd/ctrl+点击打开;URL 不进正文,字符无损);markdown_test stripANSI 扩展剥 OSC(BEL/ST 终止)。③ **kill-ring yank**(tui/state.go):Ctrl+K/U 删除记入 killBuf 单槽,Alt+P 粘贴到光标(入 undo 可撤销;model.go Alt 分支);input_edit_test 增 TestYankKillBuf。测试:tui 全绿 + 全库 -race 绿(不含预存旧二进制 pty 探针);真机:/theme 列表含 gruvbox-dark。互动细节见 docs/TUI_OPTIMIZE.md 执行记录 7 | 主题样板一键换 gruvbox(观感对齐本地 pi);md 链接可点击(终端级,零交互代码);kill 文本可粘贴(编辑器语义补全);-race 绿 |
| **P5 TUI 视觉升级(群组背景/框感/md 补全/输入聚焦/小功能)** ✅ (2026-09-08) | 对照本地 pi 实际主题(gruvbox-dark,53 token)查漏补缺的 TUI 视觉整轮(规划见 AGENTS 会话;互动细节见 docs/TUI_OPTIMIZE.md 执行记录 6):① **V1 主题 token 扩 16**(tui/palette.go):新增 user-bg/tool-bg/tool-ok-bg/tool-err-bg(黑底三层灰阶:调用 235 < 用户 236 < 结果 237)、md-link/md-quote、think-off/low/med/high(对齐 pi thinking 边框色)、syntax-var/num/type/op/punct(gruvbox 原 hex);既有 token 值零改动(M15 基线 + 旧主题文件覆盖语义不变)。② **V2 用户消息背景块**(tui/session.go):user 行整块深灰底(236),叠加顺序 = 搜索命中背景 > user 背景 > 选区反色(既有裁决路径补一层)。③ **V3 工具框感**:调用行左缘竖线 `▍` 前缀(flattenLine 复用 user ❯ 前缀机制,limit-2)+ tool-bg 背景;成功/失败结果行首物理行 tool-ok-bg/tool-err-bg(折叠摘要即框感,展开全文行走 diff/md 染色不加背景防内嵌 reset 冲突);折叠行(stRenderText 早退路径)背景保持。④ **V4 md 补全**(tui/markdown.go):链接 `[text](url)` 文本 link 蓝灰+下划线(`](url)` 原样保留,字符无损)、引用行 `>` 标记边框灰加粗+正文 quote 米白、语法高亮 3 色扩 8 分类(数字/类型大写/变量/运算符/标点)。⑤ **V5 输入聚焦**(tui/chrome.go):输入行左缘 `│` 竖线按 thinking 等级着色(off 灰/low 蓝灰/med 绿/high 紫,零额外行高不改几何)。⑥ **V6 小功能**:Ctrl+O 最近结果行折叠切换(对齐 pi app.tools.expand)、回合耗时(status running/idle 计时,状态栏空闲态 `上一回合 X.Xs`,避免事件时序依赖)、Ctrl+↑/↓ 消息跳转(跳到最早 user 行/回底)。**测试**:palette/multiline/markdown 基线同步 + 新增 rowbg_test(背景标注/渲染/竖线前缀/折叠保持)、markdown 链接/引用/语法增强、turn_test(计时/Ctrl+O/跳转);真机 pty(ANSI 断言:user 背景 48;5;236/调用 235/结果 237/输入竖线命中);全库 -race 绿(不含预存 pty 探针,旧二进制) | 对照 gruvbox-dark 的视觉整轮交付:消息/工具有背景块、工具有框感、md 链接与引用可读、输入区带思考色聚焦、折叠/耗时/跳转三小功能;主题外部化通道(M13)对新 token 天然生效;-race 绿 |
| **M12 多 provider 并存** ✅ (2026-09) | **providerfile v2**:provider.yaml 改 `{active, providers[]}`(name/base_url/api_key/model,0600 原子写);旧单对象自动迁移视图(name=域短名),下次写盘落 v2;API:Add(upsert,首自动活跃)/SetFields/SetActive/Remove/UpdateModel(活跃)/Unset(活跃,删空即移除该 provider)/Clear——Load() 仍返回活跃 provider(adapter resolveConfig 不经改)。**sdk**:MultiProviderService 可选接口(类型断言,LLMService 接口不变,mock 零破坏)+ OpenAIFetchModels(直拉 /models,聚合非活跃端点用)。**host-llm**:Providers/AddProvider(同名 upsert 激活)/SetActiveProvider(Configure+SetModel 即切)/ListAllModels(TTL 10min;活跃走适配器缓存、非活跃直拉,单条失败记 Err 不整体失败)。**TUI**:/provider show(全列★)|add(名自动=域短名,首个自动活跃)|use(二级枚举切换)|set(编辑活跃=旧单 provider 流程不变)|unset|clear;/model 枚举=全部 provider 模型聚合(Value=`provider|model`,选中自动切所属 provider+SetModel;手动 /model 无分隔符走当前活跃向后兼容);状态栏来源=活跃域短名。**范围边界**:anthropic 适配器不参与 openai 聚合(单端点,claude 手输);/provider 暂不提供单条删除(upsert 覆盖;全清走 unset 删空/clear),记 TODO | /model 一次聚合列出所有 provider 端点模型(带来源),选中即切端点与模型;多 provider 并存可 add/use;旧 provider.yaml 与 /provider set 流程零干预迁移;新增 providerfile/multi_provider/multiprovider 三层单测;-race 全绿(pre-existing pty 探针除外) |
| **M16.9 便携纪律入规范** ✅ (2026-09) | 将完全便携写成**项目硬规范**(AGENTS.md 新增「便携纪律」节 + docs/PLUGIN_DEV.md §5 检查清单项):一切 gah 运行数据/配置/密钥/插件落 GAH_HOME 单根;新代码禁止硬编码 ~/.gah/cwd/系统根/XDG/散目录;路径链唯一(GAH_HOME > gah-data(二进制同级) > ~/.gah > TempDir);密钥入 config/、启动 env 入 gah-data/env.sh 由 start.sh source(不以 shell profile 为唯一承载);例外仅项目技能 .gah/skills 与工作区写工具。后续新增配置/插件一律便携 | 新代码(配置/数据/插件路径)默认便携,review 可依据规则拦截违规落盘;`-race` 全绿 |
| **M16.9 便携数据根(gah-data 同级自动发现)** ✅ (2026-09) | 需求:数据不落系统根、与 gah 同级的统一目录、升级只替换 gah 单文件。`cmd/gah homeDir()` 改优先级:**GAH_HOME env** > **gah 二进制同级 `gah-data/`(存在即用;os.Executable+EvalSymlinks 归一双链接)** > `~/.gah` > TempDir(兜底,永不落 /)。任一层不落系统根。所有运行数据(provider/密钥、bundle/profile、sessions/memory/plans/todos、插件、偏好 gah-state)本就统一单根(GAH_HOME 启动贯通插件与外部进程)——便携仅改“根的位置”。密钥随 config/provider.yaml 在 gah-data 内,随目录整体备份/迁移。**迁移**:现有 ~/.gah(62M) `cp -a` → 仓库 gah-data(gitignore 增 /gah-data),保留原目录待确认删除。**验证**:boot profile 自 gah-data/config 读取、新会话写入 gah-data/sessions、~/.gah 无新写。README「配置与运行时目录」增便携节。不影响 tests(不经 main) | 同一 gah-data 即全套数据+密钥,升级仅替换 gah 文件;数据单根不散落;`-race` 全绿 |
| **M16.8 TUI 持久化偏好(宿主共享)** ✅ (2026-09) | TUI 此前仅 /model/providerfile 与 /theme 持久化;/thinking /sandbox 仅内存、history 仅会话级 sidecar(新会话不继承)。新增 **internal/prefs**(插件不可 import,运行时 web/tui 可用):`$GAH_HOME/config/gah-state.json` 存 thinking/sandbox/history,**兼容回退旧 web-state.json**(迁移期);便捷 SetThinking/SetSandbox/SetHistory。**接入**:TUI cmdThinking/cmdSandbox 写点 + `/settings history` 存全局偏好(会话 sidecar 保留);TUI `applyPrefs()`(NewApp 启动恢复,注入 ctx.sandbox/ctx.sessions,recover 兜底防测试 stub nil);web/settings.go 本地 webPrefs 实现移除改委 internal/prefs(旧 web 测试同步 prefs.Prefs)。恢复语义:thinking/sandbox 双端一致,history 跨新会话记忆;model 仍走 providerfile 链。**测试**:internal/prefs roundtrip/便捷更新/legacy 回退/无 GAH_HOME no-op;web 原偏好测试随迁;全库 -race 42 包绿 | TUI 与 Web 共享偏好文件:思考/沙箱/历史注入退出即记、重启恢复(跨会话);legacy 平滑迁移;`-race` 全绿 |
| **M16.8 Web 设置记忆与空态居中修正** ✅ (2026-09) | ①空态输入框贴左:`.content.empty .input-slot` 补 `margin: 0 auto` 水平居中。②**运行偏好持久化**(思考/沙箱/历史注入退出即记、重启恢复):新增 web/web-state.json(`$GAH_HOME/config`,GAH_HOME 未设=跳过持久化纯内存,测试安全);handleControl thinking/sandbox 成功设置后 savePrefs、model 增设 `providerfile.UpdateModel`(与 TUI /model 同持久化链,重启自动恢复)、handleSettingsHistory 存 History;`Server.ApplyPrefs()`(Inject 后恢复 thinking/sandbox/history,非法值跳过不阻塞)由 ui-web-app 装配调用。**测试**:TestPrefsPersistAndApply(roundtrip+Apply 到 stubLLM/stubSB/compactLog、非法 thinking 跳过)、TestControlPersistsPrefs(control 后落盘);真机重启冒烟 thinking=high/sandbox=read-only/history=10 恢复、model 保持;空态输入框 CDP 居中(x453/w720 content 中心 812) | 设置退出记忆:重启后思考/沙箱/历史/模型保持上次值;空态输入框水平居中;`-race` 全绿 |
| **M16.8 Web 左右分割布局修正(会话占满右列)** ✅ (2026-09) | 重构 App 布局:新增**右列 `.content`**(sidebar 独立左列 + 右列内放 stream-slot 与 input-slot)——输入框不再横跨整页底(原 input 为 .app 整行全宽,含侧栏下区域);进入会话后:会话流**占满右列全部宽度**(stream 100%,对称 36px 留白不缩窄),输入外壳**限宽 900 右列底部居中**(shell margin auto),左右分割保持。空态:右列内 welcome+放大输入框(720)垂直居中(content.empty justify-center,stream display none),由 fixed 定位改静态布局。InputBar shell 自身 max-width 900 + margin auto。**验证**:CDP 对话态(streamW 1095=contentW、shellW 900 且 centerOk、bottom 12)与空态(shell 720 列内居中、距底 275)双态断言;全库 -race 41 包绿 | 进入会话后界面左右分割、消息占满右侧、输入框右列底部限宽居中;空态右列居中引导;`-race` 全绿 |
| **M16.8 Web 输入一体外壳与品牌修正** ✅ (2026-09) | 对齐 DeepSeek Harness 输入排版:InputBar 重构为**一体圆角外壳**(shell)——输入 textarea(无内边框,自动生长 ≤180px,裸 Enter 提交、Shift+Enter 换行)+ 底部工具条**包裹全部按键**(框内左下:思考/沙箱/会话幽灵钮;右下:蓝色圆发送钮),命令提示/会话抽屉浮层相对外壳上缘;`centered` 空态放大(760px 居中 shadow-dialog,发送钮 40px)与底部常规态共用外壳;tooltip 右缘右对齐覆写保留。品牌:空态大标题改 **Go Agent Harness**(弃缩写),状态栏左上 `gah` 字标移除(状态栏由「就绪 模型 …」起)。工具结果长文本溢出修复(word-break+overflow-wrap anywhere)。**验证**:CDP 结构断言(标题全名、状态栏无 gah 起首、ctl×3+send 均在 .bar/shell 内、shellW 760、textarea 键入正常)+ Chrome 截图确认一体框排版接近参考;全库 -race 41 包绿 | 输入框一体包裹全部控件、空态居中放大,标题用全名、状态栏无字标;`-race` 全绿 |
| **M16.8 Web 克制动效层** ✅ (2026-09) | 遵循 taste skill 动效纪律(MOTION 低档、被动机、仅 transform/opacity、尊重 prefers-reduced-motion):①**新消息入场** fade+上移 6px(StreamView .msg/meta 一次性 animation,流式内文本更新不重触发)②**「↓ 新消息」回底胶囊**(上滚读历史时新内容到达浮现,点击回底;复用 App stick 检测,无 scroll listener)③**动效基座**(style.css:--ease-out/--dur-* token、:focus-visible accent 焦点环、全局 @media prefers-reduced-motion 降级)。**验证**:CDP(主会话 6 万 px 长历史:上滚后胶囊出现 dist 62017、注入 /jobs 不被拉走、点胶囊 dist→0 消失;msgAnim=msg-in;reduced-motion guard 存在);无窗口滚动监听/无无限循环动效;`-race` 41 包绿 | 新内容到达有轻量入场与回底引导,系统减弱动效自动降级;体验提升零依赖 |
| **M16.8 Web 可视化设置面板(命令→配置)** ✅ (2026-09) | 新增 `SettingsPanel.vue`(右侧滑出抽屉,状态栏「设置」入口,taste 纪律:五分组/分段控件/激活卡片高亮/单蓝强调):①模型下拉(/api/models?all=1 聚合,选中即切 provider+model)②思考/沙箱分段(/api/control)③历史注入下拉(/api/settings/history)+压缩按钮(/api/compact,确认条)④Provider 管理(列表/启用/删除(确认)+新增表单,字段大写契约修正——ProviderProfile/ProviderModelList 无 json tag 序列化大写)⑤插件开关(load/unload,卸载确认)+指令重载(/api/reload)。**后端新增 web/settings.go 两端点**:POST /api/compact(prompt?, CompactService 断言→501,返回 summary+folded)、POST /api/settings/history {n}(SetHistory,-1 禁/0 全部/N 最近)。**测试**:settings_test.go(compactLog wrapper:compact 200+501、history 各档 200/非法 400);全库 -race 41 包绿;UI CDP 冒烟(打开抽屉五区块渲染零运行错误、Provider 激活高亮、无横向滚动)+ Chrome 截图验证 | 原 TUI 斜杠命令能力(模型/推理/历史压缩/Provider/插件/指令)在 web 转为可视化配置,零命令记忆成本;`-race` 全绿 |
| **M16.7 修正:工作区切换真实切换目录(外部工具 cwd + 沙箱 root)** ✅ (2026-09) | 根因:切工作区仅宿主 os.Chdir,但 shell/file 工具在 **tool-basic 外部进程**执行(sh -c 无 Dir,用进程启动时 cwd)且**沙箱 root 启动时固定**(policy-sandbox workspaceRoot)——“收敛版”缺陷(TUI 注释明言“工具进程重启后按新 cwd”)。**修复(事件联动)**:host-cwd-sessions Service 增可选 `emitWS`(Plugin.Start 绑定 `c.Emit(cwd/workspace-switched, dir)`),SwitchDir/SwitchProject **真实 key 变化**时广播(同项目 touch 不发);host-bridge 订阅该事件 → `reloadAll()`(逐个 reload:注销+kill→loadOne+注册,外部进程以宿主新 cwd 重启);policy-sandbox 订阅 → `SetRoot(dir)`(ValidatePath 已带锁,读/写校验即时按新 root)。web(经 SwitchDir)/TUI(经 SwitchProject+Chdir)双入口均受益。**验证**:TestSwitchDir 增 emit 断言(真实切=1 次广播,同项目=0 次);真机冒烟切 tests→shell pwd=/tests(此前=/go-agent-harness),file 工具同新进程,切回同样生效;全库 -race 41 包绿 | 切换工作区后 shell/file 工具**真正在新目录执行**(外部进程重启继承新 cwd),沙箱 root 同步;`-race` 全绿 |
| **M16.7 UI 收敛:确认居中/连接显眼/消息分色** ✅ (2026-09) | ConfirmBar 改**页面居中弹层**(遮罩+中央对话框+pop 动画,danger 红钮);连接状态自右下角弱位**移入状态栏右侧**(绿点已连接/橙脉冲重连中+文字,App conn 浮标删除,StatusBar 增 conn prop);StreamView meta 行**按类型分色块**(command=代码面板+「命令」tag、error=红块+「错误」tag、summary/status=灰斜体),帧内 user/tool 保持既有底/胶囊;AGENTS.md 增「UI 规范」节:web 端改动默认遵循 taste skill(design-taste-frontend) + DeepSeek Harness 设计语言(token 单源 style.css)+ 二次确认/分色/信号显眼纪律。CDP 验证:确认条几何精确居中(447/302@1280×713)+danger 红;状态栏已连接显示 | 二次确认居中醒目;连接状态一眼可见;会话流按类型分色块;web 改动默认走 taste skill;`-race` 41 包绿 |
| **M16.7 会话删除语义修正(删除即干净新起点)** ✅ (2026-09) | 澄清后按 A 方案:删除会话仅删该会话 jsonl+显示名索引(项目级 memory/todo/plans 与会话无绑定,不误删)。修复残留观感:删除**当前打开会话**原实现切回主会话并回放主会话旧历史(“没删干净”);改为自动 `New()` 新建空会话承接;前端删除当前会话确认文案提示“删除后开启新会话”,侧栏删除/忘记后广播 `gah:sessions-changed`(InputBar 会话抽屉监听并关闭,防陈旧缓存)。冒烟:删除非当前(文件/列表/names 全清)、删除当前(文件删+新会话承接非被删非主);全库 -race 绿 | 删除会话后界面干净(当前会话自动开新空会话,无旧内容回放),抽屉缓存同步失效;`-race` 全绿 |
| **M16.7 补充:全操作二次确认(防误操作)** ✅ (2026-09) | 新增全局确认条 `ConfirmBar.vue`(App 挂载 fixed 底部居中,shadow-dialog,slide-up 过渡;danger 删除类红色确认按钮)+ `provide('askConfirm')` 注入通道(sdk 侧 AskConfirm {title, danger?, run});侧栏与输入抽屉**全部增删改操作**统一经 guard → 确认条:会话切换/新建/改名(内联编辑 Enter/✓ 保存前确认,点击别处 blur = 放弃不触发)、删除会话/工作区(删除类标红);工作区切换/删除;InputBar 抽屉切换/新建同样确认;无注入兜底直执行(防御)。旧内联二次确认移除(统一机制)。**验证**:CDP 冒烟(点击条目→确认条文本断言→确认执行后 GONE+无错误;工作区删除→RED-DANGER 红色按钮;取消→GONE);全库 -race 41 包绿 | 数据管理操作全部二次确认,误点不触发副作用;删除类标红强提示;`-race` 全绿 |
| **M16.7 修正:web 工作区切换 dir 语义** ✅ (2026-09) | 根因:web 端按**历史 key** 切换且 `SwitchProject(key)` 内 `recordProject(key, currentDir())` 永远以宿主当前 cwd 当 dir——web 不 os.Chdir 直接调用 → ①workspaces 记录 dir 全被污染成宿主 cwd(显示全同名) ②工具实际仍在旧 cwd(切换“失败”感知)。**修复**:sdk.CwdSessions 增 `SwitchDir(dir)`(与 TUI /workspace 对齐,TUI 为 Chdir+SwitchProject(ProjectKeyFromCwd)):os.Chdir(dir) → key=sdk.ProjectKey(dir) → recordProject(key, dir 真实目录) → 重绑+新会话;目录不可用显式失败;handleControl workspace 改调 SwitchDir;前端侧栏切换传 `w.dir`(web/server_test 适配 dir 语义 TestControlWorkspace,stubCS 补 SwitchDir;tui fake 同步;新增 TestSwitchDir(Chdir/派生/真实 dir/坏目录显式失败,含 macOS /var 符号链接归一));存量脏记录手工修正;gah 重编译 | 工作区切换真正 Chdir 生效(cwd 同步,tools 在目标目录执行),记录 dir 为真实目录,名称各异;`-race` 41 包绿 |
| **M16.7 Web 会话工作台(侧栏重构+会话/工作区管理)** ✅ (2026-09) | **后端**:sdk.SessionInfo 增 `Preview`(会话内容省略版)+ CwdSessions 接口增 `Delete(id)`(删会话 jsonl+清洗 names.json 显示名索引,删当前会话自动切主会话,id 白名单防路径穿越)与 `UnrecordProject(key)`(仅移除 workspaces 记录,不碰文件夹,幂等);host-cwd-sessions 实现 + previewOf(扫 jsonl 首条 user/message 截断 48 字,坏行容忍)+ validSessionID;web/server.go POST /api/sessions action 增 `delete`、新端点 `DELETE /api/workspaces/{key}`。**前端**(Sidebar.vue):工作区改为**固定独立区**(侧栏顶部,不归入历史分类,当前工作区浅蓝选中条);历史会话条目展示**名称 + 内容省略预览(两行截断)+ 时间**;会话名 **✎ 内联改名**(后端 rename 仅作用当前会话 → 先切目标会话再 rename)+ **× 删除会话记录**;工作区 × 删除记录(**二次确认**,仅删记录不删文件夹)。**测试**:host-cwd-sessions 增 TestPreview/TestDelete/TestUnrecordProject;tui fakeCwdSessions 同步接口新方法;web 端 t 真机冒烟(GAH_WEB_OPEN=0,26 会话 3 工作区,预览正确)+ Chrome 截图验证布局;全库 -race 41 包绿 | 工作区固定展示不埋历史;会话列表一屏看内容摘要;会话/工作区记录可改名可删除(二次确认,文件夹零触碰);`-race` 全绿 |
| **M16.6 外部 tool-mcp 多 server** ✅ (2026-09) | **extplugins/tool-mcp 扩展**(向后兼容):`GAH_MCP_COMMAND`(单 server,工具 `mcp_<name>`,语义不变)与新增 `GAH_MCP_COMMANDS`(多 server,每行 `name=command args`,# 注释/空行忽略,工具 `mcp_<server>_<name>`)并存取并集;server 名 sanitize(仅 [A-Za-z0-9-_],其余转 `_`);单个 server 连接失败记 stderr 跳过不拖垮其余(对齐宿主跳过失败插件语义),全部失败/无工具 exit 1(防静默空转);工具名跨 server 冲突记警告保留先注册。**根因修复**:mcpTool 分离 `name`(注册名,host 可见)与 `def`(原始定义)——桥协议 Definitions 用 `Definition().Name` 暴露注册名,Execute 仍按原始 `mcp_<tool>` 路由到对应 server(client 内 names map 还原),单 server 两者相同故此前无感。**配套**:tests/mcpserver 增 `-name` 实例标识;(tools)external_test.go 增 TestExternalMCPBridgeMulti(alpha/beta 两实例注册 mcp_alpha_greet/mcp_beta_greet 且路由断言),单 server TestExternalMCPBridge 保持;config 与 internal/embed/seed 两份 bundle-base 注释同步(无新条目,不 bump seed-version);gen-extplugins.sh 重跑(6 平台产物仅 tool-mcp 变化)。**用法**:`GAH_MCP_COMMANDS="deja=/opt/homebrew/Cellar/deja-vu/0.19.3/bin/deja\ncodegraph=codegraph serve --mcp" gah --profile web` | 一个 tool-mcp 进程同时挂多 MCP server(deja + codegraph 等),工具按 server 前缀注册互不冲突且路由正确;单 server 旧配置零改动;全库 -race 41 包绿 |
| **M17 审批等级三档(开放/智能/严格)** ✅ (2026-09) | policy-approval 由单档扩为三档(对齐 policy-sandbox 先例):① **sdk**:新增 `ApprovalMode`(open/smart/strict)+ `ApprovalService`(Mode/SetMode,对称 sdk.Sandbox);policy-approval 实现并 `Provide("ctx.approval")`,pre-execute 拦截按档分支:open=直接放行 / smart(默认)=命中危险模式弹确认(无通道拒绝,安全默认)/ strict=直接拒绝不弹窗。注意:§9 规划的会话级 allowlist(always)尚未实现,strict 语义即"每次拦截",实现时未引入 allowlist。② **切换**:TUI `/approval open|smart|strict`(tui/app.go cmdApproval + 命令提示;host-internal-commands 同步下沉,判重跳过)+ web 设置面板「审批」分段控件(/api/control approval 扩展,state 增 approval 字段)。③ **持久化**:internal/prefs 增 SetApproval(gah-state.json,gah-state 与 web 共享),TUI applyPrefs 启动恢复;bundle-base.yaml `policy-approval` 增 `data.mode: smart`(seed-version 9→10→11,M18 再 bump)。**测试**:policy-approval 三档行为单测(TestApprovalModes:strict 拒绝/open 放行/smart 拒绝分支/运行期切档生效;TestDefaultModeSmart)+ host-internal-commands /approval 执行+偏好持久化+注册集断言(增 approval)+ prefs roundtrip 含 approval;真机冒烟(web /api/control approval=strict → state 回读 strict);全库 -race 绿 | 危险操作按档处理:开放零拦截/智能弹确认/严格直接拒绝;TUI /approval + Web 设置面板双端可切、重启经偏好恢复;-race 全绿 |
| **M17 补充:审批档位全界面可见** ✅ (2026-09) | M17 交付后审批档仅设置面板可见,TUI/Web 状态栏缺“当前等级”展示(用户反馈)。**改动**:① **TUI 状态栏**(tui/chrome.go renderStatusLine):沙箱与会话之间插 `| 审批: 开放/智能/严格`(新增 approvalLabel 中文映射 helper,未知值回退“智能”,空档位省略段与会话段处理一致);② **Web 状态栏**(StatusBar.vue):沙箱后加淡显 `审批 开放/智能/严格`(approvalLabel computed 映射,未知不显示)。设置面板分段控件(当前档高亮)已满足查看,不动。**测试**:chrome_test TestStatusLineFields 增断言(smart→审批: 智能/三档中文映射/空档位与主会话同省略);全库 -race 绿 | TUI 与 Web 状态栏随时可见当前审批档位(开放/智能/严格),切换后与设置面板同步;-race 全绿 |
| **M18 整体备份/恢复** ✅ (2026-09) | 新插件 `plugins/host/host-backup`(落便携纪律):① **sdk**:新增 `BackupService`(`Backup(dest?)`/`List()`/`Restore(name)`)+ `BackupInfo`;host-backup 实现并 `Provide("ctx.backup")`。② **备份**:GAH_HOME 单根全部子目录(config 含密钥/plugins/sessions/env.sh/偏好等)**排除 backups/ 自身**(防递归膨胀);tar.gz 确定性(`gzip.Header.ModTime` 置零 + Name 空,对齐 -n;TestBackupDeterministic 同源两次备份字节一致);时间戳命名 `gah-backup-YYYYMMDD-HHMMSS.tar.gz`,同秒重跑追加序号防覆盖(restore 前自动备份场景);默认存 `$GAH_HOME/backups/`,外部 dest 路径支持。③ **恢复**:危险操作——恢复前**先自动备份当前状态**(安全默认,TestRestoreRoundtrip 断言至少 2 份),解压覆盖；仅接受相对条目(../ 越界中止,TestRestoreBadArchive 恶意归档),坏归档显式拒绝;提示重启生效。④ **入口**:ctx.commands 注册 `/backup`(无参=立即备份|list|restore <name>,二级选择器枚举归档)+ web `/api/backup` 端点(GET 列表/POST backup|restore,未装配 503)+ 设置面板「数据备份」区段(立即备份/恢复最新备份,恢复经 askConfirm 二次确认、danger 红)。⑤ **可选** `data.backup_on_start` 启动自动备份 + `data.keep` 轮转保留(默认 5)。⑥ **登记**:catalogue(Bundle=base,Provides ctx.backup)+ config 与 internal/embed/seed 两份 bundle-base(seed 9→10→11)+ plugins/README 行。**测试**:host-backup 单测全集(内容完整性/排除自身/确定性/外部 dest/无 GAH_HOME 显式错/恢复幂等+自动快照/坏归档与越界拒绝/经接口断言)+ web TestBackupEndpoint(未装配 503/列表/备份/恢复)+ 真机冒烟(web profile:POST backup → 归档落盘,list 可见,restore → ok 且自动快照新增,state 正常);全库 -race 绿 | `/backup` 一键整体备份(含密钥),`restore` 前自动先备份当前态+二次确认;默认存 $GAH_HOME/backups 随目录迁移;TUI + Web 双入口;-race 全绿 |
| **插件管理域声明下沉 catalogue** ✅ (2026-09) | web 插件清单 manage 分域原硬编码于 web/server.go 两 map(extExternalPlugins/extScenarioPlugins)——新增外部化/场景插件需双处同步,易漏。改为**声明驱动**:sdk.PluginInfo 增 `Manage` 字段;catalogue.Def 增 `Manage`(external 8 + scenario 3 在册声明);catalogueInfo 透传至候选清单;web handlePlugins 改读声明(优先级保持 loaded→host > 声明 > web),删两 map;types.ts 契约不变(前端零改动);plugins/README 维护约定同步;catalogue 守卫测试(值合法+历史集合不退化)+ web stub 补声明用例(tool-web 声明 external 但 loaded → host 优先) | 新增外部化/场景插件仅 catalogue 登记时声明 Manage 即自动正确展示,web 名单废弃不再双处同步;-race 全绿 |
| **超长输入可见可改(web+TUI)** ✅ (2026-09) | 粘贴超长内容时:web 输入框高度封顶 180px 且无显式滚动,超出不可见难删改;TUI 输入区按 \n 全量渲染无上限,长输入占满整屏压没会话流、无滚动、长行横向截断。修复:**web**(InputBar.vue)autoGrow 封顶改 max(180,40vh),.field 显式 overflow-y:auto + overflow-wrap:anywhere;**TUI** 新增 inputPhys(折行+光标物理行/列换算,复用 wrapSegment)与 renderInputLine 窗口化(变参 maxRows 兼容旧调用:长行折行不横向截断、物理行超 inputMaxRows=max(3,(h-8)/3) 时以光标为锚 pickWindow 滚动、上方省略指示“…↑ N 行”,光标块始终跟随);render.go 按窗口扣主区几何,长输入不压没会话流;↑/↓/Home/End 移动即查看删改全文 | inputPhys 单测(折行/双宽/光标定位)+ 窗口渲染测试(封顶滚动/触底/短输入兼容)+ 旧调用兼容(TUI 全量渲染断言不变);tui 定向与全库 -race 绿;web vue-tsc 0 错 |
| **TUI 输入框圆角矩形化** ✅ (2026-09) | 输入区由 P5 左缘竖线改**圆角矩形框**(╭─╮/╰─╯,整宽对齐终端):**边框颜色随思考等级**(thinking 语义保留,pi 编辑器 thinking 边框同款),框内内容行 pad 统一宽;renderInputLine 去左缘竖线(提示符 ❯ 与续行缩进统一 2 列前缀),折行宽随框内可用宽调整;renderInputFrame 新函数(内容多行含窗口滚动/省略指示整体包框);布局几何基准 6→8(输入区基准含顶/底边框 2 行),总输出行数恒定 height-1 不变,主区窗口相应调整 | 圆角框单测(边框符号/整宽一致/thinking 色映射)+ 布局测试适配(输入区位置/主区窗口)+ 示例测试 TestInputFrameDemo(-v 展示整屏效果);全库 -race 绿 |
| **设置面板备份端点 null 崩溃修复(web)** ✅ (2026-09) | 现象:点击状态栏「设置」无弹窗。根因:host-backup 空备份目录 `List()` 返回 nil → `GET /api/backup` 直接序列化为 `null` → 前端 loadBackups 赋值 `backups=null` → 面板渲染「数据备份」区段读 `null.length` 抛 TypeError → Vue 渲染中断整面板不出现(控制台反复报错)。**修复**(双向防守):① web/server.go handleBackup GET 段 `list==nil → []`(JSON 契约空数组);② SettingsPanel loadBackups `(await api.backups()) ?? []` 兜底。**验证**:headless Chrome CDP 真机复现(点击后 mask/panel 缺失+控制台 TypeError)→ 修复后 `GET /api/backup` 返回 `[]`、点击设置面板正常弹出(aria-expanded=true、mask/panel 均渲染、零控制台错误);web+host-backup 定向与全库 -race 绿;vue-tsc 0 错 | 设置面板在任何 profile 正常弹出(空备份目录不再 null 崩溃);后端契约空数组 + 前端 ?? [] 双保险;-race 全绿 |
| **Web 体验改进:模型筛选 + 侧栏双滚动 + models 契约修** ✅ (2026-09) | ① **设置面板模型筛选**:原生 select 改**输入框实时过滤 + 匹配列表**(provider·模型名均可匹配,列表 max-height 滚动、当前高亮、点选即应用,沿用 applyModel 链路);模型过多不再难找。② **侧栏工作区/历史会话分块双滚动**:panel 整体滚动改 flex column;工作区顶部独立区(flex-shrink 0 + max-height 250,内部自行滚动),历史会话占满剩余(flex 1,独立滚动);两区互不挤压、各自滚动条。③ **顺带契约修**:`/api/models?all=1` 无 provider 时 ListAllModels nil → 序列化 `{providers:null}`,与 backups 同族;handleModels 补 nil→[](前端 ?? [] 双保险)。**验证**:CDP 真机(侧栏注入 30 会话 → session scrollHeight 759>client 228 独立滚;设置面板弹出 + 筛选框/空态提示就位;`/api/models?all=1` 返回 `{providers:[]}`);vue-tsc 0 错 + build 通过;全库 -race 绿 | 模型过多可输入筛选快速定位;工作区与会话各自滚动互不干扰;空模型/provider 配置不再 null 干扰前端;-race 全绿 |
| **桌面化准备:POST /api/shutdown 优雅停机端点** ✅ (2026-09) | 背景:Tauri 桌面壳 spike 验证暴露——Windows 无 SIGTERM、壳被信号强杀时 dispose 不跑,缺**跨平台优雅停机通道**(dispose 链本身完整,早前“插件残留”系信号未达宿主之误判,受控 SIGTERM 实测外部插件 100% 回收)。改动:`web.Server` 增公开字段 `OnShutdown func()`(对齐 OnReady 模式)+ 路由 `POST /api/shutdown`(走 authMiddleware;先回 200+Flush 再触发回调,避免与 http.Shutdown 竞争;未装配 → 503 不静默降级);`plugins/ui/ui-web-app` 绑定 `OnShutdown` → `Emit("system/shutdown")`(cmd/gah 既有订阅 → 退出 → DisposeAll 回收插件/外部进程)。**验证**:web 单测(未装配 503 / 装配 200+回调触发+响应体 / auth_token 未授权 401 且不触发);真机端到端(真实宿主 POST → 200 → 优雅退出 → 外部插件全回收(go-plugin Kill 链日志完整)→ 端口释放);相关包 -race 绿;残留扫描干净 | `gah web` 运行中 POST /api/shutdown → 200 且宿主/外部插件/端口全量干净回收;桌面壳与 Windows 场景复用此端点优雅停机 |
| **policy-guard 审批+沙箱融合** ✅ (2026-09,seed 12) | **并行会话交付**:policy-approval(审批三档 open/smart/strict)+ policy-sandbox(沙箱三档 read-only/workspace-write/full-access)合并为单插件 `plugins/policy/policy-guard`(统一裁决点 guard.go + 审批支路 approval.go + 沙箱支路 sandbox.go + link.go 联动),档位状态机/裁决语义与迁移前一致(有“有效档”概念承接升级);原两插件从 catalogue/代码移除;config 与 seed 两份 bundle-base 同步(seed-version 11→12),tests 引用同步(web_e2e/integration 等);全库 -race 绿 | 审批+沙箱单插件统一裁决;档位/沙箱能力与迁移前等价;-race 绿 |

## Web 附件(图片/文件)+ 多模态 + 输入快捷键 ✅ (2026-09)

### 交付:附件一期(见 docs/WEB_ATTACHMENTS_PLAN.md)

| 模块 | 交付 | 验证 |
|---|---|---|
| **后端上传/托管** | web.Server `Config.AttachmentsDir`(ui-web-app 注入 `$GAH_HOME/attachments`);`POST /api/attachments`(multipart 流式;单文件 ≤20MB/总量 ≤32MB/≤8 个;类型白名单 image/text/json/pdf;原始名 filepath.Base 防穿越;时间戳目录+重名后缀);`GET /attachments/{...}` FileServerFS 静态预览(未配置 503);`inputReq.Attachments` 校验(须在附件根内且存在,越界 400)+ `[附件]` 路径引用注入 | 单测(上传/类型拒/未配置 503/穿越/越界 400/注入断言)+ 真机上传双向验证 |
| **多模态注入(看图)** | sdk `Attachment{Kind,Name,MimeType,Rel,Path(json-)}` + `LLMMessage.Attachments` + `UserMessage.Attachments`;`sdk.AttachmentInput` 可选扩展(`RunWithAttachments`);host-agent-loop 实现(Run=nil 转发);sessionlog `DeriveMessages` 透传;openai 适配器 `wireContent`:含图→content 数组(text+image_url data URI),纯文本兼容 string;anthropic 适配器 `wireImage` base64 块;Rel 随 jsonl 持久化(便携),Path 运行时(重放空=跳过视觉、保留文本引用) | 两适配器单测(payload 数组/纯文本/文件类不视觉)+ sessionlog 附件透传 + 真机 jsonl 断言(Attachments rel 落盘)+ input 带附件 202 |
| **前端附件 UI** | InputBar:工具条「附件」SVG 钮+隐藏 file(multiple)+ 拖放高亮(dragging)+ 粘贴图片(Ctrl/Cmd+V);附件 chip 区(图片缩略图 objectURL/文件名/大小/× 删除;上传中 up 高亮/失败 err+重试);提交先 uploadAll→paths→onSubmit(text,paths),命令消息禁附件;StreamView 用户气泡渲染图片附件(`/attachments/<rel>` 段编码) | vue-tsc 0 错 + build 通过 + 端到端链路(upload→input→jsonl) |
| **快捷键(web)** | InputBar 统一 `onKeydown`:`Enter`/`Cmd/Ctrl+Enter`=发送(平台归一 `e.metaKey||e.ctrlKey`),`Shift+Enter`=换行(原生),`Esc`=关会话抽屉→清空输入(有内容);全选/粘贴删除=浏览器原生 Cmd/Ctrl+A(不拦截);placeholder 明示 Enter·Shift+Enter·Esc | vue-tsc + 构建 |
| **快捷键(TUI 补全)** | Ctrl+A 全选(State.selectAll:Backspace/Delete 一次清空、输入=替换、导航复位;单测);Ctrl+B/F 逐字符左右(Alt 组合失效终端兜底);Ctrl+Y redo(Ctrl+Shift+Z 兜底) | TestSelectAllConsume + 回归 -race |

**兼容矩阵**:输入快捷键分两类——ASCII 控制键(Ctrl+*,kitty/iTerm2/Terminal.app/Windows Terminal/WezTerm/ConHost 全通)与 Alt 组合(esc 前缀;mac 需终端 Option→Meta,win ConHost 不支持→用 Ctrl+B/F 兜底)。web 端浏览器原生跨平台(Cmd/Ctrl)。详见 docs/VERIFY.md「输入快捷键兼容矩阵」。

## B3 TUI 内部命令下沉宿主 ✅ (2026-09)

> 目标:原 TUI 内部命令(thinking/model/…)注册在 ui-tui-app 装配时 → web profile 无 TUI → web `/` 下命令不可用。下沉后任意 profile 通用。

| 模块 | 交付 | 验证 |
|---|---|---|
| **宿主插件** | `plugins/host/host-internal-commands`(catalogue Bundle=base;seed 8→9):Start 经 `ctx.commands.Register` 注册 11 命令(thinking/model/provider/sandbox/plugins/settings/export/compact/workspace/session/reload),注册前判重(已存在跳过,先到先得);handler 纯服务注入(去 TUI 状态耦合),偏好/provider/插件持久化逻辑原样搬移 | 单测(注册集 11+UI 专属 8 不在宿主;thinking 执行经 stub llm;二次 Start 判重不覆盖) |
| **会话切换事件** | host-cwd-sessions 增 `emitSession`(Open/New 后广播 `cwd/session-switched`;与 workspace-switched 同款) | TestSessionSwitchEmitted(Open/New 均广播) |
| **TUI 联动** | registerInternalCommands **判重跳过**(宿主先注册则不再重复,共用注册表命令);App 订阅 `cwd/session-switched`+`cwd/workspace-switched` → `onSessionSwitched`(经 Inject 取服务复用 afterSessionSwitch 刷新:状态栏/统计重置/清流重放/工作区名);留 TUI 专属 8 命令(search/widgets/theme/help/exit/fork/tree/name) | TUI 包 -race;TestTUIProbe/NonTTY 真机通过 |
| **web 通用** | web profile 装配 base → 命令注册 → `/api/commands` 含全部 11、UI 专属零误下沉;`POST /api/input "/thinking off"` 200 且 `/api/state` thinking=off | 真机 web 全链路 |
| **回归** | 全库 43 包 -race 绿;pty 探针三处 `~/.gah` 拷贝改 Skip(便携迁移后真实 home 无 config 的环境性失败) | — |

## B5 UI 槽位 v2:设置/侧栏/附加面板扩展点插件化 ✅ (2026-09)

> 目标:v1 仅四槽位(stream/input/statusbar/confirm)覆盖制;v2 开放三个**多实例追加型**扩展点,第三方插件直接注入宿主界面区段/入口/面板,零改宿主代码。向后兼容 v1(四槽位语义不变)。

| 模块 | 交付 | 验证 |
|---|---|---|
| **registry v2** | `registry.ts` 新增三扩展注册表:settings-section / sidebar-action / extra-panel(每插件以其 id 为 key 追加;priority 排序降序,同优先级注册晚者先;`extSnapshot()` 调试断言);v1 四槽位逻辑与 `slotSnapshot()` 不动 | vue-tsc 0 错 |
| **加载器分派** | `plugins.ts` 槽位名分派:v1 四名走原 registerSlot;v2 三名走 registerSettingSection/registerSidebarAction/registerExtraPanel;未知槽位仍忽略 | 同上 |
| **落点** | SettingsPanel 尾部注入 section 组件(每插件一节 .sec);Sidebar 增「插件动作」区(actions 组件)与「附加面板」区(入口列表);App 增附加面板抽屉(右侧 340px,token 化,标题来自 manifest.title) | vue-tsc + 构建 |
| **安装侧** | `internal/install` 槽位白名单扩 v2 三名(拒装校验注释同步);web server 聚合透传(slot 名注释同步) | go 编译 + -install-ui 真机 |
| **示例插件** | `web-src/examples/extension-demo`(vite lib 三入口):settings-section(设置面板区段)+ sidebar-action(侧栏动作按钮)+ extra-panel(附加面板);复用 statusbar-demo 依赖 | `-install-ui` 安装成功(覆盖 3 槽位)+ `/api/ui-plugins` 聚合显示 3 slot |
| **回归** | 全库 43 包 -race 绿;web/internal/install/ui-web-app 重点回归 | — |

## C1 desktop 壳工程落地 ✅ (2026-09,P1)

> 目标:可行性 spike(§ DESKTOP_FEASIBILITY)形式化到仓库 `desktop/`——Tauri v2 壳 + sidecar gah 的完整 P1(签名/CI/更新属 P2/P3)。

| 模块 | 交付 | 验证 |
|---|---|---|
| **工程** | `desktop/src-tauri/`(Cargo.toml:tauri 2 + shell/single-instance/autostart/notification + tray-icon feature;tauri.conf externalBin sidecar;frontendDist=ui 占位 loading;bundle targets app+dmg)+ `.cargo/config.toml`(tuna 镜像,中国区可构建) | cargo build 通过 |
| **壳逻辑**(main.rs) | spawn sidecar `gah --profile web`(GAH_WEB_OPEN=0;GAH_HOME 不设=与 CLI 共享 ~/.gah,显式传入优先)→ 轮询 `/api/state` 就绪 → navigate(端口已占用 = 接管现有实例,启动探测兜底);单实例插件(多开 focus);托盘(显示窗口/开机自启开关/退出);回合完成通知(轮询 running 翻转);关窗驻托盘(CloseRequested prevent_close+hide);**退出链**:托盘退出 → POST /api/shutdown → 等端口释放(5s)→ 超时置强杀标记 → RunEvent::Exit kill 兜底 | 真机:sidecar spawn ✓ 2233 LISTEN ✓ WebKit 渲染(14 连接)✓ shutdown 200 → sidecar 退出/插件零残留/端口释放/壳保持 ✓ *(2026-09-16 R6 变更:spawn 改恒传 GAH_HOME(显式 env > 应用数据目录),不再与 CLI 共享 ~/.gah;见 R6 登记与 docs/DESKTOP_FEASIBILITY.md §10)* |
| **构建** | `scripts/gen-desktop.sh`(本平台 triple sidecar go build → cargo build dev/release) | 脚本跑通 |
| **登记** | .gitignore(desktop target/binaries;Cargo.lock/ui 保留入库) | — |

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
