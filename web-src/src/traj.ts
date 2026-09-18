// 轨迹/可观测视图模型(S-P0-1,对标 dsh ui-trajectory)。
//
// 输入与 sse.ts 完全相同:**同一份会话事件账本**(单一事实源),本模块只做纯函数聚合,
// 不订阅任何额外通道、不引入新后端契约。产出 turn → step → tool 三层结构 + 每层度量。
//
// 纪律:
//  - 时长只用事件自带 TS 计算。**进行中的 turn/step/tool 不给时长**(显示"进行中"),
//    不拿本地时钟补齐(否则长会话里的陈旧回合会被编造出越来越大的时长)。
//  - 工具调用归属 = 它所在 step(事件里没有更细的嵌套关系,不臆造层级)。
//  - turn 边界:由 `user/message`(起点)与 `turn/end`(终点)派生 —— `turn/start` 在
//    会话账本里是可选标记(旧会话没有它),故不作为必要输入。
// 注:本模块只做 type-only 引入 —— src/ 下的逻辑模块保持自包含(node 直跑 *.test.ts 时
// 无法解析无扩展名相对导入,而 vue-tsc 不允许带 .ts 扩展名导入)。参数摘要等展示口径
// 由视图层调 sse.argsSummary,本模型保留原始参数 JSON(精度不丢,便于 inspector 展开)。
import type { SessionEvent, ToolCall, ToolResult, UsageEvent } from './types'

export interface TrajTool {
  id: string
  name: string
  args: string // 原始参数 JSON(展示时经 argsSummary 摘要)
  status: 'called' | 'ok' | 'err'
  error?: string
  outBytes?: number // 结果字节数(IO inspector 的“出”;未回填 undefined)
  callTs: string
  resultTs?: string
}

export interface TrajStep {
  seq: number // step/start 的会话 seq(锚点)
  ts: string
  endTs?: string
  toolIds: string[]
}

export interface TrajUsage {
  model: string
  prompt: number
  completion: number
  cached: number
  requests: number
}

export interface TrajTurn {
  index: number // 1-based 回合序(视图锚点)
  seq: number // user/message 的 seq
  ts: string
  endTs?: string
  reason?: string // turn/end 载荷(done|cancelled|max_steps|…)
  steps: TrajStep[]
  toolIds: string[]
  tools: Record<string, TrajTool>
  usage: TrajUsage
  user: string
  assistant: string
}

export interface TrajModel {
  turns: TrajTurn[]
  cur?: TrajTurn // 进行中的回合(user/message 之后、turn/end 之前)
}

export function newTraj(): TrajModel {
  return { turns: [] }
}

function newUsage(): TrajUsage {
  return { model: '', prompt: 0, completion: 0, cached: 0, requests: 0 }
}

function newTurn(index: number, seq: number, ts: string, user: string): TrajTurn {
  return {
    index,
    seq,
    ts,
    steps: [],
    toolIds: [],
    tools: {},
    usage: newUsage(),
    user,
    assistant: '',
  }
}

// 取当前回合;不存在时按需开一个隐式回合(只有过程帧而没有 user/message 的会话)。
function ensure(m: TrajModel, ts: string, seq: number): TrajTurn {
  if (!m.cur) {
    const t = newTurn(m.turns.length + 1, seq, ts, '')
    m.turns.push(t)
    m.cur = t
  }
  return m.cur
}

// 最近的 open step(用于把 tool/call 归到当前 step)。
function lastStep(t: TrajTurn): TrajStep | undefined {
  return t.steps.length > 0 ? t.steps[t.steps.length - 1] : undefined
}

// findTool 跨回合回溯定位工具(结果帧可能晚于回合结束到达,如取消后收尾)。
function findTool(m: TrajModel, id: string): TrajTool | undefined {
  for (let i = m.turns.length - 1; i >= 0; i--) {
    const t = m.turns[i]
    const tool = t.tools[id]
    if (tool) return tool
  }
  return undefined
}

// trajPush 消费一帧会话事件(重放与实时共用;与 sse.consume 并行、互不影响)。
export function trajPush(m: TrajModel, ev: SessionEvent): void {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const p = ev.Payload as any
  switch (ev.Kind) {
    case 'user/message': {
      const um = p as { Content?: string }
      const cur = m.cur
      // turn/start 已开回合(账本自 2026-09-18 起在 user/message 前发该帧):回填用户消息,
      // 不再开新回合;否则(旧会话无 turn/start)= 以用户消息为回合起点。
      if (cur && cur.user === '' && cur.steps.length === 0 && cur.toolIds.length === 0 && cur.assistant === '' && !cur.endTs) {
        cur.user = um?.Content ?? ''
        cur.seq = ev.Seq
        cur.ts = ev.TS
      } else {
        const t = newTurn(m.turns.length + 1, ev.Seq, ev.TS, um?.Content ?? '')
        m.turns.push(t)
        m.cur = t
      }
      break
    }
    case 'turn/start': {
      // 回合起点标记:已有 cur(user/message 先行)不重复开。
      if (!m.cur) {
        const t = newTurn(m.turns.length + 1, ev.Seq, ev.TS, '')
        m.turns.push(t)
        m.cur = t
      }
      break
    }
    case 'turn/end': {
      const t = ensure(m, ev.TS, ev.Seq)
      t.endTs = ev.TS
      t.reason = typeof p === 'string' && p ? p : 'done'
      m.cur = undefined
      break
    }
    case 'step/start': {
      const t = ensure(m, ev.TS, ev.Seq)
      t.steps.push({ seq: ev.Seq, ts: ev.TS, toolIds: [] })
      break
    }
    case 'step/end': {
      const t = m.cur
      if (!t) break
      const s = lastStep(t)
      if (s && !s.endTs) s.endTs = ev.TS
      break
    }
    case 'tool/call': {
      const tc = p as ToolCall
      const t = ensure(m, ev.TS, ev.Seq)
      let s = lastStep(t)
      if (!s) {
        // 无 step 的工具调用(异常流/裁剪日志):补隐式 step,防丢失归属
        s = { seq: ev.Seq, ts: ev.TS, toolIds: [] }
        t.steps.push(s)
      }
      const tool: TrajTool = {
        id: tc.ID,
        name: tc.Name,
        args: tc.Arguments ?? '',
        status: 'called',
        callTs: ev.TS,
      }
      t.tools[tool.id] = tool
      t.toolIds.push(tool.id)
      s.toolIds.push(tool.id)
      break
    }
    case 'tool/result': {
      const tr = p as ToolResult
      const tool = findTool(m, tr.CallID)
      if (tool) {
        tool.resultTs = ev.TS
        if (tr.Error) {
          tool.status = 'err'
          tool.error = tr.Error
          tool.outBytes = tr.Error.length
        } else {
          tool.status = 'ok'
          tool.outBytes = (tr.Content ?? '').length
        }
      }
      break
    }
    case 'assistant/message': {
      const am = p as { Content?: string }
      const t = m.cur
      if (t && am?.Content) {
        t.assistant = t.assistant ? t.assistant + '\n\n' + am.Content : am.Content
      }
      break
    }
    case 'session/usage': {
      const ue = p as UsageEvent
      const t = m.cur
      if (!t) break
      t.usage.requests++
      t.usage.prompt += ue?.Usage?.PromptTokens ?? 0
      t.usage.completion += ue?.Usage?.CompletionTokens ?? 0
      t.usage.cached += ue?.Usage?.CachedTokens ?? 0
      if (ue?.Model) t.usage.model = ue.Model
      break
    }
    default:
      break // assistant/chunk(流式增量)等:以落定事件为准,轨迹侧不累积
  }
}

// —— 度量(进行中一律 undefined,不编造) ——

function elapsed(from: string, to?: string): number | undefined {
  if (!to) return undefined
  const a = Date.parse(from)
  const b = Date.parse(to)
  if (isNaN(a) || isNaN(b) || b < a) return undefined
  return b - a
}

export function turnMs(t: TrajTurn): number | undefined {
  return elapsed(t.ts, t.endTs)
}

export function stepMs(s: TrajStep): number | undefined {
  return elapsed(s.ts, s.endTs)
}

export function toolMs(tool: TrajTool): number | undefined {
  return elapsed(tool.callTs, tool.resultTs)
}

export interface TrajStats {
  steps: number
  tools: number
  failed: number
  pending: number // 未回填结果的工具数
  tokens: number // prompt + completion
  ms?: number
}

export function turnStats(t: TrajTurn): TrajStats {
  let failed = 0
  let pending = 0
  for (const id of t.toolIds) {
    const tool = t.tools[id]
    if (tool.status === 'err') failed++
    if (tool.status === 'called') pending++
  }
  return {
    steps: t.steps.length,
    tools: t.toolIds.length,
    failed,
    pending,
    tokens: t.usage.prompt + t.usage.completion,
    ms: turnMs(t),
  }
}

// —— 格式化(视图共用;数字一律走这里,不为各处散写口径) ——

export function fmtMs(ms?: number): string {
  if (ms === undefined) return ''
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const min = Math.floor(s / 60)
  return `${min}m${(s - min * 60).toFixed(0)}s`
}

export function fmtTok(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1000000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1000000).toFixed(2)}M`
}

export function fmtBytes(n?: number): string {
  if (n === undefined) return ''
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / 1024 / 1024).toFixed(1)}MB`
}

// clip 单行摘要(视图内的预览文本;多行折叠为空格)。
export function clip(s: string, max = 60): string {
  const one = (s ?? '').replace(/\s+/g, ' ').trim()
  if (one.length <= max) return one
  return one.slice(0, max) + '…'
}

// modelOverview 顶部固定概览(回合数/总时长/累计 token 与缓存占比)。
export interface TrajOverview {
  turns: number
  running: boolean
  ms?: number // 全部已结束回合的时长之和(有未结束回合时为 undefined)
  tokens: number
  cached: number
}

export function trajOverview(m: TrajModel): TrajOverview {
  let ms: number | undefined = 0
  let tokens = 0
  let cached = 0
  for (const t of m.turns) {
    const d = turnMs(t)
    if (d === undefined) ms = undefined
    else if (ms !== undefined) ms += d
    tokens += t.usage.prompt + t.usage.completion
    cached += t.usage.cached
  }
  return { turns: m.turns.length, running: m.cur !== undefined, ms, tokens, cached }
}
