# 开发规范(gah 仓库)

> 供 AI 与开发者共同遵守;插件开发细读 [docs/PLUGIN_DEV.md](docs/PLUGIN_DEV.md)。
> **记录存放**:交付/实现记录与未实施规划一律写 DESIGN.md §14.1(交付表/未实施清单),TUI 交互细节归 docs/TUI_OPTIMIZE.md;本文件不记任务流水,只收规范与防错纪律。当前交付总览与未实施清单见 DESIGN.md §14.1。

## 项目定位
- 微内核 harness:core/ 仅含 ctx/event/plugin/config/boot,零 Agent 能力、零 UI。
- 一切能力以插件形式存在(plugins/ 下),经 bundle 装配,配置层(profile→bundle→patch)可随时插拔开关。
- 交付形态:单一静态二进制(`CGO_ENABLED=0`),运行时依赖 = 0(见 DESIGN.md §7)。

## 红线
- **插件只 import sdk/**,不 import core/、tui/ 或其它插件包(防 import 环;依赖经 Ctx 注入,宿主内部服务经 `system.registry`/`system.catalogue` 注入)。**两处已认可的例外**:① 宿主**内置**插件(`plugins/` 下)可经 `internal/*` 取宿主内部实现(先例 `host-internal-commands`→`internal/prefs`、`host-system-prompt`→`internal/instructions`);② 插件单测要搭真实 ctx/bus,import `core/ctx`、`core/event` 属正常(仅限 `_test.go`)。树外/外部插件仍只有 `sdk/`。已知遗留:`host-bridge` 生产代码 import `core/plugin`(取文件监听 `NewWatcher`),属待还的债。
- **注册即副作用,卸载即撤销**:任何 Provide/Subscribe/工具注册必须随 Disposer 撤销,disposers 幂等。
- **插拔安全**:运行期卸载不得破坏依赖者(`BlockedByLoaded` 拒绝被依赖插件的卸载);工具同名注册非静默(记警告)。
- **插件声明单一事实源是 `plugins/catalogue`**(provides/requires/bundle 归属);新增插件必须登记,装配层按配置树 enabled 启停。
- 缺失依赖显式失败,不静默降级。
- core/ 包必须带单测;新增分发模式/生命周期语义需测试覆盖。

## 便携纪律(运行数据单根,完全便携)
**一切 gah 自身产生的运行数据/配置/密钥/插件必须落在 `GAH_HOME` 单根下**(main 启动 Setenv GAH_HOME;便携模式 = 与 gah 二进制同级的 `gah-data/` 自动发现、**不存在则首启自动新建并释放初始化内容**,M16.9/P5.7)。目标:部署目录内 gah+gah-data(+start.sh)即完整,升级只替换 gah 单文件、数据随目录整体迁移。
- **新增任何写盘路径必须经 `$GAH_HOME` 派生**(boot 后经 os.Getenv 可用;注意它是**内部贯通变量**——数据根唯一 = 二进制同级 `gah-data/`,用户不可经 env 指定,2026-09-16 收紧),并给可审计的 Path() helper(便于盘点);禁止:硬编码 `~/.gah`、`UserHomeDir` 直拼、相对 cwd 写、系统根、二进制旁散目录、XDG 位置。
- 路径解析只允许一条链(与 cmd/gah homeDir 一致):**二进制同级 `gah-data/`(唯一数据根;不存在则首启自动新建)**——新代码不得另起解析链;`GAH_HOME env` 与 `~/.gah`/TempDir 均不作输入源(2026-09-16 收紧;homeDir 创建失败返回空 + boot 报错退出);运行态 GAH_HOME 由 boot Setenv 恒设,下游一律经该 env 派生;仅不经 cmd/gah 的嵌入/单测可能为空,宁回 TempDir 也不落 cwd/根。
- 密钥类配置(provider.yaml / search.yaml 等)一律入 `config/` 随目录迁移;启动环境变量(GAH_MCP_COMMANDS 等)放 `gah-data/env.sh` 由 start.sh source,**不得以 shell profile(~/.zshrc 等)作为唯一承载**。
- 新增数据子目录(记忆/计划/会话/任务等)一律 `$GAH_HOME/<name>/`;外部插件/UI 插件落 `$GAH_HOME/plugins/`、`$GAH_HOME/ui-plugins/`。
- **例外(不属于 gah 运行数据,不受本纪律约束)**:项目级技能 `.gah/skills/`(随仓库/git 走)、工作区写工具(tool-files 等)对用户明确要操作的文件、仓库源码与 seed 样板(config/bundle-*.yaml、internal/embed/seed 为生成源)。
- review/验收点:新代码若含 `os.WriteFile`/`MkdirAll`/`os.Create`,路径根必须可回溯到 GAH_HOME 链。

## 语言与工具
- Go 1.27+;golangci-lint;testify 可选,多数用例标准库断言即可。
- **提交前门禁**:`bash scripts/gate.sh`(gofmt + go vet + staticcheck,**双 module**;`--fast` 跳过 staticcheck)。它是 gofmt/vet/staticcheck 的**单一事实源**,CI 的「本地门禁同源」步骤直接调它 —— 2026-10-02 之前这三步只活在习惯里(连 gofmt 在 CI 都没有),代价是 SA4005 只在 CI 被逮到。
- **新增文件必须先 `git add` 再跑门禁**(2026-10-08 登记):`gate.sh` 的 gofmt 一步只检查 `git ls-files '*.go'` —— **未跟踪的文件它一个都不看**。新写的测试文件在 `git add` 之前跑门禁 ⇒ 本地 `GATE_OK` 是**假的**,CI 才红(那次红的就是两个新 `_test.go`)。凡本批有新增文件:`git add -A` → 跑门禁 → 再提交;「本地绿」这个词只有在**工作树已全部入索引**时才成立。与 v0.5.0/v0.5.1 同源的一类教训(门禁跑在错误的状态上),只是这次换成「文件还没进索引」。
- 发布编译:`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)"`。
- 桌面壳(`desktop/src-tauri/`):改后必跑 `cargo fmt` 与 `cargo test --offline`(CI 的 `desktop-shell` job = `cargo fmt --check` + `cargo test --locked`;`--offline` 是因为 crates.io 偶发拉取超时)。
- CI:go vet + go test ./... -race;全库测试必须保持 -race 全绿。

## UI 规范(web/tui)
- **web 端一切相关改动默认先读并遵循 `design-taste-frontend`(taste)skill**(见 ~/.pi/agent/skills/02-design/taste-skill/SKILL.md):设计读、三转盘、防 AI 俗套(emoji/默认紫渐变/三等高卡片/过度动效)、色彩一致性(单强调色 + 语义色)、形状一致性(圆角系统)、pre-flight 清单(无 em-dash、按钮对比度、CTA 单行、taste skill §14)。taste 不适用的应用型 UI 部分(数据密度/工作台)仍遵循其色彩/字体/形状纪律。
- 界面默认语言与观感对齐 DeepSeek Harness 设计语言:bluish 中性色阶 + DeepSeek 蓝 `#4176e6` 单强调色;token 定义在 `web-src/src/style.css`(圆角/边框/阴影分层),组件不得散写裸色值。
- 增删改等有副作用操作必须二次确认(居中弹层);会话流按消息类型(text/角色/工具/系统)给不同文字色或背景块;连接状态等实时信号放显眼处(状态栏),不放右下角弱位。
- 渲染层禁止 v-html(文本一律插值转义);web 前端依赖新增必须评审(taste skill §3.F)。

## 事件与配置约定
- 插件事件名用点分(`agent/pre-step`、`tools/pre-execute`),扩展点优先 waterfall(=veto 语义)。
- 配置条目 id = `<type>-<name>`,与 plugins/ 目录同名;config/bundle-*.yaml 为样板数据源。
- 斜杠命令注册进 ctx.commands(host-commands):Register 同名冲突拒绝、随 Disposer 撤销;新命令自动进 TUI 提示/help,不改 TUI 代码。

## 测试惯例
- 插件单测:同包 + 标准库断言;涉及跨插件装配用黑盒包(如 `host-plugin-manager` 的 `_test` 包)防 import 环。
- 端到端:tests/ 包直接装配 base bundle + 注入 `system.*` 服务,验 round 流程与"模型可见即已记录"不变量。
- 外部进程插件(host-bridge/mcp-bridge):测试内 `go build` 产物到临时目录再加载。
- tests 卸载矩阵:关闭 llm-anthropic-compat(防打真实 API),按依赖序卸载(先 host-bridge 再 host-jobs/host-fanout)。
- **覆盖率门**:`bash scripts/coverage-check.sh`(CI 同源;逐包棘轮 + 全局下限 + 零覆盖包禁止 + 豁免需理由,阈值只在脚本内)。回退即 CI 红;新增关键包请同步棘轮表(未能登记的棘轮包会被报“未出现在 profile”)。**本地跑必须带 `GAH_COVER_MERGE_DIR=<目录>`**(子进程覆盖:插桩 + 合并,否则 `cmd/gah` 的 `main()` 写不到计数器、必假红 —— 实测裸跑 45.9% vs 棘轮 69、加变量后 70.7;2026-10-05 起脚本会在红字里明示这一点,但别指望脚本替你跑)。
- **子进程覆盖(2026-10-02 补)**:只有**子进程**才跑得到的代码(`cmd/gah` 的 `main()`)原先永远不写 cover 计数器,在账上与「没测」不可区分。现在:测试用 `go build -cover` 插桩 + `GOCOVERDIR` 让子进程自己落盘,再由 `scripts/covermerge` 按区块计数相加并进主 profile;驱动方式是设 `GAH_COVER_MERGE_DIR=<目录>`(CI 已设),门会自动并入该目录下的 `*.child.out`,**不设 = 不启用**(拿不到数据 ≠ 覆盖率为 0)。本地命令:`mkdir -p /tmp/gah-cover-merge && GAH_COVER_MERGE_DIR=/tmp/gah-cover-merge bash scripts/coverage-check.sh`。实现与边界见 `internal/testutil/coverchild.go`。**仍然修不了的一类**:`plugins/tool/tool-shell@linux` 与 `internal/kernelsandbox@linux` 的 Landlock 自举 helper —— 它 **exec 掉自己**去跑目标命令,进程镜像被替换 ⇒ 退出处理不执行 ⇒ 计数器永不落盘(已用最小实验验证);要真弥合得让 helper 改成 fork+父进程等待,那是动**内核沙箱的施加路径**,已登记待议。
- **结构性稀释口径(2026-10-02 定,别只靠自觉)**:棘轮是**逐包绝对百分比**,一批给某包加几百行新代码会让它因**分母变大**而下滑 —— 与“旧测试被改坏”不可区分,只剩“降门(不允许)”与“在别处凑测试(掩盖真问题)”两个坏选择。规则三条(详见 `scripts/coverage-check.sh` 头注):① 棘轮只许上调;② 本批给某包**新增语句 > 200** 且下滑 ≥ 3pp ⇒ 判为结构性稀释,**必须补测本批新增部分**,补不满才允许写进脚本里的 `DILUTION` 表并写明“为什么这些行现在测不到”(不接受“下批补”);③ 判定入口 `bash scripts/coverage-check.sh --dilution <基线根> <基线sdk> <当前根> <当前sdk>`(开工那批前先存一份基线 profile)。
- **sdk 是独立 module**(`sdk/go.mod` + `go.work`):`go vet/staticcheck/test ./...` 从根目录跑**不含它**,新增/改动 sdk 时必须 `cd sdk && …`(CI 三步已同步补齐)。
- **跨平台(Windows)**:CI 有 `test-windows` job,macOS 本地跑绿**不代表**它绿。四个反复踩到的坑:① 不硬编码 `/tmp`(用 `t.TempDir()`);② Windows 的 shell 是 git-bash(MSYS):`C:\...` 里的反斜杠会被 shell 当转义吃掉(命令实际落到相对位置),要用 `/c/...` 表达绝对路径;③ `chmod 0555` 在 Windows 不产生只读语义(走 ACL),构造不出只读目录;④ `exec.LookPath` 按 PATHEXT 解析,无扩展名脚本不是可执行文件。**逐字节比较失败时先怀疑行尾**(CRLF 检出)。
  - ③ 的**具体变体**(2026-10-08 补,踩过一次):写「只读目录」类用例时,别拿 `os.Getuid() == 0` 当 Windows 的守卫 —— 它在 Windows 上返回 **-1**(非 0)⇒ 跳过不触发,断言照样跑并在 `test-windows` 红。**必须显式判 `runtime.GOOS == "windows"`**,且**拄同包已有用例的守卫**(如 `web.TestProbeWritableReadOnly`)而不是自己发明一个。本地 macOS 跑绿 + `GOOS=windows go vet` 通过 **都不算验过** —— vet 只验类型不验行为。
- **CI 计时门口径**:`全库 -race 计时回归护栏` 只覆盖**核心包**(`go list ./... | grep -v '/tests$'`),阈值 150s(本地基线 ~40s);`tests/` 包单独一步、**不设时长门** —— 它的耗时是 pty 交互 sleep 之和(随用例数线性增长),拿墙钟做回归判据会必然假红。

## 变更纪律
- **行尾钉死**:凡参与逐字节比较的仓库内文本 fixture(如 `desktop/src-tauri/fixtures/*`)必须写进 `.gitattributes`(`text eol=lf`)—— 否则 Windows 检出成 CRLF,该测试会在 `test-windows` 上假红(内容打印完全一致却比较失败)。
- **新增插件必须**:登记 `plugins/catalogue` + 落位 `plugins/<类别>/`(host/adapter/policy/tool/mcp/ui,总览 plugins/README.md)+ 同步 config 与 internal/embed/seed 两份 bundle 样板。
- **预置角色/技能**:形态(三道闸、配方表、契约、bump `SeedRolesVersion` 的流程)见 docs/PRESET_SKILLS.md;机器判定在 `internal/embed/preset_skills_contract_test.go`(宁少勿滥的机制化,与预置角色内容契约同一条纪律)。
- **样板版本化**:config/bundle-*.yaml 头部 `# seed-version: N`(seed 与 repo 两份同步,guard 测试强制);**新增 base 能力条目必须 bump 版本号**(EnsureSeed 对低版本落盘自动备份后覆盖,否则老用户不升级)。仅 bundle 系列参与;profile/patch 不覆盖。
- **外部化二进制**:extplugins/ 独立二进制经 `scripts/gen-extplugins.sh` 按发行矩阵生成(内含架构断言,生成后立即校验);改 embed 布局后必须重跑(embed 缺失主包构建失败);产物 `gzip -9 -n`(确定性,重跑无 diff);tests 经 embed.OpenExtPlugin 读本平台产物。
- **README 双语同步**:README.md(中文)与 README_EN.md(英文)互链同步维护——改动其中任一的功能描述/命令表/状态/目录/链接时,必须同步另一份(用户工作流,2026-09-16 起);新增面向用户的文档如涉及对外可见描述,一并考虑双语。
- **验收结论的权威只有一处**：`docs/VERIFY.md §剩余任务快照`（逐条验收状态）。`docs/TODO_OVERVIEW.md` 只做**索引与未完成项汇总**，**不得单独维护验收结论** —— 2026-10-02 发现两处口径打架：TODO_OVERVIEW 的 `G-X2` 行（写于 09-21）仍写「剩 4 条纯人工」，而 VERIFY 的 09-22 快照已是「剩 **0** 条纯人工」（用户实测通过 3 条 + 4 项裁决各关 1 条）。**改验收状态只改 VERIFY，随后同步 TODO_OVERVIEW 的那一行**；两份不一致时以 VERIFY 为准。
- **不入库的决策资料要定期导出**:DESIGN.md 与 `docs/*`(除 PLUGIN_DEV.md)按 2026-10-01 决定不入库(太大/只对本地开发有意义)⇒ 决策历史只存在于本机。**清盘、换机、删目录前先跑 `bash scripts/records-export.sh`**(导出到 `~/Documents/gah-records/<时间戳>/` + 生成 INDEX.md 索引,--verify 可只跑自检)。异地备份由用户决定(脚本不读凭据、不往远端传)。
