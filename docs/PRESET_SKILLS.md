# 预置角色技能（维护手册）

> 预置角色 = 随 `gah` 释放、用户可改可删的数据（`internal/embed/seed/roles/`）。
> 预置技能 = 放在**角色目录下的私有 `skills/`**，随角色一起释放。
> 面向：往里加技能的人。机器可判的部分在 `internal/embed/preset_skills_contract_test.go`，
> 本文说清**不可机判**的部分（配方、三道闸、授权口径）。

## 1. 形态与理由

**技能随角色目录走，不塞共享库。** 理由有两条：

1. 「这个角色会用什么技能」在角色目录里一眼看得清；
2. 引用共享库的话，用户删掉某个技能会让角色的挂载整体**失效**（面板上挂着、
   模型读不到）；私有技能删了只是少一条可见。

`role.yaml` 的 `skills:` 键**替换**默认池（不是追加）。预置角色**不写**
`skills_inherit: true` —— 那会把用户自己的技能并进来，预置内容不该预设别人装了什么。

## 2. 三道闸（移植/新写都过这三关）

| 闸 | 判据 | 会被拦下的典型 |
|---|---|---|
| ① **gah 已有等价能力不配** | 已有 `/compact`、`/recap`、变更审查面、文档预览、`session_search`… | `summarize`、diff 类、文档转 Markdown |
| ② **依赖拿不到就是空壳** | 预置技能一律**纯提示词、零外部依赖**。技能里写的 CLI 在 gah 里没有 ⇒ 空壳 | `tavily-search`（要 key+CLI）、`markitdown`（要 Python 包） |
| ③ **提示注入面永不进默认池** | 「强制先读我」「必须先执行 X」「忽略之前的指令」是典型注入模式 | 生态里下载量很高的 `web-tools-guide` 就是这种 |

闸②③ 有黑名单在契约测试里机判；闸① 只能人工过（对着 §3 的配方表看）。

**授权口径**：预置技能正文**一律原创**。参考生态的**能力分类与触发词设计**，
不抄正文 —— 生态 `SKILL.md` 是第三方创作物，授权各异，抄进来有法律问题。

## 3. 当前配方（12 角色 × 2 技能 = 24 条）

| 角色 | 技能 | 管什么 |
|---|---|---|
| 通用助理 | `clarify-requirement` / `task-breakdown` | 把含糊诉求问清成可验收任务 / 拆成带判据的小步 |
| 编程大师 | `minimal-change` / `self-check-list` | 最小必要改动 / 交付前自查清单 |
| 技术负责人 | `option-tradeoff` / `task-criteria` | 方案对比（改动面·代价·回滚）/ 分活必须带判据 |
| 代码评审 | `severity-report` / `counterexample` | 只报有证据的问题 / 没触发路径的意见不算问题 |
| 运维值守 | `stop-loss-runbook` / `timeline-postmortem` | 先止损、每步写撤销 / 按时间线复盘 |
| 数据分析 | `metric-alignment` / `cleaning-report` | 口径四要素（定义·时间窗·分母·来源）/ 清洗要对账 |
| 财务 | `three-statement-tie` / `number-provenance` | 三表勾稽自检 / 每个数字可追到来源 |
| 小说家 | `character-consistency` / `pacing-check` | 人物四条线逐章对照 / 连续三段没变化就收 |
| 新闻撰稿人 | `inverted-pyramid` / `fact-check-list` | 结论—依据—背景三层 / 发稿前逐项对来源 |
| 技术文档 | `runnable-commands` / `prerequisites` | 能复制粘贴 + 给预期输出 / 前置条件每条可确认 |
| 翻译 | `glossary-consistency` / `ambiguity-parallel` | 术语表是唯一事实源 / 歧义两解并列不替读者决定 |
| 技术导师 | `diagnose-first` / `progressive-drill` | 先定位卡在哪一层 / 每题只加一个难点 |

## 4. 契约（会被测试挡下）

每条预置 `SKILL.md` 必须同时满足（`TestPresetSkillContract`）：

- frontmatter `name` == 目录名；`description` 非空且**一句话**（≤60 字）；
- `trigger` ≥ 2 条；
- 正文 ≥ 3 个 `## ` 小节、≥ 30 行、含「示例」小节；
- 不命中注入黑名单、不把外部 CLI 当前提；
- **技能名跨角色不重名** —— 技能是全局按名字 first-wins 去重，同名会互相压制。

外加（`TestPresetRoleSkillsResolve` / `TestPresetSkillNoOrphan`）：

- 每个预置角色**至少 1 条**技能；
- `role.yaml` 的声明与磁盘**双向一致**（无孤立目录、无失效挂载）。

## 5. 新增一条的流程

1. 过三道闸（§2），闸① 对着配方表确认 gah 没有等价能力。
2. 写 `seed/roles/<角色>/skills/<name>/SKILL.md`：frontmatter + ≥3 小节 + 最小示例。
3. 在该角色 `role.yaml` 的 `skills:` 下加名字（**追加到列表末尾**，别重排）。
4. `SeedRolesVersion` **bump 一格**（`internal/embed/roles.go`）—— 内容变了要能被盘点出来。
5. 跑 `go test ./internal/embed/ ./plugins/host/host-skills/`，两条护栏都要绿：
   - 契约测试（§4）
   - `TestPresetRolePrivateSkillsScanned`（「文件释放了」与「扫得到」是两件事）
6. 更新本文 §3 的配方表。

## 6. 已知边界（不是待办）

- **预置内容的修订到不了老用户**：角色目录已存在时整体跳过（不覆盖用户改过的
  `AGENTS.md`/`role.yaml`）。要给老用户某角色的新预置技能，可靠路径是删掉该角色
  目录后重放（删除进 `.trash`，可恢复）。刻意不选「覆盖 role.yaml」或
  「只追加技能文件」（后者文件到了但没挂载，更费解）。
- 预置技能**不进共享库默认池**：它们只在挂了对应角色时可见。
