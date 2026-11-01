<script setup lang="ts">
// 整卷问卷弹层(P3 扩展):tab 栏(■ 已答 / □ 未答 / ✓ 提交)+ 题干/hint + 选项
// +「✎ 自己输入」+「💬 聊聊这个」+ 确认页。逻辑全在 questionnaire.ts(纯函数,node --test 覆盖),
// 这里只做 DOM/键盘与"落地即上报"。
//
// 与单题 QuestionDialog 并列存在而不是改造它:单题路径是已交付体验(S-P0-2 的角标/收起),
// 改造等于把两条路径的回归面绑在一起。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { QuestionnaireQuestion, QuestionnaireRequest } from '../types'
import {
  qnAllAnswered,
  qnCommitMulti,
  qnCurrent,
  qnFreeTextOnly,
  qnGoTab,
  qnInit,
  qnItems,
  qnMove,
  qnOpenInput,
  qnPickOption,
  qnSubmitInput,
  qnSummary,
  qnTab,
  qnToggle,
  qnAnswered,
} from '../questionnaire'
import type { QnItem, QnResult, QnState } from '../questionnaire'

const props = defineProps<{
  request: QuestionnaireRequest | null
  minimized?: boolean
  // 逐题落库(服务端存副本 ⇒ 刷新后 tab 栏 ■ 与已选项都能恢复)
  onPick: (questionId: string, answer: QnResult) => Promise<void>
  // 收口:提交 / 整卷跳过 / 聊聊这个
  onSubmit: (cancelled: boolean, chat: string) => Promise<void>
  onMinimize?: () => void
}>()

const state = ref<QnState>(qnInit({ id: '', questions: [] }))
const busy = ref(false)

// 新整卷到达(含补推)才重置;同一个 id 重复推送(重连补推)不打断正在做的选择。
watch(
  () => props.request?.id,
  () => {
    if (props.request) state.value = qnInit(props.request)
    busy.value = false
  },
  { immediate: true },
)

const questions = computed<QuestionnaireQuestion[]>(() => props.request?.questions ?? [])
const cur = computed<QuestionnaireQuestion | undefined>(() =>
  props.request ? qnCurrent(state.value, props.request) : undefined,
)
const items = computed<QnItem[]>(() => (cur.value ? qnItems(cur.value) : []))
const freeOnly = computed<boolean>(() => (cur.value ? qnFreeTextOnly(cur.value) : false))
const allDone = computed<boolean>(() => (props.request ? qnAllAnswered(state.value, props.request) : false))
const summary = computed(() => (props.request ? qnSummary(props.request, state.value) : []))
const answeredAt = (q: QuestionnaireQuestion): boolean => qnAnswered(state.value, q)
const onConfirmTab = computed<boolean>(() => !!props.request && state.value.tab >= questions.value.length)

// 纯自由文本题:进入该题即开手写态(否则用户要先点一下才出现输入框)。
watch(
  () => [props.request?.id, state.value.tab] as const,
  () => {
    const q = cur.value
    if (q && qnFreeTextOnly(q) && !state.value.inputQid) state.value = qnOpenInput(state.value, q.id)
  },
)

// land 落地一题:先本地(界面立刻前进),再上报服务端;单题问卷答完即收口。
async function land(qid: string, ans: QnResult): Promise<void> {
  await props.onPick(qid, ans)
  if (questions.value.length === 1) await finalize(false, '')
  else await finalizeIfChat()
}

// finalizeIfChat 「聊聊这个」:把那题之外已收集的答案先落库,再收口(chat 优先于提交)。
async function finalizeIfChat(): Promise<void> {
  const chat = state.value.chat
  if (!chat) return
  await finalize(false, chat)
}

// finalize 收口。提交前把本地草稿**再刷一遍**(服务端幂等覆盖):逐题上报可能因断线失败过,
// 若直接收口,服务端按空答案组装 —— 用户明明选了,模型却收到"(未答)"。这里重试一次代价极低。
async function finalize(cancelled: boolean, chat: string): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    if (!cancelled) {
      for (const [qid, ans] of Object.entries(state.value.draft)) await props.onPick(qid, ans)
    }
    await props.onSubmit(cancelled, chat)
  } finally {
    busy.value = false
  }
}

async function pick(i: number): Promise<void> {
  const q = cur.value
  const it = items.value[i]
  if (!q || !it) return
  if (it.kind === 'other') {
    state.value = qnOpenInput(state.value, q.id)
    return
  }
  if (it.kind === 'chat') {
    state.value = qnPickOption(state.value, props.request!, it)
    await finalizeIfChat()
    return
  }
  if (q.multiple) {
    state.value = qnToggle(state.value, props.request!, it)
    return
  }
  state.value = qnPickOption(state.value, props.request!, it)
  const ans = state.value.draft[q.id]
  if (ans) await land(q.id, ans)
}

async function commitMulti(): Promise<void> {
  const q = cur.value
  if (!q || !props.request) return
  const next = qnCommitMulti(state.value, props.request)
  state.value = next
  const ans = next.draft[q.id]
  if (next.notice || !ans) return // 空集合:停在本题并提示
  await land(q.id, ans)
}

async function submitInput(): Promise<void> {
  const q = cur.value
  const qid = state.value.inputQid
  if (!qid) {
    // 没在手写态(理论上不该发生:伪选项/自由文本题都会先开输入):只开输入,不落空答案
    if (q) state.value = qnOpenInput(state.value, q.id)
    return
  }
  const next = qnSubmitInput(state.value, props.request!)
  const ans = next.draft[qid]
  state.value = next
  if (ans) await land(qid, ans)
}

function go(i: number): void {
  if (props.request) state.value = qnGoTab(state.value, props.request, i)
}

function step(delta: number): void {
  if (props.request) state.value = qnTab(state.value, props.request, delta)
}

function move(delta: number): void {
  if (props.request) state.value = qnMove(state.value, props.request, delta)
}

function onKey(e: KeyboardEvent): void {
  if (!props.request || props.minimized) return
  const el = e.target as HTMLElement | null
  // 别的模态(设置面板等)开在上面时不要抢键:本组件常驻挂载,不做这道判断会把
  // Tab/Esc/数字键从别人的输入里偷走。target 落在**其它** .mask 内 ⇒ 直接放行。
  const mask = el?.closest?.('.mask')
  if (mask && !mask.hasAttribute('data-qn')) return
  const typing = !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')
  // 手写态:只接管 Enter(提交)与 Esc(退出手写),其余键交给输入框
  if (state.value.inputQid) {
    if (e.key === 'Enter' && !e.isComposing) {
      e.preventDefault()
      void submitInput()
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      state.value = { ...state.value, inputQid: null, input: '' }
    }
    return
  }
  if (typing) return
  switch (e.key) {
    case 'Tab':
      e.preventDefault()
      step(e.shiftKey ? -1 : 1)
      return
    case 'ArrowRight':
      e.preventDefault()
      step(1)
      return
    case 'ArrowLeft':
      e.preventDefault()
      step(-1)
      return
    case 'ArrowUp':
      e.preventDefault()
      move(-1)
      return
    case 'ArrowDown':
      e.preventDefault()
      move(1)
      return
    case 'Escape':
      e.preventDefault()
      props.onMinimize?.()
      return
    case 'Enter':
      e.preventDefault()
      if (onConfirmTab.value) {
        if (allDone.value) void finalize(false, '')
        return
      }
      if (cur.value?.multiple && items.value[state.value.sel]?.kind === 'option') void commitMulti()
      else void pick(state.value.sel)
      return
    case ' ':
      if (cur.value?.multiple && items.value[state.value.sel]?.kind === 'option') {
        e.preventDefault()
        void pick(state.value.sel)
      }
      return
    default:
      break
  }
  if (/^[1-9]$/.test(e.key)) {
    const n = Number(e.key) - 1
    if (onConfirmTab.value) {
      if (n < questions.value.length) go(n) // 确认页:数字键跳回修改
      return
    }
    if (n < items.value.length) {
      e.preventDefault()
      state.value = { ...state.value, sel: n }
      void pick(n)
    }
  }
}

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Transition name="pop">
    <div v-if="request && !minimized" class="mask" data-qn @click.self="onMinimize?.()">
      <div class="dialog" role="dialog" aria-label="问卷">
        <div v-if="request.title" class="title">{{ request.title }}</div>

        <!-- tab 栏:■ 已答 / □ 未答 / ✓ 提交;窄屏横向滚动 -->
        <div class="tabs" role="tablist">
          <button
            v-for="(q, i) in questions"
            :key="q.id"
            class="chip"
            :class="{ on: state.tab === i, done: answeredAt(q) }"
            role="tab"
            :aria-selected="state.tab === i"
            @click="go(i)"
          >
            <span class="box">{{ answeredAt(q) ? '■' : '□' }}</span>{{ q.label || `Q${i + 1}` }}
          </button>
          <button
            class="chip submit"
            :class="{ on: onConfirmTab, ready: allDone }"
            role="tab"
            :aria-selected="onConfirmTab"
            @click="go(questions.length)"
          >
            ✓ 提交
          </button>
        </div>

        <!-- 题目页 -->
        <template v-if="cur">
          <pre class="prompt">{{ cur.prompt }}</pre>
          <div v-if="cur.hint" class="hint">{{ cur.hint }}</div>

          <div class="opts">
            <label
              v-for="(it, i) in items"
              :key="it.kind === 'option' ? it.value : it.kind"
              class="opt"
              :class="{ on: state.sel === i, picked: it.kind === 'option' && (state.draft[cur.id]?.values ?? []).includes(it.value) }"
              @mouseenter="state = { ...state, sel: i }"
            >
              <input
                v-if="cur.multiple && it.kind === 'option'"
                type="checkbox"
                :checked="(state.draft[cur.id]?.values ?? []).includes(it.value)"
                @change="pick(i)"
              />
              <button v-else type="button" class="pick" @click="pick(i)">
                <span class="lab">{{ it.kind === 'option' ? `${i + 1}. ${it.label}` : it.kind === 'other' ? '✎ 自己输入…' : '💬 聊聊这个' }}</span>
                <span v-if="it.kind === 'option' && it.desc" class="desc">{{ it.desc }}</span>
              </button>
              <span v-if="cur.multiple && it.kind === 'option'" class="lab">{{ `${i + 1}. ${it.label}` }}</span>
            </label>
          </div>

          <!-- 手写 / 纯自由文本题 -->
          <div v-if="state.inputQid === cur.id || freeOnly" class="input-row">
            <input
              v-model="state.input"
              class="inp"
              :placeholder="freeOnly ? '请输入你的回答…' : '你的回答(回车提交)'"
              @keydown.enter.prevent="submitInput()"
              @keydown.esc.prevent="state = { ...state, inputQid: freeOnly ? state.inputQid : null }"
            />
            <button class="mini" @click="submitInput()">确定</button>
          </div>

          <div v-if="state.notice" class="notice">{{ state.notice }}</div>
          <div class="help">
            {{ cur.multiple ? '空格勾选 · Enter 提交本题 · 数字键直选' : '数字键/点击直选 · Enter 确认' }} · Tab/←→ 切题
          </div>
        </template>

        <!-- 确认页 -->
        <template v-else>
          <div class="confirm-title">确认提交</div>
          <div class="sum">
            <div v-for="(s, i) in summary" :key="s.id" class="sum-row" @click="go(i)">
              <span class="sum-idx">{{ i + 1 }}.</span>
              <span class="sum-label">{{ s.label }}:</span>
              <span class="sum-text" :class="{ missing: s.text === '(未答)' }">{{ s.custom ? `(手写) ${s.text}` : s.text }}</span>
            </div>
          </div>
          <div class="help">
            {{ allDone ? '按 Enter 提交 · 数字键跳回修改' : '还有未答的题:数字键可跳回修改' }}
          </div>
        </template>

        <div class="actions">
          <button v-if="onMinimize" class="later" @click="onMinimize()">稍后作答</button>
          <span class="spacer" />
          <button class="skip" :disabled="busy" @click="finalize(true, '')">整卷跳过</button>
          <button class="go" :disabled="!allDone || busy" @click="finalize(false, '')">提交</button>
        </div>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 19; /* 单题弹层(z-index 20)优先盖在上面:两者语义不同,单题不排队 */
}
.dialog {
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 18px 20px;
  max-width: 620px;
  width: 92%;
  max-height: 86vh;
  overflow-y: auto;
  box-shadow: var(--shadow-dialog);
}
.title {
  color: var(--fg);
  font-weight: 600;
  margin-bottom: 10px;
}
.tabs {
  display: flex;
  gap: 6px;
  overflow-x: auto;
  padding-bottom: 6px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--line-faint);
}
.chip {
  flex: 0 0 auto;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: 999px;
  padding: 4px 10px;
  color: var(--fg-dim);
  font: inherit;
  font-size: 0.92em;
  cursor: pointer;
  transition: color var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
}
.chip .box {
  margin-right: 5px;
}
.chip.done {
  color: var(--ok);
  border-color: var(--ok-line);
}
.chip.on {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
.chip.submit.ready {
  color: var(--ok);
}
.prompt {
  margin: 0 0 8px;
  color: var(--fg);
  font: inherit;
  white-space: pre-wrap;
  word-break: break-word;
}
.hint {
  color: var(--fg-dim);
  font-size: 0.92em;
  margin-bottom: 10px;
}
.opts {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 10px;
}
.opt {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pick {
  display: flex;
  flex-direction: column;
  gap: 3px;
  width: 100%;
  text-align: left;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 12px;
  color: var(--fg);
  font: inherit;
  cursor: pointer;
  transition: border-color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.opt.on .pick,
.opt.on .lab {
  border-color: var(--accent);
}
.pick:hover {
  border-color: var(--accent);
  background: var(--accent-soft);
}
.desc {
  color: var(--fg-dim);
  font-size: 0.9em;
}
.lab {
  color: var(--fg);
}
.input-row {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}
.inp {
  flex: 1;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 12px;
  color: var(--fg);
  font: inherit;
}
.inp:focus {
  outline: none;
  border-color: var(--accent);
}
.mini {
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 14px;
  color: var(--fg-dim);
  cursor: pointer;
}
.notice {
  color: var(--tool-strong);
  font-size: 0.9em;
  margin-bottom: 6px;
}
.confirm-title {
  color: var(--accent);
  font-weight: 600;
  margin-bottom: 10px;
}
.sum {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 10px;
}
.sum-row {
  display: flex;
  gap: 6px;
  cursor: pointer;
  font-size: 0.95em;
}
.sum-row:hover .sum-text {
  color: var(--accent);
}
.sum-idx,
.sum-label {
  color: var(--fg-dim);
  flex: 0 0 auto;
}
.sum-text {
  color: var(--fg);
}
.sum-text.missing {
  color: var(--tool-strong);
}
.help {
  color: var(--fg-faint);
  font-size: 0.86em;
  margin-bottom: 10px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  align-items: center;
}
.spacer {
  flex: 1;
}
.later {
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 16px;
  cursor: pointer;
}
.later:hover {
  border-color: var(--accent);
}
.skip {
  background: transparent;
  color: var(--fg-dim);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 16px;
  cursor: pointer;
}
.skip:hover {
  color: var(--fg);
  border-color: var(--fg-dim);
}
.go {
  background: var(--accent);
  color: var(--fg-on-accent);
  border: none;
  border-radius: var(--r-input);
  padding: 8px 16px;
  cursor: pointer;
}
.go:disabled,
.skip:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
