// S-P1-2 长会话窗口(纯逻辑,零运行时依赖,可 node --test 直跑)。
//
// 三件事:
//   1. 首帧基线:服务端首连只回放尾部窗口(baseline 帧),前端把窗口边界写进模型;
//   2. 上滚分页:以模型最老事件 Seq 为 before 游标向服务端要更早一页,拼到头部,
//      按 Seq 去重(拼接点两侧可能重叠一条回合起点);
//   3. 贴底裁剪:长会话持续输出时,只在用户**贴底阅读**时裁掉头部消息,把 DOM 与内存
//      压在有界范围内;被裁掉的历史仍可从服务端分页取回(不丢数据,只挪出窗口)。
//
// 不做 JS 虚拟滚动:消息高度不定(代码块/图片/折叠),测高成本远高于收益;
// DOM 数量由「窗口裁剪」直接控制,浏览器侧用 content-visibility 跳过屏外渲染。
import type { Msg, StreamModel } from './sse'
import type { Baseline } from './types'

// 窗口内保留的消息条数上限(仅贴底时裁剪)。800 条 ≈ 一两百回合,足够回溯当前工作上下文;
// 再往前的内容走「上滚加载」(服务端事件账本是全量,窗口只是视图)。
export const MAX_LIVE_MSGS = 800
// 距顶部多少像素内触发上滚加载(留一屏余量,避免贴边抖动反复触发)
export const LOAD_EARLIER_PX = 240

// 应用首帧基线:记录窗口边界。空会话(from/to = 0)时 hasMore=false、from=0(不触发分页)。
export function applyBaseline(m: StreamModel, b: Baseline): void {
  m.from = b.from
  m.hasMore = b.has_more
}

// 裁剪头部消息至 cap 条,返回丢弃条数。
// 游标同步**前移**到剩余最老消息的 Seq:被裁掉的回合(Seq 更小)因此仍在分页范围内,
// 上滚取回时会被 prependMsgs 保留(DOM 不再持有,数据不丢)。
// 调用方负责只在「用户贴底」时调用:否则会抽掉用户正在读的内容。
export function trimHead(m: StreamModel, cap = MAX_LIVE_MSGS): number {
  if (m.msgs.length <= cap) return 0
  const drop = m.msgs.length - cap
  m.msgs.splice(0, drop)
  m.trimmed += drop
  m.hasMore = true // 被裁掉的部分就是「更早历史」
  const first = m.msgs[0]
  if (first) m.from = first.seq
  return drop
}

// 已加载的最老**消息** Seq(0 = 无消息)。仅用于展示/测试:分页游标用 m.from(事件 Seq)。
export function oldestSeq(m: StreamModel): number {
  return m.msgs.length > 0 ? m.msgs[0].seq : 0
}

// 把更早的消息拼到头部,返回实际新增条数。
// 去重口径:seq >= 现有最老消息 seq 的一律丢弃(分页是「向前」拉的,重复只可能出现在拼接点)。
export function prependMsgs(m: StreamModel, older: Msg[], from: number): number {
  let cut = older.length
  const keepFrom = oldestSeq(m)
  if (keepFrom > 0) {
    while (cut > 0 && older[cut - 1].seq >= keepFrom) cut--
  }
  const add = older.slice(0, cut)
  if (add.length > 0) m.msgs.unshift(...add)
  // 游标只前进(from 越小越老):即使本页一条消息都没产出(全是非消息事件),也要推进,
  // 否则上滚会卡在同一页死循环。
  if (from > 0 && (m.from === 0 || from < m.from)) m.from = from
  return add.length
}

// 是否应触发上滚加载(纯判定,供滚动事件与测试共用)。
export function shouldLoadEarlier(el: { scrollTop: number } | null, hasMore: boolean, loading: boolean): boolean {
  if (!el || loading || !hasMore) return false
  return el.scrollTop < LOAD_EARLIER_PX
}

// 顶部提示文案(窗口被裁剪/服务端还有更早历史时显示;空串 = 不显示)。
export function earlierHint(m: { hasMore: boolean; trimmed: number }): string {
  if (!m.hasMore) return ''
  if (m.trimmed > 0) return `已折叠 ${m.trimmed} 条更早消息 · 上滚加载`
  return '更早的消息未加载 · 上滚加载'
}

// 窗口是否不完整(轨迹/变更视图据此标注口径:窗口内的统计不等于会话全程)。
export function windowPartial(m: { hasMore: boolean; trimmed: number }): boolean {
  return m.hasMore || m.trimmed > 0
}
