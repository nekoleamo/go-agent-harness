# go-agent-harness(gah)

Go 实现的编程代理 Agent Harness:以**单二进制**交付全部能力,对齐 DeepSeek Harness 与 Cordis 的「一切皆插件」设计哲学,微内核化(内核仅加载/卸载/依赖管理,零 Agent 能力、零 UI),TUI 为交互界面。

> 设计参考:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)。
> 完整设计见 [DESIGN.md](./DESIGN.md);插件开发见 [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md)。

---

## 一、项目目的

**排除 dsh 因 Node.js 带来的依赖**:

- 单一静态二进制(`CGO_ENABLED=0`,实测 **31.9MB**),无 node 运行时、无 node_modules 分发链;
- `scp` 一个文件到目标机即开箱可用,运行时依赖 = 0(仅系统库);
- 六目标交叉编译(darwin/linux/windows × amd64/arm64),裸环境 `env -i` 可直接启动。

## 二、功能特性

| 能力 | 说明 |
|---|---|
| **微内核** | `core/` 仅含 ctx 服务容器 / 5 分发 EventBus / 插件注册表(拓扑+热重载)/ 配置层(profile→bundle→patch);零业务 |
| **一切皆插件** | 会话日志、LLM 路由、工具流水线、沙箱、审批、Agent 循环、TUI、技能,全部是插件,可随时插拔开关(配置层 + 运行期 `/plugins`) |
| **TUI 交互** | bubbletea v2 + lipgloss:会话流视图(流式渲染)/ 工具调用面板 / 状态栏(沙箱档、模型)/ 命令托盘;非 TTY 自动降级文本 |
| **headless** | 无 UI 一次性运行器(`--input`),CI/容器友好 |
| **ReAct 循环** | 对齐 dsh 轮次:pre-step → llm/stream → tool/call* → turn/end,工具可替换 |
| **结构化工具** | MCP 兼容 schema;执行流水线 pre-execute(veto)→ execute → post-execute → result 广播;工具错误结构化回传模型 |
| **LLM 统一域模型** | 纯 HTTP+SSE 的 OpenAI 兼容适配器(DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp 通吃)+ Anthropic 适配器(模型前缀路由 `claude-*`);mock 适配器供 CI |
| **沙箱三档** | read-only / workspace-write(相对路径以 workspace 为根,防 `../` 穿越)/ full-access;TUI `/sandbox` 运行期切换 |
| **安全** | 危险命令(rm -rf / git push -f / sudo / chmod 777…)经用户 y/n 确认;无确认通道时安全拒绝;凭据隔离:工具子进程 env 滤除 `*_API_KEY/_TOKEN/_SECRET` |
| **starlark workflow** | 模型写受限 starlark 脚本一把过组合多步工具调用(天然沙箱/无标准库);`background` 异步 + 收集 |
| **外部插件桥** | host-bridge:独立进程插件(go-plugin),**崩溃隔离**(外部进程被杀,宿主存活,调用转结构化错误);M6.8 增宿主回调通道(GAH_CB_ADDR),外部进程可请求宿主 tools/jobs/fanout 服务 |
| **工具类全外部化** | shell/files/web(tool-basic)+ workflow(tool-workflow)+ MCP client(tool-mcp)全部独立进程;内嵌实现停用;脚本工具调用/背景任务/子代理编排经回调回宿主 |
| **MCP server serve 形态** | `gah --profile mcp-serve`:仅工具宿主 + mcp-server,可被外部 MCP client(Claude Desktop 等)独立拉起 |
| **MCP client 桥** | mcp-bridge:stdio JSON-RPC 接入 MCP server,工具注册为 `mcp_<name>` |
| **MCP server 端** | mcp-server:本仓全部工具暴露为 MCP server(stdio JSON-RPC,initialize/tools/list/tools/call,插拔实时反映) |
| **后台任务** | host-jobs + workflow `background`:长任务异步提交/取回/终止,不阻塞回合 |
| **子代理 fanout** | host-fanout 宿主服务(ctx.fanout):`agent/parallel/pipeline` 独立上下文 ReAct,扇出多个子代理并行执行并聚合;workflow 等任意入口可复用 |
| **会话 token 压缩** | token-compress 独立插件:超预算滚动摘要(完整日志留盘),长会话注入受限可用 |
| **插件安装** | `gah -install/-uninstall/-list-plugins`:git 拉取构建落盘,一条命令装完即启用 |
| **指令文件** | 全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md` 自动注入 system prompt(顺序即覆盖) |
| **技能机制** | host-skills:扫描技能目录(SKILL.md),`list_skills`/`read_skill` 按需加载,提示注入技能索引;仓库已自注册 `gah-plugin-dev` 技能 |
| **配置自愈** | 启动失败自动回滚最近正常备份重试一次,坏配置不卡死 |
| **pty 交互** | tool-shell `data.pty` 开关(creack/pty):驱动 REPL/git 编辑器等交互进程 |

## 三、快速开始

```bash
# 构建(发布形态)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.1.0" -o gah ./cmd/gah

# 交互模式(默认 tui profile)
./gah

# headless 一轮(dev/CI 用 mock 模型,免 key)
./gah --profile headless --input "帮我执行 echo hi"

# 查看合并后的配置树(任一条目可被自己 patch 替换)
./gah --profile dev --dump-config

# 版本
./gah --version
```

### 使用真实大模型

```bash
export DEEPSEEK_API_KEY=sk-...            # 或 OPENAI_API_KEY / GAH_BASE 自定义端点
./gah --profile headless --input "你好"    # 生产:自建 patch 启用 llm-openai-compat
```

或自建 profile:复制 `config/profile-headless.yaml` 为自定义配置,patch 中关闭 `llm-mock`、启用 `llm-openai-compat`(可配 `data.base_url/model`)。

### 快速新增任意提供商(TUI 内,零改配置零重启)

任意 OpenAI 兼容端点(DeepSeek/SiliconFlow/Ollama/vLLM/Kimi…)只需一行:

```
/provider set <baseUrl> <apiKey> [model]     # 立即生效并持久化(provider.yaml, 0600)
/provider show                               # 查看当前端点/模型/凭据(打码)
/provider unset base_url|api_key|model       # 逐项删除(该项回退 env/样板,其余保留)
/provider clear                              # 全部清除+运行时立即复位
```

示例(以 SiliconFlow 为例):

```
/provider set https://api.siliconflow.cn/v1 sk-<你的key> deepseek-ai/DeepSeek-V3
```

之后正常对话即可;换回环境变量配置用 `/provider clear`。

## 四、TUI 命令

| 命令 | 作用 |
|---|---|
| `/model <名>` | 切换模型(**动态枚举当前端点全部模型**,来源括号备注如 `(siliconflow)`;列表失败/无 key 回退手动输入) |
| `/thinking off\|low\|medium\|high` | 思考等级(推理预算);**快捷键 Shift+Tab 循环前进**(off→low→medium→high→off,从 off 开始);状态栏显示 `思维: <等级>`(off 隐藏) |
| `/sandbox ro\|ws\|full` | 运行期切沙箱档(状态栏实时显示) |
| `/plugins list\|on\|off <id>` | 运行期插拔插件 |
| `/settings history N\|off\|unlimited` | 会话历史注入条数 |
| `/export` | 会话事件序列 |
| `/help` `/exit` | 帮助 / 退出(**Ctrl+C 连按两次**,防误触) |

> **键位速记**:输入中单次 `Ctrl+C` 仅清空输入(不退出);输入为空时需**连按两次** `Ctrl+C`(2 秒窗口内)才彻底退出,第一次按下会高亮提示再按一次,超时或按其他键自动解除;`Esc` 取消进行中的回合。

## 五、配置与运行时目录

```
profile-<name>.yaml     # bundles + patches 按序声明
bundle-base.yaml        # 插件条目(id + enabled + data 参数)
patch-*.yaml            # 按 id 替换/插入/启停条目(随时插拔)

$GAH_HOME(缺省 ~/.gah)  # 全局指令 AGENTS.md / 全局技能 skills/ / 会话与备份
项目 .gah/skills/        # 项目技能
项目 AGENTS.md           # 项目指令(自动注入 prompt)
```

## 六、插件开发

遵循 [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md)(仓库内已自注册为 `gah-plugin-dev` 技能,运行 gah 时模型可按需读取):

1. `plugins/<type>-<name>/`,实现 `sdk.Plugin`(Start 注册副作用,返回 Disposer)
2. 登记 `plugins/catalogue`(provides/requires/bundle)
3. `config/bundle-base.yaml` 加条目(启停/参数)
4. 单测 + `-race` 全绿后提交

红线:插件**只 import `sdk/`**,不 import core/tui/其他插件;注册即副作用,卸载即撤销。

## 七、项目结构

```
├── cmd/gah/          # boot:profile 装配入口
├── core/             # 微内核:ctx/event/plugin/config
├── sdk/              # 插件唯一依赖的接口与域模型
├── bundles/          # bundle 装配(base/tui/register)
├── plugins/          # 全量插件:按类别子目录分组(host/adapter/policy/tool/mcp/ui,
│                     # 总览见 plugins/README.md;catalogue/ 为汇总事实源)
├── extplugins/       # 外部进程插件入口(tool-echo 示例 / tool-basic 三件套)
│                     # ——工具类保持外部化(embed 释放),内置二进制只承载 host/adapter/policy/ui
├── tui/              # bubbletea v2 界面(状态机可测;独立于插件包体系,ui-tui-app 薄壳引用)
├── tests/            # 端到端 + 迷你 MCP server
├── internal/         # install/embed/providerfile(发行/首启/运行配置)
├── config/           # profile/bundle/patch 样板
├── scripts/          # gen-extplugins.sh 等构建/发行脚本
├── .gah/skills/      # 自注册技能(gah-plugin-dev)
└── docs/ DESIGN.md AGENTS.md README.md
```

> 目录分组仅为可读性整理(M6.11):不改包名/接口/装配语义;外部化目标不受影响——`plugins/tool`、`plugins/mcp` 的内嵌实现默认停用,由 `host-bridge` 加载 `extplugins/`(与 `~/.gah/plugins/` 外部进程)。

## 八、发行

```bash
# 安装 goreleaser 后;出六目标包 + checksums
goreleaser release --snapshot
```

验收实测(DESIGN.md §7.6):静态单文件 31.9MB、六目标全绿、裸机启动、sha256 附档。

## 九、状态与路线图

- **已交付**:M1 微内核 → M2 base(ReAct/LLM/工具)→ M3 TUI → M4 生态(沙箱/审批/插拔/凭据)→ M5 桥 + starlark workflow → M5.5 MCP + 配置自愈 → M5.6 指令注入 + 技能机制 → 完善 A 组(会话持久化/重试/取消)+ B 组(apiVersion 校验/export/外部热重载/embed 交付);20 包 `-race` 全绿;交付门通过(单二进制自包含实测)。
- **M6 已全部交付**:host-jobs 后台任务(`ctx.jobs` + job_list/output/kill + `/jobs`)、workflow 子代理 fanout(agent/parallel/pipeline)、tool-shell pty 交互(`data.pty` 开关)、tool-files/tool-web(file_read/write/append/edit + web_fetch,经 `ctx.sandbox` 三档联动)、会话 token 滚动摘要压缩(`token_budget_chars`,完整日志留盘)、插件安装(`gah -install <repo>@version` / `-uninstall` / `-list-plugins`,manifest 见 DESIGN §14.1)。
- **M6.10–M6.12(已交付)**:会话 token 统计与上下文窗口动态解析(host-usage-stats;状态栏显示 `上下文 12.3K/64K (19%)` 与缓存命中率)、多会话切换(`/session switch|new|current`,切换后重放历史继续、重启恢复主会话)、plugins/ 目录分组与配置持久化修复(模型/base_url 启动恢复、TUI 启动显示实际配置、history 落盘 sidecar)。M6.13 工具调用纪律(正文伪调用标签提示修正,防空转兜底)。
- **M6.14 已交付**:联网搜索 web_search(plugins/tool/tool-web 同包扩展,免新插件;`{query, num_results 默认 5 上限 10}` → 标题/URL/摘要列表,需原文正文再调 web_fetch);默认 Exa 直连(`EXA_API_KEY`,外部进程全量 env 透传、模型不可见),`data.provider` 可换其它 provider;401/429/5xx 错误结构化归一;共享 http client(30s 超时/UA/1MB 上限);`-race` 全绿,主包 39.9MB(<40MB 体积门)。
- **规划中(待实施)**:M7 Web UI(含 UI 槽位插件化 M7.2、WebSocket 通道 M7.3)与工具类 M8 tool-todo / M9 tool-subagent / M10 tool-memory / M11 tool-auto-plan 均见 DESIGN §14.1,按需求逐个实施。
- **P0+P1 外部化(已交付)**:sdk 独立 module;桥协议多工具化 + 工具级超时 + 崩溃自动拉起;shell/files/web 合并为 tool-basic 随主包 embed,首启释放 `~/.gah/plugins/`,运行时全为外部进程插件(内置工具停用,host-bridge 默认启用;M6.9 扩展到 workflow/mcp 桥,M7 gzip 回归后单二进制 31.9MB < 40MB)。
- **P2 插拔解耦(已交付)**:集成矩阵验证插件卸载解耦——被依赖者拒卸(提示含依赖者)、叶子/无依赖者可卸且回合继续、卸载后调用已卸工具给可操作提示、LLM 适配器全卸后回合显式失败不静默;宿主服务类(skills/jobs/workflow)保持进程内(外部化需宿主服务桥,与微内核/veto 语义冲突,收益低)。
- **Anthropic 支持(已交付)**:llm-anthropic-compat(Messages API + SSE);模型前缀路由——`/model claude-*` 自动走 Anthropic(`ANTHROPIC_API_KEY`),非 claude 回落 OpenAI 兼容适配器;单测 + 路由单测 + 端到端回合验证。

## 十、协议

**MIT License**(见 [LICENSE](./LICENSE),© 2026 nekoleamo):宽松许可,允许任意使用/修改/商用闭源分发。
