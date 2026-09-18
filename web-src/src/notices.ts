// NOND-N1 提示 toast 状态机(纯逻辑,零运行时依赖)。
//
// 数据源两条,载荷同形(Go sdk.Notice):
//   ① 实时 SSE 帧 `notice`(正在看页面时推来);
//   ② 页面加载/重连后的 `GET /api/notices?since=<id>` 回填(提示不进会话记录 → 错过就没了)。
// 两条来源**可能重叠**(回填返回的区间包含刚收到的实时帧),故一律按 id 去重。
//
// 三条纪律:
//   - **去重与游标只增不减**:id 单调,`since` 取 max(晚到的回填响应不得把游标拽回去)。
//   - **超窗裁剪显式计数**:可见上限 3 条,被挤掉的记 overflow(层里显示「还有 N 条未显示」),
//     不静默丢;提示本身在服务端缓冲 + 日志里仍有据可查。
//   - **warn/error 常驻**:须人看到;info 有 TTL 自动消失(不靠"用户点掉"来降低噪音)。
import type { Notice, NoticePage } from './types'

// Toast 一条可见提示(expiresAt=0 → 常驻,须显式关闭)。
export interface Toast extends Notice {
  expiresAt: number
}

// ToastState toast 层状态(组件持有;纯函数原地改,便于确定性测试)。
export interface ToastState {
  items: Toast[] // 可见(新→旧)
  seen: Set<number> // 已入列过的 id(去重)
  since: number // 回填游标(已见的最大 id)
  gap: boolean // 服务端标记:since 之后有提示被缓冲丢弃,回填不完整
  overflow: number // 被可见上限挤掉、未展示的条数
}

export const MAX_TOASTS = 3 // 可见上限(叠太多会盖住会话流)
export const INFO_TTL_MS = 6000 // info 自动消失(毫秒)
export const BACKFILL_WINDOW_MS = 30 * 60 * 1000 // 回填只取近 30 分钟(更早的打开页面时也早已失效)

export function newToasts(): ToastState {
  return { items: [], seen: new Set<number>(), since: 0, gap: false, overflow: 0 }
}

// levelClass 级别 → CSS 类名(未知值按 info 处理:不假装严重,也不丢样式)。
export function levelClass(level: string): 'info' | 'warn' | 'error' {
  return level === 'warn' || level === 'error' ? level : 'info'
}

// ttlFor 级别 → 存活时长(0 = 常驻)。
export function ttlFor(level: string): number {
  return levelClass(level) === 'info' ? INFO_TTL_MS : 0
}

// pushNotice 入列一条(去重 + 限窗)。返回是否真的新增。
export function pushNotice(st: ToastState, n: Notice, now: number): boolean {
  if (!n || typeof n.id !== 'number' || st.seen.has(n.id)) return false
  st.seen.add(n.id)
  if (n.id > st.since) st.since = n.id
  const ttl = ttlFor(n.level)
  st.items.unshift({ ...n, level: levelClass(n.level), expiresAt: ttl > 0 ? now + ttl : 0 })
  while (st.items.length > MAX_TOASTS) {
    st.items.pop() // 挤掉最旧的可见条
    st.overflow++
  }
  return true
}

// applyPage 应用一次回填(过滤过旧条目 + 去重),返回新增条数。
// 过旧 = ts 早于窗口:页面加载时把半小时前的提示再弹一遍只会淹没屏幕(服务端缓冲/日志仍可查),
// 但这些 id 同样记入 seen —— 回填已经「消费」过它们,后续同 id 的实时帧不再重复打扰。
export function applyPage(st: ToastState, page: NoticePage | null | undefined, now: number): number {
  if (!page) return 0
  st.gap = st.gap || !!page.gap
  let added = 0
  for (const n of page.items ?? []) {
    if (isStale(n, now)) {
      st.seen.add(n.id)
      continue
    }
    if (pushNotice(st, n, now)) added++
  }
  if (typeof page.max_id === 'number' && page.max_id > st.since) st.since = page.max_id
  return added
}

// isStale ts 缺失或不可解析时**不当过旧**(宁可多显示一条,不因解析失败静默吞掉)。
export function isStale(n: Notice, now: number): boolean {
  const t = Date.parse(n.ts ?? '')
  if (Number.isNaN(t)) return false
  return now - t > BACKFILL_WINDOW_MS
}

// expire 清理到期 info(定时器每 tick 调一次;到期即消失,不吃点击)。
export function expire(st: ToastState, now: number): void {
  st.items = st.items.filter((t) => t.expiresAt === 0 || t.expiresAt > now)
}

// dismiss 显式关闭一条(warn/error 只能这样消失)。
export function dismiss(st: ToastState, id: number): void {
  st.items = st.items.filter((t) => t.id !== id)
}

// hasPending 是否还有未展示的溢出条(层里据此提示,而不是假装只剩这些)。
export function hasPending(st: ToastState): boolean {
  return st.overflow > 0
}
