# go-agent-harness(gah)

> [English](./README_EN.md) | [简体中文](#)(中英同步维护)

Go 实现的编程代理 Agent Harness:以**单静态二进制**交付全部能力,零运行时依赖。对齐 DeepSeek Harness 与 Cordis 的「一切皆插件」设计哲学——微内核仅负责插件的加载/卸载/依赖管理(零 Agent 能力、零 UI),全部能力以插件形式经配置层(profile→bundle→patch)随时插拔开关。

> 设计参考:DeepSeek Harness(TS/Cordis)、[naamfung/dsc](https://github.com/naamfung/dsc)(Go/go-plugin/gRPC)、[pi coding agent](https://www.npmjs.com/package/@earendil-works/pi-coding-agent)(TS 终端 harness)。
> - 完整设计:设计登记册(`DESIGN.md`)是**本地文档,不随仓库分发**;对外的交付总览见本文与提交历史
> - 插件开发:[docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md);插件总览:[plugins/README.md](./plugins/README.md)

---

## 一、项目定位

**排除 dsh 因 Node.js 带来的依赖**:

- 单一静态二进制(`CGO_ENABLED=0`,五目标实测 **41–46 MiB**,压缩下载产物 ≈27–31 MiB;外部插件经 zstd 压缩内嵌,体积门见 `scripts/size-check.sh`),无 node 运行时、无 node_modules 分发链、无版本管理器;
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
| **回合内转向** | 回合运行中按 `Enter` 把消息**注入当前回合**(模型下一次请求即可见,状态栏「转向 N(已注入本回合)」);注入不成自动转「待发 N」,`Alt+Up` 取回继续编辑;回合结束仍未注入的**原序回吐待发**(不丢话,会话流里留一行说明);命令(`/` 前缀)**永不转向** |
| **结构化工具** | MCP 兼容 JSON schema;执行流水线 pre-execute(veto)→ execute → post-execute → result 广播;错误结构化回传模型 |
| **LLM 统一域模型** | 纯 HTTP+SSE 的 OpenAI 兼容适配器(DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp 通吃)+ Anthropic 适配器(`claude-*` 前缀路由)+ mock 适配器(CI 免外网);多 provider 并存(`/provider`) |
| **沙箱三档** | read-only / workspace-write(防 `../` 穿越)/ full-access,TUI `/sandbox` 与 Web 设置面板运行期切换;**写路径统一裁决**:`file_*` 参数与 `shell` 命令的写目标(重定向、写命令、输出旗标 `-o/-O/-C/-t/--target/--prefix`、`git clone` 目标)都必须落在档位允许范围内(`shell` 越界写 / 含变量等不可裁决写目标直接拒绝);**工具路径参数四级收敛**:自述声明 → 内置名表 → 按 `inputSchema` 参数名与工具名动词推断 → 值级兜底(参数值一眼是路径就按工具名读写意图裁决,推断不明按写),未声明**不再**等于不裁决,确无路径参数的工具用 `PathParamsDeclared` 显式声明;代理调用(`mcp_call`)的内层参数按真实目标工具裁决 —— 审批通过 ≠ 放开档位,需显式切 full-access;**档位联动可见且可控**:审批档 `open`/`strict` 会覆盖沙箱有效档(`full-access`/`read-only`),`/sandbox`、`/approval`、TUI 状态栏与 Web 状态均回显「声明档 → 有效档(联动来源)」,不再静默失效;要「开着 open 但仍守住沙箱档」就关掉联动 —— **`/sandbox sync off`** 或 Web 设置面板「档位联动」勾选框(两者同一偏好,重启恢复;关掉后沙箱档独立生效、拦截行为跟着变);**内核级沙箱(第 3 组)**:macOS seatbelt / Linux Landlock 在**子进程树**层面兜住「写目标判不出来」的写(解释器内部写、`ccache`/`make` 包装器、`go install`、`curl -O` 等),并把**写目标表**继续补全(第 2 组:`sort -o`、`patch -o/-d`、`cargo --target-dir`、`npm --cache`、`pip --cache-dir/-d`、`gcc -MF/-MJ`、`go test -coverprofile/-trace`、`find -exec/-delete/-fprint`、`mktemp -p`、`split`、`tar czf`、`zip`/`7z`、`cmake --prefix`);**写入面由内核定义、协作层与之对齐**:可写落点只有一份定义(`kernelsandbox.WritablePaths`),协作层不再比内核更严地拒掉内核已经放行的落点(临时区、`$GAH_HOME/jail`、包管理器缓存)—— 这处不一致曾让模型在同一堵墙上反复换路径重试;按执行面分两份清单(shell 面把 `TMPDIR` 重定向进 jail,外部插件面额外放行缓存与系统临时区);**内核层不在场时**(平台无能力/开关关/jail 缺失)协作层退回「只在工作区根内」的窄口径 —— 那时它是唯一的边界;**Windows 仍只有协作层**(无等价无特权机制) |
| **审批三档** | 危险命令(rm -rf / git push -f / sudo / chmod 777…)按档:开放 open(放行)/ 智能 smart(弹确认,**等你答复到底 —— 默认不超时**,无确认通道时安全拒绝,默认)/ 严格 strict(拒绝);等待期间回合停在那里不继续后续步骤(要设上限就配 `policy-guard` 的 `confirm_timeout_sec`,到点按安全默认拒绝);偏好持久化 |
| **凭据隔离** | 工具子进程 env 滤除 `*_API_KEY/_TOKEN/_SECRET`;危险操作无确认通道时安全拒绝 |
| **会话页签(多会话并行)** | Web/桌面端一个窗口开多个会话,**各自同时跑**(每个会话独立日志 + per-session 回合串行锁,同一会话内仍严格串行):页签条(新建/关闭/运行脉冲点/未读圆点,上限 8)、**草稿与滚动位置跟着页签走**、刷新后恢复页签集合;侧栏点会话 = **在当前页签打开**(不做全局切换,全局切换仍归 TUI 与 `/session switch`);关掉正在跑的页签**回合在后台继续跑**(不说「已停止」);**诚实的边界**:后台页签只知道谁在跑、看不到第几步;草稿不跨刷新;跨工作区不开页签;桌面壳的「新会话窗口」多窗口仍保留,但多开会话以页签为主路径 |
| **流式回复与长会话** | 模型输出**逐字显现**(思维过程、正在调的工具、末尾一个呼吸光标),不再只有一句「正在运行…」硬等到说完;长会话(几百条消息)开窗**不卡死** —— 实测 800 条消息的首屏从 7.9 秒冻结降到 0.1 秒,看得见的部分先出、滚近了再补排版;**诚实的边界**:内存里的排版缓存只在本窗口内有效(整页刷新后重新拉一次);窗口上限 400 条,更早的内容上滚取回(数据不丢) |
| **会话级偏好** | 每个页签各用各的**角色 / 模型 / 思考档 / 沙箱档 / 审批档**（存在会话元数据里，切回历史会话、刷新、重开进程都还在）；没单独设过的项**跟随全局当前值**（页签上有徽标区分）；沙箱档可在会话级放宽到全权，但**角色 strict 仍然封顶**；审批 strict 时沙箱不会被放宽；fork/clone 继承全部偏好；插件启停、凭据、域名白名单**保持全局**（按会话改这些会让「这个页签怎么少了个工具」变成谜题） |
| **会话管理** | 项目级隔离 + 多会话切换 + 分支树(`/fork` `/clone` `/tree` + 命名);**重开沿用上次工作区**(初始目录是用户 home/数据根/系统目录时,自动切回最近打开的工作区;从项目目录里手工拉起则不动);超预算 token 滚动摘要压缩(token-compress,完整日志留盘);**模型端报上下文超窗时自动压缩后重试本回合**(溢出兜底:判定分层、只重试一次、提示通道留痕;仍失败则给 `/compact` 与人话出路);**缓存命中率诊断**(`/cache`:前缀指纹序列,指纹变了会指出「第几条消息开始不一样」—— 回答「命中率为什么低」靠它,不靠猜) |
| **模型工具** | shell / file_read-write-append-edit / web_fetch / web_search / workflow / subagent / todo / memory / auto_plan / session_search / schedule(默认停用) / job_* / 技能读取 / MCP 桥工具(详见「五、模型可用工具」) |
| **后台任务** | host-jobs + workflow `background`:长任务异步提交/取回/终止,不阻塞回合 |
| **并行隔离** | host-worktrees(`ctx.worktrees` + `/worktree`):受管 git worktree 落 `$GAH_HOME/worktrees/`(不污染用户仓库),子代理经 `subagent(isolate="worktree")` 在独立目录与分支内工作 —— 两个并行子代理改同一批文件互不覆盖;**隔离是强制的**:相对写/绝对写都进不了主工作区(沙箱写范围收窄到该 worktree);非 git 仓库显式报错不降级;worktree 默认保留,回收走 `/worktree rm`(分支保留) |
| **定时任务** | host-schedule(`ctx.schedule`):5 字段 cron 计划(分 时 日 月 周;日字段支持 `L` = 当月最后一天)落 `$GAH_HOME/schedules/*.yaml`,到点**经既有回合入口**(agentLoop→tools,仍受审批/沙箱裁决、仍落会话记录)自动跑一轮;设置面板「计划」段管理(**默认所有使用者不会 cron**:十二档时间控件「每天/每周/工作日/每月几号/每月第几个周几/每月最后一天/每月最后一个工作日/每小时/每 N 小时/每 N 分钟/每年某天/农历节日或农历某天/只跑一次」+ 常用预设 + 一句中文如「每周一三五 早上八点半」,**界面里不出现 cron 字样**,列表显示中文排期与接下来三次触发时间);**农历节日**(春节/除夕/元宵/端午/七夕/中元/中秋/重阳/腊八 —— 农历是**纯算法可离线算**(1900-2100 年表,已与算法实现跨源核对),但**法定调休放假不做**(需年度外部数据);除夕按「腊月最后一天」算,月大月小都不会漏);**从别处粘贴排期表达式**(折叠的高级入口,界面上唯一出现 cron 的地方 —— 能反解就提示改用选择器,反解不出会明确说「保存后只能停用或删除」);**一次性任务**(只跑一次、到点自动停用,`/schedule add once <YYYY-MM-DD> <HH:MM> <描述>`);**无人值守 = 没有确认通道 → 需审批的动作一律拒绝(含 open 档)** |
| **提示通道** | host-notices(`ctx.notices`,NOND-N1):插件/宿主向**人**发提示(作业终态/计划失败或跳过/回合出错),**不进会话记录、不计 token**;进程内环形缓冲(200)+ `id` 增量回填(`GET /api/notices?since=` / SSE `notice` 帧),Web 右下 toast、TUI 状态栏项与 `/notice`;同 `Key` 60s 去重防刷屏;系统级通知已交付(NOND-N2):TUI 按终端能力逐级降级发 OSC 99/777/9 或响铃(`GAH_TUI_NOTIFY=auto\|osc\|bell\|off`,`/notify test` 自测,写 `/dev/tty`),桌面壳改为**单条 2s 轮询消费提示流**(`warn`/`error` → 桌面通知),不再按场景各写一个轮询器;**macOS 上未签名/未公证的构建系统不呈现横幅**(系统收下不弹,属预期非缺陷:壳额外用 Dock 弹跳兜底,并在壳日志如实记录投递结果);应用内提示通道与状态栏不受影响 |
| **子代理 fanout** | `agent/parallel/pipeline` 独立上下文 ReAct 扇出并行聚合;`send_message`/`fork` 注入与会话派生 |
| **starlark workflow** | 模型写受限 starlark 脚本组合多步工具调用(天然沙箱/无标准库),`background` 异步 |
| **联网搜索** | web_search(**默认 AnySearch**:匿名即可用、国内直连,无需 key;Exa 保留为备选);结果自动剔除 DuckDuckGo/Google 这类境内不可达的「搜索结果页」;key 与端点可经 env 或 `$GAH_HOME/config/search.yaml`,端点可指向自建兼容服务;与 web_fetch 协作,错误结构化归一 |
| **MCP 双向** | client 桥(接外部 server,工具 `mcp_<server>_<name>`)与 server 端(对外暴露本仓全部工具,可被 Claude Desktop 等拉起) |
| **MCP 按需检索** | 每个 MCP server 可选 `mode`: `direct`(默认,工具全量进上下文)/ `search`(工具**不进每轮上下文**,只暴露 `mcp_search` 查清单 + `mcp_call` 按名调用);配置落 `$GAH_HOME/config/mcp.yaml`(设置面板「MCP server」段可视化增删改,**保存即写盘并热重载**,无需重启),env 照旧生效(文件优先) |
| **ACP agent 端** | `gah acp`:以 **ACP v1**(Agent Client Protocol,Linux Foundation)被编辑器(Zed / Neovim 等)当一等 agent 拉起 —— 会话、流式回复、工具进度、审批弹层全走 ACP,数据与 TUI/Web **同源**(同一会话账本、同一沙箱/审批裁决,不是另一套运行时);编辑器的权限按钮只映射 `allow_once`/`reject_once`(不给 always:一次点击不该永久放宽危险操作),交互式提问与图片输入暂不支持(显式报错,去 TUI/Web 作答) |
| **外部插件桥** | host-bridge:独立进程插件(go-plugin),崩溃隔离(外部进程被杀宿主存活);宿主回调通道(GAH_CB_ADDR)供外部进程请求 tools/jobs/fanout 服务;工具类 100% 外部化(extplugins/) |
| **插件安装** | 三个入口共用一份内核:CLI `gah -install <仓库|本地目录>[@版本]`、TUI 斜杠命令 `/install`(与 `/plugins` 分工:前者管安装与信任,后者管启停)、Web/桌面设置面板「插件」段的安装框(桌面端多一个原生「浏览…」)。**本地自己写的插件不用先 `git init` + `push`** —— 直接给目录即可。三个入口的确认文案来自同一份生成函数,审批档「严格」时一律拒绝(装插件 = 引入常驻执行代码)。**构建过程本身也被收口**:默认构建是 `GOFLAGS=-mod=readonly go build`(不改 go.mod/go.sum),**只有真的构建不出来时**才补跑一次 `go mod tidy` 并在面板与回执里标出来(那意味着这个插件引入了仓库原本没声明的模块依赖);构建子进程的环境经凭据清洗(拿不到你的 `*_API_KEY`/令牌),宿主的 `GOFLAGS`/`GO111MODULE` 等会改变构建语义的开关也被去掉**哈希白名单**:`plugins/SHA256SUMS` 一存在即强制 —— 未列入或哈希不符的插件**拒绝加载**(官方产物由 embed 每次启动自登记;装完自动登记;手工放置的用 `gah -trust-plugin <名>` 补登记)。登记行带 `# audit:` 注释(时间/来源:`embed` / `install:<spec>` / `trust:manual`)——**它不是安全边界**(能改插件目录的人也能改它),解决的是「事后说不清是谁、什么时候、从哪放进来的」。**UI 插件同样有闸**(`ui-plugins/SHA256SUMS`,合格线 = manifest + 槽位声明会被 `import()` 的模块,不是整目录 —— 改 sourcemap 不该被拒):存在即强制,未列入/不符则该插件**根本不下发**。被拒的插件在设置面板插件段**显示原因与补救命令**(不是默默消失)。**加载失败**(启动失败 / 握手被拒 / 协议不兼容)也进同一张清单,并用 `kind: load` 与完整性闸的 `kind: trust` 区分 —— 两者的补救完全不同:前者信任它也没用,要看 stderr 里的原因或重装;此前加载失败**只写日志**,面板上表现为「插件不见了」而无解释。插件协议版本由 `plugin.yaml` 的`api_version` 声明(缺省 = v1),不兼容在安装那一刻就拒 |
| **指令文件与技能** | 全局/项目 AGENTS.md 自动注入(近者覆盖;`/reload` 热更);SKILL.md 技能扫描 + 模型按需加载(`list_skills`/`read_skill`);仓库自注册 `gah-plugin-dev` 技能 |
| **角色切换** | `$GAH_HOME/roles/<id>/`(人设 + 工作规则 AGENTS.md + 私有技能 + **可选模型/思考档** + **可排除的工具清单** + **只能收紧的权限档 `approval`/`sandbox`**),身份槽注入在固定引导之后、指令层之前(冲突时用户/项目优先);技能按角色挂载过滤(未挂载的读不到也列不出)、**工具按角色排除清单过滤(被排除的看不见也调不动,子代理与工作流同口径)**、**权限档只能收紧(`role.yaml` 的 `approval`/`sandbox`,声明后实际生效取「声明档 → 审批联动 → 角色档」中更严者,三端回显标注「角色收紧」)**,`/role` 或**设置面板「角色」段**切换**即生效、不换会话**;**角色声明的模型/思考档每回合自动覆盖会话档**(三处回显来源,不静默改会话偏好;模型不可用时回退会话模型并告警一次,子代理同样继承角色的身份槽/技能与工具可见性/权限收紧档);**12 个预置角色,按场景分五组**(通用:通用助理;工程:编程大师/技术负责人/代码评审/运维值守;数据:财务/数据分析;写作:小说家/新闻撰稿人/技术文档/翻译;学习:技术导师)—— 面板按组分节显示,**从没挑过角色时会有一行提示**(选中后不再出现;不挑也能用,走基线:全局指令 + 全部技能);角色/工作规则/技能挂载/技能库(含角色私有技能)都可在面板里增删改(列表顶部有**合成的「默认（基线）」行**= 不启用任何角色,一键切回),TUI 状态栏与 Web 底栏都显示当前角色;模型只有只读 `list_roles`/`read_role`(人格不可被模型自改),且写 `roles/**`、`skills/**`、全局 AGENTS.md 走审批面;技能可**改名或改归属库**(共享库 ↔ 角色私有),改名会同步改掉每个挂载它的角色引用(不会留下已失效挂载);技能可**导出/导入为技能包**(`gah-skill-<名>.zip`,只含该技能的 `SKILL.md`;导入端**不执行任何脚本** —— gah 的技能本就是提示词文件,没有可执行载荷;角色私有技能随角色包走);角色可**导出/导入为单文件角色包**(`.zip`:定义 + 工作规则 + 私有技能,可分享与复现) |
| **主题外部化** | `$GAH_HOME/config/themes/*.yaml` + `/theme` 运行期切换,零重编译换肤 |
| **整体备份/恢复** | `/backup` 一键打包 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好)→ 确定性 tar.gz;`list|restore`,恢复前自动先备份当前态 |
| **配置自愈** | 启动失败自动回滚最近正常备份重试一次,坏配置不卡死 |
| **pty 交互** | tool-shell `data.pty` 开关:驱动 REPL / git 编辑器等交互进程 |
| **Web 附件+多模态** | 传图/文件入输入框或**拖到窗口任意位置**(按钮 + 拖放 + 粘贴),芯片预览/删除;图片经 openai/anthropic 适配器结构化注入(模型看图),文本附件路径引用;落盘 `$GAH_HOME/attachments/` |
| **文档预览** | 一个块模型 + 四端同源渲染(markdown/文本/代码/CSV/notebook + PDF 页事实):Web 预览工作台(文件树/PDF 原生查看器/HTML 源码视图+沙箱)、TUI `/preview` pager(滚动/搜索/横移)、`gah doc` CLI、会话流 markdown 渲染;路径经沙箱+逃逸校验+密钥 deny-list,零 v-html;旧二进制 Office(`.doc/.xls/.ppt`)走**可选**外部转换(`data.external_converters` / `gah doc --convert`,需本机 LibreOffice,缺省关,未装时显式提示不静默) |
| **优雅停机** | `POST /api/shutdown` → DisposeAll 全回收(Windows 无 SIGTERM 的统一停机通道;桌面壳/运维复用) |
| **桌面壳(GUI)** | `desktop/` Tauri v2 壳:sidecar gah + 窗口直连本地服务;**两种形态**(macOS/Windows 安装版、Windows 便携版 —— 对照表与升级/数据根/多实例策略见「桌面端(GUI)」小节;两形态身份独立、可同时运行);托盘(关于/检查更新/开机自启/**新会话窗口**/**新实例**)/ 通知 / 单实例 / 系统文件夹选择器(与设置面板状态同源:面板开着时托盘操作 1.5 秒内同步);**便携版的「检查更新」可点**(不自动更新,但查到新版会打开便携包下载页并按网络可达性选 Gitee/GitHub);**产物栏**(会话流上方常驻一条「产物 N 个文件 · 新建 M · +X −Y」,展开列出本轮落盘的文件、点开进文档面板预览、一键跳变更审查面;只列**已落盘**的改动 —— 被拒绝的写不进清单,它没写盘)、**多实例多开**(托盘「新实例…」或启动参数 `--new-instance` 再起一个壳进程;**默认仍是单实例**——第二次启动聚焦已有窗口。多实例共享数据根,故定时计划按租约只由一个实例触发、偏好读-改-写走跨进程文件锁、worktree id 带实例短码,避免"计划跑两遍/偏好丢更新/撞名")、**多会话并行视图**(侧栏在跑的会话有脉冲点与逐会话「■ 停止」;底栏显示本会话「第 N 步」,空闲时提示「另有 N 个会话在跑」—— 步数只对本窗口会话给,别的会话只知道在跑)、**多会话窗口与并行回合**(托盘「新会话窗口…」开一个绑定独立会话的窗口,`?session=<id>` 让每个窗口各看各的会话;多个会话的回合**可同时跑**(同会话内仍严格串行,保证会话日志单写者);向指定会话提交经 `/api/input {session}`;取消可按会话 `/api/control {cancel, session}`);**诊断**:壳日志 `<用户数据目录>/gah-shell.log` 同时收录壳侧事件、sidecar stderr(滤掉 go-plugin 的 `[DEBUG]`)、sidecar 文本 stdout、页面侧错误(前端经 `shell_log` 上报)、**启动自检的通道矩阵**(同步/异步/选择器/自启四条通道各探一次)与**任何线程的 panic**(`panic @ 文件:行`);**零成本发行**(updater ed25519 自持签名 + CI 矩阵 + 无签名首次启动指引);**侧栏会话导出走壳命令落盘**(HTML/jsonl 写入下载目录,网页自动打开,侧栏回执路径) |

## 三、快速开始

### 下载安装(非编程用户推荐)

从 [Releases](https://github.com/nekoleamo/go-agent-harness/releases/latest) 下载对应文件:

| 系统 | 下载文件 | 说明 |
|---|---|---|
| macOS(Apple 芯片) | `gah_<版本>_aarch64.dmg` | 打开后把 gah 拖进「应用程序」 |
| Windows(64 位) | `gah_<版本>_x64-setup.exe` | 双击安装(仅当前用户,不要求管理员) |
| Windows 便携版 | `gah_<版本>_x64-portable.zip` | **不安装**:解压到任意可写目录,双击 `gah-desktop.exe`(见下) |
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
>
> **便携版(Windows)例外**:便携包里同目录有 `portable.marker`,壳就**就地运行**、不外置,
> 数据在本目录的 `gah-data/`(换机器请整个目录一起拷)。它**不会自动更新**(Windows 不允许
> 覆盖正在运行的程序),但托盘里的「**检查更新…(便携版:打开下载页)**」**可以点**:查一次版本,
> 有新版就自动打开便携包下载页(国内走 Gitee 镜像、取不到时回落 GitHub),下载后你自己
> 解压覆盖即可。升级时**别删 `gah-data/`,也别删 `portable.marker`**(删了就退回安装版行为)。
> 安装版与便携版**可以同时运行、互不干扰**(两者是不同的产品身份,详见下节)。
> 命令行版:数据与 `gah` 同目录,升级只替换单文件,`gah-data/` 原地保留。
>
> **国内网络升级(GitHub 直连不稳时)**:升级只依赖两样东西——`latest.json` 与安装包,两者都能用「GitHub 加速代理前缀」就地换源:把原链接拼在代理前缀之后即可,例如 `https://gh-proxy.com/https://github.com/nekoleamo/go-agent-harness/releases/download/v0.3.0/gah_0.3.0_aarch64.dmg`(Windows 换成 `gah_0.3.0_x64-setup.exe`;CLI 归档同理)。**桌面端自动升级换源**:发布侧一条命令即可把 `latest.json` 里的下载 URL 整体前置代理前缀——`bash scripts/publish-desktop.sh rewrite-url https://gh-proxy.com/`(还原:`… rewrite-url none`),或在 GitHub Actions 里跑 `release-desktop` 的 `workflow_dispatch { tag, mirror_base }`,**不需重发版本、对已装版本立即生效**。**换源不降低安全性**:updater 的 ed25519 签名只对**文件内容**验签、URL 不参与 ⇒ 代理/镜像换包会被签名直接拒掉(脚本内含「签名逐平台未被改动」自检,不过即回滚)。公共代理是公益服务、可用性不保证:失效时换一个前缀或跑 `none` 还原直链;而自控镜像**已就绪** —— Gitee Release 附件随各版本同步上传(见下段自动选源),不再单靠公共代理。
>
> **升级源自动切换(0.1.6 起)**:桌面端检查更新时同时拿着两个源 —— Gitee 镜像(国内直连)与
> GitHub Release(全球可达),默认 Gitee 优先,取不到表就自动落到另一个;**某个源「表能取到但包下不下来」
> 时会被记住并垫到最后**,下次检查先试另一个源。两份 `latest.json` 各自把安装包地址指向自己的源,
> 所以选源同时决定了取表与下载两跳。签名始终只有一套(ed25519 只对**文件内容**验签,换源不降低强度)。
> 存量 0.1.5 用户走 GitHub 那份表(其中下载地址已指向加速前缀),因此国内也能升到 0.1.6。
> 手动下载(Gitee 镜像,**附件随各版本同步上传**;若某个 tag 下尚未出现,请用 GitHub Release 里的链接):`https://gitee.com/null_593_5354/go-agent-harness/releases/download/<tag>/gah_<版本>_aarch64.dmg`
> (Windows 换 `gah_<版本>_x64-setup.exe`);代码快照在 `https://gitee.com/null_593_5354/go-agent-harness`。
> Gitee 侧只放**最新代码快照**(无历史)与桌面安装包,完整历史与 CLI 各平台归档仍在 GitHub。
> 换回 GitHub 直链:`bash scripts/publish-desktop.sh rewrite-url none`(或 CI 里 `workflow_dispatch { tag, mirror_base: none }`)。
> **两点例外(唯一会丢配置的情况)**:① Windows 卸载页有个「Delete app data」勾选框,勾了会连
> `%LOCALAPPDATA%\dev.gah.desktop`(数据根所在)一起删 —— 默认**不勾**,想留着就别勾;
> ② Linux 只有命令行包(解压即用),数据在 `gah` 同目录 —— **把新版本解压覆盖到同一目录**就保留,
> 整个目录换掉就丢了。详细矩阵见本地设计登记册(`DESIGN.md`)R25。

### 桌面端(GUI):安装版与便携版

桌面壳(Tauri v2)只是一个窗口,真正干活的是旁边的 `gah` 进程(sidecar):壳负责托盘/通知/开机自启/
文件选择器,`gah` 负责会话、工具、沙箱与审批 —— 所以「壳」和「数据」是两回事,升级与卸载才敢分开做。

| | 安装版(macOS / Windows) | 便携版(仅 Windows) | 命令行版(任意平台) |
|---|---|---|---|
| 怎么用 | macOS 拖进「应用程序」;Windows 双击 `-setup.exe`(仅当前用户) | 解压 zip 到任意**可写**目录,双击 `gah-desktop.exe` | 解压 tar.gz,得到单个 `gah` |
| 判定标记 | 应用目录里有壳 | 同目录有 `portable.marker` | 无 |
| 运行文件在哪 | 外置到用户数据目录的 `bin/`(macOS `~/Library/Application Support/dev.gah.desktop/bin/`、Windows `%LOCALAPPDATA%\dev.gah.desktop\bin\`) | **就地运行**(不复制、不外置) | 目录里 |
| 数据在哪 | 上述 `bin/gah-data/` | 本目录的 `gah-data/` | 同目录的 `gah-data/` |
| 升级 | **自动更新**:托盘「检查更新…」→ 确认 → 下载 → 替换 → 自动重启(升级前额外备份一次) | **不自动更新**:「检查更新…」可点,查到新版**打开便携包下载页**,你解压覆盖即可 | 替换单个文件 |
| 卸载 | 删应用(**默认不删数据**;Windows 卸载页有个「Delete app data」勾选框,勾了会连数据一起删) | 删掉整个目录 | 删文件(`gah-data/` 留下或一并删由你) |

**安装版与便携版可以同时跑**。它们是两份独立的程序身份(不同的 bundle identifier),数据根也天然不同,
所以互不顶:先开哪个,另一个打开时就是自己的界面。(同一形态内重复双击则聚焦已有窗口 ——
「手滑双击」不会开出第二个实例;真要多开用托盘「新实例…」或启动参数 `--new-instance`。)

**重开后回到你上次干活的那个目录**:双击图标/开机自启拉起时进程拿到的初始目录是用户 home,
不是你的意图 —— gah 会识别这种情况(初始目录 = 用户 home / 数据根 / 系统目录)并**自动切回最近打开的
工作区**;你从某个项目目录里手工拉起时,那个目录就是意图,不会被改写。

**便携版只在首次启动时**提示一次「数据在本目录的 `gah-data/`、怎么升级」;之后不再打扰(想再看:
删掉 `gah-data/.portable-notice-shown` 这个空文件即可)。

**出问题先看日志**:桌面壳把自身事件、sidecar 的 stderr、页面侧错误、以及任何线程的 panic 都收进
一份文件 —— `<用户数据目录>/gah-shell.log`(托盘「关于 gah → 诊断」可直接打开)。便携版首次启动
若 sidecar 起不来(目录不可写等),窗口会直接显示失败原因与日志路径,而不是白屏。

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
./gah                                          # TUI(默认 tui profile;stdin 非 TTY 时**显式拒绝**并提示走 headless —— 不静默降级)
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
| `-version` | 输出版本信息(纯查询:管道/CI 里没 TTY 也能打,先于 TUI 护栏) |
| `-install <repo>[@version]` | 安装线上/桥插件(`mcp:<id>:<command>` 登记 MCP 插件)。`version` 可为 tag/branch 名,也可为 **40 位 commit sha**(`@<sha>` 装回指定的那一版) |
| `-accept-drift` | 配合 `-install`:接受**同名 tag 指向了新 commit**(默认**拒绝**,见下「来源固定与漂移守卫」) |
| `-prebuilt` | 配合 `-install`:下载作者发布的**预编译产物**,**不执行仓库里的构建脚本**(见下「预编译产物 `--prebuilt`」) |
| `-install-artifact <url>` + `-id <id>` + `-name <tool-xxx>` | 装一个**别人已经构建好**的插件产物 URL。**本机不需要 Go / node / 任何工具链**,也不执行任何构建命令。`-id` / `-name` 都会拼进落位路径,故做白名单式校验(见下「装一个别人构建好的产物」) |
| `-check-plugin-updates` | 检查已装插件的来源是否有更新。**只发 `git ls-remote`,不 clone、不安装** |
| `-uninstall <id>` / `-list-plugins` | 卸载 / 列出外部插件(`-list-plugins` 附带来源三件:仓库 · ref · commit) |
| `-trust-plugin <名>` / `-untrust-plugin <名>` / `-list-trusted-plugins` | 把已放在 `plugins/` 的二进制**登记进哈希白名单** / 撤销 / 列出。用于「插件被拒了但你确认来源可信」——它只登记你手上**当前这一份**的哈希;若清单里已有不同哈希会显式拒绝,不会自动洗白 |
| `/install` | TUI 斜杠命令:`list` / `check` / `enable <名>` / `disable <名>` / `prebuilt <仓库>` / `<仓库\|目录>[@版本或 commit]` / `uninstall <id>` / `trust <名>` / `untrust <名>`。与 `/plugins` 分工写进各自说明 |
| `-trust-ui-plugin <id>` / `-untrust-ui-plugin <id>` | 给**已经放在** `ui-plugins/` 的 UI 插件补登记 / 撤销。UI 侧完整性闸**默认强制**(boot 无条件创建),手工放置的插件默认不加载,这是它的放行口 |
| `-install-ui <repo\|本地目录>` / `-uninstall-ui <id>` / `-list-ui-plugins` | UI 插件安装 / 卸载 / 列出(`-install-ui` 会自动登记进闸) |

文档阅读子命令(web/im 同级入口,零装配纯读):

```bash
gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--max-input-bytes B] [--tree] [--depth N]
# 退出码:0 成功 / 2 用法 / 3 不支持格式 / 4 超预算 / 5 解析失败
# --convert(可选):本机装有 LibreOffice 时,把旧二进制 Office(.doc/.xls/.ppt)转 PDF 再抽取;
# 产物落 $GAH_HOME/cache/doc/(7 天保留),转换失败显式回退为「不支持」提示
```

> **想按量买一批好模型?** 设置面板 → Provider 里还有一个 **OpenCode Go** 预设(OpenCode 官方的订阅网关,$10/月,OpenAI 兼容,含 Grok/GLM/Kimi/DeepSeek 等一批模型)。它按**请求头**识别客户端,预设已经替你填好 `x-opencode-session: ${session}` —— session id 取当前会话(所以同一个会话稳定、不同会话不同,正是它要的语义)。
> **自定义请求头(高级)**:provider 表单里可逐行填 `键: 值`,支持 `${session}` / `${version}` / ${cwd}` 三个占位符,保存时展开。客户端 UA 一律自带 `gah/<版本>`(不伪装成通用 HTTP 库 —— 有些网关按 UA 路由或限流)。头值会**明文**存在 `provider.yaml`(与 api_key 同级、同样 0600),长期凭据请放 api_key 字段。

> **想先零成本试一下?** 设置面板 → Provider → **OpenRouter** 预设,粘一个 key(免费注册的即可)——默认模型直接给 **`openrouter/free`**:那是 OpenRouter 官方维护的「自动路由」id,每次请求替你挑一个当前可用的**免费**模型。想要具体某个,就在模型列表里挑名字带 `:free` 的那些(列表带徽标:免费 / 工具 / 看图 / 上下文档,以及一条「只看能当 agent 用的」过滤默认开着)。
> **三点边界,别当承诺读**:① 免费模型**有速率限制**,高峰会排队或 429;② 免费清单**天天在变**(实测一天内换了一半),写死的推荐名都会过期 —— 这也是我们不内置清单、只做动态枚举的原因;③ 免费模型的**质量与工具调用稳定性没有实测**(本机没有 key,匿名调用被 401 拒),让它改代码前建议把沙箱调成只读(`/sandbox ro`)或更严的审批档 —— 沙箱与审批是 agent 真正的那道边界,不取决于模型聪不聪明。

## 四、TUI 命令

> 命令注册进宿主 `ctx.commands`,TUI/Web/headless `/` 前缀通用;输入 `/` 弹出命令提示(名称+说明,可继续输入过滤)。**所有命令支持逐级确认**:参数按声明级联(子命令枚举 → 动态候选,如 provider/会话/插件/任务/备份/主题/群列表;需手输的走自由参数断点),TUI 用选择器、Web 用同一注册表声明的候选列表(点击逐级选)。TUI 专属命令(search/widgets/statusline/traj/theme/help/exit/fork/clone/tree/name)仅 TUI 可用。

| 命令 | 作用 |
|---|---|
| `/model <名>` | 切换模型(**动态枚举当前端点全部模型**,来源括号备注如 `(siliconflow)`;选中自动切所属 provider;列表失败/无 key 回退手动输入)。**当前角色声明了 model 时**,本条只改会话档并显式提示「实际按角色跑」 |
| `/thinking off\|low\|medium\|high` | 思考等级(推理预算);**快捷键 Shift+Tab 循环前进**;状态栏显示 `思维: <等级>`(off 隐藏)。**当前角色声明了思考档时**同样只改会话档并显式提示 |
| `/sandbox ro\|ws\|full` | 运行期切沙箱档(只读/工作区写入/完全访问;状态栏实时显示,偏好持久化重启恢复)。**当前角色声明了收紧档时**,本条只改全局档并显式提示「角色收紧生效、该设置暂不生效」 |
| `/sandbox sync [on\|off]` | 审批档→沙箱有效档 的**联动开关**(无参=回显开关与当前有效档):`off` 后沙箱档独立生效,不再被 `open`/`strict` 覆盖(例:审批 `open` 想少弹确认、又不想放开越界写);用户选择持久化(`gah-state.json`),重启与无人值守场景都按它恢复;沙箱实现没有该能力时显式报不支持 |
| `/approval open\|smart\|strict` | 运行期切审批档(开放=危险命令直接放行 / 智能=命中弹确认(默认)/ 严格=直接拒绝;偏好持久化重启恢复)。**当前角色声明了收紧档时**同样只改全局档并如实回显(有效档 = 更严者) |
| `/provider show\|add\|use\|set\|unset\|remove\|clear` | 配置 LLM 提供商(多 provider 并存):`show` 列出(活跃★凭据打码)/ `add 端点 key [model]` 新增(首个自动活跃)/ `use <名>` 切换 / `set 端点 key [model]` 编辑活跃 / `unset 字段` 逐项删(回退 env/样板)/ `remove <名>` 删除单条(删活跃顺延到下一条)/ `clear` 全清并复位 |
| `/plugins list\|on\|off <id>` | 运行期插拔插件(`on/off` 持久化开关,重启仍生效;`default` 恢复配置树默认) |
| `/settings history N\|off\|unlimited` | 会话历史注入条数(`off` 禁止 / `unlimited` 全部 / N 最近 N 条;全局偏好跨会话) |
| `/compact [指示词]` | 手动滚动摘要压缩(立即折叠旧历史;自动超预算压缩不变;指示词仅记录);端点报上下文超窗时会**自动**压缩后重试本回合(溢出兜底,只重试一次),无需手动 |
| `/cache [n]` | 缓存命中率诊断(只读):累计命中率 + 最近 n 次请求的**前缀指纹**。指纹变了会指名「第几条消息开始不一样」—— 命中率先低可能是前缀被改写(改代码能修),也可能前缀太短/厂商缓存过期(改代码没用),这条命令就是把两者分开;全程不发模型请求、不写盘 |
| `/export [path]` | 导出当前会话事件序列(`.html` 结尾 = 自包含 HTML 渲染,否则 jsonl;**导出成功后默认自动用系统默认程序打开**,`GAH_EXPORT_OPEN=0` 关闭);Web/桌面端等价入口 = 侧栏会话行 `⤓` 菜单(桌面端落盘到下载目录) |
| `/role-pack export <标识> [路径]` · `/role-pack import <包路径> [--as 名] [--overwrite]` | 角色包(定义 + 工作规则 + 私有技能,单个 zip):TUI/headless 侧的分享入口(与 Web 的 `/api/rolepack` 同一实现;同名默认拒,`--overwrite` 才覆盖且旧份进回收站) |
| `/skill-export <技能名> [路径]` · `/skill-import <包路径> [--as 名] [--overwrite]` | 技能包(借鉴 5):`/skill-export` 把**共享技能**导成 `.zip`(只有它的 `SKILL.md`,缺省落当前工作区,给目录则拼 `gah-skill-<名>.zip`);`/skill-import` 导入(`--as` 改名会同步改写正文 frontmatter,**同名缺省拒**,`--overwrite` 才覆盖且旧份进回收站)。角色私有技能随**角色包**走 |
| `/workspace [目录]` | 切换工作区(项目):最近列表选择或输新目录;切换即开新会话、工具进程 cwd 真实切换、沙箱 root 同步 |
| `/session list\|switch\|new\|current` | 会话管理:列出(★ = 置顶,带概述)/ 切换(二级选择器带内容预览与时间)/ 新建(空历史)/ 查看当前 |
| `/session pin\|unpin [id]` | 置顶 / 取消置顶会话(缺 id = 当前;置顶区排在列表最前,上限 8) |
| `/session summary [id]` | 生成/查看会话概述(LLM 总结:一句话 + 主题词;**会调用模型**) |
| `/reload` | 热重载指令文件(AGENTS.md 层级/全局/附加)与角色定义(身份句/工作规则/技能挂载;外部编辑即生效,免重启) |
| `/role list\|show [id]\|use <id>\|none\|new <id>\|rename <id> <新 id>\|rm <id>\|export <id> [路径]\|import <路径> [as <新 ID>] [force]` | 角色管理:列出(★当前)/ 查看某角色(身份句+工作规则+技能)/ 切换(下一回合生效,**不换会话**)/ 退出角色 / 新建(带模板 AGENTS.md)/ 改 id / 删除(移入 `roles/.trash/`,当前角色拒绝)/ **导出为单文件角色包**(`gah-role-<id>.zip`:定义 + 工作规则 + 私有技能,`role.yaml` 原样搬运,不给路径则落在当前目录)/ **导入角色包**(默认**同名拒绝**,`force` 才覆盖且旧份先移入 `roles/.trash/`,**当前角色一律拒绝覆盖**,`as <新 ID>` 可与现有角色并存;包里出现本版本不认识的字段会被**显式拒绝**而不是静默丢掉)。模型侧只有只读 `list_roles`/`read_role`,写操作只走此命令与 Web 面板 |
| `/context [all]` | 上下文占用分解:**真实** token(累计输入/输出/缓存命中/窗口占用条)与**本地估算**分段(固定引导/全局与项目指令/附加指令/各片段/工具名清单 + 工具 schema JSON 开销/会话投影历史)分列,口径显式标注;`all` 再逐项列出每个工具 schema 的字节与粗估 token(**纯本地,不发模型请求**) |
| `/diff [路径]` | 变更审查面:无参 = 本会话改过的文件清单(+/− 行数,按路径聚合);有参 = 该文件本次会话的逐行 diff(TUI 弹 pager / Web 切到变更视图)。数据来自工具写盘时捕获的前后内容,**不依赖 git**(工作区不是仓库、还有未提交改动都不影响口径);超 32 KiB / 二进制 / 超大输入三种情况显式标注降级 |
| `/recap` | 会话速览(轮数/工具 Top 与失败数/涉及文件/最近一问一答/模型/跨度/**角色经历**——切角色不换会话,这里摊出“本会话经历过哪几个角色”;**纯本地统计,不调模型**) |
| `/answer [编号\|内容\|skip]` | 作答结构化提问(TUI):无参=回到作答态;多问并存按到达顺序逐个答;`skip` 跳过;`Esc` 退出作答态(提问与草稿都保留) |
| `/jobs [list]\|output <id>\|kill <id>` | 后台任务**与子代理**统一视图:无参=list(运行中优先,带耗时/摘要)/ 取输出 / 终止(Kill 需 running;与 workflow `background`、Web 任务面板同源;TUI 状态栏另有常驻折叠行「后台 N 运行中」;TUI 按 **F6** 展开实时坞) |
| `/worktree [list]\|rm <id> [force]` | 受管 worktree(隔离子代理的工作目录):无参=list(带分支/基线/路径 + 回收提示)/ `rm` 回收目录(**分支保留** —— 未合并的改动仍在分支上;有未跟踪改动时非 `force` 拒绝,防静默丢活) |
| `/schedule [list]\|add <cron> <描述>\|add once <YYYY-MM-DD> <HH:MM> <描述>\|rm\|on\|off\|run <id>` | 定时任务:列出 / 新建(5 字段 cron,如 `0 8 * * *` = 每天 8 点;`L` = 当月最后一天、`LW` = 当月最后一个工作日)/ 新建一次性 / 删除 / 启停 / 立即跑一次(与设置面板「计划」段同源 —— 那里用时间控件,不用手写表达式) |
| `/backup [dest]\|list\|restore <name>` | 整体备份 GAH_HOME(config 含密钥/plugins/sessions/env.sh/偏好,排除 backups/ 自身):无参=立即备份(默认存 `$GAH_HOME/backups/`,可指定外部路径)/ `list` 列出(时间倒序)/ `restore <name>` 恢复(**恢复前自动先备份当前态**,重启后完全生效) |
| `/preview <路径>` | 文档预览工作台:TUI 打开全屏 pager(↑↓/PgUp/PgDn 滚动、←→ 横移、`/` 搜索 n/N 跳转、q/Esc 关闭);Web 打开文档面板并定位该文件(markdown/文本/代码/CSV/notebook/docx/xlsx/pptx/PDF) |
| `/search <词>` | 会话内搜索(命中高亮,n/N/F3 循环跳转,Esc 退出) |
| `/theme [名]` | 切换主题(枚举 `$GAH_HOME/config/themes/*.yaml`;`default` 回默认;零重编译换肤,偏好持久化) |
| `/fork [seq]` | 从历史任意点派生分支会话(`/tree` 查看 seq;缺省=最近提问) |
| `/clone` | 复制当前会话(同一分支另一路演进) |
| `/tree` | 会话分支树(会话全貌 + 可 fork 的提问点) |
| `/name <显示名>` | 给当前会话加显示名(`-` 清除;状态栏/切换列表名优先) |
| `/widgets on\|off` | 输入区上方 widget 区开关(宿主注册的动态信息行) |
| `/statusline [项...]\|reset` | 状态栏项集合与顺序(TUI):无参=查看当前与可用项(`state/queue/questions/dock/notice/last/workspace/sandbox/approval/role/session`);给项即按序渲染(回合态项之间 `·`、其余 `|`),`reset` 恢复 F15.3 基线;偏好持久化(`gah-state.json`)重启生效 |
| `/traj` | 轨迹/可观测视图(TUI):以文本浮层呈现同一份会话事件账本的**回合 → 步 → 工具**投影(概览:回合数/总时长/累计 token 与缓存占比;逐回合倒序:时长与结束原因、步数与工具数、失败/未回填计数、token、模型名;工具行:状态/耗时/出参体积/错误原文/参数摘要)。只呈现过程与成本,不铺出参与正文;**时长只取事件时间戳,进行中的回合/步/工具一律显「进行中」**(概览改显「计算中」),不拿本地时钟补齐;浮层复用文本 pager(滚动/横移/搜索/`q` 关闭),Web 侧对应轨迹视图按钮 |
| `/notice` | 查看最近的提示(TUI,NOND-N1):以浮层列出进程内提示缓冲(最新在前:级别/时刻/来源 + 标题 + 正文),并显示缓冲 gap 与去重计数;未装配提示通道时显式报错 |
| `/notify [test\|auto\|osc\|bell\|off]` | 系统级通知开关与自测(TUI,NOND-N2):无参=回显**当前会怎么发**(落点与原因)与用法;`test` 发一条测试通知;`auto` 按终端能力探测(kitty OSC 99 / WezTerm·VTE OSC 777 / iTerm2 等 OSC 9 / 响铃 / 未附着终端则只留状态栏)、`osc` 强制转义、`bell` 只响铃、`off` 关闭(零输出);`GAH_TUI_NOTIFY` 是持久开关(未设或写错值回落 `auto`)。**只有 `warn`/`error` 会打扰人**,`info` 只更新状态栏 |
| `/help` `/exit` | 帮助 / 退出(**Ctrl+C 连按两次**,防误触) |

> **提示通道(`ctx.notices`,NOND-N1)**:插件与宿主可向**人**发一条提示(后台作业终态、定时任务失败/跳过、回合出错),不依赖盯屏。**提示不是会话内容** —— 不落 `jsonl`、不进模型上下文、不计 token,而是进程内环形缓冲(200 条)+ `id` 增量回填(`GET /api/notices?since=`;SSE `notice` 帧),所以重连/刷新不丢已发提示。三端各自决定呈现强度:Web 右下 toast(`info` 6s 自动消失、`warn`/`error` 常驻待关,超出可见上限显式计数)、TUI 状态栏 `notice` 项(无提示时零占位,逐字符不改变基线)+ `/notice` 浮层看详情;去重限频为同 `Key` 60s 内只出一条,避免重试风暴刷屏。**系统级通知(NOND-N2,已交付)**:Web 之外的两端可在人不在窗口前时把人叫回来 —— TUI 按**终端能力逐级降级**发 OSC 99/777/9 或响铃(探测而非写死支持矩阵:公开矩阵里 Windows Terminal / VTE 的结论互相矛盾,写死等于静默失效),写 `/dev/tty` 而非 stdout(否则重定向/被 hook 捕获时不算终端),未附着终端时静默降级为「仅状态栏」并在 `/notify` 里**如实回显原因**;正文按不可信输入处理(剥控制符,防提前终止转义序列),kitty 分块、tmux/screen 用 DCS 包裹。桌面壳从「每个场景一个轮询器」收敛为**单条 2s 轮询消费提示流**(判据只留在宿主一处,新类别自动进来),仅 `warn`/`error` 弹桌面通知。**真机核对**:本机已覆盖 kitty / Terminal.app / tmux 三种落点,iTerm2·GNOME VTE·WezTerm·Windows Terminal 待对应真机各跑一次 `/notify test`。

> **`!` shell 直通**:输入以 `!` 开头(如 `!git status`)= 立刻执行该 shell 命令(**走与模型工具同一条沙箱/审批管线**,不存在手敲免检旁路),输出就地回显、长命令可 `Esc` 中断;结果**只本地留痕,不进模型上下文**(避免孤立 tool 消息破坏投影)。需要模型看到输出时请让模型调用工具。
>
> **键位速记**:输入中单次 `Ctrl+C` 仅清空输入(不退出);输入为空时需**连按两次** `Ctrl+C`(2 秒窗口内)才彻底退出,第一次按下会高亮提示再按一次,超时或按其他键自动解除;`Esc` 取消进行中的回合;`Enter`(回合运行中)把消息**注入当前回合**(状态栏「转向 N」,模型下一次请求即可见;注入不成则排队为「待发 N」,回合结束后续发);`Shift+Tab` 循环思考等级;`Ctrl+T` 折叠/展开思维块;`Ctrl+O` 折叠最近工具结果;`Ctrl+↑/↓` 跳到最早用户行/回底;`Ctrl+A` 全选(删除=清空/输入=替换)、`Ctrl+B/F` 左右移动、`Ctrl+Y` redo、`Alt+P` yank 粘贴、`Alt+←/→` 按词移动;`F6` 展开/收起后台坞(↑/↓ 选择、`Enter` 看输出、`s` 定向、`x` 停止需二次确认、`Esc` 收起)。

## 五、模型可用工具(由模型调用,无需交互)

| 工具 | 说明 |
|---|---|
| `shell` | 执行 shell 命令(沙箱/审批策略拦截;写目标经路径裁决:workspace-write 下越界写被拒、只读档拒绝一切写;`data.pty` 可驱动交互式进程;凭据 env 滤除;**环境 jail**:`TMPDIR`/`XDG_CACHE_HOME`/`GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` 恒重定向到 `$GAH_HOME/jail/**`,`HOME`/`GOPATH` 等不动,`GAH_SHELL_JAIL=0` 可关;**内核级沙箱**(第 3 组):宿主把**有效**档位下发到执行入口,`shell` 在**进程树**层面限制文件写(读侧另拒凭据目录,见 `GAH_SHELL_CRED_READ_KERNEL`)—— macOS `/usr/bin/sandbox-exec`(seatbelt)、Linux **Landlock**(内核 ≥5.13,自举 helper 重新 exec 后施加);白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**` + 必要设备节点(路径先解析软链),read-only 档仍保留 jail 可写;无能力平台(Windows 等)→ 一次性 stderr 告警 + 不施加包装,`GAH_SHELL_KERNEL_SANDBOX=0` 可关;**POSIX shell 解析**(`sdk/shellpath.go`):`GAH_SHELL_PATH` > Windows 上的 Git for Windows 常见安装位/PATH → `sh`,后台任务同源,缺失时显式报错不静默降级;Windows 下关闭 MSYS 参数路径转换(`MSYS_NO_PATHCONV`),防裁决路径与实际落点脱节) |
| `file_read` / `file_write` / `file_append` / `file_edit` | 文件读写/追加/精确编辑(经沙箱路径校验) |
| **会话流思考块** | 推理模型的**思维过程与最终答复分区块呈现**:思维独立成块(左侧竖线 + 淡底 + 弱色斜体,默认折叠并标字数),正文保持正常前景色。此前 Web 侧把 `Thinking` 增量**整个丢弃** —— 思考一条都不显示,用户看到的是一段没有铺垫的结论 |
| `web_fetch` / `web_search` | 抓取 URL 正文 / 联网搜索(默认 **AnySearch**,匿名可用;provider 写 `anysearch`/`exa`);key/端点经 env(`GAH_SEARCH_API_KEY`/`ANYSEARCH_API_KEY`/`EXA_API_KEY`/`GAH_SEARCH_PROVIDER`/`GAH_SEARCH_ENDPOINT`)或 `$GAH_HOME/config/search.yaml`,**env 优先**;端点可指向自建兼容服务;401/402/429/5xx 结构化错误) |
| `workflow` / `workflow_collect` | 受限 starlark 脚本组合多步工具调用(天然沙箱);`background` 异步 + 收集 |
| `job_list` / `job_output` / `job_kill` | 后台任务查询/取输出/终止(与 `/jobs` 同源) |
| `memory` | 跨会话记忆(remember/list/recall/forget;`$GAH_HOME/memory/<project>.jsonl`,人工可编辑) |
| `todo` | 任务清单(create/start/complete/pend/delete/update/list;4 状态机 + blockedBy 依赖;`$GAH_HOME/todos/`) |
| `auto_plan` | 规划模式(create/get/list/step/confirm/complete;检测规划意图先输出结构化规划,确认前零副作用工具调用;`$GAH_HOME/plans/`) |
| `session_search` | 跨会话检索(查历史会话账本:`{query, limit, scope?}` → 命中片段 + 会话 id/时间;默认只搜**当前工作区**,`scope="all"` 才跨项目;纯只读、无索引文件、命中是**历史记录**非当前事实) |
| `schedule` | 定时计划管理(list/add/update/remove/run;经 `ctx.schedule` 委托 host-schedule,与 `/schedule` 同源)。触发时**无人值守 = 没有确认通道**,故计划里需审批的动作会被直接拒绝;`update` 为部分更新(只改给到的字段)。**该工具默认停用**(`bundle-base.yaml` 里 `tool-schedule` 条目 `enabled: false`),要用需先在配置层打开 |
| `subagent` | 子代理委派(delegate/spawn/agents/agent_status/agent_kill/send_message/fork;独立上下文 ReAct,后台带句柄);`isolate="worktree"`(可配 delegate/spawn/fork)= **隔离运行**:子代理在受管 git worktree 内工作,改动只落该目录、不进主工作区,回包含 worktree 路径/分支(父级据此合并或回收);非 git 仓库/未启用 host-worktrees 显式报错(不静默退化为非隔离) |
| `list_skills` / `read_skill` | 技能索引 / 按需加载 SKILL.md(项目 `.gah/skills/`、`$GAH_HOME/skills/`) |
| `list_roles` / `read_role` | 角色清单与详情(当前角色、身份句、工作规则、已挂载技能;`$GAH_HOME/roles/<id>/`)—— **只读**:模型不可增删改角色(写走 `/role` 与 Web 面板) |
| `mcp_<server>_<工具>` | MCP 桥工具(`mode: direct`,见「MCP 接入」) |
| `mcp_search` / `mcp_call` | MCP 检索模式代理工具(`mode: search`):按关键词查工具清单(空查询 = 全量),再按名调用 |

## 六、Web 使用(设置面板/REST)

`gah web` 起 http://127.0.0.1:2233(自动开浏览器;`GAH_WEB_OPEN=0` 关闭)。功能与 TUI 对等,另有可视化面板:

**鉴权(token 模式 = `data.auth_token` 非空)**:全表面鉴权——接口、静态资源、`/attachments/`(附件)、`/ui-plugins/` 一律需凭据(此前只有 `/api/*` 受保护)。浏览器请用启动日志里的 `#token=<token>` 地址打开:凭据在 **URL fragment** 中(不发往服务端,不进访问日志/Referer),引导页经 `POST /api/auth` 换取 HttpOnly + SameSite=Strict 的 `gah_token` cookie 后转入 UI;桌面壳经 `GAH_WEB_TOKEN` 传同一 token(`?token=` 已废弃)。

- **设置面板**(状态栏 ⚙,**左栏分区导航**:模型/推理/指令/角色/会话历史/Provider/数据备份/计划/MCP server/插件/关于 gah 与插件注入段逐项可点即达、当前段高亮,长面板不再需要从头滚):模型下拉(聚合全部 provider;每条带**能力徽标**(免费 / 工具 / 看图 / 上下文档)与一条「**只看能当 agent 用的**」过滤(**默认开**)——没有工具调用能力的模型在 gah 里只能聊天、**工具不会执行**,这类条目不藏起来(藏了等于让用户以为不存在)、沉到末尾并在标签后标出原因;徽标与可用性由**后端统一判定**,TUI 与 Web 同一口径,前端不重复判一次)、思考/沙箱/**审批**分段控件、历史注入下拉 + 压缩按钮、**「角色」段**(角色列表 / 工作规则 / 技能挂载 / 技能库 / 回收站,以及角色可携带的**模型与思考档**、**只能收紧的权限档(审批/沙箱)** —— 声明了就回显为“每回合生效”,收紧档还会在沙箱/审批段说明「当前角色收紧为 X」),Provider 管理(启用/删除/新增;**首启引导**:一个 provider 都没有时自动打开面板并给 DeepSeek/Kimi/智谱/千问/硅基流动/OpenRouter/OpenAI/Ollama **一键预设**,只需粘 api_key,保存后自动**连通性自检**并把 401/404/DNS 等端点错误翻译成人话与原始报错并列)、**定时计划**(「计划」段:**不会 cron 也能建** —— 重复方式下拉(每天/每周(周几多选)/每个工作日/每月几号/每月第几个周几/每月最后一天/每小时/每 N 小时/每 N 分钟/只跑一次)+ 时刻选择 + **常用预设**(每天下班前/每周一早会前…)+ **一句话直述**(「每天早上8点」→ 填好控件让你确认,看不懂就回退到控件,不会报错卡住),**保存前给出接下来三次触发时间**,列表显示中文排期而非表达式,支持编辑/启停/立即运行/删除;**农历节日**(中秋/春节/除夕…)与**每年某天**都有专门档位(农历计划不存表达式 —— 公历上每年都在变,预览给出的正是它实际会跑的那三天);一句话直述还认「月底前」「月初」「月中」这类区间说法(**收敛成确定日期并明说按什么理解**,不反问你,也不静默猜)、「每周1,3,5」数字写法、节日名(「中秋」「除夕」);高频档位明说代价(如「每 30 分钟 = 一天约 48 次」),每月 29–31 号明说会跳月;无人值守语义在段内明示)、**全局指令**(「指令」段:直接编辑 `$GAH_HOME/AGENTS.md` —— 这一份对**所有**角色生效;字节计数 + 32 KiB 上限拒写 + 保存二次确认,保存后服务端自动重载指令,重载失败会如实提示「文件已写入但未生效」;手改出的超限文件仍可读可看,注入时按上限截断并标注「已截断」),**MCP server**(「MCP server」段:逐项名称/启动命令/启停/模式(全量注册或按需检索)、环境变量来源只读对照、逐行「已加载 N 个工具」状态、**保存并重载**即时生效)、**插件**(「插件」段:默认只列前 5 条(≤5 条时全列)——超过时可筛选 ID/类型/状态并「展开全部」,指令重载同段)、**数据备份**(立即备份 / 恢复备份——**二次确认**);**桌面版底部固定条**(版本 + 检查更新,不随内容滚动;**检查只查不装** —— 发现新版本后底栏才多一个「升级到 X」按钮,点它并**二次确认**后才下载安装并重启;托盘「检查更新…」同样先弹确认框,默认焦点在「稍后」——升级会换掉整个应用本体,不能被一次回车顺手带过去);全部设置退出即记(偏好持久化,gah-state.json 与 TUI 共享)。面板里的可编辑内容(角色工作规则、技能 `SKILL.md` 原文、全局指令、**MCP server 配置**)有**未保存**标记 —— 切换目标前会问一声(且确认文案只列**这次真会被丢弃**的那几项),清草稿只有「保存」与「放弃修改」两个出口,不会在你换角色/换技能/重拉配置时被静默覆盖;角色**改标识不会收起编辑区**(改名不是换角色)。角色定义里的即时提交(挂载勾选、并入默认池、排除全局指令、模型/思考档)会**按顺序逐个提交**、提交期间相关控件置灰并显示「提交中…」—— 连点两次不会互相覆盖(挂载清单与工具排除都是整份替换,旧实现会丢改动)。角色**工具排除**同样即时提交:勾上即排除、勾掉即恢复,「已排除 N 项」常驻显示。角色段还可**导出角色包**(浏览器点「导出」直接下载 zip;桌面壳改走原生「选择保存目录」再交服务端写文件 —— WebView 没有下载通道,`<a download>` 在壳里静默无反应)与**导入角色包**(「⤒ 导入」选 `.zip`,可填「导入为」换个标识;目标同名时**先问一声**,确认后旧份进 `roles/.trash/` 可恢复)。
- **侧栏**:工作区固定区(切换 = 真实切目录) + 历史会话(名称/**概述或内容预览**/时间,★ 置顶、⟳ 生成概述(调用模型,二次确认)、✎ 改名、× 删除——改删需二次确认)、附件上传(按钮/拖放/粘贴,图片缩略图 + 模型看图)、会话导出(⤓ 展开菜单:自包含网页 / 原始 jsonl;浏览器端直接下载,桌面端落盘到下载目录、网页自动打开)。当前项为卡片式选中(左侧竖条 + 描边 + 名称加粗),与 hover 明确分档。
- **状态栏**(只放别处没有的只读事实):就绪/运行中、**沙箱生效档**、未命名会话标识、连接状态(绿/橙/红三态)、上下文·缓存使用率、版本号(**点开「关于 gah」**;桌面版升级入口常驻在设置面板底部条,不随内容滚动)。模型/思考/审批各有唯一交互位(输入框工具条、设置面板),不在底栏重复显示;后台任务钮在右上角(运行徽标 + 列表/输出/终止)。
- **文档预览面板**(侧栏「文档预览」):左侧工作区文件树(过滤/懒展开/工作区切换整树重置)+ 右侧预览(markdown 块渲染、代码/表格、docx/xlsx/pptx 块模型、PDF 浏览器原生查看器、图片、HTML **默认源码视图 + 点击才加载沙箱 iframe**;截断与警告黄色提示条);工具结果行含可预览路径时出现「预览」按钮;会话流 assistant 文本走 markdown 块渲染(服务端解析,前端零 v-html)。
- **结构化提问**:模型 `ask_user_question` 弹层展示问题与选项;**可「稍后作答」收起为输入区上方角标**(不阻塞继续对话,点角标回到作答);多问并存按到达顺序逐个答。
- **轨迹视图**(状态栏右上「轨迹」切换,记忆偏好):把同一份会话事件按**回合 → 步骤 → 工具**聚合,专看过程与成本 —— 粘顶固定概览(回合数/总时长/累计 token 与缓存占比 + 可点击回跳的回合胶囊)、回合状态徽标(完成/已取消/步数上限/进行中)、每步工具行(名称/参数/耗时/**出参字节**/成功失败,失败展开错误原文)与回合级 token 分解;**时长只取事件时间戳,进行中的回合/步骤/工具一律显「进行中」**(不编造时长);窗口不完整(长会话更早历史未加载/已折叠)时概览改标「窗口内 token」并说明仅覆盖当前窗口。
- **变更视图**(状态栏右上循环切换:会话流 → 轨迹 → 变更 → 看板,记忆偏好):只看**本次会话经工具改过的文件** —— 粘顶概览(文件数/+行/−行/改动次数 + 可点击回跳的文件胶囊)、逐文件折叠块(新建/二进制/已截断 徽标、操作与 ± 计数)、展开后逐行着色 patch(每段带 `#序号 操作 时间` 头,便于对着会话流定位)与降级说明。数据来自写盘时捕获的前后内容,口径明确**不依赖 git**,不是「工作区当前 vs HEAD」;`/diff <路径>` 可从命令直接定位到某个文件。
- **看板视图**(第四种投影,状态栏右上循环切换):把已在手的数据聚合成一屏信息面,**五张卡片** —— 用量(累计输入/输出/缓存命中率/请求数/上下文占用)、回合(已完成回合数、总时长、工具调用与失败数、平均 token)、后台任务(运行中/记录数/最近一条)、文件变更(文件数/±行/改动次数)、定时计划(启用数/下次运行/最近终态)。卡片可**隐藏 / 上下移动顺序**、可「恢复默认」,布局记忆在浏览器(新增卡片自动补到尾部);卡片动作直达对应视图或抽屉(轨迹 / 变更 / 任务面板 / 设置的计划段)。**口径写在卡上**:用量是会话累计、回合数含进行中、变更来自工具写盘旁路(不依赖 git);数据全部来自本机事件账本与既有接口,不引入可执行内容、无新增后端契约。
- **侧栏停靠区**(状态栏「侧栏」展开,记忆偏好):把变更 / 看板 / 任务三个面板**停靠在对话流右侧并排显示** —— 不用在「切走会话流看变更」和「让任务面板盖住对话」之间二选一;面板在停靠区顶部标签间切换,宽度可**拖拽**(也可聚焦分隔线用 `←/→`,双击复位),布局落浏览器存储。**宽度给对话流让位**:视口不足时停靠区先让步(下限 280 / 上限 720 / 至少给对话留 520);窄屏(<900px)自动退化为覆盖式抽屉。零后端契约:面板内容复用既有视图组件,数据管道不变。**外壳永不整页滚动**(每个滚动区各有归属:会话流 / 侧栏列表 / 抽屉),这条纪律有真浏览器布局回归门禁守着(见「十」)。
- **长会话窗口**(首帧基线 + 上滚分页):打开一个很长的会话不再重放全部历史 —— 首连只回放**尾部窗口**(约 400 条事件,回合对齐),并先发一帧 **基线** 告诉前端窗口边界与「更早历史是否还有」;向上滚到接近顶部(或点顶部按钮)即自动按游标拉更早一页拼在前面(按序号去重、拼接后**滚动位置不动**)。贴底阅读时自动折叠最老的消息(上限 800 条,`已折叠 N 条更早消息`),被折叠的内容上滚可重新取回 —— DOM 与内存不随会话长度增长;轨迹/变更视图在窗口不完整时显式标注口径,不把窗口内合计说成会话全程累计。
- **WebSocket 通道**:`/api/events/ws`(与 SSE 同 payload,前端自动降级;两条路断线续传都带 `after` 游标)。
- **回合运行中可继续说话**(转向):回合没结束时输入框**依然可编辑可回车** —— 普通消息注入当前回合(模型下一次请求即可见,会话流插一条「已注入当前回合」),带附件的提交会被拒(409:转向通道只带文本,不静默丢附件);同时输入区出现**「停止」按钮**(中止回合,审批正等着你答复时也用它退出)。
- **断连行为**(显式三态:已连接/重连中/**已断开**):连接丢失时输入区上方出现红色横幅与「重试连接」,**提交与审批/作答一律拦截且草稿、附件、弹层原样保留**(未送达不许挥掉本地状态,恢复后手动重发,不做队列自动重放);恢复时以服务端快照接管状态并丢弃陈旧完成帧。断连判定只依据可观测事实(浏览器 `navigator.onLine`、EventSource 已放弃、探活失败),不用「多久没收到帧」猜测;切回前台/睡眠唤醒(时钟跳变 >20s)会主动重握一次。
- **通用 REST 能力面**(前端/脚本均可直接调用,未装配服务 503/501 显式):

| 方法 路径 | 说明 |
|---|---|
| `POST /api/auth` | 引导通道:`{"token":"…"}`(或 `Authorization: Bearer`)换 `gah_token` cookie(POST-only,错误 token 401);唯一豁免鉴权门的路径,仍受 Host 白名单 + 同源校验 |
| `GET /api/state` | 状态快照(model/thinking/sandbox/sandbox_effective/sandbox_sync/approval/stats/session/running/version;**model_from/thinking_from 与 model_session/thinking_session** = 生效值与来源(role\|session)以及被覆盖的会话档)。**`?session=<id>`** 查指定会话:回显 `session_id`、列出 `active_sessions`(其它窗口正占用的会话);非主会话的 `running` 恒 false(并行回合未落地,不谎报) |
| `POST /api/input` | 提交回合;body 可带 `session`;`/` 前缀走命令;回合运行中普通消息**注入当前回合**(转向,响应带 `accepted:"steer"`);**带附件的提交回落 409**(转向通道只带文本,不静默丢附件);无法注入时(未装配转向能力)与命令路径仍 409。**向非当前会话提交恒 409**(多会话并行回合未落地,内容会写进当前会话 ⇒ 显式拒收而非静默写错) |
| `POST /api/confirm` | 审批应答 `{id, ok, session?}`;`session` 与当前会话不符 ⇒ 409(防一个窗口替另一个应答) |
| `GET /api/events` + `GET /api/events/ws` | 事件流(SSE 断线重放 / WS;首连发 `baseline` 基线 + 尾部窗口,续传按 `after` 补差集)。**`?session=<id>`** 只收该会话的会话帧(帧带 `session` 字段),**非会话帧**(status/notice/plan/diff)仍全量广播 |
| `GET /api/session/events` | 会话事件分页(`?before=<seq>&limit=<n>&session=<id>`):长会话上滚加载更早历史,窗口回合对齐、返回 `has_more`;`session` 读的是**那个会话自己的**日志 |
| `GET /api/sessions`、`POST /api/sessions` | 会话列表 / `{action: switch\|new\|spawn\|fork\|clone\|delete}`;**`spawn`** = 新建独立会话且**不切换当前**(多窗口用) |
| `POST /api/sessions/rename`、`GET /api/sessions/{id}/export` | 会话改名 / 导出会话(`?format=html` = 自包含网页,缺省 jsonl;无 id 路径 = 主会话) |
| `GET /api/workspaces`、`DELETE /api/workspaces/{key}` | 工作区历史 / 删除记录(不动文件夹) |
| `GET /api/commands`、`POST /api/commands/{name}` | 命令注册表 / 直接执行 `{args}` |
| `GET /api/tools`、`POST /api/tools/{name}` | 工具清单 / 调用(参数 JSON 透传) |
| `GET /api/jobs`、`GET/POST /api/jobs/{id}[/kill]` | 后台任务 |
| `GET/POST /api/schedules`、`PATCH/DELETE /api/schedules/{id}`、`POST /api/schedules/{id}/run`、`POST /api/schedules/resolve` | 定时计划:列表/新增/改(仅覆盖传入字段)/删/立即触发;`resolve` 排期解释(传 `cron` 或一句中文 `text`,返回控件档位 + 中文描述 + 接下来三次触发时刻;**解析不出也返回 200 与中文原因** —— 「没看懂」是日常不是故障) |
| `GET/POST /api/mcp` | MCP server 配置:GET 返回配置 + 逐项运行期状态(是否已加载/工具数);POST 提交整表 = 写 `config/mcp.yaml` 并重启插件(`reload:false` 只写盘;写盘成功但重载失败 → 200 + `reload_err`) |
| `GET /api/plugins`、`POST /api/plugins/{id}/load\|unload` | 插件启停 |
| `GET /api/models?all=1`、`GET/POST/DELETE /api/providers...` | 模型聚合 / provider 增删改(删除同步运行期与持久化) |
| `POST /api/control` | 状态栏级控制 `{model?\|thinking?\|sandbox?\|approval?\|workspace?\|cancel?}`(偏好持久化;`cancel:true` = 中止运行中的回合,Web 输入区的「停止」用的就是它) |
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
| `GAH_SHELL_KERNEL_SANDBOX` | `0` = 关闭 shell 的**内核级沙箱**(默认开启:macOS `sandbox-exec` seatbelt / Linux Landlock 在进程树层面限制文件写;写之外另拒**凭据目录的读**(macOS,见 `GAH_SHELL_CRED_READ_KERNEL`),其余读与网络不限;能力缺失平台会自动告警并降级为纯协作式控制) |
| `GAH_SHELL_CRED_READ_KERNEL` | `0` = 关闭**内核层凭据读拒绝**(默认开启:macOS 把 `~/.ssh`/`~/.gnupg`/`~/.aws`/`~/.config/gcloud`/`$GAH_HOME/config` 写进 seatbelt profile 拒**读**)。为何需要:协作层的凭据判定只看得见命令文本里的**字面**路径,`python3 -c "open('~/.ss'+'h/id_'+'rsa')"` 这类动态构造完全绕过它(2026-09-27 审计实测),而读侧此前没有内核层兜底(写侧由 `deny file-write*` 覆盖);关掉即退回纯文本层判定。代价:直读 keyfile 的 `ssh`/`git push`、`aws`/`gcloud` CLI 会被拒(用 agent/keychain 时不受影响)。**Linux 分支无此能力**:Landlock 规则是 allow-list,无法表达"除凭据目录外全放行读" |
| `GAH_SHELL_JAIL` | `0` = 关闭 shell 执行的**环境 jail**(默认开启:把 `TMPDIR`/`XDG_CACHE_HOME`/`GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` 重定向到 `$GAH_HOME/jail/**`,让构建缓存与临时文件不再散落用户家目录;`HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` 刻意保留以照常读 git/ssh 配置) |
| `GAH_EXT_ENV_PASS` | 显式放行给外部进程插件的环境变量(逗号分隔):外部插件默认**不继承宿主凭据**(`*_API_KEY`/`*_TOKEN`/`AWS_*`/`GAH_CB_*` 等已滤除),确需凭据的插件在此点名(如 `EXA_API_KEY`);或写进 `$GAH_HOME/config/search.yaml` —— 宿主按插件声明**代读并注入进程 env**(第八十五批;插件进程被内核读拒挡在 `config/` 外),点名且有值优先于文件值 |
| `GAH_EXT_KERNEL_SANDBOX` | `0` = 关闭**MCP server 的内核级沙箱**(默认开启:MCP server 是第三方代码,只经工具参数的无路径裁决对它毫无约束——它自己选定的 DB/缓存/临时文件落点完全看不见;故起进程时按同一套 seatbelt/Landlock 包装限制文件写,白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**` + 包管理器缓存与系统临时目录) |
| `GAH_EXT_RW_PATHS` | 追加给 MCP server 的**可写路径**(冒号分隔):server 需要写自己的数据目录(如 `~/Library/Application Support/<app>`)时在此点名;默认白名单只含缓存与临时区,不猜应用数据目录 |
| `GAH_EXT_CRED_READ_DENY` | `1` = 开启**MCP server 的内核层凭据读拒绝**(默认**关**:读凭据目录是 server 的正当职责,默认拒会大面积打断;只拒目录不拒文件,同 `GAH_SHELL_CRED_READ_KERNEL`,仅 macOS 有等价能力) |
| `GAH_EXT_PLUGIN_SANDBOX` | `0` = 关闭**外部插件进程的内核级沙箱**(默认开启:插件进程是独立代码,协作层只看得见经工具参数传入的路径,它自己选定的写落点看不见;白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**` + 包管理器缓存与系统临时目录 + 插件**自报**的数据目录) |
| `GAH_EXT_PLUGIN_RW_PATHS` | 追加给**外部插件进程**的可写路径(冒号分隔):第三方插件要写自己的数据目录/DB 时在此点名;自研插件请改用能力自报(`DataWrites`) |
| `GAH_EXT_PLUGIN_CRED_READ_DENY` | `1` = 开启**外部插件进程的内核层凭据读拒绝**(默认**关**,同 MCP server 那一条的理由;仅 macOS 有等价能力) |
| `GAH_MCP_SERVE` / `GAH_PLUGIN` / `GAH_VERSION` | 外部进程工具入口参数(serve/加载插件/版本通告;由 host-bridge 拉起时注入) |
| `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM 密钥(可按 provider 前缀路由;或经 `/provider` 写入 provider.yaml) |
| `GAH_SEARCH_API_KEY` | web_search 通用搜索密钥(env > `$GAH_HOME/config/search.yaml` 的 `api_key`)。默认 AnySearch **匿名即可用**,通常无需设;配 key 只是提高并发与额度 |
| `ANYSEARCH_API_KEY` / `EXA_API_KEY` | 对应 provider 的专用密钥(优先于通用 `api_key`);换 provider 到 exa 时才需要 |
| `ANYSEARCH_ENDPOINT` / `EXA_ENDPOINT` | 对应 provider 的专用端点(优先于通用 `GAH_SEARCH_ENDPOINT`)。**key 与端点都按 provider 分家**,切 provider 时只改 `search.yaml` 的 `provider:` 一行即可 —— 串味的后果是静默失败(key 发错家 401、端点发错家打到另一家服务) |
| —(文件) | `search.yaml` 带 `search-config-version` 标记;**升级后 gah 会自动把它补齐到当前 schema**(旧版备份到 `config/config-backups/search.yaml.*`,备份同样 0600)。归属明确的通用值(`api_key`/`endpoint`,provider 未写或写 `exa`)自动搬进 exa 专属键;归属不明的**原样保留并提示** —— 自动搬等于猜,猜错的表现是搜索静默失效 |
| `GAH_SEARCH_PROVIDER` / `GAH_SEARCH_ENDPOINT` | web_search 的提供商名与端点(端点可指向**自建 Exa 兼容服务**,此时无需 key —— key 为空不发 `Authorization`);env > 配置文件。注:外部插件进程读不到 `config/`(内核凭据读拒),配置文件由宿主代读注入,故**改文件后需重载 `tool-basic` 插件**(或重启)才生效 |
| `GAH_WEB_APPROVE` | `web_fetch` 的**出口审批档位**:`auto`(**默认**,跟随审批档:open/smart 直接放行、strict 等同 new-host)/ `off`(不管)/ `new-host`(每个新域名首次访问弹一次确认,批准后加入白名单)/ `query`(每次访问都确认,不落白名单)。反提示注入、内网/DNS 重绑定拦截、凭据读拒不依赖这道门,故智能/开放档不再逐域名打断 |
| `GAH_WEB_ALLOW_HOSTS` | `web_fetch` 的**静态域名白名单**(逗号分隔的精确 host;`*.suffix.com` 匹配其子域):无人值守/无确认通道场景的事前放行手段(批准过的域名另记在 `$GAH_HOME/config/gah-state.json` 的 `web_allow_hosts`) |
| `GAH_WEB_ALLOW_PRIVATE` | `1` = 放行 `web_fetch` 抓**内网/环回/链路本地**地址(默认**拒**:抓取的 URL 来自模型,防它被诱导去读本机未鉴权服务与云元数据端点 `169.254.169.254`;本地开发服务或**经本地 HTTP 代理**上网时需要打开——后者守卫看到的拨号目标是代理地址,不豁免) |
| `GAH_ALLOW_PLUGIN_GOENV` | `1` = 插件安装的构建子进程**保留**宿主的 `GOPRIVATE`/`GONOSUMDB`/`GOSUMDB`(默认剥掉,见「构建环境隔离与它的放行口」)。只影响 go 的模块解析与校验,不含凭据 |
| `GAH_ALLOW_IMPLICIT_PLUGIN_BUILD` | `1` = 允许对**未声明 `build:`** 的 plugin.yaml 走默认构建命令。**当前恒为不生效**(A 口径是「照常装 + 标注那条命令是 gah 替你选的」);常量与判定点已留好,收紧成「拒绝」时改一个函数即可 |
| `GAH_DOC_CONVERTER_SANDBOX` | `0` = 关闭**文档转换器(LibreOffice/pdftoppm)子进程的内核级沙箱**(默认开启:它吃的是不可信文档——网页下载/附件;白名单 = 转换缓存 + `$GAH_HOME/jail/**` + 系统临时目录,档位固定 read-only;外部转换器本身默认关:见 `data.external_converters`) |

### MCP 接入(桥 client / serve 形态)

> **MCP server 按内核沙箱起**(2026-09-27 审计 A3):server 是第三方代码,而路径裁决只看得到经 `mcp_*` 工具传入的参数 —— 它自己选定的 DB/缓存/临时落点完全看不见。故宿主按**当前有效档位**把它起在 seatbelt(macOS)/Landlock(Linux)包装里:白名单 = 有效档允许的 workspace 根 + `$GAH_HOME/jail/**` + 包管理器缓存与系统临时目录(实测:`npx` 类 server 少了缓存白名单会直接起不来)。需要写自己的数据目录时 `GAH_EXT_RW_PATHS=/path/a:/path/b` 点名;`GAH_EXT_KERNEL_SANDBOX=0` 整体关闭;`GAH_EXT_CRED_READ_DENY=1` 额外拒凭据目录的读(默认关)。
>
> **外部插件进程同样在作用面内**(2026-09-27 审计 A3b + A6):`tool-mcp`/`tool-workflow`/`tool-subagent` 与第三方插件进程整体被包装(同一套白名单 + 插件**自报**的数据目录),插件自己再起的子进程继承该 profile(不再重复施加 —— 内核沙箱不可嵌套)。`tool-basic` 也一样被包装:它内部给 shell 按调用施加的那层会因 `GAH_KERNEL_SANDBOXED` 标记自动跳过,由外层 profile 统一生效(故无需"自己会套就别包我"这类豁免口);它经 `bridge.ServeToolsWith(..., Capabilities{CredentialReadDeny:true})` 声明"进程内会跑 shell 命令",宿主据此默认开凭据目录读拒(延续 F1 的保护)。已加载插件在档位/工作根变更后**先同步按新档位重建再执行本次调用**(绝不拿旧 profile 跑,也不会让切工作区后的第一次调用失败)。声明方式与可写集见 `docs/PLUGIN_DEV.md`。

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
./gah -install <repo>@<40位sha>      # 装回**指定的那一次提交**(tag 被改、想回退时用)
./gah -install mcp:<id>:<command>   # MCP 插件经同一入口登记桥配置
./gah -install-ui <repo|本地目录>   # UI 插件(manifest.json 声明槽位覆盖;两重拒装护栏:v-html 指令扫描 + 产物含未替换裸 process.env)
./gah -list-plugins / -uninstall <id>
./gah -list-ui-plugins / -uninstall-ui <id>
```

#### 来源固定与漂移守卫

**第三方插件 = 别人上传到他自己的仓库里的插件**(以及任何公开仓库的插件)。gah **不验签名,后果自担**,
但把「我装的到底是哪一份代码」变成**被记录、被显示、可拒绝漂移**的事实。

**为什么要有**:`git clone --depth 1 --branch <ref>` 原本**没有任何固定** —— 同名 **tag 可被 force-push 覆盖**;
GitHub / Gitee 的**用户名可以注销后被重新注册**(原作者放弃插件 → 账号被回收 → 新人注册同名账号推一个
「v1.2.0」)。于是你重跑**一模一样的命令**就装到了别人的代码,全程无痕。`SHA256SUMS` 挡不住这件事 ——
它只管「装完有没有被换」,不管「装的那一刻拿到的是不是同一份」。

- **来源账** `$GAH_HOME/plugins/sources.yaml`(0600):按**仓库**记「我当初装的是哪一份」——
  归一化后的仓库名、ref、**实际解析到的 40 位 sha**、插件 id、装机时的协议版本、来源归属、安装时间。
  与 `SHA256SUMS` 刻意**不合并**:后者按**二进制**、随插件增删,前者按**仓库**、只在安装时更新,两者生命周期与键空间都不同。
  仓库 URL 归一化(`https://github.com/a/b` / `git@github.com:a/b.git` / `github.com/a/b` / 带尾斜杠 是同一个仓库)——
  不归一化的话守卫形同虚设。**账坏了会显式报错**,不当空账:静默当空账的话下一次安装会把它整份覆盖掉,
  而那份记录是唯一能回答「上周那个插件是哪一份」的东西。
- **漂移判定**:同一 ref 解析到**相同** sha ⇒ 幂等重装。解析到**不同** sha ⇒
  - **tag** ⇒ **拒绝**,并给两条出路:`--accept-drift`(换成作者现在这一版)/ `@<旧 sha>`(留在原版);
  - **branch / 默认分支** ⇒ 放行,但**醒目提示并记 `drifted`**。
  **拦 tag 不拦 branch**:tag 是作者「这一版就是这一版」的承诺,被重推属于违反承诺;拦 branch 等于逼所有人打 tag,而长尾作者很少打。
- **`@<commit sha>` 今天才做得到**:`git clone --branch` 不接受裸 sha,所以在上面那条拒绝文案里,「装回原来那版」在批一之前**根本做不到**。现在走 `git fetch --depth 1 <sha>` + `checkout FETCH_HEAD`;托管商不允许对任意历史 commit 浅 fetch 时自动退回完整 clone。
- **面板与清单如实显示**来源三件:`github.com/a/b @v1.2.0 (tag · 9f2c1ab3e5f7)`,「会移动」的引用会标出来。

**检查更新(check-then-ask),不是自动更新**:`gah -check-plugin-updates` / `/install check` / 面板「检查更新」按钮,
用 `git ls-remote` 问一次远端当前指向(**不 clone**),三态:无更新 / 分支前移 / **同名 tag 被改**(按守卫会被拒,文案给两条出路)。确认后才走安装。

**为什么不做自动更新**:① 在**没有签名**的前提下,自动更新通道是一条无认证的、持续性的远程代码执行通道 ——
手动装一次是「你读过的、你决定的」,自动更新让它变成后台持续发生、且**无法回答这个包来自那个作者**(作者账号被攻陷时,手动模式只有主动重跑的人中招,自动模式**所有用户同时中招**);
② 它会消灭「gah 没有插件自动更新」这条正面性质;③ 与 gah 自己不一致 —— 桌面壳刚把「检查到就自动下载安装」改成「先问再装」,宿主自己有签名才敢问一句。
即便将来加了签名,自动更新仍危险:**「更新到最新版」与「你信任的最新版」不是一回事**,tag 本身就是作者表达「这一版」的机制。

#### 预编译产物 `--prebuilt`:把陌生构建脚本移出你的机器

装一个插件原本要在**你的机器上**跑作者声明的**任意 shell 命令**。前面的构建收口(`-mod=readonly`、
不主动 tidy、凭据清洗)限制了它,但那仍然是任意 shell —— 构建脚本还能读你磁盘上的文件。
`--prebuilt` 把这一格**整个消掉**:只下载作者发布的产物,**本机可以没有 go / node / make**。

发布方在 `plugin.yaml` 里声明(Release 就是作者发产物的地方;不另建索引服务 —— 那意味着还要信任另一个人):

```yaml
prebuilt:
  darwin/arm64: https://github.com/a/b/releases/download/v1.2.0/tool-demo-darwin-arm64
  linux/amd64:  https://…
```

- **没写这段,或者没写当前平台 ⇒ 显式报错**,**绝不悄悄回退到源码构建** —— 你点这个开关就是为了不跑它的脚本,回退等于把它变成一个谎言。报错里会列出作者声明过哪些平台,并告诉你去掉开关就能源码构建。
- 装之前校验三条:非空 / 体积上限(512 MiB)/ **架构魔数**(照抄发行脚本 `gen-extplugins.sh` 的 `assert_arch`:ELF / Mach-O LE64 / PE)。架构不符是最容易犯也最难查的一类错 —— 不验的症状是「装上了,一调就 exec format error」,用户会以为是插件坏了。
- URL 只收 `http/https`:`file:///etc/shadow` 会把「下载一个产物」变成「读你本机的任意文件并装成可执行插件」。
- **与漂移守卫联动**:`prebuilt:` 段是 manifest 的一部分,manifest 是 clone 下来的 ⇒ **tag 漂移守卫在下载之前就已经生效**。被顶替的 tag 给出的预编译 URL 同样会被拦。
- 产物哈希照常进白名单、来源账照常记 repo/ref/commit;额外记下**产物 URL**,审计来源标成 `install-prebuilt:<url>` —— 事后要能一眼分清「这个二进制是下载来的,不是在你机器上构建的」。

> ⚠️ **诚实的代价**:下载地址是**作者自己声明的**,而 gah **不验签名** ⇒ **装的那一刻没有独立校验**。这是不做签名的直接后果,不是可以绕过的实现瑕疵。唯一的缓解是:装完的哈希进白名单,**装完再换会被下一次加载挡住**。这段话同时出现在安装确认文案里,不是只写在文档里。

#### 构建环境隔离与它的放行口

构建子进程拿不到你环境里的 API key/令牌(`sdk.SanitizedEnv`),宿主的 `GOFLAGS`/`GO111MODULE`/`GOWORK`
也被去掉 —— 它们**改变构建语义**,而语义该由这次安装决定,不该由你机器上碰巧的环境变量决定。

`GOPRIVATE`/`GONOSUMDB`/`GOSUMDB` 默认也剥掉:剥掉 `GOPRIVATE` 会让 go 把**私有模块路径当公开模块**
去查 sumdb,那等于**把内部模块名泄漏到公网查询里**,方向是对的。但依赖私有模块的企业内部插件会构建失败,
而"构建不出来"对用户是死路 ⇒ 给一条显式放行口:`GAH_ALLOW_PLUGIN_GOENV=1`。

> `GAH_ALLOW_IMPLICIT_PLUGIN_BUILD` 是另一件事(未声明 `build:` 的隐式构建)的放行口,
> **当前恒为不生效**:A 口径是「照常装 + 三处标注这条命令是 gah 替你选的,不是作者写的」。
> 收紧成 C(拒绝)的**判定点**已在代码里(`implicitBuildAllowed`),改一个函数即可。

`copyDir`(本地目录安装)修掉两个缺陷:① **拒绝符号链接** —— 原实现 `Walk` 用 Lstat(不跟随)却用
`os.ReadFile`(跟随),一个指向 `~/.ssh/id_rsa` 的软链会被**读出来写进临时克隆**,而那份克隆随后就是插件仓库,
构建脚本完全可见;② **保留可执行位** —— 原先一律写 `0o644`,本地插件用 `./build.sh` 构建会报
`Permission denied`,而那个报错与真实原因毫无关系。

#### 装一个别人构建好的产物(`-install-artifact`):长尾插件也能免 Go

`--prebuilt` 能在**作者配合**时免工具链安装(manifest 里声明 `prebuilt:` + 加那个开关)。但长尾第三方插件基本不会写那一段 —— 那要求作者额外发七份产物并维护一张表。对这些插件,今天免 Go 的唯一路径是「手工下载 → 扔进 `plugins/` → `gah -trust-plugin`」三步手工活。

`-install-artifact <url> -id <插件id> -name <tool-xxx>` 把那三步收成一条命令:

```bash
./gah -install-artifact https://.../tool-demo-darwin-arm64 -id demo -name tool-demo
```

- **不需要 Go / node / make,也不跑任何构建脚本** —— 校验架构魔数之后直接落位。
- 校验:只收 `http/https` / 体积上限 / **架构魔数**(照抄发行脚本的 `assert_arch`)。架构不符是最容易犯也最难查的一类错,不验的症状是「装上了,一调就 exec format error」。
- **`-id` / `-name` 会直接拼进落位路径**,故做**白名单式**校验(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`、不接受 `..`、名称必须 `tool-` / `cmd-` 开头)。白名单比黑名单可靠:黑名单永远漏一个。用的是**加载器自己的**前缀判据,而不是另立一套 —— 两处规则一漂,就会出现「装的时候过了、加载时被静默跳过」。
- **不猜任何东西**:`-id` 与 `-name` 都必须显式给。不接受「从文件名里猜 id/平台」那种形态(猜错的形态是「装上了,工具名不是你要的那个」)。
- 装完照常登记哈希白名单 + 来源账(`kind: artifact`)+ 审计行(`artifact-install:<url>`)。产物路径**不进**更新检查(它连 repo 都不是,拿 URL 去 `ls-remote` 只会得到一条对用户毫无意义的错误)。
- TUI 侧等价物:`/install artifact <url> <id> <name>`。

> ⚠️ 与 `--prebuilt` **同款的诚实代价**:下载地址由给出 URL 的人决定,而 gah **不验签名** ⇒ **装的那一刻没有独立校验**。唯一的缓解是:装完的哈希进白名单,**装完再换会被下一次加载挡住**。这段话同时在安装确认文案里。

**零网络的本地兼容性提示**:装机时记下的协议版本写在来源账里(不给插件的 `Capabilities` 加字段 —— 那是给作者加负担)。
`api_version` 是**兼容范围**(`api_version: v1` 或 `api_version: [v1, v2]`,单值与列表两种写法等价),本版 gah 不认的插件在面板出**一条汇总**提示(不是逐条弹),零网络。

#### 插件探测:超时不再被伪装成「没有」(2026-10-03)

外部插件在**启动前**要跑两次短命探测(`bin --roles` 问角色、`bin <role> --gah-caps` 问能力声明)。二者原先共用一个 3s 超时,而**超时后的降级方向相反**:角色探测超时 ⇒ 多角色二进制被当成「单角色」启动 ⇒ 缺角色参数 ⇒ 握手失败 ⇒ **工具整组消失**,日志里只有插件自己打的用法提示;能力探测超时 ⇒ 作者的能力声明(`CredentialReadDeny` 等)**被静默丢弃**。

两者都把「**没测出来**」当成了「**测出来了,是空**」。高负载机器上(实测:并发跑全量测试 + 六目标交叉编译时)这会让官方 `tool-kit` 整个加载失败。

现在:
- 角色探测与能力探测**各用各的超时**。角色探测 3s → **10s**(一个 4 角色的 kit 最坏只跑一次,因为它按二进制而非按角色探测);能力探测**保持 3s** —— 它在 `loadOne` 里**逐角色**执行,提到 10s 就是最坏 40s 启动。两者共用一个常量本来就是隐患:调大一个会顺手改了另一个。
- 超时**仍然**降级为「按单角色启动」,**方向故意不改**:第三方单角色插件在超时下恰好得到正确结果;若改成「超时就跳过」,那才是回归 —— 完全正常的插件会在慢机器上集体消失。要改的是**留痕**,不是降级方向。
- 超时会**明说**:日志与被拒清单都会点出「这个二进制我们没测准」,能力探测那条还会说明**丢的是哪些声明**。
- 总预算(20s)必须大于单探测超时 —— 否则预算机制会早于任何探测完成就返回,事实上失效。这条作为不变量由单测钉住。

#### 生命周期:第三方外部进程插件随时可启停(停用 ≠ 卸载)

此前 `sdk.ExternalPlugins` **只有 `Reload`** ⇒ 没有**任何运行时手段**关掉一个行为不良的插件。在「不验签名、后果自担」的模型下,启停不是锦上添花,**它就是用户的止损手段**。

| | 停用(`disable`) | 卸载(`uninstall`) |
|---|---|---|
| 进程 / 工具 | 停掉、撤销注册 | 停掉、撤销注册 |
| 插件文件 | **留着** | 删掉 |
| 白名单 / 来源账条目 | **留着**(重开**不需要**重新 `trust`) | 撤销 |

面板按钮、TUI `/install enable|disable`、`POST /api/plugins/install/{disable,enable}` 共用同一份内核。**按二进制停用,不是按角色** —— 一个 `tool-kit` 提供四个角色,但用户的心智单位是「tool-kit」这一件。

三条由此改掉的行为:

- **顺序是承重的**:卸载必须「先停进程,再删文件」。反过来就是「用户删了文件,进程却继续持着工具注册与回调 token 跑到 gah 重启」—— **虚假的安全感**。停不掉就**中止卸载并报错**(文件不删),让用户有可重试的对象。
- **删掉二进制 = 真卸载**,不必等重启。watcher 此前只报写/建/改名,**删除被显式排除**(注释「删除由 plugin-manager 决定」),而外部插件这条路**没有 plugin-manager 介入** ⇒ `rm` 之后进程一路跑到重启。
- **停用状态在 boot 时就生效**。这是止损手段在真实事故里的用法(关掉 → 停用 → 再开回来);只在运行中生效的话,重启一次它就回来了。
- **重新启用会重验漂移**:停用半年后回来,作者可能已经把 tag 换了 —— 不重验的话「停用」就成了绕过漂移守卫的后门。tag 漂移 ⇒ 拒绝并给出路(重新安装 / 留在原版);**问不到远端(离线)⇒ 放行**,因为判不出漂移 ≠ 有漂移,否则离线用户永远开不了插件。
- 回合执行中停用 ⇒ 正在跑的工具调用**给可读错误**且可重试,**宿主不崩**(RPC 断连走既有软降级)。

#### UI 插件完整性闸默认强制(批四 —— 一条被**断掉**的老路)

UI 插件是**与宿主同源同权限**的最宽面:产物经动态 `import()` 进主页面,能调全部 API(含工具执行)。
此前它的 `ui-plugins/SHA256SUMS` **只有 `-install-ui` 会创建** ⇒ **从没装过 UI 插件的用户,
手工拷一个目录进 `ui-plugins/` 就能直接用** —— 本项目最宽的一个零校验口。

现在:闸由 `ui-web-app` 在 Start 时**无条件**创建(失败只记 WARN 不阻断启动)。因此:

- **手工放置的 UI 插件默认不加载。** 放行:`gah -trust-ui-plugin <id>`(只登记**当前那一份**的
  entry-scope 摘要 —— manifest + 槽位声明的模块;清单里已有**不同**摘要 ⇒ 显式拒绝,不自动洗白)。
  `-install-ui` 走同一份登记,不需要额外一步。
- 这条老路被断掉的那一刻,**面板与日志各留一行说明**(不是静默拒绝)—— 否则用户只会看到「我放的插件不见了」,以为产品坏了。
- **UI 插件也有启停**(停用 = 不下发,文件与闸条目留着,重新启用不需要重新登记)。`POST /api/ui-plugins/{enable,disable}`。
  状态落在 `prefs.ui_disabled`,**与 `external_disabled` 分开两个字段** —— 键空间不同(一个是二进制基名,一个是 manifest id),混在一个列表里迟早出错。
- 「两种空」在 `plugintrust` 里被分开(**两侧统一**):**0 字节** = 被写坏 ⇒ 报错(fail-closed 不变);**只有注释行** = 合法 ⇒ 启用但当前无被信任插件。这条是「无条件建闸」与「fail-closed」不冲突的前提 —— UI 侧**没有官方插件**可供 embed 自登记,「像进程型那样每次 boot 登记官方件」那条路根本不存在。
- 面板插件段**按来源分两组**:「随 gah 附带的插件」(由 embed 每次启动与嵌入清单比对)与「你安装的插件」(三态:启用 / 已停用 / 二进制缺失)。这两个集合的处置完全不同,混在一张表里用户每次都要先读一列才知道哪些按钮对自己有意义。

> `/plugins` 与 `/install` 的分工(两条命令的说明里都写明):`/plugins` 管**进程内**插件(配置树 + `patch-runtime.yaml`);外部进程插件的启停走 `/install` 或面板插件段。先想到 `/plugins off` 却对第三方插件无效,是这个分工最常见的误用。

### 指令文件与技能

- **指令**:全局 `$GAH_HOME/AGENTS.md` + 项目 `AGENTS.md`(近者覆盖远者;`AGENTS.override.md` 同级替换)——自动注入 system prompt,`/reload` 热更。
- **技能**:项目 `.gah/skills/` + 全局 `$GAH_HOME/skills/` + 当前角色的 `roles/<id>/skills/`,SKILL.md 扫描(按角色挂载过滤;目录集**每次扫描现算** —— `/workspace` 切目录后新项目的 `.gah/skills/` 立刻可见,不必重启);模型经 `list_skills`/`read_skill` 按需加载。**重名是全局按名字 first-wins**:角色私有技能会**压掉同名共享技能**(包括其它角色显式挂载的那个名字,它们既列不出也读不到)⇒ 私有技能名请当作全局唯一;重名每次重扫都告警(日志),不再只在启动时报一次。
- **「所有角色都生效」的全局层(现状即已具备)**:没有单独的「默认角色」实体,全局性由两层承担 —— **全局指令** `$GAH_HOME/AGENTS.md`(无角色、或角色未声明 `exclude_global` 时注入;设置面板「指令」段可直接编辑,不必再去改文件;上限 32 KiB —— 面板拒写超限内容,手改出的超限文件注入时截断并标注「已截断」)与**共享技能库** `$GAH_HOME/skills/`(= 默认池:角色未写 `skills` 键即全部可见,写了 `skills` 则被替换、`skills_inherit: true` 再并回)。角色要「用不到某些全局内容」是**主动 opt-out**(`exclude_global` / `skills` 替换),不是默认排除。
- **角色**:`$GAH_HOME/roles/<id>/` = `role.yaml`(名称/身份句/`exclude_global`/技能挂载/**`model` 与 `thinking`**/**`approval` 与 `sandbox`**)+ `AGENTS.md`(工作规则,上限 32 KiB 超限截断并标注)+ `skills/`(私有技能)。**角色可携带模型与思考档**:声明了就**每回合自动覆盖**会话档(TUI 输入行右侧、Web 底栏与设置面板均标「角色指定」并显示被覆盖的会话档;`/model`、`/thinking` 仍可改会话档,只在停用角色后生效);角色模型没有任何 provider 能接时**本轮回退会话模型并告警一次**(不静默、不硬失败);角色**不能**选 provider/凭据。**并行子代理会继承当前角色的模型/思考档/身份槽/技能可见性/工具可见性**(同一角色的分身同模型、同身份、同可见技能集、同可用工具 —— 身份走同一份系统提示组装,可见性与工具表走同一份会话级判定)。**角色可排除工具**:`role.yaml` 的 `tools_exclude` 列出即不给(默认全部工具可用 —— 方向与技能挂载相反,新装插件的工具对老角色依然可见),被排除的工具模型**看不见也调不动**(凭记忆调用会被显式拒绝,文案与"工具不存在"区分开),子代理/工作流/外部插件回调/MCP server 五个消费面同口径;面板「角色 → 工具」小节可勾选(展开才拉全量清单)、一键「全部恢复」,已排除但当前不存在的名字(插件卸载后残留)会标注并可单独清除。**角色可把权限往里收(不能往外放)**:`role.yaml` 的 `approval`(`smart`/`strict`)与 `sandbox`(`read-only`/`workspace-write`)声明的是**下限** —— 实际生效取「全局声明档 → 审批联动 → 角色档」里更严的那个(所以审批 `open` + 联动把沙箱放大到 `full-access` 时,角色的 `read-only` 依然压得住),审批档被角色收到 `strict` 时危险命令**直接拒绝、不弹确认框**(无人值守的定时任务与子代理同样按它裁决)。展示三端如实:状态栏审批段标 `审批: 智能→严格(角色收紧)`、`/approval` 与 `/sandbox` 回显「有效: X(角色收紧)」、Web 底栏与设置面板同口径 —— 偏离来源由策略器自报,不写死成“联动所致”。`open`/`full-access` 这类**放宽**值会被显式拒绝(400 / 报错,不是静默忽略也不是静默取最严),因为它们属提权面,须配显式开关与二次确认。**不做**角色级启停插件(会动用户的界面面与依赖图)与白名单式工具集(新插件会静默不可见)。旧版本手写在 `role.yaml` 里的 `model:` 键从此真的生效。思考档只认 `off`/`low`/`medium`/`high`(大小写与首尾空白自动归一);写别的值会被当成**坏角色**显式报出(面板与 `list_roles` 都看得到),不会静默按 `off` 跑。Web 设置面板「角色」段同样支持新建/改名/删除(进 `roles/.trash/`,当前角色拒绝删除)、编辑工作规则(带字节计数)、勾选技能挂载、新建与编辑技能(共享库或角色私有,删除进技能库 `.trash/`)。删除落在各自 `.trash/`(**各保留最近 20 份,按删除时间**) ,面板「回收站」折叠区列出角色/技能两组条目并可按目录名恢复(已存在同名角色/技能时显式拒绝,不覆盖现役)。技能可在面板里**改名或改归属库**(共享库 ↔ 角色私有;目录名即身份,frontmatter 的 `name` 同步改),**改名会同步改掉每个挂载它的角色引用**,不会留下已失效挂载;**移进角色私有库时会提示可见性后果**(该技能不再属于共享库默认池,并列出其它显式挂载它的角色);角色切换会往会话记录里落一条 `role/switch` 事件(Web 会话流里标出切换点,`/recap` 据此报「角色经历」)。预置 5 个可直接 `/role use`;技能未写 `skills` 键 = 默认池,写了 = 替换,`skills_inherit: true` = 替换后再并默认池;角色私有技能只增不减、对其它角色不可见(同名时私有压过共享库)。**角色可打包分享**:`/role export <id> [路径]` 把角色收成**单文件** `gah-role-<id>.zip`(清单 `gah-role.json` + `role.yaml` **原样搬运** + `AGENTS.md` + 私有技能,同角色同刻导出字节一致),`/role import <路径>` 在另一台 gah 上导入即复现(也可走 Web:设置面板「角色」段的导出/导入,或 `GET /api/rolepack/{id}` 下载、`POST /api/rolepack` 上传);导入默认**同名拒绝**,`force` 覆盖时旧份先整份移入 `roles/.trash/`,**当前角色一律拒绝覆盖**;包里若有本版本不认识的键、未知条目、超限内容 —— 一律**显式报错且零字节落盘**(解包是白名单式的:只认那四种条目名,磁盘路径由解析出的名字重新拼)。导出**只带角色本身**,不含全局 `AGENTS.md`、共享技能库与凭据。
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

- `scripts/gen-extplugins.sh` 按发行矩阵(darwin/linux × amd64/arm64 + windows/amd64)构建外部插件产物,仓内 `scripts/zstdpack` 确定性压缩(纯 Go,五平台 CI 不依赖系统 zstd),embed 按平台拆包(每目标只嵌本平台产物)。
- `goreleaser release --snapshot` 可直接出六目标包;`.goreleaser.yaml` 已配置 before hooks。

- **门禁(CI 与本地同源)**:`go vet` + 全库 `go test -race` + `bash scripts/coverage-check.sh`(逐包棘轮 + 全局下限,豁免需理由;**本地跑请带 `GAH_COVER_MERGE_DIR=<目录>`**,否则 `cmd/gah` 的子进程覆盖没并入、会假红)+ `scripts/size-check.sh`(体积门 ≤36 MiB / gz ≤23 MiB)+ 前端 `npm test`(逻辑单测)+ **`npm run test:layout`**(真浏览器布局回归:5 视口 × 4 停靠态,断言整页不滚 / 无越界元素 / 骨架在场,并含**检测器自检**;CI 上带 `GAH_LAYOUT_REQUIRE=1`,环境不满足即红灯而不是静默跳过)。`sdk/` 是独立 module(`go.work`),三步需单独跑。

验收实测(DESIGN §7.6 / 最新基线):`CGO_ENABLED=0` 静态单文件 **30–34 MiB** 五目标(体积门 = `scripts/size-check.sh`,现值 ≤36 MiB / gz ≤23 MiB)、六目标交叉编译全绿、裸机 `env -i` 启动成功、sha256 附档。

## 十一、协议

**MIT License**(见 [LICENSE](./LICENSE),© 2026 nekoleamo):宽松许可,允许任意使用/修改/商用闭源分发。
