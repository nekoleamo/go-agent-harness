// SSE 消费者:帧流 → 渲染模型。语义对齐 tui 会话流(用户/助手/工具行 + meta)。
// 历史重放与实时帧共用同一消费引擎(chunk 拼 pending、assistant/message 落定)。
import type { AttachmentInfo, SessionEvent, ToolCall, ToolResult, UsageEvent, LLMStreamDelta } from './types'

export interface ToolRow {
  id: string
  name: string
  args: string // 参数摘要(JSON 关键字段;解析失败截断原样)
  status: 'called' | 'ok' | 'err'
  result: string // 结果摘要(截断)
  full: string // 全文(折叠展开)
}

export interface Msg {
  kind: 'user' | 'assistant' | 'tool' | 'meta'
  text: string
  full?: string // tool:全文;assistant:无
  tools?: ToolRow[] // assistant 携带的工具调用(落定时的快照)
  atts?: AttachmentInfo[] // user:附件(图片预览/文件引用)
  seq: number // 会话事件 Seq(重放/渲染定位)
  ts: string
  err?: boolean
}

// Cursor 渲染游标(重建流时续接:仅展示新建帧)
// S-P1-2 窗口状态:from = 已加载的最老**事件** Seq(上滚分页游标);hasMore = 服务端/本地
// 是否还有更早事件;trimmed = 本次连接已折叠的消息条数(贴底阅读时裁剪头部,避免长会话
// DOM/内存随会话长度线性增长)。三者共同描述「当前窗口」,视图须据此标注口径而非谎报全量。
export interface StreamModel {
  msgs: Msg[]
  pending: string // 进行中 assistant 文本(chunk 增量)
  pendingTool?: ToolRow // 进行中工具(等待 result)
  step: number
  from: number
  hasMore: boolean
  trimmed: number
}

export function newModel(): StreamModel {
  return { msgs: [], pending: '', step: 0, from: 0, hasMore: false, trimmed: 0 }
}

// 一批事件 → 消息(一次性、独立模型)。用于 S-P1-2 上滚分页:必须整页喂进 consume,
// 因为助手消息的工具行要靠**后续** tool/result 事件回填,逐事件拼会丢工具行。
export function msgsOfEvents(events: SessionEvent[]): Msg[] {
  const m = newModel()
  for (const ev of events) consume(m, ev)
  return m.msgs
}

// 工具参数摘要:JSON 对象取键值对截断;非 JSON 截断原样
export function argsSummary(raw: string): string {
  try {
    const o = JSON.parse(raw) as Record<string, unknown>
    const parts = Object.entries(o).slice(0, 3).map(([k, v]) => `${k}=${strOf(v)}`)
    let s = parts.join(' ')
    if (s.length > 80) s = s.slice(0, 80) + '…'
    return s
  } catch {
    let s = raw
    if (s.length > 80) s = s.slice(0, 80) + '…'
    return s
  }
}
function strOf(v: unknown): string {
  if (typeof v === 'string') {
    return v.length > 30 ? v.slice(0, 30) + '…' : v
  }
  return JSON.stringify(v) ?? ''
}
export function resultSummary(raw: string): { text: string; full: string } {
  if (raw.length <= 400) return { text: raw, full: raw }
  return { text: raw.slice(0, 400) + '\n…', full: raw }
}

// consume 处理一帧会话事件(SSE 顺序保证;重放与实时共用)
export function consume(m: StreamModel, ev: SessionEvent): void {
  const kind = ev.Kind
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const p = ev.Payload as any
  switch (kind) {
    case 'user/message': {
      const um = p as { Content: string; Attachments?: AttachmentInfo[] }
      m.msgs.push({ kind: 'user', text: um.Content ?? '', atts: um.Attachments ?? [], seq: ev.Seq, ts: ev.TS })
      break
    }
    case 'assistant/chunk': {
      const d = p as LLMStreamDelta
      if (d.Delta) m.pending += d.Delta
      if (d.ToolCallID) {
        // 工具调用增量归并(流式参数;实时下常与 message 落定重叠,保守归并即可)
        if (!m.pendingTool || m.pendingTool.id !== d.ToolCallID) {
          m.pendingTool = { id: d.ToolCallID, name: d.ToolCallName ?? '', args: d.ToolCallArgs ?? '', status: 'called', result: '', full: '' }
        } else {
          m.pendingTool.name += d.ToolCallName ?? ''
          m.pendingTool.args += d.ToolCallArgs ?? ''
        }
      }
      break
    }
    case 'assistant/message': {
      const am = p as { Content: string; ToolCalls: ToolCall[] }
      const text = am.Content || m.pending
      const tools: ToolRow[] = (am.ToolCalls ?? []).map((c) => m.pendingTool && m.pendingTool.id === c.ID ? m.pendingTool : ({ id: c.ID, name: c.Name, args: argsSummary(c.Arguments), status: 'called', result: '', full: '' }))
      // 已落定的叫工具行保持结果;新条目挂到 assistant 消息
      m.pending = ''
      m.pendingTool = undefined
      m.msgs.push({ kind: 'assistant', text, tools, seq: ev.Seq, ts: ev.TS })
      break
    }
    case 'tool/call': {
      const tc = p as ToolCall
      const row: ToolRow = { id: tc.ID, name: tc.Name, args: argsSummary(tc.Arguments), status: 'called', result: '', full: '' }
      m.pendingTool = row
      break
    }
    case 'tool/result': {
      const tr = p as ToolResult
      if (!m.pendingTool || m.pendingTool.id !== tr.CallID) {
        // 结果帧先于调用帧到达(异常流):补条挂起行,防丢结果
        m.pendingTool = { id: tr.CallID, name: tr.Name, args: '', status: 'called', result: '', full: '' }
      }
      const row = m.pendingTool!
      row.status = tr.Error ? 'err' : tr.Content ? 'ok' : 'ok'
      if (tr.Error) {
        row.status = 'err'
        row.result = tr.Error
        row.full = tr.Error
      } else {
        const { text, full } = resultSummary(tr.Content ?? '')
        row.result = text
        row.full = full
      }
      const mark = row.status === 'err' ? '✗' : '✓'
      m.msgs.push({
        kind: 'tool',
        text: `${mark} ${row.name} ${row.args}`.trim(),
        full: row.full,
        seq: ev.Seq,
        ts: ev.TS,
        err: row.status === 'err',
      })
      // 同步更新对应 assistant 消息内的工具快照(若其 tools 仍指向该行)
      const lastAs = m.msgs[m.msgs.length - 2]
      if (lastAs && lastAs.tools) {
        const t = lastAs.tools.find((x) => x.id === tr.CallID)
        if (t) Object.assign(t, row)
      }
      m.pendingTool = undefined
      break
    }
    case 'session/summary': {
      const s = (p as { Summary?: string })?.Summary ?? (p as string)
      const txt = typeof s === 'string' ? s : String(p ?? '')
      m.msgs.push({ kind: 'meta', text: `(滚动摘要) ${txt.slice(0, 120)}…`, seq: ev.Seq, ts: ev.TS })
      break
    }
    case 'turn/end': {
      const why = typeof p === 'string' ? p : 'done'
      if (why === 'cancelled') {
        m.msgs.push({ kind: 'meta', text: '⟳ 回合已取消', seq: ev.Seq, ts: ev.TS, err: true })
      }
      break
    }
    // step/start、step/end、turn/start、session/usage:过程帧(运行指示由 status 帧承担)
    default:
      break
  }
}

// Usage 刷新需求(收到 usage 事件后调用方拉 /api/state 更新状态栏)
export function isUsage(ev: SessionEvent): boolean {
  void (ev as { Payload: UsageEvent })
  return ev.Kind === 'session/usage'
}
