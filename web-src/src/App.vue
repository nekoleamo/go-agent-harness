<script setup lang="ts">
// 根组件:SSE 消费(历史重放 + 实时)+ REST 上行 + 四槽位装配(registry.ts 契约 v1)。
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from 'vue'
import { api } from './api'
import type { AskConfirm } from './types'
import { consume, isUsage, newModel, type StreamModel } from './sse'
import { createTransport, type Transport } from './transport'
import { extraPanel, slotComponent, type MetaLine } from './registry'
import type { SessionEvent, StateView, ConfirmRequest, CommandResult, QuestionRequest } from './types'
import StatusBar from './components/StatusBar.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import QuestionDialog from './components/QuestionDialog.vue'
import ConfirmBar from './components/ConfirmBar.vue'
import SettingsPanel from './components/SettingsPanel.vue'
import JobsPanel from './components/JobsPanel.vue'
import Sidebar from './components/Sidebar.vue'

const state = ref<StateView>({
  model: '',
  thinking: 'off',
  sandbox: '',
  stats: { PromptTokens: 0, CompletionTokens: 0, CachedTokens: 0, Requests: 0, Window: 0 },
  running: false,
  version: '',
})
const model = ref<StreamModel>(newModel())
const metas = ref<MetaLine[]>([])
const confirm = ref<ConfirmRequest | null>(null)
const question = ref<QuestionRequest | null>(null)
const connState = ref<'open' | 'reconnecting'>('open')
const err = ref('')
// 侧栏数据刷新信号:会话/工作区切换后 +1,Sidebar watch 重拉列表
const refreshKey = ref(0)
// 全局二次确认(增删改前置):Sidebar/InputBar 经 inject('askConfirm') 触发
const settingsOpen = ref(false)
const jobsOpen = ref(false)
const openPanel = ref<string | null>(null)
// v2 扩展点:附加面板(侧栏入口 → 右侧抽屉;组件经 App 渲染)
const openPanelComp = computed(() => extraPanel(openPanel.value ?? '')?.component ?? null)
const openPanelTitle = computed(() => extraPanel(openPanel.value ?? '')?.title ?? '面板')
const runningJobs = ref(0)
async function refreshJobs(): Promise<void> {
  try {
    const list = await api.jobs()
    runningJobs.value = (list ?? []).filter((j) => j.state === 'running').length
  } catch {
    /* 服务未装配时忽略(徽标不显示) */
  }
}
// 后台任务徽标轮询(实时信号:运行中任务数;面板本身另有 3s 轮询)
let jobsTimer: ReturnType<typeof setInterval> | null = null
onMounted(() => {
  void refreshJobs()
  jobsTimer = setInterval(() => void refreshJobs(), 5000)
})
onUnmounted(() => {
  if (jobsTimer) clearInterval(jobsTimer)
})
// 空状态(当前会话尚无消息且未运行):输入框居中 + 欢迎引导;有会话内容后沉底
// 注:会话切换重建瞬间会短暂置空(欢迎闪现一次,可接受)
const empty = computed(() => !state.value.running && model.value.msgs.length === 0 && metas.value.length === 0)
const askRef = ref<AskConfirm | null>(null)
function ask(a: AskConfirm): void {
  askRef.value = a
}
function onAskConfirm(): void {
  const a = askRef.value
  askRef.value = null
  a?.run()
}
function onAskCancel(): void {
  askRef.value = null
}
provide('askConfirm', ask)

// —— 会话流自动滚动:内容变化且用户贴底(未上滚读历史)时自动滚到最新 —
const streamEl = ref<HTMLElement | null>(null)
let stick = true // 是否贴底(滚动监听维护;上滚 >140px 即暂停跟随)
const showNewest = ref(false) // 上滚读历史时新内容到达 → 浮现回底胶囊
function onStreamScroll(): void {
  const el = streamEl.value
  if (!el) return
  stick = el.scrollHeight - el.scrollTop - el.clientHeight < 140
  if (stick) showNewest.value = false
}
function pinBottom(): void {
  const el = streamEl.value
  if (el) el.scrollTop = el.scrollHeight
  stick = true
  showNewest.value = false
}
function goNewest(): void {
  pinBottom()
}
// 新消息/流式文本增长/历史重放/会话切换后:若贴底则 nextTick(等 DOM 更新)后滚到底
watch(
  () => model.value.msgs.map((m) => m.text + (m.kind === 'user' ? 'u' : 'a')).join('|').length,
  () => {
    const go = stick
    void nextTick(() => {
      if (go) pinBottom()
      else showNewest.value = true
    })
  },
)
watch(
  () => [metas.value.length, state.value.running],
  () => {
    const go = stick
    void nextTick(() => {
      if (go) pinBottom()
      else showNewest.value = true
    })
  },
)

let transport: Transport | null = null
let statsTimer: ReturnType<typeof setInterval> | null = null

function rebuild(keepCursor: boolean): void {
  // 会话切换/全新连接:清流重放全量(通道按 after 游标差集重放)
  if (!keepCursor) {
    model.value = newModel()
    try {
      sessionStorage.removeItem('gah.lastSeq')
    } catch {
      /* 无痕模式忽略 */
    }
  }
  metas.value = []
  transport?.close()
  // M7.3:WS 优先,失败自动降级 EventSource(transport 内部完成)
  transport = createTransport()
  transport.onopen = () => {
    connState.value = 'open'
  }
  transport.onreconnecting = () => {
    connState.value = 'reconnecting'
  }
  transport.on('session', (f) => {
    const se = f.payload as SessionEvent
    consume(model.value, se)
    if (isUsage(se)) refreshStats()
  })
  transport.on('status', (f) => {
    state.value.running = f.payload === 'running'
  })
  transport.on('command', (f) => {
    const r = f.payload as CommandResult
    if (r.error) {
      metas.value.push({ kind: 'error', text: r.raw + ': ' + r.error })
    } else if (r.output) {
      metas.value.push({ kind: 'command', text: r.output })
    }
  })
  transport.on('error', (f) => {
    metas.value.push({ kind: 'error', text: String(f.payload) })
  })
  transport.on('confirm', (f) => {
    confirm.value = f.payload as ConfirmRequest
  })
  transport.on('question', (f) => {
    question.value = f.payload as QuestionRequest
  })
}

async function refreshStats(): Promise<void> {
  try {
    state.value = await api.state()
  } catch {
    /* 未联接时忽略(SSE 状态帧仍驱动 running) */
  }
}

async function onSubmit(text: string, attachments?: string[]): Promise<void> {
  const t = text.trim()
  if (!t) return
  if (t.startsWith('/')) {
    try {
      await api.input(t)
    } catch (e) {
      metas.value.push({ kind: 'error', text: (e as Error).message })
    }
    return
  }
  try {
    await api.input(t, attachments ?? [])
    refreshStats()
  } catch (e) {
    metas.value.push({ kind: 'error', text: (e as Error).message })
  }
}

// 会话切换/新建(侧栏/抽屉):刷新状态 + 重建 SSE(全量重放新会话历史)+ 侧栏列表刷新
function sessionChanged(): void {
  void refreshStats()
  rebuild(false)
  refreshKey.value++
}

async function onQuestionAnswer(values: string[], text: string): Promise<void> {
  const req = question.value
  if (!req) return
  question.value = null
  try {
    await api.questionAnswer(req.id, values, text)
  } catch (e) {
    metas.value.push({ kind: 'error', text: '作答提交失败: ' + (e as Error).message })
  }
}

async function onAnswer(ok: boolean): Promise<void> {
  const req = confirm.value
  if (!req) return
  confirm.value = null
  try {
    await api.confirm(req.id, ok)
  } catch {
    /* 应答失败由回合超时兜底(安全默认拒绝) */
  }
}

// 槽位:默认组件(registry 可被 UI 插件覆盖;宿主直挂渲染避免绕模板)
const hasSlot = (n: 'stream' | 'input' | 'statusbar' | 'confirm') => slotComponent(n) !== null

onMounted(async () => {
  await refreshStats()
  rebuild(false)
  // 统计节流刷新(usage 事件外,兜底上下文/缓存显示)
  statsTimer = setInterval(() => void refreshStats(), 3000)
})
onUnmounted(() => {
  transport?.close()
  if (statsTimer) clearInterval(statsTimer)
})
</script>

<template>
  <div class="app" :class="{ empty }">
    <!-- 槽位:statusbar(含连接状态与设置入口) -->
    <section class="statusbar-slot" data-ui-slot="statusbar">
      <StatusBar v-if="hasSlot('statusbar')" :state="state" :conn="connState" />
      <button class="gear" data-tip="设置(模型/Provider/插件/历史)" :aria-expanded="settingsOpen" @click="settingsOpen = !settingsOpen">设置</button>
      <button class="gear" data-tip="后台任务(运行中 {{ runningJobs }})" :aria-expanded="jobsOpen" @click="jobsOpen = !jobsOpen">任务<span v-if="runningJobs" class="jobs-badge">{{ runningJobs }}</span></button>
    </section>

    <div class="main">
      <!-- 左侧历史抽屉(可收起):会话 + 工作区 -->
      <Sidebar
        :refresh-key="refreshKey"
        :cur-session="state.session?.id"
        :cur-key="state.session?.key"
        @session-changed="sessionChanged"
        @open-panel="openPanel = $event"
      />

      <!-- 右侧列:会话流 + 输入框(左右分割;输入框只在右侧底部,不横跨侧栏) -->
      <div class="content" :class="{ empty }">
        <!-- 槽位:stream(会话流 + meta 行) -->
        <section ref="streamEl" class="stream-slot" data-ui-slot="stream" @scroll="onStreamScroll">
          <component :is="slotComponent('stream') || 'div'" :frames="model.msgs" :metas="metas" :running="state.running" />
        </section>

        <!-- 空状态欢迎(输入上方引导) -->
        <div v-if="empty" class="welcome">
          <div class="w-title">Go Agent Harness</div>
          <p class="w-sub">向 Agent 描述任务,开启新会话</p>
        </div>

        <!-- 槽位:input(空状态列内居中放大;有会话内容后右列底部) -->
        <section class="input-slot" :class="{ centered: empty }" data-ui-slot="input">
          <component
            :is="slotComponent('input') || 'div'"
            :disabled="state.running"
            :on-submit="onSubmit"
            :state="state"
            :centered="empty"
            @session-changed="sessionChanged"
            @changed="refreshStats"
          />
        </section>
      </div>
    </div>

    <!-- 槽位:confirm(审批弹层) -->
    <section class="confirm-slot" data-ui-slot="confirm">
      <ConfirmDialog v-if="hasSlot('confirm')" :request="confirm" :on-answer="onAnswer" />
    </section>

    <!-- 结构化提问弹层(P3 语义交互;宿主直挂,不经槽位覆盖) -->
    <QuestionDialog :request="question" :on-answer="onQuestionAnswer" />

    <div v-if="err" class="err-banner">{{ err }}</div>

    <!-- 全局二次确认条 -->
    <ConfirmBar :pending="askRef" @confirm="onAskConfirm" @cancel="onAskCancel" />

    <!-- 可视化设置抽屉 -->
    <SettingsPanel :open="settingsOpen" :state="state" @close="settingsOpen = false" @changed="refreshStats" />

    <!-- 后台任务面板 -->
    <JobsPanel :open="jobsOpen" @close="jobsOpen = false" />

    <!-- v2 扩展点:附加面板抽屉(插件声明 extra-panel) -->
    <div v-if="openPanel" class="ext-mask" @click.self="openPanel = null">
      <aside class="ext-panel">
        <div class="ep-head">
          <span class="ep-title">{{ openPanelTitle }}</span>
          <span class="ep-close" data-tip="关闭" @click="openPanel = null">×</span>
        </div>
        <div class="ep-body">
          <component :is="openPanelComp" />
        </div>
      </aside>
    </div>

    <!-- 上滚读历史时新消息到达 → 回底胶囊 -->
    <button v-if="showNewest" class="newest" data-tip="回到最新消息" @click="goNewest">
      ↓ 新消息
    </button>
  </div>
</template>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.statusbar-slot {
  display: flex;
  align-items: center;
  gap: 8px;
  border-bottom: 1px solid var(--line-faint);
  padding: 4px 14px;
  font-size: 12px;
  color: var(--fg-dim);
}
.gear {
  background: none;
  border: 1px solid var(--line);
  color: var(--fg-faint);
  cursor: pointer;
  font-size: 12px;
  padding: 1px 8px;
  border-radius: var(--r-input);
  flex-shrink: 0;
  transition: border-color 0.15s ease, color 0.15s ease, background 0.15s ease;
}
.gear:hover {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
.jobs-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 16px;
  height: 16px;
  margin-left: 5px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--fg-on-accent);
  font-size: 11px;
  padding: 0 4px;
}
/* v2 扩展点:附加面板抽屉(固定右侧,对齐 JobsPanel 几何;taste 纪律) */
.ext-mask {
  position: fixed;
  inset: 0;
  z-index: 29;
  background: var(--overlay);
}
.ext-panel {
  position: fixed;
  right: 0;
  top: 0;
  bottom: 0;
  width: 340px;
  background: var(--bg);
  border-left: 1px solid var(--line);
  box-shadow: var(--shadow-dialog);
  display: flex;
  flex-direction: column;
  z-index: 30;
  animation: ep-in 0.28s var(--ease-out);
}
@keyframes ep-in {
  from {
    transform: translateX(24px);
    opacity: 0;
  }
  to {
    transform: translateX(0);
    opacity: 1;
  }
}
.ep-head {
  display: flex;
  align-items: center;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line);
}
.ep-title {
  flex: 1;
  font-weight: 600;
  color: var(--fg);
}
.ep-close {
  cursor: pointer;
  color: var(--fg-faint);
  font-size: 16px;
  padding: 2px 6px;
}
.ep-close:hover {
  color: var(--fg);
}
.ep-body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 14px;
}
.main {
  flex: 1;
  display: flex;
  min-height: 0;
}
/* 右侧列:会话流(flex1)+ 输入框(右列底部,宽度只占右侧不与侧栏同宽) */
.content {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.stream-slot {
  flex: 1;
  overflow-y: auto;
  padding: 12px 36px 20px; /* 左右对称留白:消息流占满右列不贴侧栏也不缩窄居中 */
}
.input-slot {
  padding: 10px 24px 12px;
  background: var(--bg);
  border-top: 1px solid var(--line);
}
.err-banner {
  position: fixed;
  left: 12px;
  bottom: 10px;
  color: var(--err);
  font-size: 12px;
}
/* —— 空状态(右列内垂直居中):欢迎 + 放大输入框;有会话内容后 input 回落列底 —— */
.content.empty {
  justify-content: center;
}
.content.empty .stream-slot {
  display: none;
}
.welcome {
  text-align: center;
  margin: 0 0 28px;
  padding: 0 24px;
  animation: welcome-fade 0.4s var(--ease-out);
}
.w-title {
  font-size: 24px;
  font-weight: 700;
  letter-spacing: 0.02em;
  color: var(--fg);
}
.w-sub {
  margin: 8px 0 0;
  font-size: 13px;
  color: var(--fg-faint);
}
.content.empty .input-slot {
  background: transparent;
  border: none;
  padding: 0;
  width: min(720px, calc(100% - 72px));
  margin: 0 auto; /* 未输入空态:输入框水平居中(不贴最左) */
  animation: welcome-in 0.4s var(--ease-out);
}
@keyframes welcome-fade {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
@keyframes welcome-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
/* 回底胶囊:入场 fade-up,置于输入区上方不遮挡 */
.newest {
  position: fixed;
  right: 18px;
  bottom: 72px;
  z-index: 30;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: 999px;
  box-shadow: var(--shadow-pop);
  color: var(--accent);
  font-size: 12px;
  padding: 5px 12px;
  cursor: pointer;
  animation: newest-in var(--dur-base) var(--ease-out);
  transition: border-color var(--dur-fast) ease, background var(--dur-fast) ease;
}
.newest:hover {
  border-color: var(--accent);
  background: var(--accent-soft);
}
@keyframes newest-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
