// S-P1-1 变更审查面:会话事件账本 → 按文件聚合的改动模型(纯逻辑,零运行时依赖)。
//
// 数据源:`file/change` 会话事件(由工具写盘前后自取内容算出 unified diff 落账本)。
// 与 traj.ts 同一纪律:只做投影,不发明数据 —— 计数、patch、截断标记全部来自事件;
// 视图层负责渲染。**不依赖 git**:即使工作区不是仓库、用户还有其它未提交改动,
// 这里呈现的也只是「本次会话经工具改过的文件」。
import type { SessionEvent } from './types'

// 一次改动(一条 file/change 事件)。**键名 = 线上真实形状**:sdk.FileChangeEvent 带小写
// json tag(见 sdk/session.go),所以这里必须用小写键 —— 与 Go 字段名不一致(PascalCase)
// 会静默取不到值(2026-09-18 实测:默认发行态下事件首次真正产生时才发现取值全空)。
export interface ChangePayload {
  path?: string
  rel?: string
  op?: string
  tool?: string
  added?: number
  removed?: number
  created?: boolean
  binary?: boolean
  bytes?: number
  diff?: string
  truncated?: boolean
  coarse?: boolean
}

// 单次改动(视图按时间序展示,便于对着会话流定位)。
export interface ChangeHunk {
  seq: number
  ts: string
  op: string
  tool: string
  added: number
  removed: number
  diff: string
  truncated: boolean
  binary: boolean
  created: boolean
  coarse: boolean
}

// 一个文件在本次会话里的累计改动。
export interface FileChange {
  key: string // 聚合主键(Rel 优先,无 Rel 回落绝对路径)
  path: string // 绝对路径
  added: number
  removed: number
  ops: string[] // 出现过的操作(去重保序)
  hunks: ChangeHunk[]
  created: boolean
  binary: boolean
  truncated: boolean
}

export interface ChangesModel {
  files: FileChange[]
  byKey: Record<string, FileChange>
  total: number // 事件条数(不是文件数)
}

export function newChanges(): ChangesModel {
  return { files: [], byKey: {}, total: 0 }
}

// fileChangeOf 从会话事件取改动载荷;非 file/change 或缺路径返回 null。
export function fileChangeOf(ev: SessionEvent): ChangePayload | null {
  if (!ev || ev.Kind !== 'file/change') return null
  const p = ev.Payload as ChangePayload | null
  if (!p || typeof p !== 'object') return null
  if (!p.rel && !p.path) return null
  return p
}

// changesPush 把一条会话事件并入模型(原地更新,返回是否产生了变化)。
// 无变化返回 false,调用方可据此跳过重渲染。
export function changesPush(m: ChangesModel, ev: SessionEvent): boolean {
  const p = fileChangeOf(ev)
  if (!p) return false
  const key = p.rel || p.path || ''
  if (!key) return false
  let f = m.byKey[key]
  if (!f) {
    f = {
      key,
      path: p.path || '',
      added: 0,
      removed: 0,
      ops: [],
      hunks: [],
      created: false,
      binary: false,
      truncated: false,
    }
    m.byKey[key] = f
    m.files.push(f)
  }
  const added = p.added ?? 0
  const removed = p.removed ?? 0
  f.added += added
  f.removed += removed
  f.created = f.created || !!p.created
  f.binary = f.binary || !!p.binary
  f.truncated = f.truncated || !!p.truncated
  const op = p.op || 'write'
  if (!f.ops.includes(op)) f.ops.push(op)
  f.hunks.push({
    seq: ev.Seq ?? 0,
    ts: ev.TS ?? '',
    op,
    tool: p.tool || '',
    added,
    removed,
    diff: p.diff || '',
    truncated: !!p.truncated,
    binary: !!p.binary,
    created: !!p.created,
    coarse: !!p.coarse,
  })
  m.total++
  return true
}

// changesStats 汇总(概览条用)。
export function changesStats(m: ChangesModel): { files: number; added: number; removed: number; changes: number } {
  let added = 0
  let removed = 0
  for (const f of m.files) {
    added += f.added
    removed += f.removed
  }
  return { files: m.files.length, added, removed, changes: m.total }
}

// findChange 按用户/命令给的路径定位文件:精确 → 路径后缀 → 文件名(与 Go 侧 /diff 同一宽松度)。
// 多个候选返回第一个命中集合中的唯一项;歧义时返回 undefined(视图只做定位,不猜)。
export function findChange(m: ChangesModel, path: string): FileChange | undefined {
  const arg = (path || '').replace(/\\/g, '/')
  if (!arg) return undefined
  const base = arg.split('/').pop() || arg
  const tiers: FileChange[][] = [[], [], []]
  for (const f of m.files) {
    const k = f.key.replace(/\\/g, '/')
    const p = f.path.replace(/\\/g, '/')
    if (k === arg || p === arg) tiers[0].push(f)
    else if (k.endsWith('/' + arg) || k.endsWith(arg)) tiers[1].push(f)
    else if (k.split('/').pop() === base) tiers[2].push(f)
  }
  for (const t of tiers) {
    if (t.length === 1) return t[0]
    if (t.length > 0) return t[0] // 歧义:返回首个(视图只滚动定位,不做选择)
  }
  return undefined
}

// —— diff 行分类(视图着色用;纯函数便于单测) ——
export type DiffLineKind = 'add' | 'del' | 'hunk' | 'file' | 'ctx'

// diffLineKind 行首判类:+++/---/@@ 头、+ 新增、- 删除,其余上下文。
// 只看行首(允许前导空白),不做 diff 语义解析(与 TUI diffToolRow 同口径)。
export function diffLineKind(line: string): DiffLineKind {
  const s = line.replace(/^[ \t]+/, '')
  if (s.startsWith('+++') || s.startsWith('---')) return 'file'
  if (s.startsWith('@@')) return 'hunk'
  if (s.startsWith('+')) return 'add'
  if (s.startsWith('-')) return 'del'
  return 'ctx'
}

// diffLines patch → 带类别的行(视图直接渲染,避免每帧重复分类)。
export function diffLines(patch: string): { text: string; kind: DiffLineKind }[] {
  const out: { text: string; kind: DiffLineKind }[] = []
  for (const text of (patch || '').replace(/\n+$/, '').split('\n')) {
    out.push({ text, kind: diffLineKind(text) })
  }
  return out
}

// fmtBytes 紧凑字节数(与 traj.ts 同口径:1 位小数,不堆单位)。
export function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / (1024 * 1024)).toFixed(1) + ' MB'
}

// fmtClock 事件时间戳 → 本地 HH:MM:SS(解析失败返回空串,不编造时间)。
export function fmtClock(ts: string): string {
  if (!ts) return ''
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}
