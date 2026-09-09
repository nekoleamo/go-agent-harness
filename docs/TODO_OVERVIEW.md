# gah 待完成项统筹总览(单页)

> 全部未完成/待办/暂缓事项的**单一入口**(2026-09-06 统筹)。各专项细节见对应文档;本文档只做**排序与状态**。
> 状态图例:`🧭 待执行`(已确认、按序开工)/ `⏳ 暂缓或按需` / `🚧 规划定稿` / `✅ 已闭环`。
> 权威细节:DESIGN §14.1 交付表与未实施清单、docs/ROADMAP.md、docs/WEB_ATTACHMENTS_PLAN.md、docs/DESKTOP_FEASIBILITY.md。

## 执行总览(2026-09-16 更新:Q/R 组三端检查全交付;未实施清单清零;待办仅 C 组发行流程明确暂缓)

```
Q 组:2026-09-09 三端全面检查登记 → 2026-09-16 全交付 ✅(Q1 轮询/~ 展开/Q2 token 化/Q3 注释加固/Q4 提交;R5 复查 + R6 观察项完善),详见 DESIGN §14.1 未实施清单
```

```
1. A 组:Web 附件(图片/文件)+ 快捷键          ✅ 已交付(附件/多模态/前端/快捷键/TUI 键/文档)
2. ROADMAP 冲突行修订 + A6 文档登记            ✅ 已交付(图片富媒体撤销标记、DESIGN/VERIFY 登记)
3. 穿插小项:B2 export / C0 GAH_WEB_ADDR / B4 mcp 看护   ✅ 已交付
4. B1 jobs 面板 + B3 命令下沉                  ✅ 已交付( JobsPanel;11 命令下宿主/TUI 判重/事件联动)
5. B5 UI 槽位 v2                              ✅ 已交付(registry v2 三扩展点/落点/示例插件)
6. C1 桌面壳 P1 ✅(已交付);C2/C3 **零成本方案工程侧已铺(2026-09-16)**:updater 接入 + 发布脚本 + CI 矩阵骨架,待 repo 公开 + 首个 tag v* 启用(不购证书)
```

> 当前待办(= 🚧):**C2/C3 零成本发行工程侧已铺(2026-09-16)**,剩余:repo 公开(用户操作)+ 首个 tag v* 触发验证 + win runner 真机调 NSIS。A/B/C0/C1/Q/R/M17/M18/policy-guard 历史交付见下各表与 DESIGN §14.1,未实施清单已清零。

## A 组 · Web 附件/输入快捷键(✅ 已交付 2026-09,登记 DESIGN §14.1 / VERIFY)

规划:docs/WEB_ATTACHMENTS_PLAN.md(F1–F7;看图一期 / 落盘 GAH_HOME/attachments / Esc+全选 / TUI 兼容矩阵 全部落地)。

| 编号 | 项 | 量级 | 状态 |
|---|---|---|---|
| A1 | F1 后端附件上传/托管/静态预览(+单测) | S–M | ✅ |
| A2 | F3 多模态注入(sdk 消息升级 + openai/anthropic 适配器 + jsonl;视觉=常开) | L | ✅ |
| A3 | F2 前端附件 UI(chip/拖放/粘贴/图片渲染) | M | ✅ |
| A4 | F4 web 快捷键(Esc 清空 / Cmd+Enter / 平台归一) | S | ✅ |
| A5 | F5 TUI 键补全(Ctrl+A/B/F/Y)+ F6 兼容矩阵 | M | ✅ |
| A6 | F7 文档纪律登记(DESIGN §14.1 / VERIFY 矩阵 / 本文档) | S | ✅ |

## B 组 · DESIGN §14.1 二期(B1–B5 全部 ✅ 已交付 2026-09)

| 编号 | 项 | 量级 | 建议 |
|---|---|---|---|
| B1 | Web jobs 任务面板 | M | ✅ 已交付 2026-09:JobsPanel(状态栏任务钮+运行徽标+列表/状态色/输出展开/终止)+ 3s 轮询 + api.jobs/jobKill |
| B2 | Web 会话导出 /export | S | ✅ 已交付 2026-09:GET /api/sessions/{id}/export(原始 jsonl 下载)+ 侧栏 ⤓ 按钮 + 单测 |
| B3 | TUI 内部命令下沉宿主(web `/` 通用) | L | ✅ 已交付 2026-09:host-internal-commands 宿主插件承载 11 命令(thinking/model/provider/sandbox/plugins/settings/export/compact/workspace/session/reload);host-cwd-sessions 增 cwd/session-switched 事件;TUI 注册判重跳过+事件订阅驱动刷新;catalogue/bundle-base(seed 8→9)登记;web /api/commands 含 11 命令、UI 专属零误下沉、真机 /thinking off 生效;全库 43 包 -race 绿 |
| B4 | mcp-bridge server 崩溃看护 | M | ✅ 已交付 2026-09:holder.supervise(60s 节流 respawn)+ mcpTool 经 current() 恒取活动连接 + TestHolderRespawnOnCrash |
| B5 | UI 槽位 v2(设置/侧栏/附加面板扩展点) | L | ✅ 已交付 2026-09:registry v2 三扩展点(settings-section/sidebar-action/extra-panel,多实例追加)+ 加载器分派 + 三落点(SettingsPanel/Sidebar/App 抽屉)+ 安装侧白名单扩 + extension-demo 示例;全库 43 包 -race 绿(登记 DESIGN「B5 UI 槽位 v2」)

## C 组 · 桌面壳(C0/C1 壳工程 ✅;发行 C2/C3 零成本方案 🚧 工程侧已铺 2026-09-16)

规划:docs/DESKTOP_FEASIBILITY.md;前置已交付:`POST /api/shutdown`(跨平台优雅停机,已登记 §14.1)。

| 编号 | 项 | 量级 | 依赖 |
|---|---|---|---|
| C0 | gah 侧 GAH_WEB_ADDR 动态端口(桌面前置小改) | S | ✅ 已交付(ui-web-app 读 env,真机 2244 端口验证)
| C1 | desktop/ Tauri 壳工程(退出链/托盘/通知/自启/单实例) | L | ✅ 已交付 2026-09(P1):desktop/src-tauri 壳(spawn/轮询/navigate/单实例/托盘/自启/通知/退出链)+ scripts/gen-desktop.sh + 真机验证(sidecar/渲染/shutdown 全链);C2 签名 CI、C3 updater 属 P2/P3 |
| C2 | mac 签名公证 + win NSIS + CI 矩阵 | L | 🚧 **零成本方案工程侧已铺(2026-09-16)**:不购 Apple/微软证书;publish-desktop.sh + release-desktop.yml(公开 repo 免费 runner);待 repo 公开 + 首个 tag v* 启用 |
| C3 | updater 更新通道(自托管 + 签名) | M | 🚧 **已接入零成本**(2026-09-16):tauri-plugin-updater + ed25519 自持签名(~/.tauri/gah.key)+ GitHub Releases 为端点(conf 已配)+ 托盘「检查更新」;发布时随 CI 生成 latest.json |

## Q 组 · 三端全面检查(**已交付** ✅ 2026-09-16,登记 DESIGN §14.1 未实施清单)

| 编号 | 项 | 量级 | 状态 |
|---|---|---|---|
| Q1 | 三端 bug 修复:JobsPanel 轮询不随开合(改 watch(open) 启停)+ host-backup `/backup ~` 未展开(复用 ~ 展开) | S | ✅ |
| Q2 | web 端散写裸色值 token 化(29 处;补语义 soft token --ok-soft/--err-soft/--tool-soft/--overlay/--fg-on-accent,taste 评审) | M | ✅ |
| Q3 | 注释与加固:desktop 注释同步便携根 gah-data + web SSE `retry:`/`nosniff` 头 | S | ✅ |
| Q4 | 提交纪律:Q1–Q3 + M17/M18 + seed-version 11 一并落库(2e69e16) | S | ✅ |
| R5 | 三端复查(承接 56a0267 弃用 ~/.gah):uiPluginsHome/themeHome/pluginHome 旧解析链收敛 + desktop 注释 + AGENTS.md 便携纪律同步 | M | ✅ |
| R6 | 观察项完善:SSE 重放/订阅 gap 修复(先订阅后重放+seq 去重)+ WS 指数退避重连 + 桌面壳数据根(appDataHome)与失败提示 | M | ✅ |

## 已闭环(2026-09,不计入待办)

- Tauri sidecar spike 验证(mac arm64:直连/SSE/WS/sidecar/退出全链)→ docs/DESKTOP_FEASIBILITY.md §5
- `POST /api/shutdown` 优雅停机端点(Web.OnShutdown + ui-web-app 绑定 + 单测 + 真机端到端)→ DESIGN §14.1 交付表 + VERIFY.md
- **M17 审批等级三档**:policy-approval 扩三档(open 放行 / smart 默认弹确认 / strict 直接拒绝)+ sdk.ApprovalService + TUI `/approval` + Web 设置面板「审批」分段 + 偏好持久化 + bundle mode 配置(seed 10)→ DESIGN §14.1 M17
- **M18 整体备份/恢复**:新插件 host-backup(`/backup` 一键打包 GAH_HOME 确定性 tar.gz,排除 backups/ 自身;/backup list|restore,恢复前自动先备份当前态 + 越界拒绝)+ web `/api/backup` + 设置面板「数据备份」区段 + catalogue/bundle(seed 10→11)→ DESIGN §14.1 M18
- **policy-guard 融合**(并行会话,2026-09):policy-approval + policy-sandbox 合并为单插件统一裁决点(seed 12)→ DESIGN §14.1 交付表 + R5 登记
- **R5 三端复查 / R6 观察项完善**(2026-09-16):旧解析链收敛、SSE gap、WS 重连、桌面数据根 → DESIGN §14.1 R5/R6 登记

## 决策/冲突登记

- **2026-09-06 撤销**:ROADMAP「明确不做/远期」中的**图片富媒体**一项——已转为 Web 附件一期(含多模态看图),同步修订于 ROADMAP L95。
- 维持不做:S3.1 TUI scrollback 双形态(方案 A)、全量键位自定义。