// S-P2-2 看板视图数据层:把已在手的三份投影(宿主状态 / 轨迹 / 变更)+ 后台任务 + 定时计划
// 聚合成卡片。数据全部来自既有事件账本与既有 REST,**不新增后端契约、不占二进制**。
//
// 为什么是独立模块:self-contained(除 `import type` 外零运行时依赖)——与 traj.ts / changes.ts /
// streamwin.ts 同规矩,`node --test` 可直接跑本文件。数字与时长的小格式化函数因此在本文件内自带
// (口径与 traj.ts 的同名函数一致):运行时 import './traj' 会让本模块不再 self-contained。
import type { StateView } from './types'

export type BoardLevel = 'ok' | 'warn' | 'err' | 'off'

export interface BoardRow {
  label: string
  value: string
  /** 语义色档位:仅用于真正的状态信号(正常/进行中/失败),不做装饰。 */
  level?: BoardLevel
}

/** 卡片动作目标:由 App.vue 解释(打开抽屉 / 切视图 / 打开设置)。 */
export type BoardTarget = 'jobs' | 'changes' | 'traj' | 'settings'

export interface BoardCard {
  id: string
  title: string
  rows: BoardRow[]
  action?: { label: string; target: BoardTarget }
  /** 口径说明:与别的视图/命令口径不同的地方必须写清(不假精确)。 */
  note?: string
}

/** 看板输入:三份投影的聚合量(jobs/plan 的展示文本由调用方格式化,避免本模块依赖 schedule.ts)。 */
export interface BoardInput {
  state: StateView
  traj: { turns: number; running: boolean; ms?: number; tokens: number; cached: number; tools: number; failed: number }
  changes: { files: number; added: number; removed: number; count: number }
  jobs: { running: number; total: number; latest?: string }
  plan: { total: number; enabled: number; next?: string; last?: string }
}

/** 卡片规范顺序(也是首次打开看板时的默认顺序)。 */
export const BOARD_CARDS = ['usage', 'turns', 'jobs', 'changes', 'plan'] as const

// —— 格式化(与 traj.ts / StatusBar 口径一致) ——

/** fmtTok token 计数:1000 以下原样,千位 1 位小数 K,百万位 2 位小数 M。 */
export function fmtTok(n: number): string {
  if (!isFinite(n) || n <= 0) return '0'
  if (n < 1000) return String(n)
  if (n < 1000000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1000000).toFixed(2)}M`
}

/** fmtMs 时长:1s 以下毫秒,1min 以下秒(1 位小数),以上 XmYs。 */
export function fmtMs(ms?: number): string {
  if (ms === undefined) return ''
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const min = Math.floor(s / 60)
  return `${min}m${(s - min * 60).toFixed(0)}s`
}

/** pct 占比(分母 0 → 0,不假精确)。 */
export function pct(part: number, whole: number): number {
  if (whole <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((part / whole) * 100)))
}

/** fmtDur 时长单元格:undefined(回合进行中)= 进行中。 */
function fmtDur(ms?: number): string {
  return ms === undefined ? '进行中' : fmtMs(ms)
}

/** jobStateLabel 任务状态中文名(看板卡与任务面板共用同一口径,不各写一份)。 */
export function jobStateLabel(s: string): string {
  return { running: '运行中', done: '完成', failed: '失败', killed: '已终止' }[s] ?? s
}

// —— 卡片派生 ——

/** boardCards 按规范顺序生成卡片(纯函数:同一输入同一输出)。 */
export function boardCards(inp: BoardInput): BoardCard[] {
  const s = inp.state
  const st = s.stats
  const used = st.PromptTokens + st.CompletionTokens
  const cachePct = pct(st.CachedTokens, st.PromptTokens)
  const avgTok = inp.traj.turns > 0 ? Math.round(inp.traj.tokens / inp.traj.turns) : 0

  return [
    {
      id: 'usage',
      title: '用量',
      rows: [
        { label: '输入', value: fmtTok(st.PromptTokens) },
        { label: '输出', value: fmtTok(st.CompletionTokens) },
        { label: '缓存', value: st.CachedTokens > 0 ? `${fmtTok(st.CachedTokens)} · ${cachePct}%` : '0' },
        { label: '请求', value: String(st.Requests) },
        {
          label: '上下文',
          value:
            st.Window > 0
              ? `${fmtTok(used)}/${fmtTok(st.Window)} · ${pct(used, st.Window)}%`
              : used > 0
                ? `${fmtTok(used)}(窗口未知)`
                : '–',
          level: st.Window > 0 && pct(used, st.Window) >= 90 ? 'warn' : undefined,
        },
      ],
      note: '累计口径(整个会话);缓存 = 命中缓存的输入占比',
    },
    {
      id: 'turns',
      title: '回合',
      rows: [
        { label: '已完成', value: String(inp.traj.turns) },
        { label: '总时长', value: fmtDur(inp.traj.ms) },
        { label: '工具调用', value: String(inp.traj.tools) },
        {
          label: '工具失败',
          value: String(inp.traj.failed),
          level: inp.traj.failed > 0 ? 'err' : undefined,
        },
        { label: '平均 token', value: avgTok > 0 ? fmtTok(avgTok) : '–' },
      ],
      action: { label: '查看轨迹', target: 'traj' },
      note: inp.traj.running
        ? '有回合进行中:已完成数不含它,总时长要等它结束才完整'
        : '已完成回合数与工具调用涵盖整场会话',
    },
    {
      id: 'jobs',
      title: '后台任务',
      rows: [
        {
          label: '运行中',
          value: String(inp.jobs.running),
          level: inp.jobs.running > 0 ? 'warn' : undefined,
        },
        { label: '记录', value: String(inp.jobs.total) },
        { label: '最近', value: inp.jobs.latest || '–' },
      ],
      action: { label: '打开任务面板', target: 'jobs' },
    },
    {
      id: 'changes',
      title: '文件变更',
      rows: [
        { label: '文件', value: String(inp.changes.files) },
        { label: '新增行', value: '+' + String(inp.changes.added), level: inp.changes.added > 0 ? 'ok' : undefined },
        { label: '删除行', value: '−' + String(inp.changes.removed), level: inp.changes.removed > 0 ? 'err' : undefined },
        { label: '改动次数', value: String(inp.changes.count) },
      ],
      action: { label: '查看变更', target: 'changes' },
      note: '来自工具写盘旁路(不依赖 git),只含本次会话经工具改过的文件',
    },
    {
      id: 'plan',
      title: '定时计划',
      rows: [
        { label: '启用', value: `${inp.plan.enabled}/${inp.plan.total}` },
        { label: '下次运行', value: inp.plan.next || '未排期' },
        { label: '最近终态', value: inp.plan.last || '–' },
      ],
      action: { label: '管理计划', target: 'settings' },
    },
  ]
}

// —— 布局(pin/排序/隐藏;持久化由调用方负责) ——

export interface BoardLayout {
  /** 显示顺序(始终包含全部已知卡片,未知 id 已被丢弃)。 */
  order: string[]
  /** 已隐藏的卡片 id。 */
  hidden: string[]
}

export function newBoardLayout(ids: readonly string[] = BOARD_CARDS): BoardLayout {
  return { order: [...ids], hidden: [] }
}

/** normalizeBoard 收敛任意来源的布局:未知 id 丢弃、缺失 id 追加尾部(加卡片后自动出现)。 */
export function normalizeBoard(raw: unknown, ids: readonly string[] = BOARD_CARDS): BoardLayout {
  const known = new Set(ids)
  const seen = new Set<string>()
  const order: string[] = []
  const hidden: string[] = []
  const o = (raw as { order?: unknown } | null | undefined)?.order
  if (Array.isArray(o)) {
    for (const v of o) {
      if (typeof v === 'string' && known.has(v) && !seen.has(v)) {
        seen.add(v)
        order.push(v)
      }
    }
  }
  const h = (raw as { hidden?: unknown } | null | undefined)?.hidden
  if (Array.isArray(h)) {
    for (const v of h) {
      if (typeof v === 'string' && known.has(v) && !hidden.includes(v)) hidden.push(v)
    }
  }
  for (const id of ids) if (!seen.has(id)) order.push(id)
  return { order, hidden }
}

/** parseBoard 读持久化布局(JSON 坏值/类型不符 → 默认布局;无痕模式由调用方兜住)。 */
export function parseBoard(raw: string | null | undefined, ids: readonly string[] = BOARD_CARDS): BoardLayout {
  if (!raw) return newBoardLayout(ids)
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return newBoardLayout(ids)
  }
  return normalizeBoard(parsed, ids)
}

export function serializeBoard(l: BoardLayout): string {
  return JSON.stringify({ order: l.order, hidden: l.hidden })
}

/** toggleCard 显示/隐藏一张卡片(未知 id 忽略)。 */
export function toggleCard(l: BoardLayout, id: string): BoardLayout {
  if (!l.order.includes(id)) return l
  const hidden = l.hidden.includes(id) ? l.hidden.filter((x) => x !== id) : [...l.hidden, id]
  return { order: [...l.order], hidden }
}

/** moveCard 在 order 内上移/下移一格(dir = -1 上移,+1 下移;越界不动)。 */
export function moveCard(l: BoardLayout, id: string, dir: -1 | 1): BoardLayout {
  const i = l.order.indexOf(id)
  if (i < 0) return l
  const j = i + dir
  if (j < 0 || j >= l.order.length) return l
  const order = [...l.order]
  order[i] = order[j]
  order[j] = id
  return { order, hidden: [...l.hidden] }
}

/** visibleCards 按布局排序并过滤已隐藏项(卡片缺失时跳过,不报错)。 */
export function visibleCards(cards: BoardCard[], l: BoardLayout): BoardCard[] {
  const by = new Map(cards.map((c) => [c.id, c]))
  const hidden = new Set(l.hidden)
  const out: BoardCard[] = []
  for (const id of l.order) {
    if (hidden.has(id)) continue
    const c = by.get(id)
    if (c) out.push(c)
  }
  return out
}
