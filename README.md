# go-agent-harness(gah)

Go 实现的编程代理 Agent Harness:以**单二进制**交付全部能力,对齐 DeepSeek Harness 与 Cordis 的「一切皆插件」设计哲学,微内核化(内核仅加载/卸载/依赖管理,零 Agent 能力、零 UI),TUI 为交互界面。

> 设计参考:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)。
> 完整设计见 [DESIGN.md](./DESIGN.md);插件开发见 [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md)。

---

## 一、项目目的

**排除 dsh 因 Node.js 带来的依赖**:

- 单一静态二进制(`CGO_ENABLED=0`,实测 **15.8MB**),无 node 运行时、无 node_modules 分发链;
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
| **LLM 统一域模型** | 纯 HTTP+SSE 的 OpenAI 兼容适配器(DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp 通吃)+ Anthropic 适配器计划;mock 适配器供 CI |
| **沙箱三档** | read-only / workspace-write(相对路径以 workspace 为根,防 `../` 穿越)/ full-access;TUI `/sandbox` 运行期切换 |
| **安全** | 危险命令(rm -rf / git push -f / sudo / chmod 777…)经用户 y/n 确认;无确认通道时安全拒绝;凭据隔离:工具子进程 env 滤除 `*_API_KEY/_TOKEN/_SECRET` |
| **starlark workflow** | 模型写受限 starlark 脚本一把过组合多步工具调用(天然沙箱/无标准库);`background` 异步 + 收集 |
| **外部插件桥** | host-bridge:独立进程插件(go-plugin),**崩溃隔离**(外部进程被杀,宿主存活,调用转结构化错误) |
| **MCP client 桥** | mcp-bridge:stdio JSON-RPC 接入 MCP server,工具注册为 `mcp_<name>` |
| **指令文件** | 全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md` 自动注入 system prompt(顺序即覆盖) |
| **技能机制** | host-skills:扫描技能目录(SKILL.md),`list_skills`/`read_skill` 按需加载,提示注入技能索引;仓库已自注册 `gah-plugin-dev` 技能 |
| **配置自愈** | 启动失败自动回滚最近正常备份重试一次,坏配置不卡死 |

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

## 四、TUI 命令

| 命令 | 作用 |
|---|---|
| `/model <名>` | 切换模型 |
| `/sandbox ro\|ws\|full` | 运行期切沙箱档(状态栏实时显示) |
| `/plugins list\|on\|off <id>` | 运行期插拔插件 |
| `/settings history N\|off\|unlimited` | 会话历史注入条数 |
| `/export` | 会话事件序列 |
| `/help` `/exit` | 帮助 / 退出(Ctrl+C 亦可) |

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
├── plugins/          # 16 个内置插件(host-*/tool-*/policy-*/llm-*/ui/mcp/skills)
├── extplugins/       # 外部进程插件示例(tool-echo,隔离验证)
├── tui/              # bubbletea v2 界面(状态机可测)
├── tests/            # 端到端 + 迷你 MCP server
├── config/           # profile/bundle/patch 样板
├── .gah/skills/      # 自注册技能
└── docs/ DESIGN.md AGENTS.md README.md
```

## 八、发行

```bash
# 安装 goreleaser 后;出六目标包 + checksums
goreleaser release --snapshot
```

验收实测(DESIGN.md §7.6):静态单文件 15.8MB、六目标全绿、裸机启动、sha256 附档。

## 九、状态与路线图

- **已交付**:M1 微内核 → M2 base(ReAct/LLM/工具)→ M3 TUI → M4 生态(沙箱/审批/插拔/凭据)→ M5 桥 + starlark workflow → M5.5 MCP + 配置自愈 → M5.6 指令注入 + 技能机制 → 完善 A 组(会话持久化/重试/取消)+ B 组(apiVersion 校验/export/外部热重载/embed 交付);20 包 `-race` 全绿;交付门通过(单二进制自包含实测)。
- **已交付 M6.1–M6.5**:host-jobs 后台任务(`ctx.jobs` + job_list/output/kill + `/jobs`)、workflow 子代理 fanout(agent/parallel/pipeline)、tool-shell pty 交互(`data.pty` 开关)、tool-files/tool-web(file_read/write/append/edit + web_fetch,经 `ctx.sandbox` 三档联动)、会话 token 滚动摘要压缩(`token_budget_chars`,完整日志留盘)。
- **规划中(M6)**:插件安装(`gah install` + 线上索引)——明细见 DESIGN.md §14.1。

## 十、协议

**MIT License**(见 [LICENSE](./LICENSE),© 2026 nekoleamo):宽松许可,允许任意使用/修改/商用闭源分发。
