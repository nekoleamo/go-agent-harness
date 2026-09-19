<script setup lang="ts">
// 根组件:SSE 消费(历史重放 + 实时)+ REST 上行 + 四槽位装配(registry.ts 契约 v1)。
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from 'vue'
import { api } from './api'
import { probeAsync } from './desktop'
import type { AskConfirm } from './types'
import { consume, isUsage, msgsOfEvents, newModel, type StreamModel } from './sse'
import {
  applyBaseline,
  earlierHint,
  MAX_LIVE_MSGS,
  prependMsgs,
  shouldLoadEarlier,
  trimHead,
  windowPartial,
} from './streamwin'
import { clip, newTraj, trajOverview, trajPush, turnStats, type TrajModel } from './traj'
import { changesPush, changesStats, newChanges, type ChangesModel } from './changes'
import {
  BOARD_CARDS,
  boardCards as makeBoardCards,
  jobStateLabel,
  moveCard,
  newBoardLayout,
  parseBoard,
  serializeBoard,
  toggleCard,
  visibleCards,
  type BoardInput,
  type BoardLayout,
  type BoardTarget,
} from './board'
import { fmtNextRun, isUnsetTime, statusLabel } from './schedule'
import {
  BUILTIN_PANELS,
  clampDockWidth,
  isNarrow,
  newDock,
  openDockPanel,
  panelLabel,
  parseDock,
  persistKey as dockPersistKey,
  serializeDock,
  toggleDock as toggleDockState,
  type DockState,
} from './dock'
import { connReduce, newConn, offlineHint, submitAllowed, type ConnEv, type ConnModel } from './conn'
import { createTransport, type Transport } from './transport'
import { extraPanel, slotComponent, type MetaLine } from './registry'
import { OPEN_DOC_EVENT, docRequest } from './docstore'
import type {
  SessionEvent,
  StateView,
  ConfirmRequest,
  CommandResult,
  QuestionRequest,
  Frame,
  Baseline,
  Job,
  Notice,
  NoticePage,
  Schedule,
} from './types'
import {
  applyPage,
  dismiss as dismissToast,
  newToasts,
  pushNotice,
  type ToastState,
} from './notices'
import StatusBar from './components/StatusBar.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import QuestionDialog from './components/QuestionDialog.vue'
import ConfirmBar from './components/ConfirmBar.vue'
import SettingsPanel from './components/SettingsPanel.vue'
import JobsPanel from './components/JobsPanel.vue'
import Sidebar from './components/Sidebar.vue'
import TrajectoryView from './components/TrajectoryView.vue'
import ChangesView from './components/ChangesView.vue'
import BoardView from './components/BoardView.vue'
import DockView from './components/DockView.vue'
import ToastStack from './components/ToastStack.vue'

const state = ref<StateView>({
  model: '',
  thinking: 'off',
  sandbox: '',
  stats: { PromptTokens: 0, CompletionTokens: 0, CachedTokens: 0, Requests: 0, Window: 0 },
  running: false,
  version: '',
})
const model = ref<StreamModel>(newModel())
// S-P0-1 轨迹视图:与流视图并列的模式(同一事件账本、并行投影;不替代槽位 stream 的插件覆盖)
const traj = ref<TrajModel>(newTraj())
// S-P1-1 变更审查视图:同一账本的第三种投影(只呈现文件改动与逐行 diff)
const changes = ref<ChangesModel>(newChanges())
const views = ['stream', 'traj', 'changes', 'board'] as const
type ViewName = (typeof views)[number]
const view = ref<ViewName>(readView())
// S-P2-2 看板布局(显示顺序 + 隐藏项):纯呈现偏好,与 gah.view 同机制落 localStorage
const board = ref<BoardLayout>(readBoard())
// 命令定位(/diff <路径> → diff 帧):路径 + 递增 nonce(同一路径可重复定位)
const chgFocus = ref('')
const chgNonce = ref(0)
function readView(): ViewName {
  try {
    const v = localStorage.getItem('gah.view')
    return v === 'traj' || v === 'changes' || v === 'board' ? v : 'stream'
  } catch {
    return 'stream' // 无痕/禁用存储:回退流视图
  }
}
// readBoard 读看板布局(坏 JSON / 无痕模式 → 默认布局;新增卡片自动出现在尾部)
function readBoard(): BoardLayout {
  try {
    return parseBoard(localStorage.getItem('gah.board'))
  } catch {
    return newBoardLayout()
  }
}
function saveBoard(next: BoardLayout): void {
  board.value = next
  try {
    localStorage.setItem('gah.board', serializeBoard(next))
  } catch {
    /* 无痕模式:仅本次会话生效 */
  }
}
// nextView 循环切换:会话流 → 轨迹 → 变更 → 会话流(按钮文案 = 目标视图)
function nextView(): ViewName {
  return views[(views.indexOf(view.value) + 1) % views.length]
}
// VIEW_LABEL 视图中文名(状态栏按钮文案与看板动作共用一份,不再各写三元表达式)
const VIEW_LABEL: Record<ViewName, string> = { stream: '会话流', traj: '轨迹', changes: '变更', board: '看板' }
const viewTargetLabel = computed(() => VIEW_LABEL[nextView()])
const metas = ref<MetaLine[]>([])
// NOND-N1 提示 toast(状态机在 notices.ts):实时 `notice` 帧 + 连接后 /api/notices 回填,
// 两路按 id 去重。提示不进会话流 —— 它是「需要人回来」的信号,不是对话内容。
const toasts = ref<ToastState>(newToasts())
function onDismissToast(id: number): void {
  dismissToast(toasts.value, id)
}
const confirm = ref<ConfirmRequest | null>(null)
const question = ref<QuestionRequest | null>(null)
// S-P0-2:提问弹层可收起(收起=角标,不阻塞继续对话)。新提问/换提问一律回到展开态。
const questionMin = ref(false)
// S-P1-3 连接健康三态(open/reconnecting/offline):由 conn.ts 状态机统一裁决;
// offline = 明确断开 → 横幅 + 禁止提交(草稿与附件保留),reconnecting 不拦截。
const conn = ref<ConnModel>(newConn())
// lastTick 上次与服务端成功交互的时刻(睡眠检测基准:合盖期间定时器停摆 → 唤醒时时钟跳变)
let lastTick = Date.now()
const offline = computed(() => !submitAllowed(conn.value))
const connHint = computed(() => offlineHint(conn.value))
const err = ref('')
// 侧栏数据刷新信号:会话/工作区切换后 +1,Sidebar watch 重拉列表
const refreshKey = ref(0)
// 全局二次确认(增删改前置):Sidebar/InputBar 经 inject('askConfirm') 触发
const settingsOpen = ref(false)
const openPanel = ref<string | null>(null)
// S-P2-1 轻量版:侧栏停靠区(单面板)。布局落 localStorage['gah.dock'](纯呈现偏好,
// 与 gah.view/gah.board 同机制);宽度可拖拽,窄屏退化为覆盖式抽屉。
const dock = ref<DockState>(readDock())
const vw = ref(typeof window === 'undefined' ? 0 : window.innerWidth)
const dockNarrow = computed(() => isNarrow(vw.value))
const dockPanels = BUILTIN_PANELS
// dockPanelLabel 状态栏按钮文案:展开时显示当前面板名(收起时显示「侧栏」)
const dockPanelLabel = computed(() => panelLabel(dock.value.panel) || '侧栏')
// readDock 读停靠布局(坏 JSON / 未知面板 / 越界宽度 → 逐字段回落,见 dock.ts)
function readDock(): DockState {
  try {
    return parseDock(localStorage.getItem(dockPersistKey()))
  } catch {
    return newDock() // 无痕/禁用存储:仅本次会话生效
  }
}
// saveDock 落盘一次(拖拽过程中的实时宽度只在内存里改,抬起时才写)
function saveDock(next: DockState, persist = true): void {
  dock.value = next
  if (!persist) return
  try {
    localStorage.setItem(dockPersistKey(), serializeDock(next))
  } catch {
    /* 无痕模式:仅本次会话生效 */
  }
}

// W3 首启引导:无 provider 时自动打开设置并定位到 Provider 段(每个浏览器会话只自动弹一次)
const focusSection = ref<'provider' | 'about' | 'schedule' | null>(null)
const providerCount = ref<number | null>(null)
// v2 扩展点:附加面板(侧栏入口 → 右侧抽屉;组件经 App 渲染)
const openPanelComp = computed(() => extraPanel(openPanel.value ?? '')?.component ?? null)
const openPanelTitle = computed(() => extraPanel(openPanel.value ?? '')?.title ?? '面板')
const runningJobs = ref(0)
const jobsTotal = ref(0)
const jobsLatest = ref('')
// latestJobText 最近一条任务(按 created_at 取最新,不依赖接口返回顺序):状态 + 命令摘要
function latestJobText(list: Job[]): string {
  let best: Job | null = null
  for (const j of list) if (!best || (j.created_at ?? '') > (best.created_at ?? '')) best = j
  if (!best) return ''
  const cmd = clip(best.command ?? '', 24)
  return cmd ? `${jobStateLabel(best.state)} · ${cmd}` : jobStateLabel(best.state)
}
async function refreshJobs(): Promise<void> {
  try {
    const list = await api.jobs()
    const arr = list ?? []
    runningJobs.value = arr.filter((j) => j.state === 'running').length
    jobsTotal.value = arr.length
    jobsLatest.value = latestJobText(arr)
  } catch {
    /* 服务未装配时忽略(徽标不显示;看板卡片显示 0) */
  }
}
// —— S-P2-2 看板:三份投影的聚合量 + 任务/计划摘要(卡片内容在 board.ts 派生) ——
const planInfo = ref<{ total: number; enabled: number; next?: string; last?: string }>({ total: 0, enabled: 0 })
async function refreshPlan(): Promise<void> {
  try {
    const arr = (await api.schedules()) ?? []
    const now = new Date()
    let next = ''
    let last = ''
    let lastAt = ''
    for (const sc of arr) {
      const n = sc.next_run
      if (sc.enabled && !isUnsetTime(n) && (next === '' || (n as string) < next)) next = n as string
      if (sc.last_run_at && sc.last_run_at > lastAt) {
        lastAt = sc.last_run_at
        last = statusLabel(sc.last_status) + ' · ' + clip(sc.name || sc.id, 16)
      }
    }
    planInfo.value = {
      total: arr.length,
      enabled: arr.filter((s: Schedule) => s.enabled).length,
      next: next ? fmtNextRun(next, now) : undefined,
      last: last || undefined,
    }
  } catch {
    planInfo.value = { total: 0, enabled: 0 } // 未装配定时计划:卡片如实显示 0
  }
}
const boardInput = computed<BoardInput>(() => {
  const t = traj.value
  const ov = trajOverview(t)
  let tools = 0
  let failed = 0
  for (const turn of [...t.turns, ...(t.cur ? [t.cur] : [])]) {
    const ts = turnStats(turn)
    tools += ts.tools
    failed += ts.failed
  }
  const cs = changesStats(changes.value)
  return {
    state: state.value,
    traj: { turns: ov.turns, running: ov.running, ms: ov.ms, tokens: ov.tokens, cached: ov.cached, tools, failed },
    changes: { files: cs.files, added: cs.added, removed: cs.removed, count: cs.changes },
    jobs: { running: runningJobs.value, total: jobsTotal.value, latest: jobsLatest.value || undefined },
    plan: planInfo.value,
  }
})
const boardList = computed(() => makeBoardCards(boardInput.value))
const boardVisible = computed(() => visibleCards(boardList.value, board.value))
// —— S-P2-1 侧栏停靠区操作(全部经 dock.ts 的纯函数收敛,组件不自己算) ——
// selectDockPanel 切到指定面板(停靠区已展开时只换面板)
function selectDockPanel(id: string): void {
  saveDock(openDockPanel(dock.value, id))
}
// toggleDockPanel 快捷入口(状态栏「任务」等):已停靠同一面板 → 收起,否则切过去
function toggleDockPanel(id: string): void {
  if (dock.value.open && dock.value.panel === id) saveDock(toggleDockState(dock.value))
  else saveDock(openDockPanel(dock.value, id))
}
// toggleDock 展开/收起(保留面板与宽度)
function toggleDock(): void {
  saveDock(toggleDockState(dock.value))
}
// onDockResize 拖拽中的实时宽度:只改内存(不写盘),落盘交给 onDockCommit
function onDockResize(width: number): void {
  saveDock({ ...dock.value, width: clampDockWidth(width, vw.value) }, false)
}
function onDockCommit(): void {
  saveDock(dock.value)
}
// onViewportResize 视口变化:窄屏判定 + 已存宽度按新视口重新收敛(否则窗口缩小后停靠区会挤死对话)
function onViewportResize(): void {
  vw.value = window.innerWidth
  if (!dock.value.open) return
  const w = clampDockWidth(dock.value.width, vw.value)
  if (w !== dock.value.width) saveDock({ ...dock.value, width: w }, false)
}

// 看板动作:打开停靠区 / 切视图 / 打开设置(计划段)。目标语义由 board.ts 声明,这里只做映射。
function onBoardGo(target: BoardTarget): void {
  if (target === 'jobs') toggleDockPanel('jobs')
  else if (target === 'settings') {
    focusSection.value = 'schedule'
    settingsOpen.value = true
  } else view.value = target
}
function onBoardMove(id: string, dir: -1 | 1): void {
  saveBoard(moveCard(board.value, id, dir))
}
function onBoardToggle(id: string): void {
  saveBoard(toggleCard(board.value, id))
}
function onBoardReset(): void {
  saveBoard(newBoardLayout())
}
// 后台任务徽标轮询(实时信号:运行中任务数;面板本身另有 3s 轮询)
const schedTick = ref(0) // 定时计划运行信号(schedule/run 帧 → 设置面板重拉状态)
let jobsTimer: ReturnType<typeof setInterval> | null = null
let planTimer: ReturnType<typeof setInterval> | null = null
// 计划摘要只在看着板时轮询(其余视图不需要;设置面板自己另有一份)
function syncPlanTimer(on: boolean): void {
  if (on && !planTimer) {
    void refreshPlan()
    planTimer = setInterval(() => void refreshPlan(), 15000)
  } else if (!on && planTimer) {
    clearInterval(planTimer)
    planTimer = null
  }
}
watch(view, (v) => syncPlanTimer(v === 'board'), { immediate: true })
onMounted(() => {
  window.addEventListener('resize', onViewportResize)
  void refreshJobs()
  jobsTimer = setInterval(() => void refreshJobs(), 5000)
  void maybeOnboard()
})
onUnmounted(() => {
  window.removeEventListener('resize', onViewportResize)
  if (jobsTimer) clearInterval(jobsTimer)
  if (planTimer) clearInterval(planTimer)
})
// ONBOARD_KEY 自动弹层标记(sessionStorage:同一标签页关掉后不再弹,刷新页也不骚扰)
const ONBOARD_KEY = 'gah.onboard.auto'
// maybeOnboard 一次都没配 provider → 直接落到「粘一个 Key」入口
async function maybeOnboard(): Promise<void> {
  if (sessionStorage.getItem(ONBOARD_KEY)) return
  sessionStorage.setItem(ONBOARD_KEY, '1')
  try {
    const list = await api.providers()
    providerCount.value = (list ?? []).length
    if (providerCount.value === 0) openProviderSettings()
  } catch {
    /* 未装配多 provider(501):不做引导、不显示提示 */
  }
}
// refreshProviders 重拉 provider 数量(设置面板里增删 provider 后,首屏「还没配置模型」入口
// 必须同步消失 —— 此前只在 maybeOnboard 里取一次,用户加完 provider 后提示仍挂着)。
async function refreshProviders(): Promise<void> {
  try {
    const list = await api.providers()
    providerCount.value = (list ?? []).length
  } catch {
    /* 未装配多 provider(501):不做引导、不显示提示 */
  }
}
// onSettingsChanged 设置面板「已改」统一入口:状态与 provider 数一起刷新
function onSettingsChanged(): void {
  void refreshStats()
  void refreshProviders()
}
// openProviderSettings 打开设置并定位到 Provider 段
function openProviderSettings(): void {
  focusSection.value = 'provider'
  settingsOpen.value = true
}
// openAboutSettings 底栏版本号 → 「关于 gah」(升级入口;与托盘菜单同一实现)
function openAboutSettings(): void {
  focusSection.value = 'about'
  settingsOpen.value = true
}
function closeSettings(): void {
  settingsOpen.value = false
  focusSection.value = null
}
// 空状态(当前会话尚无消息且未运行):输入框居中 + 欢迎引导;有会话内容后沉底。
// S-P1-2:还要等 baseline 首帧到 —— 首连历史是**异步**回放的(尾部窗口),不等基线就会
// 在切会话/刷新的瞬间闪一下欢迎页。
const empty = computed(
  () => !state.value.running && baseSeen.value && model.value.msgs.length === 0 && metas.value.length === 0,
)
const askRef = ref<AskConfirm | null>(null)
const baseSeen = ref(false) // S-P1-2:本连接的首帧基线已到(历史窗口已确定)
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
  // S-P1-2:接近顶部就拉更早一页(留一屏余量,避免贴边反复触发)
  if (shouldLoadEarlier(el, model.value.hasMore, loadingEarlier.value)) void loadEarlier()
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

// —— S-P1-2 长会话窗口 ——
// 首连只回放尾部窗口(baseline 帧描述边界),更早历史由上滚分页拉取;
// 贴底阅读时裁掉头部消息,把 DOM/内存压在有界范围内(被裁内容仍可从上滚取回)。
const loadingEarlier = ref(false)
const earlierText = computed(() => earlierHint(model.value))
// 窗口不完整(还有更早历史/已折叠):轨迹与变更视图据此标注口径,不谎报「累计」
const partialWindow = computed(() => windowPartial(model.value))

// 上滚分页:以模型最老事件 Seq 为游标拉更早一页 → 拼到头部 → **锚定滚动位置**
// (视口里正看着的那条消息不能在拼接后跳走)。失败不静默:提示可重试。
async function loadEarlier(): Promise<void> {
  const el = streamEl.value
  const m = model.value
  if (!el || loadingEarlier.value || !m.hasMore) return
  loadingEarlier.value = true
  const gen = connGen // 代际守门:请求回来时会话可能已切(不能把旧会话的消息拼进新会话)
  const before = m.from
  const prevH = el.scrollHeight
  const prevTop = el.scrollTop
  try {
    const page = await api.sessionEvents(before, 200)
    if (gen !== connGen) return
    if (page.events.length > 0) {
      prependMsgs(m, msgsOfEvents(page.events), page.from)
    }
    // 服务端没给内容/游标未前进时也必须收敛 hasMore,避免上滚反复打同一页
    m.hasMore = page.has_more && page.from > 0
    await nextTick()
    el.scrollTop = prevTop + (el.scrollHeight - prevH)
    stick = false // 刚补过历史:用户此刻在看旧内容,不自动跟底
  } catch (e) {
    if (gen !== connGen) return
    metas.value.push({ kind: 'error', text: '加载更早消息失败: ' + (e as Error).message + '。上滚可重试。' })
  } finally {
    loadingEarlier.value = false
  }
}
// 新消息/流式文本增长/历史重放/会话切换后:若贴底则 nextTick(等 DOM 更新)后滚到底
watch(
  () => model.value.msgs.map((m) => m.text + (m.kind === 'user' ? 'u' : 'a')).join('|').length,
  () => {
    if (view.value !== 'stream') return // 轨迹模式下不动流视图滚动位置
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
// connGen 链路代际号(rebuild 时 +1):旧连接的状态回调与迟到帧按代际丢弃。
let connGen = 0
let statsTimer: ReturnType<typeof setInterval> | null = null
// streamSessionId = 会话流当前绑定的会话 id('' = 未校准:首次快照或刚由本地切换)。
// 用于发现「服务端当前会话 ≠ 界面所绑会话」(命令切会话、别的端切走)→ 重放新会话。
let streamSessionId = ''

function rebuild(keepCursor: boolean): void {
  // 会话切换/全新连接:清流重放全量(通道按 after 游标差集重放)
  if (!keepCursor) {
    model.value = newModel()
    traj.value = newTraj()
    changes.value = newChanges()
    loadingEarlier.value = false
    baseSeen.value = false
    try {
      sessionStorage.removeItem('gah.lastSeq')
    } catch {
      /* 无痕模式忽略 */
    }
  }
  metas.value = []
  // S-P1-3:代际号 —— 旧连接(已 close 的 WS/降级的 SSE)的迟到帧与状态回调一律丢弃,
  // 防止它们把新一轮的状态(如 running/连接态)改回去。
  // 顺序要紧:先递增代际再 close 旧通道 —— 否则旧通道的 closed 回调会被当成当前事实,
  // 会话切换瞬间闪一下断连横幅。
  connGen++
  const gen = connGen
  transport?.close()
  // M7.3:WS 优先,失败自动降级 EventSource(transport 内部完成)
  const tp = createTransport()
  transport = tp
  tp.onstate = (s) => {
    if (gen !== connGen) return
    applyConn(s === 'open' ? { k: 'link-open' } : s === 'reconnecting' ? { k: 'link-retrying' } : { k: 'link-closed' })
  }
  // 帧处理统一包一层代际守门:陈旧连接的迟到帧直接丢弃
  const gate = (fn: (f: Frame) => void): ((f: Frame) => void) => {
    return (f: Frame) => {
      if (gen !== connGen) return
      fn(f)
    }
  }
  // S-P1-2 首帧基线:首连只回放尾部窗口 → 前端据此知道「更早历史未加载」
  transport.on('baseline', gate((f) => {
    applyBaseline(model.value, f.payload as Baseline)
    baseSeen.value = true
  }))
  transport.on('session', gate((f) => {
    const se = f.payload as SessionEvent
    consume(model.value, se)
    trajPush(traj.value, se)
    changesPush(changes.value, se)
    // 贴底阅读时裁头部:长会话持续输出时 DOM/内存不随会话长度增长(上滚可重新取回)
    if (stick && model.value.msgs.length > MAX_LIVE_MSGS) trimHead(model.value)
    if (isUsage(se)) refreshStats()
  }))
  transport.on('status', gate((f) => {
    state.value.running = f.payload === 'running'
  }))
  transport.on('command', gate((f) => {
    const r = f.payload as CommandResult
    if (r.error) {
      metas.value.push({ kind: 'error', text: r.raw + ': ' + r.error })
    } else if (r.output) {
      metas.value.push({ kind: 'command', text: r.output })
    }
  }))
  transport.on('error', gate((f) => {
    // 后端已把 agent/error 载荷归一为文本;这里再兜一层:对象载荷不再渲染成 "[object Object]"
    const p = f.payload as unknown
    metas.value.push({ kind: 'error', text: typeof p === 'string' ? p : JSON.stringify(p) })
  }))
  transport.on('confirm', gate((f) => {
    confirm.value = f.payload as ConfirmRequest
  }))
  transport.on('question', gate((f) => {
    question.value = f.payload as QuestionRequest
    questionMin.value = false
  }))
  // G-E5-4:提问已解决(question/resolved 事件订阅面)——多端并存时本端弹层
  // 可能还开着(已由其它渠道作答/超时)→ 按 id 关闭遗留弹层。
  transport.on('questiondone', gate((f) => {
    const p = f.payload as { id?: string }
    if (p?.id && question.value && question.value.id === p.id) {
      question.value = null
      questionMin.value = false
    }
  }))
  // G-E5-4:审批已裁决(confirm/resolved;Confirm 无端侧 id → 按 prompt 关联)
  transport.on('confirmdone', gate((f) => {
    const p = f.payload as { prompt?: string }
    if (p?.prompt && confirm.value && confirm.value.prompt === p.prompt) confirm.value = null
  }))
  // 文档预览意图(D5:模型 doc_open / `/preview` 命令)→ 打开文档面板并定位文件
  transport.on('doc', gate((f) => {
    const d = f.payload as { path?: string }
    if (d?.path) onOpenDoc(new CustomEvent(OPEN_DOC_EVENT, { detail: d.path }))
  }))
  // S-P1-1 变更审查意图(`/diff` 命令):切到变更视图;带路径时定位到该文件
  transport.on('diff', gate((f) => {
    const p = f.payload as { path?: string }
    view.value = 'changes'
    if (p?.path) {
      chgFocus.value = p.path
      chgNonce.value++
    }
  }))
  // 定时计划跑完一轮(NOND-W4 schedule/run 帧):无人值守任务没人在场看到过程,
  // 状态(上次运行/失败原因)必须主动刷新到设置面板。
  transport.on('schedule', gate(() => {
    schedTick.value++
    if (view.value === 'board') void refreshPlan()
  }))
  // NOND-N1 提示帧:立即弹 toast(处理与去重在 notices.ts)。
  transport.on('notice', gate((f) => {
    pushNotice(toasts.value, f.payload as Notice, Date.now())
  }))
  // 提示不进会话记录 → 通道建立后必须回填「上次看到之后」错过的几条。
  // 放在建连之后(不阻塞首屏):回填与实时帧按 id 去重,谁先到都不会重复弹。
  void backfillNotices(gen)
}

// backfillNotices 拉取错过的提示(页面加载 / 重连 / 重新可见时调用)。
// 代际守门与帧一致:旧连接的迟到响应不得写进新一轮状态。
// gap=true 时如实标记「回填不完整」(不谎报完整):服务端环形缓冲丢过一段。
async function backfillNotices(gen: number): Promise<void> {
  try {
    const page: NoticePage = await api.notices(toasts.value.since)
    if (gen !== connGen) return
    applyPage(toasts.value, page, Date.now())
  } catch {
    /* 提示回填是锦上添花:失败不打扰用户(实时帧仍可送达) */
  }
}

async function refreshStats(): Promise<void> {
  try {
    state.value = await api.state()
    lastTick = Date.now() // 活跃心跳(睡眠检测基准)
    // 会话被**命令**切走(如 `/session new`、`/session switch`)时前端收不到任何信号:
    // SSE 订阅还挂在旧会话上 → 用户后续输入的消息服务端已记录,界面上却一个帧都不来(静默丢显示)。
    // 故以服务端快照为事实:监到当前会话 id 与流所绑定的不一致 → 重放全量(与侧栏切换同一条路径)。
    const sid = state.value.session?.id ?? ''
    if (sid && sid !== streamSessionId) {
      if (streamSessionId === '') {
        streamSessionId = sid // 首次快照 / 本地刚切换(见 sessionChanged)→ 只校准,不重放
      } else {
        streamSessionId = sid
        rebuild(false)
        refreshKey.value++
      }
    }
  } catch (e) {
    // S-P1-3:网络层失败(非 HTTP 状态错误)= 链路事实 → 据实降级,不等用户发现。
    // HTTP 4xx/5xx 说明服务可达(请求本身被拒),不当作断连。
    const msg = (e as Error).message ?? ''
    if (!msg.startsWith('HTTP ')) applyConn({ k: 'probe', ok: false })
  }
}

// applyConn 消费一条链路事实到连接状态机(S-P1-3)。
function applyConn(ev: ConnEv): void {
  const next = connReduce(conn.value, ev)
  if (next === conn.value) return
  const wasOffline = conn.value.state === 'offline'
  conn.value = next
  // 重新可用:立即拉一次权威快照 —— 断连期间的回合状态(running/统计)以服务端为准,
  // 不靠本地推算(对齐「以服务端事实接管」的语义),并让侧栏重拉列表。
  if (wasOffline && next.state !== 'offline') {
    void refreshStats()
    void backfillNotices(connGen) // 断连期间错过的提示:恢复后补上(按 id 去重)
    refreshKey.value++
  }
}

// probeConn 探活:HTTP 上行是否真的可达(睡眠后旧 socket 可能自认为还开着)。
async function probeConn(): Promise<void> {
  try {
    await api.state()
    applyConn({ k: 'probe', ok: true })
  } catch {
    applyConn({ k: 'probe', ok: false })
  }
}

// retryConn 重试连接:重置退避与降级态重握一次(唤醒/切网/用户点击都走这里)。
function retryConn(): void {
  transport?.reconnect()
  void probeConn()
}

// —— 网络层与唤醒信号(桌面壳睡眠/切网是真场景)——
// onNetOffline 浏览器报无网:立即离线(不靠「多久没收到帧」猜测)。
function onNetOffline(): void {
  applyConn({ k: 'net', online: false })
}
// onNetOnline 网络恢复:先落「重连中」(等链路确认),并主动重握 + 探活。
function onNetOnline(): void {
  applyConn({ k: 'net', online: true })
  retryConn()
}
// onWake 窗口重获焦点:睡眠/切网后旧 socket 可能已死但 JS 未感知。
// 判定两条:明确离线 → 重握;时钟跳变(睡眠,定时器停摆)→ 旧连接大概率已死,也重握一次
// (代价可控:WS 重连带 after 游标,只补差集不重放全史)。
const SLEEP_GAP_MS = 20000
function onWake(): void {
  const now = Date.now()
  const slept = now - lastTick > SLEEP_GAP_MS
  lastTick = now
  if (slept || !submitAllowed(conn.value)) retryConn()
  else void probeConn()
}
// onVisibility 回到前台(桌面壳最小化恢复)同上;后台时不动(省网络)。
function onVisibility(): void {
  if (document.visibilityState !== 'visible') return
  onWake()
}

// onSubmit 提交回合;返回 false = 未受理(离线/失败)→ 调用方**保留草稿与附件**。
// S-P1-3 纪律:断连期间不提交(宁可让用户等,也不要把输入掷进不可达的链路后丢掉)。
async function onSubmit(text: string, attachments?: string[]): Promise<boolean> {
  const t = text.trim()
  if (!t) return true
  if (!submitAllowed(conn.value)) {
    metas.value.push({ kind: 'error', text: '未发送:连接不可用。草稿与附件已保留,恢复后请重新发送。' })
    return false
  }
  try {
    await api.input(t, attachments ?? [])
    return true
  } catch (e) {
    metas.value.push({ kind: 'error', text: '未发送:' + (e as Error).message + '。草稿已保留。' })
    // 提交失败往往就是链路已断:探一次并据实降级(不靠猜测)
    void probeConn()
    return false
  }
}

// 会话切换/新建(侧栏/抽屉):刷新状态 + 重建 SSE(全量重放新会话历史)+ 侧栏列表刷新
function sessionChanged(): void {
  streamSessionId = '' // 本地已切换:下一次 refreshStats 只校准 id,不重复重放
  void refreshStats()
  rebuild(false)
  refreshKey.value++
}

async function onQuestionAnswer(values: string[], text: string): Promise<void> {
  const req = question.value
  if (!req) return
  // S-P1-3:断开时不掷掉作答 —— 弹层/角标保留(作答只回填提问通道,丢失即只能等超时)
  if (!submitAllowed(conn.value)) {
    metas.value.push({ kind: 'error', text: '作答未提交:连接已断开。弹层已保留,恢复后可重试。' })
    return
  }
  try {
    await api.questionAnswer(req.id, values, text)
    question.value = null
  } catch (e) {
    metas.value.push({ kind: 'error', text: '作答提交失败: ' + (e as Error).message + '。弹层已保留。' })
    void probeConn()
  }
}

async function onAnswer(ok: boolean): Promise<void> {
  const req = confirm.value
  if (!req) return
  // S-P1-3:断开时不掷掉审批决定 —— 弹层保留,恢复后可重答(服务端侧超时仍按安全默认拒绝)
  if (!submitAllowed(conn.value)) {
    metas.value.push({ kind: 'error', text: '审批未提交:连接已断开。弹层已保留,恢复后可重试。' })
    return
  }
  try {
    await api.confirm(req.id, ok)
    confirm.value = null
  } catch (e) {
    // 未送达就不关弹层:关掉等于丢掉用户的决定(回合会一直阻塞到超时)
    metas.value.push({ kind: 'error', text: '审批应答未送达:' + (e as Error).message + '。弹层已保留。' })
    void probeConn()
  }
}

// 槽位:默认组件(registry 可被 UI 插件覆盖;宿主直挂渲染避免绕模板)
// 注:槽位渲染一律走 `<component :is="slotComponent(name) || 默认组件">` ——
// 用 hasSlot() 布尔值配写死组件会让插件覆盖永不生效(statusbar/confirm 曾如此)。
// 文档预览意图(工具行/侧栏):打开工作台抽屉并定位文件(单一入口,含 docRequest 赋值)
// 侧栏徽标 → 打开附加面板抽屉(通用机制:与 docstore 同型,窗口事件解耦)
// 注:通用面板跳转经窗口事件解耦(不依赖具体面板实现)。
const OPEN_PANEL_EVENT = 'gah:open-panel'
function onOpenPanel(ev: Event): void {
  const key = (ev as CustomEvent<string>).detail
  if (key) openPanel.value = key
}

function onOpenDoc(ev: Event): void {
  const path = (ev as CustomEvent<string>).detail
  if (!path) return
  docRequest.value = path
  openPanel.value = 'host-docview'
}

onMounted(async () => {
  window.addEventListener(OPEN_DOC_EVENT, onOpenDoc)
  window.addEventListener(OPEN_PANEL_EVENT, onOpenPanel)
  // S-P1-3:网络层事实(navigator.onLine)与唤醒信号 → 状态机 / 主动重连。
  // 浏览器报无网 = 确定离线(睡眠/切网/拔网线);唤醒/切回前台时探一次活,不干等退避。
  window.addEventListener('online', onNetOnline)
  window.addEventListener('offline', onNetOffline)
  window.addEventListener('focus', onWake)
  document.addEventListener('visibilitychange', onVisibility)
  // 桌面壳:启动时探一次异步命令通道(结果只写壳日志,见 desktop.ts 的 probeAsync)。
  // 真机上出现过 async 命令连函数体都没进,这一行是分辨「任务没被调度」与「请求没到」的证据。
  probeAsync()
  await refreshStats() // 首帧就探活:服务端本就不在时立刻显离线横幅(非「安静会话」误判)
  rebuild(false)
  // 统计节流刷新(usage 事件外,兜底上下文/缓存显示)
  statsTimer = setInterval(() => void refreshStats(), 3000)
})
onUnmounted(() => {
  window.removeEventListener(OPEN_DOC_EVENT, onOpenDoc)
  window.removeEventListener(OPEN_PANEL_EVENT, onOpenPanel)
  window.removeEventListener('online', onNetOnline)
  window.removeEventListener('offline', onNetOffline)
  window.removeEventListener('focus', onWake)
  document.removeEventListener('visibilitychange', onVisibility)
  transport?.close()
  if (statsTimer) clearInterval(statsTimer)
})
</script>

<template>
  <div class="app" :class="{ empty }">
    <!-- 槽位:statusbar(含连接状态与设置入口)。覆盖走 slotComponent:
         此前写死 <StatusBar> 使 M7.2 的 statusbar 插件覆盖永不生效(2026-09-19 本机验收遯到) -->
    <section class="statusbar-slot" data-ui-slot="statusbar">
      <component :is="slotComponent('statusbar') || StatusBar" :state="state" :conn="conn.state" @open-about="openAboutSettings" />
      <button class="gear" data-tip="设置(模型/Provider/插件/历史)" :aria-expanded="settingsOpen" @click="settingsOpen = !settingsOpen">设置</button>
      <button
        class="gear"
        :data-tip="'后台任务(运行中 ' + runningJobs + '):在侧栏停靠区查看,不遮挡对话流'"
        :aria-expanded="dock.open && dock.panel === 'jobs'"
        @click="toggleDockPanel('jobs')"
      >
        任务<span v-if="runningJobs" class="jobs-badge">{{ runningJobs }}</span>
      </button>
      <!-- S-P2-1 侧栏停靠区:与对话流并排显示一个能力面板(变更 / 看板 / 任务) -->
      <button
        class="gear"
        :class="{ on: dock.open }"
        :data-tip="
          dock.open
            ? '收起侧栏(对话流恢复满宽);侧栏内可切换变更 / 看板 / 任务,拖动左侧分隔线调宽'
            : '展开侧栏:在对话旁并排显示变更 / 看板 / 任务,不用切走会话流'
        "
        :aria-expanded="dock.open"
        @click="toggleDock()"
      >
        {{ dock.open ? dockPanelLabel : '侧栏' }}
      </button>
      <button
        class="gear"
        data-tip="切换视图:会话流 / 轨迹(回合·步骤·成本)/ 变更(文件改动与逐行 diff)/ 看板(会话聚合卡片)"
        @click="view = nextView()"
      >
        {{ viewTargetLabel }}
      </button>
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
          <!-- 轨迹模式走内建视图(不接管槽位 stream:UI 插件对该槽位的覆盖仍是流视图的实现) -->
          <TrajectoryView v-if="view === 'traj'" :model="traj" :running="state.running" :partial="partialWindow" />
          <BoardView
            v-else-if="view === 'board'"
            :cards="boardVisible"
            :layout="board"
            :total="BOARD_CARDS.length"
            @move="onBoardMove"
            @toggle="onBoardToggle"
            @reset="onBoardReset"
            @go="onBoardGo"
          />
          <ChangesView
            v-else-if="view === 'changes'"
            :model="changes"
            :focus="chgFocus"
            :focus-nonce="chgNonce"
            :partial="partialWindow"
          />
          <template v-else>
            <!-- S-P1-2 上滚加载:入口不依赖槽位实现(插件覆盖 stream 时仍可用) -->
            <div v-if="earlierText" class="earlier">
              <button class="earlier-btn" :disabled="loadingEarlier" data-tip="加载更早的历史消息" @click="loadEarlier">
                {{ loadingEarlier ? '加载中…' : earlierText }}
              </button>
            </div>
            <component :is="slotComponent('stream') || 'div'" :frames="model.msgs" :metas="metas" :running="state.running" />
          </template>
        </section>

        <!-- 空状态欢迎(输入上方引导) -->
        <div v-if="empty" class="welcome">
          <div class="w-title">Go Agent Harness</div>
          <p class="w-sub">向 Agent 描述任务,开启新会话</p>
          <!-- W3:没有任何 provider 时,把「粘一个 Key」入口摆在首屏 -->
          <p v-if="providerCount === 0" class="w-hint">
            <button class="w-link" @click="openProviderSettings">还没有配置模型:点这里粘一个 API Key</button>
          </p>
        </div>

        <!-- 结构化提问角标(S-P0-2:弹层收起后不阻塞输入,点角标回到作答) -->
        <div v-if="question && questionMin" class="q-chip" role="status">
          <span class="q-tag">待答</span>
          <span class="q-text">{{ question.prompt }}</span>
          <button class="q-act" data-tip="回到提问弹层作答" @click="questionMin = false">作答</button>
        </div>

        <!-- S-P1-3 断连横幅(仅明确离线时出现;重连中走状态栏角标,不弹横幅) -->
        <div v-if="offline" class="off-banner" role="status">
          <span class="off-dot" />
          <span class="off-text">{{ connHint }}</span>
          <button class="off-act" data-tip="立即重握事件通道并探活" @click="retryConn">重试连接</button>
        </div>

        <!-- 槽位:input(空状态列内居中放大;有会话内容后右列底部) -->
        <section class="input-slot" :class="{ centered: empty }" data-ui-slot="input">
          <component
            :is="slotComponent('input') || 'div'"
            :disabled="state.running || offline"
            :disabled-hint="offline ? '连接已断开:草稿与附件已保留,恢复后请重新发送' : ''"
            :on-submit="onSubmit"
            :state="state"
            :centered="empty"
            @session-changed="sessionChanged"
            @changed="refreshStats"
          />
        </section>
      </div>

      <!-- S-P2-1 侧栏停靠区(单面板):内容复用既有视图组件,不另造渲染 -->
      <DockView
        v-if="dock.open"
        :panels="dockPanels"
        :panel="dock.panel"
        :width="dock.width"
        :narrow="dockNarrow"
        @select="selectDockPanel"
        @close="toggleDock"
        @resize="onDockResize"
        @commit="onDockCommit"
      >
        <ChangesView
          v-if="dock.panel === 'changes'"
          :model="changes"
          :focus="chgFocus"
          :focus-nonce="chgNonce"
          :partial="partialWindow"
        />
        <BoardView
          v-else-if="dock.panel === 'board'"
          :cards="boardVisible"
          :layout="board"
          :total="BOARD_CARDS.length"
          @move="onBoardMove"
          @toggle="onBoardToggle"
          @reset="onBoardReset"
          @go="onBoardGo"
        />
        <JobsPanel v-else :open="true" docked @close="toggleDock" />
      </DockView>
    </div>

    <!-- NOND-N1 提示 toast 层(宿主直挂,不经槽位覆盖) -->
    <ToastStack :state="toasts" :on-dismiss="onDismissToast" />

    <!-- 槽位:confirm(审批弹层;覆盖走 slotComponent,同理不再写死默认组件) -->
    <section class="confirm-slot" data-ui-slot="confirm">
      <component :is="slotComponent('confirm') || ConfirmDialog" :request="confirm" :on-answer="onAnswer" />
    </section>

    <!-- 结构化提问弹层(P3 语义交互;宿主直挂,不经槽位覆盖;S-P0-2 可收起) -->
    <QuestionDialog
      :request="question"
      :minimized="questionMin"
      :on-answer="onQuestionAnswer"
      :on-minimize="() => (questionMin = true)"
    />

    <div v-if="err" class="err-banner">{{ err }}</div>

    <!-- 全局二次确认条 -->
    <ConfirmBar :pending="askRef" @confirm="onAskConfirm" @cancel="onAskCancel" />

    <!-- 可视化设置抽屉 -->
    <SettingsPanel
      :open="settingsOpen"
      :state="state"
      :focus="focusSection ?? undefined"
      :sched-tick="schedTick"
      @close="closeSettings"
      @changed="onSettingsChanged"
    />

    <!-- v2 扩展点:附加面板抽屉(插件声明 extra-panel) -->
    <div v-if="openPanel" class="ext-mask" @click.self="openPanel = null">
      <aside class="ext-panel">
        <div class="ep-head">
          <span class="ep-title">{{ openPanelTitle }}</span>
          <span class="ep-close" data-tip="关闭" @click="openPanel = null">×</span>
        </div>
        <div class="ep-body">
          <component :is="openPanelComp" @close="openPanel = null" />
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
/* 停靠区展开态:与 hover 拉开两档(sel > hover > 常态),故写在 .gear 之后 */
.gear.on {
  border-color: var(--sel-border);
  background: var(--sel-bg);
  color: var(--fg);
}
.gear:hover {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
.q-chip {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 12px 6px;
  padding: 6px 10px;
  border: 1px solid var(--accent);
  border-radius: var(--r-input);
  background: var(--accent-soft);
  color: var(--fg);
  font-size: 13px;
}
.q-tag {
  flex: none;
  color: var(--accent);
  font-weight: 600;
}
.q-text {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.q-act {
  flex: none;
  background: var(--accent);
  color: var(--fg-on-accent);
  border: none;
  border-radius: var(--r-input);
  padding: 4px 10px;
  cursor: pointer;
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
/* S-P1-3 断连横幅:贴在输入区上方(与提问角标同一竖列),不遮会话流 */
.off-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 12px 6px;
  padding: 6px 10px;
  border: 1px solid var(--err-line);
  border-radius: var(--r-input);
  background: var(--err-soft);
  color: var(--err);
  font-size: 13px;
}
.off-dot {
  flex: none;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--err);
}
.off-text {
  flex: 1;
  min-width: 0;
}
.off-act {
  flex: none;
  background: none;
  border: 1px solid var(--err-line);
  border-radius: var(--r-input);
  color: var(--err);
  cursor: pointer;
  font-size: 12px;
  padding: 3px 10px;
}
.off-act:hover {
  border-color: var(--err);
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
.w-hint {
  margin: 14px 0 0;
  font-size: 12px;
}
.w-link {
  background: none;
  border: none;
  padding: 0;
  color: var(--accent);
  cursor: pointer;
  font-size: 12px;
}
.w-link:hover {
  text-decoration: underline;
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

/* S-P1-2 上滚加载:滚动容器内的顶部行(随内容滚动;不占固定位) */
.earlier {
  display: flex;
  justify-content: center;
  padding: 8px 12px 2px;
}
.earlier-btn {
  background: transparent;
  border: 1px solid var(--line-faint);
  border-radius: 999px;
  color: var(--fg-dim);
  font-size: 12px;
  padding: 4px 12px;
  cursor: pointer;
  transition: border-color var(--dur-fast) ease, color var(--dur-fast) ease;
}
.earlier-btn:hover:not(:disabled) {
  border-color: var(--line-strong);
  color: var(--fg);
}
.earlier-btn:disabled {
  color: var(--fg-faint);
  cursor: default;
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
