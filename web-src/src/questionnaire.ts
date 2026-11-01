// 整卷问卷的**纯逻辑**状态机(P3 扩展):tab 切换 / 选项/伪选项 / 多选 / 手写 / 确认页。
//
// 为什么要单独抽一层:Vue 组件里塞状态机意味着只能靠真机点,而问卷的分支密度
// (单选→前进、多选→回车才算完、手写→覆盖、聊聊这个→提前收口、确认页→跳回)
// 恰恰是最容易"改了 A 坏 B"的地方。这里零依赖、零 DOM,`node --test src/*.test.ts` 直接跑。
import type { QuestionAnswerView, QuestionnaireQuestion, QuestionnaireRequest } from './types'

export interface QnResult {
  values: string[]
  text: string
  custom: boolean
  index: number // 单选序号(1 起;手写/多选为 0)
}

export type QnItem =
  | { kind: 'option'; index: number; value: string; label: string; desc?: string }
  | { kind: 'other' }
  | { kind: 'chat' }

export interface QnState {
  tab: number // [0..len]:len = 确认页
  sel: number // 当前题的选项光标
  draft: Record<string, QnResult> // 题 id → 已作答
  input: string // 「自己输入」编辑中的文本
  inputQid: string | null // 正在手写的题 id
  notice: string // 一次性提示(如"先勾选至少一项")
  chat: string // 用户选了「聊聊这个」的题 id(非空 ⇒ 调用方应立即收口)
}

// qnInit 初始态:把服务端补推的 answered 原样接进来(tab 栏 ■ 与已选项都能恢复)。
export function qnInit(req: QuestionnaireRequest): QnState {
  const draft: Record<string, QnResult> = {}
  for (const [qid, a] of Object.entries(req.answered ?? {})) {
    draft[qid] = toResult(a)
  }
  return { tab: 0, sel: 0, draft, input: '', inputQid: null, notice: '', chat: '' }
}

// toResult 载荷 → 本地作答(缺字段按零值,不 undefined 扩散)。
function toResult(a: QuestionAnswerView): QnResult {
  return {
    values: Array.isArray(a.values) ? [...a.values] : [],
    text: typeof a.text === 'string' ? a.text : '',
    custom: a.custom === true,
    index: typeof a.index === 'number' ? a.index : 0,
  }
}

// qnItems 当前题的可选行:真选项 + 「✎ 自己输入」/「💬 聊聊这个」伪选项。
// 无选项的题不给伪选项 —— 输入框就是唯一作答路径(与 Go 侧 buildQuestionnaire 同口径)。
export function qnItems(q: QuestionnaireQuestion): QnItem[] {
  const out: QnItem[] = []
  for (let i = 0; i < (q.options?.length ?? 0); i++) {
    const o = q.options![i]
    out.push({ kind: 'option', index: i, value: o.value, label: o.label || o.value, desc: o.desc })
  }
  // 缺省 true(与 Go 侧 buildQuestionnaire / pi 的 allowOther|allowChat 默认同口径)
  if (out.length > 0 && q.allow_other !== false) out.push({ kind: 'other' })
  if (out.length > 0 && q.allow_chat !== false) out.push({ kind: 'chat' })
  return out
}

// qnFreeTextOnly 该题只能手写(无选项)。
export function qnFreeTextOnly(q: QuestionnaireQuestion): boolean {
  return qnItems(q).length === 0
}

// qnCurrent 当前题(在确认页时返回 undefined)。
export function qnCurrent(s: QnState, req: QuestionnaireRequest): QuestionnaireQuestion | undefined {
  return s.tab < req.questions.length ? req.questions[s.tab] : undefined
}

// qnAnswered 该题是否已作答(多选 ≥1、单选有值、手写有文本)。
export function qnAnswered(s: QnState, q: QuestionnaireQuestion): boolean {
  const a = s.draft[q.id]
  return !!a && (a.values.length > 0 || a.text.trim() !== '')
}

// qnAllAnswered 全部题都答过(确认页允许提交的前提)。
export function qnAllAnswered(s: QnState, req: QuestionnaireRequest): boolean {
  return req.questions.every((q) => qnAnswered(s, q))
}

// qnTab 切题(含确认页,循环;切题后光标回 0)。
export function qnTab(s: QnState, req: QuestionnaireRequest, delta: number): QnState {
  const n = req.questions.length + 1
  const tab = ((s.tab + delta) % n + n) % n
  return { ...s, tab, sel: 0, notice: '', inputQid: null, input: '' }
}

// qnGoTab 直接跳到某 tab(点 tab chip / 确认页数字键跳回)。
export function qnGoTab(s: QnState, req: QuestionnaireRequest, tab: number): QnState {
  if (tab < 0 || tab > req.questions.length) return s
  return { ...s, tab, sel: 0, notice: '', inputQid: null, input: '' }
}

// qnMove 选项光标移动(夹紧;确认页无光标)。
export function qnMove(s: QnState, req: QuestionnaireRequest, delta: number): QnState {
  const q = qnCurrent(s, req)
  if (!q) return s
  const n = qnItems(q).length
  if (n === 0) return s
  const sel = Math.min(Math.max(0, s.sel + delta), n - 1)
  return { ...s, sel, notice: '' }
}

// qnAfterAnswer 落地一题之后:单题问卷停在原地(由调用方立即提交),多题前进到下一题/确认页。
export function qnAfterAnswer(s: QnState, req: QuestionnaireRequest): QnState {
  if (req.questions.length <= 1) return s
  return qnGoTab(s, req, Math.min(s.tab + 1, req.questions.length))
}

// qnPickOption 单选/点击落地一个真选项(多选请用 qnToggle)。
// 「聊聊这个」把 chat 置为该题 id:调用方见到 s.chat 非空即收口(转对话,不重发整卷)。
export function qnPickOption(s: QnState, req: QuestionnaireRequest, item: QnItem): QnState {
  const q = qnCurrent(s, req)
  if (!q) return s
  if (item.kind === 'other') return { ...s, inputQid: q.id, input: '', notice: '', chat: '' }
  if (item.kind === 'chat') return { ...s, notice: '', chat: q.id }
  const next: QnState = {
    ...s,
    notice: '',
    draft: { ...s.draft, [q.id]: { values: [item.value], text: '', custom: false, index: item.index + 1 } },
  }
  return qnAfterAnswer(next, req)
}

// qnToggle 多选:空格/点击勾选框切换一项(不前进 —— 回车/提交本题才收口)。
export function qnToggle(s: QnState, req: QuestionnaireRequest, item: QnItem): QnState {
  const q = qnCurrent(s, req)
  if (!q || item.kind !== 'option') return s
  const prev = s.draft[q.id]?.values ?? []
  const values = prev.includes(item.value) ? prev.filter((v) => v !== item.value) : [...prev, item.value]
  return {
    ...s,
    notice: '',
    draft: { ...s.draft, [q.id]: { values, text: '', custom: false, index: 0 } },
  }
}

// qnCommitMulti 多选提交本题:空集合不算已答(给一次性提示,不静默通过)。
export function qnCommitMulti(s: QnState, req: QuestionnaireRequest): QnState {
  const q = qnCurrent(s, req)
  if (!q) return s
  const a = s.draft[q.id]
  if (!a || a.values.length === 0) return { ...s, notice: '先按空格勾选至少一项' }
  return qnAfterAnswer(s, req)
}

// qnOpenInput 进入某题的手写态。
export function qnOpenInput(s: QnState, questionId: string): QnState {
  return { ...s, inputQid: questionId, input: '', notice: '' }
}

// qnCloseInput 退出手写态(不回退已答)。
export function qnCloseInput(s: QnState): QnState {
  return { ...s, inputQid: null, input: '', notice: '' }
}

// qnSetInput 手写文本变化。
export function qnSetInput(s: QnState, text: string): QnState {
  return { ...s, input: text }
}

// qnSubmitInput 手写落地:空白按「(no response)」记(与 pi 同口径,不静默丢题)。
export function qnSubmitInput(s: QnState, req: QuestionnaireRequest): QnState {
  const qid = s.inputQid
  if (!qid) return s
  const text = s.input.trim() || '(no response)'
  const next: QnState = {
    ...s,
    inputQid: null,
    input: '',
    notice: '',
    draft: { ...s.draft, [qid]: { values: [], text, custom: true, index: 0 } },
  }
  if (req.questions.length <= 1) return next
  const idx = req.questions.findIndex((q) => q.id === qid)
  return qnGoTab(next, req, Math.min(idx + 1, req.questions.length))
}

// qnSummary 确认页/摘要:逐题(未答显式标注),供 UI 与提交前自查。
export function qnSummary(req: QuestionnaireRequest, s: QnState): { id: string; label: string; text: string; custom: boolean }[] {
  return req.questions.map((q, i) => {
    const a = s.draft[q.id]
    const label = q.label || `Q${i + 1}`
    if (!a || (a.values.length === 0 && a.text.trim() === '')) return { id: q.id, label, text: '(未答)', custom: false }
    if (a.custom || a.text.trim() !== '') return { id: q.id, label, text: a.text, custom: true }
    const labels = a.values.map((v) => q.options?.find((o) => o.value === v)?.label || v)
    return { id: q.id, label, text: labels.join('、'), custom: false }
  })
}
