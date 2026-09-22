# go-agent-harness(gah)

> [English](./README_EN.md) | [简体中文](#)(中英同步维护)

Go 实现的编程代理 Agent Harness:以**单静态二进制**交付全部能力,零运行时依赖。对齐 DeepSeek Harness 与 Cordis 的「一切皆插件」设计哲学——微内核仅负责插件的加载/卸载/依赖管理(零 Agent 能力、零 UI),全部能力以插件形式经配置层(profile→bundle→patch)随时插拔开关。

> 设计参考:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)、[pi coding agent](https://www.npmjs.com/package/@earendil-works/pi-coding-agent)(TS 终端 harness)。
> - 完整设计:[DESIGN.md](./DESIGN.md)(§14.1 交付表/未实施清单)
> - 插件开发:[docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md);插件总览:[plugins/README.md](./plugins/README.md)

---

## 一、项目定位

**排除 dsh 因 Node.js 带来的依赖**:

- 单一静态二进制(`CGO_ENABLED=0`,五目标实测 **41–46 MiB**,压缩下载产物 ≈27–31 MiB;外部插件经 gzip 内嵌,体积门见 `scripts/size-check.sh`),无 node 运行时、无 node_modules 分发链、无版本管理器;
- `scp` 一个文件到目标机即开箱可用,运行时依赖 = 0(裸环境 `env -i` 可直接启动);
- 六目标交叉编译(darwin/linux/windows × amd64/arm64);
- 便携数据根:`gah-data/` 随二进制同级自动创建,部署目录内 gah+gah-data 即完整,升级只替换单文件。

三端同源:同一 gah 二进制承载 **TUI / Web / headless** 三种形态,Web 前端经 `go:embed` 内嵌(运行时零构建)。

## 二、功能特性

| 能力 | 说明 |
|---|---|
| **微内核** | `core/` 仅含 ctx 服务容器 / 5 分发 EventBus / 插件注册表(拓扑+热重载)/ 配置层(profile→bundle→patch);零业务 |
| **一切皆插件** | 会话日志、LLM 路由、工具流水线、沙箱、审批、Agent 循环、界面,全部是插件;配置层 + 运行期 `/plugins` 随时插拔,卸载即撤销副作用 |
| **双界面** | TUI(bubbletea v2)与 Web(Vue3,默认自动开浏览器)能力对等,同一 `$GAH_HOME`;另有无 UI 的 headless 一次性形态(CI/脚本) |
| **ReAct 循环** | 对齐 dsh 轮次:pre-step → llm/stream → tool/call* → turn/end,AgentLoop 本身可替换;**步数上限可配**(bundle 条目 `data.max_steps`,**缺省不限** —— 上限本是启发式,不该由 harness 替用户定;设 N 即硬上限,耗尽时错误文案直接给出放宽方式) |
| **结构化工具** | MCP 兼容 JSON schema;执行流水线 pre-execute(veto)→ execute → post-execute → result 广播;错误结构化回传模型 |
| **LLM 统一域模型** | 纯 HTTP+SSE 的 OpenAI 兼容适配器(DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp 通吃)+ Anthropic 适配器(`claude-*` 前缀路由)+ mock 适配器(CI 免外网);多 provider 并存(`/provider`) |
| **沙箱三档** | read-only / workspace-write(防 `../` 穿越)/ full-access,TUI `/sandbox` 与 Web 设置面板运行期切换;**写路径统一裁决**:`file_*` 参数与 `shell` 命令的写目标(重定向、写命令、输出旗标 `-o/-O/-C/-t/--target/--prefix`、`git clone` 目标)都必须落在档位允许范围内(`shell` 越界写 / 含变量等不可裁决写目标直接拒绝)——审批通过 ≠ 放开档位,需显式切 full-access;**档位联动可见且可控**:审批档 `open`/`strict` 会覆盖沙箱有效档(`full-access`/`read-only`),`/sandbox`、`/approval`、TUI 状态栏与 Web 状态均回显「声明档 → 有效档(联动来源)」,不再静默失效;要「开着 open 但仍守住沙箱档」就关掉联动 —— **`/sandbox sync off`** 或 Web 设置面板「档位联动」勾选框(两者同一偏好,重启恢复;关掉后沙箱档独立生效、拦截行为跟着变);**内核级沙箱(第 3 组)**:macOS seatbelt / Linux Landlock 在**子进程树**层面兜住「写目标判不出来」的写(解释器内部写、`ccache`/`make` 包装器、`go install`、`curl -O` 等),并把**写目标表**继续补全(第 2 组:`sort -o`、`patch -o/-d`、`cargo --target-dir`、`npm --cache`、`pip --cache-dir/-d`、`gcc -MF/-MJ`、`go test -coverprofile/-trace`、`find -exec/-delete/-fprint`、`mktemp -p`、`split`、`tar czf`、`zip`/`7z`、`cmake --prefix`);**Windows 仍只有协作层**(无等价无特权机制) |
| **审批三档** | 危险命令(rm -rf / git push -f / sudo / chmod 777…)按档:开放 open(放行)/ 智能 smart(弹确认,无确认通道时安全拒绝,默认)/ 严格 strict(拒绝);偏好持久化 |
| **凭据隔离** | 工具子进程 env 滤除 `*_API_KEY/_TOKEN/_SECRET`;危险操作无确认通道时安全拒绝 |
| **会话管理** | 项目级隔离 + 多会话切换 + 分支树(`/fork` `/clone` `/tree` + 命名);超预算 token 滚动摘要压缩(token-compress,完整日志留盘);**模型端报上下文超窗时自动压缩后重试本回合**(溢出兜底:判定分层、只重试一次、提示通道留痕;仍失败则给 `/compact` 与人话出路) |
| **模型工具** | shell / file_read-write-append-edit / web_fetch / web_search / workflow / subagent / todo / memory / auto_plan / session_search / schedule(默认停用) / job_* / 技能读取 / MCP 桥工具(详见「五、模型可用工具」) |
| **后台任务** | host-jobs + workflow `background`:长任务异步提交/取回/终止,不阻塞回合 |
| **并行隔离** | host-worktrees(`ctx.worktrees` + `/worktree`):受管 git worktree 落 `$GAH_HOME/worktrees/`(不污染用户仓库),子代理经 `subagent(isolate="worktree")` 在独立目录与分支内工作 —— 两个并行子代理改同一批文件互不覆盖;**隔离是强制的**:相对写/绝对写都进不了主工作区(沙箱写范围收窄到该 worktree);非 git 仓库显式报错不降级;worktree 默认保留,回收走 `/worktree rm`(分支保留) |
| **定时任务** | host-schedule(`ctx.schedule`):5 字段 cron 计划(分 时 日 月 周)落 `$GAH_HOME/schedules/*.yaml`,到点**经既有回合入口**(agentLoop→tools,仍受审批/沙箱裁决、仍落会话记录)自动跑一轮;设置面板「计划」段管理(中文「下次运行时间」回显,不自研 cron 构造器);**无人值守 = 没有确认通道 → 需审批的动作一律拒绝(含 open 档)** |
| **提示通道** | host-notices(`ctx.notices`,NOND-N1):插件/宿主向**人**发提示(作业终态/计划失败或跳过/回合出错),**不进会话记录、不计 token**;进程内环形缓冲(200)+ `id` 增量回填(`GET /api/notices?since=` / SSE `notice` 帧),Web 右下 toast、TUI 状态栏项与 `/notice`;同 `Key` 60s 去重防刷屏;系统级通知已交付(NOND-N2):TUI 按终端能力逐级降级发 OSC 99/777/9 或响铃(`GAH_TUI_NOTIFY=auto\|osc\|bell\|off`,`/notify test` 自测,写 `/dev/tty`),桌面壳改为**单条 2s 轮询消费提示流**(`warn`/`error` → 桌面通知),不再按场景各写一个轮询器;**macOS 上未签名/未公证的构建系统不呈现横幅**(系统收下不弹,属预期非缺陷:壳额外用 Dock 弹跳兜底,并在壳日志如实记录投递结果);应用内提示通道与状态栏不受影响 |
| **子代理 fanout** | `agent/parallel/pipeline` 独立上下文 ReAct 扇出并行聚合;`send_message`/`fork` 注入与会话派生 |
| **starlark workflow** | 模型写受限 starlark 脚本组合多步工具调用(天然沙箱/无标准库),`background` 异步 |
| **联网搜索** | web_search(默认 Exa,`EXA_API_KEY`;`data.provider` 可换)与 web_fetch 协作,错误结构化归一 |
| **MCP 双向** | client 桥(接外部 server,工具 `mcp_<server>_<name>`)与 server 端(对外暴露本仓全部工具,可被 Claude Desktop 等拉起) |
| **MCP 按需检索** | 每个 MCP server 可选 `mode`: `direct`(默认,工具全量进上下文)/ `search`(工具**不进每轮上下文**,只暴露 `mcp_search` 查清单 + `mcp_call` 按名调用);配置落 `$GAH_HOME/config/mcp.yaml`(设置面板「MCP server」段可视化增删改,**保存即写盘并热重载**,无需重启),env 照旧生效(文件优先) |
| **ACP agent 端** | `gah acp`:以 **ACP v1**(Agent Client Protocol,Linux Foundation)被编辑器(Zed / Neovim 等)当一等 agent 拉起 —— 会话、流式回复、工具进度、审批弹层全走 ACP,数据与 TUI/Web **同源**(同一会话账本、同一沙箱/审批裁决,不是另一套运行时);编辑器的权限按钮只映射 `allow_once`/`reject_once`(不给 always:一次点击不该永久放宽危险操作),交互式提问与图片输入暂不支持(显式报错,去 TUI/Web 作答) |
| **外部插件桥** | host-bridge:独立进程插件(go-plugin),崩溃隔离(外部进程被杀宿主存活);宿主回调通道(GAH_CB_ADDR)供外部进程请求 tools/jobs/fanout 服务;工具类 100% 外部化(extplugins/) |
| **插件安装** | `gah -install <repo>[@version]`(外部/桥插件)与 `-install-ui <repo|目录>`(UI 槽位插件)一条命令装完即启用 |
| **指令文件与技能** | 全局/项目 AGENTS.md 自动注入(近者覆盖;`/reload` 热更);SKILL.md 技能扫描 + 模型按需加载(`list_skills`/`read_skill`);仓库自注册 `gah-plugin-dev` 技能 |
| **主题外部化** | `$GAH_HOME/config/themes/*.yaml` + `/theme` 运行期切换,零重编译换肤 |
| **整体备份/恢复** | `/backup` 一键打包 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好)→ 确定性 tar.gz;`list|restore`,恢复前自动先备份当前态 |
| **配置自愈** | 启动失败自动回滚最近正常备份重试一次,坏配置不卡死 |
| **pty 交互** | tool-shell `data.pty` 开关:驱动 REPL / git 编辑器等交互进程 |
| **Web 附件+多模态** | 输入框传图片/文件(按钮+拖放+粘贴),芯片预览/删除;图片经 openai/anthropic 适配器结构化注入(模型看图),文本附件路径引用;落盘 `$GAH_HOME/attachments/` |
| **文档预览** | 一个块模型 + 四端同源渲染(markdown/文本/代码/CSV/notebook + PDF 页事实):Web 预览工作台(文件树/PDF 原生查看器/HTML 源码视图+沙箱)、TUI `/preview` pager(滚动/搜索/横移)、`gah doc` CLI、会话流 markdown 渲染;路径经沙箱+逃逸校验+密钥 deny-list,零 v-html;旧二进制 Office(`.doc/.xls/.ppt`)走**可选**外部转换(`data.external_converters` / `gah doc --convert`,需本机 LibreOffice,缺省关,未装时显式提示不静默) |
| **优雅停机** | `POST /api/shutdown` → DisposeAll 全回收(Windows 无 SIGTERM 的统一停机通道;桌面壳/运维复用) |
| **桌面壳(P1)** | `desktop/` Tauri v2 壳:sidecar gah + 窗口直连本地服务;托盘(关于/检查更新/开机自启)/ 通知 / 单实例 / 系统文件夹选择器(与设置面板状态同源:面板开着时托盘操作 1.5 秒内同步);**诊断**:壳日志 `<用户数据目录>/gah-shell.log` 同时收录壳侧事件、sidecar stderr(滤掉 go-plugin 的 `[DEBUG]`)、sidecar 文本 stdout、页面侧错误(前端经 `shell_log` 上报)、**启动自检的通道矩阵**(同步/异步/选择器/自启四条通道各探一次)与**任何线程的 panic**(`panic @ 文件:行`);**零成本发行**(updater ed25519 自持签名 + CI 矩阵 + 无签名首次启动指引);**侧栏会话导出走壳命令落盘**(HTML/jsonl 写入下载目录,网页自动打开,侧栏回执路径) |

## 三、快速开始

### 下载安装(非编程用户推荐)

从 [Releases](https://github.com/nekoleamo/go-agent-harness/releases/latest) 下载对应文件:

| 系统 | 下载文件 | 说明 |
|---|---|---|
| macOS(Apple 芯片) | `gah_<版本>_aarch64.dmg` | 打开后把 gah 拖进「应用程序」 |
| Windows(64 位) | `gah_<版本>_x64-setup.exe` | 双击安装(仅当前用户,不要求管理员) |
| 命令行版(任意平台) | `go-agent-harness_<版本>_<系统>_<架构>.tar.gz` | 解压即用的单文件二进制(另附 `.zip` 与 `checksums.txt`) |

> **首次打开会被系统拦一下**(本应用未购买 Apple 开发者证书 / 微软代码签名证书 —— 程序本身完好,
> 只是系统第一次不认识它):
>
> **macOS** 有两种提示,处理方式不同:
> - 「**无法验证开发者**」→ 在「应用程序」里**右键 gah → 打开 → 打开**(只需一次)。
> - 「**已损坏,无法打开,您应该将它移到废纸篓**」→ **文件没坏**。这是 macOS 对「未公证 + 从网络
>   下载」的固定措辞(应用只有本地临时签名),**右键打开对它无效**,终端执行一次即可(拖进
>   「应用程序」后跑;之后升级若再碰到,重跑同一行):
>   ```bash
>   xattr -dr com.apple.quarantine /Applications/gah.app
>   ```
> - **Windows** 出现蓝色 SmartScreen 警告 → 点「**更多信息** → **仍要运行**」(只需一次)

装好双击即用(内置对话界面 + 设置面板)。首次使用只需在设置面板里填一个模型服务商
(DeepSeek / Kimi / 智谱 / 通义 / Ollama 等有一键预设,粘贴 API Key 即可)。

> **数据在哪 / 升级与卸载**:数据(会话、记忆、计划、密钥)都在 `gah-data/`,与运行文件同级。
> 桌面版首启会把运行文件复制到**你的用户数据目录**再启动(Windows `%LOCALAPPDATA%\dev.gah.desktop\bin\`,
> macOS `~/Library/Application Support/dev.gah.desktop/bin/`),数据因此落在**应用目录之外** ——
> **升级(整包替换应用)不会动数据,卸载也不会删数据**。升级前桌面壳仍会额外备份一份到
> `~/gah-upgrade-backup/`(备份失败会取消升级,不拿数据冒险);想手动备份/恢复就用
> `/backup <应用目录之外的路径>` 与 `/backup restore <名字>`。
> 命令行版:数据与 `gah` 同目录,升级只替换单文件,`gah-data/` 原地保留。
> **两点例外(唯一会丢配置的情况)**:① Windows 卸载页有个「Delete app data」勾选框,勾了会连
> `%LOCALAPPDATA%\dev.gah.desktop`(数据根所在)一起删 —— 默认**不勾**,想留着就别勾;
> ② Linux 只有命令行包(解压即用),数据在 `gah` 同目录 —— **把新版本解压覆盖到同一目录**就保留,
> 整个目录换掉就丢了。详细矩阵见 `DESIGN.md` R25。

### 构建(发布形态)

```bash
# 直接构建(单文件,dev 版本号)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o gah ./cmd/gah
# 或统一入口(自动保障前端内嵌;v0.x.y 写 VERSION=v0.x.y)
./scripts/gen.sh cli
```

### 全局安装(一次部署,任意目录启动,pi 式)

```bash
# macOS / Linux:构建 → ~/.local/gah + 符号链接 ~/.local/bin/gah(缺 PATH 时自动提示)
bash scripts/install.sh
# Windows(PowerShell):构建 → %LOCALAPPDATA%\gah + 加入用户 PATH(新开终端生效)
powershell -ExecutionPolicy Bypass -File scripts\install.ps1
```

安装后任意目录直接: `gah` / `gah web` / `gah --profile headless -input "…"`。
数据根 = 安装目录下 `gah-data/`(首次运行自动创建;符号链接启动亦解析到真实安装目录);会话/记忆/todo 按项目 cwd 自动隔离。升级 = 重跑脚本;卸载 = `--uninstall` / `-Uninstall`。

> **Windows 前置**:`shell` 工具与后台任务经 POSIX shell 执行(`sh -c`),故 Windows 需安装
> [Git for Windows](https://git-scm.com/download/win)(自带 `bash.exe`,自动探测
> `%ProgramFiles%\Git\bin\bash.exe`、`%ProgramFiles(x86)%\Git\bin\bash.exe`、
> `%LOCALAPPDATA%\Programs\Git\bin\bash.exe` 与 PATH 上的 `bash.exe`),
> 或用 `GAH_SHELL_PATH` 显式指定。**不提供 cmd.exe/PowerShell 回退** —— 命令的写目标裁决按
> POSIX 词法进行,换 shell 会让沙箱判定与实际执行脱节;缺失时工具**显式报错**(不静默降级)。

**两种模式并存(同一二进制,模式 = 位置)**:① **全局共用**——install 后任意目录敲 `gah`,数据根统一在安装目录 `gah-data/`,会话/记忆按项目 cwd 自动隔离;② **便携单飞**——直接把 `gah` 复制/下载到任意目录即用,该目录自动建独立 `gah-data/`,与全局数据完全隔离(适合临时环境/隔离试验/分发)。两者互不影响,无需切换。

### 三种运行形态

```bash
./gah                                          # TUI(默认 tui profile;非 TTY 自动降级文本)
./gah web                                      # Web UI(≡ --profile web),http://127.0.0.1:2233,自动开浏览器
./gah acp                                      # ACP agent 形态(被编辑器如 Zed 经 stdio 拉起,≡ --profile acp)
./gah --profile headless --input "帮我执行 echo hi"   # headless 一轮(CI/脚本/dev 用 mock 模型免 key)
./gah --profile mcp-serve                      # MCP server 形态(被外部 MCP client 经 stdio 拉起)
./gah --profile dev --dump-config              # 查看合并后的配置树(任一条目可被自己 patch 替换)
./gah --ephemeral --profile headless --input "hi"  # 临时数据根,退出即焚(隔离测试)
./gah --version                                # 版本
```

> `--ephemeral` 将全部运行数据(含外部插件目录)放入临时 home,退出即焚,适合 CI 隔离冒烟。

### 使用真实大模型

```bash
export DEEPSEEK_API_KEY=sk-...            # 或 OPENAI_API_KEY / ANTHROPIC_API_KEY(供 claude-* 模型)
./gah --profile headless --input "你好"
```

生产自建 profile:复制 `config/profile-headless.yaml` 为自定义配置,patch 中关闭 `llm-mock`、启用 `llm-openai-compat`(可配 `data.base_url/model`)。密钥也可经 `/provider` 写入 `provider.yaml`(见下),不依赖 shell env。

### 快速新增任意提供商(TUI 内,零改配置零重启)

任意 OpenAI 兼容端点(DeepSeek/SiliconFlow/Ollama/vLLM/Kimi…)只需一行:

```
/provider set <baseUrl> <apiKey> [model]     # 立即生效并持久化(provider.yaml, 0600)
/provider show                               # 查看当前端点/模型/凭据(打码)
/provider unset base_url|api_key|model       # 逐项删除(该项回退 env/样板,其余保留)
/provider remove <名>                        # 删除单条(删活跃自动顺延到剩余首个,删空回退 env/样板)
/provider clear                              # 全部清除+运行时立即复位
```

示例(SiliconFlow):

```
/provider set https://api.siliconflow.cn/v1 sk-<你的key> deepseek-ai/DeepSeek-V3
```

之后正常对话即可;换回环境变量配置用 `/provider clear`。

### 常用 CLI 参数

| 参数 | 作用 |
|---|---|
| `-profile <名>` | profile(tui/headless/dev/web/acp/mcp-serve/自定义) |
| `-input <文本>` | headless 一次输入,跑一轮输出回复并退出 |
| `-dump-config` | 输出合并后的配置树并退出 |
| `-ephemeral` | 临时数据根,退出即焚 |
| `-version` | 输出版本信息 |
| `-install <repo>[@version]` | 安装线上/桥插件(`mcp:<id>:<command>` 登记 MCP 插件) |
| `-uninstall <id>` / `-list-plugins` | 卸载 / 列出外部插件 |
| `-install-ui <repo\|本地目录>` / `-uninstall-ui <id>` / `-list-ui-plugins` | UI 插件安装 / 卸载 / 列出 |

文档阅读子命令(web/im 同级入口,零装配纯读):

```bash
gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--max-input-bytes B] [--tree] [--depth N]
# 退出码:0 成功 / 2 用法 / 3 不支持格式 / 4 超预算 / 5 解析失败
# --convert(可选):本机装有 LibreOffice 时,把旧二进制 Office(.doc/.xls/.ppt)转 PDF 再抽取;
# 产物落 $GAH_HOME/cache/doc/(7 天保留),转换失败显式回退为「不支持」提示
```

## 四、TUI 命令

> 命令注册进宿主 `ctx.commands`,TUI/Web/headless `/` 前缀通用;输入 `/` 弹出命令提示(名称+说明,可继续输入过滤)。**所有命令支持逐级确认**:参数按声明级联(子命令枚举 → 动态候选,如 provider/会话/插件/任务/备份/主题/群列表;需手输的走自由参数断点),TUI 用选择器、Web 用同一注册表声明的候选列表(点击逐级选)。TUI 专属命令(search/widgets/statusline/traj/theme/help/exit/fork/clone/tree/name)仅 TUI 可用。

| 命令 | 作用 |
|---|---|
| `/model <名>` | 切换模型(**动态枚举当前端点全部模型**,来源括号备注如 `(siliconflow)`;选中自动切所属 provider;列表失败/无 key 回退手动输入) |
| `/thinking off\|low\|medium\|high` | 思考等级(推理预算);**快捷键 Shift+Tab 循环前进**;状态栏显示 `思维: <等级>`(off 隐藏) |
| `/sandbox ro\|ws\|full` | 运行期切沙箱档(只读/工作区写入/完全访问;状态栏实时显示,偏好持久化重启恢复) |
| `/sandbox sync [on\|off]` | 审批档→沙箱有效档 的**联动开关**(无参=回显开关与当前有效档):`off` 后沙箱档独立生效,不再被 `open`/`strict` 覆盖(例:审批 `open` 想少弹确认、又不想放开越界写);用户选择持久化(`gah-state.json`),重启与无人值守场景都按它恢复;沙箱实现没有该能力时显式报不支持 |
| `/approval open\|smart\|strict` | 运行期切审批档(开放=危险命令直接放行 / 智能=命中弹确认(默认)/ 严格=直接拒绝;偏好持久化重启恢复) |
| `/provider show\|add\|use\|set\|unset\|remove\|clear` | 配置 LLM 提供商(多 provider 并存):`show` 列出(活跃★凭据打码)/ `add 端点 key [model]` 新增(首个自动活跃)/ `use <名>` 切换 / `set 端点 key [model]` 编辑活跃 / `unset 字段` 逐项删(回退 env/样板)/ `remove <名>` 删除单条(删活跃顺延到下一条)/ `clear` 全清并复位 |
| `/plugins list\|on\|off <id>` | 运行期插拔插件(`on/off` 持久化开关,重启仍生效;`default` 恢复配置树默认) |
| `/settings history N\|off\|unlimited` | 会话历史注入条数(`off` 禁止 / `unlimited` 全部 / N 最近 N 条;全局偏好跨会话) |
| `/compact [指示词]` | 手动滚动摘要压缩(立即折叠旧历史;自动超预算压缩不变;指示词仅记录);端点报上下文超窗时会**自动**压缩后重试本回合(溢出兜底,只重试一次),无需手动 |
| `/export [path]` | 导出当前会话事件序列(`.html` 结尾 = 自包含 HTML 渲染,否则 jsonl;**导出成功后默认自动用系统默认程序打开**,`GAH_EXPORT_OPEN=0` 关闭);Web/桌面端等价入口 = 侧栏会话行 `⤓` 菜单(桌面端落盘到下载目录) |
| `/workspace [目录]` | 切换工作区(项目):最近列表选择或输新目录;切换即开新会话、工具进程 cwd 真实切换、沙箱 root 同步 |
| `/session list\|switch\|new\|current` | 会话管理:列出(★ = 置顶,带概述)/ 切换(二级选择器带内容预览与时间)/ 新建(空历史)/ 查看当前 |
| `/session pin\|unpin [id]` | 置顶 / 取消置顶会话(缺 id = 当前;置顶区排在列表最前,上限 8) |
| `/session summary [id]` | 生成/查看会话概述(LLM 总结:一句话 + 主题词;**会调用模型**) |
| `/reload` | 热重载指令文件(AGENTS.md 层级/全局/附加;外部编辑即生效,免重启) |
| `/context [all]` | 上下文占用分解:**真实** token(累计输入/输出/缓存命中/窗口占用条)与**本地估算**分段(固定引导/全局与项目指令/附加指令/各片段/工具名清单 + 工具 schema JSON 开销/会话投影历史)分列,口径显式标注;`all` 再逐项列出每个工具 schema 的字节与粗估 token(**纯本地,不发模型请求**) |
| `/diff [路径]` | 变更审查面:无参 = 本会话改过的文件清单(+/− 行数,按路径聚合);有参 = 该文件本次会话的逐行 diff(TUI 弹 pager / Web 切到变更视图)。数据来自工具写盘时捕获的前后内容,**不依赖 git**(工作区不是仓库、还有未提交改动都不影响口径);超 32 KiB / 二进制 / 超大输入三种情况显式标注降级 |
| `/recap` | 会话速览(轮数/工具 Top 与失败数/涉及文件/最近一问一答/模型/跨度;**纯本地统计,不调模型**) |
| `/answer [编号\|内容\|skip]` | 作答结构化提问(TUI):无参=回到作答态;多问并存按到达顺序逐个答;`skip` 跳过;`Esc` 退出作答态(提问与草稿都保留) |
| `/jobs [list]\|output <id>\|kill <id>` | 后台任务**与子代理**统一视图:无参=list(运行中优先,带耗时/摘要)/ 取输出 / 终止(Kill 需 running;与 workflow `background`、Web 任务面板同源;TUI 状态栏另有常驻折叠行「后台 N 运行中」;TUI 按 **F6** 展开实时坞) |
| `/worktree [list]\|rm <id> [force]` | 受管 worktree(隔离子代理的工作目录):无参=list(带分支/基线/路径 + 回收提示)/ `rm` 回收目录(**分支保留** —— 未合并的改动仍在分支上;有未跟踪改动时非 `force` 拒绝,防静默丢活) |
| `/schedule [list]\|add <cron> <描述>\|rm\|on\|off\|run <id>` | 定时任务:列出 / 新建(5 字段 cron,如 `0 8 * * *` = 每天 8 点)/ 删除 / 启停 / 立即跑一次(与设置面板「计划」段同源) |
| `/backup [dest]\|list\|restore <name>` | 整体备份 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好,排除 backups/ 自身):无参=立即备份(默认存 `$GAH_HOME/backups/`,可指定外部路径)/ `list` 列出(时间倒序)/ `restore <name>` 恢复(**恢复前自动先备份当前态**,重启后完全生效) |
| `/preview <路径>` | 文档预览工作台:TUI 打开全屏 pager(↑↓/PgUp/PgDn 滚动、←→ 横移、`/` 搜索 n/N 跳转、q/Esc 关闭);Web 打开文档面板并定位该文件(markdown/文本/代码/CSV/notebook/docx/xlsx/pptx/PDF) |
| `/search <词>` | 会话内搜索(命中高亮,n/N/F3 循环跳转,Esc 退出) |
| `/theme [名]` | 切换主题(枚举 `$GAH_HOME/config/themes/*.yaml`;`default` 回默认;零重编译换肤,偏好持久化) |
| `/fork [seq]` | 从历史任意点派生分支会话(`/tree` 查看 seq;缺省=最近提问) |
| `/clone` | 复制当前会话(同一分支另一路演进) |
| `/tree` | 会话分支树(会话全貌 + 可 fork 的提问点) |
| `/name <显示名>` | 给当前会话加显示名(`-` 清除;状态栏/切换列表名优先) |
| `/widgets on\|off` | 输入区上方 widget 区开关(宿主注册的动态信息行) |
| `/statusline [项...]\|reset` | 状态栏项集合与顺序(TUI):无参=查看当前与可用项(`state/queue/questions/dock/notice/last/workspace/sandbox/approval/session`);给项即按序渲染(回合态项之间 `·`、其余 `|`),`reset` 恢复 F15.3 基线;偏好持久化(`gah-state.json`)重启生效 |
| `/traj` | 轨迹/可观测视图(TUI):以文本浮层呈现同一份会话事件账本的**回合 → 步 → 工具**投影(概览:回合数/总时长/累计 token 与缓存占比;逐回合倒序:时长与结束原因、步数与工具数、失败/未回填计数、token、模型名;工具行:状态/耗时/出参体积/错误原文/参数摘要)。只呈现过程与成本,不铺出参与正文;**时长只取事件时间戳,进行中的回合/步/工具一律显「进行中」**(概览改显「计算中」),不拿本地时钟补齐;浮层复用文本 pager(滚动/横移/搜索/`q` 关闭),Web 侧对应轨迹视图按钮 |
| `/notice` | 查看最近的提示(TUI,NOND-N1):以浮层列出进程内提示缓冲(最新在前:级别/时刻/来源 + 标题 + 正文),并显示缓冲 gap 与去重计数;未装配提示通道时显式报错 |
| `/notify [test\|auto\|osc\|bell\|off]` | 系统级通知开关与自测(TUI,NOND-N2):无参=回显**当前会怎么发**(落点与原因)与用法;`test` 发一条测试通知;`auto` 按终端能力探测(kitty OSC 99 / WezTerm·VTE OSC 777 / iTerm2 等 OSC 9 / 响铃 / 未附着终端则只留状态栏)、`osc` 强制转义、`bell` 只响铃、`off` 关闭(零输出);`GAH_TUI_NOTIFY` 是持久开关(未设或写错值回落 `auto`)。**只有 `warn`/`error` 会打扰人**,`info` 只更新状态栏 |
| `/help` `/exit` | 帮助 / 退出(**Ctrl+C 连按两次**,防误触) |

> **提示通道(`ctx.notices`,NOND-N1)**:插件与宿主可向**人**发一条提示(后台作业终态、定时任务失败/跳过、回合出错),不依赖盯屏。**提示不是会话内容** —— 不落 `jsonl`、不进模型上下文、不计 token,而是进程内环形缓冲(200 条)+ `id` 增量回填(`GET /api/notices?since=`;SSE `notice` 帧),所以重连/刷新不丢已发提示。三端各自决定呈现强度:Web 右下 toast(`info` 6s 自动消失、`warn`/`error` 常驻待关,超出可见上限显式计数)、TUI 状态栏 `notice` 项(无提示时零占位,逐字符不改变基线)+ `/notice` 浮层看详情;去重限频为同 `Key` 60s 内只出一条,避免重试风暴刷屏。**系统级通知(NOND-N2,已交付)**:Web 之外的两端可在人不在窗口前时把人叫回来 —— TUI 按**终端能力逐级降级**发 OSC 99/777/9 或响铃(探测而非写死支持矩阵:公开矩阵里 Windows Terminal / VTE 的结论互相矛盾,写死等于静默失效),写 `/dev/tty` 而非 stdout(否则重定向/被 hook 捕获时不算终端),未附着终端时静默降级为「仅状态栏」并在 `/notify` 里**如实回显原因**;正文按不可信输入处理(剥控制符,防提前终止转义序列),kitty 分块、tmux/screen 用 DCS 包裹。桌面壳从「每个场景一个轮询器」收敛为**单条 2s 轮询消费提示流**(判据只留在宿主一处,新类别自动进来),仅 `warn`/`error` 弹桌面通知。**真机核对**:本机已覆盖 kitty / Terminal.app / tmux 三种落点,iTerm2·GNOME VTE·WezTerm·Windows Terminal 待对应真机各跑一次 `/notify test`。

> **`!` shell 直通**:输入以 `!` 开头(如 `!git status`)= 立刻执行该 shell 命令(**走与模型工具同一条沙箱/审批管线**,不存在手敲免检旁路),输出就地回显、长命令可 `Esc` 中断;结果**只本地留痕,不进模型上下文**(避免孤立 tool 消息破坏投影)。需要模型看到输出时请让模型调用工具。
>
> **键位速记**:输入中单次 `Ctrl+C` 仅清空输入(不退出);输入为空时需**连按两次** `Ctrl+C`(2 秒窗口内)才彻底退出,第一次按下会高亮提示再按一次,超时或按其他键自动解除;`Esc` 取消进行中的回合;`Shift+Tab` 循环思考等级;`Ctrl+T` 折叠/展开思维块;`Ctrl+O` 折叠最近工具结果;`Ctrl+↑/↓` 跳到最早用户行/回底;`Ctrl+A` 全选(删除=清空/输入=替换)、`Ctrl+B/F` 左右移动、`Ctrl+Y` redo、`Alt+P` yank 粘贴、`Alt+←/→` 按词移动;`F6` 展开/收起后台坞(↑/↓ 选择、`Enter` 看输出、`s` 定向、`x` 停止需二次确认、`Esc` 收起)。

## 五、模型可用工具(由模型调用,无需交互)

| 工具 | 说明 |
|---|---|
| `shell` | 执行 shell 命令(沙箱/审批策略拦截;写目标经路径裁决:workspace-write 下越界写被拒、只读档拒绝一切写;`data.pty` 可驱动交互式进程;凭据 env 滤除;**环境 jail**:`TMPDIR`/`XDG_CACHE_HOME`/`GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` 恒重定向到 `$GAH_HOME/jail/**`,`HOME`/`GOPATH` 等不动,`GAH_SHELL_JAIL=0` 可关;**内核级沙箱**(第 3 组):宿主把**有效**档位下发到执行入口,`shell` 在**进程树**层面限制文件写 —— macOS `/usr/bin/sandbox-exec`(seatbelt)、Linux **Landlock**(内核 ≥5.13,自举 helper 重新 exec 后施加);白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**` + 必要设备节点(路径先解析软链),read-only 档仍保留 jail 可写;无能力平台(Windows 等)→ 一次性 stderr 告警 + 不施加包装,`GAH_SHELL_KERNEL_SANDBOX=0` 可关;**POSIX shell 解析**(`sdk/shellpath.go`):`GAH_SHELL_PATH` > Windows 上的 Git for Windows 常见安装位/PATH → `sh`,后台任务同源,缺失时显式报错不静默降级;Windows 下关闭 MSYS 参数路径转换(`MSYS_NO_PATHCONV`),防裁决路径与实际落点脱节) |
| `file_read` / `file_write` / `file_append` / `file_edit` | 文件读写/追加/精确编辑(经沙箱路径校验) |
| `web_fetch` / `web_search` | 抓取 URL 正文 / 联网搜索(默认 Exa,`EXA_API_KEY`;`data.provider` 可换;401/429/5xx 结构化错误) |
| `workflow` / `workflow_collect` | 受限 starlark 脚本组合多步工具调用(天然沙箱);`background` 异步 + 收集 |
| `job_list` / `job_output` / `job_kill` | 后台任务查询/取输出/终止(与 `/jobs` 同源) |
| `memory` | 跨会话记忆(remember/list/recall/forget;`$GAH_HOME/memory/<project>.jsonl`,人工可编辑) |
| `todo` | 任务清单(create/start/complete/pend/delete/update/list;4 状态机 + blockedBy 依赖;`$GAH_HOME/todos/`) |
| `auto_plan` | 规划模式(create/get/list/step/confirm/complete;检测规划意图先输出结构化规划,确认前零副作用工具调用;`$GAH_HOME/plans/`) |
| `session_search` | 跨会话检索(查历史会话账本:`{query, limit, scope?}` → 命中片段 + 会话 id/时间;默认只搜**当前工作区**,`scope="all"` 才跨项目;纯只读、无索引文件、命中是**历史记录**非当前事实) |
| `schedule` | 定时计划管理(list/add/update/remove/run;经 `ctx.schedule` 委托 host-schedule,与 `/schedule` 同源)。触发时**无人值守 = 没有确认通道**,故计划里需审批的动作会被直接拒绝;`update` 为部分更新(只改给到的字段)。**该工具默认停用**(`bundle-base.yaml` 里 `tool-schedule` 条目 `enabled: false`),要用需先在配置层打开 |
| `subagent` | 子代理委派(delegate/spawn/agents/agent_status/agent_kill/send_message/fork;独立上下文 ReAct,后台带句柄);`isolate="worktree"`(可配 delegate/spawn/fork)= **隔离运行**:子代理在受管 git worktree 内工作,改动只落该目录、不进主工作区,回包含 worktree 路径/分支(父级据此合并或回收);非 git 仓库/未启用 host-worktrees 显式报错(不静默退化为非隔离) |
| `list_skills` / `read_skill` | 技能索引 / 按需加载 SKILL.md(项目 `.gah/skills/`、`$GAH_HOME/skills/`) |
| `mcp_<server>_<工具>` | MCP 桥工具(`mode: direct`,见「MCP 接入」) |
| `mcp_search` / `mcp_call` | MCP 检索模式代理工具(`mode: search`):按关键词查工具清单(空查询 = 全量),再按名调用 |

## 六、Web 使用(设置面板/REST)

`gah web` 起 http://127.0.0.1:2233(自动开浏览器;`GAH_WEB_OPEN=0` 关闭)。功能与 TUI 对等,另有可视化面板:

**鉴权(token 模式 = `data.auth_token` 非空)**:全表面鉴权——接口、静态资源、`/attachments/`(附件)、`/ui-plugins/` 一律需凭据(此前只有 `/api/*` 受保护)。浏览器请用启动日志里的 `#token=<token>` 地址打开:凭据在 **URL fragment** 中(不发往服务端,不进访问日志/Referer),引导页经 `POST /api/auth` 换取 HttpOnly + SameSite=Strict 的 `gah_token` cookie 后转入 UI;桌面壳经 `GAH_WEB_TOKEN` 传同一 token(`?token=` 已废弃)。

- **设置面板**(状态栏 ⚙):模型下拉(聚合全部 provider)、思考/沙箱/**审批**分段控件、历史注入下拉 + 压缩按钮、Provider 管理(启用/删除/新增;**首启引导**:一个 provider 都没有时自动打开面板并给 DeepSeek/Kimi/智谱/千问/硅基流动/OpenRouter/OpenAI/Ollama **一键预设**,只需粘 api_key,保存后自动**连通性自检**并把 401/404/DNS 等端点错误翻译成人话与原始报错并列)、**定时计划**(「计划」段:5 字段 cron + 中文「下次运行时间」回显,运行/停用/删除;无人值守语义在段内明示)、**MCP server**(「MCP server」段:逐项名称/启动命令/启停/模式(全量注册或按需检索)、环境变量来源只读对照、逐行「已加载 N 个工具」状态、**保存并重载**即时生效)、插件开关、指令重载、**数据备份**(立即备份 / 恢复备份——**二次确认**);全部设置退出即记(偏好持久化,gah-state.json 与 TUI 共享)。
- **侧栏**:工作区固定区(切换 = 真实切目录) + 历史会话(名称/**概述或内容预览**/时间,★ 置顶、⟳ 生成概述(调用模型,二次确认)、✎ 改名、× 删除——改删需二次确认)、附件上传(按钮/拖放/粘贴,图片缩略图 + 模型看图)、会话导出(⤓ 展开菜单:自包含网页 / 原始 jsonl;浏览器端直接下载,桌面端落盘到下载目录、网页自动打开)。当前项为卡片式选中(左侧竖条 + 描边 + 名称加粗),与 hover 明确分档。
- **状态栏**(只放别处没有的只读事实):就绪/运行中、**沙箱生效档**、未命名会话标识、连接状态(绿/橙/红三态)、上下文·缓存使用率、版本号(**点开「关于 gah」**= 升级入口)。模型/思考/审批各有唯一交互位(输入框工具条、设置面板),不在底栏重复显示;后台任务钮在右上角(运行徽标 + 列表/输出/终止)。
- **文档预览面板**(侧栏「文档预览」):左侧工作区文件树(过滤/懒展开/工作区切换整树重置)+ 右侧预览(markdown 块渲染、代码/表格、docx/xlsx/pptx 块模型、PDF 浏览器原生查看器、图片、HTML **默认源码视图 + 点击才加载沙箱 iframe**;截断与警告黄色提示条);工具结果行含可预览路径时出现「预览」按钮;会话流 assistant 文本走 markdown 块渲染(服务端解析,前端零 v-html)。
- **结构化提问**:模型 `ask_user_question` 弹层展示问题与选项;**可「稍后作答」收起为输入区上方角标**(不阻塞继续对话,点角标回到作答);多问并存按到达顺序逐个答。
- **轨迹视图**(状态栏右上「轨迹」切换,记忆偏好):把同一份会话事件按**回合 → 步骤 → 工具**聚合,专看过程与成本 —— 粘顶固定概览(回合数/总时长/累计 token 与缓存占比 + 可点击回跳的回合胶囊)、回合状态徽标(完成/已取消/步数上限/进行中)、每步工具行(名称/参数/耗时/**出参字节**/成功失败,失败展开错误原文)与回合级 token 分解;**时长只取事件时间戳,进行中的回合/步骤/工具一律显「进行中」**(不编造时长);窗口不完整(长会话更早历史未加载/已折叠)时概览改标「窗口内 token」并说明仅覆盖当前窗口。
- **变更视图**(状态栏右上循环切换:会话流 → 轨迹 → 变更 → 看板,记忆偏好):只看**本次会话经工具改过的文件** —— 粘顶概览(文件数/+行/−行/改动次数 + 可点击回跳的文件胶囊)、逐文件折叠块(新建/二进制/已截断 徽标、操作与 ± 计数)、展开后逐行着色 patch(每段带 `#序号 操作 时间` 头,便于对着会话流定位)与降级说明。数据来自写盘时捕获的前后内容,口径明确**不依赖 git**,不是「工作区当前 vs HEAD」;`/diff <路径>` 可从命令直接定位到某个文件。
- **看板视图**(第四种投影,状态栏右上循环切换):把已在手的数据聚合成一屏信息面,**五张卡片** —— 用量(累计输入/输出/缓存命中率/请求数/上下文占用)、回合(已完成回合数、总时长、工具调用与失败数、平均 token)、后台任务(运行中/记录数/最近一条)、文件变更(文件数/±行/改动次数)、定时计划(启用数/下次运行/最近终态)。卡片可**隐藏 / 上下移动顺序**、可「恢复默认」,布局记忆在浏览器(新增卡片自动补到尾部);卡片动作直达对应视图或抽屉(轨迹 / 变更 / 任务面板 / 设置的计划段)。**口径写在卡上**:用量是会话累计、回合数含进行中、变更来自工具写盘旁路(不依赖 git);数据全部来自本机事件账本与既有接口,不引入可执行内容、无新增后端契约。
- **侧栏停靠区**(状态栏「侧栏」展开,记忆偏好):把变更 / 看板 / 任务三个面板**停靠在对话流右侧并排显示** —— 不用在「切走会话流看变更」和「让任务面板盖住对话」之间二选一;面板在停靠区顶部标签间切换,宽度可**拖拽**(也可聚焦分隔线用 `←/→`,双击复位),布局落浏览器存储。**宽度给对话流让位**:视口不足时停靠区先让步(下限 280 / 上限 720 / 至少给对话留 520);窄屏(<900px)自动退化为覆盖式抽屉。零后端契约:面板内容复用既有视图组件,数据管道不变。**外壳永不整页滚动**(每个滚动区各有归属:会话流 / 侧栏列表 / 抽屉),这条纪律有真浏览器布局回归门禁守着(见「十」)。
- **长会话窗口**(首帧基线 + 上滚分页):打开一个很长的会话不再重放全部历史 —— 首连只回放**尾部窗口**(约 400 条事件,回合对齐),并先发一帧 **基线** 告诉前端窗口边界与「更早历史是否还有」;向上滚到接近顶部(或点顶部按钮)即自动按游标拉更早一页拼在前面(按序号去重、拼接后**滚动位置不动**)。贴底阅读时自动折叠最老的消息(上限 800 条,`已折叠 N 条更早消息`),被折叠的内容上滚可重新取回 —— DOM 与内存不随会话长度增长;轨迹/变更视图在窗口不完整时显式标注口径,不把窗口内合计说成会话全程累计。
- **WebSocket 通道**:`/api/events/ws`(与 SSE 同 payload,前端自动降级;两条路断线续传都带 `after` 游标)。
- **断连行为**(显式三态:已连接/重连中/**已断开**):连接丢失时输入区上方出现红色横幅与「重试连接」,**提交与审批/作答一律拦截且草稿、附件、弹层原样保留**(未送达不许挥掉本地状态,恢复后手动重发,不做队列自动重放);恢复时以服务端快照接管状态并丢弃陈旧完成帧。断连判定只依据可观测事实(浏览器 `navigator.onLine`、EventSource 已放弃、探活失败),不用「多久没收到帧」猜测;切回前台/睡眠唤醒(时钟跳变 >20s)会主动重握一次。
- **通用 REST 能力面**(前端/脚本均可直接调用,未装配服务 503/501 显式):

| 方法 路径 | 说明 |
|---|---|
| `POST /api/auth` | 引导通道:`{"token":"…"}`(或 `Authorization: Bearer`)换 `gah_token` cookie(POST-only,错误 token 401);唯一豁免鉴权门的路径,仍受 Host 白名单 + 同源校验 |
| `GET /api/state` | 状态快照(model/thinking/sandbox/sandbox_effective/sandbox_sync/approval/stats/session/running/version) |
| `POST /api/input` | 提交回合;`/` 前缀走命令;running 时 409 |
| `POST /api/confirm` | 审批应答 `{id, ok}` |
| `GET /api/events` + `GET /api/events/ws` | 事件流(SSE 断线重放 / WS;首连发 `baseline` 基线 + 尾部窗口,续传按 `after` 补差集) |
| `GET /api/session/events` | 会话事件分页(`?before=<seq>&limit=<n>`):长会话上滚加载更早历史,窗口回合对齐、返回 `has_more` |
| `GET /api/sessions`、`POST /api/sessions` | 会话列表 / `{action: switch\|new\|fork\|clone\|delete}` |
| `POST /api/sessions/rename`、`GET /api/sessions/{id}/export` | 会话改名 / 导出会话(`?format=html` = 自包含网页,缺省 jsonl;无 id 路径 = 主会话) |
| `GET /api/workspaces`、`DELETE /api/workspaces/{key}` | 工作区历史 / 删除记录(不动文件夹) |
| `GET /api/commands`、`POST /api/commands/{name}` | 命令注册表 / 直接执行 `{args}` |
| `GET /api/tools`、`POST /api/tools/{name}` | 工具清单 / 调用(参数 JSON 透传) |
| `GET /api/jobs`、`GET/POST /api/jobs/{id}[/kill]` | 后台任务 |
| `GET/POST /api/schedules`、`PATCH/DELETE /api/schedules/{id}`、`POST /api/schedules/{id}/run` | 定时计划:列表/新增/改(仅覆盖传入字段)/删/立即触发 |
| `GET/POST /api/mcp` | MCP server 配置:GET 返回配置 + 逐项运行期状态(是否已加载/工具数);POST 提交整表 = 写 `config/mcp.yaml` 并重启插件(`reload:false` 只写盘;写盘成功但重载失败 → 200 + `reload_err`) |
| `GET /api/plugins`、`POST /api/plugins/{id}/load\|unload` | 插件启停 |
| `GET /api/models?all=1`、`GET/POST/DELETE /api/providers...` | 模型聚合 / provider 增删改(删除同步运行期与持久化) |
| `POST /api/control` | 状态栏级控制 `{model?\|thinking?\|sandbox?\|approval?\|workspace?}`(偏好持久化) |
| `POST /api/compact`、`POST /api/settings/history` | 手动压缩 / 历史注入条数 |
| `GET /api/todo` | 任务面板数据(todo 工具 list 透传) |
| `GET /api/backup`、`POST /api/backup` | 备份列表 / `{action: backup\|restore, dest?, name?}` |
| `POST /api/sessions`(`action: pin\|unpin`)、`POST /api/sessions/summary` | 会话置顶/取消置顶 · 生成/取回概述(会调用模型;列表请求只读缓存) |
| `POST /api/attachments` + `GET /attachments/...` | 附件上传(20MB/类型白名单)/ 静态预览 |
| `POST /api/reload` | 指令文件热更 |
| `POST /api/shutdown` | 优雅停机(→ system/shutdown → DisposeAll 全回收;桌面壳/运维复用) |
| `GET /api/ui-plugins` + `/ui-plugins/` | UI 插件聚合视图 / 静态托管;聚合项带 `trusted`/`trust_note`(UI 插件与主应用**同源同权限**:可调用全部 API(含 `/api/input` → 工具执行),只安装你信任的插件;SPA 带严格 CSP 切断纯外发通道) |
| `GET /api/doc/preview` `raw` `asset` `tree` `html`、`POST /api/doc/render` | 文档预览(块模型 JSON)/ 原生字节(Range,`dl=1` 下载)/ 内嵌资产(MIME 白名单)/ 文件树 / HTML 沙箱(CSP)/ markdown 文本→块模型 |

## 七、配置与运行时目录

### 配置层(profile→bundle→patch)

```
profile-<name>.yaml     # bundles + patches 按序声明
bundle-base.yaml        # 插件条目(id + enabled + data 参数)
patch-*.yaml            # 按 id 替换/插入/启停条目(随时插拔)
```

样板源在 `config/`(与 `internal/embed/seed` 两份同步,`# seed-version: N` 头,新增 base 条目必须 bump)。`--dump-config` 打印的任一条目都可被自己的 patch 替换。

### 运行时数据根(GAH_HOME,完全便携)

数据根 = **gah 二进制同级的 `gah-data/`(唯一;不存在则首次启动自动新建并释放初始化内容——config 样板与随包插件,部署目录即自包含)**;创建失败(目录只读/不可写)= 启动**显式报错退出**。`GAH_HOME env` 与 `~/.gah` **均不作为数据根输入源**(2026-09-16 收紧;boot 后经 GAH_HOME 贯通内部,见 cmd/gah homeDir)。

```bash
# 1) 把 gah 放到任一目录,首次运行自动建 gah-data/ 并释放 config 样板 + 插件:
./gah --profile web
#    已有数据(如从旧 ~/.gah):mkdir gah-data && cp -a ~/.gah/. gah-data/  # 密钥随 config 进入
# 2) 后续升级只替换 gah 这一个文件,数据不动;只读目录无法自建 → 设 GAH_HOME 或移到可写目录
```

- 多份 gah 副本可共享同一 gah-data(数据仍单根,不落系统根/不散 cwd);显式覆盖用 `GAH_HOME=/path ./gah ...`。
- API key 等密钥统一在 `gah-data/config/`(provider.yaml / search.yaml),随目录整体备份/迁移。
- **整体备份**:`/backup` 即打包整个 gah-data(排除 backups/ 自身)→ 默认落 `gah-data/backups/`(随目录迁移)或指定外部路径;`/backup restore <name>` 恢复(先自动备份当前态)。

### 环境变量

| 变量 | 作用 |
|---|---|
| `GAH_HOME` | 内部贯通变量(boot 自动设为便携根 `gah-data/`,插件/外部进程经它派生子目录);**用户显式设置被忽略**(数据根唯一 = 二进制同级 `gah-data/`,2026-09-16 起,设不同值启动时告警) |
| `GAH_PROFILE` / `GAH_NO_TUI` | 默认 profile / 强制关闭 TUI(headless/CI) |
| `GAH_WEB_ADDR` / `GAH_WEB_OPEN` / `GAH_WEB_STATIC` | Web 监听地址(默认 127.0.0.1:2233)/ 是否自动开浏览器 / 静态目录覆写(开发态 HMR);桌面壳另用 `GAH_WEB_TOKEN` 传 token 并导航到 `#token=` 地址(就绪探测把 401 也视为已就绪),并**自己挑一个空闲端口**以 `GAH_WEB_ADDR` 覆写监听地址(固定端口会连上别的实例:孤儿 sidecar 或用户自己的 `gah web`),同时置 `GAH_WEB_PARENT_WATCH=1`:父进程一死 sidecar 即自行优雅退出(不留占着数据根的孤儿) |
| `GAH_EXPORT_OPEN` | `/export <路径>.html` 导出后是否自动用系统默认程序打开(默认**开**;`0`/`false` 关;等价于 config 里 `host-internal-commands` 条目的 `data.export_open_browser`) |
| `GAH_MCP_COMMAND` / `GAH_MCP_COMMANDS` | MCP 桥接入(单 server / 多 server 每行 `name=command args`);**`$GAH_HOME/config/mcp.yaml` 优先**,同名以文件为准(env 独有条目在设置面板标「环境变量」只读) |
| `GAH_CB_ADDR` / `GAH_CB_TOKEN` | host-bridge 回调通道(外部进程插件请求宿主 tools/jobs/fanout 服务;含鉴权 token;**不外泄**:`SanitizedEnv` 拦在下游) |
| `GAH_SHELL_KERNEL_SANDBOX` | `0` = 关闭 shell 的**内核级沙箱**(默认开启:macOS `sandbox-exec` seatbelt / Linux Landlock 在进程树层面限制文件写;仅约束写,读与网络不限;能力缺失平台会自动告警并降级为纯协作式控制) |
| `GAH_SHELL_JAIL` | `0` = 关闭 shell 执行的**环境 jail**(默认开启:把 `TMPDIR`/`XDG_CACHE_HOME`/`GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` 重定向到 `$GAH_HOME/jail/**`,让构建缓存与临时文件不再散落用户家目录;`HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` 刻意保留以照常读 git/ssh 配置) |
| `GAH_EXT_ENV_PASS` | 显式放行给外部进程插件的环境变量(逗号分隔):外部插件默认**不继承宿主凭据**(`*_API_KEY`/`*_TOKEN`/`AWS_*`/`GAH_CB_*` 等已滤除),确需凭据的插件在此点名(如 `EXA_API_KEY`);或改用配置文件(推荐 `$GAH_HOME/config/search.yaml`) |
| `GAH_MCP_SERVE` / `GAH_PLUGIN` / `GAH_VERSION` | 外部进程工具入口参数(serve/加载插件/版本通告;由 host-bridge 拉起时注入) |
| `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM 密钥(可按 provider 前缀路由;或经 `/provider` 写入 provider.yaml) |
| `EXA_API_KEY` | web_search 联网搜索密钥(默认提供商;也可 `data.provider` 换其它) |

### MCP 接入(桥 client / serve 形态)

两种配法(优先级:配置文件 > 环境变量;GUI 改的是配置文件):

```yaml
# $GAH_HOME/config/mcp.yaml(设置面板「MCP server」段同源写入)
servers:
  - name: deja
    command: /opt/homebrew/bin/deja
  - name: codegraph
    command: codegraph serve --mcp
    mode: search      # 工具多时用 search:只给模型 mcp_search/mcp_call,省上下文
```
```bash
# 环境变量(启动面,只读;工具自动注册 mcp_<server>_<name>)
export GAH_MCP_COMMANDS="deja=/opt/homebrew/bin/deja\ncodegraph=codegraph serve --mcp"
./gah --profile web

# 作为 MCP server 对外提供本仓全部工具(可被 Claude Desktop 等外部 client 经 stdio 拉起)
./gah --profile mcp-serve
```

### ACP 接入(编辑器 ↔ gah)

`gah acp` 让编辑器把 gah 当 ACP agent 拉起(需 `$GAH_HOME/config/profile-acp.yaml`,出厂已带;`gah acp` ≡ `gah --profile acp`,bundle = base + confirm-fusion)。

```jsonc
// Zed(~/.config/zed/settings.json):agent_servers 段
{
  "agent_servers": {
    "gah": { "command": "/path/to/gah", "args": ["acp"] }
  }
}
```

编辑器里可用的:新会话(工作区 = `session/new` 的 cwd)、流式回复与思考块、工具调用与结果(含文件改动 patch)、命令菜单(`/` 前缀走 gah 自身的宿主命令,不经模型)、危险命令的审批弹层(应答直接回到 gah 的审批管线)。
单进程**只服务一个工作区**(首个 `session/new` 的 cwd 即绑定;换目录请另起一个 agent 实例)、**一次只跑一个回合**(并发提示返回 busy,可先 `session/cancel`)。
缺 `ctx.confirmFusion`(profile 未含 confirm-fusion bundle)时**拒绝启动**并给出原因:审批无人应答会让危险操作一律被拒,不该静默跑下去。

### 插件安装(TUI/Web 之外的能力扩展)

```bash
./gah -install <repo>[@version]     # 外部插件(go-plugin 桥/gRPC):git 拉取 → 构建 → 落 $GAH_HOME/plugins/<id>/ → 幂等登记,装完即启用
./gah -install mcp:<id>:<command>   # MCP 插件经同一入口登记桥配置
./gah -install-ui <repo|本地目录>   # UI 插件(manifest.json 声明槽位覆盖;两重拒装护栏:v-html 指令扫描 + 产物含未替换裸 process.env)
./gah -list-plugins / -uninstall <id>
./gah -list-ui-plugins / -uninstall-ui <id>
```

### 指令文件与技能

- **指令**:全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md`(近者覆盖远者;`AGENTS.override.md` 同级替换)——自动注入 system prompt,`/reload` 热更。
- **技能**:项目 `.gah/skills/` + 全局 `$GAH_HOME/skills/`,SKILL.md 扫描;模型经 `list_skills`/`read_skill` 按需加载。
- **主题**:`$GAH_HOME/config/themes/*.yaml`(`/theme` 切换,零重编译;仓库自带 gruvbox-dark 样板)。

## 八、插件开发

遵循 [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md)(仓库内已自注册为 `gah-plugin-dev` 技能,运行 gah 时模型可按需读取):

1. `plugins/<type>-<name>/`,实现 `sdk.Plugin`(Start 注册副作用,返回 Disposer)
2. 登记 `plugins/catalogue`(provides/requires/bundle)
3. `config/bundle-base.yaml` 加条目(启停/参数)+ `internal/embed/seed` 同步样板并 bump seed-version
4. 单测 + `-race` 全绿后提交

红线:插件**只 import `sdk/`**,不 import core/tui/其他插件;注册即副作用,卸载即撤销。工具类插件的执行体默认外部化到 `extplugins/`(崩溃隔离、独立升级),内置实现停用。

## 九、项目结构

```
├── cmd/gah/          # boot:profile 装配入口 + CLI 参数(profile/input/install/ephemeral…)
├── core/             # 微内核:ctx 服务容器 / event(5 分发)/ plugin(拓扑+热重载)/ config(profile→bundle→patch)
├── sdk/              # 插件唯一依赖的接口与域模型(独立 module)
├── bundles/          # bundle 装配(base/tui/web/register)
├── plugins/          # 全量插件:host/adapter/policy/tool/mcp/ui 类别分组
│                     # (catalogue/ 为汇总事实源;总览见 plugins/README.md)
├── extplugins/       # 外部进程插件入口(tool-basic/tool-workflow/tool-mcp/tool-subagent/tool-echo 示例)
├── tui/              # bubbletea v2 界面(状态机可测;ui-tui-app 薄壳引用)
├── web/              # Web 服务端 Go 运行时包(SSE/WS + REST + 静态托管 embed web/dist)
├── web-src/          # 前端工程(Vue3+Vite+TS;构建产物内嵌,UI 插件示例在 web-src/examples/)
├── tests/            # 端到端 + 迷你 MCP server(卸载矩阵见 AGENTS.md)
├── internal/         # embed(seed 样板/外部分发)/ install(插件安装)/ prefs(偏好持久化)/ providerfile
├── config/           # profile/bundle/patch 样板(seed-version 与 internal/embed/seed 同步)
├── scripts/          # gen.sh(统一构建)/ gen-web.sh / gen-extplugins.sh / gen-desktop.sh / publish-desktop.sh(桌面零成本发行)/ verify-release.mjs(发版后校验:平台矩阵/签名/包内版本)/ ws-smoke.go
├── desktop/          # 桌面壳 P1(Tauri v2 + sidecar gah;零成本发行:updater ed25519 自持签名 + CI 矩阵,见「三」)
├── .gah/skills/      # 自注册技能(gah-plugin-dev)
└── docs/             # 本地设计文档(随仓库分发仅 PLUGIN_DEV.md;其余设计/排期/清单为本地资料)
```

## 十、构建与发行

```bash
./scripts/gen.sh cli             # 本平台单二进制(TUI+Web+headless;前端已内嵌,前端未改动时自动跳过构建)
./scripts/gen.sh release         # goreleaser 六目标发行(tar.gz/zip + checksums)
./scripts/gen.sh desktop         # 桌面壳:sidecar(=cli 产物)→ tauri build(.app/.dmg,未签名)
./scripts/gen.sh web-dev         # 前端热更开发(vite watch + GAH_WEB_STATIC 免重启)
```

三端同一 gah 二进制:Web 前端内嵌,TUI/Web/headless 是同一进程的三个 profile;桌面壳复用同一 sidecar 产物。发行矩阵配套:

- `scripts/gen-extplugins.sh` 按发行矩阵(darwin/linux × amd64/arm64 + windows/amd64)构建外部插件产物,`gzip -9 -n` 确定性压缩,embed 按平台拆包(每目标只嵌本平台产物)。
- `goreleaser release --snapshot` 可直接出六目标包;`.goreleaser.yaml` 已配置 before hooks。

- **门禁(CI 与本地同源)**:`go vet` + 全库 `go test -race` + `bash scripts/coverage-check.sh`(逐包棘轮 + 全局下限,豁免需理由)+ `scripts/size-check.sh`(体积门 ≤36 MiB / gz ≤23 MiB)+ 前端 `npm test`(逻辑单测)+ **`npm run test:layout`**(真浏览器布局回归:5 视口 × 4 停靠态,断言整页不滚 / 无越界元素 / 骨架在场,并含**检测器自检**;CI 上带 `GAH_LAYOUT_REQUIRE=1`,环境不满足即红灯而不是静默跳过)。`sdk/` 是独立 module(`go.work`),三步需单独跑。

验收实测(DESIGN §7.6 / 最新基线):`CGO_ENABLED=0` 静态单文件 **30–34 MiB** 五目标(体积门 = `scripts/size-check.sh`,现值 ≤36 MiB / gz ≤23 MiB)、六目标交叉编译全绿、裸机 `env -i` 启动成功、sha256 附档。

## 十一、协议

**MIT License**(见 [LICENSE](./LICENSE),© 2026 nekoleamo):宽松许可,允许任意使用/修改/商用闭源分发。
