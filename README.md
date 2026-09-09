# go-agent-harness(gah)

Go 实现的编程代理 Agent Harness:以**单静态二进制**交付全部能力,零运行时依赖。对齐 DeepSeek Harness 与 Cordis 的「一切皆插件」设计哲学——微内核仅负责插件的加载/卸载/依赖管理(零 Agent 能力、零 UI),全部能力以插件形式经配置层(profile→bundle→patch)随时插拔开关。

> 设计参考:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)。
> - 完整设计:[DESIGN.md](./DESIGN.md)(§14.1 交付表/未实施清单)
> - 插件开发:[docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md);插件总览:[plugins/README.md](./plugins/README.md)
> - 能力与排期:交付记录见 [docs/ROADMAP.md](./docs/ROADMAP.md)(兼作实施排期)、[docs/TODO_OVERVIEW.md](./docs/TODO_OVERVIEW.md)(待办统筹单页)
> - 专项文档:[docs/VERIFY.md](./docs/VERIFY.md)(真机验收清单)、[docs/TUI_OPTIMIZE.md](./docs/TUI_OPTIMIZE.md)(TUI 交互细则)、[docs/WEB_ATTACHMENTS_PLAN.md](./docs/WEB_ATTACHMENTS_PLAN.md)(Web 附件规划)、[docs/DESKTOP_FEASIBILITY.md](./docs/DESKTOP_FEASIBILITY.md)(桌面端可行性)、[docs/PI_COMPARISON.md](./docs/PI_COMPARISON.md)

---

## 一、项目定位

**排除 dsh 因 Node.js 带来的依赖**:

- 单一静态二进制(`CGO_ENABLED=0`,实测 **36.8MB**),无 node 运行时、无 node_modules 分发链、无版本管理器;
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
| **ReAct 循环** | 对齐 dsh 轮次:pre-step → llm/stream → tool/call* → turn/end,AgentLoop 本身可替换 |
| **结构化工具** | MCP 兼容 JSON schema;执行流水线 pre-execute(veto)→ execute → post-execute → result 广播;错误结构化回传模型 |
| **LLM 统一域模型** | 纯 HTTP+SSE 的 OpenAI 兼容适配器(DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp 通吃)+ Anthropic 适配器(`claude-*` 前缀路由)+ mock 适配器(CI 免外网);多 provider 并存(`/provider`) |
| **沙箱三档** | read-only / workspace-write(防 `../` 穿越)/ full-access,TUI `/sandbox` 与 Web 设置面板运行期切换 |
| **审批三档** | 危险命令(rm -rf / git push -f / sudo / chmod 777…)按档:开放 open(放行)/ 智能 smart(弹确认,无确认通道时安全拒绝,默认)/ 严格 strict(拒绝);偏好持久化 |
| **凭据隔离** | 工具子进程 env 滤除 `*_API_KEY/_TOKEN/_SECRET`;危险操作无确认通道时安全拒绝 |
| **会话管理** | 项目级隔离 + 多会话切换 + 分支树(`/fork` `/clone` `/tree` + 命名);超预算 token 滚动摘要压缩(token-compress,完整日志留盘) |
| **模型工具** | shell / file_read-write-append-edit / web_fetch / web_search / workflow / subagent / todo / memory / auto_plan / job_* / 技能读取 / MCP 桥工具(详见「五、模型可用工具」) |
| **后台任务** | host-jobs + workflow `background`:长任务异步提交/取回/终止,不阻塞回合 |
| **子代理 fanout** | `agent/parallel/pipeline` 独立上下文 ReAct 扇出并行聚合;`send_message`/`fork` 注入与会话派生 |
| **starlark workflow** | 模型写受限 starlark 脚本组合多步工具调用(天然沙箱/无标准库),`background` 异步 |
| **联网搜索** | web_search(默认 Exa,`EXA_API_KEY`;`data.provider` 可换)与 web_fetch 协作,错误结构化归一 |
| **MCP 双向** | client 桥(接外部 server,工具 `mcp_<server>_<name>`)与 server 端(对外暴露本仓全部工具,可被 Claude Desktop 等拉起) |
| **外部插件桥** | host-bridge:独立进程插件(go-plugin),崩溃隔离(外部进程被杀宿主存活);宿主回调通道(GAH_CB_ADDR)供外部进程请求 tools/jobs/fanout 服务;工具类 100% 外部化(extplugins/) |
| **插件安装** | `gah -install <repo>[@version]`(外部/桥插件)与 `-install-ui <repo|目录>`(UI 槽位插件)一条命令装完即启用 |
| **指令文件与技能** | 全局/项目 AGENTS.md 自动注入(近者覆盖;`/reload` 热更);SKILL.md 技能扫描 + 模型按需加载(`list_skills`/`read_skill`);仓库自注册 `gah-plugin-dev` 技能 |
| **主题外部化** | `$GAH_HOME/config/themes/*.yaml` + `/theme` 运行期切换,零重编译换肤 |
| **整体备份/恢复** | `/backup` 一键打包 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好)→ 确定性 tar.gz;`list|restore`,恢复前自动先备份当前态 |
| **配置自愈** | 启动失败自动回滚最近正常备份重试一次,坏配置不卡死 |
| **pty 交互** | tool-shell `data.pty` 开关:驱动 REPL / git 编辑器等交互进程 |
| **Web 附件+多模态** | 输入框传图片/文件(按钮+拖放+粘贴),芯片预览/删除;图片经 openai/anthropic 适配器结构化注入(模型看图),文本附件路径引用;落盘 `$GAH_HOME/attachments/` |
| **优雅停机** | `POST /api/shutdown` → DisposeAll 全回收(Windows 无 SIGTERM 的统一停机通道;桌面壳/运维复用) |
| **桌面壳(P1)** | `desktop/` Tauri v2 壳:sidecar gah + 窗口直连本地服务;托盘/通知/自启/单实例;发行签名(CI/更新通道)属 P2 暂缓 |

## 三、快速开始

### 构建(发布形态)

```bash
# 直接构建(单文件,dev 版本号)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o gah ./cmd/gah
# 或统一入口(自动保障前端内嵌;v0.x.y 写 VERSION=v0.x.y)
./scripts/gen.sh cli
```

### 三种运行形态

```bash
./gah                                          # TUI(默认 tui profile;非 TTY 自动降级文本)
./gah web                                      # Web UI(≡ --profile web),http://127.0.0.1:2233,自动开浏览器
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
| `-profile <名>` | profile(tui/headless/dev/web/mcp-serve/自定义) |
| `-input <文本>` | headless 一次输入,跑一轮输出回复并退出 |
| `-dump-config` | 输出合并后的配置树并退出 |
| `-ephemeral` | 临时数据根,退出即焚 |
| `-version` | 输出版本信息 |
| `-install <repo>[@version]` | 安装线上/桥插件(`mcp:<id>:<command>` 登记 MCP 插件) |
| `-uninstall <id>` / `-list-plugins` | 卸载 / 列出外部插件 |
| `-install-ui <repo\|本地目录>` / `-uninstall-ui <id>` / `-list-ui-plugins` | UI 插件安装 / 卸载 / 列出 |

## 四、TUI 命令

> 命令注册进宿主 `ctx.commands`,TUI/Web/headless `/` 前缀通用;输入 `/` 弹出命令提示(名称+说明,可继续输入过滤)。TUI 专属命令(search/widgets/theme/help/exit/fork/clone/tree/name)仅 TUI 可用。

| 命令 | 作用 |
|---|---|
| `/model <名>` | 切换模型(**动态枚举当前端点全部模型**,来源括号备注如 `(siliconflow)`;选中自动切所属 provider;列表失败/无 key 回退手动输入) |
| `/thinking off\|low\|medium\|high` | 思考等级(推理预算);**快捷键 Shift+Tab 循环前进**;状态栏显示 `思维: <等级>`(off 隐藏) |
| `/sandbox ro\|ws\|full` | 运行期切沙箱档(只读/工作区写入/完全访问;状态栏实时显示,偏好持久化重启恢复) |
| `/approval open\|smart\|strict` | 运行期切审批档(开放=危险命令直接放行 / 智能=命中弹确认(默认)/ 严格=直接拒绝;偏好持久化重启恢复) |
| `/provider show\|add\|use\|set\|unset\|clear` | 配置 LLM 提供商(多 provider 并存):`show` 列出(活跃★凭据打码)/ `add 端点 key [model]` 新增(首个自动活跃)/ `use <名>` 切换 / `set 端点 key [model]` 编辑活跃 / `unset 字段` 逐项删(回退 env/样板)/ `clear` 全清并复位 |
| `/plugins list\|on\|off <id>` | 运行期插拔插件(`on/off` 持久化开关,重启仍生效;`default` 恢复配置树默认) |
| `/settings history N\|off\|unlimited` | 会话历史注入条数(`off` 禁止 / `unlimited` 全部 / N 最近 N 条;全局偏好跨会话) |
| `/compact [指示词]` | 手动滚动摘要压缩(立即折叠旧历史;自动超预算压缩不变;指示词仅记录) |
| `/export [path]` | 导出当前会话事件序列(`.html` 结尾 = 自包含 HTML 渲染,否则 jsonl) |
| `/workspace [目录]` | 切换工作区(项目):最近列表选择或输新目录;切换即开新会话、工具进程 cwd 真实切换、沙箱 root 同步 |
| `/session list\|switch\|new\|current` | 会话管理:列出 / 切换(二级选择器带内容预览与时间)/ 新建(空历史)/ 查看当前 |
| `/reload` | 热重载指令文件(AGENTS.md 层级/全局/附加;外部编辑即生效,免重启) |
| `/jobs list\|output <id>\|kill <id>` | 后台任务列表 / 取输出 / 终止(与 workflow `background`、Web 任务面板同源) |
| `/backup [dest]\|list\|restore <name>` | 整体备份 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好,排除 backups/ 自身):无参=立即备份(默认存 `$GAH_HOME/backups/`,可指定外部路径)/ `list` 列出(时间倒序)/ `restore <name>` 恢复(**恢复前自动先备份当前态**,重启后完全生效) |
| `/search <词>` | 会话内搜索(命中高亮,n/N/F3 循环跳转,Esc 退出) |
| `/theme [名]` | 切换主题(枚举 `$GAH_HOME/config/themes/*.yaml`;`default` 回默认;零重编译换肤,偏好持久化) |
| `/fork [seq]` | 从历史任意点派生分支会话(`/tree` 查看 seq;缺省=最近提问) |
| `/clone` | 复制当前会话(同一分支另一路演进) |
| `/tree` | 会话分支树(会话全貌 + 可 fork 的提问点) |
| `/name <显示名>` | 给当前会话加显示名(`-` 清除;状态栏/切换列表名优先) |
| `/widgets on\|off` | 输入区上方 widget 区开关(宿主注册的动态信息行) |
| `/help` `/exit` | 帮助 / 退出(**Ctrl+C 连按两次**,防误触) |

> **键位速记**:输入中单次 `Ctrl+C` 仅清空输入(不退出);输入为空时需**连按两次** `Ctrl+C`(2 秒窗口内)才彻底退出,第一次按下会高亮提示再按一次,超时或按其他键自动解除;`Esc` 取消进行中的回合;`Shift+Tab` 循环思考等级;`Ctrl+T` 折叠/展开思维块;`Ctrl+O` 折叠最近工具结果;`Ctrl+↑/↓` 跳到最早用户行/回底;`Ctrl+A` 全选(删除=清空/输入=替换)、`Ctrl+B/F` 左右移动、`Ctrl+Y` redo、`Alt+P` yank 粘贴、`Alt+←/→` 按词移动。

## 五、模型可用工具(由模型调用,无需交互)

| 工具 | 说明 |
|---|---|
| `shell` | 执行 shell 命令(沙箱/审批策略拦截;`data.pty` 可驱动交互式进程;凭据 env 滤除) |
| `file_read` / `file_write` / `file_append` / `file_edit` | 文件读写/追加/精确编辑(经沙箱路径校验) |
| `web_fetch` / `web_search` | 抓取 URL 正文 / 联网搜索(默认 Exa,`EXA_API_KEY`;`data.provider` 可换;401/429/5xx 结构化错误) |
| `workflow` / `workflow_collect` | 受限 starlark 脚本组合多步工具调用(天然沙箱);`background` 异步 + 收集 |
| `job_list` / `job_output` / `job_kill` | 后台任务查询/取输出/终止(与 `/jobs` 同源) |
| `memory` | 跨会话记忆(remember/list/recall/forget;`$GAH_HOME/memory/<project>.jsonl`,人工可编辑) |
| `todo` | 任务清单(create/start/complete/pend/delete/update/list;4 状态机 + blockedBy 依赖;`$GAH_HOME/todos/`) |
| `auto_plan` | 规划模式(create/get/list/step/confirm/complete;检测规划意图先输出结构化规划,确认前零副作用工具调用;`$GAH_HOME/plans/`) |
| `subagent` | 子代理委派(delegate/spawn/agents/agent_status/agent_kill/send_message/fork;独立上下文 ReAct,后台带句柄) |
| `list_skills` / `read_skill` | 技能索引 / 按需加载 SKILL.md(项目 `.gah/skills/`、`$GAH_HOME/skills/`) |
| `mcp_<server>_<工具>` | MCP 桥工具(GAH_MCP_COMMAND 单 / GAH_MCP_COMMANDS 多 server,见「MCP 接入」) |

## 六、Web 使用(设置面板/REST)

`gah web` 起 http://127.0.0.1:2233(自动开浏览器;`GAH_WEB_OPEN=0` 关闭)。功能与 TUI 对等,另有可视化面板:

- **设置面板**(状态栏 ⚙):模型下拉(聚合全部 provider)、思考/沙箱/**审批**分段控件、历史注入下拉 + 压缩按钮、Provider 管理(启用/删除/新增)、插件开关、指令重载、**数据备份**(立即备份 / 恢复备份——**二次确认**);全部设置退出即记(偏好持久化,gah-state.json 与 TUI 共享)。
- **侧栏**:工作区固定区(切换 = 真实切目录) + 历史会话(名称/内容预览/时间,✎ 改名、× 删除——二次确认)、附件上传(按钮/拖放/粘贴,图片缩略图 + 模型看图)、会话导出(⤓ jsonl/HTML)。
- **状态栏**:连接状态(绿/橙)、模型/思维/沙箱/会话、上下文·缓存使用率、后台任务钮(运行徽标 + 列表/输出/终止)。
- **WebSocket 通道**:`/api/events/ws`(与 SSE 同 payload,前端自动降级)。
- **通用 REST 能力面**(前端/脚本均可直接调用,未装配服务 503/501 显式):

| 方法 路径 | 说明 |
|---|---|
| `GET /api/state` | 状态快照(model/thinking/sandbox/approval/stats/session/running/version) |
| `POST /api/input` | 提交回合;`/` 前缀走命令;running 时 409 |
| `POST /api/confirm` | 审批应答 `{id, ok}` |
| `GET /api/events` + `GET /api/events/ws` | 事件流(SSE 断线重放 / WS) |
| `GET /api/sessions`、`POST /api/sessions` | 会话列表 / `{action: switch\|new\|fork\|clone\|delete}` |
| `POST /api/sessions/rename`、`GET /api/sessions/{id}/export` | 会话改名 / 导出 jsonl |
| `GET /api/workspaces`、`DELETE /api/workspaces/{key}` | 工作区历史 / 删除记录(不动文件夹) |
| `GET /api/commands`、`POST /api/commands/{name}` | 命令注册表 / 直接执行 `{args}` |
| `GET /api/tools`、`POST /api/tools/{name}` | 工具清单 / 调用(参数 JSON 透传) |
| `GET /api/jobs`、`GET/POST /api/jobs/{id}[/kill]` | 后台任务 |
| `GET /api/plugins`、`POST /api/plugins/{id}/load\|unload` | 插件启停 |
| `GET /api/models?all=1`、`GET/POST /api/providers...` | 模型聚合 / provider CRUD |
| `POST /api/control` | 状态栏级控制 `{model?\|thinking?\|sandbox?\|approval?\|workspace?}`(偏好持久化) |
| `POST /api/compact`、`POST /api/settings/history` | 手动压缩 / 历史注入条数 |
| `GET /api/todo` | 任务面板数据(todo 工具 list 透传) |
| `GET /api/backup`、`POST /api/backup` | 备份列表 / `{action: backup\|restore, dest?, name?}` |
| `POST /api/attachments` + `GET /attachments/...` | 附件上传(20MB/类型白名单)/ 静态预览 |
| `POST /api/reload` | 指令文件热更 |
| `POST /api/shutdown` | 优雅停机(→ system/shutdown → DisposeAll 全回收;桌面壳/运维复用) |
| `GET /api/ui-plugins` + `/ui-plugins/` | UI 插件聚合视图 / 静态托管 |

## 七、配置与运行时目录

### 配置层(profile→bundle→patch)

```
profile-<name>.yaml     # bundles + patches 按序声明
bundle-base.yaml        # 插件条目(id + enabled + data 参数)
patch-*.yaml            # 按 id 替换/插入/启停条目(随时插拔)
```

样板源在 `config/`(与 `internal/embed/seed` 两份同步,`# seed-version: N` 头,新增 base 条目必须 bump)。`--dump-config` 打印的任一条目都可被自己的 patch 替换。

### 运行时数据根(GAH_HOME,完全便携)

优先级:`GAH_HOME env` > **gah 二进制同级的 `gah-data/`(不存在则首次启动自动新建并释放初始化内容——config 样板与随包插件,部署目录即自包含)** > `~/.gah` > TempDir。

```bash
# 1) 把 gah 放到任一目录,首次运行自动建 gah-data/ 并释放 config 样板 + 插件:
./gah --profile web
#    已有数据(如从旧 ~/.gah):mkdir gah-data && cp -a ~/.gah/. gah-data/  # 密钥随 config 进入
# 2) 后续升级只替换 gah 这一个文件,数据不动;只读目录无法自建时回落 ~/.gah
```

- 多份 gah 副本可共享同一 gah-data(数据仍单根,不落系统根/不散 cwd);显式覆盖用 `GAH_HOME=/path ./gah ...`。
- API key 等密钥统一在 `gah-data/config/`(provider.yaml / search.yaml),随目录整体备份/迁移。
- **整体备份**:`/backup` 即打包整个 gah-data(排除 backups/ 自身)→ 默认落 `gah-data/backups/`(随目录迁移)或指定外部路径;`/backup restore <name>` 恢复(先自动备份当前态)。

### 环境变量

| 变量 | 作用 |
|---|---|
| `GAH_HOME` | 数据单根(优先于便携自动发现;缺省链 = `GAH_HOME` > 二进制同级 `gah-data/` > `~/.gah` > TempDir) |
| `GAH_PROFILE` / `GAH_NO_TUI` | 默认 profile / 强制关闭 TUI(headless/CI) |
| `GAH_WEB_ADDR` / `GAH_WEB_OPEN` / `GAH_WEB_STATIC` | Web 监听地址(默认 127.0.0.1:2233)/ 是否自动开浏览器 / 静态目录覆写(开发态 HMR) |
| `GAH_MCP_COMMAND` / `GAH_MCP_COMMANDS` | MCP 桥接入(单 server `name=command` / 多 server 每行 `name=command args`,工具 `mcp_<server>_<工具>`) |
| `GAH_CB_ADDR` / `GAH_CB_TOKEN` | host-bridge 回调通道(外部进程插件请求宿主 tools/jobs/fanout 服务;含鉴权 token) |
| `GAH_MCP_SERVE` / `GAH_PLUGIN` / `GAH_VERSION` | 外部进程工具入口参数(serve/加载插件/版本通告;由 host-bridge 拉起时注入) |
| `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM 密钥(可按 provider 前缀路由;或经 `/provider` 写入 provider.yaml) |
| `EXA_API_KEY` | web_search 联网搜索密钥(默认提供商;也可 `data.provider` 换其它) |

### MCP 接入(桥 client / serve 形态)

```bash
# 作为 MCP client 接入外部 server(一行一个 server,工具自动注册 mcp_<server>_<name>)
export GAH_MCP_COMMANDS="deja=/opt/homebrew/bin/deja\ncodegraph=codegraph serve --mcp"
./gah --profile web

# 作为 MCP server 对外提供本仓全部工具(可被 Claude Desktop 等外部 client 经 stdio 拉起)
./gah --profile mcp-serve
```

### 插件安装(TUI/Web 之外的能力扩展)

```bash
./gah -install <repo>[@version]     # 外部插件(go-plugin 桥/gRPC):git 拉取 → 构建 → 落 $GAH_HOME/plugins/<id>/ → 幂等登记,装完即启用
./gah -install mcp:<id>:<command>   # MCP 插件经同一入口登记桥配置
./gah -install-ui <repo|本地目录>   # UI 插件(manifest.json 声明槽位覆盖;v-html 指令扫描拒装)
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
├── scripts/          # gen.sh(统一构建)/ gen-web.sh / gen-extplugins.sh / gen-desktop.sh / ws-smoke.go
├── desktop/          # 桌面壳 P1(Tauri v2 + sidecar gah;发行签名 P2 暂缓)
├── .gah/skills/      # 自注册技能(gah-plugin-dev)
└── docs/             # PLUGIN_DEV/VERIFY/TUI_OPTIMIZE/ROADMAP/TODO_OVERVIEW/DESKTOP_FEASIBILITY/…
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

验收实测(DESIGN §7.6 / 最新基线):`CGO_ENABLED=0` 静态单文件 **36.8MB**(<40MB 体积门)、六目标交叉编译全绿、裸机 `env -i` 启动成功、sha256 附档。

## 十一、当前状态

- **里程碑全部交付**:M1–M18、P0–P5.7、M7 Web 全线、TUI 优化线(S1–S3)*、P4 体验 12 项——DESIGN §14.1 未实施清单已清零;全库 `go test ./... -race` 全绿。
- **双界面并存**:TUI 与 Web 能力对等,同一数据根;桌面壳(P1)已落地,发行签名/CI/更新通道(C2/C3)暂缓。
- 交付明细与逐项验收见 [DESIGN.md](./DESIGN.md) §14.1 交付表 / [docs/ROADMAP.md](./docs/ROADMAP.md) / [docs/TODO_OVERVIEW.md](./docs/TODO_OVERVIEW.md)。

## 十二、协议

**MIT License**(见 [LICENSE](./LICENSE),© 2026 nekoleamo):宽松许可,允许任意使用/修改/商用闭源分发。
