# go-agent-harness(gah)

> [English](./README_EN.md) | 简体中文(两份同步维护)

Go 实现的编程代理 Agent Harness:一个静态二进制承载 TUI / Web / headless / 桌面壳,零运行时依赖。设计对齐 DeepSeek Harness 的「一切皆插件」——微内核只负责 ctx 服务容器、事件总线、插件注册表与配置层,其余能力全是插件。

- 插件开发:[docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md) · 插件总览:[plugins/README.md](./plugins/README.md)
- 设计文档与交付记录为本地资料,不随仓库分发;对外交付总览即本文与提交历史

## 一、项目定位

- **单一静态二进制**:`CGO_ENABLED=0`,六目标交叉编译(darwin / linux / windows × amd64 / arm64);实测单文件 27–29 MiB、gz 12–14 MiB,体积门 ≤30 / ≤15 MiB(`scripts/size-check.sh`)。
- **零运行时依赖**:无 node 运行时、无 node_modules、无版本管理器;裸环境 `env -i` 可直接启动。
- **完全便携**:数据根 = 二进制同级 `gah-data/`,首次启动自动创建;部署目录内 `gah` + `gah-data/` 即完整,升级只换单文件。
- **三端同源**:TUI、Web(前端 `go:embed` 内嵌)、headless 是同一进程的三个 profile;桌面壳复用同一个 sidecar 产物。

## 二、功能特性

| 能力 | 说明 |
|---|---|
| **微内核** | `core/` 仅含 ctx / event / plugin(拓扑 + 热重载)/ config(profile→bundle→patch)/ boot,零业务逻辑 |
| **一切皆插件** | 会话、LLM 路由、工具、沙箱、审批、Agent 循环、界面均为插件;`/plugins` 运行期插拔,卸载即撤销副作用 |
| **双界面 + headless** | TUI(bubbletea v2)与 Web(Vue 3)能力对等、共享同一 `$GAH_HOME`;`--profile headless` 供 CI/脚本一次性跑 |
| **ReAct 循环** | `pre-step → llm/stream → tool/call* → turn/end`,步数上限可配(缺省不限) |
| **回合内转向** | 回合运行中回车即把消息注入当前回合;注入不成转「待发」,`Alt+Up` 取回 |
| **工具流水线** | MCP 兼容 schema;`pre-execute`(可 veto)→ execute → post-execute → result 广播;错误结构化回传 |
| **LLM 统一域模型** | OpenAI 兼容适配器(DeepSeek / OpenAI / Ollama / vLLM / Kimi …)+ Anthropic 适配器 + mock;多 provider 并存 |
| **沙箱三档** | read-only / workspace-write / full-access;写路径统一裁决,`shell` 的重定向与输出旗标同口径;macOS seatbelt、Linux Landlock 在进程树层面兜底 |
| **审批三档** | open / smart(默认,弹确认且不超时)/ strict;无确认通道一律安全拒绝 |
| **会话与上下文** | 项目级隔离、多会话、fork/clone/tree、超预算滚动摘要、`/cache` 命中率诊断、`/usage` 跨会话用量 |
| **多会话并行** | Web/桌面端页签各自独立跑;同一会话内严格串行,会话日志单写者 |
| **任务与计划** | 后台任务、workflow(starlark)、子代理 fanout、worktree 隔离、定时计划(中文排期控件,含农历) |
| **提示通道** | `ctx.notices` 面向人发提示(不进会话、不计 token);TUI 走终端通知,桌面壳走系统通知 |
| **MCP / ACP 双向** | MCP client 桥与 server 端;`gah acp` 被编辑器当一等 agent,`acp-client` 把任务委派给外部编码 agent |
| **插件安装** | CLI `/install` / Web 面板共用一份内核;来源固定与漂移守卫、`--prebuilt` 免构建脚本、完整性闸、随时启停 |
| **桌面壳** | Go + Wails v3:托盘、通知、开机自启、单实例、多实例、`gah-shell.log` 诊断;自动更新(ed25519 自持签名) |
| **文档预览** | 一套块模型 + 四端渲染(markdown / 文本 / 代码 / CSV / notebook / PDF / Office),Web 工作台、TUI pager、`gah doc` |

## 三、快速开始

### 下载安装(非编程用户推荐)

从 [Releases](https://github.com/nekoleamo/go-agent-harness/releases/latest) 下载对应文件:

| 系统 | 下载文件 | 说明 |
|---|---|---|
| macOS(Apple 芯片) | `gah-<版本>-aarch64-apple-darwin.app.zip`(另有 `.dmg`) | 解压后把 gah 拖进「应用程序」 |
| Windows(64 位) | `gah-<版本>-setup.exe` | 双击安装,仅当前用户,不要求管理员 |
| Windows 便携版 | `gah-<版本>-portable-x86_64-pc-windows-msvc.zip` | 不安装:解压到可写目录,双击 `gah-desktop.exe` |
| Windows on ARM | `gah-<版本>-portable-aarch64-pc-windows-msvc.zip` | 同上(arm64 不单独出安装器) |
| 命令行版(任意平台) | `go-agent-harness_<版本>_<系统>_<架构>.tar.gz` | 解压即用的单文件二进制(另附 `.zip` 与 `checksums.txt`) |

**首次打开会被系统拦一次**(未购买 Apple / 微软签名证书,程序本身完好):

- macOS「无法验证开发者」→ 在「应用程序」里右键 gah → 打开 → 打开(只需一次)。
- macOS「已损坏,无法打开」→ 文件没坏,是「未公证 + 从网络下载」的固定措辞,终端跑一次:`xattr -dr com.apple.quarantine /Applications/gah.app`。
- Windows SmartScreen → 「更多信息」→「仍要运行」。

装好后双击即用,首次在设置面板填一个模型服务商(DeepSeek / Kimi / 智谱 / 通义 / Ollama 等有一键预设)。

**数据与升级**

- 数据(会话、记忆、计划、密钥)都在 `gah-data/`,升级与卸载都**不动**它;卸载页「Delete app data」勾选才会删(默认不勾)。
- 桌面版升级前额外备份一份到 `~/gah-upgrade-backup/`,备份失败则取消升级。
- 国内网络:升级源默认 Gitee 优先、取不到表自动落 GitHub;「表能取到但包下不下来」的源会被记忆并垫后。签名只对文件内容验签,换源不降低强度。
- **从 0.7.x 桌面版升到 0.8.x 需手动装一次**:0.8.0 起壳从 Tauri 换成 Go + Wails v3,清单格式与签名口径都变了,旧壳读不了新清单。手动覆盖安装一次即可,数据不丢;之后恢复自动更新。命令行版不受影响。

### 桌面端形态

| | 安装版(macOS / Windows) | 便携版(仅 Windows) | 命令行版 |
|---|---|---|---|
| 怎么用 | macOS 拖进「应用程序」;Windows 跑 `-setup.exe` | 解压到可写目录,双击 `gah-desktop.exe` | 解压 tar.gz,得到单个 `gah` |
| 判定标记 | 应用目录里有壳 | 同目录有 `portable.marker` | 无 |
| 运行文件 | 外置到用户数据目录 `bin/`(macOS `~/Library/Application Support/dev.gah.desktop/`,Windows `%LOCALAPPDATA%\dev.gah.desktop\`) | 就地运行 | 目录内 |
| 数据 | 上述 `bin/gah-data/` | 本目录 `gah-data/` | 同目录 `gah-data/` |
| 升级 | 托盘「检查更新…」→ 确认 → 下载 → 替换 → 重启 | 可原地自动更新;目录不可写则只查不装并给下载指引 | 替换单文件 |

安装版与便携版身份独立、可同时运行;同形态重复双击只聚焦已有窗口(要多开用托盘「新实例…」或 `--new-instance`)。出问题先看 `<用户数据目录>/gah-shell.log`(托盘「关于 gah → 诊断」可直接打开)。

### 构建

```bash
# 单文件(版本号取自 git tag)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o gah ./cmd/gah

# 或统一入口(自动保障前端内嵌;发布用 VERSION=v0.8.0 ./scripts/gen.sh cli)
./scripts/gen.sh cli
```

### 全局安装(一次部署,任意目录启动)

```bash
# macOS / Linux:构建 → ~/.local/gah + 符号链接 ~/.local/bin/gah
bash scripts/install.sh

# Windows(PowerShell):构建 → %LOCALAPPDATA%\gah + 加入用户 PATH
powershell -ExecutionPolicy Bypass -File scripts\install.ps1
```

安装后任意目录:`gah` / `gah web` / `gah --profile headless -input "…"`。升级 = 重跑脚本,卸载 = `--uninstall`。

> **Windows 前置**:`shell` 工具与后台任务经 POSIX shell 执行,需装 [Git for Windows](https://git-scm.com/download/win)(自动探测常见安装位)或设 `GAH_SHELL_PATH`。不提供 cmd.exe / PowerShell 回退:写目标裁决按 POSIX 词法,换 shell 会让沙箱判定与实际执行脱节。

### 三种运行形态

```bash
./gah                                              # TUI(默认;stdin 非 TTY 时显式拒绝,提示走 headless)
./gah web                                          # Web UI,http://127.0.0.1:2233,自动开浏览器
./gah acp                                          # ACP agent 形态(编辑器经 stdio 拉起)
./gah --profile headless --input "执行 echo hi"     # headless 一轮
./gah --profile mcp-serve                          # MCP server 形态(外部 client 经 stdio 拉起)
./gah --profile dev --dump-config                  # 查看合并后的配置树
./gah --ephemeral --profile headless --input "hi"  # 临时数据根,退出即焚
```

### 接入真实大模型

```bash
export DEEPSEEK_API_KEY=sk-...     # 或 OPENAI_API_KEY / ANTHROPIC_API_KEY(供 claude-* 模型)
./gah --profile headless --input "你好"
```

TUI 内零改配置、零重启挂任意 OpenAI 兼容端点:

```bash
/provider set https://api.siliconflow.cn/v1 sk-<key> deepseek-ai/DeepSeek-V3   # 立即生效并持久化(0600)
/provider show | use <名> | unset <字段> | remove <名> | clear                 # 增删改查与复位
```

### 常用 CLI 参数

| 参数 | 作用 |
|---|---|
| `-profile <名>` | profile(tui / headless / dev / web / acp / mcp-serve / 自定义) |
| `-input <文本>` | headless 一次输入,跑一轮后退出 |
| `-dump-config` | 输出合并后的配置树 |
| `-ephemeral` | 临时数据根,退出即焚 |
| `-version` | 版本信息(纯查询,管道/CI 可用) |
| `-install <repo\|本地目录>[@版本或 commit]` | 安装插件;`mcp:<id>:<command>` 登记 MCP 插件 |
| `-prebuilt` / `-accept-drift` | 装作者发布的预编译产物 / 接受同名 tag 指向新 commit |
| `-install-artifact <url> -id <id> -name <tool-x>` | 装别人已构建的产物(本机无需工具链) |
| `-check-plugin-updates` | 只发 `git ls-remote` 查来源更新,不 clone 不安装 |
| `-list-plugins` / `-uninstall <id>` | 外部插件清单(带来源三件)/ 卸载 |
| `-trust-plugin` / `-untrust-plugin` / `-list-trusted-plugins` | 哈希白名单登记 / 撤销 / 列出 |
| `-install-ui` / `-list-ui-plugins` / `-uninstall-ui` / `-trust-ui-plugin` | UI 插件安装与完整性闸放行口 |

```bash
gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--tree] [--depth N]
# 退出码:0 成功 / 2 用法 / 3 不支持格式 / 4 超预算 / 5 解析失败
# --convert:本机有 LibreOffice 时把旧二进制 Office 转 PDF 再抽取(缺省关)
```

## 四、TUI 命令

命令注册进 `ctx.commands`,TUI / Web / headless 的 `/` 前缀通用;输入 `/` 弹出提示,逐级补全。

| 命令 | 作用 |
|---|---|
| `/model <名>` | 切换模型(动态枚举当前端点;角色声明模型时只改会话档并提示) |
| `/thinking off\|low\|medium\|high` | 思考档(`Shift+Tab` 循环) |
| `/sandbox ro\|ws\|full` · `/sandbox sync on\|off` | 沙箱档 / 审批档联动开关 |
| `/approval open\|smart\|strict` | 审批档(持久化) |
| `/provider show\|add\|use\|set\|unset\|remove\|clear` | 多 provider 增删改查 |
| `/plugins list\|on\|off <id>` | 运行期插拔插件 |
| `/compact [指示词]` · `/cache [n]` · `/context [all]` · `/usage [天数\|all]` | 手动压缩 / 命中率诊断 / 上下文分解 / 跨会话用量 |
| `/workspace [目录]` · `/session list\|switch\|new\|current\|pin\|summary` | 工作区与会话管理 |
| `/fork [seq]` · `/clone` · `/tree` · `/name <名>` | 分支派生与命名 |
| `/role list\|use\|new\|rm\|export\|import` · `/role-pack export\|import` | 角色管理与角色包分享 |
| `/skill-export` · `/skill-import` | 技能包导出 / 导入 |
| `/diff [路径]` · `/recap` · `/traj` · `/plan` · `/todo` | 变更审查 / 会话速览 / 轨迹 / 规划 / 待办 |
| `/jobs` · `/worktree` · `/schedule` | 后台任务 / 隔离工作树 / 定时计划 |
| `/backup [dest]\|list\|restore <名>` | 整体备份与恢复 |
| `/preview <路径>` · `/search <词>` · `/theme [名]` | 文档预览 / 会话内搜索 / 主题切换 |
| `/export [path]` | 导出会话(`.html` = 自包含网页,否则 jsonl;Web/桌面端等价入口见侧栏 ⤓ 与顶栏「导出」) |
| `/reload` · `/settings history N` · `/statusline` · `/notify` · `/help` | 热重载 / 历史条数 / 状态栏 / 系统通知 / 帮助 |

> 输入 `!` 开头执行 shell 命令,走与模型工具同一条沙箱/审批管线。退出:`Ctrl+C` 连按两次。

## 五、模型可用工具

| 工具 | 说明 |
|---|---|
| `shell` | 执行命令(沙箱/审批拦截,写目标裁决,凭据 env 滤除,环境 jail;`data.pty` 可驱动交互进程) |
| `file_read` / `file_write` / `file_append` / `file_edit` | 读写编辑;`file_read` 支持 `offset`/`limit` 分页并如实报告截断 |
| `file_list` / `file_glob` / `file_grep` | 列目录 / 通配查找 / 内容搜索 |
| `file_mkdir` / `file_move` / `file_copy` / `file_rename` | 默认不覆盖;同目录临时文件 + rename 原子落盘,跨设备自动改走复制 |
| `file_delete` / `file_trash_list` / `file_restore` | 默认移入回收站(`$GAH_HOME/trash/`),`permanent: true` 才是真删 |
| `image_ops` | info / convert / resize / crop / rotate / compress / thumbnail(默认剥离元数据含 GPS) |
| `web_fetch` / `web_search` | 抓正文 / 联网搜索(默认 AnySearch 匿名可用;反注入与内网拦截默认开) |
| `workflow` / `workflow_collect` | starlark 脚本组合多步工具调用(天然沙箱,可后台) |
| `job_list` / `job_output` / `job_kill` | 后台任务 |
| `memory` · `todo` · `auto_plan` · `session_search` | 跨会话记忆 / 待办 / 规划模式 / 历史会话检索 |
| `schedule` | 定时计划管理(默认停用,需在配置层打开) |
| `subagent` | 子代理委派;`isolate="worktree"` 隔离到受管 git worktree |
| `list_skills` / `read_skill` · `list_roles` / `read_role` | 技能与角色(角色只读,写走 `/role` 与面板) |
| `mcp_<server>_<工具>` · `mcp_search` / `mcp_call` | MCP 工具(direct 模式 / search 按需检索) |
| `ask_user_question` | 结构化提问(Web 弹层,TUI `/answer`) |

## 六、Web 使用

`gah web` 起 `http://127.0.0.1:2233`(`GAH_WEB_OPEN=0` 关闭自动开浏览器)。功能与 TUI 对等,另有可视化面板:设置(模型 / 推理 / 指令 / 角色 / 会话历史 / Provider / 备份 / 计划 / MCP / 插件)、文档预览工作台、轨迹 / 变更 / 看板三视图、侧栏停靠区、结构化提问、附件与图片注入。

`token` 模式(`data.auth_token` 非空)下全表面鉴权,`POST /api/auth` 是唯一豁免路径。

主要 REST 能力(未装配服务返回 503/501):

| 路径 | 说明 |
|---|---|
| `GET /api/state` · `POST /api/input` · `POST /api/confirm` · `POST /api/control` | 状态快照(`?session=`) / 提交回合(运行中即转向) / 审批应答 / 模型·沙箱·审批·中止 |
| `GET /api/events` · `GET /api/events/ws` · `GET /api/session/events` | 事件流(SSE / WS,断线按 `after` 续传)+ 长会话上滚分页 |
| `GET/POST /api/sessions` · `rename` `/api/sessions/{id}/export` | 会话列表与 switch/new/spawn/fork/clone/delete / 改名 / 导出 |
| `GET/POST/DELETE /api/providers` `GET /api/models?all=1` | provider 增删改与模型聚合 |
| `GET /api/tools` · `POST /api/tools/{name}` · `GET /api/commands` | 工具与命令直调 |
| `GET/POST /api/schedules`(`PATCH`/`DELETE`/`run`/`resolve`) | 定时计划;`resolve` 把一句中文或 cron 解释成排期 |
| `GET/POST /api/mcp` · `GET /api/plugins` · `POST /api/plugins/{id}/load\|unload` | MCP 配置(写盘 + 热重载)/ 插件启停 |
| `POST /api/attachments` + `GET /attachments/...` | 附件上传(20MB + 类型白名单)/ 预览 |
| `GET /api/jobs` · `GET /api/todo` · `GET/POST /api/backup` | 后台任务 / 待办 / 备份恢复 |
| `POST /api/compact` · `POST /api/reload` · `POST /api/shutdown` | 压缩 / 指令热更 / 优雅停机 |
| `GET /api/doc/preview\|raw\|asset\|tree\|html` · `POST /api/doc/render` | 文档预览(块模型 / 原生字节 / 资产 / 文件树 / HTML 沙箱) |

## 七、配置与运行时目录

### 配置层(profile → bundle → patch)

```
profile-<name>.yaml     # bundles + patches 按序声明
bundle-base.yaml        # 插件条目(id + enabled + data)
patch-*.yaml            # 按 id 替换 / 插入 / 启停条目
```

样板源在 `config/`(与 `internal/embed/seed` 两份同步,头部 `# seed-version: N`)。配置条目 id = `<type>-<name>`,与 `plugins/` 目录同名。

### 数据根(完全便携)

- 数据根 = **二进制同级 `gah-data/`**(唯一;不存在则首启自动新建并释放 config 样板与随包插件)。创建失败(只读目录)启动报错退出,移到可写目录即可。
- 密钥统一在 `gah-data/config/`(provider.yaml / search.yaml),随目录整体备份迁移。
- 整体备份:`/backup` 打包整个 `gah-data/`(排除 `backups/` 自身)到 `gah-data/backups/` 或指定外部路径。

### 环境变量(常用)

| 变量 | 作用 |
|---|---|
| `GAH_PROFILE` / `GAH_NO_TUI` | 默认 profile / 强制关闭 TUI |
| `GAH_WEB_ADDR` / `GAH_WEB_OPEN` / `GAH_WEB_STATIC` / `GAH_WEB_TOKEN` | Web 监听地址 / 自动开浏览器 / 静态目录覆写(HMR)/ 桌面壳 token |
| `GAH_EXPORT_OPEN` | `/export` 后是否自动打开(`0` 关) |
| `GAH_MCP_COMMAND(S)` | MCP 桥接入(`config/mcp.yaml` 优先) |
| `GAH_SHELL_KERNEL_SANDBOX` / `GAH_SHELL_CRED_READ_KERNEL` / `GAH_SHELL_JAIL` | shell 内核沙箱 / 凭据目录读拒绝 / 环境 jail(默认全开,`0` 关) |
| `GAH_EXT_ENV_PASS` · `GAH_EXT_KERNEL_SANDBOX` · `GAH_EXT_RW_PATHS` | 外部插件:显式放行 env / 内核沙箱 / 追加可写路径 |
| `GAH_WEB_APPROVE` · `GAH_WEB_ALLOW_HOSTS` · `GAH_WEB_ALLOW_PRIVATE` | `web_fetch` 出口审批档 / 静态白名单 / 放行内网 |
| `GAH_SEARCH_PROVIDER` / `GAH_SEARCH_API_KEY` / `ANYSEARCH_API_KEY` / `EXA_API_KEY` | 搜索 provider 与密钥(env 优先于 `search.yaml`) |
| `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM 密钥(或经 `/provider` 写入 provider.yaml) |
| `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` | 出网代理;桌面壳未设时自动读 macOS 系统代理 |

> `GAH_HOME` 是内部贯通变量(boot 设为便携根),用户显式设置被忽略。

### MCP 接入

```yaml
# $GAH_HOME/config/mcp.yaml(设置面板「MCP server」段同源写入,保存即热重载)
servers:
  - name: codegraph
    command: codegraph serve --mcp
    mode: search        # search:只给模型 mcp_search/mcp_call,省上下文
  - name: remote-crm
    transport: http
    url: https://mcp.example.com/rpc   # 必须 https;明文只允许本机回环
```

MCP server 是第三方代码,起进程时按内核沙箱限制文件写。作为 MCP server 对外提供本仓工具:`./gah --profile mcp-serve`。

### ACP 接入

`gah acp` 让编辑器把 gah 当 ACP v1 agent 拉起(会话、流式回复、工具进度、审批全走 ACP,数据与 TUI/Web 同源):

```jsonc
// Zed(~/.config/zed/settings.json)
{ "agent_servers": { "gah": { "command": "/path/to/gah", "args": ["acp"] } } }
```

单进程只服务一个工作区,一次只跑一个回合;编辑器权限按钮只映射 `allow_once` / `reject_once`。

### 插件安装

```bash
./gah -install <repo>[@版本或40位sha]        # 外部插件:拉取 → 构建 → 落 $GAH_HOME/plugins/<id>/ → 登记
./gah -install <本地目录>                    # 本地插件不用先 git init + push
./gah -install-ui <repo|本地目录>            # UI 插件(两重拒装护栏:v-html 扫描 + 裸 process.env)
./gah -install-artifact <url> -id <id> -name <tool-x>   # 免工具链装别人构建好的产物
```

- **来源固定**:同名 tag 指向新 commit 默认拒绝(`-accept-drift` 才接受);`@<sha>` 可装回指定那次提交。
- **检查更新而非自动更新**:`-check-plugin-updates` 只 `git ls-remote`;**不验签名**,故不做自动更新。
- **完整性与启停**:`SHA256SUMS` 一存在即强制(UI 插件同);停用 ≠ 卸载,停用状态在 boot 时即生效。
- **构建收口**:`-mod=readonly` 构建、凭据 env 清洗、`GOFLAGS` 等语义开关剥除;`--prebuilt` 完全不执行仓库脚本。

### 指令文件与技能

- **指令**:全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md`(近者覆盖,`AGENTS.override.md` 同级替换),自动注入,`/reload` 热更。
- **技能**:项目 `.gah/skills/` + `$GAH_HOME/skills/` + 当前角色私有技能;模型经 `list_skills` / `read_skill` 按需加载。
- **角色**:`$GAH_HOME/roles/<id>/`(人设 + 工作规则 + 私有技能 + 可选模型/思考档 + 工具排除 + 只能收紧的权限档),12 个预置角色按场景分五组;`/role` 或面板切换即生效、不换会话。
- **主题**:`$GAH_HOME/config/themes/*.yaml`,`/theme` 运行期切换。

## 八、插件开发

细读 [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md)(仓库自注册为 `gah-plugin-dev` 技能,模型可按需读取):

1. `plugins/<类型>-<名字>/` 实现 `sdk.Plugin`(Start 注册副作用,返回 Disposer)
2. 登记 `plugins/catalogue`(provides / requires / bundle)
3. `config/bundle-base.yaml` 加条目 + `internal/embed/seed` 同步样板并 bump `seed-version`
4. 单测 + `-race` 全绿后提交

红线:插件只 import `sdk/`,不 import `core/`、`tui/` 或其他插件;注册即副作用,卸载即撤销,disposers 幂等。

## 九、项目结构

```
├── cmd/gah/      # boot:profile 装配 + CLI 参数
├── core/         # 微内核:ctx / event / plugin / config / boot
├── sdk/          # 插件唯一依赖的接口与域模型(独立 module)
├── bundles/      # bundle 装配(base / tui / web / register)
├── plugins/      # host / adapter / policy / tool / mcp / ui 分类插件(catalogue 为事实源)
├── extplugins/   # 外部进程插件入口
├── tui/          # bubbletea v2 界面
├── web/          # Web 服务端(SSE/WS + REST + 静态托管)
├── web-src/      # Vue 3 + Vite + TS 前端(产物内嵌)
├── desktop/      # Go + Wails v3 桌面壳
├── tests/        # 端到端与卸载矩阵
└── scripts/      # 构建 / 发行 / 门禁 / 校验
```

## 十、构建与发行

```bash
./scripts/gen.sh cli       # 本平台单二进制(TUI + Web + headless,前端已内嵌)
./scripts/gen.sh release   # goreleaser 六目标发行(tar.gz / zip + checksums)
./scripts/gen.sh desktop   # 桌面壳:sidecar → 壳构建 + 组装 .app / .app.zip(可选 dmg)
./scripts/gen.sh web-dev   # 前端热更开发(vite watch + GAH_WEB_STATIC)
```

- **门禁(CI 与本地同源)**:`bash scripts/gate.sh`(gofmt + vet + staticcheck,三 module)、全库 `go test -race`、`scripts/coverage-check.sh`(逐包棘轮 + 全局下限)、`scripts/size-check.sh`(单文件 ≤30 MiB / gz ≤15 MiB)、前端 `npm test` 与 `npm run test:layout`(真浏览器布局回归)。
- **发行**:打 `v*` tag 触发两条流水线(CLI 与桌面壳),共用同一个 GitHub Release 与 `desktop-latest.json`;Gitee 镜像由 `scripts/mirror-gitee.sh` 本机同步。
- `sdk/` 是独立 module(`go.work`),`go vet/test/staticcheck` 需在其中单独跑。

## 十一、协议

**MIT License**(见 [LICENSE](./LICENSE),© 2026 nekoleamo):可任意使用、修改、商用闭源分发。
