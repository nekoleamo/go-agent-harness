# docs 文档导航(单一入口)

> 项目规划/规约/交付记录分散在各文档,本文件为统一索引,避免散乱。
> **交付总览以 DESIGN.md §14.1 交付表为准**;跨文档状态冲突时以 DESIGN 为准。

## 核心文档
| 文档 | 内容 |
|---|---|
| [README.md](../README.md) | 项目门面:定位/功能特性/命令/配置/使用(英文版 [README_EN.md](../README_EN.md),中英同步) |
| [DESIGN.md](../DESIGN.md) | 完整设计文档;**§14.1 交付表/未实施清单 = 交付记录单一入口** |
| [AGENTS.md](../AGENTS.md) | 项目硬规范(红线/便携纪律/UI 规范/测试惯例/变更纪律/README 双语同步) |

## 规划与专项
| 文档 | 内容 | 何时看 |
|---|---|---|
| [ROADMAP.md](ROADMAP.md) | 里程碑实施排期(P0–P3 阶段、依赖、验收;历史交付亦登记) | 下一步做什么/排期 |
| [TODO_OVERVIEW.md](TODO_OVERVIEW.md) | 待办统筹单页(待办/暂缓/已闭环 + 状态与顺序) | 当前哪些没做/排到哪 |
| [VERIFY.md](VERIFY.md) | 真机验证清单(交互类逐项勾选;Web/桌面节含协议级自动化对照) | 发版前人工验收 |
| [TUI_OPTIMIZE.md](TUI_OPTIMIZE.md) | TUI 交互优化专项(S1–S3 已交付项与滚动/搜索/划选实现细节) | TUI 相关需求/改动 |
| [WEB_ATTACHMENTS_PLAN.md](WEB_ATTACHMENTS_PLAN.md) | Web 附件(图片/文件)+ 输入快捷键规划(F1–F7) | Web 附件/快捷键 |
| [DESKTOP_FEASIBILITY.md](DESKTOP_FEASIBILITY.md) | 桌面壳可行性报告 + 实施蓝图(spike/架构/数据根/分发/决策记录) | 桌面壳相关 |
| [RELEASE.md](RELEASE.md) | 桌面零成本发行(updater ed25519 自持签名/发布脚本/CI 矩阵/无签名首次放行) | 发桌面版/改发布流水线 |
| [PI_COMPARISON.md](PI_COMPARISON.md) | pi vs gah 对比基线(改进点按价值×成本评级,实施落 DESIGN) | 参考 pi 做体验改进 |

## 开发规约
| 文档 | 内容 |
|---|---|
| [PLUGIN_DEV.md](PLUGIN_DEV.md) | 插件开发规范(红线:只 import sdk、注册即副作用、catalogue 单一事实源、样板演进) |
| [plugins/README.md](../plugins/README.md) | 插件类别总览(host/adapter/policy/tool/mcp/ui;catalogue 为事实源) |

## 变更约定(与 AGENTS「变更纪律」对齐)
- **交付登记**:新交付写 DESIGN §14.1(交付表或未实施清单区),专项细节入对应专项文档并标 ✅/日期;代码同步(登记/样板/测试)先行。
- **规划变化**:更新 ROADMAP 状态列(✅/进行中/未动)与 TODO_OVERVIEW 状态。
- **README 双语**:README.md 与 README_EN.md 同步维护(AGENTS「README 双语同步」)。
