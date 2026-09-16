# Go Agent Harness 设计方案

> 基于 DeepSeek Harness(dsh)"一切皆插件"设计哲学 + Cordis 可逆插件框架理念,使用 Go 实现。Cordis 插件系统 Go 化。暂不使用 Web 端,以 TUI 为界面。
> 参考项目:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)。
> 状态:已实施。M1–M6、P0–P4、M7 Web 线 + M16.7(Web 会话工作台/taste UI 收敛/工作区真实切目录/删除语义/tool-mcp 多 server)+ M16.8(可视化设置面板/输入一体外壳/左右分割/动效层/偏好持久化 TUI 共享/插件管理域)+ M16.9(便携数据根 gah-data + 便携纪律)全部交付(M7 起 Web 端已启用,与 TUI 双界面并存);**2026-09 二期五项全部交付**:Web jobs 面板(B1)/会话 export(B2)/命令下沉宿主(B3)/mcp-bridge 看护(B4)/UI 槽位 v2(B5);另交付:优雅停机端点 POST /api/shutdown、Web 附件(图片/文件)+多模态、快捷键(与兼容矩阵)、GAH_WEB_ADDR。**M17 审批等级三档(开放/智能/严格)+ M18 整体备份/恢复 已交付(见 §14.1 交付表)**:审批档 open/smart/strict 运行期切换(TUI /approval + Web 设置面板,偏好持久化);/backup 一键打包 GAH_HOME(确定性 tar.gz,排除 backups/ 自身,恢复前自动先备份当前态)。**policy-guard 融合 + seed-version 12**(policy-approval/policy-sandbox 合并单插件统一裁决,并行会话交付)。**R5 三端复查 + R6 观察项完善已交付(2026-09-16)**:~/.gah 旧解析链残留收敛(57739b1 弃用同步)、SSE 重放/订阅 gap 修复(先订阅后重放+Seq 去重)、WS 指数退避重连、桌面壳数据根恒传应用数据目录 + 启动失败窗口提示;DESIGN §14.1 未实施清单清零。**R7 桌面零成本发行工程(C2/C3 变体,2026-09-16)**:updater 接入(ed25519 自持签名 + GitHub Releases 端点 + 托盘检查更新)+ publish-desktop.sh + release-desktop CI 矩阵 + docs/RELEASE.md 无签名分发指引(见 §14.1 R7;待 repo 公开 + 首 tag 启用)。**R8 数据根收紧(2026-09-16)**:排除 GAH_HOME env 输入,数据根唯一 = 二进制同级 gah-data/(无则自动创建,禁 ~/.gah;桌面壳同步去应用数据目录,见 §14.1 R8)。交付总览以 AGENTS.md「便携纪律」与 DESIGN §14.1 交付表、docs/ROADMAP.md P3 表、docs/TODO_OVERVIEW.md 为准。**IM 线已整体放弃(2026-11-14)**:曾交付的 IM 远程控制线(微信 iLink / QQ Bot v2 / 飞书自建应用三通道及其插件、SDK 契约、Web 端点、命令面)全部删除,细节见 §14.1。**R10 审计三片补跑(data-io / docview-ext / tui-front,2026-11-14)**:首轮 6 片中 3 片因上游 429 失败的切片已重跑并全量修复(含 1 P0 / 6 P1),细见 §14.1 R10 补跑段。**R10 能力化路径裁决(2026-11-14)**:`sdk.ToolDefinition.PathParams` 可选能力声明 + policy-guard 声明优先裁决(已知未闭环 ④ 关闭),细见 §14.1;此前 **R10 安全加固与生命周期修复**已交付:Web 跨站攻击面收口(guard 护栏 + token cookie 引导 + `?token=` 废弃)/ 默认发行态路径沙箱修复(宿主 pre-execute 统一裁决)/ Provide 生命周期可回收 + 卸载即撤销 / 子进程凭据隔离 / 工具级超时与进程组回收,细见 §14.1 R10(含 4 条已知未闭环登记)。**R10 后续收敛(2026-11-14)**:已知未闭环 ④(工具能力声明)/ ⑥(外部执行可中断:桥协议增 `Plugin.Cancel` RPC + `CallID`/`TimeoutMs`,旧插件自动降级)/ ⑦(docview 资产按最近预览文档窗口淘汰)均已补齐(④⑥⑦ 关闭,遗留边界见 §14.1);同时建立**覆盖率门**(`scripts/coverage-check.sh`:48 个关键包逐包棘轮 + 全局下限 + 零覆盖包禁止,阈值单一事实源)并修复 **CI 盲区**(`sdk` 为独立 module,CI 的 `go vet`/`staticcheck`/`go test` 三步此前从未覆盖它,已补 `cd sdk && …`)。**R10 剩余未闭环收敛(2026-11-14)**:① **shell 命令显式写目标**纳入路径裁决(审批通过 ≠ 放开档位:workspace-write 下越界写即便人工同意仍被拒,需 `/sandbox full`)、③ **Web 全表面鉴权**(静态资源/附件/UI 插件产物与 `/api/*` 同等保护;入口页不再自动下发 cookie,改 URL fragment 引导 + `POST /api/auth` 换 HttpOnly cookie)均已关闭;顺带修复 `web.Server` 的 Start/Shutdown 数据竞争与卸载后孤儿监听(测试暴露)。覆盖率补强后棘轮同步上调(host-internal-commands 95.9% / catalogue 100% / ui-web-app 79.5% / cmd/gah 56.5%);R10 未闭环仅余 ② 与 ⑤(均为设计取舍/规划项),详情见 §14.1。

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
| 体积复测(2026-11-14,矩阵) | ⚠️ 2026-10-11 的 46/30 门在 **2/5 目标已失败**(darwin/amd64 46.14 MiB·gz 30.30、windows/amd64 46.28 MiB·gz 30.50)→ 二轮重定基 **48/32**(余量 ~1.5–1.7 MiB),归因与降体路径登记在 `scripts/size-check.sh` 头 + 本节交付门行;40 MiB 时代的「31.9MB」读数已彻底过时 |
| 体积复测(2026-10-11,矩阵) | ⚠️ 旧门(<40 MiB)在 **4/5 目标已失败**:darwin/amd64 43.47、windows/amd64 43.63、linux/amd64 42.38、darwin/arm64 40.59、linux/arm64 38.88 MiB;gz 产物 25.81–29.46 MiB。按实测**重定基**(E-C):二进制 ≤46 MiB / gz ≤30 MiB,阈值单一事实源 = `scripts/size-check.sh`(CI 同一脚本);构成 = 基线 19.3–22.3 MiB + 本平台 extplugins gz embed 19.6–21.6 MiB |
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
- **审批策略**(policy-guard 插件):危险操作经确认通道(确认档见 M17:open 直接放行 / smart 弹确认 / strict 直接拒绝);与 waterfall veto 组合(policy 先决,用户后决)。**注:本节早期规划的 `y/n/always` + 会话级 allowlist 未实现**(实际只有 y/n;M17 交付说明与 R10 记录均以此为准)。
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
| **交付门**(已通过;**2026-10-11 E-C 重定基 → 2026-11-14 二轮重定基**) | 单二进制 ≤**48 MiB** 且 gz 产物 ≤**32 MiB**(2026-10-11 定 46/30;2026-11-14 首个 Release 前 CI 复跑实证 46/30 再次被突破:<br>darwin/amd64 46.14 MiB·gz 30.30、windows/amd64 46.28 MiB·gz 30.50 超门 → 按实测重定基,余量 ~1.5–1.7 MiB;归因 = 基线 19.3–22.3 → 21.0–24.4 MiB(R10/M16.x/二期/M17–M18/host-schedule/MCP 代理等)+ extplugins gz embed 21.0–21.9 MiB(仍为主体:4 件 ×~5.4 MiB 的 go-plugin→gRPC 栈);**降体路径本轮未做,已登记体积债**:① 协议去 gRPC 化 ≈-14 MiB/插件 ② extplugins 附包化 ≈-20 MiB(破「单一静态二进制」承诺) ③ embed 换 xz/zstd 纯 Go 解码 ≈-3~5 MiB(需依赖评审)—— 新功能再破门时先执行其一,不第三次抬门)/ `CGO_ENABLED=0` / 五目标交叉编译 / 裸机 scp 启动(首启释放插件后可用) | ✅ 实测数据见 §7.6;阈值与护栏 = `scripts/size-check.sh`(CI 调用,超门即失败;阈值沿革与归因写在脚本头) |

### 14.1 未交付规划清单(M6,按需逐个实现)

> **状态图例**:标题 `✅` = 已交付实施;标题 `⏳ 未实施` = 规划待执行、尚未开工(规划条目正文为完整方案,按切片实施)。
>
> **未实施清单:M17 审批等级三档 + M18 整体备份/恢复已交付 ✅(2026-09,见下方交付行)**;2026-09 二期(Web jobs 面板/会话 export/命令下沉宿主/mcp-bridge 看护/UI 槽位 v2)全交付 ✅(→ DESIGN 交付表与 docs/TODO_OVERVIEW.md)。其余远期增量(会话树 Web 可视化 M7.2.1、类型分发等)见 docs/ROADMAP.md。**⏳ 体积债(SZ-1,2026-11-14 登记)**:extplugins 是 embed 主体(4 件 ×~5.4 MiB gz = 21.0–21.9 MiB,go-plugin→gRPC 栈),三条降体路径(① 协议去 gRPC 化 ≈-14 MiB/插件 ② extplugins 附包化 ≈-20 MiB,代价=打破「单一静态二进制」承诺 ③ embed 换 xz/zstd + 纯 Go 解码 ≈-3~5 MiB,代价=新增依赖)本轮均未做 —— 体积门已被两次抬升(<40 → 46 → 48 MiB),**下一次破门前必须先执行其一**,不再抬门;细则与实测表见 `scripts/size-check.sh` 头。已交付 ✅:M6.1–M6.21、M8-T1、M10、M11-T1、M11-T2、**M9 子代理全交付(one-shot + 后台控制 + send_message/fork,见下交付行)**、M12 多 provider 并存(2026-09)、**M13 TUI 主题外部化(2026-09)**、**M14 外部命令桥(2026-09)**、**M15 TUI π 式默认样式(2026-09,见下交付表)**、**M7 Web UI 全交付(S1–S3,见下交付行)**、**M7.2 UI 槽位插件化(见下交付行)**、**M7.3 WebSocket 通道(见下交付行)**、**M8-T2 展示联动(见下交付行)**。TUI 线全交付 ✅(S1.1–S2.2 + 折叠交互 + M13 主题外部化;S3.1 决策方案 A 记录在案;S3.2 框架覆盖)。外部命令桥交付后,新命令插件不再需要重编译 gah(见 M14 行与 docs/PLUGIN_DEV.md §4.1)。
>
> **体验改进(对比 pi 基线)**:P4 12 项**全部交付** ✅(2026-09 收口:P4-1 消息队列 · P4-2 @引用+Tab 补全 · P4-3 代码块高亮 · P4-4 会话命名 · P4-5 C6 多级上下文 · P4-6 多行输入/外部编辑器 · P4-7 工具视觉增强 · P4-8 /compact · P4-9 语义色 token 化 · P4-10 C1 会话树/分支(树可视化 UI 归 M7 后)· P4-11 /reload · P4-12 widget 槽位;细见 `docs/ROADMAP.md` P4 节)。
>
> **TUI 界面优化**(非里程碑,详见 `docs/TUI_OPTIMIZE.md`):S1.1–S1.6 全交付 ✅;S1.4 Markdown 轻渲染、S2.1 消息分组/工具行去重、S2.2 渲染组件化(render/session/chrome/markdown 四层)+ 折叠交互 已交付 ✅(2025-09/10 迭代);S3.1 主屏 scrollback 决策=方案 A 维持现状(2025-10,理由与 B/C 备选记录于 docs/TUI_OPTIMIZE.md S3),S3.2 框架覆盖 ✅(S3.2 synchronized output 由 bubbletea v2 框架自动启用 ✅)。
>
> **实施排期**:P0 ✅、P1 ✅、P2 ✅、P3 ✅、P4 ✅ 均已交付(P0:划选/搜索/滚动条/输入增强 + M8-T1 + M10 + /workspace;P1:S1.4/S2.1 + M9.1 + M11-T1;P2:S2.2/S3.2 + M9.2/9.3 + M11-T2 + S3.1 决策方案 A;P3:M7 Web UI 全家桶 + M7.2 槽位 + M7.3 WS + M8-T2 展示联动 + M13/M14/M16,完成于 2026-09)。
>
> **三端文档预览线(D 组 D0–D6,规划正文见 docs/DOC_PREVIEW_PLAN.md;D0–D5 已交付 ✅ 2026-10-11;D6 可选增强见下方「未实施清单」)**——一个文档模型 `sdk.DocView` + 三个呈现器(TUI / Web+桌面 / headless CLI+模型工具),覆盖 markdown/Word/PPT/Excel/PDF;基线新增 2 个纯 Go 库、**新增传递依赖 0**(gopdf BSD-3 仅需 gah 已有的 `golang.org/x/text`;goldmark MIT 零依赖),OOXML 自研抽取器,保真走浏览器原生查看器;顺带补齐 **Web 会话流 markdown 渲染缺口**(现 StreamView 零 md 渲染,与 TUI 不对等)。**已锁决策**:K1 = PDF 文本抽取引 `Detective-XH/gopdf`(不自研);K2 = Linux 桌面壳(WebKitGTK)PDF 降级为「下载」,**不引入 pdf.js**。切片:D0 契约/依赖引入/安全预算 → D1 文本族+工作台+pager+CLI(含会话流 md) → D2 docx → D3 xlsx/pptx → D4 pdf(量级 M) → D5 模型工具与 `doc/open` 三端联动 → D6 可选增强(光栅/外部转换器)。**待执行**。
> ✅ **D0 文档预览契约与骨架(2026-10-11)已交付**:① **sdk 契约**新增 `sdk/doc.go`(DocFormat/DocBlockKind/DocRun/DocCell/DocAsset/DocBlock/DocSheet/DocView/DocRequest/DocLine/DocPDFFacts/DocText + `DocService` 五方法 Detect/Preview/Text/Asset/Raw;结构化块模型,零 HTML 通道;哨兵错误映射 CLI 退出码),`DocView/DocText` 可 JSON 序列化,新增字段一律 omitempty。② **新插件 `plugins/host/host-docview`**(bundle=base,Provides `ctx.doc`,ctx.sandbox 可选注入;只 import sdk+stdlib,零写盘)**= 统一前置**:resolver(相对→workspace 根 / 绝对→沙箱三档校验 / EvalSymlinks 前缀归属防逃逸 / strict 模式收窄为 workspace∪$GAH_HOME/attachments 并叠密钥 deny-list:`.env`·`credentials*`·`id_rsa*`·`*.pem`·`*.key`·数据根 `config/**`)+ 预算(源 50MiB/预览 1MiB/4000 块/表格 200×60/资产 20×20MiB/zip 单 part 50MiB·累计 200MiB·膨胀比 1000× 炸弹三重封顶/抽取 10s;插件 data 可部分覆盖)+ 内存 LRU 缓存(path+size+mtime+参数签名,32 条/64MiB,**默认不落盘**)+ 格式探测(仅扩展名;未知才读 512B 判 `%PDF-` 魔数,再按 UTF-8 合法性分 text/binary;`.doc/.xls/.ppt`/归档/音视频显式 unsupported)+ 块模型→行号化 Markdown 渲染(四端共用:标题/列表/引用/围栏/表格/图片/分页,单行与输出字节双封顶,offset/limit 分页,`DocText.PDF` 透传页事实)。③ **K1 依赖落地**:引入 `github.com/Detective-XH/gopdf v0.8.7`(BSD-3-Clause,纯 Go/无 CGO)并接头**PDF 页事实抽取器**(`DocumentSummary` → `Kind` text/scanned/empty/mixed、`pages_needing_ocr`、`Info` 标题作者、`Warnings` 透传(50 条上限)、>1000 页跳过逐页分类显式警告、加密 PDF 结构化「需口令」、损坏 PDF 结构化解析错误)——**实测 `go mod` 变化仅 `golang.org/x/text v0.23.0 → v0.37.0`,传递依赖净增 0(符合 K1 核实结论)**。④ **基线抽取器**:text/code(围栏 + 语言提示 + 截断标记)、binary(信息卡 + 4KiB hexdump + MIME/魔数)、image(stdlib 解码器探尺寸 + 资产元数据)、unsupported(结构化拒绝 + E2 建议);markdown/csv/notebook/docx/xlsx/pptx/html 未交付时**显式 Warnings + 切片归属**,不静默降级。⑤ **登记**:catalogue(`host-docview`,bundle=base)+ plugins/README.md + `config/bundle-base.yaml` 与 `internal/embed/seed/bundle-base.yaml` 双份同步 **seed-version 13 → 14**(带 data 预算默认值)。⑥ **测试**:42 项同包单测(探测表驱动 24 例、逃逸矩阵 19 例含 symlink/`../`/deny-list/空/NUL/目录/不存在、zip 炸弹与累计预扣、LRU 容量与参数隔离、文本/二进制/图片/不支持/未交付格式、预算拒绝与 Truncated、缓存命中与失效、Raw/Detect/资产 round-trip、渲染 6 类块 + 分页 + 单行截断、PDF 文本/扫描/损坏/加密/分类表驱动、data 预算覆盖);全库 `go test ./... -race -count=1` 绿(43 包)。
> ✅ **D1 文本族全链路(2026-10-11)已交付**:① **文本族抽取器**:markdown(goldmark CommonMark+GFM 表格/删除线/任务列表 → 块模型:标题/段落/富文本行内/列表含嵌套深度/引用/围栏代码 + 语言/分隔线/表格(含对齐)/HTML 块与行内**转义为纯文本**不进退任何 HTML 通道/图片相对路径→资产、远程图**不自动加载**占位 + 警告、`javascript:`/`data:` 链接丢弃并告警)、CSV/TSV(定界符在 `, ; \t |` 中按行一致性打分,**非格式嗅探**;行 200/列 60/单元格 200 字符三重封顶 + Truncated)、notebook(.ipynb:markdown 单元走 md 块、code 单元代码块、文本输出块、错误 traceback、二进制输出只给类型说明)。② **Web 端点 6 条**(`/api/doc/preview|raw|asset|tree|html` + `POST /api/doc/render`):**懒解析 ctx.doc**(对齐 policy-guard 时序坑:ui-web-app 与 host-docview 无拓扑依赖,启动期一次性 Inject 会恒 nil;改为请求期现取 + 缓存,未装配 503 而非 404)、strict 路径策略、`http.ServeContent` Range(浏览器 PDF 查看器翻页必需)、`X-Content-Type-Options: nosniff`、**`text/html`/`svg`/`octet-stream` 永不 inline**(一律 attachment,下载名经 `mime.FormatMediaType` 转义)、资产 MIME 白名单(仅 `image/*` 且排除 svg,否则 415)、HTML 预览独立 CSP 路由(`default-src 'none'`)、错误映射 400/403/404/413/415/422/503、render 请求体上限 256KiB(会话流 md 渲染**服务端解析**,前端零 markdown 依赖、零 v-html)。③ **前端**(web-src,依赖仍仅 vue):`doc/DocBlocks.vue`(块 → Vue 组件,全插值转义,链接仅 http(s)/相对 + `rel="noopener noreferrer"`)、`doc/DocPanel.vue`(工作台:左文件树懒展开/过滤 + 右预览:markdown 块、PDF 原生 iframe + 下载兜底(含 Linux WebKitGTK 说明,K2)、HTML `sandbox=""` iframe、图片;截断/警告提示条;工作区切换**整树重置**;Esc capture 优先于清空输入)、`doc/DocText.vue`(会话流 assistant 文本 md 渲染,失败/503 自动回退纯文本;流式两档防抖)、`docstore.ts`(工具行「预览」按钮 → 事件打开面板并定位,可预览扩展名白名单)、`main.ts` 启动探测 `/api/doc/tree` 决定是否注册内置 extra-panel(priority 0,外部 UI 插件同 key 覆盖)、StreamView 工具行「预览」动作 + assistant 走 DocText。④ **TUI pager**(`tui/docview.go`,独立组件**不动会话流渲染路径**——既有 `markdown_test.go` 全绿作护栏):块摊平(表格定宽 ASCII + CJK runewidth 对齐 + 超宽截断)、↑/↓/PgUp/PgDn/Home/End/g/G 滚动、←/→ 水平偏移、`/` 搜索输入态 + 命中高亮 + n/N 跳转、q/Esc 关闭、状态行(行号/截断/警告让位操作提示)、滚轮路由到 pager;`State.Doc` 模态 + `Render` 早退 + `handleKey` 最优先分支;`App.OpenDoc` 经 `tea.Program.Send` 投递。⑤ **命令与事件联动**:`host-docview` 注册 `/preview <路径>`(先本地校验格式与可达性,**失败意图不广播**),成功后 `Emit(sdk.EventDocOpen)`;TUI 订阅 `doc/open` 自动弹 pager(模型 `doc_open` 工具与 Web 侧联动归 D5)。⑥ **`gah doc` CLI**(§6.3 契约):`gah doc <path> [--json|--md|--text|--tree] [--page|--sheet|--max-bytes|--max-input-bytes|--lines|--depth]`,退出码 **0 成功 / 1 其它 / 2 用法 / 3 不支持格式 / 4 超预算 / 5 解析失败**,纯读零装配(直接构造 host-docview,不触碰数据根);`sdk.DocRequest` 增 `MaxInputBytes` 覆写(omitempty 兼容)。⑦ **登记/文档**:catalogue `host-docview` 增 `Requires: ctx.commands`(命令注册顺序)、README 双语(功能表 + `/preview` 命令 + `gah doc` CLI + Web 面板与 `/api/doc/*` REST)、docs/VERIFY.md 新增 D 组清单。⑧ **测试**:新增 host-docview 文本族/web doc 端点/TUI pager/CLI 退出码/tests e2e(doc/open 事件)共 ~120 项断言;全库 `go test ./... -race -count=1` 绿;`vue-tsc --noEmit` 0 错 + `vite build` 产物落 `web/dist`。
> ✅ **D2 docx 抽取器(2026-10-11)已交付**:① **OOXML 通用层**(`plugins/host/host-docview/ooxml.go`,自研,零第三方依赖):zip 容器打开 + `[Content_Types].xml` 部件类型判定(Override 优先 / 扩展名 Default 回退)+ 关系图(rels)惰性解析缓存(包根 `_rels/.rels` 与部件级 rels,Target 归一为容器内路径,External 保 URL)+ 主部件定位(officeDocument 关系优先,固定候选回退)+ **全容器声明尺寸预检**(单 part / 膨胀比;累计额度随读取扣减,不信任声明值)+ 容器内图片尺寸探测(`image.DecodeConfig`)。② **docx 抽取器**(`extract_docx.go`,`word/document.xml` **流式 token 解析,不整体 Unmarshal**):标题(`styles.xml` outlineLvl + basedOn 一层继承 + 行内 outlineLvl + `Heading N`/`标题 N` 命名回退,级别 1–6 裁剪)、富文本运行(b/i/strike/Verbatim;区分运行 rPr 与段落标记 rPr,**运行结束才复位格式**)、超链接(rels 解析 + `safeLink` 过滤 javascript:/data:)、换行/制表(`w:br`/`w:cr`/`w:tab`)、列表(`w:numPr` 行内 + 样式表 numPr 继承,ilvl 层级)、表格(`w:gridSpan`→ColSpan(表头按跨度展开空列占位)、`w:vMerge`→RowSpan(**按 (锚行,列) 记账、行落定后回填**,规避值拷贝指针失效;语义 restart = 起始、缺省/continue = 延续)、表头判定(`w:tblHeader` 或整行加粗启发)、行列单元格三重封顶 + Truncated)、内嵌图片(`a:blip@r:embed` / `v:imagedata@r:id` → rels → `word/media/*` → DocAsset 真图 + 尺寸 + 预算封顶占位)、元信息(`docProps/core.xml` 标题/作者)、**嵌套表格整体跳过 + warning**、公式(`m:oMath` 纯文本兜底 + warning)、**页眉/页脚/脚注/尾注/批注显式 Warnings**(不静默丢弃)。③ **测试**:夹具在测试内构造真实 OOXML 容器(确定性 zip,无二进制入库):标题层级 / 富文本 / 链接安全 / 换行 / 列表两级 / 表头跨列 / 纵向合并 RowSpan=2 / 数值标记 / 图片资产尺寸 + 资产端点取回 / 公式与页眉脚注警告 / 嵌套表格跳过 / 行列单元格封顶 Truncated / 坏容器与缺主部件结构化错误 / 空文档占位 / zip 爆炸拒绝;全库 `-race` 绿。
> ✅ **D3 xlsx + pptx 抽取器(2026-10-11)已交付**(共用 D2 的 OOXML 通用层):① **xlsx**(`extract_xlsx.go`,worksheet 流式解析):工作表清单(名/顺序/隐藏,`workbook.xml` + workbook rels;`sheet` 块标注当前表,多表显式提示 + Web 面板工作表标签切换)、**共享串惰性建索引**(第一遍只收集被引用的下标,第二遍按需物化 → 百万串不全量常驻;`<si>` 内多 `<r>` 分段正确拼合)、单元格类型(`s` 共享 / `inlineStr` / `str` 公式串结果 / `b` 布尔 / 数值)、**公式只取缓存值 `<v>`**(不重算)、数字格式(`styles.xml` cellXfs → numFmtId;内建日期表 14–22/27–36/45–47/50–58 + 自定义格式码扫描(去引号字面量与 `[]` 段后判 y/m/d/h/s,规避 `"yyyy"0` 误判)→ Excel 序列号转 ISO 时间(**含 1900 假闰日修正**);百分比 ×100 + `%`;普通数值去尾零)、合并单元格(`mergeCells` → ColSpan/RowSpan,covered 单元格清空;**行号映射 rowMap 保证空行折叠后合并区域仍指向正确行**)、**稀疏空行折叠**(连续 > 3 空行折叠为一行「⋯ 省略 N 个空行」,保持单表网格而非拆多表)、行列单元格三重封顶 + Truncated(10 万行稀疏声明样例验证:50 行封顶 + 折叠提示,解析不拖垮)。② **pptx**(`extract_pptx.go`):幻灯片顺序**以 `p:sldIdLst` 为准**(不信文件名排序;`sldId` 同带 `id` 与 `r:id`,命名空间区分手工 token 解析)、每张 `slide` 块 + 文本框段落(`a:pPr@lvl` 层级;标题占位 `type=title|ctrTitle` → `heading` 块 + `DocView.Title`)、表格 `a:tbl` → table(列封顶 + 数值标记)、图片(`p:pic`/`a:blip@r:embed` → rels → media → DocAsset 真图 + 尺寸)、**标题占位继承**仅做标题(slide → slideLayout → slideMaster,正文继承不做 + 显式提示)、备注页存在即显式 warning(默认不解析)、块/表格/资产三重预算封顶。③ **前端**:Web 文档面板新增 **xlsx 工作表标签**(多表切换 + 隐藏表标注 + 行×列 tooltip)。④ **测试**:夹具在测试内构造真实 OOXML 容器:共享串(含 r 分段)/内联串/布尔/日期/百分比/公式缓存值/数值格式表驱动(含假闰日 60)/合并 ColSpan/稀疏空行折叠/--sheet 切换与越界回退/行列单元格封顶/10 万行稀疏预算;pptx 的 sldIdLst 顺序/标题与层级/表格/图片资产取回/备注页 warning/标题占位继承/块预算/坏容器与空 sldIdLst 结构化错误;全库 `-race` 绿,`vue-tsc` 0 错。
> ✅ **D4 PDF 接线(2026-10-11)已交付**(K1/K2 均已锁):① **文本/块**:逐页 `Page.Blocks()`(**列优先阅读序**,多栏文档免自研排版近似)→ paragraph 块,页标记块 `page` 分隔;块解析失败回退 `GetPlainText` 整页纯文本(按空行分段);**标题保守启发**(块字号 ≥ 本页主导字号 ×1.35 + 行数 ≤2 + 文本 ≤80 字 → heading;主导字号按行数加权众数、同票取小,宁少判不误判;PDF 无版式语义时不猜级别,统一 h2)。② **表格**:`Page.Tables()` 仅取框线类 → table 块(首行作表头、行列单元格三重封顶),**低置信度显式 warning**(「可能误判/漏线,请以原始 PDF 为准」);与表格单元格重复的文本不再以段落重复输出。③ **图片元数据**:`Page.Images()`(不解码)→ 单页 note 块给张数 + 绘制尺寸 + 声明像素 + 滤镜;**真图由浏览器原生查看器呈现**(与 docx/pptx 导出真图不同,是有意取舍)。④ **页事实与页码标签**:`DocumentSummary()` → `Kind`(text/scanned/empty/mixed)+ `pages_needing_ocr`;`PageLabels()` → `Meta["page_labels"]`(≤64 页);请求页集支持 `Page` 起始页与显式 `Pages`(显式优先);块预算封顶 → Truncated + 「用 --page 指定起始页」提示。⑤ **加密**:`NewReaderEncrypted` + 口令回调,口令取 `GAH_PDF_PASSWORD`(env,**不落盘不入日志**);缺口令 → 结构化 unsupported(明确「需口令」并以本地查看器另存为副本的建议),不静默失败。⑥ **Warnings 透传**:`Reader.Warnings()`(上限 50 条,超额计数提示)+ 超时(逐页检查 ctx,超时给「已完成前 N 页」)。⑦ **保真接线(K2)**:Web `/api/doc/raw`(Range + `ServeContent`)+ DocPanel `<iframe>` 浏览器原生查看器(翻页/缩放/搜索);**下载入口恒在**(`?dl=1`)且面板显式说明 Linux 桌面壳 WebKitGTK 不支持内嵌 PDF → 用下载本地查看——**不引入 pdf.js**(前端依赖零新增);不做 UA 嗅探式能力探测(不可靠),常驻下载入口严格优于探测降级。⑧ **测试**:测试内构造确定性 PDF(带正确 xref 偏移):多页文本(页标记 + 行号化文本含页标记)/起始页与显式页集/标题启发(24pt 标题 vs 10pt 正文)/全框线表格重建(A1/B1 表头 + 不重复输出)/图片元数据(scanned 分类)/块预算封顶/加密缺口令结构化错误/MaxInputBytes 超预算;全库 `-race` 绿。
> ✅ **D5 模型工具与三端联动(2026-10-11)已交付**:① **新插件 `plugins/tool/tool-doc`**(bundle=base,**Requires `ctx.tools` + `ctx.doc`**(缺失即显式失败,不静默降级);三工具:`read_document`(→ 行号化 Markdown `{path,format,offset,lines[{number,text}],totalLines,truncatedByBytes,pdf?,warnings[],note?}`,预算默认集对齐 dsh-document:`readLimit 2000` / `maxOutputBytes 50KiB` / `maxInputBytes 50MiB` / `pdfMaxPages 100`,并可经 manifest data 覆盖;`offset/limit` 分页、`pages[]` 页集、`sheet` 工作表)、`doc_open`(先 `Detect` 校验格式与可达性 → **失败意图不广播** → `Emit(doc/open)`;返回「已在界面打开」+ 不要复读全文的指令)、`doc_list`(目录树 + 格式 + `previewable` 标注;`path` 缺省 = 当前工作区根)。② **`doc/open` 三端联动**(单一事件源,各端 presenter):**TUI**(D1 已接:自动弹 pager 浮层)、**Web**(本切片新增:`EventHub` 订阅 `doc/open` → 新 SSE 帧类别 `doc`(载荷 `sdk.DocOpenEvent`)→ 前端 `transport.on('doc')` → `onOpenDoc` 打开文档面板并定位文件;**REST/命令/模型工具三条入口汇聚同一事件**)。
>
> 🗑️ **IM 线整体放弃(2026-11-14)**:用户决策放弃全部 IM / 远程控制通道能力——微信 iLink(`ilink/`)、QQ Bot v2(`qqbot/`)、飞书自建应用(`feishu/`)三条通道,连同共享运行时 `im/`、插件 `plugins/ui/ui-im-wechat|ui-im-qq|ui-im-feishu` 与 `plugins/tool/tool-im`、bundle/profile 样板(`bundles/im-*`、`config/bundle-im-*.yaml`、`config/profile-im-*.yaml` 及 seed 同名副本)、SDK 契约(`sdk.IM*`)、Web 端点(`/api/im/*`)、命令面(`/im` `/wechat` `/qq` `/feishu`、`gah im*`)与首启引导(`FirstRunGuide`、`/api/guides`、`prefs.DismissedGuides`)**全部删除**;规划正文 docs/IM_REMOTE.md / docs/IM_CONNECT_PLAN.md / docs/IM_FEISHU_TEST.md 同删。本行取代此前的 E 组 / IM 各交付行(历史见 git 历史与 docs/(已删))。
>
> **会话与工作区体验线(F 组 F0–F4,规划正文 docs/SESSION_UX_PLAN.md;F0–F4 已交付 ✅ 2026-10-11;无剩余项)**——① **选中态重做**(根因:双层信号打架/选中与 hover 区分不足/工作区与会话共用 `.cur` 语义混同;方案:语义 token `--sel-bg`·`--sel-rail` + 圆角左边条替代 inset 阴影 + 语义分离 + `aria-current`);② **会话置顶/取消置顶**(`$GAH_HOME/sessions/meta.json` 与 `names.json` 双写兼容 + `SetPinned` + `action: pin|unpin` + 三端 ★);③ **会话概述(LLM 总结)**(新插件 `host-session-summary` 提供 `ctx.sessionSummary`,复用 ctx.llm 生成 标题+一句话概述+主题词,落 meta.json 缓存,全局单飞/每会话 ≥60s 节流/输入 8KB 预算,降级链 summary→name→Preview,生成前过脱敏并首次提示)。**已锁决策 K3:概述「回合后自动生成」默认开**(`data.auto_summary`;列表请求绝不触发模型,批量预热默认关)。
> ✅ **F 组 会话与工作区体验(2026-10-11)已交付 F0–F4**:① **F0 会话元数据升级**:新增 `$GAH_HOME/sessions/meta.json`(`{name,pinned,pinned_at,summary{text,topics,covered_frames,model,ts,input_hash}}`)+ **与 `names.json` 双写兼容**(读以 meta 为准、缺失回退 names;Rename/SetName 双写;删除会话连同元数据条目一并移除不留残留)+ 坏文件容忍 + **0600 原子写**(CreateTemp + rename);`sdk.SessionInfo` 扩展 `Pinned/PinnedAt/Summary/SummaryTopics/SummaryCoveredFrames/SummaryState`(ready/stale/missing/unavailable,全部 omitempty 向后兼容)+ **排序单一实现**(`sortSessionsByPinned`:置顶区 pinned_at 倒序 → 主会话 → 其余 mtime 倒序;三端共用)。② **F2 置顶**:`sdk.CwdSessions.SetPinned`(幂等;**上限 8 显式报错**不静默丢弃;不存在会话报错)+ `SetName`/`SetSummary`(按 id 定位,补 Rename 只作用于当前会话的缺口);Web `POST /api/sessions {action: pin|unpin}`(**不新增端点**,回最新列表)+ TUI `/session pin|unpin [id]`(二级选择器复用会话枚举)+ IM `/sessionlist` ★(当前 `*` 与置顶 `★` 并列)。③ **F1 选中态重做**(`style.css` 语义 token + `Sidebar.vue`):新增 `--sel-bg`/`--sel-rail`/`--sel-border`/`--hover-bg`(sel 比 hover 深一档,拉开两档对比);会话当前项 = **卡片(填充 + 1px 描边 + 8px 圆角)+ 圆角左边条(`::before` 3px,替代不跟圆角的 inset 阴影)+ 名称 600 字重**;**工作区当前项改为身份标识**(竖条 + 强调色 + 圆点,**不铺底**)——与会话选中语义分离,同屏两个"当前"不再混同;操作图标**去掉选中行常显**(仅 hover/focus 出现),★ 作为状态常显;`aria-current`(会话 `true` / 工作区 `location`)。④ **F3 会话概述**(新插件 `plugins/host/host-session-summary`,bundle=base,Provides `ctx.sessionSummary`,Requires `ctx.cwdSessions`,ctx.llm 可选):输入 = **首 2 轮 + 最近 6 轮**(工具调用只保留工具名,不喂参数/结果),总预算 8KB(超限按字节在 rune 边界截断并显式标注省略条数),**先过脱敏**(`sk-`/`Bearer `/`api_key=`/`apiKey:`/`*_secret`/`AppSecret`/`password`/`token=` → 保留键名、值替 `[REDACTED]`;线性扫描零回溯,修掉了替换标记自身再匹配导致的死循环)→ 严格 JSON(容忍代码围栏/前后杂文;title ≤20/概述 ≤60/topics ≤3 各 ≤8 字)**解析失败重试 1 次**再失败即显式报错;缓存落 meta.json(covered_frames 驱动 ready/stale),**列表请求绝不触发模型**;全局单飞 + 每会话 ≥600s 节流 + 短会话(用户轮 <2)跳过 + 24 帧落后才自动刷新;**K3 自动档默认开**(订阅 `turn/end` 后台异步,失败不打扰);标题仅在会话未命名时回填(不覆盖用户起的名字);TUI `/session summary`、Web `POST /api/sessions/summary`(422 结构化文案)+ 列表第二行概述(降级链 summary → preview → （空会话）)与「待更新」标记、hover ⟳ 生成(二次确认提示"会把会话内容发送给当前模型")。⑤ **F4 三端收口**:Web 侧栏行含概述与帧数、TUI `/session list` 带 ★ 与概述行、IM `/sessionlist` 概述优先;README 双语同步(`/session pin|unpin|summary` 命令、侧栏描述、REST 行)。⑥ **登记**:catalogue + plugins/README.md + config 与 seed 双份 bundle `# seed-version 15 → 16`(host-session-summary 与其实例参数)+ tests 卸载矩阵按依赖序补 `host-session-summary`/`tool-doc`/`host-docview`。⑦ **测试**:host-cwd-sessions 新增元数据单测(双写/迁移三态/置顶幂等与上限/删除无残留/概述 ready-stale-missing/0600/排序表驱动);host-session-summary 新增(输入裁剪与脱敏/字节预算/JSON 解析正反例与截断/生成与缓存命中不调模型/force/已命名不回填/模型缺失与轮次不足与模型报错与重试计数/单飞/自动档节流与阈值/data 覆盖);全库 `go test ./... -race -count=1` 绿,`vue-tsc` 0 错 + `vite build`。
>
> ✅ **G 组收口 · G-D6-4 HTML 预览收口(2026-10-11 已交付)**:① **服务端源码视图**(新 `plugins/host/host-docview/extract_html.go`):HTML 族(.html/.htm/.xhtml)不再走 `pendingView`;输出 `<title>`(stdlib `html.UnescapeString` 实体解码 + 空白折叠 + 200 字上限截断)+ **单 `code` 块(Lang=html,源码原样、换行归一化、非 UTF-8 替 U+FFFD、预算截断 + Truncated)**——块模型本身无 HTML 通道(XSS 由结构消除);含 `<script`/`onerror`/`onload` 时**显式告警**「沙箱预览不执行(仅源码可见)」;`Meta.mime=text/html`;**`pending` 表清零**(全部声明格式均有抽取器)。② **Web 门控**(`DocPanel.vue`):HTML 默认**源码视图**(DocBlocks 渲染),「沙箱预览」按钮显式点击才挂载 `sandbox=""` iframe(`/api/doc/html` 独立 CSP 路由不变);切文件/切工作区/外部预览意图一律重置回源码档;`.dp-mode` tab 语义(`role=tab`/`aria-selected`)。③ **测试**:`extract_html_test.go`(标题提取表驱动含无闭合/大小写/实体/长标题截断、源码原样、脚本告警、预算截断、行号化文本);`service_test.go` 的 pending 用例替换为**收口守护栏**(pending 为空 + 全格式抽取器齐备 + HTML 走源码视图)。④ **同步**:README 双语(功能表 + 文档预览面板描述)、docs/TODO_OVERVIEW.md G-D6-4 → ✅、docs/DOC_PREVIEW_PLAN.md §9 D6-4 收口。全库 `go test ./... -race -count=1` 绿 + `vue-tsc --noEmit` 0 错 + `vite build`。
>
> ✅ **G 组收口 · G-E5-4 question 完整事件化(2026-10-11 已交付)**:① **广播面补齐到全部 profile**:新增 `sdk.ObservedQuestion(c, channel, inner)`(装饰器:补 `Question.ID` → 广播 `question/requested` → 转发 Ask → 广播 `question/resolved`;`RegisterQuestioner` 透传;nil Ctx/inner 原样回落)与 `sdk.NewQuestionID()`;两个单 UI profile 的 provider 均包装(web/tui,channel 分别为 `web`/`tui`)——此前只有装配 host-confirm-fusion 的多端 profile 才有事件(单 profile 完全无广播)。② **事件可关联/可判自**:`QuestionEvent`/`ConfirmEvent` 新增 `Channel`(请求/作答渠道),Fusion 按 presenter 名追踪**胜出渠道**(单渠道与竞速两路分支均记录)并补齐空 `Question.ID`;`web.QuestionService.PresentQuestion` 优先采用调用方 `q.ID`(弹层 id = 事件 id)。**顺带修复历史缺口**:单 web profile 曾 `Provide *QuestionService`(仅 QuestionPresenter 不实现 Ask)→ 工具侧 Inject 类型不符,`ask_user_question` 实际不可用;新增 `QuestionService.SingleChannel()` 适配器修复。③ **各端订阅(事件观察面)**:`sdk.QuestionEventOf/ConfirmEventOf`(值/指针兼容)与 `sdk.AnswerSummary`;web `EventHub` 新增帧 **`questiondone`/`confirmdone`**(载荷 `*QuestionDone`/`*ConfirmDone`)+ 前端按 id/prompt 关闭**已由其它渠道处理**的遗留弹层(此前 fusion 下本端弹层会悬挂,作答打到已删 id 被静默忽略);TUI `App.NoteInteraction`(新 `interactionMsg` → 会话流 meta 行)+ ui-tui-app 订阅(Channel 非 `tui` 才提示)。④ **测试**:`sdk/interaction_test.go`(装饰器广播/ID 补齐与显式 id 不覆盖/错误透传/nil 回落/访问器/摘要/ID 生成);fusion(Channel 追踪 + ID 补齐 + 显式 id);web(`questiondone`/`confirmdone` 帧 + 无关载荷不产帧 + SingleChannel 适配与 id 沿用);im(外部作答回推/取消文案/本渠道与空渠道静默/confirm 批准与拒绝/撤销后不再回推/无活跃会话);tui(审计行入流、空文本与未启动忽略)。全库 `go test ./... -race -count=1` 绿 + `vue-tsc` 0 错 + `vite build`。
>
> ✅ **G 组收口 · G-D6-2 外部转换器探测(2026-10-11 已交付)**:① **探测与门控**(新 `plugins/host/host-docview/converter.go`):PATH 探测 `soffice`/`libreoffice` 与 `pdftoppm`(探测不影响启用);**默认关闭**,仅 `data.external_converters: true`(或 `Options.ExternalConverters`/CLI `--convert`)启用——基线不引入部署面。② **转换与接入**:旧二进制 Office(.doc/.xls/.ppt,CFB 头)在启用且有 soffice 时 `--headless --norestore --convert-to pdf --outdir <tmp>` 转换 → 走 **D4 PDF 抽取器**(文本/表格/页事实);视图身份仍归源文件(Path/Name/Size/ModTime,`Format` 保持 unsupported 以防前端拿源字节当 PDF 渲染),`Meta.preview_via=external-converter` + 显式 Warnings;转换产物**注册为文档资产**(`RegisterFileAsset`,`application/pdf`)→ Web 面板用**原生查看器 iframe** 呈现高保真版式(下载仍给原始文件)。③ **便携纪律与清理**:唯一落盘点 `$GAH_HOME/cache/doc/`(`cacheDirPath()` helper;GAH_HOME 空仅嵌入/单测退 TempDir),产物名 = 源路径+size+mtime 的 sha1(同源复用,不重转),**7 天按龄清理**;超时 30s、产物体积上限 200MiB、失败带 stderr 尾巴(显式回退为「不支持 + 原因」,不假装成功)。④ **提示链**:未启用但已装 soffice → 提示 `data.external_converters`;未装 → 提示安装 LibreOffice;非旧 Office 格式不出现转换器文案。⑤ **CLI**:`gah doc --convert`(默认零写盘不变);`sdk.DocText` 增 `Meta` 透传,CLI 据 `preview_via` 判定退出码(**转换成功 0**,不可用仍 3)。⑥ **端点**:资产 MIME 白名单扩为「图片(排除 svg)+ `application/pdf`」(与 raw 内联查看同风险等级,仍 `nosniff`)。⑦ **测试**:converter(转换成功链路与身份回写/资产取回/同源缓存复用、未启用与未安装提示、非 Office 不提示、转换失败与坏产物显式回退、缓存名安全与 mtime 失效、按龄裁剪、探测描述、不可用报错);web(PDF 资产 200 + nosniff、svg 仍 415);cmd/gah(`--convert` 解析与退出码 3/0 语义);seed 样板双份注释 `external_converters`(默认关闭)。全库 `go test ./... -race -count=1` 绿 + `vue-tsc` 0 错 + `vite build`。
>
> 📌 **未实施清单(可选增强 / 待人工 / 外部条件;2026-10-11 登记,D/F 主体交付后的收口;G 组已收口 D6-4/E5-4/D6-2)**:D/F 两组主体已交付 ✅;下列各项**不进入当前迭代**(按需可选档、或卡人工真机、或卡外部条件),逐项登记「现有 hook → 缺什么 → 量级 → 触发/阻塞」以便随时开工。任务表见 docs/TODO_OVERVIEW.md「G 组」,细则见 docs/DOC_PREVIEW_PLAN.md §9 D6。
>
> | 编号 | 项 | 现有 hook(已就绪的落点) | 缺什么 | 量级 | 触发 / 阻塞 |
> |---|---|---|---|---|---|
> | **D6-1(E1)** | pdfium-on-WASM 光栅 | ✅ **主力路径已由 RST-1 交付**(外部 pdftoppm:零体积、契约 `DocRasterService` 已就位);余下仅 **SELF-1 自包含档**(⏸ 暂存)(**有网实测已完成 2026-10-11**:功能可行、与 poppler 仅差抗锯齿;**+cost≈5.5 MiB 破当前体积门 + `invoke_*` 无法忠实实现**——见下方「SELF-1 前置实测」) | ~~有网实测~~(✅ 已完成);余阻塞 = 降体路径/体积门决策 | M–L | 📊 **评测见上**;**TUI 位图明确不做**(无图形协议) | **评测(见下方「G 组剩余项评测分析」)**:被依赖阻塞 + 破体积门;TUI 无图形协议支持 → TUI 位图**新登记不做**;建议仅在 E-C 完成后评估按需档
> | ~~**D6-2(E2)**~~ ✅ | 外部转换器探测 | ✅ **已交付 2026-10-11**:`converter.go`(PATH 探测 soffice/libreoffice/pdftoppm;**默认关闭** `data.external_converters`)+ `--headless --convert-to pdf` → D4 PDF 抽取 + 产物落 `$GAH_HOME/cache/doc/`(sha1 复用名,7 天按龄清理,30s 超时,200MiB 上限)+ Web 原生查看器(资产端点放行 `application/pdf`)+ 失败显式回退 + `gah doc --convert` | — | ✅ 已收口 |
> | **D6-3(E3)** | 引入 `excelize` 替代自研 xlsx | ✅ **判定已定(2026-10-11,见下方「D6-3 判定实测」)**:高级特性语料跑出 content=5(**批注/回复式批注/文本框/内嵌图/图表/透视**);结论 = **不引入 excelize**,改为零依赖窄修(✅ **DOC-2 已交付 2026-10-11**,复验 content_gaps=0) | ~~仅当出现 content 级缺口才评估~~(✅ 已出现);引入前须有网实测依赖与体积 | M | ✅ **判定完成:不引入**(见下方实测);✅ **窄修 DOC-2 已交付**(content_gaps=0) | 📊 **评测(2026-10-11 实测定论)**:触发条件**成立**(高级特性语料 content=5);但图表/透视属**视觉性损失而数据仍在单元格**(接受),真丢文本者=批注/回复式批注/文本框,可零依赖修;excelize 不渲染图表且需新增 x/crypto·x/image(体积门冲突)
> | ~~**D6-4**~~ ✅ | HTML 预览收口 | ① **HTML 源码视图块模型已交付**(`extract_html.go`:`<title>` 提取(实体解码/空白折叠/200 字上限)+ 单 `code` 块(零 HTML 通道)+ 含 `<script>`/内联事件时显式告警;`pending` 表清零)② **门控已交付**(DocPanel 默认源码,「沙箱预览」按钮显式点击才挂载 `sandbox=""` iframe;切文件/切工作区重置回源码) | — | ✅ 2026-10-11 已交付 |
> | ~~**E5-4**~~ ✅ | question 完整 dsh 事件化 | ✅ **已交付 2026-10-11**:广播面补齐到全部 profile(`sdk.ObservedQuestion` + `NewQuestionID`,单 UI profile 亦广播)+ 事件 `Channel`/`ID` 可关联(Fusion 追踪胜出渠道)+ `sdk.QuestionEventOf/ConfirmEventOf/AnswerSummary` + 各端订阅:web `questiondone`/`confirmdone` 帧与前端关闭遗留弹层、TUI `NoteInteraction` 审计行;**顺带修复单 web profile `ctx.question` 类型不符(ask_user_question 不可用)** | — | ✅ 已收口 |
> | ~~**C2**~~ ✅ | mac 签名公证 + win NSIS + CI 矩阵 | 零成本方案:`scripts/publish-desktop.sh` + `.github/workflows/release-desktop.yml` + `.github/workflows/release-cli.yml`(公开 repo 免费 runner,不购证书) | ✅ **2026-09-12 已发布 v0.1.0**(两个 workflow 均绿;详见 §14.1 NOND-W2 ⑨) | L | 已达成(repo 已公开 + tag `v0.1.0`) | 📊 **评测**:工程侧齐备,纯外部条件(repo 公开 + tag + secret;矩阵缺 mac x86_64);下载体积 ~27 MiB/平台
> | ~~**C3**~~ ✅ | updater 更新通道 | **已接入且已实证**:`tauri-plugin-updater` + ed25519 自持签名(`~/.tauri/gah.key`)+ GitHub Releases 端点 + 托盘「检查更新」 | ✅ **2026-09-12 已进 Release**:`latest.json` 双平台带签名;`releases/latest/download/latest.json` 实测 HTTP 200 | M | 已达成 | 📊 **评测**:同上(updater 端点指向 GitHub Releases,repo 不公开则客户端无法未鉴权读取)
> | **X-2** | 三端真机核对(人工) | VERIFY 清单已列 | D 组(三平台 PDF 查看器 / Linux K2 下载降级 / 工作区切换整树重置 / 中英混排宽表)、F 组(选中态视觉 / 置顶三端一致 / 概述中文宽度截断)、PDF 口令 | 人工 | **卡真机环境** | 📊 **评测**:交互类断言可借 **E-E(vitest,dev-only)** 部分自动化;视觉/平台类仍需人工
>
> **明确不做(已锁决策,不再评估)**:`pdf.js`(前端依赖 + v-html 边界;K2 已锁 Linux 降级为下载)、`unidoc/unipdf`(商业/AGPL)、`go-fitz`(CGO,违反 `CGO_ENABLED=0`)、`pdfcpu`(体量且非文本抽取导向)、`mgilbir/spine` 整体引入(仅作参考实现与夹具来源)、OCR 引擎(只做识别 + 提示)、预览内编辑与外部编辑器拉起、iWork/CAD/音视频/压缩包、`.doc/.xls/.ppt` CFB 解析(仅 E2 可选转换)、**PDF 内嵌图导出**(gopdf 不解码;真图归 Web 原生查看器)、S3.1 scrollback 双形态(方案 A)、全量键位自定义。
>
> ✅ **G 组开工前置件 E-A / E-C / E-D / E-E(2026-10-11 已交付)**:① **E-A 工具级审批**(`plugins/policy/policy-guard`):`ApprovalPolicy.tools` + `RequiresToolApproval/ApprovalTools/checkTool`(参数摘要 120 字)+ `decide()` 统一三档语义(命令支路与工具支路共用,危险命令提示文案不变),`parseApprovalTools` 容错列表/逗号字符串,pre-execute 单一裁决点内 shell 危险模式与工具名单**相互独立**;`data.approval_tools` 默认空 = 行为零变化(seed 双份注释);测试 6 组(smart 批准/拒绝、strict 直拒、open 放行、运行期切档、无确认通道安全拒绝、未列出/空名单零影响、shell 入名单、解析表驱动、参数摘要边界)。② **E-C 体积门重定基与护栏**:五目标实测揭示旧门「<40 MiB」已在 darwin/amd64·windows/amd64·linux/amd64·darwin/arm64 **4/5 失败**(40.59–43.63 MiB);新阈值 **二进制 ≤46 MiB / gz 产物 ≤30 MiB**,单一事实源 `scripts/size-check.sh`(本地 `--all` 或单目标;打印「二进制/embed/基线/gz」四列 + embed 明细 TOP 归因;`GAH_SIZE_MAX_BIN_MIB|GAH_SIZE_MAX_GZ_MIB` 可覆盖;超门非零退出);CI 去掉内联阈值改调脚本;降体路径(外部插件协议去 gRPC 化 ≈-14 MiB/插件、extplugins 附包化 ≈-20 MiB)登记在脚本头与 §14.1,作为 D6-1/D6-3 需空间时的前置动作。③ **E-D 真实文档保真语料 + harness**:`scripts/gen-doc-corpus.sh` 用**本机第三方生产者**离线造语料(textutil→docx/odt、cupsfilter→PDF、系统真实 PDF 拷贝,额外真实文件/目录可作参数传入;默认落 `$GAH_HOME/cache/doc-corpus`);`plugins/host/host-docview/fidelity_test.go` 为 **opt-in**(`GAH_DOC_CORPUS`)——逐文件打印 `REPORT file/format/kind/blocks/kinds/chars/warnings/truncated` + 硬性不变量(不 panic、结构化结果、Office/文本族不得空视图、不支持格式必须告警、**poppler 抽到文本而我们抽到 0 = 失败**、无文本 PDF 不得判 `Kind=text`)+ **poppler `pdftotext` 独立实现字符覆盖率对照**(`GAH_DOC_CORPUS_STRICT=1` 时低于阈值即失败)。**首轮语料 8 件**(textutil docx、CUPS PDF、3 个系统真实 PDF、**真实合同 docx ×2 + 真实 xlsx 模板 ×1**)全过硬性不变量;覆盖率 corpus-cups 1.000 / fixtures-test 1.000 / 无文本 PDF 双方 0(kind=empty)。**E-D 首个真实缺口已修**:CUPS 产出 PDF 的「文」被 gopdf 映射成康熙部首 U+2F42 → 新增 `normalizeCJKRadicals`(`plugins/host/host-docview/pdftext_norm.go`,仅对 U+2E80–U+2FDF 做单字符 NFKC,**不做全局 NFKC**——全角标点必须保留)并接入 PDF 段落/表格/纯文本兜底三处,修正后该文件覆盖率 0.974 → 1.000。④ **E-E 前端逻辑单测(零新增依赖)**:放弃 vitest 方案,改用 Node 内置 `node --test`(Node ≥23 原生类型剥离,可直接测 .ts;导入 vue 亦可);`web-src` 加 `npm test` 共 **14 项**(会话流消费引擎 11:摘要截断/落定/工具状态与 assistant 快照同步/结果先到不丢/工具增量归并/取消与摘要 meta/未知帧前向兼容;文档意图白名单 3);`tsconfig` 排除 `*.test.ts`(离线无 @types/node,测试由 npm test 执行);CI 增「前端逻辑单测」步。⑤ **验证**:全库 `go test ./... -race -count=1` 绿;`vue-tsc --noEmit` 0 错 + `vite build`;`npm test` 14/14;`bash scripts/size-check.sh --all` 五目标通过(30 MiB 阈值反例验证非零退出);`GAH_DOC_CORPUS=… GAH_DOC_CORPUS_STRICT=1 go test ./plugins/host/host-docview/ -run TestFidelityCorpus -v` 通过。
>
> ### G 组剩余项评测分析(2026-10-11;实测/外部证据驱动,**只做评测,不实施**)
>
> 目的:把 G 组剩余 7 项的「量级」从估数换成可核对证据,并区分**真按需**、**被依赖阻塞**、**被外部条件阻塞**三类,给出开工前置件与触发条件。
>
> **结论速览**
>
> | 项 | 性质判定 | 关键实测/外部证据 | 开工前置件 | 建议优先级 |
> |---|---|---|---|---|
> | **G-D6-1** pdfium-on-WASM 光栅 | 🔴 **被依赖阻塞 + 与体积门冲突**(非单纯按需) | ① 体积:现 release 二进制 **40.58 MiB**(-trimpath -s -w,gz 产物 27.1 MiB),已**触顶/越过** §7.6「<40MB」门;pdfium.wasm 2.7MB(gz,EmbedPDF)/~4.5MB(剥符号)/5.8–5.9MB(发行 zip),wazero 自身 +<1.5MB → 合计 **+3～5.5MiB**,推到 ~45–48MiB;② 运行面:wazero 解释模式约原生 30–40%、AOT 60–80%(启动 10–50ms);Emscripten `STANDALONE_WASM` 构建**上游仍未定**(bblanchon/pdfium-binaries #28,partition_alloc 需关);③ 消费端:**TUI 无任何图形协议支持**(全仓仅 OSC52 剪贴板;docview pager 的 image 块只出 `[图片] name(W×H,N 字节)` 文本)→ 「TUI 位图」是新建子系统(图形协议探测 + 降级),不是接线;Web 端 PDF 已由浏览器原生查看器呈现,位图**无增量价值** | **E-C 体积门决策** + 有网环境实测 wazero/pdfium;建议**明确不做 TUI 位图** | **P3**(依赖未解前不开工) |
> | **G-D6-3** excelize | 🔴 **证据缺口,无法评估保真差异**(卡在「证伪」前置) | ① 依赖:excelize 需新增 `mscfb`/`msoleps`/`efp`/`nfp`/`tiendc/go-deepcopy` + **`golang.org/x/crypto` 与 `x/image`**(gah 现 `go.sum` 对二者命中 **0**,`go.mod` 仅 x/net·x/sync·x/sys·x/term·x/text);② **当前环境无外网**(proxy.golang.org 8s 超时)→ 依赖体积/二进制增量**无法实测**,需有网环境复核;③ **仓库内真实文档语料为 0**(全仓无任何 .docx/.xlsx/.pptx/.pdf 样本,无 testdata 目录;D2–D4 抽取器全部只对手工构造夹具验证过)→ 「自研保真被真实文档证伪」这一触发条件**当前不可判定** | **E-D 真实文档保真语料 + harness**(小体积样本 + opt-in 保真测试目标) | **P2**:先补 E-D;有语料后按差异清单决定是否引入 |
> | **G-C2/G-C3** | ⛔ **纯外部条件**,工程侧已铺满 | `.github/workflows/release-desktop.yml`(mac-aarch64 + win-x86_64 两 job)、`scripts/publish-desktop.sh`、updater 端点 `https://github.com/nekoleamo/go-agent-harness/releases/latest/download/latest.json`、`TAURI_SIGNING_PRIVATE_KEY` 走 CI secret(ed25519 自持签名);缺:repo 公开(免费 runner/未鉴权 updater 可读 Releases)、首个 `v*` tag、`latest.json` 由 CI merge;矩阵缺 mac x86_64;下载体积 ~27 MiB/平台 | 所有者操作(repo 公开 + 打 tag + 配 secret) | **P4**(等外部条件) |
> | **G-X1/G-X2** | ⏳ **真机/人工**,但**可部分自动化** | `docs/VERIFY.md` 未勾选人工项共 **104** 条(不止 G 组:M7 Web UI 16、M18 7、D 组 7、M17 6、R8 4、P4-10 4、M12 4 …);G 组自身约 12 条。前端**无测试运行器**(web-src 仅 vue-tsc + vite,无 vitest)→ 交互类断言当前只能人工;API 层断言已尽数自动化 | 真机(三平台桌面);可选 **E-E 引入 vitest(dev-only)** 把「面板/引导/帧逻辑」类断言自动化 | **P3**(E-E 可先做,成本小) |
>
> **横切发现(影响所有剩余项)**
>
> 1. **体积门已触顶**:release 40.58 MiB(§7.6 记录 31.9MB 已过时);构成 = 基线约 20.6 MiB + 本平台 extplugins embed **19.9 MiB gz**(darwin-arm64 4 件,每件 ~4.8–5.2 MiB;build-tag 单平台嵌入,无跨平台浪费)。任何 D6-1/D6-3 级新增(3～5.5MiB)都会破门 → **先做体积决策**(① 重定义门并登记分解 / ② extplugins 转「附包插件」按需获取(§7.6 已预留该形态) / ③ 压缩基线依赖),否则 D6-1/D6-3 无法验收。
> 2. **工具级审批缺失**:policy-guard 的 `tools/pre-execute` 单一裁决点目前只识别 `shell` 命令模式 → 一切「非 shell 但有远程/破坏副作用」的工具(远程/破坏性副作用的工具)没有统一闸门。补上后审批语义(open 放行/smart 确认/strict 拒绝)自动与档位一致。
> 3. **真实文档语料缺失**:D 组全部保真结论目前只建立在自造夹具上;这既卡住 D6-3 的证伪判定,也是 D2–D4 的回归盲区。
> 4. **当前执行环境无外网**:任何新增第三方依赖(excelize/wazero/pdfium.wasm)在本环境**无法下载与实测体积**;需在有网环境执行「依赖复核 + 体积增量实测」后再评估。
> 5. **可自动化的边界已探明**:会话/文档的 API 与事件面已全部有自动化断言;剩余人工项集中在**视觉/真机/平台侧**(二维码扫描、平台推送、三平台 PDF 查看器、托盘通知)——除前端交互可借 E-E 提升外,无低成本自动化路径。
>
> **开工前置件(建议顺序;每件独立可交付、均不引入运行时依赖)**
>
> | 编号 | 前置件 | 解锁 | 量级 | 风险 |
> |---|---|---|---|---|
> | ~~**E-A**~~ ✅ | 工具级审批:policy-guard `data.approval_tools` + `decide()` 三档复用 + 参数摘要 | ✅ **已交付 2026-10-11**:`RequiresToolApproval`/`checkTool`(+120 字参数摘要)/`parseApprovalTools`(列表或逗号字符串),默认空 = 行为零变化;seed 样板双份注释 | — | 已收口 |
> | ~~**E-C**~~ ✅ | 体积门重定基 + `scripts/size-check.sh`(阈值单一事实源;二进制≤46 MiB / gz≤30 MiB)+ CI 改用脚本 + 增长归因输出 | ✅ **已交付 2026-10-11**:五目标实测表(40.59–43.63 MiB;旧门 40 MiB 已在 4/5 目标失败)+ 归因(基线 19.3–22.3 + extplugins 19.6–21.6 MiB gz)+ 降体路径(协议去 gRPC 化 ≈-14 MiB/插件 / 附包化 ≈-20 MiB)登记在脚本头与本节;**2026-11-14 二轮重定基(48/32,首个 Release 前 CI 复跑实证)与体积债登记见交付门行** | G-D6-1、G-D6-3 **已解锁(体积部分)** | 已收口(阈值沿革持续登记) |
> | ~~**E-D**~~ ✅ | 真实文档保真语料 + opt-in harness(`scripts/gen-doc-corpus.sh` + `TestFidelityCorpus`,poppler `pdftotext` 独立对照) | ✅ **已交付 2026-10-11**:首轮真实语料 8 件(docx 3 / pdf 4 / xlsx 1,含第三方产出的真实合同与模板)→ 硬性不变量全过 + 覆盖率对照;**首个真实缺口已修**(见下「E-D 发现」) | G-D6-3 **判定前提就绪**;D2–D4 回归盲区补齐 | 已收口 |
> | ~~**E-E**~~ ✅ | 前端逻辑单测(**零新增依赖**:Node 内置 `node --test` + 类型剥离,替代 vitest)+ 引导判定纯函数化 | ✅ **已交付 2026-10-11**:`npm test` 14 项(sse 消费引擎 11 / 文档意图白名单 3);CI 加「前端逻辑单测」步 | G-X2 交互类断言可自动化 | 已收口(不再需要 vitest/E-E 原方案) |
>
> **本批收口(2026-10-11)**:前置件 **E-A / E-C / E-D / E-E** 与实施方案 **DOC-1 / RST-1** 共 **6 项交付**(逐项交付行见上,全部带单测/e2e 与实测证据)。**暂存 1 项**(已登记前置,随时可开工):**SELF-1**(pdfium-WASM 自包含档;需有网实测体积/内存 + E-C 体积门决策,仅在「零外部依赖部署」需求成立时做)。**非代码侧**:X-2 三端真机验收(人工)、C2/C3 发行(等 repo 公开 + 首个 tag)—— **后续状态(2026-09-12)**:repo 已公开、`v0.1.0` 已发布,C2/C3 达成,仅剩真机安装/首启验收。验证基线:全库 `go test ./... -race -count=1` 绿 + `go vet` 干净 + 前端 `vue-tsc` 0 错/单测 24 项 + `scripts/size-check.sh` 通过(SELF-1 路线 B 后本平台实测 43.32 MiB / gz 28.04 MiB,门 ≤46/≤30)。
>
> ✅ **DOC-1 · D6-3 判定 GAP 报告(2026-10-11 已交付;D4 口径 = 只测量不改抽取器)**:① **探针**(`plugins/host/host-docview/fidelity_gap_test.go`,test-only 不进二进制):直读源 OOXML 容器,按格式列 **12(xlsx)/11(docx)/7(pptx)** 条特性探针,输出 `GAP file=… feature=… source_hits=… parts=… ours=… severity=…` 与跨文件 `GAPSUMMARY`;严重度三档 **content(可见内容整体丢失)/style(文字在、格式丢)/info(有意取舍或已显式告警)**——**D4 门槛 = 仅 content 命中才评估重型依赖**。② **按承载部件定级**(关键防误报):页眉/页脚/脚注/尾注/批注内的特性一律降级 info(这些部件我们**已显式告警**不解析,不重复计为内容缺口);正文命中才是 content;多部件取最严重者。③ **首轮真实语料结论**:8 件(docx 3 / pdf 4 / xlsx 1)→ **content_gaps = 0**;命中仅 `textBox/field`(两处真实合同,均在 `word/footer1.xml` → info,由既有「含页脚,本期不解析」告警覆盖)、`headerFooter`(info)、`mergeCell`(ours=supported,info)。**判定:当前真实语料未证伪自研抽取器 → 不引入 excelize**;触发条件 = 把带图表/透视/批注/内嵌图的真实 xlsx 放入语料目录跑 harness 后出现 content 级 GAP(命令:`bash scripts/gen-doc-corpus.sh <dir> <真实文件…>` + `GAH_DOC_CORPUS=<dir> go test ./plugins/host/host-docview/ -run TestFidelityCorpus -v`;`GAH_DOC_CORPUS_GAP_STRICT=1` 可把 content 缺口升级为失败)。④ **测试**:探针单测(`fidelity_gap_unit_test.go`:合成 OOXML 锁定 xlsx 12 项/docx 部件降级与多部件取最严重/pptx 7 项/汇总格式/排序/坏容器结构化错误/非 OOXML 空结果),与抽取器行为一一对应。全库 `go test ./... -race -count=1` 绿。
>
> ✅ **RST-1 · 外部 pdftoppm 光栅(2026-10-11 已交付;D3 口径 = 外部优先,自包含档后置)**:① **契约**(`sdk/doc.go`):`DocRaster{Path,Page,DPI,W,H,Bytes,Mime,Data}` + **可选能力** `DocRasterService.Raster(ctx,req,page,dpi)`(类型断言发现;未实现 → 调用方显式报「光栅预览未启用」,`DocService` 不破坏)。② **实现**(`host-docview`):`data.external_raster`(默认关)+ `pdftoppm -png -singlefile -r <dpi> -f <page> -l <page>`;DPI 裁剪 36–300(默认 96)、单页上限 8MiB、超时 30s;产物落 **`$GAH_HOME/cache/doc/raster/`**(sha1(源路径+size+mtime+页+dpi) 复用名,同页同 dpi 不重转,7 天按龄清理);页数越界 → `ErrDocNotFound`、未启用/无 pdftoppm → `ErrDocUnsupported`、超限 → `ErrDocTooLarge`、渲染失败 → `ErrDocParse`;非 PDF 显式拒绝(提示可先 `external_converters` 转 PDF)。③ **消费端**:Web `GET /api/doc/raster?path=&page=&dpi=`(image/png + `nosniff` + 私缓存;未启用 503、缺 path 400、错误映射 415/404/413/422)+ `gah doc <pdf> --raster [--page N] [--dpi D] [-o out.png]`(默认写 stdout;实测本机 CUPS 产出 PDF → 816×1056 PNG / 11446 字节)。**TUI 位图仍不做**(无图形协议)。④ **测试**:converter 光栅 4 组(注入 run 不依赖 poppler:成功落缓存目录/缓存命中不重跑/换页换 dpi 重跑/源变更换名、未启用与无 pdftoppm 报错、run 失败上下文、DPI 裁剪表、超限不留缓存、按龄清理);service 5 组(契约自检、成功含 W/H、越界 NotFound、未启用 Unsupported、非 PDF Unsupported、超限 TooLarge);web(200 PNG + nosniff + 参数透传 + 缺 path 400 + 三类错误映射 + 未实现能力 503);CLI(真实 pdftoppm 光栅断言 PNG 魔数,无 poppler 或无语料时跳过)。全库 `go test ./... -race -count=1` 绿 + 前端 24/24。
>
> ### G 组剩余项实施方案(2026-10-11;分析后定稿 → **全部实作已交付(含 DOC-2/DOC-3a-c/SELF-1 路线 B)**)
>
> 前置件 E-A/E-C/E-D/E-E 已交付,下列方案的依赖与成本都已实测(证据见上方「评测分析」)。**优先级 = 依赖最少 × 风险最低 × 收益明确**。
>
> | 方案 | 交付物 | 依赖 | 量级 | 风险 | 建议顺位 |
> |---|---|---|---|---|---|
> | ~~**P1 · DOC-1**~~ ✅ | D6-3 判定 GAP 报告 | ✅ **2026-10-11 已交付**(首轮真实语料 content_gaps=0 → 暂不引依赖) | — | 已收口 | — |
> | ~~**P2 · RST-1**~~ ✅ | 外部 pdftoppm 光栅 | ✅ **2026-10-11 已交付**(`DocRasterService` + `/api/doc/raster` + `gah doc --raster`;零二进制增量) | — | 已收口 | — |
> | ~~**P4 · SELF-1**~~ ✅ D6-1c 自包含档 | pdfium-WASM + wazero **兜底**外部光栅(路线 B) | ✅ **2026-10-11 已交付**:`pdfium` 包(wazero 宿主 + 渲染)+ 按需取件 + `data.selfcontained_raster`;体积 +2.53 MiB → 43.32 MiB 门内 | 已收口(pdftoppm 优先) |
> | ~~**P5 · DOC-2**~~ ✅ D6-3 窄修 | xlsx 批注(legacy+回复式)/文本框文本 → note + `xl/media/*` 复用 `DocAsset` + 隐藏行/列提示;探针档位同步 | ✅ **2026-10-11 已交付**:`extract_xlsx_extra.go` + 4 组单测;语料 strict 复验 `content_gaps=0` | 已收口(图表/透视的视觉性损失登记为接受) |
> | **P6 · DOC-3** D6-3 同类剩余 | **DOC-3a** docx 批注(legacy+回复式)/文本框 + mc:Fallback 去重 → ✅ **已交付 2026-10-11**(`extract_docx_extra.go` + 5 组单测;语料 docx content_gaps=0);**DOC-3b** pptx 讲者备注 → ✅ **已交付 2026-10-11**(3 组单测);**DOC-3c** 图表数据 + SmartArt 文字 → ✅ **已交付 2026-10-11**(4 组单测 + 语料 content_gaps=0);余 **pptx 内嵌表**未解包(登记接受/待办) | 方案已定(2026-10-11) | DOC-3a/3b/3c 全部收口 | ✅ 已收口(内嵌表缺口保留) |
>
> ---
>
> ✅ **DOC-2 · xlsx 批注/文本框/内嵌图片/隐藏行列(2026-10-11 已交付;零依赖)**:① **批注**:`extract_xlsx_extra.go` 解析 legacy `xl/comments*.xml`(作者名经 `<authors>` 索引回填、多 run 文本拼接为一条)+ 现代回复式批注 `xl/threadedComments/*`(经 `xl/persons/person.xml` 显示名,同单元格回复串合并并标注「(回复)」)→ **note 块**(文本可见,与 docx「批注仅告警」不同)。② **文本框**:`xl/drawings/*.xml` 的 `xdr:txBody` 文字(同一文本框多段落合并)→ note 块。③ **内嵌图片**:`xl/media/*` → image 块 + `DocAsset`(**复用 docx 的资产预算链路**:张数/字节超限 → 占位 + 告警),尺寸经 `imageDims` 探测。④ **隐藏行/列**:`xlsxSheetSize` 扫描顺带统计(原签名 `(rows,cols)` → `(rows,cols,hiddenRows,hiddenCols)`)→ note + warning(此前内容照常显示但**无任何提示**)。⑤ **探针同步**:`chart`/`pivotTable` 由 content 降为 **style**(实测数据/结果仍在单元格,视觉性损失)、`comments`/`threadedComments`/`textBox`/`image` → `info+supported`、`hiddenRowCol` → `warned`,并加**回归断言**(xlsx 探测表不得再出现 content 档)。⑥ **测试**:新增 4 组(四类内容可见(含作者/多 run/多段落/尺寸/资产可取回)、仅当前表不串味、图片超预算占位、坏部件容错不 panic)+ 探针单测更新;语料复验 `GAH_DOC_CORPUS_GAP_STRICT=1` **通过**(`content_gaps=0`;`advanced-all.xlsx` 块数 2→6 / chars 97→207)。**范围外(未登记)**:docx/pptx 批注文本仍只告警、pptx 图表 SmartArt 等仍为 content 档。
> #### D6-3 判定实测(2026-10-11;结论 = **不引入 excelize** → 零依赖窄修 DOC-2 **已交付**,复验 content_gaps=0)
>
> **为什么要有这轮实测**:D6-3 的触发条件写的是「真实样本出现 content 级缺口」,但首轮真实语料 content_gaps=0 是**因为语料里没有 xlsx 生产者**(本机无 openpyxl/xlsxwriter/LibreOffice;WPS 无脚本接口)——即**触发条件从未被真正测过**。
>
> **做法**:新增评测件 `scripts/gen-xlsx-advanced.py`(**纯标准库按 ECMA-376 手工构造**含高级特性的工作簿,补上缺失的 xlsx 生产者;已接入 `scripts/gen-doc-corpus.sh`)+ 修探针盲点 + `GAH_DOC_CORPUS(_GAP_STRICT)` harness 复跑。语料 3 件:`advanced-all.xlsx`(图表+透视+legacy 批注+内嵌 PNG+文本框+表格对象/自动筛选/条件格式/数据验证/合并/隐藏行/公式/富文本)、`threaded-comments.xlsx`(现代回复式批注 + persons)、`pivot-values-only.xlsx`(透视结果只写在透视表工作表单元格)。
>
> **实测结果**:**content 级缺口确实存在** —— `advanced-all.xlsx` = content=5/style=5/info=3;`pivot-values-only.xlsx` = content=1;`threaded-comments.xlsx` = content=1(`GAH_DOC_CORPUS_GAP_STRICT=1` 如期非零退出,即触发条件成立)。**同时发现探针自身两处盲点**(已修,含单测):① 现代「回复式批注」`xl/threadedComments/*` 此前**完全探测不到**(Excel 2018+/WPS 的默认批注形态);② xlsx 文本框(`xl/drawings/*.xml` 里的 `xdr:txBody`)同样无规则。不修则判定工具会对「真丢文字」的文件报 content_gaps=0。
>
> **逐项研判(信息是否真的不可见 —— 决定要不要上重型依赖)**:
>
> | 特性 | 探针档位 | 信息去向(实测) | 判定 |
> |---|---|---|---|
> | 批注 legacy(`xl/comments*.xml`) | content | 文字**只在**该部件;gah 输出无任何告警 | **真丢文本** → 窄修① |
> | 回复式批注(`xl/threadedComments/` + `persons`) | content | 同上(且此前探测不到) | **真丢文本** → 窄修① |
> | xlsx 文本框(`xdr:txBody`) | content | 文字只在 drawing | **真丢文本** → 窄修① |
> | 内嵌图片(`xl/media/`) | content | 视觉唯一承载;**docx 同位置已走 `DocAsset` 资产端点**,xlsx 未接 | **能力缺口** → 窄修②(复用既有资产链路) |
> | 图表(`xl/charts/`) | content | 视觉丢失,但**源数据在单元格**(chart 的 numCache 与 Data 表同时存在) | 可接受取舍(登记不改) |
> | 透视表(`xl/pivotCache`/`pivotTables`) | content | 同上:透视结果本身也是**透视表工作表的单元格**,`--sheet` 可切;源数据在另一表 | 可接受取舍(登记不改) |
> | 隐藏行/列 | info | 内容照常显示,**无任何提示**(探针 `ours=absent`) | 窄修③(一条 note,可选) |
>
> **交付后复验(2026-10-11)**:`GAH_DOC_CORPUS_GAP_STRICT=1` **通过** —— 三件 xlsx 语料 `content_gaps=0`(style/info 档仍在);`advanced-all.xlsx` 块数 2→6(chars 97→207)、`threaded-comments.xlsx` 3 块(chars 32→84)。
>
> **结论与理由(不引入 excelize)**:① **excelize 不渲染图表**——对「图表不可见」零收益,而它擅长的读写/改工作簿不是本仓目标(我们只要**阅读视图**:文本 + 资产);② 满足触发条件的项**恰好都是零依赖可修**的窄面(读 `comments*.xml`/`threadedComments`/`txBody`、复用 `DocAsset`),自研 XML 读取已具备;③ 引入成本 = x/crypto·x/image 等新依赖 + 体积增量,与 E-C 门(46/30 MiB)直接冲突(SELF-1 实测已表明余量很薄)。
>
> **窄修已交付(DOC-2,零依赖;2026-10-11,见下「DOC-2 交付行」)**:① `extract_xlsx.go` 增批注文本(legacy + threaded + persons 显示名)→ note 块(或单元格附注),与 docx「未解析部件显式提示」同口径;② `xl/media/*` 接既有 `DocAsset` 预算链路(与 docx 一致);③ drawing `xdr:txBody` 文本 → note 块;④ 隐藏行/列 → note;⑤ 同步把探针表 `ours` 由 `absent` 改 `supported/warned` 并用 harness 复验 content_gaps 归零(仅剩图表/透视的「视觉性损失」,单独登记为已接受)。预计 1 包改动 + 3 组测试 + 语料回归。
>
> #### SELF-1 前置实测(2026-10-11;有网实测,**结论 = 功能可行但仍暂存**)
>
> **做法**:评测探针 `scripts/eval/pdfium-wasm-probe/`(嵌套模块,不进主模块构建;`GOWORK=off` 构建)+ npm 候选 wasm + 本机 poppler `pdftoppm` 逐像素对照。候选与体积:`@embedpdf/pdfium` 2.15.0 = 4.42 MiB raw / **2.03 MiB gz**(env 30 + wasi 7 导入);`@hyzyla/pdfium` 2.1.13 = 3.80 / **1.92**(env 23 + wasi 7);`pdfium-wasm`(urish)0.0.2 = 旧式 asm.js 产物(**导入 memory/table/global**,自包含档不适用)。
>
> **实测结果(纯 Go 运行时 wazero v1.12,`@embedpdf` 版)**:① **跑通**:`初始化(1ms) → 载入(<1ms) → 渲染`;10 页 @144dpi(900×1120)= **23ms**;线性内存 17.75 MiB(基线)→ 41.38 MiB(10 页);进程峰值 RSS ≈256–294 MiB;编译冷 **828ms** / 命中 compilation cache 26ms(可落 `$GAH_HOME/cache`)。② **保真**:与 `pdftoppm -r 144` 逐像素对照 —— 合成 10 页文档 differing=**0.15%**/meanΔ 0.04(墨迹量差 0.1%)、iWork 图形页 **0.96%**/Δ0.22(墨迹量相同)、系统矢量+认证标志页 **5.62%**/meanΔ2.04(墨迹 +15%);**差异叠加图经视觉核验全部位于文字/图形/表格线的抗锯齿轮廓,无整块缺失或多余元素 → 无结构性差异**。③ **宿主面**:env 30 + WASI 7 个导入全部由 Go 提供(**Emscripten 的 WASI 为 32 位偏移变体,不能复用 wazero 标准 WASI 宿主**,签名须取自 `CompiledModule.ImportedFunctions()`);有语义者仅 4 个:`_emscripten_memcpy_js`(真内存拷贝,实测 413 次)、`emscripten_resize_heap`(对导出 memory `Grow`,15 次)、`emscripten_date_now`、`fd_write`(诊断转 stderr);`__syscall_*` 8 次(FS 探测,零值返回即可)。
>
> **残留风险(未解,必须随实施一并解决)**:Emscripten `invoke_*`(JS 侧函数指针 trampoline)在 wazero **无法忠实实现**——公开 API 无 table / function-reference 调用能力,只能空 stub;实测真实认证页触发 **6 次**,渲染经像素对照无可见影响,**但不能证明安全**(setjmp/longjmp 路径尤甚)。彻底解决 = 自行以 `-sSTANDALONE_WASM` / `-sSUPPORT_LONGJMP=wasm` 构建 pdfium(emsdk + depot_tools,重型,上游 #28)。
>
> **对决策的影响(硬数字)**:体积 **+3.46 MiB(wazero 运行时,实测 hello 1.12→probe 4.59 MiB「-s -w」)+ 2.03 MiB(wasm.gz)** ≈ **+5.5 MiB** → 二进制 40.72 → **≈46.2 MiB,超出当前 ≤46 MiB 门**;gz 27.16 → 29.2 MiB(门 ≤30,余量 0.8)。故 **SELF-1 不能单独上**,必须先做 E-C 登记的降体路径(外部插件协议去 gRPC 化 ≈ -14 MiB / extplugins 附包化 ≈ -20 MiB)或重定体积门。若实施,gz 后的 wasm 可**在内存解压**后交给 wazero(不走临时文件),能力面照 RST-1 契约挂在 `DocRasterService` 之后(pdftoppm 优先、自包含档兜底)。
>
> ✅ **DOC-3a · docx 批注(legacy + 回复式)+ 文本框(2026-10-11 已交付;零依赖,与 DOC-2 同构)**:① **批注**(`extract_docx_extra.go`):解析 `word/comments.xml` 的 `<w:comment w:id w:author w:date>`(多 run/多段落文本拼接、首个段落 `w14:paraId` 记录),经 `word/commentsExtended.xml` 的 `w15:parentParaId` **合并回复线程**(根 + 「作者(回复)」串),`word/people.xml` 提供显示名回落 → note 块;`finish()` 不再把 comments.xml 列为未解析部件(**消除「含批注,本期不解析」的误导告警**)。② **文本框**:正文 `w:txbxContent` 内 `w:t` 经独立流式扫描(不动正文解析器状态机)→ 每个文本框一条 `文本框: …` note(Choice/Fallback 重复内容去重)。③ **顺带修复真实保真缺陷**:Word 的 `mc:AlternateContent` 会把同一内容写进 `mc:Choice`(现代)与 `mc:Fallback`(兼容),原实现**把文本框文字在正文里打印两遍**;主解析器新增 `skip` 子树机制 —— `w:txbxContent` 交 note 输出、`mc:Fallback` **仅在同层确有 `mc:Choice` 时**跳过(**纯 Fallback 内容必须保留**,含单测防回归)。④ **语料**:新增 `scripts/gen-docx-advanced.py`(纯标准库构造含批注线程/people/文本框的 docx —— `textutil` 无法写批注)并接入 `gen-doc-corpus.sh`。⑤ **测试**:新增 5 组(线程合并与显示名回落/文本框去重/普通文档零影响/坏部件容错/孤立 commentsExtended)+ AlternateContent 规则 2 例(双写只留一份、纯 Fallback 不丢);探针 docx `comments`/`textBox` 由 `content+absent/warned` 改为 **info + supported**。⑥ **复验**:`GAH_DOC_CORPUS_GAP_STRICT=1` **通过**(docx `content_gaps=0`;`advanced-comments.docx` blocks=6/note=3/chars=193/warnings=0)。**范围外**:pptx 备注(DOC-3b)、docx/pptx 图表与 SmartArt(DOC-3c,仍为 content 档)。
> ✅ **DOC-3b · pptx 讲者备注(2026-10-11 已交付;零依赖)**:`extract_pptx_notes.go` 依幻灯片 rels 定位 `notesSlide` 部件,流式取各形状 `a:t`(多形状/多段落合并单行),**跳过 `p:ph type="sldNum"` 编号占位**(避免把页码当备注);`parseSlide` 的「含备注页,本期不解析」告警改为 note 块 `备注(第 N 张): …`(N = `sldIdLst` 展示顺序);部件缺失 → 显式告警,空备注 → 静默(无内容不等告警)。测试 3 组(文本合并与编号跳过/缺失或空/部件缺失告警 + 截断容错)+ 既有 deck 夹具升级(真实 notesSlide + sldNum)与 `TestExtractPPTX` 断言更新;探针 pptx `notes` → **info + supported**。**已知语料缺口**:本机无 pptx 生产者(textutil 只出 docx/odt),故 pptx 侧仅单测覆盖、不进语料 harness。
> ✅ **DOC-3c · 图表数据 + SmartArt 文字(2026-10-11 已交付;零依赖)**:① **图表**(`extract_office_graphics.go`):流式取 `c:title`(→ note)与各 `c:ser` 的 `c:tx`/`c:cat`/`c:val` **缓存点**(`c:pt idx` 重排,兼容 num/str/multiLvl 缓存)→ **数据表格块**(首列类别 + 每系列一列,数值列 `Numeric` 标记,行列按 `Budget` 封顶并显式告警);**无缓存**(仅链接外部数据源)→ note 说明「未含缓存数据」而非静默。② **SmartArt**:`dgm:dataModel` 内 `a:t` 去重后「 / 」连接 → note。③ **定位**:部件优先经 rels(按 Id 排序 → 输出确定性),**rels 缺失时按部件前缀回退扫描**;docx 走 `docxAnnotations`,pptx 在 `parseSlide` 内(块带幻灯片号)。④ **明确不做**:图表/SmartArt **视觉还原**(阅读视图定位;真需视觉走 PDF/光栅路径)、**pptx 内嵌表**(`ppt/embeddings` OLE 未解包 —— 探针保持 content 档,登记为剩余缺口)。⑤ **探针**:docx/pptx `chart`、`smartArt` → **info + supported**,并加回归断言(docx 不得再有 content 档;pptx 剩余 content 档仅 `groupedShape`/`embeddedSheet`)。⑥ **测试**:新增 4 组(标题+两系列表格与 idx 乱序重排/无缓存 + rels 回退/行列预算截断/pptx 幻灯片级图表);语料 `gen-docx-advanced.py` 增图表(含缓存)与 SmartArt 部件 → `GAH_DOC_CORPUS_GAP_STRICT=1` **通过**(`advanced-comments.docx` blocks=9/note=5/table=2/chars=280,`content_gaps=0`)。
> ✅ **SELF-1 · 自包含光栅(2026-10-11 已交付;路线 B)**:① **运行时**(`plugins/host/host-docview/pdfium`):纯 Go **wazero** 编译并运行 pdfium.wasm,宿主导入(`env` + `wasi_snapshot_preview1`,**签名取自模块自身导入定义** —— Emscripten 的 WASI 是 32 位偏移变体,复用 wazero 标准 WASI 宿主会签名不匹配);有语义 stub 仅 5 类(`fd_write` 诊断、`emscripten_resize_heap`/`notify_memory_growth`、`_emscripten_memcpy_js` 真拷贝、`date_now`/`clock_time_get`、`__syscall_*` **返回 -ENOSYS**);`invoke_*` 空实现 + 计数 → `>0` 时**显式告警**(毒化实验证明其返回值未被使用,但不可证明对所有文档安全);渲染链路 `LoadMemDocument → GetPageCount → LoadPage → 尺寸×dpi/72 → Bitmap_CreateEx(BGRA)+FillRect → RenderPageBitmap → GetBuffer → RGBA`。② **取件与便携**(`pdfium/wasm.go`):`data.pdfium_wasm_path`/`GAH_PDFIUM_WASM` → `$GAH_HOME/cache/pdfium/pdfium.wasm` → 下载(默认 pdfium-lib 8046d **STANDALONE_WASM** 构建,zip 内取 `.std.wasm`)**sha256 固定**;校验不符即丢弃重取。③ **接入**:`data.selfcontained_raster`(默认关)+ `pdfium_wasm_path`/`pdfium_wasm_url`;`Raster` 中 **pdftoppm 优先、自包含兜底**,产物落同一 `cache/doc/raster/`(复用 RST-1 的命名/缓存/清理,超 8 MiB 报错),运行时懒加载、卸载释放。④ **CLI**:`gah doc --raster-self`;`--raster` 现在自带兜底(装 poppler 仍优先外部路径)。⑤ **实测**:真实 wasm 渲染 267×133@96dpi(非白像素 4123)、`invoke=0`/`syscalls=0`(该文档);端到端 CLI 无 poppler PATH 下走自包含路径出 PNG;体积 **+2.53 MiB → 43.32 MiB(gz 28.04)**,门 ≤46/≤30 内(比二轮预估 +3.46 更省)。⑥ **测试**:pdfium 包 5 组(取件本地/缓存/下载+sha 校验/zip 选件/真实渲染)+ host-docview 集成 2 组,均以 `GAH_PDFIUM_WASM` opt-in(CI 无网无件跳过)。**已知差异**:与 pdftoppm 的像素差异为抗锯齿/字形策略级(二轮实测 0.15–5.6%,差异全在边缘),且 pdfium 与 poppler 对页高取整可差 1 px(133 vs 134)。
> #### SELF-1 可能性分析(二轮,2026-10-11;多构建对比 + 保真交叉验证 + 四条落地路径)
>
> **目的**:一轮实测已证「wazero 上跑得动、与 poppler 仅差抗锯齿」,但留下两个未决问题(① `invoke_*` 能否忠实实现 ② 体积门怎么办)。二轮把这两问用**可核对证据**收口,并把落地方式算成四条路线。
>
> **① 候选矩阵(4 个独立构建,tool 同源不同 flag;均为「无 memory/table 导入」的自持内存产物)**
>
> | 构建 | raw | gz | 导入面 | 观察 |
> |---|---|---|---|---|
> | `@embedpdf/pdfium` 2.15.0 | 4.42 MiB | **2.03 MiB** | env 30 + wasi 7 = 37 | 另有 `PDFiumExt_*` 扩展(PNG 编码等);需真 `_emscripten_memcpy_js`(实测 413 次)与 `emscripten_resize_heap`(15 次) |
> | `@hyzyla/pdfium` 2.1.13 | 3.80 MiB | **1.92 MiB** | env 23 + wasi 7 = 30 | 最小 gz;但 `invoke_*` 调用最频繁(10 页流程 63 次) |
> | `pdfium-lib` 8046d(normal) | 5.05 MiB | 2.36 MiB | env 26 + wasi 7 = 33 | 含 `_tzset_js`/`_abort_js` 等 JS glue |
> | **`pdfium-lib` 8046d `pdfium.std.wasm`(`-sSTANDALONE_WASM=1`)** | 5.06 MiB | 2.36 MiB | **env 11 + wasi 8 = 19** | **宿主面最小**:无 `_emscripten_memcpy_js`/无 `resize_heap`(改 `emscripten_notify_memory_growth`)、`__syscall_*` 仅 4 个;`invoke_*` 仍有 5 个 |
>
> **② `invoke_*` 问题(结论:无法忠实实现,但**可证明无害**且必须留告警)**:① 实现上无解 —— wazero 公开 API **无 table / function-reference 调用能力**(`api` 包无 `Table`/`FunctionReference`),宿主 stub 无法回调 wasm 函数指针;② **`-sSTANDALONE_WASM=1` 并不能消除**(实测 std 构建仍导入 5 个 `invoke_*`)——上游结论一致:Emscripten 的 setjmp/longjmp trampoline 在 standalone 下仍需宿主提供,`-sSUPPORT_LONGJMP=wasm` 支持不一致(上游 issue 未定),自建构建是重型路线(emsdk + depot_tools);③ **但实测其返回值不被使用**:改动探针把 `invoke_*` 返回值**毒化**(i32→0xdeadbeef / f64→-1.5)后重渲染,与 no-op 版**逐像素完全相同**(0 px,认证页 6 次调用 / std 构建 4 次调用,目标函数为 3 个未导出小函数 43/109/439 字节,发布版无 name 段无法命名);④ 生产守卫:`invoke` 计数 > 0 时在光栅结果上附**显式告警**(「该文档触发 pdfium JS-glue 回调路径,结果未经交叉校验」),语料像素门负责兜底。
>
> **③ 保真交叉验证(三方对照,认证页/144dpi)**:`pdfium(std)` vs `poppler` = differing 5.61% / meanΔ2.04;`pdfium(embedpdf)` vs `poppler` = 5.62% / 2.04;**`pdfium(embedpdf)` vs `pdfium(std)` = 0 px(maxΔ3)**。即:与 poppler 的差异来自 **poppler 侧的字形/AA 策略**(差异叠加图经视觉核验全在边缘),**两个独立构建的 pdfium 结果完全一致** —— 反证 stub 未扭曲输出。合成 10 页 0.15% / iWork 图形页 0.96%(墨迹量相同)。
>
> **④ 体积与落地路线(实测数字;当前 40.74 MiB / gz 27.17,门 ≤46 / ≤30)**
>
> | 路线 | 主仓二进制 | 部署动作 | 判定 |
> |---|---|---|---|
> | **A 嵌入** | +3.46(wazero)+2.03~2.36(wasm.gz)= **≈46.2–46.6 MiB 越门** | 无 | ✗ 需先降体 |
> | **B(推荐)wazero 内置 + wasm 按需(sha256 固定)落 `$GAH_HOME/cache/pdfium/`** | +3.46 → **≈44.2 MiB ✓** | 首次光栅一次性拉取 2.0–2.4 MiB(或随部署目录放一份) | ✓ 保持「主仓二进制不膨胀」+ pdftoppm 优先、自包含兜底 |
> | C 发行侧可安装 external plugin(`gah --install`,提供 `ctx.docRaster`) | **+0** | 插件产物 ≈10–11 MiB gz(go-plugin 5 + wazero 3.5 + wasm 2) | ✓ 架构最贴插件模型;多一套安装/生命周期 |
> | D 先降体(去 gRPC 化 -14 / extplugins 附包化 -20)再走 A | +5.5 后仍门内 | 无 | ✓ 但降体本身是独立工程 |
>
> **⑤ 其他实测约束**:wasm 线性内存 ≈17.9 MiB(空载)→ 41.6 MiB(10 页 @144dpi);**编译期内存是主要开销**(探针进程峰值 RSS:embedpdf 256 MiB / pdfium-lib std 594 MiB;10 页渲染耗时 23 ms);冷编译 828 ms、命中 compilation cache 26 ms → 生产应把 cache 落 `$GAH_HOME/cache/wazero/`(首次摊销)。宿主面仅 19–37 个导入,其中有语义者 4 类:`fd_write`(诊断)、`emscripten_notify_memory_growth`/`resize_heap`(内存通知/增长)、`__syscall_*`(**必须返回 -ENOSYS 而不是 0**,否则 Emscripten FS 会误判 openat 成功);`clock_time_get` 等 WASI 29 个可直接复用 wazero 标准 WASI 宿主,但 Emscripten 的 32 位偏移变体(`fd_seek(i32,…)`)须按模块自身导入签名构建宿主。
>
> **结论**:SELF-1 **技术上成立且保真达标**(残差仅「无法证明对未测文档绝对安全」+ 体积/内存成本),推荐 **路线 B**,并以「`invoke>0` 告警 + 语料像素门(有 poppler 时强制交叉核对)」作为验收纪律;是否开工仍取决于「零外部依赖部署」是否为真实需求(当前 pdftoppm 已覆盖功能)。
>
> #### DOC-3 方案(docx/pptx 同类剩余项;2026-10-11 提出 → **DOC-3a/3b/3c 已全部交付**;余 pptx 内嵌表未解包)
>
> **现状(DOC-2 后,探针声明档位)**:xlsx 侧已清零 content;docx/pptx 仍有**真丢文本**与**视觉性损失**两类——
>
> | 项 | 部件/证据 | 现状 | 分类 | 方案 |
> |---|---|---|---|---|
> | ~~docx 批注(legacy)~~ | `word/comments.xml` | ✅ **DOC-3a 已交付**:文本+作者+日期 → note | — | ✅ 已收口 |
> | ~~docx 回复式批注~~ | `word/commentsExtended.xml` + `word/people.xml` | ✅ **DOC-3a 已交付**:parentParaId 线程合并 + people 显示名 | — | ✅ 已收口 |
> | ~~docx 文本框~~ | `w:txbxContent` | ✅ **DOC-3a 已交付**:note 块(且**修掉 mc:Choice/Fallback 双写导致的正文重复** —— 实测原输出把文本框文字打印两遍) | — | ✅ 已收口(页眉/页脚内仍属未解析告警) |
> | ~~docx 图表 / SmartArt~~ | `word/charts/*`、`word/diagrams/*` | ✅ **DOC-3c 已交付**:图表缓存 → **数据表格块**(标题 note);SmartArt → note | — | ✅ 已收口 |
> | ~~pptx 讲者备注~~ | `ppt/notesSlides/*` | ✅ **DOC-3b 已交付**:备注文本 → note(编号占位跳过) | — | ✅ 已收口 |
> | ~~pptx 图表 / SmartArt~~ | `ppt/charts/*`、`ppt/diagrams/*` | ✅ **DOC-3c 已交付**(幻灯片级 rels → 表格/note) | — | ✅ 已收口 |
| pptx **内嵌表** | `ppt/embeddings/*`(OLE,含内嵌 xlsx) | 未解包 | 数据(可能含幻灯片外的内容) | ⏸ **保留缺口**:需「解包 → 复用 xlsx 抽取器」的嵌套容器支持(非本批范围);探针保持 **content** 档,登记接受/待办 |
>
> **交付顺序与验收**:**DOC-3a**(docx 批注 + 文本框;零依赖、真丢文本、与 DOC-2 完全同构,预计 1 文件 + 3 组单测)→ **DOC-3b**(pptx 备注,小)→ **DOC-3c**(图表/SmartArt 数据提取,**先探针再决定**:若提取后 `GAPSUMMARY` 仅剩视觉性差异,则把 chart/smartArt 降为 style 并登记接受,与 DOC-2 的 chart/pivot 处理一致)。每步验收沿用同一口径:`GAH_DOC_CORPUS(_GAP_STRICT)` 复跑 + 探针 `ours`/severity 同步 + 回归断言(不得回退 content 档)。
>
> **明确不做**:pptx 形状/动画保真渲染、SmartArt 版式还原、图表视觉还原(与「阅读视图」定位一致;真需视觉看图走 PDF/光栅路径)。
>
> #### P1-2 · DOC-1 D6-3 判定(GAP 报告;**不改抽取器**)
>
> **做法**:`host-docview` 的保真 harness 增 `capabilityProbe`——**直接读源 OOXML**(zip)统计「源使用了哪些高级特性」,再对照我们输出块模型能表达的信号,打印 `GAP` 行:
>
> | 源特性(xlsx) | 探测点 | 我们现状 | 判定 |
> |---|---|---|---|
> | 富文本单元格 | `sharedStrings <si>` 内含 `<r>` | 合并为纯文本(丢样式) | 内容不丢,样式丢 → 通常可接受 |
> | 条件格式 | `conditionalFormatting` | 不表达 | 不影响文本可读性 → 可接受 |
> | 图表 / 透视 | `charts/*`, `pivotCache*` | 不表达 | **内容不可见的真缺口**(需用户判断) |
> | 数据校验 / 批注 | `dataValidation`, `comments*` | 不表达 | 中 |
> | 隐藏行列 / 自动筛选 | `<row hidden>`, `autoFilter` | 不表达 | 低 |
>
> docx/pptx 同法(页眉/页脚/批注/域/修订/嵌套表/文本框/OMML 公式/图表)。**输出**:每文件一行 `GAP file=… feature=chart source_hits=1 ours=absent` + 汇总。
> **验收**:真实富格式样本目录跑一次 → 得到「是否有内容级缺口」的确定答案;据结果决定是否进 D6-3 引入 excelize(引入前须有网实测依赖与体积)。
>
> #### P2-2 · RST-1 D6-1a 外部光栅(D6-1 的**推荐实现路径**)
>
> **分析结论**:D6-1 原本假定必须引 `wazero + pdfium.wasm`(+3～5.5 MiB、`STANDALONE_WASM` 上游未定、需有网实测);但 D6-2 的转换器链已探测 `pdftoppm`,本机实测 `pdftoppm -png -r 96 -f 1 -l 1 x.pdf` → 11.4 KB/页可用 PNG —— **零二进制增量、零新依赖**,保真优于逆向自研光栅。
> **做法**:`host-docview` 增 `Raster(ctx, req, page, dpi)`(opt-in `data.external_raster: true`;pdftoppm 缺失 → 结构化 unsupported + 安装提示;产物落 `$GAH_HOME/cache/doc/raster/<sha1>-p<page>-<dpi>.png`,7 天按龄清理;超时/尺寸/页数上限);Web 增 `GET /api/doc/raster`(image/png,与 `/api/doc/asset` 同级白名单);**TUI 位图仍不做**(无图形协议,已登记)。自包含档(SELF-1)仅在"必须零外部依赖部署"时再评估。
> **验收**:真实 PDF 语料光栅化 + Web 缩略图;无 pdftoppm 环境下的结构化提示。
>
> ---
>
> **📦 本批交付汇总(2026-10-11;单一入口)**
>
> | 切片 | 提交 | 交付物 | 验证 |
> |---|---|---|---|
> | DOC-1(D6-3 判定) | `b0d4eae` | GAP 探针(xlsx 12/docx 11/pptx 7 特性 × content/style/info)+ GAPSUMMARY | 探针单测 + 首轮真实语料 content_gaps=0 |
> | RST-1(D6-1a) | `9e9a52e` | `DocRasterService` + `/api/doc/raster` + `gah doc --raster`(外部 pdftoppm,零体积) | converter/service/web/CLI + 真机实测 PNG |
> | DOC-2(D6-3 窄修) | `a4e27f4` | xlsx 批注(legacy+回复式)/文本框/内嵌图片/隐藏行列;探针档位同步 | 4 组单测 + 语料 strict 复验 content_gaps=0 |
> | DOC-3a(D6-3 同类) | `badf601` | docx 批注(legacy+回复式)/文本框 → note + `mc:Fallback` 去重 | 5 组单测 + 语料 strict 复验 docx content_gaps=0 |
> | DOC-3b(D6-3 同类) | `70d1248` | pptx 讲者备注 → note(编号占位跳过;替换原「本期不解析」告警) | 3 组单测 + 既有 deck 夹具/断言更新 |
> | DOC-3c(D6-3 同类) | `2641987` | 图表缓存数据 → 表格块 + SmartArt 文字 → note(docx/pptx;rels 缺失回退) | 4 组单测 + 语料 strict 复验 content_gaps=0 |
> | SELF-1(路线 B) | `36e3fcd` | 内置 wazero + pdfium.wasm 自包含光栅(pdftoppm 优先兜底;++2.53 MiB) | pdfium 包 5 组 + 集成 2 组(opt-in 真实 wasm)+ CLI 无 poppler 实测 |
> | 前置件 | `50a7aad`/`124b06d`/`1e3a7e0`/`1a4329b` | E-A 工具级审批、E-C 体积门重定基、E-D 真实语料 harness(+部首码位修正)、E-E 前端零依赖单测 | 见各自提交 |
>
> **暂存(可随时开工)**:SELF-1(pdfium-WASM 自包含档;有网实测 + 体积决策)。
>
> **需拍板的决策点(开工前确认)**
>
> | # | 决策 | 选项与建议 |
> |---|---|---|
> | D3 | 光栅实现 | 建议:**外部 pdftoppm**(零体积,需本机装 poppler)优先;仅在需要零外部依赖时评估 pdfium-WASM |
> | D4 | D6-3 门槛 | 建议:先只做 GAP 报告;**出现"内容级缺口"(图表/透视/批注等不可见内容)才引入 excelize** |
>
> **本方案明确不做**:TUI 位图渲染(无图形协议;Web 原生查看器已覆盖保真需求)。
>
> 🔧 **历史瘦身(2026-09-16)**:仓库 .git 641MB(早期未压缩 embed 裸二进制 ~100MB + .gz 产物 6 代迭代 ~400MB,gzip 高熵不可 delta)→ `git filter-repo --strip-blobs-bigger-than 1M` 清理全部 >1MB 历史 blob 后重建 HEAD 产物,**641MB → 113MB**(pack 99.5MB 几乎全为当前 embed 产物);提交 hash 全部改写,本文件与 docs/ 中引用的 commit hash 已按 filter-repo commit-map 同步替换(残留 0);备份 /tmp/gah-backup-641.bundle。后续 embed 产物每次变更仍会新增 blob(不可压缩),属设计内。
> 🚧 **R7 桌面壳零成本发行工程(C2/C3 变体,2026-09-16 铺,待 repo 公开 + 首 tag 启用)** 已交付工程侧:用户决策①repo 公开②接受无签名③先铺工程。①**updater 接入(C3 零成本)**:Cargo.toml + `tauri-plugin-updater`;main.rs 注册 Builder + 托盘「检查更新…」菜单(checkForUpdates:check → download_and_install → 通知+重启;失败仅通知不打断);tauri.conf `plugins.updater`(active/endpoints=GitHub Releases `releases/latest/download/latest.json`/pubkey);ed25519 密钥 `tauri signer generate`(~/.tauri/gah.key 无密码;公钥入 conf,私钥不入库)。cargo check 过(4m 新 crate)。②**发布流水线(C2 零成本,不购证书)**:`scripts/publish-desktop.sh`(platform darwin-aarch64/x86_64/windows-x86_64:go build sidecar → tauri release bundle(--config 覆写 version)→ 提取 .app.tar.gz/dmg/setup.exe → npx signer sign 每产物 → latest.<平台>.json;merge 模式 jq 合成含全平台 latest.json);`.github/workflows/release-desktop.yml`(tag v* 触发;macos-14 aarch64 + windows-latest x86_64 两 job + merge-upload 合成 latest.json 并经 softprops 上传 Release;secret TAURI_SIGNING_PRIVATE_KEY)。③**无签名分发指引**:docs/RELEASE.md(首次启动右键打开/`xattr -dr com.apple.quarantine`/SmartScreen 放行;密钥管理;本地发布路径)。tauri.conf bundle.targets 增 nsis(currentUser);docs 登记同步(TODO C 组/ROADMAP P2P3/DESKTOP_FEASIBILITY §9§10/README 索引)。**验收待真机**:repo 公开后首 tag v0.2.0 触发 CI → Release 含安装包+latest.json → 旧版托盘检查更新自动升级;win runner NSIS 真机调。
> ✅ **R8 数据根收紧(2026-09-16,取代 R6 的应用数据目录方案)** 已交付:用户决策「排除 GAH_HOME env,只允许 gah-data 存数据,无则自动创建,禁止 ~/.gah」——① `cmd/gah homeDir()` 移除 GAH_HOME env 输入源(仅便携 gah-data 自动新建;用户设不一致 env 启动告警不生效);boot 报错文案同步;main_test 补 `TestHomeDirIgnoresEnv`。② 桌面壳 `desktop/src-tauri/src/main.rs`:spawn 不再传 GAH_HOME、删除 appDataHome()(系统应用数据目录方案作废),数据根 = sidecar 同级 gah-data(Contents/MacOS/gah-data);启动失败提示改 gah-data 可写性指引(并提示升级 .app 前 /backup)。③ 文档同步:README/README_EN(便携节+env 表 GAH_HOME 行)、AGENTS.md「便携纪律」(解析链去 env 输入、写盘路径经 $GAH_HOME 派生语义注)、DESIGN §7.3、docs/DESKTOP_FEASIBILITY(§2/架构/落地变更/§10 R8 决策)、docs/RELEASE.md(数据根 = sidecar 同级 gah-data,升级替换需 /backup)、start.sh(去 export GAH_HOME)。验证:cargo check 通过;pty 探针重构(bin 同级 gah-data 预置数据,去 GAH_HOME env);全库 44 包 go test -race 全绿。副作用记录:.app 升级会替换 Contents/MacOS/gah-data,桌面形态数据保留依赖 /backup(文档已注);桌面壳数据不再存应用数据目录。
> 📌 **远期改进观察(2026-09-16,PI_COMPARISON 整合 dsh 后)**:非排期待办,按需启用——pi 线:C4 分支摘要/E5 toast/E4 配置热更/T8 键位自定义;dsh 线:session_search 工具面(中)/delegation 边界审批 pin(低中)。对比基线见 docs/PI_COMPARISON.md(pi + dsh 双参考)。
> ✅ **R9 全局安装与 symlink 数据根补正(2026-09-16,承接 R8)** 已交付:① **全局安装(方案 A,pi 式,两模式并存)**:`scripts/install.sh`(macOS/Linux:构建 → `~/.local/gah` + symlink `~/.local/bin/gah`,INSTALL_DIR/BIN_DIR 可覆盖、`--uninstall`、PATH 缺失提示)+ `scripts/install.ps1`(Windows:构建 → `%LOCALAPPDATA%\gah` + **用户级** PATH,`-InstallDir`/`-Uninstall`)——装完任意目录 `gah` 启动;**两种模式并存由部署位置决定**(全局共用:统一数据根 + cwd project key 隔离;便携单飞:cp 到任意目录即独立 gah-data),互不干扰。② **symlink 数据根修复**(实测暴露):PATH symlink 启动时 `os.Executable()` 返回**入口链接路径** → 数据根错误落符号链接目录;`cmd/gah homeDir` 增 `execRealPath`(EvalSymlinks 归一)→ 数据根跟随**真实二进制**(全局 symlink 场景与便携拷贝语义一致);补 `TestExecRealPath`(含 mac `/var→/private/var` 归一断言);`TestHomeDirIgnoresEnv` 期望同步归一。③ **文档**:README(中英)Quick start 增「全局安装」小节(跨平台用法/两种模式对照);DESIGN 未实施清单 R8 后登记;scripts 入库(不受 docs 忽略影响)。验证:install.sh 实测(临时目录安装/符号链接启动数据根落真实目录);全库 44 包 `go test -race` 全绿。
> ✅ **R10 安全加固与生命周期修复(2026-11-14,承接「完整分析当前项目」审计:3 P0 / 13 P1 / P2-P3 若干)** 已交付(按批次):
> ① **P0-1 Web 跨站攻击面收口**:新增 `web/guard.go` 护栏中间件——Host 白名单拦 DNS rebinding;非 GET/HEAD 跨站来源 403(Origin,缺 Origin 用 Referer 回退);`/api/` 下 POST/PUT/PATCH 强制 `application/json|multipart/form-data`(把请求逼进浏览器预检);统一安全响应头(`nosniff`/`X-Frame-Options: DENY`/`frame-ancestors 'none'`/`Referrer-Policy: same-origin`);请求体上限(API 1 MiB / 附件 64 MiB);`Handler()` 与 `Start()` 统一挂 `guardMiddleware(authMiddleware(handler()))`(**此前 `Handler()` 完全绕过鉴权**,任何库内直挂使用方都是裸奔);token 比较改 `subtle.ConstantTimeCompare` 并**移除 `?token=`**(会进浏览器历史/访问日志/Referer);token 模式下入口页下发 `SameSite=Strict`+`HttpOnly` 的 `gah_token` cookie(**修「开 token 后 Web UI 全 401 不可用」**:WS 握手与 fetch 无法自定义头);`/api/providers` 不再回传明文 api_key(`maskKey` 掩码,修 P1-9 密钥外泄面);`/api/input` 改 CAS 抢占(修 check-then-act 双提交/并发回合)、`/api/control` 校验档位枚举(未知值 400);`web/ws.go` 写侧加锁 + 写超时 + 读泵(drain)+ ping 保活;`web/doc.go` 惰性注入加锁。
> ② **P0-2 默认发行态路径沙箱失效修复**:根因——`bundle-base` 默认 `tool-files/tool-shell/tool-web` 为 `enabled:false`+`watch:true`(走 host-bridge 外部进程),而 `extplugins/tool-basic` 的 `toolfiles.NewTools()` 工具侧 `sb=nil`,`sandbox.go` 的 `ValidatePath` 无生产调用方 ⇒ **默认安装下 `file_*` 可任意绝对路径读写**。修法:**宿主 pre-execute 单点裁决**(工具侧沙箱缺失也拦得住)——`sdk` 增可选能力接口 `ReadValidator`(读校验)/`EffectiveSandbox`(联动后有效档,不破坏既有实现/测试 fake);`policy-guard` 新增 `pathpolicy.go`(凭据 deny-list:`provider.yaml`/`search.yaml`/`.env`/`credentials*`/`id_rsa`/`*.pem`/`.ssh`/`.aws` 等任何档位拒 + `EvalSymlinks` 真实路径 + 段边界归属判定,修 `Clean+HasPrefix` 的 symlink/前缀绕过);`ValidateRead`(非 full-access 限 workspace ∪ `$GAH_HOME`);`CheckPathArgs`(按工具名分读/写解析 `path`/参数,解析失败**显式失败**);guard 在 shell 危险模式之外增 `CheckPathArgs` 裁决;`tool-files` 读写统一走沙箱接口(并支持 `EffectiveMode`);审批匹配改**先 JSON 解码 command 再匹配**(修 `rm` 文本级绕过)+ 模式扩展(强制推送含 `git -C`/`env` 前置、`chmod 0777|a+rwx|+s`、解释器 `os.remove/rmtree`、`base64|curl | sh` 管道执行、系统文件/设备覆写、计划任务持久化),审批弹窗内嵌命令原文(200 字,修 P1-2)。
> ③ **P0-3 生命周期不可逆修复**:`core/ctx` 增 `ProvideScoped`(值同一才回收)/`Unprovide`;`core/plugin` 用 `scopedCtx` 记录每实例服务键 ⇒ **卸载、启动失败、并发启动竞争三条路径都归还服务**——此前 `Provide` 无注销路径,凡提供服务的插件 `unload→load` 恒报 `already provided`(TUI `/plugins off|on` 事实上不可用),且卸载后依赖者仍能 Inject 到已停实例(破「卸载即撤销」「缺失依赖显式失败」双红线);registry 改「锁内取快照、锁外跑插件代码」(修 Start 内经 `system.registry` 回调自锁死)。`host-jobs`/`host-fanout` 的 disposer 由空实现改 `Stop()`/`Shutdown()`(卸载即终止在跑任务/子代理);`host-fanout` 修 `f.agents[id]` 锁外读竞态 + 已完成会话上限 50。
> ④ **P1/P2 批量**:child-process 凭据隔离(`sdk.env` 补 `GAH_CB_*`/`AWS_*`/`*_KEY`/`*_APIKEY`/`*_API_TOKEN`/`*_AUTH_TOKEN`/`*_SESSION_TOKEN`/`*_PASSWD`/`*_PRIVATE_KEY`/`*_ACCESS_KEY(_ID)`/`*_PAT` 滤除 + `SanitizedChildEnv`,`host-bridge`/`mcp-bridge`/`host-docview` 全部 spawn 点改净化 env,外部插件确需凭据由 `GAH_EXT_ENV_PASS` 点名放行);工具级超时 `sdk.ToolDefinition.TimeoutMs`(shell 65s / web 35s)覆盖桥默认 3s,RPC 调用改 `cl.Go`+定时器(不再泄漏阻塞 goroutine);shell/pty 独立进程组 + `WaitDelay` + 子进程回收(修 `sleep 300 &` 持 stdout 永不返回、pty 僵尸/孤儿);`host-jobs` 输出限长 1 MiB;`host-agent-loop` 回合状态对象化(`turn`)+ 回合串行化 + 步数耗尽**显式失败**(此前静默记为 `done` 返回 nil)+ 会话 Append 错误不再静默丢;前端 SSE 白名单补 `question|doc|questiondone|confirmdone`(**此前 4 帧在 SSE 降级路径被静默丢弃**,App.vue 的 `questiondone/confirmdone` 订阅形同虚设)+ `types.ts` FrameType 同步;`internal/providerfile`/`internal/prefs` 落盘改 tmp+rename 原子写;`core/config` `LoadLatestBackup` 错误不再吞;`catalogue` 补 `ctx.question` 声明(host-confirm-fusion 实际提供未登记);死代码 + IM 残留注释清理;`~/.gah` 兜底解析链彻底收敛(新增 `sdk.Home()` 作为插件侧唯一数据根入口,7 处 GAH_HOME 空兜底 `~/.gah` 全改 TempDir —— 与 R5/R8 纪律一致;用户输入的 `~` 展开不动);`extplugins/tool-mcp/main.go` 先存 gofmt 差异修正(注释缩进;连带重跑 extplugins 产物)。
> ⑥ **静态分析入 CI**:`staticcheck@2026.2.1`(版本固定,本地/CI 一致)接入 `.github/workflows/ci.yml`(go vet 之后);全仓告警 **27 → 0**:死代码删除(测试辅助 `writeTemp`/`entriesOf`/`const p3`/`noRaster` 字段)、测试 nil context 改 `context.TODO()`(memory/todo/exakey)、`S1016` 结构体字面量改类型转换(`agentloop.go`/`tui/state.go`)、`SA4006` 死赋值(host-system-prompt `ex`、tui 滚动指标)、`SA4023` 恒假判定(docview 接口 nil 判断改编译期断言)、`SA1019` 弃用 `filepath.HasPrefix`、`S1029/SA6003` `range []rune(s)` 改 `range s`。
> ⑤ **验证**:`go build ./...` / `go vet ./...` 干净;全库 `CGO_ENABLED=0 go test ./... -race -count=1` 全绿;新增回归测试——`plugins/policy/policy-guard/pathpolicy_test.go`(symlink 逃逸写读双拒 / 凭据 deny-list / `CheckPathArgs` 表驱动 / `shellCommand` 解码必要性 / **默认发行态 `sb=nil` 下 guard veto 越界 `file_*` 的 P0 证明**)、`core/plugin/lifecycle_test.go`(卸载归还服务 + 重载 + 启动失败回滚)、`web/guard_test.go`(CSRF/Host rebinding/Content-Type/安全头/token cookie 引导/档位校验)、`sdk/env_test.go`(扩展凭据模式 + 子进程净化)、tool-shell/tool-files 路径与超时用例;`vue-tsc --noEmit` 0 错 + `npm test` 14/14 + `vite build`(web/dist 重建);**外部插件产物重跑**(`scripts/gen-extplugins.sh` 五平台 ×4 件,tool-basic 含本轮 tool-files/tool-shell/sdk 改动,重跑两份 diff 为空 = 确定性);`scripts/size-check.sh` 体积门通过(darwin/arm64 二进制 42.62 MiB < 46 MiB,gz 27.76 < 30)。
> ✅ **R10 补跑审计切片(2026-11-14;首轮 6 片中 3 片因上游 429 失败,本次重跑 data-io / docview-ext / tui-front 并全量修复)** 已交付:
> ① **数据落盘与 IO 一致性(data-io)**:会话**路径穿越**(`cwdsessions.Open` 未校验 id,`filepath.Join` 会 Clean 掉 `..` 段 → 会话 jsonl 可写到 `$GAH_HOME` 之外;同文件 `Delete` 早已校验,`Open` 漏同一防线)/ **广播元数据恒零值**(`Log.Append` 把 Seq/TS 回填在**副本**上 → Web 帧 `ID=0`/`TS=0`,前端游标永不推进,每次 WS 重连全量重放 + 时间戳 `00:00:00`;`appendLocked` 改返回回填后事件,广播与落盘同源)/ **配置自愈一次性**(boot 成功后 `SaveBackup` 存的是**启动失败的那棵树**,首次自愈即覆盖唯一好备份,第二次回滚必然失败;`bootWithRecovery` 回传生效树 + 原子写)/ **数据根分裂**(`providerfile`、`tool-auto-plan` 仍走 `UserHomeDir()/.gah` → 统一 `sdk.Home()`)/ **覆写式索引非原子**(`workspaces.json`/`names.json`/`fork-tree.json` 直接 `os.WriteFile`,半截即整表消失;统一 `writeIndexAtomic` + 坏 json 隔离为 `.corrupt-<ts>` 而非当空表)/ `Service.key/path/current` 跨 goroutine 无锁(加 RWMutex + 访问器)/ `sessionlog.Load` 错误路径不原子 + **jsonl 尾部残行**(断电半写)会与下一条 Append 粘成坏行 → 读成功才提交状态 + 残行修复(能解析补换行、否则截断;会话文件与 history sidecar `0644→0600`)/ prefs 读-改-写共享快照(进程内互斥 + `Update` 锁内重读再改)/ 附件失败路径遗孤(统一回收已落盘文件)/ `extractTarGz` 只拦前缀 `../`(改 `filepath.Rel` 段级归属判定)。
> ② **外部进程与文档链路(docview-ext)**:host-docview **资产表无锁并发读写(P0)**——`fatal error: concurrent map writes` 属运行时 throw,任何 recover 都救不回,直接崩宿主(web 每请求一 goroutine 为真实触发面)/ 回调 `jobs.kill` 成功路径 **nil error 解引用**(`JobService.Kill` 契约成功返回 nil → net/rpc handler panic 崩宿主)/ `host-jobs` 命令任务与 docview 转换器缺 `WaitDelay`(转换器另缺进程组):逃逸孙进程持住 stdout → 任务永停 `running`、预览请求挂死 / `fanout.Parallel` **无并发宽度上限**(模型可驱动的 1000 项 = 1000 个 ReAct 并发;`maxParallel=8`,超限显式报错不静默截断)/ 桥默认 3s 超时误杀长 MCP 工具(MCP server 不声明 timeout_ms → 统一声明 120s;超时与"不可达"文案区分)/ 桥 `Execute` 改走 `rpcCall`(调用方 ctx deadline 收紧超时,不再用 goroutine 包同步 Call)+ 结果 8 MiB 上限 / 桥卸载与崩溃重建撞车会**复活**进程与工具注册(`closed` 双检 + `closeAll` 短锁摘条目、锁外优雅关闭)/ `mcp-bridge.call` 持锁阻塞 `ReadBytes`(server 卡住即挂死整回合且取消无效)→ 独立读线程 + `select ctx.Done()` 可中断(JSON-RPC 有 id,残留响应由后续调用按 id 跳过)/ 转换器输出 64 KiB 封顶 + 资产实际读取按声明尺寸 `LimitReader` 封顶 / `web/doc.go` 缓存 `*DocView` 就地改写改浅拷贝 / **host-bridge 外部插件进程独立进程组 + 组杀**(否则宿主退出后插件派生的 MCP server 变孤儿继续占用端口/资源)。
> ③ **TUI / 前端(tui-front)**:**WS 单帧 64 KiB 上限**(工具结果全文入帧,读一个 >64KB 文件即断流 → 前端退避重连三次全失败后永久降级 SSE;长度头补 64 位分支,RFC 6455 §5.2)/ **主题对主要前景色完全无效**(`fg()` 固化在包级 `lipgloss.Style`,仅 `ApplyTheme` 改 map;包级样式改指针 + `rebuildStyles` 原地重建,`init`/`ApplyTheme`/`ResetTheme` 触发;测试只断言 `colorVal` 故永远绿)/ Web 输入框与改名框 **Enter 未判 IME 组字**(CJK 上屏即被当提交;三处补 `isComposing`/`keyCode===229` 守卫)/ TUI **丢弃所有制表符**(代码/补丁缩进全失,整行仅 `\t` 时物理行消失 → 按 8 列 tab stop 展开)/ 结构化提问弹层**无关闭路径**(加"跳过",回传空答案走取消语义)/ 斜杠命令 `strings.Fields` 分词无引号转义(含空格参数被打散;新增 `sdk.SplitArgs`/`JoinArgs`,TUI 命令 + 补全/向导 + Web `runCommand` 共用)/ **慢消费者丢会话帧无自愈**(游标 replay 只在连接重建时发生 → 丢会话帧即摘流关闭,让客户端按 `after` 重连重放补齐,非会话帧丢弃无害)/ `cancelFn` 数据竞争(Esc 取消改 `atomic.Pointer`)/ 按字节截断切裂多字节字符(摘要/全文/参数改 rune 截断)/ `/export` 帮助文案承诺 HTML 而实现只写 JSONL(文案改 jsonl)/ `stream` 槽位契约声明 `SessionEvent[]` 而宿主实传 `Msg[]`(契约类型对齐)/ QuestionDialog 等三处引用**未定义设计 token**(`--r-btn`/`--bg-soft`/`--on-accent`/`--border` 静默失效 → 正名 `--r-input`/`--bg2`/`--fg-on-accent`/`--line`)。
> ④ **顺带收敛**:`sdk.AppendJSONLine`——memory/todo 的 jsonl 追加与 sessionlog 同类缺陷(残行粘行 → 读侧按行解析失败 → 两条记录同时静默丢失),新增共享助手(残行不可解析则截断到上一完整行、缺换行则补换行),两插件去掉重复实现。
> ⑤ **验证**:`go build ./...` / `go vet ./...` / `staticcheck` 干净;全库 `CGO_ENABLED=0 go test ./... -race -count=1` 全绿(交叉编译 windows/amd64、linux/amd64 通过);新增回归:sessionlog(广播回填 Seq/TS、残行修复×2、读失败保持现状)、cwdsessions(非法 id 拒绝、损坏索引隔离、索引原子往返)、prefs 并发字段保留、web 附件失败回收、backup 中间段越界、docview 资产并发读写(P0 证明)、fanout 宽度封顶、回调 jobs.kill nil error、WS 64 位长度头、慢消费者摘流、主题渲染变色(旧实现下必失败)、制表符展开、`sdk.SplitArgs`/`JoinArgs` 表驱动 + round-trip、`sdk.AppendJSONLine`(残行截断/补换行/正常/新建);`vue-tsc --noEmit` 0 错 + `npm test` 14/14 + `vite build`;extplugins 五平台 ×4 件产物重跑。
> ✅ **R10 能力化路径裁决(2026-11-14,关闭已知未闭环 ④「工具能力声明缺位」)** 已交付:路径沙箱从「工具名表」升级为「能力声明优先」。
> ① **sdk 能力契约**:`sdk.PathAccess`(read/write)+ `sdk.PathParam{Arg, Access, Many, Optional}` + `sdk.ToolDefinition.PathParams []PathParam`(可选字段)——只走宿主↔插件协议,**不下发模型**(适配层仅取 Name/Description/InputSchema,零 token 成本)。
> ② **policy-guard 声明优先**:新增 `SandboxPolicy.CheckToolCall(name, args, params)`,`CheckPathArgs` 退化为「无声明」入口;声明非空以声明为准(支持自定义参数名、字符串数组 `Many` 逐元素裁决、`Optional` 缺省跳过),声明为空/缺失**回退内置名表**——内置工具零改动,且「声明空」无法绕过已知工具的裁决,不引入降级面;参数类型不符 / 必填路径参数缺失 / 空路径 → **显式报错 veto**(不静默放行)。guard 侧经 `declaredPathParams(c, name)` 每次现取 `ctx.tools`(同 `ctx.confirm` 现取先例:host-tools 与本插件无拓扑顺序约束,一次性注入会恒 nil)。
> ③ **内置工具自述**:`tool-files` 四件套(`file_read` 读 / 其余写,`fileAccess()`)与 `tool-doc`(`read_document`/`doc_open` 的 `file_path` 读;`doc_list` 的 `path` 读 + `Optional`)补声明,外部化(ToolDefinition → `serve.go` td → `defDTO` → 宿主注册表)全链透传。
> ④ **桥协议透传**:`defDTO`/`td` 增 `PathParams`(旧单工具协议经 `json.Unmarshal` 进 `sdk.ToolDefinition` 天然携带,新多工具协议逐字段映射);外部插件声明能力**零额外协议变更**。
> ⑤ **验证**:新增 `pathpolicy_test.go` —— `TestGuardVetoesDeclaredPathOfCustomTool`(自定义名 `save_note` 声明写路径 → 越界写被宿主 veto 且工具未执行)、`TestGuardDeclaredManyAndOptional`(数组逐元素 / 可选缺省放行但给出越界值仍拒)、`TestCheckToolCallUnit`(声明优先 / 名表兜底 / 必填缺失与类型不符显式失败 / 未声明未知名不受拦的诚实边界);`host-bridge` DTO 往返由既有测试与编译期字段校验覆盖。文档:`docs/PLUGIN_DEV.md` 新增 §2.6「路径参数声明(涉及文件路径的工具必读)」+ 提交检查清单项。全库 `-race` 绿。
> ✅ **R10 执行可中断(2026-11-14,关闭已知未闭环 ⑥「外部工具执行侧不可中断」)** 已交付:外部工具调用从「只传参数」升级为「可取消调用契约」。
> ① **协议**:`ExecArgs`/`ExecNamedArgs` 增 `CallID`(宿主生成,`<插件名>#<序号>`)与 `TimeoutMs`(插件侧执行超时);新增 `CancelArgs{CallID}` + `Plugin.Cancel` RPC(gob 按名匹配 ⇒ 旧插件忽略未知字段,零破坏)。
> ② **插件侧**(`serve.go`):`toolServer` 增 `running map[string]context.CancelFunc`(mu 保护);`callContext` 按 CallID 登记取消、`TimeoutMs>0` 时用 `context.WithTimeout`;`Cancel` 总是写回命中判定(未命中/已结束 = false,不报错),登记项由 `exec` 的 defer 清理(幂等);`toolServerBridge.Server` 初始化 running 表。
> ③ **宿主侧**(`bridge.go`):`toolRPCClient.Execute` 生成 CallID、下发 80% 超时(让插件正常超时下先回明确错误);新增 `rpcCallCtx` —— `ctx.Done()`/宿主计时器触发时**先发 `Plugin.Cancel`(2s 上限,尽力而为)再回错误**,响应写入带缓冲 done channel(不泄漏 goroutine,同 `rpcCall` 纪律);取消回 `外部插件调用已取消(context canceled)`,超时错误与既有文案区分。
> ④ **顺带收益**:取消沿调用链继续下传 —— `tool-mcp` 的外部 MCP 调用、`host-jobs` 取消任务(若其 ctx 传入工具调用)都因此获得真正的插件侧中断,而非仅宿主不再等待。
> ⑤ **验证**:新增 `plugins/host/host-bridge/cancel_test.go` —— `TestToolServerCancelSemantics`(命中并中断 / 未命中返回 false / 结束后登记项清理)、`TestToolServerTimeoutPropagates`(TimeoutMs 传导到插件 ctx);`TestExternalToolCancelInterruptsProcess` **真实外部进程全链路**:宿主 cancel → Cancel RPC → 插件内工具立即中断(以插件写的 cancelled 标记文件为证,插件声明 30s 超时以排除"宿主超时"这一替代解释,并断言未跑到自然结束)。文档:`docs/PLUGIN_DEV.md` §4.1 桥协议补「执行可中断(必读)」——插件 `Execute` 必须尊重 ctx。全库 `-race` 绿。
> ✅ **R10 docview 资产窗口淘汰(2026-11-14,关闭已知未闭环 ⑦「资产表非 LRU」)** 已交付:资产表从「进程退出前只增不减」改为「按最近预览文档窗口淘汰」。
> ① **淘汰策略**(`service.go`):`previewDocs` 最近预览文档窗口(前 = 最新,去重上浮);`Preview` 在抽取前 `notePreview(abs)` —— 窗口外文档的资产**整篇淘汰**(`deleteDocAssetsLocked`),窗口上限 `maxAssetDocs=8`,硬上限 `maxAssetEntries=2048`(超出时继续从最旧文档起淘汰,**永不动最新一篇** —— 用户正在看的那篇必须完整,否则预览内图片整片 404)。
> ② **缓存联动**:淘汰某文档资产时同步 `docCache.invalidatePath(doc)`(新增)——否则缓存视图会引用已淘汰的资产 ID(取回 404);因资产 ID 是 (路径, part) 的确定性哈希,**重抽取会以同一 ID 重新登记**,故"淘汰+重抽取"后前端 URL 仍稳定。锁序:先 `assetsMu` 后 `cache.mu`(cache 侧无反向持锁路径),淘汰动作在释放 `assetsMu` 后执行。
> ③ **取舍说明**:资产条目是**元数据**(`assetRef{kind,doc,abs,part,mime}`,不含字节;字节在 `Asset()` 取回时经 `limit.go` 按声明大小限流读取),所以这里解决的是"大量不同文档的元数据/句柄常驻到进程退出",而非字节内存;窗口因此给得宽松(8 篇),以免影响正常翻页浏览。

> ✅ **R10 覆盖率门与 CI 盲区修复(2026-11-14,承接「继续按推荐依次修改」第 ③ 项)** 已交付:覆盖率从"只有水位没有门"升级为"逐包棘轮 + 全局下限",并补上 CI 从未跑过的 sdk module。
> ① **新增 `scripts/coverage-check.sh`(阈值单一事实源)**:`bash scripts/coverage-check.sh [profile ...]`;无参自采(根模块 + sdk module 两个 profile,`CGO_ENABLED=0 go test -coverprofile`),有参复用 CI 的 profile。三条判据:**棘轮表**(48 个关键包逐包下限 = 实测留 5~8pp 余量,回退即失败;**棘轮包未出现在 profile** 也失败 —— 包改名/拆分必须同步表)、**全局下限**(总覆盖率 ≥65%,实测 69.7%)、**零覆盖包禁止**(非豁免包 0% 直接失败,防新增无测试包);**豁免表**必须写理由(bundles 装配层 / extplugins 独立二进制 / scripts / tests 支撑 / catalogue 纯声明 / ui-* 启动胶水)。输出按覆盖率升序打印全表 + 豁免理由,通过打印 `COVERAGE_OK`,失败打印 `COVERAGE_FAIL` 并退出 1(反例已验证:提高下限、只喂 sdk profile 均正确失败)。
> ② **CI 盲区修复(本轮发现的真实缺口,P1 级)**:`sdk` 是**独立 Go module**(`sdk/go.mod` + `go.work`),而 CI 的 `go vet ./...` / `staticcheck ./...` / `go test -race ./...` 全部从根模块执行 —— **`./...` 不跨 module**,故 sdk 的校验与单测(含本轮新增的 `env_test.go`/`jsonl_test.go`/`slashargs_test.go`)在 CI **从未跑过**。已把三步均补 `(cd sdk && …)`,覆盖率 profile 一并产出供门复用。
> ③ **补测(host-internal-commands 14.7% → 42.0%)**:新增 `plugins/host/host-internal-commands/commands_exec_test.go` + `stubs_test.go`(以内嵌接口的轻量桩替代手写全量桩),覆盖 `/sandbox`(三档 + 非法档 + 未装配)、`/settings history`(off/unlimited/N/负值)、`/export`(jsonl 逐行事件 + html 自包含 + 无路径回事件数 + 未装配)、`/stop`(空闲/运行中取消/未装配)、`/compact`(folded=0/摘要归一空白/错误透传/非 CompactService)、`/session`(list/current/new/switch 的 main 归一/显示名优先/未装配/非法子命令)、`/reload`(热重载成功/失败保留旧值/未实现 ReloadableInstructions)与纯函数(摘要截断/打码/来源短名/模型备注/会话描述/时间格式)。
> ④ **未做(诚实登记;已于「R10 覆盖率补强」批次补上,见下)**:`plugins/catalogue`(纯声明,一致性守卫已由既有 `catalogue_test.go` 承担)、`plugins/ui/ui-web-app`(9.6%)、`cmd/gah`(34.9%)、`host-bridge`(51.8%)等仍属薄弱区,已用棘轮锁住不再回退;后续补测按「端到端优先于逐函数」推进。
> ④ **验证**:新增 `plugins/host/host-docview/asset_evict_test.go` —— `TestAssetEvictionByDocWindow`(窗口外整篇淘汰 / 窗口内保留 / 重复预览只上浮不新增)、`TestAssetEvictionHardCap`(单篇超量不大卸当前篇;新篇进入后超量旧篇整篇淘汰)、`TestAssetEvictionInvalidatesCache`(淘汰同步失效缓存视图,窗口内文档缓存不受影响)、`TestAssetEvictionKeepsCurrentPreviewAssets`(端到端:预览后滚动窗口,当前文档资产仍可取回);既有 `TestAssetConcurrentAccess`(P0 并发回归)与 `TestAssetRoundTrip` 继续绿。全库 `-race` 绿。
> ✅ **R10 shell 路径化(2026-11-14,关闭已知未闭环 ①「workspace-write 下 shell 可写任意路径」)** 已交付:路径沙箱从「只管 `file_*` 参数」扩到「shell 命令的**显式写目标**」。
> ① **实现**:新增 `plugins/policy/policy-guard/shellpaths.go`(极简 shell 词法:引号/转义/`;` `|` `&&` `||` 与换行分隔、fd 前缀重定向(`1>`/`2>`/`>>`/`&>`)、`<<` heredoc 正文跳过、`$( )` 识别、写命令表(`rm/mv/cp/install/mkdir/touch/truncate/sed -i/tee/dd of=/chmod/chown/ln/tar -x/rsync` 等),`bash -c` 递归一层)+ `SandboxPolicy.CheckShellCommand`,接入 `guard.go` 的 shell 分支(顺序:**危险模式审批 → 工具级审批 → 路径裁决**,与 `file_*` 一致)。
> ② **语义(用户可见,勿误解)**:审批通过**不等于**放开档位 —— workspace-write 下 `rm -rf /tmp/x` 即便人工点「允许」仍被拒(错误信息引导切 `/sandbox full`);只读档拒绝一切显式写目标;full-access 放行。读目标只过凭据 deny-list(不做工作区归属限制,否则 `cat /etc/hosts`、编译器读 `/usr/include`、`ls /tmp` 会被大面积误伤)。
> ③ **不可裁决即拒绝**:含 `$VAR`/反引号/`$( )`/`{}` 占位符的写目标、以及 `cd` 出工作区后的相对路径 → 显式拒绝(提示改写为确定路径或切 full)。误报护栏已入测:`/dev/null`/`/dev/tty`/`-`、glob 前缀裁决(`./build/*` 放行 vs `/tmp/*` 拒绝)、`cd sub && …` 放行 vs `cd /tmp && …` 拒绝、`grep -rn id_rsa .` 与 `echo credentials` 不被当作路径。
> ④ **遗留边界(诚实登记)**:间接写入不完全覆盖 —— 构建缓存、`go build -o`/`gcc -o`/`curl -o`、包管理器、`git clone` 目标、`run_code`/`lisp_eval`、以及变量拼出的命令文本(`CMD='rm …'; $CMD`)仍靠危险模式 + 审批档兜底(与未闭环 ② 同款,「审批是确认、不是授权」)。**内核级沙箱已交付**(2026-11-14 第 3 组:macOS seatbelt / Linux Landlock 在进程树层面限制文件写,见下方「内核级沙箱(①-E)+ 写目标表扩充」),故 macOS/Linux 上「判不出的写」已被兜住;仅 Windows 仍是纯协作式控制。
> ⑤ **验证**:新增 `shellpaths_test.go`(词法 / 写读扫描 / 模式矩阵表)+ `shellguard_test.go`(装配级;主导用例用**非危险**命令 `echo hi > <越界路径>`,故否决只可能来自新层 —— 证明裁决来源;危险用例配「同意」确认通道并断言错误文本来自路径层);`approval_test.go` 1 行适配(审批用例钉 `full-access` 以隔离两层)。policy-guard 171 用例 `-race` 绿,包覆盖率 88.2% → 88.7%。**无 sdk/工具协议改动 → extplugin 无需重生成**。

> ✅ **R10 Web 全表面鉴权(2026-11-14,关闭已知未闭环 ③「静态资源不鉴权」)** 已交付:token 模式从「只保护 `/api/*`」收紧为「**全表面**鉴权」,并删除入口页自动下发 cookie 的旧引导。
> ① **门收紧**:`authMiddleware` 覆盖 `/api/*`、静态资源、`/attachments/`(用户附件)、`/ui-plugins/` 产物;缺凭据时 API → 401、其他 GET/HEAD → **引导页**、非 GET/HEAD → 401。唯一豁免 `POST /api/auth`(凭据在 body/Bearer,仍受 Host 白名单 + 同源校验)。
> ② **引导改 fragment**:新增 `web/bootstrap.go`(纯内联引导页:零外部资源、零业务数据,CSP `default-src 'none'`;`Cache-Control: no-store` + `Referrer-Policy: no-referrer` + `X-Frame-Options: DENY`)。启动日志给出 `http://…/#token=<token>` 地址 —— 凭据在 **URL fragment**(浏览器不发往服务端,故不进访问日志/Referer),页面 POST `/api/auth` 换 HttpOnly + SameSite=Strict cookie 后 `location.replace(path+query)` 抹掉 fragment(保留 `?shell=desktop` 等深链)。`data.auth_token` 为空时行为与改动前完全一致(回归保护)。
> ③ **配套**:`ui-web-app` 的 `OnReady` 恒设(非 token 模式也打印访问地址;token 模式打印带 fragment 的地址,`open_browser=false` 也能从日志取到入口);桌面壳读 `GAH_WEB_TOKEN`、导航 `#token=` 地址、就绪探测把 **401 也视为就绪**、停机 POST 携带 cookie;`config/bundle-web.yaml` 与 seed 注释、README 双语、`docs/VERIFY.md` 清单、`docs/DESKTOP_FEASIBILITY.md` 陈旧断言同步。
> ④ **测试**:`web/bootstrap_test.go`(哨兵文件证明 `/`、`/index.html`、`/app.js`、`/attachments/…`、`/ui-plugins/…` 无凭据拿不到任何内容(只得到引导页,且引导页**不内嵌 token**),有 cookie 正常、错 cookie 拒绝;`/api/auth` 的 204/401/405/跨源 403;空 token 回归;`?token=` 仍不生效)+ `guard_test.go` 断言入口页**不再下发 cookie**。web 包 80 用例 `-race` 绿,覆盖率 68.3% → 70.1%;`cargo check` 通过(仅存量 6 warning)。
> ⑤ **顺带修复(本轮测试暴露的真实缺陷)**:`web.Server.Start()`(插件 goroutine)无锁写 `s.http` 与 `Shutdown()` 读并发 → **数据竞争 + 卸载后孤儿监听(端口泄漏)**;已用 `lifeMu` + `closed` 标志收敛(Start 发布 `s.http` 前自检 `closed`,已卸载即关闭 listener 放弃监听),新增 `TestShutdownBeforeStartLeavesNoOrphan` / `TestStartShutdownConcurrent`(20 轮)回归保护。

> ✅ **R10 覆盖率补强与棘轮上调(2026-11-14,承接覆盖率门第 ④ 项「薄弱区补测」)** 已交付:薄弱包按「端到端优先」补测,并把棘轮**同步上调**(棘轮是防回退下限,不是目标值)。
> ① **补强**:`plugins/catalogue` 0% → **100%**(真实执行 `RegisterAll` + manifest 拷贝隔离 + 工厂↔id/`Name()` 一致性)、`host-internal-commands` 42.0% → **95.9%**、`host-system-prompt` 56.9% → **95.4%**、`host-backup` 58.4% → **87.1%**、`cmd/gah` 34.9% → **56.5%**(profile→bundle→patch 加载 / 装配启动 / `bootWithRecovery` 自愈**回传生效树**回归保护 / headless 输出 / `gah doc` CLI 边界)、`plugins/ui/ui-web-app` 9.6% → **79.5%**(最小 `sdk.Ctx` 桩跑真实启动链),`web` 68.3% → **70.1%**、`host-bridge` 51.8% → **87.7%**(宿主回调通道真实 TCP+net/rpc 全链、外部插件侧协议/DTO 直调、进程内扮演外部插件驱动宿主代理:声明透传/旧协议回退/结果超限拒绝/超时≠断连/卸载不复活/命令级联/环境放行矩阵)、`host-jobs` 48.2% → **96.5%**(插件接线 + 两级级联 + `/jobs` 全分支 + 输出上限 + Stop 幂等 + 并发)。
> ② **棘轮上调**:cmd/gah 30→50、web 60→65、host-backup 50→80、host-internal-commands 40→88、host-system-prompt 48→88、host-bridge 45→80、host-jobs 42→88、新增 catalogue 70 与 ui-web-app 70;豁免表同步移除 catalogue/ui-web-app(不再无意义豁免)。
> ③ **测试纪律**:所有新增用例断言真实不变量(失败即代表回归);依赖本机外部二进制才能过的用例不入 CI(如 `gah doc` 光栅成功路径需 poppler/LibreOffice,仍由 `GAH_DOC_CORPUS` 门控既有用例承担)。
> ④ **顺带修复 P0(补测发现的真实安全缺陷)**:**外部插件凭据隔离被 go-plugin 静默绕过** —— `startPlugin` 构造 go-plugin `ClientConfig` 时未设 `SkipHostEnv`,而 go-plugin 会执行 `cmd.Env = append(cmd.Env, os.Environ()...)`,把**整份宿主环境**接在 `sdk.SanitizedEnv` 之后(同名后者胜)→ `*_API_KEY`/`*_TOKEN` 等凭据实际全部下传外部插件进程,R10「子进程凭据隔离」形同虚设(host-jobs/tool-shell 走 `exec.Command`+`SanitizedEnv`,不受影响)。修复 = 该 `ClientConfig` 加 `SkipHostEnv: true`(go-plugin 仍会追加必需的 `PLUGIN_*` 握手键);同时删除测试里的「已知缺陷跳过开关」,`TestExternalPluginCredentialIsolation` 由 SKIP 转为**真实回归保护**(断言子进程环境里凭据键为空值、点名放行键才带值)。**顺带对齐** `cbJobs.Kill` —— 宿主把业务失败写进 reply,此前被忽略(「没杀掉」当成功),现与 `cbFanout.KillAgent` 一致回错(`TestCallbackJobsKillFailureReply` 锚定契约);`externalEnvPass` 注释由「大小写不敏感」改为与 `os.LookupEnv` 一致的**敏感**语义。

> ✅ **R10 剩余规划项第 1 组(2026-11-14,零新依赖)** 已交付:把上一批「已修补但未收敛」的 ②/⑤/① 三项做成**可回归的收口**,并顺带修掉一个真实 P1。
> ① **档位联动可见性(②-1)**:审批档 `open`/`strict` 覆盖沙箱有效档时不再静默 —— `/sandbox`(无参/切档)回显「声明档 → 有效档(联动来源 approval=<x>)」并在被覆盖时提示「该设置暂不生效」;`/approval` 回显「审批: <x>;沙箱有效: <y>」;TUI 状态栏与 Web 状态同步显示**有效档**并标注来源。**Web 状态修复一个既有小缺陷**:`/api/state` 此前报 `sb.Mode()`(声明档)而实际拦截按 `EffectiveMode()` → 显示与行为矛盾;现增量下发 `sandbox_effective`/`sandbox_derived`(omitempty,未实现 `sdk.EffectiveSandbox` 的桩/旧实现两字段均不下发,不把「没覆盖」说成「覆盖了」),前端状态栏据此显示;TUI/宿主命令两侧文案**字节一致**,由 `tests/display_text_sync_test.go` 的「同源两份」护栏钉住(改名/漏同步即失败,已做灵敏度验证)。
> ② **工具执行入口委派不变式(②-4)**:把「所有工具执行都经 `ctx.tools` → `tools/pre-execute`」从实现约定升为测试 —— 5 个入口(`host-tools` registry、`tool-workflow` 嵌套调用、`host-fanout`、`mcp-server`、`host-bridge` 宿主侧 `toolsCall`)各一条**委派契约测试**(注入录制/veto 桩 registry:参数逐字透传、veto 以 `blocked:` 回显、工具清单来自 registry、不可达路径零调用),加 `tests/policy_entries_e2e_test.go` 端到端三条:① agent-loop 真实回合里模型请求的越界写被拦(文件不存在 + 结构化错误投影为 RoleTool 消息),**再切 full-access 同命令必须放行**(拦截归因,防误伤);② registry veto 必须让工具**不产生副作用**;③ 有效档派生契约(smart→workspace-write / open→full-access / strict→read-only,且**声明档不被改写**)。纪律同步:`docs/PLUGIN_DEV.md` §2.7「工具执行唯一入口」+ 检查清单。
> ③ **UI 插件纵深与信任模型(⑤-1/⑤-2)**:SPA 静态面加严格 CSP(`default-src 'none'` + 显式白名单,`connect-src 'self'`,仅有据放行 `frame-src 'self'`/`style-src 'unsafe-inline'`/`img-src blob:`;`/api/*`、`/attachments/`、文档端点的全局头不受影响),切断「已安装 UI 插件纯外发」通道;`/api/ui-plugins` 每项带 `trusted`/`trust_note`、设置面板照显,文档写明**UI 插件与主应用同源同 realm = 同权限**(可经 `/api/input` 达工具执行 → 等同本地代码执行),只安装可信插件(§4.2)。
> ④ **shell 间接写入收敛(①-A/①-B)**:①-A **环境 jail**(档位无关:`TMPDIR`/`TMP`/`TEMP`、`XDG_CACHE_HOME`、`GOCACHE`、`GOMODCACHE`、`npm_config_cache`、`PIP_CACHE_DIR` 恒重定向到 `$GAH_HOME/jail/**`;缺失键**补上**,否则 Go/Node/pip 回落家目录使 jail 形同不设;`HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` **刻意不动**以照常读 git/ssh 配置;目录 0700、创建失败**显式报错**、`GAH_SHELL_JAIL=0` 可关;`jail/tmp` 每次顺带回收 24h 前条目,缓存根复用),把「无界的间接写」收敛成「数据根内可盘点」;①-B **输出型写目标纳入裁决**(`curl -o|--output`、`wget -O`、`gcc/clang/cc -o`、`go build|install -o`、`pip install -t|--target`、`npm install --prefix`、`tar -C`、`unzip -d`、`git clone <repo> <dir>` 位置目标;分离/紧贴/`--flag=value` 三形态;`-C` 换 cwd 后的相对目标判为不可裁决并拒绝,只增拒绝面、不放松读凭据判定)。独立抽查(直接调 `CheckShellCommand`)确认 10 类越界写全拒、工作区内放行、`curl -T ~/.ssh/id_rsa` 仍拒;诚实边界见 PLUGIN_DEV §2.6(解释器内部写、`ccache`/`make` 等包装器、`go install` 无 `-o`、外部进程写)。
> ⑤ **顺带修复 P1(守护栈下文档预览被拦)**:全局护栏给所有响应加 `X-Frame-Options: DENY` + CSP `frame-ancestors 'none'`,而文档三端点(`/api/doc/raw` PDF / `asset` 转换产物 / `html` 沙箱预览)未覆写 → 生产栈(`gah web`)里 DocPanel 的**同源 iframe 预览会被浏览器直接拒绝**(既有单测走 `handler()` 不含护栏,测试看不见)。修法 = 新增 `setDocInlineFrame`(XFO `SAMEORIGIN` + `frame-ancestors 'self'`),HTML 预览的 `default-src 'none'` 沙箱策略不放宽;新增**守护栈**回归 `TestDocInlineFrameHeadersGuarded`(三端点同源可嵌 + HTML 沙箱不放宽 + SPA 面保持 `DENY`)。
> **验收**:全库 `go test ./... -race` 绿(含 sdk module);`gofmt`/`vet`/`staticcheck` 双 module 干净;覆盖率门 **COVERAGE_OK**,棘轮按实测上调 10 项(web 65→68、tui 62→71、policy-guard 80→86、host-bridge 80→84、host-fanout 72→77、host-internal-commands 88→93、host-tools 74→86、mcp-server 80→85、tool-shell 80→87、tool-workflow 66→72);前端 `vue-tsc` + `npm test` 14/14 + `vite build` 通过;CSP 与构建产物相容性反查(无 `eval`/内联脚本/跨源引用);extplugin 按新 `tool-shell` 重生成,两次哈希一致 + 体积门通过。

- **内核级沙箱(①-E)+ 写目标表扩充(第 2/3 组)**:`shell` 现在有**进程树级**文件写限制,协作层判不出的写不再等于缺口。
  - **档位管道**:新增 `sdk.SandboxHint{Mode, Root}`(`WithSandboxHint`/`SandboxHintOf`);执行唯一入口 `host-tools` 在 `tools/pre-execute` 之后注入**有效**档位(`EffectiveSandbox.EffectiveMode()` —— 联动后的档,不是声明档),经 `host-bridge` 协议到插件侧 ctx(`ExecArgs`/`ExecNamedArgs` 各加 `SandboxMode`/`WorkspaceRoot`,gob 兼容)。**空档位/空根 = 未知,不猜**(消费者按不可放行处理);未注入 = 与改动前逐字一致。
  - **macOS**:`/usr/bin/sandbox-exec -p <profile> sh -c <cmd>`;profile = `(allow default)(deny file-write*)(allow file-write* …)`,白名单 = 解析后的 workspace 根 + jail + `/dev/{null,stdout,stderr,tty,ptmx}` + `/dev/fd` + `/dev/ttys*` regex;只约束写,读与网络不限制。`sandbox-exec` 实测是 **fork 而非 exec**(同 pgid),故进程组取消/超时语义不变。
  - **Linux**:Landlock(内核 ≥5.13)。因 Landlock 对进程**不可撤销**,采用**自举 helper**:插件侧探 ABI → `argv = [self, --gah-landlock-exec, <档位>, <根>, sh, -c, cmd]`;helper 在 `init()` 校验档位(非法 `os.Exit(126)`,绝不无约束执行)→ 建 ruleset(仅写类权利;ABI≥2 加 `REFER`、≥3 加 `TRUNCATE`)→ `PATH_BENEATH` 规则 → `PR_SET_NO_NEW_PRIVS` → `landlock_restrict_self` → `syscall.Exec`。`golang.org/x/sys v0.45.0` 只有 syscall 号、无 `LANDLOCK_*` 常量,故常量/结构体本地按内核头定义;`landlock_path_beneath_attr` 在内核里是 packed(12 字节),Go 侧 `{u64; s32}` 布局一致、内核只读前 12 字节。**单文件规则只能用文件级权利**(`WRITE_FILE`/`TRUNCATE`,含目录级权利 → `EINVAL`),故 `/dev/null` 等按单文件规则放行、`/dev/pts` 只能整目录放行(pty slave 名动态),个别项加不上汇总进一次性告警。
  - **能力缺失一律明示**:无 `sandbox-exec` / 无 Landlock / Windows 等 → 一次性 stderr 告警 + 不施加包装;`GAH_SHELL_KERNEL_SANDBOX=0` 全局关闭;`GAH_SHELL_JAIL=0` 时内核沙箱一并跳过(白名单失去锚点,否则 `go build` 类命令会大面积失败)。read-only 档**保留 jail 可写**,否则 `TMPDIR`/`GOCACHE` 断裂会让命令大面积失败。白名单根加不上 → **整体失败**(不许白名单没落地而命令照跑)。
  - **写目标表(第 2 组)**:新增/扩展 13 类命令级裁决 —— `sort -o`、`patch -o`(`-d` 按目录写目标)、`go test -coverprofile/-cpuprofile/-trace`、`cargo --target-dir`、`npm --cache`、`pip --cache-dir/-d`、`gcc -MF/-MJ`(`joinedFlagValue` 取最长匹配,防 `-o` 抢占)、`find -exec/-execdir/-ok/-delete/-fprint`(递归分类内部命令并把 `{}` 代入搜索根)、`mktemp -p/--tmpdir`、`split`(输出前缀=第二操作数)、`tar` 旧式旗标簇(`tar czf`)、`zip`/`7z` 首操作数、`cmake --install --prefix`。**只增拒绝面**:区内写与既有放行构造回归探针 0 偏差。
  - **端到端证据**(父代理独立复核,非子代理自述):`tests/kernel_sandbox_e2e_test.go` 经真实管道(`ctx.tools` → host-tools → 插件)对 `python3 -c "open('<区外>','w')"`(策略层**看不见**的写)断言:文件未落盘 + 错误为内核层 `Operation not permitted` + 区内同类写成功 + 切 `full-access` 后同一命令成功(归因);另一条断言**管道传的是有效档** —— 只把审批档切 `open`(沙箱声明档不动),越界写即放行。两条测试的**灵敏度**已反证:把 `EffectiveMode` 改回声明档 / 让 `kernelWrap` 恒返回空 → 各自变红。
  - 覆盖与产物:policy-guard 90.8%、tool-shell 93.3%、host-tools 91.0%(棘轮 89/90/88);20 件 extplugin 产物重生成(两遍逐字节一致);`gofmt`/`vet`/`staticcheck`(两 module)/全库 `-race`/`./tests/` 全绿;`go mod tidy` 顺带清掉 IM 卸载后已无引用的 `gorilla/websocket`、`rsc.io/qr`,并把 `x/sys` 转为直接依赖。
  - 未闭环(诚实登记):**Linux 分支未经真机验证**(本机仅 macOS;已交叉编译 + vet,CI ubuntu 会因测试改为平台无关探测而**真跑 Landlock**,不可用环境 skip 而非红);**Windows 无内核层**;`cmake --install` 不给 `--prefix`、`npm run --prefix`、`make install DESTDIR=`、`go install`、`curl -O`、`python3 -m venv`、`ccache gcc -o`、二层 `bash -c` 嵌套等仍是表外边界(由内核层兜住,仅错误信息不如协作层精确)。
> ⏳ **R10 剩余规划项方案(2026-11-14 登记;第 1 组零依赖小改已随本批交付 ✅,见上方「R10 剩余规划项第 1 组」;第 2/3 组已实施 ✅,见上方「内核级沙箱(①-E)+ 写目标表扩充」)**:三项剩余均为**设计取舍 / 规划项**(非缺陷),此处登记完整方案与分层,按「零新依赖 → 需新依赖 → 大工程」三组推进。
> **② 审批档↔沙箱联动**。现状(代码事实):`policy-guard/link.go` 在 `sync=true`(默认)时 `approval=open → 沙箱有效档 full-access`、`strict → read-only`、`smart` 不覆盖;`sync` 仅来自插件 config,无运行期开关;`/sandbox <mode>` 设的是**声明档**,被联动覆盖时**静默失效**(不回显不报错);状态展示取 `sb.Mode()`(web/server.go 状态视图、tui 状态栏)而非 `EffectiveMode()` → **显示与行为不一致**(approval=open 时显示 workspace-write,实际 full-access),而 `sdk.EffectiveSandbox` 已存在且 tool-files 已用;工具执行入口 8 处(agent-loop / fanout / workflow / mcp-server / web ×2 / host-bridge 宿主侧 `toolsCall`)已核实**全部**经 host-tools registry → `tools/pre-execute`,无已知绕过点,但**无不变式测试**。选项:②-1 可见性收敛(状态/命令统一走 `EffectiveSandbox`,被覆盖时回显警告;不改语义、无破坏性)、②-2 `/sandbox sync on|off` 运行期开关 + 偏好持久化(中)、②-3 approval 与 sandbox 彻底解耦(破坏性,老用户行为变化,**不做**)、②-4 入口委派契约测试(表驱动断言每个入口都被注入的 `sdk.ToolRegistry` 接管、registry 必发 `tools/pre-execute` 且 veto 真能阻止执行;新增入口须登记)。**推荐 ②-1 + ②-4(第 1 组)**,②-2 视需要。
> **⑤ 外部插件目录可写 / UI 插件信任模型**。现状:外部插件二进制落 `$GAH_HOME/plugins/`、UI 插件落 `$GAH_HOME/ui-plugins/`;UI 插件经 `web-src/src/plugins.ts` 动态 `import()` 载入**主页面同源同 realm**,而 SPA 响应只有 `nosniff`/`X-Frame-Options`、**无 CSP** → 已安装插件可自由外发会话内容,并可用 cookie 调全量 API(含 `/api/input` 投喂 prompt → 触发 shell = 等同本地代码执行);③ 只解决「未授权读取产物」,不解决「已安装插件」。威胁模型结论:**UI 插件 = 同权限可信代码**(与外部插件二进制等价,执行域在浏览器),属设计取舍,缺的是纵深与明示。选项:⑤-1 SPA 加严格 CSP(`default-src 'none'` + 显式白名单,`connect-src 'self'`;已核实前端零跨源引用、WS/EventSource 同源、图片预览用 `blob:`)切断纯外发通道、⑤-2 信任模型明示(`/api/ui-plugins` 增 `trusted`/说明字段 + 面板提示 + 文档写清威胁模型)、⑤-3 插件产物 sha256 完整性提示(可见性而非授权,中)、⑤-4 `iframe sandbox` + postMessage 能力桥 + manifest `permissions`(大工程,仅当 UI 插件成为**网络分发**面才值得)。**推荐 ⑤-1 + ⑤-2(第 1 组)**,⑤-3 次之,⑤-4 不做。
> **① shell 间接写入的遗留边界**。现状:已覆盖显式写目标(重定向 / 写命令操作数 / `sudo·env·xargs` 前缀 / `eval`·`bash -c` 一层 / `~` 展开 / 不可裁决写直接拒),未覆盖:构建缓存与临时目录等**间接写**(`go build`、`gcc`、包管理器)、`-o/-O/-C/-t/--target/--prefix` 类输出 flag、`git clone` 位置目标、`run_code`/`lisp_eval`(当前不存在该类工具)、变量拼出的命令文本。**本质判断:当前是「工具边界的协作式控制」而非进程沙箱** —— 外部进程(MCP server 子进程、host-bridge 外部插件)的写完全在裁决面之外,故「逐 flag 补漏」不可能收敛,必须分层。选项:①-A **环境 jail**(档位无关:恒定把 `TMPDIR/TMP/TEMP`、`XDG_CACHE_HOME`、`GOCACHE`、`GOMODCACHE`、`npm_config_cache`、`PIP_CACHE_DIR` 指向 `$GAH_HOME/jail/…`;`HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` **刻意不动**,git/ssh 正常;目录 0700,创建失败**显式失败**;`GAH_SHELL_JAIL=0` 可关;`jail/tmp` 清 24h 前条目、缓存复用)、①-B **输出型 flag 表**(curl/wget/gcc/go build/tar/pip/npm + `git clone` 位置目标,只增拒绝面)、①-C **命令 AST 解析**(`mvdan.cc/sh/v3/syntax`,纯 Go;消除 `exec 3>f`、`find -exec`、here-string 等漏判,仍需后置依赖;不解决变量展开;**需批准新增依赖**)、①-D **事后检测**(对 `~/.ssh`/`~/.aws`/shell rc/`$GAH_HOME/config` 做执行前后 mtime+size 快照,变更即报告 + 审计;预防→检测补位,不完备但便宜)、①-E **内核级沙箱**(唯一能覆盖**子进程树**的正解 —— **✅ 已交付 2026-11-14,见下方第 2/3 组交付块**:macOS `sandbox-exec -p`(系统自带二进制,Apple 已弃用但可用)+ Linux **Landlock**(可用已有 `golang.org/x/sys/unix` 直接系统调用,无需新 module;需内核 ≥5.13 且 LSM 启用,不可用则**显式拒绝或明示降级**)+ Windows 无等价无特权方案 → **明示不支持**;Landlock 须在 exec 前的子进程内施加(let registry / self re-exec 后再 `syscall.Exec`);成本大且需真实 Linux 验证,建议独立里程碑)、①-F workspace-write 下默认拒绝一切不可静态裁决的写(**不做**:打断 `go build`/`npm install` 等高频流程)。**推荐 ①-A + ①-B(第 1 组)**,①-C/①-D 次之,**①-E 独立里程碑**。
> **推进排期(第 1 组 = 本批,全部零新依赖)**:②-1 可见性收敛 + ②-4 入口委派契约测试 + ⑤-1 SPA CSP + ⑤-2 信任模型明示 + ①-A 环境 jail + ①-B 输出 flag 表。**第 2 组**:①-C(需批准依赖)、①-D、⑤-3、②-2 → **①-C 经 48 例探针实证否决**(控制流/复合命令/重定向已全部正确 DENY,AST 带来 ≈0 增量,故不引入依赖),改为**扩充写目标表 + `find -exec` 递归**;**第 3 组**:①-E 内核级沙箱 ✅ **已交付**(macOS seatbelt / Linux Landlock,见上方第 2/3 组交付块)。**仅记录不做**:②-3、⑤-4、①-F。验收点:②-1 状态/回显与 `EffectiveMode` 一致(web `/api/state` + TUI 状态栏 + `/sandbox`·`/approval` 回显);②-4 每个入口一条委派契约测试 + registry 必发 `pre-execute` 且 veto 阻止执行;⑤-1 CSP 头断言 + 前端回归(SSE/WS/预览);⑤-2 接口字段 + 文案;①-A jail 路径/权限/开关/失败路径用例 + 真实 `go build` 落 jail 的集成用例;①-B 每 flag 正反例 + git clone 单双操作数。

> ⚠️ **R10 已知未闭环(诚实登记,非遗漏)**:① ~~**shell 不做路径化**~~ **✅ 已补(2026-11-14,见「R10 shell 路径化」)**:shell 命令的**显式写目标**已纳入路径裁决(词法级扫描 + 写命令表 + `bash -c` 递归一层 + 不可裁决写目标直接拒);遗留边界:**已于 2026-11-14 第 1 组收敛两项**——构建缓存/临时落点由**环境 jail** 收进 `$GAH_HOME/jail/**`,`-o`/`-O`/`-C`/`-t`/`--prefix`/`git clone` 目标等**输出型写目标**已纳入裁决(见「R10 剩余规划项第 1 组」④);**仍未覆盖**(诚实边界,见 PLUGIN_DEV §2.6):解释器内部写(`python3 -c "open(...)"`)、`ccache`/`make` 等包装器自选落点、`go install` 无 `-o`(GOPATH/bin)、`curl -O`(落 cwd)、变量拼出的命令文本、以及**外部进程**(MCP server / 外部插件)的写;这些靠危险模式 + 审批档兜底,并**已由内核级沙箱兜住**(macOS seatbelt / Linux Landlock,2026-11-14 第 3 组交付:进程树层面限制文件写;仅 Windows 仍是纯协作式控制)。命令 AST(①-C)经 48 例探针实证否决(AST 增量 ≈0,不引入依赖),写目标表已按第 2 组继续扩充;方案与分层见下方「R10 剩余规划项方案」①。② 审批档↔沙箱联动**语义不变**(单向派生:strict→read-only、open→full-access;tool-files 按 `EffectiveMode` 判定),**但可见性已于 2026-11-14 第 1 组收敛**:`/sandbox`、`/approval`、TUI 状态栏与 Web `/api/state` 均回显「声明档 → 有效档(联动来源)」,被覆盖时显式提示「该设置暂不生效」,不再静默失效(见「R10 剩余规划项第 1 组」①);「不走 `tools/pre-execute` 的工具直调」这一不变式**已由测试钉住**(5 个入口的委派契约测试 + `tests/policy_entries_e2e_test.go` 端到端注册点清单,见同条第 ② 项)。方案见下方「R10 剩余规划项方案」②(第 2 组:可选解耦)。③ ~~**静态资源不鉴权**~~ **✅ 已补(2026-11-14,见「R10 Web 全表面鉴权」)**:token 模式改为**全表面**鉴权(静态资源 / `/attachments/` 附件 / `/ui-plugins/` 产物与 `/api/*` 同等对待);入口页不再自动下发 cookie,改 URL fragment 引导 + `POST /api/auth` 换 HttpOnly+SameSite=Strict cookie。④ ~~**工具能力声明缺位**~~ **✅ 已补(2026-11-14,见「R10 能力化路径裁决」)**:`sdk.ToolDefinition.PathParams` 能力声明落地,裁决改**声明优先**(自定义名工具自带路径参数与读写意图)+ 内置名表**保留兜底**(未声明工具仍按表裁决,声明为空无法绕过已知工具)。遗留边界:既不声明、名字也不在内置表的工具仍不受路径沙箱约束 —— 由 `docs/PLUGIN_DEV.md` §2.6(涉及文件路径必读)与提交检查清单约束,属「插件自述即契约」;⑤ 外部插件目录(`$GAH_HOME/plugins/`、`$GAH_HOME/ui-plugins/`)可写 = 用户替换插件即等同宿主代码执行(设计如此;**UI 插件更甚:经动态 import 进主页面,与主应用同源同 realm,可调用全部 API(含 `/api/input` → 工具执行)** —— 纵深已于 2026-11-14 第 1 组落地:SPA 严格 CSP 切断纯外发 + `/api/ui-plugins` 逐项 `trusted`/`trust_note` 明示,见「R10 剩余规划项第 1 组」③;**能力隔离(iframe sandbox)未做**,方案见下方「R10 剩余规划项方案」⑤);其进程**凭据隔离**曾因 go-plugin `SkipHostEnv` 缺省而失效,已于 2026-11-14 修复,见「R10 覆盖率补强」④);`EXA_API_KEY` 默认不再下传外部 `web_search`,推荐改用 `$GAH_HOME/config/search.yaml`,或 `GAH_EXT_ENV_PASS=EXA_API_KEY` 显式放行(README 双语已注)。⑥ ~~**外部工具执行侧不可中断**~~ **✅ 已补(2026-11-14,见「R10 执行可中断」)**:`ExecNamedArgs/ExecArgs` 增 `CallID`+`TimeoutMs`,插件侧按 CallID 登记可取消 ctx,宿主取消/超时经 `Plugin.Cancel` RPC 真正中断外部执行(旧插件无该方法 → 忽略方法缺失,退化为宿主侧超时)。⑦ ~~**同进程 TUI/Web 的 docview 资产表为进程内 map**(非 LRU)~~ **✅ 已补(2026-11-14,见「R10 docview 资产窗口淘汰」)**:资产按「最近预览文档窗口」整体淘汰(窗口 8 篇 / 硬上限 2048 条目,永不动最新一篇),淘汰同步 `cache.invalidatePath` 防缓存视图引用死 ID;资产本身是元数据(不含字节,读取时按需打开),故窗口给得宽松。
> ⏳ **非编程用户可用性(NOND-*,2026-11-14 评估登记;状态回填 2026-09-12:W1/W3/W4、M1 第 1/2/3 步已交付、W2 **已发布 v0.1.0**,见下方 ✅ 块;其余未实施)**:针对「财务 / 自媒体 + Windows」受众完成可用性评估与改造路线,**本批只出方案不实施**——完整方案 `docs/NONDEV_ROADMAP.md`,验收清单 `docs/VERIFY.md` 同名节。结论:当前对非编程用户**不具备实用价值**,定位仍是技术用户的本地 harness;底牌是零运行依赖单二进制 / 自研纯 Go 文档解析(docx·xlsx·pptx·pdf,不依赖 LibreOffice/Python)/ 便携数据根。**P0(不做则用户拿不到东西)**:`NOND-W1` **Windows 上 `shell` 工具依赖 POSIX `sh`**——`tool-shell/shell.go:105` 与 `pty.go:43` 硬编码 `sh -c`,全仓 grep `COMSPEC`/`powershell`/`bash` **零命中** → 无 Git for Windows 的机器上 `shell` 全断(只剩 `file_*`/`web_*`/`doc_*`),而 `policy-guard/shellpaths.go` 的写目标裁决是 POSIX 词法,**故修法 = 探测/兜底一个 POSIX shell(`GAH_SHELL_PATH` > `%ProgramFiles%\Git\bin\bash.exe` > PATH),不做 PowerShell 双语义**;三个隐藏坑必须一并处理:Git Bash 的 **MSYS 参数路径改写**(spawn 需 `MSYS_NO_PATHCONV=1`,否则白名单与实际落点不一致 = 「以为拦住了其实没拦」)、盘符大小写不敏感归一(`D:\repo`)、`internal/embed/embed.go:154` 与 `gen-extplugins.sh:73` 的 windows 外部插件产物**无 `.exe` 后缀**(`exec.Command` 完整路径解析在 win 上**未实测**);`NOND-W2` ~~**无任何 Release**——`git tag` 为空~~ **【评估时快照;✅ 已解决 2026-09-12:`v0.1.0` 已发布,Release 含 mac dmg + win NSIS + updater `latest.json` + 命令行五目标归档与 `checksums.txt`,见 ✅ 块 ⑨】**,而 `desktop/`(Tauri v2 sidecar)+ `release-desktop.yml`(mac aarch64 / win x86_64 / NSIS)+ `publish-desktop.sh` + `docs/RELEASE.md` 均已铺好;`bundle.windows.webviewInstallMode` 按范围决策 ③ 不设置(走 Tauri 默认 `downloadBootstrapper`,安装时联网;真机触发下载再改 `skip`)。**P1**:`NOND-W3` 首启引导 + provider 预设(现状仅 `SettingsPanel.vue` 4 个空输入框 `name/base_url/api_key/model`,无向导无 OAuth → 用户必须自己知道 base_url 与模型名)、`NOND-W4` 定时任务 `host-schedule`(复用 host-jobs 后台作业,缺时间触发源;无人值守必须有明确档位策略「无确认通道 → 危险动作拒」,且新入口须登记 e2e 入口矩阵)、`NOND-M1` MCP 上下文成本(**同一份工具 schema 计费两遍**:`host-system-prompt/systemprompt.go:211` 把全部 schema 用文本写进 system prompt,`host-agent-loop/agentloop.go:232` 又结构化下发 `req.Tools`;此外 stdio-only(无 SSE/Streamable HTTP/OAuth)、仅 `GAH_MCP_COMMANDS` env 配置且改完需重启、无 `/mcp` 命令与 GUI 页——pi 侧解法是单代理工具 + 按需 search + 懒启动)。**P2**:`NOND-W5` IM 单向投递(飞书/企微/钉钉/通用 webhook,纯 Go `HTTP POST` 零依赖;**不做双向对话网关**——IM 线 2026-11-14 已整体删除)、`NOND-B1` 浏览器自动化(与零运行依赖红线冲突,建议 **extplugin 可选形态**,待决策)。**明确不做**:双向 IM 网关、Windows 内核级沙箱(已登记无等价机制)、自研 cron 表达式构造器 UI。**诚实标注**:评估机 macOS,**Windows 侧的「能不能构建/发行」已升级为实测级别**(GitHub `windows-latest` runner 上打包链多轮全绿并产出真实安装包与签名,见 §14.1 NOND-W2 ⑨),但**「装得上、跑得起、SmartScreen 放行」仍是代码证据 + 官方文档级别**(`DESKTOP_FEASIBILITY.md` §8 自列「Windows 真机验证」为未决项),须按 `docs/NONDEV_ROADMAP.md` §7 清单在真机复现后才可当作事实;桌面壳升级会**连同 `gah-data/` 一起被替换**(§2.8 风险 5)属非编程用户的真实数据丢失风险 —— **W2 已落地缓解**(升级前自动备份到 `~/gah-upgrade-backup/<时间戳>/`,失败即取消升级)并在 README(中英)写明「数据在哪/升级前必读」;卸载仍会删除数据,彻底解(数据根移出应用目录)触碰便携纪律,登记 **`NOND-W2b`** 待真机后评估。**范围决策(2026-11-14 用户拍板)**:① IM 场景(`NOND-W5`)与 OAuth **暂缓**,留到最后再决定(方案保留不删,从执行顺序与验收范围移出);② **PowerShell 后置**(`NOND-W1b`)—— 先 Git Bash 兜底再单独立项,不与 `NOND-W1` 混合(避免两套 shell 语义同时进 `shellpaths.go`);③ 其余按评估建议 —— 浏览器自动化维持 P2 + 可选 extplugin 形态、`webviewInstallMode` 不设置(走 Tauri 默认,真机触发下载再改 `skip`);④ 执行顺序 `NOND-W1 → Windows 真机验证 → NOND-W2 → NOND-W3 → NOND-M1 去重步骤(零风险可插队)→ NOND-W4`;`repo 公开`为 W2 前置、需用户操作。**本批交付为纯文档**(本块 + `docs/NONDEV_ROADMAP.md` + `docs/VERIFY.md` 同名节),无代码改动。**补充登记(2026-11-14,通知线)**:`NOND-N1` 通知能力层 + `NOND-N2` TUI 系统级落点(方案见紧随其后的 ⏳ 块)。
> ⏳ **通知线补充登记(NOND-N1 能力层 / NOND-N2 TUI 系统级落点;2026-11-14 评估登记,方案待实施)**:动因 = 现状里「需要人回来的时刻」**没有任何主动提示**,只能靠人盯屏;且桌面壳的通知逻辑已经**按场景各写一份**(`main.rs` 6 个 `async_runtime::spawn`,其中「回合结束」与「定时任务失败」各一个轮询器),每加一个无人值守场景就要再加一个轮询器。**口径澄清(避免按 pi 的形状抄)**:pi 的 `ctx.ui.notify(msg, type)` 是**页内 toast**,pi 全库无 OS 通知 / 响铃 / OSC 实现;故本题真正缺的是两层,而不是「再开一条通知通道」。**现状(代码事实)**:① `sdk/` 全库无 `Notify`/`Notice` 字样 → **插件没有任何提示用户的通道**(想让用户注意某事,只能把文本交给模型转述);② `web/events.go` 帧类型(session/status/error/confirm/question/command/doc/questiondone/confirmdone/schedule)无 `notice`;③ Web 前端无 toast,也未用浏览器 `Notification` API;④ TUI 只有内部 `statusMsg`(`tui/app.go:297`,宿主命令自用,插件拿不到且不显眼),`tui/` 无响铃/Osc 通知(仅剪贴板 OSC52);⑤ headless 只有 stderr + 退出码。
> **N1 · 通知能力层(建议 P1,量级 S~M)**:① 契约 `sdk.Notice{Level: info|attention, Title, Body, Key, Source}` + 服务 `ctx.notices`(`Level` 语义 = `info` 仅应用内提示、`attention` 需人回来);② 宿主实现 = 广播到各端 sink + **进程内环形缓冲(200 条)+ 单调 `seq`**,经 `GET /api/notices?since=<seq>` 增量暴露(**刻意不给桌面壳加 SSE/Rust HTTP 流依赖**:壳已有 `httpGETAuth` 轮询风格与凭据 cookie,增量轮询即可,零新依赖);③ 同时广播 SSE 新帧 `notice`,**三处同步护栏**(`web/events.go` ↔ `web-src/src/types.ts` ↔ `transport.ts`;已有 `tests/frame_sync_test.go` 的集合相等断言可扩展,该护栏正是为「新增帧漏登记 → SSE 降级路径静默丢帧」而建);④ Web 端页内 toast(纯前端、零新依赖、遵循 taste:单 accent 色 + 语义色、无 em-dash、标签在输入框上方);⑤ TUI 端渲染到状态行 + 累计计数。**触发点固定三个(数据源都已存在,不新造)**:① **等待用户回答/确认**(`web/confirm.go`/`web/question.go` 的 `pending` map 已有 `len()` 访问器) —— 最该通知:人不在场会被**卡死**,而桌面壳当前恰好**没有覆盖**这一情形;② 回合结束(壳 `handle4` 的 `state.running` 翻转);③ 定时任务失败(`NOND-W4c` 的 `handle5` 轮询 `/api/schedules`)。**去重与打扰控制**:同 `Key` 冷却(默认 60s)+ 环形缓冲上限 + 每端可关;桌面壳改为**消费通知流**,把 `handle4`/`handle5` 收敛掉,消除「每个场景一个轮询器」。**零新依赖**;新服务未装配时 = 行为零变化。**N2 · TUI 系统级落点(建议 P1',量级 M,唯一需要真机矩阵的部分)**:`tui/notify.go` 探测 + **逐级降级(不写死支持矩阵)** —— 探测 `KITTY_WINDOW_ID`/`TERM_PROGRAM`/`WEZTERM_PANE`/`WT_SESSION`/`VTE_VERSION`/`TERM` + `TMUX`/`STY`;落点优先级 **OSC 99**(kitty:支持 `p=?` **主动查询探测**;`o=unfocused` 可让**终端**做在场抑制,绕开「应用无法判断自己是否前台」)→ **OSC 777**(WezTerm/Ghostty/foot/VTE 系)→ **OSC 9**(iTerm2/VS Code/Warp/Cursor/Ghostty;Windows Terminal 的支持证据在公开矩阵里**互相矛盾**,故只作候选、必须探测后降级)→ `\a` bell(通用但粗)→ 仅状态行 + 窗口标题(OSC 2);**tmux 需 DCS 包裹**(`\ePtmux;\e\e]…\a\e\\`,tmux ≥3.3 + `allow-passthrough on`),现实中存在大量「配置了也不响」报告 → **探测不到就只留 bell**(不制造静默失效),GNU Screen 自动包裹;输出写 **/dev/tty**(stdout 被重定向/被 hook 捕获时不算终端)且失败静默降级;开关 `GAH_TUI_NOTIFY=auto|osc|bell|off`(默认 auto)+ 命令 `/notify test`(注册进 `ctx.commands` → 自动进 TUI 提示/help,不改 TUI 代码);只有 `attention` 级才触发系统级,`info` 只更新状态行。**明确不做**:引入 `terminal-notifier`/`BurntToast`/`notify-send` 等外部二进制(破零依赖)、为 CLI 单独做 `.app` 去纠正 macOS 通知署名(走 osascript 会显示为「脚本编辑器」,属已知外观妥协)、IM 投递(`NOND-W5` 暂缓)、进度通知(OSC 9;4)。**验收点**:N1 = sdk 服务单测(冷却/去重/上限/并发 `-race`)+ `/api/notices?since=` 增量语义 + 帧同源护栏 + 前端纯函数单测 + 壳侧消费测试;N2 = 每档位在真机矩阵各跑一次(mac iTerm2 / Terminal.app / tmux;Linux GNOME Terminal(VTE) / WezTerm / kitty;Windows Terminal)+ 开关 off 时零输出断言(清单见 `docs/VERIFY.md` 真机清单)。**排期**:N1 → N2 → 桌面壳收敛(P2);**范围决策(2026-11-14)**:能力层 + Web/TUI 应用内提示进 P1;TUI 系统级落点按主场景定档(以桌面壳为主 → P2;服务器常驻 + 定时/长任务为主 → P1,因为那里的用户根本不在窗口前)。
> ✅ **NOND-W1 · Windows shell 与路径语义落地(2026-11-14 已交付;Windows 真机验证待办)**:① **POSIX shell 解析下沉 sdk**(新 `sdk/shellpath.go`):`ResolvePOSIXShell()`(`GAH_SHELL_PATH` > Windows 的 `%ProgramFiles%` / `%ProgramFiles(x86)%` / `%LOCALAPPDATA%\Programs` 下 `Git\bin\bash.exe` → PATH 的 `bash.exe`/`sh.exe` > 其他平台 PATH `sh` → `/bin/sh`;环境变量缺失时**不产出相对候选**)、`ShellExecEnv()`(Windows/MSYS 注入 `MSYS_NO_PATHCONV=1` + `MSYS2_ARG_CONV_EXCL=*`)、`ErrPOSIXShellMissing`,变量 `winSemantics` 供跨平台单测覆盖。**为什么放 sdk**:tool-shell 与 host-jobs 都要用,而插件不得互相 import(红线)。② **三个执行入口统一**(原来 `sh` 硬编码):`tool-shell/shell.go`(普通路径)、`tool-shell/pty.go`(pty)、`host-jobs/jobs.go`(后台任务原为 `exec.Command("/bin/sh",… )`,改为**提交即解析**,不留注定失败的任务);**不提供 cmd.exe/PowerShell 回退** —— 裁决是 POSIX 词法,换 shell 会让判定与实际执行脱节,缺失时显式报错。③ **策略层 Windows 语义**(`policy-guard`):新增 `winRootRelativePath` —— MSYS 根相对路径(`/c/x`、`/tmp/x`)在 Windows 上被 `filepath` 当**相对路径**,`Join(root,"/tmp/x")` 算成 `<root>\tmp\x` 而 MSYS 实陒写到别处 = **静默击穿**,故写语义下一律按**不可裁决拒绝**(读语义不阻断)且错误文案补该形态;`pathWithin` 在 Windows 做**大小写折叠**(否则 `D:\Repo` 与 `d:\repo\sub` 互判根外);`isDevicePath` 在 Windows 语义下豁免 `NUL/CON/PRN/AUX`。④ **外部插件 `.exe`(真 bug,非清洁度)**:`os/exec` 在 Windows 走 PATHEXT 补全,`findExecutable` 对**无扩展名**文件**不 stat 字面路径**(Go 1.27 `os/exec/lp_windows.go`),而原落点 `plugins/<bin>/<bin>` 无扩展名 → 外部插件在 Windows 上必然 `ErrNotFound`;`scripts/gen-extplugins.sh` 生成 `tool-*.exe.gz`,`embed.ExtPluginBinary`/`pluginDst`(目录名去扩展名 = `plugins/tool-basic/tool-basic.exe`,目录口径跨平台统一)。⑤ **平台告警措辞**:`kernel_other.go` 区分「平台能力缺口」(Windows,用户无从修复)与配置问题,不复述开关。⑥ **测试/验证**:sdk 8 项(shell 解析与不可用显式失败/候选不含相对路径/MSYS 开关/可执行位)、policy-guard 4 组(根相对路径判定 + shellCmdPaths 端到端不可裁决/设备名/大小写折叠)、tool-shell 原 30 项保持;`GOOS=windows GOARCH=amd64 go build ./...` 与 `go vet ./...` 均过(**Windows 分支真实编译**);全库 `-race` 50 包绿 + sdk 模块绿;`staticcheck` 两 module 干净;`coverage-check.sh` **COVERAGE_OK**(tool-shell 92.5/90、policy-guard 90.9/89、host-jobs 96.1/88);20 件 extplugin 产物重生成(**两遍逐字节一致**,含 4 件 windows `.exe`)。⑦ **文档**:README 双语(Windows 前置段 + `shell` 工具行)。**未闭环**:Windows 真机验证(本机 macOS;按 `docs/NONDEV_ROADMAP.md` §7 清单跑),PowerShell 工具按决策后置为 `NOND-W1b`。

> ✅ **NOND-W1-verify · Windows 自动回归与平台差异修复(2026-09-14 已交付;真机 GUI 过场待办)**:① **CI job**:`ci.yml` 新增 `test-windows`(`windows-latest`,shell=`bash` = Git Bash;repo 公开故用量不计费、本地零占用) —— `gen-extplugins.sh` → `gen-web.sh` → `go vet` → `go test`(探测到 gcc 才带 `-race`) → `windows/amd64` 构建 → 裸机 `--profile headless --ephemeral` 冒烟 + 真实回合 → WebSocket 通道(`scripts/ws-smoke.go`) → `POST /api/shutdown` 优雅停机(Windows 无 SIGTERM 的统一通道)。② **首轮 19 包约 180 项失败,三类归因**:**(B) 真 bug 1 项** —— `host-cwd-sessions.SwitchDir` 与 `tui.cmdWorkspace`用**输入**路径而非**已归一化**路径派生 ProjectKey:Windows 上 `t.TempDir()` 给短名(`C:\Users\RUNNER~1\…`)而 `filepath.EvalSymlinks` 给长名(`C:\Users\runneradmin\…`),同一目录产出两个 key → `/workspace` 切换被误判越界(400);修法 = `os.Chdir` 前先 `EvalSymlinks` 归一。**(A) 测试适配(不改产品语义)** —— 新增 `internal/testutil`(`ExeName`/`IsWindows`/`PosixPerm`/`SkipNoPTY`/`ShellPath`);外部插件与测试助手二进制名补 `.exe`;权限断言平台化(Windows 无 `0600`/`0700` 对应位);含路径的 JSON 手写拼接改 `fmt.Sprintf(%q)`(原写法在 Windows 上生成 `\U` 非法转义);policy-guard 的 POSIX 用例显式 `withWinSemantics(false)`/`withCaseFold(false)` 固定语义(MSYS 根相对路径分支仍由 `shellpaths_winsemantics_test.go` 覆盖);PTY 用例在 Windows 跳过(产品侧显式 `unsupported`);`sh -c` 命令文本里的路径走 `testutil.ShellPath`(反斜杠会被 sh 当转义符:`echo x > C:\Users\a` 实写 `C:Usersa`)。**(C) 已知缺口(未修)** —— PTY 未实现(`.goreleaser.yaml` 的 `windows/arm64` 据此暂缓)、外部插件进程终止 `TerminateProcess: Access is denied`、Git Bash 下 `TMPDIR` 携带 POSIX 值(`/tmp`)与会话/工作区 Windows 路径混用。③ **验证**:macOS 全库 `go test ./...` 绿(28 文件改动,本地无退化);Windows 侧待 CI 复核。**诚实标注:本轮未在真机 Windows 上验证**;`docs/NONDEV_ROADMAP.md` §7 清单里「`shell` 可执行」「越界写被拒」两条已由该 job 自动覆盖,安装器/托盘/通知/updater/单实例仍须真机。
> ✅ **NOND-W1-verify2 · 桌面壳真机缺陷修复(2026-09-15 已交付;真机复验待办)**:Windows 真机试装 v0.1.2 报两个症状 ——「界面里找不到升级入口」「托盘图标看不到、左右键点了都不弹菜单」。取证后发现是**一条因果链**,不是两个独立问题。① **界面内从来没有升级入口**(设计缺口,非遗漏):`web-src/src/` 搜 `check_update|检查更新|升级` **零命中**;`desktop/src-tauri/src/main.rs` **零 `invoke_handler`**(web UI 根本没有渠道调壳的 updater);壳 `emit` 的 `tray-ready`/`runtime-*` 前端**零监听** —— 升级唯一入口是托盘菜单「检查更新…」,菜单弹不出来 = 完全没有升级入口。② **托盘图标不可见 = 没给图标**:`TrayIconBuilder::new()` 不会回退到 `default_window_icon`,而 tray-icon 在图标为 `None` 时**不设置 `NIF_ICON` 标志**(`tray-icon/src/platform_impl/windows/mod.rs:570-576`),`Shell_NotifyIcon` 照旧返回成功 → 托盘区留下一个**没有图标的空占位**(真机正是「看不到图标」)。修法 = 显式 `.icon(app.default_window_icon())` —— Windows 目标下 tauri-codegen 会从 `bundle.icon` 挑 `.ico` 嵌入(多尺寸、高 DPI 清晰;见 `tauri-codegen/src/context.rs:211` 的硬编码 `Some(...)`)。③ **菜单不弹 = 点击回调抢前台**:原实现对**任意** `TrayIconEvent::Click`(含右键)执行 `show()` + `set_focus()`,而 Windows 托盘菜单走 `TrackPopupMenu` —— 菜单只在自身保持前台时才展开,被别的窗口抢走前台立即关闭(表现为点了没反应);修法 = 移除该回调(窗口打开统一走菜单「显示窗口」项)+ 显式 `show_menu_on_left_click(true)`(左右键都弹菜单)。**附带纠错**:`TrayIconEvent::DoubleClick` 是 **Windows-only**,第一版本想拿双击当跨平台的「打开窗口」通道,会让 macOS 左键彻底无反应,已弃用。④ **界面内升级入口**:把 `checkForUpdates` 拆成「返回 `UpdateOutcome{status,version,message}` 的内层」+ 两个出口(托盘 → `notifyUpdate` 系统通知;`#[tauri::command] check_update` → 前端),**同一套逻辑不做两份**;`tauri.conf.json` 开 `app.withGlobalTauri`,前端以 `window.__TAURI__.core.invoke` 调用(**零新前端依赖**);设置面板新增「关于 gah」段(仅 `__TAURI__` 存在时渲染);`installed` 后**延时 1.5s 再 `restart()`**,否则前端拿不到响应窗口就被杀。⑤ **CI Rust 编译门**:新增 `desktop-shell` job(`windows-latest`)—— 此前桌面壳只在 tag 触发的 `release-desktop.yml` 里编译,**日常 push 发现不了 Rust 侧错误**(本轮排查即踩到)。两个坑写进注释:tauri-build 的 build.rs 会 `copy_binaries` 校验 `externalBin`(缺 sidecar 直接让 `cargo check` 失败,**本机实测过**)、项目级 `desktop/.cargo/config.toml` 的清华 **git** 镜像在海外 runner 上反而更慢(job 内先删掉、走官方 sparse)。⑥ **本地验证**:`cargo check` 0 error(新增警告仅为新函数沿用项目 camelCase 命名约定)、`go test ./...` 全绿、`vue-tsc --noEmit` 0 错 + `vite build` + `npm test` 38/38。**诚实标注:托盘行为未在真机 Windows 验证**(本机 macOS 验不了托盘);待 `release-desktop.yml` 打包预演产出安装包后在 VM 复验。
> ✅ **NOND-W1-verify3 · Windows CI 收敛至全绿 + macOS 平台自动化补全(2026-09-14 已交付;真机 GUI/安装器复验待办)**:① **Windows 从 104 项失败收敛到 0**(轨迹 `104 → 22 → 10 → 5 → 2 → 0`,`test-windows` 现为 **success**)。除 `verify` 批已记类别,本轮再修 11 处:**(B) 产品真 bug 4 项** —— (a) `host-docview/service.go` 的 `openPDFFile` 打开源 PDF 后**从不关闭**(`gopdf.NewReader` 持 `io.ReaderAt` 惰性读、须活到读完,于是读完后没人关)→ 每次 `Raster` 泄漏一个文件句柄,Windows 上表现为 `t.TempDir()` 清理 `unlinkat … Access is denied`(POSIX 允许删已打开文件,本地零症状);改为返回 closer、调用方 `NumPage()` 后立即释放(`extract_pdf.go` 本就是 `defer f.Close()` 的正确写法,只有这条路径错)。(b) `host-jobs/jobs_proc_windows.go` 的 `killCmdGroup` 只调 `Process.Kill()`,杀掉的是 `sh` 本体,`sh -c "sleep 30"` 的 `sleep` 成孤儿 → `Jobs.Kill` 等满 8s 报「终止超时」;改用 `taskkill /F /T /PID` 终止整棵进程树(POSIX 侧本就用进程组负 PID 组杀)。(c) 同上再补 **`cmd.WaitDelay = 3s`** —— 单靠 `taskkill` 仍不稳(实测同一用例两轮一过一败):`cmd.Wait` 会阻塞在 stdout/stderr 管道上,只要孙子进程还持有写端就永不返回;`WaitDelay`(Go 1.20+)正是为这一情形提供的确定性兜底。(d) `internal/mcpconfig` 的 `normalizeLenient`/`ParseEnv` 用 `sdk.SplitArgs` 拆 `command`,而它按 POSIX shell 语义**把 `\` 当转义符消费** —— Windows 路径 `C:\Users\…` 被拆成 `C:Users…`,MCP server 起不来(整组 `mcp_<name>_*` 工具缺席);该字段首项是**直接 exec 的 argv[0]**、不是 shell 表达式,改用不做反斜杠转义的 `splitCommand`(引号语义保留;POSIX 路径不含 `\`,两种实现在那边等价)。**(A) 测试适配 6 项** —— `converter_test` 资产句柄由 `defer` 改为读完即 `Close`(Windows **不允许 `os.Rename` 替换已打开的文件**,而该用例中途要让同源第二次转换覆盖同一缓存产物);`TestSwitchDir` 的 cwd 还原改裸 `os.Chdir`+`defer` —— `t.Chdir` 内部 `os.Open(".")` 会**保住旧 cwd 的目录句柄直到 cleanup**,而 Windows 禁止删除被打开的目录;`TestCommandsExecuteWorkspace` 补 `USERPROFILE`(Windows 的 `os.UserHomeDir` 读它、不读 `HOME`);`TestCommandsExecuteModel` 的「持久化失败」改把 `provider.yaml` 占为**目录** —— 原来把 `config` 目录换成文件时 Windows 回 `ERROR_PATH_NOT_FOUND`→`os.IsNotExist` 为真→`LoadFile` 当成「文件不存在」返回空 `File`→`UpdateModel` 命中 `idx<0` 的静默分支,提示消失;`jail_test` 的 `TMPDIR` 断言由 root 前缀改为归一化尾部(Git Bash 把 `TMPDIR` 设成 POSIX 表示 `/tmp`→`%TEMP%`,与 root 的 `C:\Users\…` 前缀不同却指向同一位置,前缀比较恒失败);`TestExternalPluginReloadLoadsNewBinary` 新增 `waitUnlocked` cleanup(Windows **进程退出与文件锁释放是异步的**,`DisposeAll` 一返回就 `RemoveAll` 会撞上还没释放的 exe 锁;go-plugin 日志里的 `TerminateProcess: Access is denied.` 是它对已自行退出的进程再补一刀的无害噪声,不代表进程没死)。**(C) CI 脚本自身缺陷 1 项** —— `test-windows` 的「WS 通道冒烟 + 优雅停机」step 一直被 `go test` 的失败 skip 掉,tests 转绿后**首次真正执行即暴露**:裸 `curl -X POST /api/shutdown` 没带 `Content-Type`,而状态变更接口受 CSRF 护栏约束(`web/guard.go`)被挡回 **415**,进程自然不停 → 改为显式 `-H 'Content-Type: application/json' -d '{}'` 并补 200 断言(本机实测复现并确认:裸 POST→415、带 header→200 且进程自行退出;ubuntu job 无需改,它靠 SIGTERM 停机,Windows 是唯一依赖 `/api/shutdown` 通道的平台 —— 这也正是该 step 存在的意义)。② **macOS 此前 CI 零覆盖**(`ci.yml` 原先只有 ubuntu 与 windows),而 macOS 有**独有的内核级沙箱实现**(`plugins/tool/tool-shell/kernel_darwin.go`,seatbelt/sandbox-exec profile)与独有路径语义(`/var`→`/private/var` 符号链接,且这也正是开发机平台)。新增两个 job:`test-macos`(`macos-14` arm64)= 外部插件/前端产物重建 → 前端单测 → `go vet` → `staticcheck`(与 ubuntu 同版本)→ `go test -race`(含 sdk 模块;runner 自带 clang)→ darwin/arm64 主包 + **Mach-O cputype 校验** → 裸机 `env -i` 冒烟 → headless 真实回合(覆盖 seatbelt 包装路径)→ WS 通道冒烟;`desktop-shell-macos` = 与 windows 侧对称的 Rust `cargo check` 编译门(此前 macOS 侧编译错误要等 tag 触发 `release-desktop.yml` 才暴露)。**刻意不跑覆盖率门与体积门** —— 两者阈值都按 linux/amd64 基线定,平台分支差异会误报(与 `test-windows` 同口径)。③ **打包预演全绿**:`release-desktop.yml`(tag 留空 = 只打包不碰 Release)run `34880426673` 两个平台 job **均 success**,产物 `gah_0.1.2_x64-setup.exe`(33,151,025 B)与 `gah_0.1.2_aarch64.dmg` 均已入 artifact(可下载复验)。④ **验证口径**:每轮以**本机 macOS 全量 `go test ./...` 绿 + `go vet`/`staticcheck` 干净**为前置,Windows 结论一律以 `test-windows` job 实测为准;`GOOS=windows GOARCH=amd64 go build/vet` 覆盖 Windows 分支真实编译。**诚实标注**:① 本轮 Windows 修复的**最终判据是 CI job**,真机 Windows 上的**托盘/GUI/安装器/updater**仍未验证(`verify2` 已登记该待办);② macOS job 是**首次跑绿**,未做之事与 ubuntu 相同(真机 GUI 观感属人工);③ `taskkill` + `WaitDelay` 是针对「Git Bash 的 `sh` 派生进程树」的收敛手段,若将来插件改用原生 Windows 进程树,兜底仍有效但失效路径可能不同。
> ✅ **NOND-W3 · 首启引导与零配置起步(2026-11-14 已交付)**:① **provider 预设表**(`web-src/src/providers.ts`,纯前端常量,零新依赖):DeepSeek / Kimi / 智谱 GLM / 通义千问(百炼兼容模式)/ 硅基流动 / OpenRouter / OpenAI / Ollama(本地),全部 OpenAI 兼容端点;**不硬编码模型名**(模型名几个月一变,写死必然过期)→ 预设只给 `name + base_url`,保存后从实时 `/api/models` 下拉里选。② **首启探测**:`App.vue` 启动时拉一次 `/api/providers`(**未装配多 provider 则静默跳过**),为空则自动打开设置面板并滚到 Provider 段(`sessionStorage` 每标签页只自动弹一次,不骚扰刷新);首屏欢迎处另有**常驻入口**。③ **空状态即入口**:无 provider 时显示引导句 + 预设 chip(点击一键填入并聚焦 `api_key`)+「不知道选哪个就用 DeepSeek;不想花钱可用 Ollama」;表单改成**标签在输入框上方**(taste §4.6,不再用 placeholder 当标签);顺手删掉 IM 线移除后遗留的 `.qr*` 死样式。④ **连通性自检**:保存后立刻用刚刷新的聚合列表判断端点是否真通,失败给人话 + 原始报错 +「重新自检」;`Err` 从后端透出 —— `sdk.ProviderModelList.Err` 是 `error` 接口**直接 JSON 序列化只能得到 `{}`**(前端永远拿不到原因),现转成**截断到 300 rune 的字符串**;配套 `host-llm` 在 `AddProvider`/`switchActive` 时**失效聚合模型缓存**(10 分钟 TTL 会让刚保存的自检读到旧结果)+ `explainProbeError` 覆盖 401/403/404/429/DNS/连接拒绝/超时/TLS/协议前缀。⑤ **人话错误(验收点 3)**:新增 `sdk.HTTPStatusHint`(两个适配器共用),非 200 时输出 `HTTP 401(Key 无效或已过期:…)` + 原始响应体;`llm-openai` 空端点(`missing protocol scheme`)、`host-llm` 空模型(`model not set`)均改中文指引。⑥ **测试**:`providers.test.ts` 6 项(预设自洽/不含 model/本地免 Key/查找/错误翻译 9 例/空值回落)+ 新增 `sdk/errors_test.go`(命中与未知码必须空串,防输出空括号)+ `web` 侧 `TestModelsAllErrString`(Err 透出/截断/Models 归一空数组)+ `host-llm` `TestListAllModelsCacheInvalidatedOnProviderChange`(新增与切换后立刻可见);全库 `-race` 50 包绿 + `vue-tsc --noEmit` 0 错 + `vite build` + `npm test` 20/20。⑦ **文档**:README 双语(设置面板行)。⑧ **顺手修的 CI 隐患**:`tui` 的 `TestCmdApprovalStatusReportsSandboxEffect` 用 `map` 迭代设定审批档,末尾又断言当前档为 `smart` —— Go 的 map 遍历顺序随机,该用例**约 2/3 概率随机失败**(实测 `-count=12` 败 2 次),改为有序切片。**未闭环**:Windows 与干净 `gah-data/` 的一次人工过场(随 `NOND-W2` 真机清单一起跑)。
> ✅ **NOND-M1(第 1 步)· 工具 schema 去重(2026-11-14 已交付)**:`host-system-prompt` 此前把每个工具的名称 + 描述 + **完整 JSON Schema** 用文本写进 system prompt(`systemprompt.go:211`),而 agent-loop 又经 `LLMRequest.Tools`(`agentloop.go:232`)结构化下发同一份 → **同一份 schema 计费两次**(MCP 工具尤其贵:名称长、参数多)。现改为**只输出一行工具名清单**,结构化定义照旧完整下发。实测(20 个典型 MCP 工具,各带一句描述 + 4 个属性 schema):提示词内 schema 副本 **15061 字节 → 0**,工具体量降 ≈93%。护栏:`TestAssemblePromptIndependentOfToolSchemaSize`(同一工具名配微小/巨大 schema,提示词必须**逐字节相同**)+ `TestAssembleOrderToolsAndHistory`(禁止 `inputSchema`、工具描述、`"properties"` 出现在提示词里)。**与原方案的偏差**:不新增「工具 schema 估算 token」调试端点(字节级护栏测试更便宜、不增接口面且进 CI)。**第 2/3 步(按 server 门控 + GUI + 热重载)已交付**(2026-11-14,见下方 ✅ 块)。
> ✅ **NOND-W4 · 定时任务 host-schedule(2026-11-14 已交付)**:① **新插件** `plugins/host/host-schedule`(类别 host;catalogue + 两份 bundle 样板 + seed-version **18→19** + `plugins/README.md` 登记),`Provides: ctx.schedule`,`Requires: ctx.agentLoop + ctx.commands`(`ctx.turnControl` 可选注入;未装配则不做忙闲判定)。② **触发走既有回合入口(红线)**:到点经 `ctx.agentLoop.Run` 提交一轮,输入带 `[计划:<名称>]` 前缀 —— 工具仍只经 `ctx.tools`(`tools/pre-execute` 单一裁决点)、仍落会话记录(不变量「模型可见即已记录」);**定时任务不是第二条执行路径**。③ **无人值守安全(关键决策)**:新增 `sdk.WithUnattended`/`sdk.UnattendedOf` 标记,host-schedule 触发时施加;`policy-guard.decide()` 在无人值守下**一律拒绝需审批的动作且绝不弹确认 —— 连 open 档也不放行**(open 的语义是「你在场时不用问」,不是「没人问就等于同意」);沙箱档位照旧由用户配置决定(不因定时而变松)。④ **cron 自研 5 字段解析**(`cron.go`,零新依赖):`*` / `N` / `a-b` / `*/n` / `a-b/n` / 逗号列表 / 三字母月份与星期 / `7`=周日;**只有裸 `*` 视为通配**(`*/2` = 受限,参与 dom+dow 的 OR 判定,即 Vixie 语义);DST 经 `time.Date` 归一化(春季跳过不存在时刻 → 顺延次日;秋季取首次出现);`Add` 时以 `nextOrError` 拒绝「4 年内永不匹配」的表达式(如 `0 0 30 2 *`)—— 不留一个永不触发的计划。⑤ **持久化**:`$GAH_HOME/schedules/<id>.yaml`(`sdk.Home()` 派生,便携纪律;目录 0700/文件 0600;原子 tmp+rename;`id` 白名单 `^[a-z0-9][a-z0-9-]{0,63}$` 兼防路径穿越);单文件损坏只跳过该条并告警(不整体失败);`NextRun` 是派生值**不落盘**(cron 改了旧值即失效),启动时重算。⑥ **并发语义**:到点有回合在跑时等待 `busy_wait_seconds`(默认 60s,2s 轮询),超时记 `skipped`(不排队、不叠加);`planEntry.rev` 版本号保证「在跑的旧回合」不会把状态回写到已被改过的计划上。⑦ **UI(设置面板「计划」段,零新依赖)**:列表(名称 + 上次终态徽标 + cron + 「下次:明天 08:00(12 小时后)」中文回显 + 失败原因)+ 新增表单(名称/cron/任务描述)+ 运行/停用/删除(删除走既有二次确认);**不自研 cron 构造器**,只做形状校验(段数 + 字符集,完整语义仍由后端判定,避免两套 parser 漂移);无人值守行为在段落文案里明示。**新增端点** `GET/POST /api/schedules`、`PATCH/DELETE /api/schedules/{id}`、`POST /api/schedules/{id}/run`(未装配 → 503);**新增 SSE 帧 `schedule`**(`schedule/run` 事件)→ 无人值守跑完自动刷到面板(没人在场时状态必须主动推,否则失败要等明天才被看见)。⑧ **e2e 入口矩阵**:新增 `tests/schedule_e2e_test.go` 4 项 —— 触发落会话记录 + 前缀 / 无人值守拒危险动作(`open` 档,配「同一命令有人值守必放行」灵敏度对照)/ 重启后计划保留且下次触发重算(含停用不排期)/ 卸载无残留(拒新操作 + goroutine 计数回落);`TestPluginUnloadMatrix` 的卸载序同步(`host-schedule` 依赖 `host-agent-loop` → 必须先卸)。⑨ **测试与验证**:插件包 26 项(cron 表驱动 + DST + store 原子写/损坏容忍/路径穿越 + 触发器/忙跳过/卸载 + 命令)、web 端点与 SSE 帧用例、前端 `schedule.test.ts` 8 项;`gofmt`/`vet`/`staticcheck` 双 module 干净;全库 `CGO_ENABLED=0 go test ./... -race -count=1` 65 包 **1449 项全绿**;前端 `vue-tsc --noEmit` 0 错 + `npm test` 28/28 + `vite build`;`scripts/coverage-check.sh` **COVERAGE_OK**(host-schedule 82.6% **已入棘轮表**,下限 76;总覆盖 76.7%→76.9%;Windows/Linux 交叉 build 亦过)。⑩ **顺手修掉的真实缺口(本轮自己踩到)**:`web-src/src/transport.ts` 的 **EventSource 降级路径按帧类型白名单分发**,而新增的 `schedule` 帧只登记在 Go 侧 → **WS 路径正常、SSE 降级时该帧被静默丢弃**(本地不退到 SSE 就永远发现不了);已同步 `types.ts` 联合类型 + `transport.ts` 白名单,并新增 `tests/frame_sync_test.go` 同源护栏(把「`web/events.go` 的 `FrameXxx` ↔ `types.ts` 联合类型 ↔ `transport.ts` 白名单」钉成**集合相等**,两个方向都报错;**已做灵敏度反证**:删掉白名单里的 `schedule` 后护栏必失败)。⑪ **文档**:README 双语(能力表 / 命令表 `/schedule` / REST 表 / 设置面板行)+ `docs/PLUGIN_DEV.md` §2.7 入口矩阵 + `docs/NONDEV_ROADMAP.md` W4 节(状态行 / 就地标注偏差 / 验收点勾选)+ `docs/VERIFY.md` W4 节(证据落点 + 剩余未闭环两项)。**与原方案的偏差(有意,已记)**:① **不做计划级审批/沙箱档位字段** —— 无人值守下审批一律拒(档位字段无意义),沙箱沿用全局档(§2.2 原文列了这两项);② **本轮不加模型侧工具**(`schedule_add`/`schedule_list`)—— 加进 `approval_tools` 会破坏「默认空 = 行为零变化」的既有不变量,登记为后续 `NOND-W4b`;③ 投递目标随 `NOND-W5` 暂缓,产出**只落会话记录**(与本轮范围一致)。**未闭环**:① 无人值守的失败/跳过目前只在会话记录与设置面板可见,**无主动通知**(待投递线 W5/W4b);② 跨夏令时边界的具体触发时刻只做了单元级验证(真机时区过场随 `NOND-W2` 真机清单一起跑);③ 「立即运行」按**无人值守**语义执行(它是「到点会发生什么」的预演,且 Web 请求立即返回、回合可能在用户离开后仍在跑)—— 这是有意选择,非缺口。
> ✅ **NOND-W2b-α · 桌面壳运行态外置(2026-11-14 已交付)** —— 关掉「升级/卸载会带走用户数据」这条真实数据丢失风险,**且不触碰便携纪律**:数据根仍是「二进制同级 `gah-data/`」唯一解析链(不读 env、不传参),换的是**二进制的落点** —— 桌面壳首启把 sidecar 复制到 `app_local_data_dir()/bin/gah`(mac `~/Library/Application Support/dev.gah.desktop/bin/`,win `%LOCALAPPDATA%\dev.gah.desktop\bin\`),再从那里 spawn;同级 `gah-data/` 自然落在应用目录之外 → **升级整包替换不再影响数据、NSIS 卸载不再删数据**。① 新 `desktop/src-tauri/src/stage.rs`(纯函数 + 9 项 cargo 测试):指纹(版本 + 源大小 + 源 mtime)决定是否重放(升级与开发期重建都会重放)、**原子替换**(临时文件 → rename)、unix 置 0755、**清「来自网络」标记**(mac `fs::copy` 走 `copyfile(COPYFILE_ALL)` 会带上 `com.apple.quarantine`,win 会带 `Zone.Identifier`;不清则同一份已被用户放行的程序换个位置又被拦一次);② **一次性迁移**:旧版数据在应用目录内且新位置无数据时 `copy_tree` 过去(**只复制不删除**),新旧位置都告知用户;③ **显式失败不静默**:取不到用户数据目录或复制失败 → 系统通知 + 退回应用目录内运行(启动失败页显示**真实**数据根);`backupBeforeUpgrade` 改为按本次运行的真实数据根备份(新增 `DataRoot` 托管态);④ 与 `NOND-W2` 的关系:升级前备份 + README 指引保留为纵深防御,README(中英)的「数据在哪/升级前必读」改写为「升级不替换、卸载不删除」;⑤ **修掉一条「死信号」**:这些提示原以 `emit("runtime-notice")` 送前端,但就绪后窗口已 `navigate` 到 sidecar 的 `http://127.0.0.1:2233`(跨源,Tauri IPC 到不了),系统通知又可能被用户拒绝授权 → 数据安全级提示会**彻底消失**;改为与失败页同一通道 `w.eval()` **把提示条贴进页面**(幂等节点 `#gah-shell-notice`、可关闭、JSON 编码文案防脚本破坏、就绪后 6×500ms 重试覆盖 Vue 挂载窗口),`noticesScript()` 为纯函数并有单测(空 → None、引号/反斜杠/换行转义、多条合并)。本地实跑已确认外置落点、`gah-data/` 不在应用目录、迁移只复制不删除、`/api/mcp` 报出的是外置数据根路径。
> ✅ **NOND-W4c · 无人值守失败主动通知(2026-11-14 已交付)**:此前计划失败只在会话记录与设置面板里(人不在电脑前就看不到)。桌面壳新增第三个轮询器(`/api/schedules`,5s,带凭据 cookie),发现**新出现的** `last_status=failed` 终态 → 系统通知「计划「X」执行失败:<错误摘要 ≤200 字>」。`newScheduleFailures` 为纯函数(启动后首轮**只记不发**,不把历史失败当新闻;同一 `(last_run_at,status)` 只报一次;`ok`/`skipped`/空状态与非法响应体一律不报),5 项 cargo 测试覆盖。仍不做的是「跨设备投递」= `NOND-W5`(暂缓)。
> ✅ **NOND-M1-3b · search 模式逐工具审批(2026-11-14 已交付)**:第 2/3 步留的已知代价 —— search 模式下工具不进注册表,按单工具名写的 `approval_tools` 只看到 `mcp_call`,等于被一个间接名**整体绕过**(安全语义缺口,不只是 UX)。修法是**能力声明**而非特判:① `sdk.ToolDefinition.ApprovalTargetParam`(代理工具的「真实目标参数名」;只在宿主↔插件协议间流转,不下发模型);② 跨进程契约三处同步(`bridge.go defDTO` / `serve.go` 序列化 / 宿主侧重建)+ 协议护栏测试断言(旧单工具协议走 `sdk.ToolDefinition` 整份反序列化,天然携带);③ `mcp_call` 声明 `ApprovalTargetParam: "name"`,policy-guard 在 `tools/pre-execute` 经 `approvalTarget()` 取真实目标,命中规则时确认/拒绝文案显示 `mcp_call → 真实工具`;④ 兜底:参数缺失/非法 → 按代理工具自身名匹配(名单含 `mcp_call` 仍拦),无人值守一律拒(代理层不是绕过口),未声明工具行为**零变化**。测试:`toolapproval_proxy_test.go` 6 项 + bridge 协议 2 项 + mcp-bridge 定义契约 1 项;前端模式说明与单测同步(不再是「不能按单个工具名设审批」)。**顺带状态回填**:「GUI 无独立连通性自检按钮」实际已由 `NOND-W3` 交付(`probeProvider` + 失败态「重新自检」按钮 + 打开设置即显示活跃 provider 的失败原因);「server 连上但零工具时与「未加载」不可区分」已由 W4/M1 交付(`serverStateLabel` 三态:已停用 / 未连接或元工具未注册 / N 个工具可用)。
> ✅ **NOND-M1(第 2/3 步)· MCP 按 server 门控 + GUI 管理 + 热重载(2026-11-14 已交付)**:① **配置载体下沉到数据根**:新 `internal/mcpconfig`(与 `internal/providerfile` 同款:0600、同目录临时文件 + rename 原子写、头部中文注释)读写 `$GAH_HOME/config/mcp.yaml`,`servers: [{name, command, args, enabled, mode}]`;优先级 **文件 > env**(`GAH_MCP_COMMAND(S)` 照旧生效、同名以文件为准),env 独有条目在 GUI 里标「环境变量」只读;**无 mcp.yaml = 行为与改动前逐字节一致**(回归红线)。② **读盘也走同一条规范化**:`normalizeLenient`(name 净化 + 整行命令按 `sdk.SplitArgs` 拆出 command/args + mode 大小写容错)被写盘与读盘共用 —— 否则「GUI 里写 `npx -y @x/y` 能跑、手抄同样内容进文件却报 no such file or directory」(本批 e2e 实证踩到,见 ⑨)。③ **门控语义全在外部插件进程内**:`extplugins/tool-mcp/main.go` 退化为薄壳(`mcpconfig.Load()` → `mcpbridge.Assemble` → `ServeTools`),`Assemble` 按 `mode` 分流 —— `direct`(默认,现状)= 全量注册;`search` = 工具**不进 `Definitions()`**,汇总进进程内索引,只暴露 `mcp_search`(查清单)/`mcp_call`(按名调用)两个代理工具(宿主零改动)。两种模式共用同一命名函数 `mcp_<server>_<工具>`(无名单 server 保持 `mcp_<工具>` 历史兼容)→ **切模式不换名**,既有会话与审批名单不失效。④ **可达集合刻意收窄**:`mcp_call` 只能调本索引里的工具(不代调 direct 工具),保住「搜到的 = 能调的」一一对应;**已知限制**:search 模式下按**单工具名**设的 `data.approval_tools` 不再逐工具触发(guard 只看到 `mcp_call`)→ 设置面板模式说明里明示「不能按单个工具名设审批规则」,需门控单工具时改用 direct 或改门 `mcp_call`。⑤ **GUI**:设置面板新增「MCP server」分区(列表内联编辑名称/启动命令/启停/模式、删除、环境变量只读行、「保存并重载」+「放弃修改」+ 配置文件路径回显、按名称+模式匹配的逐行状态徽标);REST `GET /api/mcp`(视图:配置 + 逐项 `loaded`/`tools`,direct 数已注册 `mcp_<server>_*`,`search` 数取自 `mcp_search` 空查询索引)+ `POST /api/mcp`(整表提交 → 写 mcp.yaml → 重启插件;写盘成功但重载失败 = **200 + `reload_err`**,如实报告部分成功;坏配置读盘 = 500,不伪装成空列表)。前端纯函数 `web-src/src/mcp.ts`(+10 项单测),零新依赖。⑥ **热重载**:新 `sdk.ExternalPlugins{Reload(name)}`(零新依赖),host-bridge 装配后 `Provide("ctx.extplugins")`(catalogue `Provides` 同步);`Bridge.Reload` 按名定位二进制(**名字 = 文件基名去扩展名**,同时支持发布布局 `plugins/<名>/<名>` 与扁平布局 `plugins/<名>`,不硬编码 `.exe`),`reload` 后**校验条目仍在**(插件启动失败/配置为空时返回显式错误,不假装成功);新增 `reloadMu` 串行化三处重载入口(文件监听 / 工作区切换 / ctx.extplugins),防同路径双载造成进程泄漏与工具双注册残留。⑦ **装配期显式失败,不静默空转**:单 server 连接失败/停用记 note 跳过;全部不可用 → 插件 `exit 1`(宿主跳过该插件并 ERROR,其余工具不受影响);e2e 断言「重载成全坏配置」时 Reload 报错、旧工具已撤销(不留半死状态)、宿主工具照旧可用。⑧ **棘轮与体积**:mcp-bridge 50→**72**(实测 76.1),新增 `internal/mcpconfig 80`(实测 85.0);总覆盖 76.9%→77.4%;`go build`(含 windows/amd64、linux/amd64 交叉)、`go vet`、`staticcheck`、全库 `-race`、sdk module、`vue-tsc` + `npm test`(38 项)、`vite build` 全绿。⑨ **外部插件产物重生成**:改 sdk + 新增 mcpconfig 影响全部工具类 extplugin → `scripts/gen-extplugins.sh` 重跑,20 件 `internal/embed/extplugins/**/*.gz` 全量更新(e2e 走 embed 产物,**不重生成会导致 e2e 仍跑到旧 tool-mcp**,本批已踩到)。⑩ **护栏**:`internal/mcpconfig` 单测(规范化/拆命令/优先级/坏 yaml 显式报错/不写 env 条目/空列表回读)、`plugins/mcp/mcp-bridge/assembly_test.go`(替身 `Conn`;direct 命名与转发不变、search 只暴露代理工具、停用与失败跳过、全失败显式错且关连接、同名保留先注册、检索打分与截断、`mcp_call` 参数宽容与错误引导、代理工具 schema 完整)、`web/mcp_test.go`(状态推导三类来源 / 保存与重载 / `reload` 显式 false / 400 校验 / 重载失败报告 / 未装配提示 / 坏配置 500)、`tests/external_test.go` 新增 `TestExternalMCPConfigFile` 端到端(direct 全量、search 隐藏工具、索引只含 search 模式、`mcp_call` 路由到对应实例、全坏重载报错)+ 两个历史 MCP 用例补 `GAH_HOME` 隔离(否则会读开发者真实配置)。⑪ **与原方案的偏差**:工具数可见性用 `mcp_search` 空查询推导,**不给桥协议加 `Meta()` RPC**(零协议改动,代价:server 连上但零工具时与「未加载」不可区分,登记为已知边界)。⑫ **未闭环(诚实登记)**:MCP 传输面仍 **stdio-only**(Streamable HTTP 未做,登记为 `NOND-M1` 第 4 步可选);GUI 无独立「连通性自检」按钮(以保存后逐行 `loaded` 状态代替);Windows/干净 `gah-data/` 真机过场随 W1/W3/W4 一并待跑。
> ✅ **NOND-W2 · 首个 Release(2026-11-14 工程侧交付 → 2026-09-12 **已发布 v0.1.0**;真机安装/首启验收待硬件)**:repo 已公开(Releases/`releases/latest` 端点可用),发版链补全与「会被非编程用户踩到」的三个缺口一并修掉。① **命令行发行流水线此前缺失**(真实缺口):`goreleaser` 只在 `ci.yml` 里跑 `--snapshot` 冒烟,**没有任何 workflow 在 tag 上真正 `goreleaser release`** → 推 tag 只会得到桌面安装包,命令行归档与 `checksums.txt` 不会进 Release,而 `docs/RELEASE.md` 写的却是「tag 驱动 goreleaser + 桌面壳同版本」。新增 `.github/workflows/release-cli.yml`(tag `v*` → checkout full history → 预生成 extplugin/前端 → `goreleaser check` 配置漂移即红 → `goreleaser release --clean` → 五目标归档抽查),并在 `.goreleaser.yaml` 增 `release: {draft: false, mode: keep-existing}` —— 两个 workflow **共用同一个 tag 的 Release**,后到者只追加产物、不覆盖正文、不因「已存在」失败;`draft: false` 保证 `releases/latest` 立刻可用(updater 端点取它)。② **「检查更新」的失败文案**(W2 验收点 2):`desktop/src-tauri/src/main.rs` 原样把错误抛给用户(端点 404 → 「检查更新失败:…」),首版发布前必然出现且会让用户以为程序坏了;现按错误内容分流 —— `404`/`not found` → 「暂无可用更新(线上还没有发布版本)」,其余 → 「检查更新未完成:`<原因>`」(已是最新 / 有新版本两条原路径不变)。③ **升级数据保护(§8 风险 5 的落地,本轮最关键)**:数据根是**应用目录内**的 `gah-data/`(桌面壳里 sidecar 位于 `.app/Contents/MacOS` 或 Windows 安装目录),而桌面版升级是**整包替换** → 不处理就是「点一下检查更新,会话/记忆/密钥全没」。现于 `download_and_install` **之前**把 `gah-data` 递归复制到 `~/gah-upgrade-backup/<unix 时间戳>/`(标准库 `copyTree`,桌面壳零新依赖;路径在**应用目录之外**,整包替换后仍存活),备份成功通知落地路径,**备份失败即 `return Err` 取消升级**并提示先手动 `/backup`(宁可让用户手动备份,也不拿数据冒险);无数据(首装即升级)跳过备份。④ **非编程用户入口文案**(W2 设计要点 4):README(中/英)「快速开始」新增「下载安装」节 —— 下载表(mac arm64 dmg / win x64 setup.exe / 命令行 tar.gz)、**首次打开会被系统拦一下**的两步放行(mac 右键→打开 / `xattr -dr com.apple.quarantine`;win SmartScreen「更多信息→仍要运行」)+ 「**数据在哪 / 升级前必读**」`/backup ~/gah-备份`(必须落在应用目录之外)与卸载语义。⑤ **发版清单**:`docs/RELEASE.md` 记「人工只做 3 件事」(push master → tag → 盯两个 workflow),并写明**版本号唯一事实源 = tag**(`goreleaser` 镜像 / sidecar `-X main.version` / tauri bundle version 三处均由 tag 注入,`publish-desktop.sh` 用 `git describe` + `--config '{"version":…}'`,**不必手改 `tauri.conf.json`**)、`latest.json` 只在桌面壳 merge job 生成(若该 job 失败,用户端会看到「暂无可用更新」属预期,修好重跑即可)。⑥ **桌面打包链在首次真跑时暴露的 6 个真实缺陷(本轮本地实跑发现并修)** —— `publish-desktop.sh` 与 `release-desktop.yml` 此前**从未被真正执行过**,本地一跑全暴露:(a) `tauri-cli` v2 的 `build` **没有 `--release`**(v1 语义;v2 默认即 release,只有 `-d/--debug`)→ 必报 `unexpected argument '--release'`;(b) npx 分支写成 `npx -y @tauri-apps/cli@2 tauri build …`,把 bin 名重复当子命令传 → `unrecognized subcommand 'tauri'`,而 CI runner 无 cargo-tauri **必走此分支** = 桌面发行从未能跑通;(c) `bundle.createUpdaterArtifacts` 未设 → tauri 不产 `gah.app.tar.gz`(updater 唯一可安装产物)→ 自动更新必然失效;补设后又暴露「签名私钥必须在 **tauri build 之前**注入」(否则 `A public key has been found, but no private key`),故密钥解析整体前移;(d) 产物收集三处错:`.app.tar.gz` 在 `bundle/macos/`、`dmg` 在 `bundle/dmg/`,原脚本拿 `bdir`(=macos)当公共前缀 → **dmg 从未被收集**且 `cp … || true` 把错误吞掉;`bundle_dmg.sh` 的中间产物 `rw.*.dmg`(≈90 MB)会被裸 `*.dmg` 一起收走;Windows 侧 updater 只认 `*-setup.exe.zip`(`tauri-plugin-updater` 对 NSIS 的期望形态)而原脚本只收 `-setup.exe`;(e) `latest.<平台>.json` **从未落盘**(只 `cat` 不写)→ 脚本最后一行必失败;且原实现把平台目录里**每个文件**都写进同一个平台键(dmg 覆盖 `app.tar.gz`)→ updater 会下到装不上的 dmg;现只写 updater 产物、补 `version`/`pub_date`、签名优先复用 tauri 自产 `.sig`,并加「缺 updater 产物/缺安装包/签名失败/多平台 version 不一致」四条显式失败;(f) `release-desktop.yml` 两个 job **没跑 `gen-web.sh`**(main 包 `go:embed web/dist`,产物不入库)→ sidecar 构建必失败;顺带补 `*-setup.exe.zip` 上传(否则 updater 的 URL 404)、两 job 的 `setup-node` 缓存与 `merge-upload` 的 `startsWith(github.ref,'refs/tags/')` 门(使 `workflow_dispatch` 能做「只打包不发布」的预演)。⑦ **本地实跑验收(macOS arm64;`CI=true` 跳过 `bundle_dmg.sh` 里那步 Finder AppleScript —— 无 GUI 自动化授权的会话下会 `AppleEvent 超时(-1712)`,tauri-bundler 在 CI 环境自动加 `--skip-jenkins` 绕过)**:`publish-desktop.sh darwin-aarch64` 全绿 → `gah_0.1.0_aarch64.dmg`(35 MB)+ `gah.app.tar.gz`(updater)+ `.sig` + `latest.darwin-aarch64.json`(签名/URL 齐备);`merge` 合成与「版本不一致即失败」负例均验证;**挂载 dmg 实跑**:`.app/Contents/MacOS/gah`(= sidecar,含 embed 的外部插件与 web 资产)在 `env -i` 空环境 boot 成功、从临时数据根释放并加载 5 个外部插件、无密钥时输出人话错误 `HTTP 401(Key 无效或已过期:…)`(W3 文案的端到端印证);`Info.plist` 的版本 0.1.0 / identifier / 最低系统 10.15 正确。⑧ **验证**:`cargo check` / `goreleaser check` / YAML lint / `bash -n` 全过;Go 测试面不受影响。⑨ **首个 Release 已发布(v0.1.0,2026-09-12)** —— 「工程侧」至此闭环为「真的发出去了」,且**发行流水线自身的 4 个缺陷是被真 tag 打出来的**(此前全部无从暴露):(a) `release-cli` 的 check 步骤写成裸 `run: goreleaser check`,runner 上 CLI 尚未安装(action 才负责装)→ `command not found` exit 127;(b) 真发行要求**干净工作树**,而 before hooks 里的 `gen-extplugins.sh` 会重建**已入库**的 embed 产物(跨工具链 build id 必然字节不同)、`gen-web.sh` 的 `npm install` 会重排 `package-lock.json` → `git is in a dirty state`(ci.yml 走 `--snapshot` 允许脏树,所以这条**只在真发行路径**炸)→ 现:发行只用入库 embed 产物 + `npm ci` + `goreleaser --skip=before`;(c) Windows 侧 sidecar 必须带 `.exe`(`binaries\gah-<triple>.exe` else 报 `resource path … doesn't exist`)、且 `tauri-build` 需要真 `icons/icon.ico`(仓库只有 png)→ 用 `tauri icon` 生成标准图标集 + 脚本加前置检查;(d) `release-desktop` 的 merge job **没有 `permissions: contents: write`** → 上传 Release 403 `Resource not accessible by integration`(平台 job 全绿、installers 都产出来了,只差上传)。配套机制:两个 workflow 都补了**补发路径**(`workflow_dispatch` + `tag` / `run_id` 输入 → 检出目标 tag、可用 `actions/download-artifact` 的 `run-id` 复用既有打包产物,不移动已推送的 tag),发布正文归属确定化(`.goreleaser.yaml` `changelog.disable` + 桌面壳用 **tag 注解**作正文:两个 workflow 并发时正文只能有一个作者)。**产物实证**:Release `v0.1.0`(`draft=false`)含 10 个命令行归档(darwin/linux/windows × amd64/arm64 × tar.gz/zip)+ `checksums.txt` + `gah_0.1.0_aarch64.dmg`(35.8 MB)+ `gah_0.1.0_x64-setup.exe`(33.1 MB)+ `gah.app.tar.gz`(updater)+ `latest.json`(双平台签名齐备);`https://…/releases/latest/download/latest.json` 实测 **HTTP 200**、`version=0.1.0`、`darwin-aarch64` 与 `windows-x86_64` 两条目带签名字符串 —— updater 端点链路与「版本号唯一事实源 = tag」均已成立。**未闭环**:① **真机验收 1/3**(mac arm64 + win x64 各一台:下载→安装→首启→对话 §7 全过;卸载后 `gah-data/` 残留符合文档)—— Windows 需硬件;mac 侧已推进到「dmg 内含可运行 `.app` + 首启释放插件 + 真实回合」,仅剩 Gatekeeper 首启放行需人在 GUI 点;② **Windows 安装/运行**仍未在 Windows 真机验证(打包链已在 CI 全绿并产出真实产物,但「装得上、跑得起、SmartScreen 放行」属真机范畴);③ **数据根移出应用目录 / 卸载保留数据** = `NOND-W2b`,涉便携纪律(重大决策),待真机后评估;④ `NOND-W2b` 之外的「首个真机自动升级」验证(需先有 v0.1.1)。

> ✅ **R6 观察项分析与完善(2026-09-16,承接 R5 三端复查的 3 个观察项)** 已交付:①**SSE 重放/订阅 gap 产品级 bug**(R5 观察项 3 flake 根因实为 `web/server.go` `consumeStream` 先重放后订阅——重放快照与 `hub.Stream()` 之间广播的帧永久丢失;非测试时序问题)→ 改为**先订阅后重放 + 按 Seq 去重**(seen 游标,非会话帧 ID=0 不参与);新增 `web/consume_test.go` `TestConsumeStreamGapNoLossNoDup`(阻塞式 Replay stub 精确构造窗口;回退旧实现即失败=灵敏度验证);`tests/web_e2e_test.go` 调序为先建 SSE 再提交回合(对齐真实前端时序,实时路径覆盖)。②**WS 掉线永久降级 SSE**→ `web-src/src/transport.ts` WsTransport 增指数退避重连(1s/2s/4s,带 after 游标差集续传,onopen 清零退避),持续失败(约 7s)才降级 EventSource(浏览器自愈);vue-tsc 通过。③**桌面壳数据根**(R5 观察项 2)→ `desktop/src-tauri/src/main.rs` spawn 时**恒传 GAH_HOME**:显式 env > 系统应用数据目录 `appDataHome()`(macOS ~/Library/Application Support/gah,Linux XDG_DATA_HOME|~/.local/share/gah,Windows %APPDATA%\gah)——壳内便携根(.app/Contents/MacOS/gah-data)随升级丢失且可能只读不作桌面默认;启动 12s 未就绪时窗口注入失败提示(含数据根路径,不再永远停在“正在启动”);`cargo check` 通过(仅存量命名 warning)。验收:全库 44 包 `go test -race` 全绿;consumeStream gap 单测灵敏度验证(旧实现 FAIL/新实现 PASS);O3 e2e 连续通过。
> ✅ **R5 三端复查(2026-09-16,承接 57739b1「弃用 ~/.gah」纪律清理)** 已交付:全库基线 44 包 -race 绿(含并行会话 policy-guard 融合);审查确认 `~/.gah` 旧解析链残留 4 处代码 + 2 处规范/注释漂移,统一收敛为「GAH_HOME env > 二进制同级 gah-data/(首启自动新建);不可便携= boot 报错退出,~/.gah/TempDir 兑底弃用;运行态 GAH_HOME 恒设,空(仅嵌入/单测)宁回 TempDir 不落 cwd/根」:① `plugins/ui/ui-web-app/web.go` uiPluginsHome 此前空 GAH_HOME 回退 **$HOME 根**(→ ~/ui-plugins、~/attachments 外泄,最重)改 TempDir;② `tui/theme.go` themeHome 回退 ~/.gah 改 TempDir(注释去失效 exakey.go 引用);③ `tui/app.go` pluginHome 同款;④ `desktop/src-tauri/src/main.rs` 顶部注释同步新链;⑤ AGENTS.md「便携纪律」解析链描述同步。审查确认无确定功能 bug(前端 timer/listener 清理完整、web Go 无 goroutine 泄漏信号、transport WS→SSE 降级设计正确);观察项记录:transport.ts WS 掉线后不重试 WS(永久降级 SSE,设计取舍);desktop .app 未设 GAH_HOME 时 sidecar 便携根落 Contents/MacOS/gah-data 的权限与 GUI 提示待完善(需设计决策)。另:TestWebEndToEndTurn 全量并行下偶发 flake(SSE 断言窗口时序,单跑稳定过,与本次改动无关)。
> ✅ **Q1 三端 bug 修复** 已交付:① `web-src/src/components/JobsPanel.vue` 轮询改 `watch(open)` 启停(immediate 首开即拉,关窗 clearInterval,组件常驻/v-if 控显语义不变,App 5s 徽标轮询未动);② `plugins/host/host-backup/backup.go` `backupCmd` 对 `/backup ~/<名>` 展开 `~`(复用 host-internal-commands 同款逻辑,os.UserHomeDir + TrimPrefix;dest 与展示分支同用展开值)。
> ✅ **Q2 web 端 token 化** 已交付:style.css 补 `--ok-soft/--err-soft/--err-line/--tool-soft/--tool-line/--tool-strong/--overlay/--fg-on-accent` 八枚 token(soft 系浅背景/浅边框/深字、遮罩统一 0.28、accent 钮反白);App/ConfirmDialog/ConfirmBar/InputBar/JobsPanel/SettingsPanel/Sidebar/StreamView 八组件 29 处裸色值(含 rgba 遮罩 4 处)全部 token 化,`grep '#..|rgba'` 组件目录零残留(存量 style.css 内仅剩 token 定义与全局 tooltip 基础样式);vue-tsc + vite build 通过。
> ✅ **Q3 注释与加固** 已交付:① `desktop/src-tauri/src/main.rs` 两处注释同步为便携根解析链(GAH_HOME env > 二进制同级 gah-data/(首启自动新建)> ~/.gah > TempDir),代码行为不变;② `web/server.go` SSE 增 `retry: 3000` 断线重连指令(与 Last-Event-ID 续传配合)+ `X-Content-Type-Options: nosniff`。
> ✅ **Q4 提交纪律** 已交付:Q1–Q3 + M17/M18 + seed-version 11 一并落库(`b98eb17`,216 文件;**纠偏:.gitignore 原只忽略 `/web-src/node_modules/`,示例 UI 插件 `web-src/examples/*/node_modules`(2145 文件)裸入库风险→补 `web-src/examples/**/node_modules/` 规则**,清除 ci.yml.snip 0 字节残留);Q1 补单测 TestBackupCmdTildeExpand(`8be6cf8`);全库 45 包 `go test -race -count=1` 全绿 + go vet 通过,工作区干净。
>
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
>
> 目标:原 TUI 内部命令(thinking/model/…)注册在 ui-tui-app 装配时 → web profile 无 TUI → web `/` 下命令不可用。下沉后任意 profile 通用。
>
| 模块 | 交付 | 验证 |
|---|---|---|
| **宿主插件** | `plugins/host/host-internal-commands`(catalogue Bundle=base;seed 8→9):Start 经 `ctx.commands.Register` 注册 11 命令(thinking/model/provider/sandbox/plugins/settings/export/compact/workspace/session/reload),注册前判重(已存在跳过,先到先得);handler 纯服务注入(去 TUI 状态耦合),偏好/provider/插件持久化逻辑原样搬移 | 单测(注册集 11+UI 专属 8 不在宿主;thinking 执行经 stub llm;二次 Start 判重不覆盖) |
| **会话切换事件** | host-cwd-sessions 增 `emitSession`(Open/New 后广播 `cwd/session-switched`;与 workspace-switched 同款) | TestSessionSwitchEmitted(Open/New 均广播) |
| **TUI 联动** | registerInternalCommands **判重跳过**(宿主先注册则不再重复,共用注册表命令);App 订阅 `cwd/session-switched`+`cwd/workspace-switched` → `onSessionSwitched`(经 Inject 取服务复用 afterSessionSwitch 刷新:状态栏/统计重置/清流重放/工作区名);留 TUI 专属 8 命令(search/widgets/theme/help/exit/fork/tree/name) | TUI 包 -race;TestTUIProbe/NonTTY 真机通过 |
| **web 通用** | web profile 装配 base → 命令注册 → `/api/commands` 含全部 11、UI 专属零误下沉;`POST /api/input "/thinking off"` 200 且 `/api/state` thinking=off | 真机 web 全链路 |
| **回归** | 全库 43 包 -race 绿;pty 探针三处 `~/.gah` 拷贝改 Skip(便携迁移后真实 home 无 config 的环境性失败) | — |

## B5 UI 槽位 v2:设置/侧栏/附加面板扩展点插件化 ✅ (2026-09)
>
> 目标:v1 仅四槽位(stream/input/statusbar/confirm)覆盖制;v2 开放三个**多实例追加型**扩展点,第三方插件直接注入宿主界面区段/入口/面板,零改宿主代码。向后兼容 v1(四槽位语义不变)。
>
| 模块 | 交付 | 验证 |
|---|---|---|
| **registry v2** | `registry.ts` 新增三扩展注册表:settings-section / sidebar-action / extra-panel(每插件以其 id 为 key 追加;priority 排序降序,同优先级注册晚者先;`extSnapshot()` 调试断言);v1 四槽位逻辑与 `slotSnapshot()` 不动 | vue-tsc 0 错 |
| **加载器分派** | `plugins.ts` 槽位名分派:v1 四名走原 registerSlot;v2 三名走 registerSettingSection/registerSidebarAction/registerExtraPanel;未知槽位仍忽略 | 同上 |
| **落点** | SettingsPanel 尾部注入 section 组件(每插件一节 .sec);Sidebar 增「插件动作」区(actions 组件)与「附加面板」区(入口列表);App 增附加面板抽屉(右侧 340px,token 化,标题来自 manifest.title) | vue-tsc + 构建 |
| **安装侧** | `internal/install` 槽位白名单扩 v2 三名(拒装校验注释同步);web server 聚合透传(slot 名注释同步) | go 编译 + -install-ui 真机 |
| **示例插件** | `web-src/examples/extension-demo`(vite lib 三入口):settings-section(设置面板区段)+ sidebar-action(侧栏动作按钮)+ extra-panel(附加面板);复用 statusbar-demo 依赖 | `-install-ui` 安装成功(覆盖 3 槽位)+ `/api/ui-plugins` 聚合显示 3 slot |
| **回归** | 全库 43 包 -race 绿;web/internal/install/ui-web-app 重点回归 | — |

## C1 desktop 壳工程落地 ✅ (2026-09,P1)
>
> 目标:可行性 spike(§ DESKTOP_FEASIBILITY)形式化到仓库 `desktop/`——Tauri v2 壳 + sidecar gah 的完整 P1(签名/CI/更新属 P2/P3)。
>
| 模块 | 交付 | 验证 |
|---|---|---|
| **工程** | `desktop/src-tauri/`(Cargo.toml:tauri 2 + shell/single-instance/autostart/notification + tray-icon feature;tauri.conf externalBin sidecar;frontendDist=ui 占位 loading;bundle targets app+dmg)+ `.cargo/config.toml`(tuna 镜像,中国区可构建) | cargo build 通过 |
| **壳逻辑**(main.rs) | spawn sidecar `gah --profile web`(GAH_WEB_OPEN=0;GAH_HOME 不设=与 CLI 共享 ~/.gah,显式传入优先)→ 轮询 `/api/state` 就绪 → navigate(端口已占用 = 接管现有实例,启动探测兜底);单实例插件(多开 focus);托盘(显示窗口/开机自启开关/退出);回合完成通知(轮询 running 翻转);关窗驻托盘(CloseRequested prevent_close+hide);**退出链**:托盘退出 → POST /api/shutdown → 等端口释放(5s)→ 超时置强杀标记 → RunEvent::Exit kill 兜底 | 真机:sidecar spawn ✓ 2233 LISTEN ✓ WebKit 渲染(14 连接)✓ shutdown 200 → sidecar 退出/插件零残留/端口释放/壳保持 ✓ *(2026-09-16 R6 变更:spawn 改恒传 GAH_HOME(显式 env > 应用数据目录),不再与 CLI 共享 ~/.gah;见 R6 登记与 docs/DESKTOP_FEASIBILITY.md §10)* |
| **构建** | `scripts/gen-desktop.sh`(本平台 triple sidecar go build → cargo build dev/release) | 脚本跑通 |
| **登记** | .gitignore(desktop target/binaries;Cargo.lock/ui 保留入库) | — |

## R11 真机清单实跑修复(VERIFY 交互类)✅ (2026-09-13)

> 背景:跑 `docs/VERIFY.md` 真机清单(自动可跑子集全实跑,kitty 按键/浏览器/桌面壳类仍由人工验)。
> 实跑暴露 4 个问题,本轮全部修复;回归 = 新增单测 + 全库 `-race` 绿。
>
| 模块 | 交付 | 验证 |
|---|---|---|
| **① 内核沙箱首条命令误拦(真 bug)** | 根因:`shell.go`/`pty.go` 曾**先**算 `kernelWrapCtx(ctx)`、**后**调 `jailEnv()` —— 全新数据根的首条命令执行时 `jailRoot()` 尚不存在,`resolvePath` 的 `EvalSymlinks` 失败并回退**未解析**路径;`GAH_HOME` 含软链组件(`/tmp`、`/var`、macOS TempDir)时 seatbelt 按真实路径匹配 → jail 白名单整条失效(首条 `go build`/`npm install` 报 operation not permitted,第二条起正常)。修法:两者**换序**(先建 jail 再算 profile,顺序注释锁定)+ `resolvePath` 加固:路径尚不存在时逐级向上找最近的存在祖先再拼回尾部(不再把软链前缀原样丢给 seatbelt;确实解析不出来才回退原路径,保守方向=少放行) | 新增 `TestShellToolFirstCommandWritesJailOnFreshSymlinkedHome`(软链 GAH_HOME + 全新数据根,走真 `ShellTool.Execute`;修复前必红,已实录)+ `TestResolvePathSymlinkedAncestorForMissingPath` |
| **② 端口被占用不退出** | 根因:`ui-web-app` 在**返回成功之后**才在后台 goroutine 里 `srv.Start()`,监听失败只落一条日志 → 进程挂着不动、无可服务端口、`/api/shutdown` 也到不了它(只能 kill,外部插件子进程一并残留)。修法:`web.Server` 拆出 **`Listen()`**(幂等、失败显式返回)+ `Start()` 复用句柄;插件在**一切装配之前**(先于订阅/渠道注册)同步 `Listen()`,失败即启动失败(→ boot 失败 → exit 1);`Shutdown()` 兼顾「Listen 过但未 Serve」:就地关闭句柄不留孤儿端口 | `TestPluginStartFailsFastOnPortTaken`(占口 → Start 同步报错且无半截装配)+ `TestListenThenShutdownReleasesPort` / `TestListenFailsOnTakenPort`(web 包);§14.1 之外的真机验收:重建二进制后端口占用 → 启动即失败、无残留进程 |
| **③ M16 计时口径过期** | 真机实测首跑 `go test ./... -count=1 -race` **91.98s**(干净缓存)/ 82.17s(预热);本轮后续两次全库跑 90.57s / 86.97s。原文档 ≈74s(基线 ~100s)已不符 | `docs/VERIFY.md` M16 条目重定基为 **85–95s**(实测四次均在带内),并注明干净/预热两口径;重复运行(带缓存)3.57s ≤ 5s |
| **④ RST-1 无 poppler 错误不可读** | 外部光栅不可用而走内置兜底、兜底又失败时,原样抛出的只有裸 `pdfium: 下载读取失败: context deadline exceeded`,会被误读成纯网络问题。修法:`Service.Raster` 补上真实原因前缀 —— **两条分支分开说**(`rasterOn` 开着 = 「本机未检测到 poppler pdftoppm」;开关未开 = 「外部光栅未启用(data.external_raster=false)」,不能谎报本机没装)+ 内置 pdfium.wasm 兜底的原始错误 + 替代做法(装 pdftoppm 或 `data.pdfium_wasm_path` 离线部署) | 新增 `TestRasterNoPopplerErrorNamesBackend`(注入无 pdftoppm 的 converter + 不存在的 wasm 路径;两分支各验:含 poppler/pdfium 字样 + `ErrDocParse`、开关关时须提 `data.external_raster` 且不得声称缺 poppler) |
| **⑤ 全库 -race 偶发红(顺带修复)** | 全库首轮 `-race` 在 `host-schedule.TestScheduleCommand` 偶发红:`TempDir RemoveAll cleanup: schedules: directory not empty`。根因(与本轮 4 项无关、预存):`/schedule run` 触发的是**后台** `execute`,用例在 `run` 后立即 `rm`/结束,而 `execute` 写完运行记录才 `savePlan` —— 高负载(全库并行跑)下写盘落在 teardown 之后,与 TempDir 清理竞态。修法(仅测试):`run` 后等**落盘文件**出现 `last_run_at` 再继续(rename 原子,读到即完整;不用内存 `LastRunAt` 观察,它写在 savePlan 之前) | `-count=8 -race` 单包 216 项全绿;随后全库 `-count=1 -race` 连跑两次:首轮 90.57s 但因此 flake 红(先于修复)/ 次轮全绿;修复后再跑单包 6 轮 ×TestScheduleCommand 全绿 |
| **⑥ 清单回填 + 剩余任务归档** | `docs/VERIFY.md` 按实跑证据回填:`[x]` 从 78 → **95 条**(协议层/CLI/脚本/内核沙箱级),每项方括号注明证据来源并显式标出「视觉待验」的部分;文件头加回填图例;R6「桌面壳数据根(`~/Library/Application Support/gah`)」标注**已作废(被 R8 取代)**;文末新增「剩余验收任务」段:146 条未勾按「需要什么才能跑」分 6 类(TUI 键盘 42 / Web 交互 27 / 文档预览 11 / 桌面壳与 Windows 27+18(A 表)/ 其它真机与决策 18 / 未实施 21)+ 建议过场顺序 | 逐条复核:勾选项均有本轮实跑命令或单测出处;`docs/` 不入库(.gitignore),仅本地清单 |

## R12 Windows 桌面端反馈六修 ✅ (2026-09-14)

> 背景:Windows 真机使用反馈 6 条(设置误关 / 首启提示不消 / 附件拖入无效与提交 400 / 超限提示常驻 / tooltip 遮挡越界 / 无法切换工作区)。
> 约束:网页端改动依 `design-taste-frontend`(不引入裸色值,复用 token);桌面壳不新增 Rust 依赖(离线环境无 dialog 插件)。
> 回归:web-src `vue-tsc`+`npm test`(38)+`vite build`、Go 全库 `-count=1 -race`(1489 / 66 包)、`coverage-check` COVERAGE_OK、`size-check --all` 五目标 OK(win/amd64 46.29 MiB)、真机 HTTP 探针 9/9。
>
| 模块 | 交付 | 验证 |
|---|---|---|
| **① 设置误关** | 唯一入口语义:删掉 `SettingsPanel.vue` 遮罩上的 `@click.self="emit('close')"`(点空白不再关),出口只剩 ✕ 与 **Esc**;Esc 用**捕获阶段 + stopPropagation**,避免同按触发输入框「Esc 清空」把草稿一起清掉 | 类型检查 + 静态复核(`grep @click.self` 无残留);Win 端人工 |
| **② 首启提示不同步** | `providerCount` 原先只在 `maybeOnboard`(每会话一次)取过,设置里加完 provider 主界面仍挂「还没有配置模型」。修法:`App.vue` 新增 `refreshProviders()`(读 `/api/providers` 计数,501 静默),设置面板 `@changed` 改走 `onSettingsChanged` = `refreshStats` + `refreshProviders` | 真机 `GET /api/providers` 200;Win 端人工确认提示消失 |
| **③ 附件(拖入 + 400)** | 两处独立根因:(a) **拖入无效** = Tauri v2 默认 `drag_drop_enabled:true` 在 WebView 层注册 OS 级拖放,HTML5 drop 事件到不了前端 → `tauri.conf.json` windows[0] 加 `"dragDropEnabled": false`(键名按 tauri-utils 2.9.3 `WindowConfig.drag_drop_enabled` 核对);(b) **提交 400「路径非法」** = 前端原样回传绝对路径,`validAttachment` 用 `filepath.Rel` 做归属判定,**Windows 大小写不敏感路径会因大小写差异被判越界**(另叠加分隔符/数据根漂移风险)。修法:前端提交改用**相对标识** `/attachments/<rel>`(`AttachmentView.url`;绝对路径仍兼容),服务端新增 `resolveAttachment()` —— 两种形式都解析成附件根内绝对路径,归属判定加 `attachmentWithinRootFold()`(Windows 折叠大小写),失败按原因分档回 400 且**服务端日志留详情**(越界/不存在/是目录/空路径) | 上传 + 相对标识提交 **202**、绝对路径兼容 **202**、越界 `/attachments/../etc/passwd` **400**(信息 `附件不可用: …(不在附件目录内)`)—— 均为重建二进制 + 真实数据根的真机 HTTP 探针;新增 `resolveAttachment` 边界断言 + `attachmentWithinRootFold` 大小写两分支(ubuntu CI 也可回归) |
| **④ 超限提示常驻** | `InputBar.attErr` 只是纯文本行,无关闭也无超时,换完文件旧提示还挂着。修法:`setAttErr()`(写入即起 **10s** 自动消失定时器)+ `clearAttErr()`(× 按钮/移除附件/提交成功/卸载时清),提示行右侧加关闭按钮(样式沿用 token) | 类型检查 + 前端构建;Win 端人工 |
| **⑤ tooltip 遮挡/越界** | 根因:`[data-tip]:hover::after` 伪元素挂在触发器**内部**,祖先 `overflow:hidden`(设置抽屉/侧栏滚动区/输入外壳)直接裁掉;居中定位在视口边缘又横向溢出。修法:删伪元素,改**单例 fixed 层**(新 `web-src/src/tip.ts`,`main.ts` 挂载前 `installTips()`):按触发器 rect 定位 → 视口夹取 → 上方不够翻下方 → 滚动重算 / 尺寸变化与点按收起;文案一律 `textContent`(不解析 HTML) | 构建产物核对:dist CSS 含 `.gah-tip` 且**无**旧 `[data-tip]:hover::after`;Win 端人工目视 |
| **⑥ 无切换工作区入口** | `Sidebar.vue`「工作区」区原本只有列表/切换/删记录,**没有「打开文件夹」**。修法:区标题加「＋ 打开」→ 内联绝对路径输入(回车/打开/取消,Esc 取消,自动剥掉资源管理器「复制路径」的引号)→ 经全局确认条 → `POST /api/control{workspace}`(后端 `SwitchDir`:Chdir + 记入工作区历史 + 新建会话 + 通知宿主同步沙箱 root)→ 刷新列表 + 重建 SSE。**不用原生文件夹选择器**:浏览器安全模型拿不到绝对路径,且离线环境无法加 `tauri-plugin-dialog`(crates 缓存无该包) | 真机:切换 200 且 `GET /api/workspaces` 立即含新目录 `…/wsdemo`;[非移动端通用:浏览器/桌面同一实现] |

> **发布**:v0.1.1(2026-09-13)已出 —— macOS `gah_0.1.1_aarch64.dmg`(34.18 MiB)+ Windows `gah_0.1.1_x64-setup.exe`(31.59 MiB)+ updater `gah.app.tar.gz`/`latest.json`(实测 `releases/latest/download/latest.json` HTTP 200,`version=0.1.1`,双平台签名齐备)+ 命令行五目标归档与 `checksums.txt`;三个 workflow(`ci`/`release-cli`/`release-desktop`)全绿,流程与产物清单见 `docs/RELEASE.md`「发布记录:v0.1.1」。

### 未实施 / 待人工验(诚实登记)

| 项 | 状态 |
|---|---|
| Windows 真机 6 项人工复核 | ⏳ 需 Win 机器(清单见 `docs/VERIFY.md` 文末「剩余验收任务」B 段) |
| 原生文件夹选择器(桌面端) | ⏳ 需网络加 `tauri-plugin-dialog`(现已用路径输入覆盖同一能力) |
| 拖放非附件文件(图片直贴等)到窗口的其它落点 | ⏳ 本轮只放开 WebView 拖放,业务落点仍仅输入区 |

## R13 Windows 安装包桌面快捷方式缺失 ✅ (2026-09-14)

> 用户反馈:Windows 安装后**桌面没有图标**(开始菜单快捷方式正常)。
>
> 根因(读上游 tauri v2 模板 `installer.nsi`,与本地 CLI 2.11.4 / tauri-utils 2.9.3 核对):
> ① 桌面图标只在**完成页复选框**被勾选时创建(`MUI_FINISHPAGE_SHOWREADME_FUNCTION = CreateOrUpdateDesktopShortcut`);
> ② 该函数内部还有守卫 —— `$UpdateMode = 1`(覆盖安装/升级)或 `$NoShortcutMode = 1`(`/NS`)**直接 return**:
>    因此「先装 0.1.0 再装 0.1.1」的机器桌面无图标,而开始菜单快捷方式不受这两个守卫影响(所以只缺桌面);
> ③ 静默/被动安装(updater 走这条)模板会自动建,故不是全场景缺失。
>
| 模块 | 交付 | 验证 |
|---|---|---|
| **修法** | 新增 `desktop/src-tauri/nsis/hooks.nsh` + `tauri.conf.json` `bundle.windows.nsis.installerHooks`:`NSIS_HOOK_POSTINSTALL` **无条件**建 `$DESKTOP\${PRODUCTNAME}.lnk` → `$INSTDIR\${MAINBINARYNAME}.exe`(主程序 `gah-desktop.exe`,**不是** sidecar `gah.exe`),并复用模板 `SetLnkAppUserModelId`(任务栏分组/固定行为一致);`NSIS_HOOK_POSTUNINSTALL` 按 `IsShortcutTarget` 判定后删除(卸载兜底,不动用户改过目标的快捷方式)。`${...}` 在宏**插入点**展开,故 hooks 先于 `!define` 的 include 顺序不影响 | 配置键对本地 `@tauri-apps/cli` 自带 `config.schema.json`(2.11.4)逐层校验通过(`NsisConfig` 允许键集 = {installMode, installerHooks});NSIS 实际编译在 release-desktop 的 windows job 内完成;真机验收 = docs/VERIFY.md **B-5 第 159 条** |
| **发行** | 走 tag 驱动发 v0.1.2(不改 `tauri.conf.json` 的 version;不移动已发布的 v0.1.1 标签) | ✅ 2026-09-13:三个 workflow(`ci`/`release-cli`/`release-desktop`)全绿;`desktop-win-x86_64` 内 NSIS 编译通过(= hooks 路径能被 bundler `canonicalize` 到且 `!include` 成功);Release 含 `gah_0.1.2_x64-setup.exe`(33,140,083 B)/ `gah_0.1.2_aarch64.dmg` / `latest.json`;`releases/latest/download/latest.json` 实测 **0.1.2**(win sig 412) |

## R14 v0.1.3 发行实证 ✅ (2026-09-15)

> 用户要求:Windows 平台修复全部收敛后发一版。HEAD = `d0aa248`(`ci` **5/5 绿**:`test` / `test-windows` / `test-macos` / `desktop-shell` / `desktop-shell-macos`)。
>
> 本版相对 v0.1.2 共 **18 个提交**,其中产品代码改动 **10 个文件**(其余为测试适配 / CI / 文档):桌面壳托盘与升级入口 3 件(`main.rs` / `tauri.conf.json` / `SettingsPanel.vue`)、Windows 真 bug 6 件(`host-docview` 句柄泄漏、`host-jobs` 进程树终止与 `WaitDelay`、`internal/mcpconfig` 命令拆词、`host-backup` 绝对路径判定、`host-cwd-sessions` + `tui/app.go` 短长名归一)。这是**首个包含「托盘图标可见 / 菜单可弹 / 界面内升级入口」的版本** —— 那三个症状正是 Windows 真机试装 v0.1.2 时报出的。

| 项 | 证据 |
|---|---|
| **tag** | `v0.1.3`(annotated;正文即 Release notes —— `.goreleaser.yaml` `changelog.disable: true` 保证两个 workflow 并发时正文归属唯一)。版本号**不手改任何文件**:goreleaser 镜像 / sidecar `-X main.version` / tauri bundle version 三处均由 tag 注入;两份 README 的下载链接都是 `/releases/latest` 动态链接,亦无需改 |
| **release-cli** | run `34938346058` ✅ —— goreleaser 十件归档(darwin/linux/windows × amd64/arm64 × tar.gz/zip)+ `checksums.txt`(1,073 B) |
| **release-desktop** | run `34938346051` ✅ —— `desktop-mac-aarch64` / `desktop-win-x86_64` / **`merge-upload`** 三 job 全 success(这次 `merge-upload` **真的上传成功**;对比 v0.1.0 首发时它因缺 `permissions: contents: write` 报 403 `Resource not accessible by integration`,已修补) |
| **Release 内容** | `draft=false` / `prerelease=false`;15 个 asset:`gah_0.1.3_x64-setup.exe`(33,152,441 B)、`gah_0.1.3_aarch64.dmg`(35,852,557 B)、`gah.app.tar.gz`(35,745,661 B)、10 件命令行归档、`checksums.txt`、`latest.json`(1,215 B) |
| **updater 端点** | `gh api …/releases/latest` → **v0.1.3**;`latest.json` 内 `darwin-aarch64` → `gah.app.tar.gz`、`windows-x86_64` → `gah_0.1.3_x64-setup.exe`,两条的**签名内联在 json 字符串里**(故 Release 里没有独立 `.sig` 属正常,不是漏传);签名注释的 `file:` 字段与 asset 名逐字一致 |

**关于「Windows 侧缺 `*-setup.exe.zip`」的判定(本轮核实,非缺陷)**:`scripts/publish-desktop.sh:103` 的注释与 `release-desktop.yml` 上传段的 pattern 都保留 `*-setup.exe.zip` 只为兼容未来改名;当前 tauri 版本**直接签裸 `-setup.exe`**(`tauri-plugin-updater` 的 `extract_exe` 分支接受它),`latest.json` 指向的也正是裸 exe —— **自洽,无需补传**。DESIGN.md 早前 v0.1.0 记录里「Windows updater 只认 zip 变体」的说法在当前版本已不适用,以此条为准。

**未闭环(诚实标注)**:① **真机验收仍未做** —— 本轮的验收止于「打包链全绿 + 产物齐全」,托盘与 GUI 观感必须装机复验(`docs/VERIFY.md` 剩余人工项);② macOS 侧只做了产物与签名核对,未挂载实跑(上一次挂载实跑是 v0.1.0);③ **自动升级的端到端未验证** —— 「从 v0.1.2 点检查更新升到 v0.1.3」这条路径需真机跑。

## R15 Windows 真机白屏 / 外置暂存失败 / 应用图标 (2026-09-15,真机复验待做)

> 用户反馈三条,按发现顺序:① **v0.1.3 装到 Windows 真机打开白屏**(同机 v0.1.2 正常);② 换成带壳侧日志的诊断包后界面能出来了,但报「无法把运行文件放到用户数据目录(替换 `…\bin\gah.exe` 失败:拒绝访问。(os error 5));本次退回应用目录内运行」;③ 托盘与桌面只有默认图标,要求做扁平化 / 极简风的应用图标。

**① 白屏的机制(已定,与具体设备无关)**:Tauri **先创建窗口、后跑 setup**,且 setup **没有 `catch_unwind`** —— `tauri/src/app.rs` 里窗口在 2524–2525 行建好,`setup` 在 2531 行才被调用。于是 setup 内任何一个 panic 都会让进程直接退出,而此刻窗口已经创建、splash 还没渲染完 → 用户看到的就是「窗口白闪一下就没了 = 白屏」,**界面连一次启动机会都没有**。v0.1.2→v0.1.3 的全部改动里,唯一新增的启动期 panic 点正是托盘图标那两个 `.expect()`(`default_window_icon().cloned()` 与 `TrayIconBuilder::build()`;Windows 侧托盘要把 `Image` 转 `HICON` 并 `Shell_NotifyIcon` 注册,两者都可能失败)。**修法不依赖这个判断成立**:托盘与图标失败一律降级成「记一行日志、继续启动」,`sidecar` spawn 失败也不再 panic 而是把原因与数据根/日志路径直接写进页面 —— 界面是主体,托盘只是便利入口。同时新增壳侧诊断日志 `<用户数据目录>/gah-shell.log`(**逐行追加**,崩溃前写下的行留得下来),让「机器上取不到证据、只能靠读代码猜」这件事不再发生。

**② 暂存失败的根因(已证)**:`stage_sidecar` 用**固定文件名** `bin/gah[.exe]` + `rename` 覆盖,而 **Windows 不允许覆盖正在运行的 exe** —— 旧版 gah 只要还在跑(桌面壳关窗口 ≠ 退出、驻留托盘;或用户按上一轮分诊指引自己开着 `gah --profile web`),rename 就必然 EACCES,然后一路降级到「在应用目录内运行」。**修法**:副本文件名改**内容寻址** `gah-<版本>-<内容指纹>[.exe]`(指纹 = 源大小 + 源 mtime),目标名按内容定、此刻必然不存在 ⇒ 不可能撞上文件锁;同名即同内容可直接复用;`.gah-stage` 标记文件随之取消。配套 `prune_stale` 尽力清理历史副本(**删不掉一律忽略** —— 正在运行的旧副本本来就删不掉,下次启动它不在了自然会清;清理失败绝不该影响本次启动),且**显式跳过目录**:同级 `gah-data/` 是用户数据,误删后果比留垃圾严重得多(有回归测试守着)。顺带:启动前若 2233 已在服务(典型是旧实例还驻留托盘),壳侧日志记一行 —— 这种现象极易被误读成「升级没生效」。

**③ 图标**:新增 `desktop/icons/gen-icon.mjs`,把**几何与配色作为唯一源**,同时产出 `gah-icon.svg`(人读的设计稿)与 `gah-icon-1024.png`(交给 `tauri icon` 生成整套)。门面用**终端提示符 `>_`**:gah 是跑在本机的 agent 运行时,这个符号一眼说明是给开发者用的;扁平单色、无渐变无阴影,16px 托盘尺寸下仍认得出。配色取 `web-src/src/style.css` 的 `--accent`(**#4176e6**),界面与图标同色。自画 + 自写 PNG 编码器,不引任何依赖(macOS 上装了 rsvg/cairosvg/ImageMagick 的环境反而是少数)。

| 项 | 证据 |
|---|---|
| **CI(诊断包)** | run `35000612291` ✅ —— `desktop-win-x86_64` / `desktop-mac-aarch64` 全 success;`merge-upload` **skipped**(未打 tag,刻意只出 artifact、不碰 Release)。**本次同时验证了新 `icon.ico`(16/24/32/48/64/256 六档、条目全为 PNG 压缩)能被 NSIS 安装器构建接受** —— 这是换图标唯一的真风险点 |
| **产物** | `gah_0.1.3_x64-setup.exe` **33,136,912 B**,比上一版诊断包小 **22,036 B** —— 与 `icon.ico` 由 42,769 B 降到 19,321 B(差 23,448 B)吻合,是「新图标确实被嵌进产物」的旁证 |
| **本机** | `cargo check --locked` 0 error(重跑 tauri-codegen 成功解码新 `.ico`);`cargo test --locked` **19 passed**(含新增的「清理绝不碰 `gah-data/`」回归);顺手清掉两处既有 dead code(`httpGET` 早已被 `httpGETAuth` 取代、`StageOutcome.staged` 此前只在测试里读),dead_code 警告 2 → 0 |

**未闭环(诚实标注)**:① 三条修复**都还没在 Windows 真机复验** —— 白屏那条目前只能说「不再可能由托盘 panic 引起」,暂存那条要看到降级告警消失,图标那条要看托盘与桌面快捷方式的实际观感;② 白屏的**首要嫌疑**(Windows 上托盘 `build()` 失败)在 macOS 上无法验证,本轮是靠「把可失败点变成可观测」收敛的,**不是靠复现**;③ 若真机仍白屏,`gah-shell.log` 会指出死在哪一步 —— 那才是根因判定的依据。

## R16 白屏二次排查 / 壳侧自检 / ACL 修复 (2026-09-16,真机复验待做)

> 用户报「装了带图标与暂存修复的诊断包后**又变成白屏**」。本轮先穷尽本机能做的排除,再把壳里的黑盒全部照亮,并顺手挖出并修掉一个**用户明确要的功能其实是坏的**的真缺陷。

**已排除(都有硬证据,不是推理)**:① **前端**——本机 CDP 驱动 headless Chrome 直取真实 DOM:`/` 与 `/?shell=desktop`(注入 `__TAURI__` 桩)的 `#app` 均有子节点、`mounted=true`;前端根本不读 `?shell=desktop`(壳的注释说「web UI 依 shell=desktop 走桌面布局」并不成立);`web-src/src` 全仓无 `isPermissionGranted`,bundle 里只有一处 `toSorted`(ES2023)且不在启动路径,而 v0.1.3 前端新增的 48 行全是 ES2020 以内语法(`?.`/`??`/`globalThis`),vite `build.target` 已锁 `es2020`;清点本机 `web/dist` 的 mtime 与内容(含「检查更新」字样)**确认测的就是 v0.1.3 的产物**,不是旧包。② **sidecar**——本机 `go build` 后 `nohup gah --profile web` 实测 `/`、`/?shell=desktop`、`/?x=1`、`/?shell=web` 全 200/602B 且不崩。③ **壳的启动链路**——`scripts/gen-desktop.sh` 产出与发行同一形态的产物,**在本机 macOS 上把桌面壳完整跑起来**:`external=true`、staged 副本 `bin/gah-0.1.0-5ceb9286`、`lsof` 确认**持有 2233 的正是这个 staged 二进制本身**、导航后自述 `mounted=true / title="gah Web" / bodyLen=22707`。⇒ **白屏在 macOS 上复现不了,是 Windows 特有现象**;第一次跑还意外复现了「旧实例占着 2233、探活命中的是旧实例」这个极易误读的场景,证明新增的端口占用日志正是为它准备的。

**壳侧黑盒全部照亮(本轮真正的产出)**:① `let _ = w.navigate(u)` 吞错误 → 改为记录 navigate 结果、单独记录「取不到 main 窗口」;② 新增 `spawnSelfCheck`:**导航后两轮(3s/7s)用 `eval_with_callback` 向当前文档索要一份自述** —— `href`/`readyState`/`title`/`#app` 子节点数/`typeof __TAURI__`/未捕获异常/前端→壳 IPC 结果/正文前 200 字,写进 `gah-shell.log`;两轮都拿不到回话 ⇒ WebView2 渲染进程不可用(白屏的直接成因,应用内无法自救),`href` 仍停在 `tauri://` ⇒ 导航没生效,`href` 是 `127.0.0.1` 而 `#app` 为空 ⇒ 页面送达却没渲染。用 `eval_with_callback` 而非 IPC,**不需要改前端、不需要新命令**;③ 早期错误捕获钩子(导航后 30×150ms 小剂量重注入):ESM 延迟执行,命中新文档的那次能赶在 bundle 之前装上监听器,于是前端任何未捕获异常都能被下一轮自检读出来(带 300 字栈);④ 兜底面板:已连上 `127.0.0.1` 却连 `#app` 都没挂上时,2.5s 后把「版本+数据根+导航目标+壳侧日志尾部+页面正文+前端错误」直接铺满窗口 —— **宁可难看,也不要让用户对着一片白屏只能报「白屏」**。判定前先看 `hostname`:splash 是 `tauri://localhost`,它本来就没有 `#app`,不能误伤。

**挖出的真缺陷:桌面壳从未有任何 capabilities/permissions,于是前端→壳的命令一律被 ACL 拒掉** —— 错误捕获钩子当场捞到 `shell_probe not allowed. Plugin not found`,并同时捞到一条插件全局 API 的 `notification.is_permission_granted` 被拒。**后果是用户上一轮明确要的「界面内升级入口」其实一直不通**(按钮点下去只会拿到拒绝)。修法:Tauri v2 的应用自有命令要用 `permissions/*.toml` 声明(`commands.allow` 里写命令原名),再在 capability 里按**标识符**引用 —— 命令名 `check_update` 含下划线不能直接当标识符(这是最初报错的原因),故新建 `permissions/app-commands.toml`(标识符 `allow-app-commands`)+ `capabilities/default.json`(`windows:["main"]`、`remote.urls:["http://127.0.0.1:2233"]`、`core:default`)。**实测 `ipc: ok: ok`** ⇒ 该功能首次端到端打通。

**顺手做的 A/B:`withGlobalTauri` 关掉**。它是 v0.1.2→v0.1.3 **唯一**的配置差异(`tauri.conf.json` 整个 diff 只有这一行;main.rs 那 192 行里没有任何能造成白屏的东西,已逐行过)。关掉后前端改走 `__TAURI_INTERNALS__.invoke`(壳的 init 脚本**总会**注入它,它本来就是 IPC 通道本体;`__TAURI__` 只在 `withGlobalTauri` 打开时才有)。好处:sidecar 页面不再被塞进整套 Tauri API 与各插件的 JS 全局(实测那会带来必然被 ACL 拒掉的无谓请求)。**实测:`tauri: undefined`(全局确实没了)而 `ipc: ok: ok`(通道照通)** ⇒ 关掉不损失功能。若白屏真凶就是这次注入,则下一版直接可用;若不是,日志会给出结论。

| 项 | 证据 |
|---|---|
| **本机端到端(macOS)** | 全链路:端口清空 → staged 副本 → spawn → 探活 → navigate → 自述 `mounted=true`;`lsof` 证实 2233 由 staged 二进制持有 |
| **ACL 修复** | 修复前 `ipc: denied: shell_probe not allowed. Plugin not found` → 修复后 **`ipc: ok: ok`**;`gen/schemas/acl-manifests.json` 收录 `allow-app-commands`/`shell_probe` |
| **A/B** | `tauri: undefined` + `ipc: ok: ok` + `mounted=true` ⇒ 关掉 `withGlobalTauri` 不损失功能 |
| **回归** | `cargo check --locked` 0 error / `cargo test --locked` 19 passed;前端 `npm test` **38 pass / 0 fail**;`go build ./...` 通过 |

**未闭环(诚实标注)**:① **白屏根因仍未确定** —— 本轮只做到「本机无法复现 + 日志已具备定位能力」,`withGlobalTauri` 只是唯一剩下的变量、**不是已证实的病因**;② 上述全部证据来自 macOS,Windows 上的托盘/WebView2/x64-on-ARM64 归因**无法在本机验证**;③ `notification.is_permission_granted` 被拒仍会出现在每个页面(关掉 `withGlobalTauri` 也在,说明调用方不是插件全局 JS 而是更底层的东西),**已确认无害**(v0.1.2 路径里同样存在且页面正常),故本轮不动它 —— 记为待查;④ 还没在真机上验证过下载安装、托盘观感、检查更新按钮的实际点击效果。

## R17 白屏闭环 ✅ (2026-09-16,真机复验通过)

> 用户装了 run `35050302870`(commit `ea86f06`,sha256 `8c566a73…`)的安装包后回复:**「白屏问题修复了」** —— 第二次白屏闭环。

**归因(诚实标注)**:这一版与白屏的那一版同时落地了两处改动,单次真机结果**无法区分**是谁起的作用:

1. **`withGlobalTauri` 由 `true` 改为 `false`** —— 它是 v0.1.2→v0.1.3 **唯一**的配置差异;打开时它给**每一个文档**(包括 sidecar 那个远端页面)注入整套 Tauri API 与各插件 JS 全局,并在加载期做原型混入 + `Object.freeze(Object.prototype)`。
2. **补上 capabilities/permissions** —— 此前前端→壳的命令一律被 ACL 拒掉。

**首要嫌疑是第 1 条**,理由是第 2 条单独解释不了「v0.1.2 正常 / v0.1.3 白屏」:v0.1.2 同样没有任何 capability 却能正常显示,而被拒的 IPC 只是 unhandled rejection(不会阻止页面渲染)。**但没有做隔离验证**(把 `withGlobalTauri` 单独打开再让用户装一次)—— 那意味着很可能又让用户白白吃一次白屏,收益不值。

**因此立为长期约定**:`withGlobalTauri` 保持 `false`;前端经 `__TAURI_INTERNALS__.invoke` 调壳(壳的 init 脚本总会注入它,它本来就是 IPC 通道本体;本机实测 `ipc: ok`)。日后若真需要 `__TAURI__` 全局,**必须先在 Windows 真机上单独验证这一项**,不得直接跟着别的改动一起上。

**这轮真正的教训**:两处改动一起上,于是修好了也不知道是怎么好的 —— 本来就是为「白屏不可观测」才做的自检,结果自己又制造了一个不可归因的局面。排查类改动应当一次只上一个变量。

| 项 | 证据 |
|---|---|
| **真机** | 用户复验:**白屏已修复** |
| **产物** | run `35050302870` → `gah_0.1.3_x64-setup.exe` **33,186,535 B**,sha256 `8c566a73c10c0134eef93e2af443f791484b5835df397c1accc0cc968e69c227`;`merge-upload` skipped(未打 tag) |
| **CI** | `ci.yml` run `35050292235`(commit `ea86f06`)**5/5 success**;`release-desktop` macOS 与 Windows 两个 job 均 success |

**仍待真机确认(打 v0.1.4 之前)**:① 上一轮那条「无法把运行文件放到用户数据目录…拒绝访问」降级告警**是否已消失**(内容寻址命名那次的复验,从未真正确认过);② 托盘与桌面/开始菜单**是不是新的蓝色 `>_` 图标**;③ 托盘**左右键都弹菜单**;④ 设置面板「关于 gah / 检查更新」**可见且点击有反应**(ACL 修好后的首次真机验证)。

## R18 真机七项反馈:修复四项 + 两个同源隐患 (2026-09-16,真机复验待做)

> 用户装了 `ea86f06` 的包后反馈七条。上一轮 R17 的教训是「排查类一次只上一个变量」;本轮都是**功能修复**,所以合并成一次装机验完,少让用户来回。

**先确认已好的三条**(对应 R17 的待确认清单 ①②③):① 「无法把运行文件放到用户数据目录…拒绝访问」降级告警**已消失**;② 托盘与桌面/开始菜单**已是新的蓝色 `>_` 图标**;③ 托盘**左右键都弹菜单**。⇒ 内容寻址暂存、应用图标、`show_menu_on_left_click(true)` 三项修复全部真机落地。

### 修的四条

| # | 现象 | 根因 | 处置 |
|---|---|---|---|
| 4 | 托盘菜单里没有「关于 gah」;点「检查更新…」「开机自启」没反应(「显示窗口」有反应) | ① 托盘菜单本就没有「关于 gah」项(它在设置面板里);② 「开机自启」是普通 `MenuItem`,切完不回写状态、不给任何反馈;③ 「检查更新」唯一的反馈是系统通知,通知不来就等于没反应 | 托盘新增「关于 gah」(版本 / Web 地址 / 数据根 / 日志路径,原生对话框);「开机自启」改 `CheckMenuItem` 并按**实际状态**回写勾选 + 落日志;检查更新结果改走**原生对话框 + 通知**双通道 |
| 5 | 上传附件报 `HTTP 400: 附件不可用: (空路径)` | 契约错位:`handleAttachments` 返回 `{ok, attachments:[视图]}`,而前端 `api.upload` 当**单个视图**用 ⇒ `v.url === undefined` ⇒ `JSON.stringify` 把 `undefined` 变 `null` ⇒ 服务端解成 `""` ⇒ `resolveAttachment` 报「空路径」 | `api.upload` 就地拆包,缺 `url` 显式抛错(不再退化成「提交时才发现」);新增 `api.test.ts` 三条回归(拆包 / 缺 url / 非 2xx) |
| 6 | 新建工作区只能手输路径 | `Sidebar.vue` 原注释「浏览器拿不到文件夹路径,故不用选择器」在桌面壳里不成立 | 桌面壳走 `plugin:dialog|open` 系统文件夹选择器(「＋ 打开」直接弹出,另留「✎ 路径」手输入口);浏览器形态保持原样;新增 `web-src/src/desktop.ts` 收敛壳桥接 |
| 7 | 设置里已选模型,但「当前模型」不显示 | 两个真源:高亮读 `state.model`(运行时权威),合成的「(当前)」项却读 provider 配置默认值;且 watcher 依赖里**漏了 `props.state.model`**,选完不重算 | 抽出 `web-src/src/modelsel.ts`(唯一真源 = `state.model`);watcher 补上 `state.model` 并 `immediate`;新增 5 条单测(含 provider 不一致 / 重名歧义 / 空态) |

### 顺带修掉的两个隐患(与真机症状同源)

1. **壳不再连固定 2233**:改用 `pick_free_port()` 现挑空闲端口,经 `GAH_WEB_ADDR`(web 插件早已预留的覆写口,此前壳从未用)交给 sidecar。固定端口会撞上**别人的**实例 —— 壳崩溃留下的孤儿 sidecar,或用户自己开的 `gah web` —— 于是「装的明明是新版,界面却没有新功能」。capability 的 `remote.urls` 相应改为 `http://127.0.0.1:*`(URLPattern 通配);**本机实测随机端口下 `ipc: ok`**,通配确实生效。这一条不实测就等于把「关于 gah」再改坏一次:ACL 匹配不上会**静默**拒掉命令。
2. **父死子死**:sidecar 新增 `GAH_WEB_PARENT_WATCH=1` 监视(仅壳设置,手工 `gah web` 不受影响),靠 **stdin EOF** 判定父进程消失 ⇒ 走与 `/api/shutdown` 同一条优雅退出,5 秒兜底强退;壳侧 `RunEvent::Exit` 同时改为无条件回收。**本机实测 `kill -9` 壳后 sidecar 在 3 秒内消失**。

另:sidecar 的 stderr 现**追加进 `gah-shell.log`**(此前只在事件里一闪而过)⇒ 服务端错误(如那条「附件不可用」400)首次变成真机上可取回的证据。

| 项 | 证据 |
|---|---|
| **本机** | `cargo check --locked` 0 error;`cargo test --locked` 19 passed;`npm test` **51 pass / 0 fail**(+13);`vue-tsc` 类型检查通过;`go build ./...` + `web`/`ui-web-app` 包测试通过;端到端实跑:随机端口 `63629`、`mounted: true`、`ipc: ok`、bodyLen 22707→22777(新前端确实被服务) |
| **真机** | 待复验(见下) |

**待真机确认(打 v0.1.4 之前)**:① 托盘「关于 gah」能弹出、内容正确;② 托盘「检查更新…」有可见反馈(对话框);③ 托盘「开机自启」勾选态可切换且重开仍是实际状态;④ 选文件夹按钮能弹出系统选择器并选中目录;⑤ 上传附件(图片与文本各一)**不再报「空路径」**;⑥ 设置面板「当前模型」显示为刚选的模型。

## R19 白屏第三次回归 + 检查更新无进行中反馈 (2026-09-16,定位待用户日志)

> 用户对新包(R18)的反馈原文:**「还是白屏的,托盘检查更新没有反馈,即使是检查中也要有提示,托盘其他项正常」**。分开处理:后半句本轮直接修完;前半句先补观测 —— **白屏已经靠猜修过两次,不能再猜第三次**。

### 检查更新(已修)

| 问题 | 处置 |
|---|---|
| 点了没任何反馈 | 点击**立刻**把托盘项换成「检查更新中…」并禁用 + 发一条「正在检查更新…」通知;结束时恢复文字、结果走原生对话框 |
| 为何两轮都「没反应」 | 最可能:检查要连 GitHub,**网络到不了就无限期挂住**。加 45 秒超时并给出「可配代理后重试 / 直接去 Releases 下包」的指引 |

两条反馈通道职责不同,必须都有:通知立刻到手但系统可能不弹;菜单文字必然可见但要再点开托盘才看得到。

### 白屏:先把失败路径全部变成看得见的东西

已知的事实(而不是推测):

- 真机「关于 gah」能弹出、能看内容 ⇒ **壳进程活着、托盘与对话框插件都正常**。
- 壳里本来就有兜底:导航后 +3s/+7s 自检,`mounted === false` 时会把诊断面板铺满窗口(见 R16);探活超 12 秒也会注入「gah 服务未能启动」。⇒ **纯白屏意味着这两种情况都没发生**,更可能是「页面挂载正常、窗口渲染不出东西」或「用户没等到错误页就关了窗口」。

因此本轮不再猜根因,而是把每种失败都变成**日志里的一行 + 窗口上的一屏**:

| 失败路径 | 以前 | 现在 |
|---|---|---|
| sidecar 进程提前退出 | 要干等探活超 12 秒 | `CommandEvent::Terminated` 立刻写日志 + 把退出详情铺到窗口 |
| 探活超时 | 手拼 HTML 提示,无现场 | 共用 `showShellDiag`:标题 + 说明 + **壳日志尾部**(sidecar 的 stderr 也在里面,绑定失败/插件崩溃一目了然) |
| 父进程监视误判 | 就绪前 EOF 会立刻自杀 ⇒ 界面永远停在启动页且无痕迹 | 加就绪门:**宁留孤儿也不自杀** |

### 待用户提供(一次就能定性)

1. **托盘 →「关于 gah」的弹窗内容**(尤其版本号与 Web 地址:地址是 `127.0.0.1:2233` = 旧包,是随机端口 = R18 包)。
2. **`%LOCALAPPDATA%\dev.gah.desktop\gah-shell.log`** —— 里面会直接给出是哪一种失败:自检自述(`mounted`/`errs`)、探活结果、`navigate` 结果、sidecar 自己的 stderr。
3. **窗口现在显示什么**:纯白 / 小灰字「正在启动 gah 服务…」/「gah 服务未能启动」。

### 本轮验证

`cargo check --locked` 0 error;`go build ./...` + `ui-web-app` 包测试 15 passed;`gofmt` 干净;新增 tokio(time) 依赖(tauri 运行时本就是 tokio,不引入新版本)。

## R20 交互四项缺陷:选择器无反应 / 切换不刷新 / 初始工作区 / 版本号 (2026-09-17,真机复验待做)

> 用户原文:**「＋ 打开和浏览… 点击无反应;通过输入路径切换,确认之后实际切换了,但是界面没变化,点击刷新后新切换的路径才显示,输入路径的界面未消失,之前输入的路径仍然显示」**,并附启动日志与截图。

### 首先:R19 的白屏第三次回归确认不复现 ✅

用户提供的日志证明本轮包正常:随机端口 `http://127.0.0.1:61170`、sidecar 外置暂存、`托盘已就绪`、`导航已受理`、自检 `mounted:true / appChildren:1 / bodyLen:22740`、`ipc:"ok: ok"`、`errs:[]`,且上一轮的 `notification.is_permission_granted not allowed` 也不再出现。R19 补的失败可见化没有触发 —— 说明那三种失败路径本轮都没走到。

### 缺陷一:「＋ 打开」「浏览…」点击无反应(已修)

症状是**页面侧静默失败**:`pickDirectory` 走的是 `plugin:dialog|open`(JS 插件通道),真机上既不弹选择器、页面上也没有可见报错(错误只落在侧栏底部那行 `.err`,而它在屏幕外)。

处置:改走**壳自有命令** `pick_folder`,三条理由 ——

1. 自有命令通道**在同一台机器上实测是通的**(`shell_probe` → `ipc: ok`),而 JS 插件通道此前已被 ACL 拒过一次;
2. Rust 侧 `pick_folder` 与**真机已验证能弹出来的**「关于 gah」用的是同一个 dialog 实现;
3. 顺带把「有没有弹、选了什么」写进壳日志 —— 选择器是否真的弹出,只有它能自证。

`pick_folder` / `shell_log` 已入 `permissions/app-commands.toml` 的 `allow-app-commands`(capability 随之**撤掉**不再需要的 `dialog:allow-open`,保持最小权限)。本机实测:探针调用后壳日志出现 `web: 打开文件夹选择器` ⇒ 命令已穿过 ACL 到达 Rust 并开始弹选择器。

### 缺陷二:切换成功但界面不刷新(已修)

后端 `SwitchDir` 的顺序是 **`os.Chdir` → 记工作区历史 → 新建空会话 → 通知宿主重启外部工具进程并同步沙箱 root**。也就是说「成功」有很多中间态,而前端只认那一个 HTTP 响应:响应慢或失败时界面就停在旧状态(用户只好点 ↻)。本机实测这段在 macOS 上是 42 ms,但**重启外部工具进程在 Windows 上可能慢得多**。

处置:**以机器状态为准** —— 请求结果与「目标目录出现在工作区列表里」赛跑,谁先到谁定论;无论结果如何都在 `finally` 重拉列表并广播 `session-changed`。目录比较用新抽出的 `sameDir`(归一分隔符/大小写/尾分隔符,配单测);失败信息就地显示在输入面板里,不再只出现在屏幕外那一行。

### 缺陷三:初始工作区落在 `C:\WINDOWS\system32`(已修)

壳被自启/快捷方式拉起时 cwd 由进程管理器给定 —— 真机日志里就是 system32,于是 agent 的默认工作区落在系统目录上(既不是用户想要的,让 agent 在那儿写文件也不安全)。处置:`defaultWorkspace()` 在 **cwd 属于系统目录时**改用用户主目录(Windows 判 `%SystemRoot%`,类 Unix 判 `/`、`/System`、`/usr`);手工从某个项目目录拉起时仍沿用那个目录(那是明确的意图)。本机实测日志:`sidecar 初始工作目录: /Users/nekoleamo`。

### 缺陷四:版本号自相矛盾(已修)

「关于 gah」用的是 `package_info().version`(tauri 配置版本,发布时由 tag 注入),而**暂存文件名**用的是 `env!("CARGO_PKG_VERSION")` = Cargo.toml 里永不跟版本的 `0.1.0`(Cargo.toml 与 tauri.conf.json 都停在 0.1.0,靠 `--config` 覆盖 tauri 那份)。于是真机上出现「关于 gah 说 0.1.3、文件却叫 `gah-0.1.0-a7625a51.exe`」。统一到产品版本。

附带发现:CI 浅克隆没有 tag 时 `git describe` 会失败并回落 `0.1.0`,所以**诊断包的版本号只看安装包文件名**(NSIS 用 `--config` 注入的那份)。

### 本轮新增的观测能力(为下一轮定位服务)

| 新增 | 作用 |
|---|---|
| `shell_log` 命令 + 前端 `shellLog()` | 页面侧失败首次能落进壳日志(桌面版没有终端,这是唯一取证通道) |
| `web/server.go` 工作区切换日志 | 开始 / 完成(耗时)/ 失败(耗时 + 原因)——「请求挂住」与「请求立刻报错」在日志里长得完全不同 |
| sidecar stderr 滤 `[DEBUG]` 后落盘 | 服务端错误可取回,但 go-plugin 的噪音清掉(一次切换几十行,会把 `showShellDiag` 要展示的日志尾部挤满) |

附带溯源:自检里长期存在的 `rejection: notification.is_permission_granted not allowed` **不是本项目的前端调用**,而是 `tauri-plugin-notification` 自己注入的 init 脚本在页面加载时做权限自检(`src/init-iife.js`);它只在非 Windows 上会发这条 invoke(源码里是 `"default" !== Notification.permission || __TEMPLATE_windows__ ? ... : invoke(...)`,Windows 直接短路)。最小权限面下它必然被拒 —— 想让它消失就得给页面 `notification:default`(页面并不需要),所以保留为已知无害噪音,不再当待查项。

### 本轮验证

本机(macOS)端到端实跑:切换 `HTTP 200` + 日志「web: 切换工作区完成 …… 耗时=42.6ms」;不存在目录 `HTTP 400` + 「web: 切换工作区失败 …… err=chdir … no such file or directory」;前端 `npm test` **57 pass**(`vue-tsc` 0 error;新增 `desktop.shell.test.ts` 3 条、`wsdir.test.ts` 3 条);`cargo check --locked` 0 error;`go build ./...` + `gofmt -l` 干净。

### 待真机确认(下一轮)

1. 「＋ 打开」/「浏览…」是否弹出系统选择器(日志里应有 `web: 打开文件夹选择器` + `web: 文件夹选择器返回 …`);
2. 输入路径切换后面板是否自动收掉、列表是否自动更新;
3. 初始工作区是否落在用户主目录;
4. R18 遗留的观察项:检查更新进行中提示、开机自启勾选、当前模型显示、附件上传不再报空路径。

## R21 真机第二轮:两个「无反应」共根 / 附件路径 / 当前模型与自启显示 (2026-09-17,真机复验待做)

> 用户原文:**「1、两种都仍然失效 2、成功 3、成功 4、变成检查更新中,但是一直处在这个状态,长时间未有变化 5、勾选开机自启后,关于 gah 的内容未有变化 6、列表已选中当前模型,provider 中也已展示当前模型,但是当前模型的位置、输入框中仍然为空 7、附件上传并分析成功,但过程中会先弹出提示:docview: 文件不存在: 20260916-213605」**,并附完整启动日志。R20 修的三项(切换以机器状态为准 / 初始工作区 / 版本号)真机确认全部生效 ✅ —— 日志里 `gah-0.1.3-70b61dd8.exe`(版本前缀已随产品版本)、`sidecar 初始工作目录: C:\Users\nekoleamo`、`web: 切换工作区成功: C:\test` 三条都在。

### 首要发现:1 与 4 是同一个根因 —— 真机上 async 命令从未进入函数体

日志给出的证据链是决定性的:四次点击 `＋ 打开/浏览…`,页面前端每次都写了 `web: 点击「＋ 打开/浏览…」:开系统文件夹选择器`,而**壳侧完全没有 `web: 打开文件夹选择器` 这一行**(那是 `pick_folder` 函数体的第一句),也**没有** `文件夹选择器失败: …`(那是 catch 分支)。两边都没有 ⇒ 既没执行、也没报错 ⇒ **前端的 `await` 永久挂起**。

同机对照:同步命令(`shell_probe` → `ipc: "ok: ok"`、`shell_log`)全通,走 `tauri::async_runtime::spawn` 的壳侧自检任务也在跑 ⇒ 卡住的是 **async 命令这条派发路径本身**,不是 ACL(同权限组的 `shell_log` 能用)、也不是网络。检查更新停在进行中同理:走托盘那条 `spawn` 的任务没回来,**连 45 秒超时都没触发**(超时那层自己也没跑)。

**诚实标注**:「为什么会这样」本机无法复现(macOS 上同一条 async 命令实测能弹框),所以本轮**不猜根因**,而是换成已被真机证明可用的通道,并把「到底走到哪一步」做成日志可自证。

### 缺陷一:「＋ 打开」「浏览…」再次无反应(已改通道,待真机复验)

选择器改为**三条同步命令 + 独立线程 + 轮询**:

| 命令 | 作用 |
|---|---|
| `pick_folder_begin(title)` | 同步置位状态并 `std::thread::spawn` 去弹框,立即返回 `started`/`busy` |
| `pick_folder_poll()` | 同步取结果:`{"status":"pending"}` / `{"status":"done","path":…}` / `{"status":"cancel"}`,取到终态即复位 |
| (原 `pick_folder` async 命令) | **已删除** —— 留着一个走不通的入口只会掩盖问题 |

三点理由:(1) 只依赖同机已验证的同步命令通道;(2) 阻塞的是自建线程,不再占用 async 运行时的工人(一个卡住全卡是怀疑中的连环效应);(3) `blocking_pick_folder` 的官方约束正是「不得在主线程调用」,普通线程是它期望的位置。前端 `pickDirectory` 改为 begin + 300 ms 轮询(上限 5 分钟,超时给结论而不是空等),`parsePick` 抽成纯函数并配单测。

本机验证:临时挂一次 begin,壳日志出现 `web: 文件夹选择器线程启动` 且原生选择器真的弹出(验证后已还原)。

### 缺陷四:检查更新永远停在「检查更新中…」(已修)

加了**看门狗**(75 秒):那一轮若还没有结果,说明 async 任务没回来 —— 写日志自证、解除菜单的进行中状态(不再卡死)、并弹框告知「这不是网络问题」。前端另加 75 秒兜底超时(设置面板入口),保证界面一定能给出结论。两个入口都补了入口/结束日志(耗时 + status)。

### 缺陷六:当前模型位置显示为空(已修)

根因是**标签与控件的语义错位**:`设置 → 模型` 那行里,`当前模型` 标签后面挂的是**筛选输入框**(placeholder 是「筛选模型名/Provider」),它本来就该是空的 —— 用户把它当成了「当前模型」的显示位,于是读成「已选了模型但显示为空」(上一轮 R20 修的列表高亮是另一处,用户确认那处已好)。处置:输入框占位符改为显示当前值(`当前:<provider · 模型> · 输入以筛选`),并在下方**单独一行**显示 `当前生效:<标签>`(取不到显示 `(未设置)`),满足用户提的「占位符显示或另开一行」。

### 缺陷五:开机自启改了但「关于 gah」没变化(已修)

那段里**根本没有自启信息**(只有版本/Web 地址/数据根/日志),用户自然会读成「没变化」。处置:托盘「关于 gah」与设置面板「关于 gah」都显示**按系统实际状态查询**的开机自启(新命令 `autostart_state`,不复用勾选态 —— 用户可能从系统设置里改过);设置面板每次打开都刷新一次,托盘勾完回来立刻能看到变化。

### 缺陷七:附件可用但先弹「docview: 文件不存在: 20260916-213605」(已修)

`docview: 文件不存在` 是 `sdk.ErrDocNotFound` 的原文,后面那串是**请求里的路径**。注入给模型的附件信息此前只给「相对附件根」的形式(`<时间戳>/<文件名>`),即一个**没有根**的路径 —— 真机上模型把目录名当成了文件名。两处一起修:

1. `web/server.go` 注入给模型的内容改为**同时给绝对路径与附件标识**:`1. <名>(文件路径 <绝对路径>;附件标识 /attachments/<rel>)`;
2. `host-docview` 的解析器加**回退**:相对路径在 workspace 下找不到时,再按附件根试一次,`/attachments/<rel>` 也兜住(两种都是模型可能给出的合法写法);**绝对路径不抽奖**(越界仍被拒,报错里保留用户给的原串便于对照)。

配 Go 回归 `TestResolveAttachmentFallback`(相对附件根 / 前端标识 / 越界拒绝 / 不存在时保留原路径 四种)。

### 本轮新增的观测能力

| 新增 | 作用 |
|---|---|
| **panic 钩子落壳日志**(`panic @ 文件:行`) | 桌面版没有终端。**async 任务里 panic 尤其致命**:没人回 IPC 响应 ⇒ 前端永久挂起 ⇒ 表现就是「点了没反应」。有了这行,「任务没被调度」与「任务跑了但崩了」立刻可分 |
| **自检通道矩阵**(`chan`) | 启动自检除 `shell_probe` 外再探 `probe_async` / `pick_folder_poll` / `autostart_state`,结果进 `页面自述.chan` —— 一条日志同时回答「同步/异步/选择器/自启四条通道通不通」 |
| `probe_async` 命令 | 只写一行「函数体已执行」,专门分辨「任务没被调度 vs 请求没到处理函数」 |
| `sidecar 事件循环已启动` | 区分「async 任务没跑」与「事件源没数据」(此前 stderr 一条都没有时无从判断) |
| sidecar **stdout** 也收(只收文本行、上限 200 行) | stdout 同时是 go-plugin 的二进制 RPC 通道,不能原样倒日志;但真机上若日志被写到 stdout,这里是唯一入口 |

### 本轮验证

本机(macOS)端到端实跑:壳日志 `sidecar 事件循环已启动` + 自检 `ipc:"ok: ok"` + `chan:{"pick_folder_poll":"{\"status\":\"pending\"}","autostart_state":"off","probe_async":"ok"}`;临时 begin 验证选择器线程 → 原生框弹出。测试:Go `host-docview` **122 passed**、前端 `npm test` **60 pass**(含新增 `parsePick`/超时/通道断言,`vue-tsc` 0 error)、Rust `cargo test` **20 passed**(新增 `pickJson` Windows 反斜杠转义)、`go build ./...` 与 `gofmt -l` 干净、`cargo check --locked` 0 error。

### 待真机确认(下一轮)

1. 「＋ 打开」/「浏览…」是否弹出系统选择器(日志应出现 `web: 文件夹选择器线程启动` → `web: 文件夹选择器返回 …`);
2. 设置 → 模型:输入框占位符与「当前生效」行是否显示当前模型;
3. 托盘勾选开机自启后,「关于 gah」(托盘与设置面板两处)是否随之变化;
4. 附件上传分析是否不再先弹「文件不存在」;
5. 检查更新:是否会给出结论(若卡住,应出现 `托盘: 检查更新第 N 轮 75 秒无结果 —— async 任务没有返回` 这行);
6. 若仍有问题,请提供 `%LOCALAPPDATA%\dev.gah.desktop\gah-shell.log` —— 现在里面有 channel 矩阵、panic 行与每一步的入口日志。

## R22 真机第三轮:选择器与附件闭环,设置面板与托盘实时对齐 (2026-09-17,真机复验待做)

> 用户原文:**「1、打开和浏览功能正常 2、占位符可以取消,只保留下面行 3、当设置界面显示时,如果通过托盘勾选开机自启和检查更新,设置界面不会立刻同步更新状态,要重新打开设置菜单才能同步 4、附件正常」**

**真机闭环两项** ✅:「＋ 打开」/「浏览…」在 R21 换成「同步命令 + 独立线程 + 轮询」后正常工作（确认了 async 命令通道在那台机器上确实不可用）;附件上传分析正常(R21 的绝对路径注入 + 解析器回退生效）。

### 1. 占位符撤销,只留「当前生效」行

R21 两样都给了(占位符显示当前值 + 单独一行),用户选后者。占位符回到原样(`筛选模型名/Provider`),避免把「筛选框」当作「当前值展示位」的语义错位再次出现。

### 2. 设置面板与托盘实时对齐(同一状态,两个视图)

根因是**两个视图各拿各的局部状态**:托盘勾开机自启 / 点检查更新,只改了托盘侧(菜单文字与系统状态),面板里的 `autoState`/`updBusy` 是另一个局部变量,只在面板打开时拉一次。

处置:**把检查更新状态变成壳侧单一真源**,两个视图都读它 ——

| 新增 | 作用 |
|---|---|
| `UPDATE_STATE` 快照(busy / seq / status / message / version) | 壳侧唯一状态;托盘路径与命令路径都写它(含看门狗那一条兜底) |
| `update_state` 同步命令 | 面板开着时每 1.5 秒轮询,托盘的进行中/结果 1.5 秒内就在面板里 |
| `applyUpdateSnapshot`(前端) | 进行中始终跟随;结束时**只认比本地见过的更新的那一轮**(`seq`)—— 否则轮询会把面板自己刚写的错误提示覆盖回去 |

开机自启同理:面板开着时轮询 `autostart_state`(直接问系统,不复用勾选态)。两处轮询都只走**已验证的同步命令通道** —— 不用 Tauri 事件通道,因为插件/事件那条 JS 通道在同台真机上已经静默失败过两次。

附带两处:

1. `autostart_state` 去掉每次调用的日志(轮询会刷屏),它的存在性已由启动自检的通道矩阵报出;
2. **命令路径也挂上 75 秒看门狗** —— 之前只有托盘路径有,面板自己的检查更新若任务不回来会永远「检查中…」,现在与托盘一致:解除进行中 + 给出结论。

### 本轮验证

本机(macOS)实测:启动自检通道矩阵 `chan:{"pick_folder_poll":"{\"status\":\"pending\"}","autostart_state":"off","update_state":"{\"busy\":false,\"seq\":0,…}","probe_async":"ok"}` —— 新命令已穿过 ACL。测试:Rust `cargo test` **21 passed**(新增 `updateJson` 转义与可解析性)、前端 `npm test` **61 pass**(新增 `parseUpdateState` 形状回归)、`vue-tsc` 0 error、`npm run build` ok。

### 待真机确认(下一轮)

1. 设置面板开着时,从托盘勾/取消开机自启 → 面板「关于 gah」是否 1.5 秒内跟着变;
2. 面板开着时,从托盘点「检查更新…」→ 面板是否先显示「正在检查…」再给出同一个结论;
3. 设置 → 模型:输入框占位符与「当前生效」行是否仍正常。

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
>
> 待确认后从 M1 微内核开始实施。
>