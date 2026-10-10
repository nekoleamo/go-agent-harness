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
import { FrameQueue, rafScheduler } from './framequeue'
import { clip, newTraj, trajOverview, trajPush, turnSpeedLine, turnStats, type TrajModel, type TrajTurn } from './traj'
import { SessionCache, switchPlan, type SwitchPlan } from './sessioncache'
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
import { clearSessionCursor, createTransport, setTransportSession, type Transport } from './transport'
import { isUserCanceled } from './turns'
import { DEFAULT_TAB_LIMIT, decodeTabs, encodeTabs, TabSet } from './tabset'
import type { TabMeta } from './tabset'
import { shortSessionId, tabTitle } from './frame-routing'
import { boundSession } from './session-scope'
import { foreignOwner, foreignTodoText } from './frame-routing'
import { extraPanel, slotComponent, type MetaLine, type PendingView } from './registry'
import SessionPrefsPanel from './components/SessionPrefsPanel.vue'
import { hasSessionOverride, sessionSetCount } from './scope'
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
// A-5#124 系统通知(按来源降级):localhost 才申请权限,LAN IP 访问下静默走页内 toast。
import { createNotifier } from './notify'
import StatusBar from './components/StatusBar.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import QuestionDialog from './components/QuestionDialog.vue'
import ConfirmBar from './components/ConfirmBar.vue'
import SettingsPanel from './components/SettingsPanel.vue'
import JobsPanel from './components/JobsPanel.vue'
import Sidebar from './components/Sidebar.vue'
import TrajectoryView from './components/TrajectoryView.vue'
import ArtifactsBar from './components/ArtifactsBar.vue'
import ChangesView from './components/ChangesView.vue'
import BoardView from './components/BoardView.vue'
import DockView from './components/DockView.vue'
import ToastStack from './components/ToastStack.vue'
import TabBar from './components/TabBar.vue'

const state = ref<StateView>({
  model: '',
  thinking: 'off',
  sandbox: '',
  stats: { PromptTokens: 0, CompletionTokens: 0, CachedTokens: 0, Requests: 0, LastPromptTokens: 0, Window: 0 },
  running: false,
  running_sessions: [],
  version: '',
})
// 页签状态容器(骨架批):**每会话一份**会话流状态。
// 本批只开一个页签(= 当前会话),UI 与切换交互留给下一批;但"按会话隔离"这件事
// 从现在就成立,后面加页签条只是把同一份状态画出来。
const tabs = new TabSet<StreamModel>()
// TABS_KEY 页签集合的持久化位置(sessionStorage)。为什么存 sessionStorage 而不是
// 后端:这是**本窗口的视图状态**(用户在这个窗口开了哪几个页签),不是会话数据;
// 落盘到 $GAH_HOME 会变成跨设备的共享状态 —— 那不是这个功能要的。
// 只存 id 与激活项,**不存流内容**(流由各页签重连后按游标补齐)。
const TABS_KEY = 'gah.tabs'
function persistTabs(): void {
  try {
    sessionStorage.setItem(TABS_KEY, encodeTabs(tabs.ids(), tabs.activeId()))
  } catch {
    /* 无痕模式:不持久化,刷新后回到单页签(不算故障) */
  }
}

/** 刷新后恢复页签集合。返回激活页签 id(空 = 没有可恢复的,走单页签默认)。 */
function restoreTabs(): string {
  let raw: string | null = null
  try {
    raw = sessionStorage.getItem(TABS_KEY)
  } catch {
    return '' // 无痕模式读不到:按单页签默认走
  }
  const { ids, active } = decodeTabs(raw, DEFAULT_TAB_LIMIT)
  for (const id of ids) tabs.ensure(id, newModel)
  return active
}

// 首屏页签:URL ?session= 优先(桌面壳多窗口既有语义),否则先落在"主会话"占位。
// 为什么不等第一次 state 快照:那之前页签条会是空的(用户看不到自己在哪个会话里);
// 快照到了再用 TabSet.rekey 把占位页改绑到真实 id(而不是新建第二个页签)。
const restoredActive = restoreTabs()
const bootTab = boundSession() || restoredActive || 'main'
tabs.ensure(bootTab, newModel, boundSession() ? '' : bootTab === 'main' ? '主会话' : '')
// 恢复出来的页签若**就是真实会话 id**(刷新页面 / 桌面壳重开:sessionStorage 里存着上一轮的
// 会话),必须和「URL 带 ?session=」同一条语义 —— 请求层与事件层一起绑过去。
//
// 为何(2026-10-09 实测真 bug):api 绑定原先只在 refreshStats 的**首次校准**分支里做,
// 而那个分支的前提是 `calibrated = bootTab !== 'main'` —— 恢复出真实 id 时它一上来就是 true,
// 分支永不执行 ⇒ `boundSession()` 停在 main.ts 绑的 ''(= 全局档)⇒
// 「当前会话设置」的**每一次写都静默落成全局档**(实测 POST /api/control 的 session 变成 "",
// 页签标着某个会话、设置却写给所有人)。事件层同理:不绑就退化成服务端当前会话。
if (!boundSession() && bootTab !== 'main') {
  api.bindSession(bootTab)
  setTransportSession(bootTab)
}
// tabId 当前页签的会话 id。
//
// **页签与「服务端当前会话」是两个东西**:页签是"我现在在看哪个会话",
// 后者的切换是全局操作(TUI /session、命令 /session switch)且会受切走闸门约束。
// 所以这里一律用 tabId,不拿 state.session.id 当页签键。
// tabId 必须**是 ref**:它是模板依赖(输入框草稿、页签高亮)的唯一数据源。
// 用普通 let 时 Vue 追踪不到变化 —— 真机手测据此发现"切页签后草稿/输入框都不更新"。
const tabId = ref(bootTab)
// calibration 首屏占位页是否已完成改绑(见 refreshStats 的首次校准)。
let calibrated = bootTab !== 'main' // URL 已带 ?session= 时不需要校准
// mainTabKey 本窗口「代表服务端当前会话」的那个页签键(null = 本窗口没有这样的页签:
// 由 ?session= 或会话恢复带着具体会话启动的多窗口/刷新,不跟随服务端当前会话)。
//
// 为什么要它(W1):`state.session.id` 是**全局当前会话**(后端 handleState 恒下发 CurrentSession(),
// 与 ?session= 无关),而页签是「我在看哪个会话」。非主会话页签若拿它当自己的 id,
// 别的端/命令一切走当前会话,它就会误判「我绑的会话被切走」→ 白重放一次(清 TPS/命令回显行)。
// 判据必须是「只有代表当前会话的那个页签才跟随」,其余页签一律用**自己的**键。
let mainTabKey: string | null = bootTab === 'main' ? 'main' : null
// sessionIdFor 把页签键解析成**服务端真实会话 id**(占位 'main' → 当前会话 id)。
//
// 为何必须解析(第一百三十八批真 bug):后端以「session 参数非空」判会话档、空串判全局档
// (web/server.go handleControl 的 sessionScoped),而页签首帧前只知道占位 'main'。
// 绑占位会在**主会话**上把「当前会话设置」写成全局默认 —— 症状:设了模型/角色后页签仍标
// 「跟随全局」,而且切到别的页签也跟着变(README 说的“各用各的”根本没发生)。
// 首帧后 state.session.id 就是真实 id(后端 handleState 下发),拿它绑定即对。
function sessionIdFor(key: string): string {
  if (key && key !== 'main') return key
  return state.value.session?.id ?? ''
}
function curTabId(): string {
  return tabId.value || state.value.session?.id || 'main'
}
// tabList 是给模板的**副本**(TabSet 是普通对象,Vue 感知不到它的内部变化;
// 每次变更后 syncTabs() 重建一次 —— 比把容器做成 reactive 更省心,也不会误触发深层代理)。
const tabList = ref<TabMeta[]>([])
function syncTabs(): void {
  persistTabs()
  tabList.value = tabs.ids().map((id) => {
    const t = tabs.get(id)!
    return {
      id: t.id, title: t.title, running: t.running, unread: t.unread, role: t.role,
      custom: t.custom,
      draft: t.draft, scrollTop: t.scrollTop, atBottom: t.atBottom,
    }
  })
  // 标记要给**别的**页签也探一次(当前页签的来源由 state 直接可知,见 watchStateCustom)。
  for (const id of tabs.ids()) probeTabCustom(id)
}

// hasSessionOverride 从共享判据取(见 scope.ts):只看 state.session_prefs。
// 曾经在这里用 *_from === 'session' 判过 —— 那是错的,后端那个值在没有角色时恒为 'session'
// ⇒ 跟随全局的会话也会被判成独立。判据已收口到 scope.ts 一处,这里不再自己拼。

// probed:已探过「是否独立」的会话。别的页签按需各问后端一次
// (/api/state?session=),**每个会话只问一次** —— 页签上限 8,反复问会把 3s 一次的
// statsTimer 变成 N 倍请求。页签关闭时要在 closeTab 里删掉(见那里),否则同一会话
// 重新打开时会拿一份可能已经过期的标记。
const probed = new Set<string>()
function probeTabCustom(id: string): void {
  if (id === curTabId() || probed.has(id)) return
  probed.add(id)
  void api
    .stateFor(id)
    .then((s) => {
      const t = tabs.get(id)
      if (!t) return // 已关掉
      if (t.custom !== hasSessionOverride(s)) {
        t.custom = hasSessionOverride(s)
        syncTabs()
      }
    })
    .catch(() => {
      probed.delete(id) // 失败下轮再试,不把错误答案定死
    })
}
// applyCustomTo:把一份 state 的「是否独立」记到**指定**页签上。
//
// 为何要传 id 而不是用 curTabId()(第一百三十五批 review 修的两个错):
//   ① refreshStats 是异步的 —— 请求飞行途中切了页签,`curTabId()` 已经是**别的**页签,
//      而这份 state 回答的是**发问时**那个页签。记到当前页签头上 = 记错对象。
//      所以调用方在发问时就把 id 传进来,结果永远归它本来该去的地方。
//   ② 早先还有一个跨页签共享的 `currentCustom` 缓存用来短路「值没变」——
//      切页签时它还留着**上一页**的值,新页签若恰好同值就直接 return,
//      新页签的标记于是永远不更新(方块不出现)。现在只跟目标页签自己的 custom 比。
function applyCustomTo(tabIdFor: string, s: StateView): void {
  const t = tabs.get(tabIdFor)
  if (!t) return // 页签已关
  const v = hasSessionOverride(s)
  if (t.custom === v) return
  t.custom = v
  syncTabs()
}
const model = ref<StreamModel>(newModel())
// 本窗口会话的当前步数(第一百零三批)。
//
// 诚实的边界:**只有本连接订阅的那个会话**能数到步 —— 事件连接按会话过滤
// (`?session=`),别的窗口在跑到第几步我们收不到帧。能拿到的只有 `running_sessions`
// (谁在跑)。所以侧栏对别的会话只显示「运行中」,不编造步数。
const steps = ref(0)
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
// pushMeta 追一行前端侧产生的 meta(命令回显/回合错误/审批未应答)。
// after 记「此刻会话流已落定的最后一条消息 seq」—— StreamView 据此把它织回
// 正确的位置,而不是统统堆在所有消息之后(见 registry.ts 的 MetaLine.after)。
// turnCanceled:本回合是用户自己按停止结束的,这类行不产生(见 turns.ts)。
function pushMeta(kind: MetaLine['kind'], text: string, tps = false): MetaLine | undefined {
  if (isUserCanceled(text)) return undefined // 用户自己的停止不是错误(2026-10-03)
  const msgs = model.value.msgs
  const line: MetaLine = { kind, text, after: msgs.length ? msgs[msgs.length - 1].seq : 0 }
  if (tps) line.tps = true
  metas.value.push(line)
  return line // 供需要回头改写自己的行(如回合速记被迟到的 usage 补正)使用
}
// —— 回合结束速记(输出 TPS)——
// 行文本盯住回合对象:收尾的 usage 帧偶发晚于 turn/end,那时先落一行「用量待补」,
// usage 一到就把**同一行**改写(而不是再追一行),免得同一回合出现两行统计。
const tpsTurn = ref<TrajTurn | null>(null)
const tpsLine = ref<MetaLine | null>(null)
function showTps(): void {
  const turns = traj.value.turns
  const t = turns.length ? turns[turns.length - 1] : undefined
  if (!t || !t.endTs) return // 无结束标记(被 stop 卡在半路的脏帧):不造数字
  tpsTurn.value = t
  tpsLine.value = pushMeta('status', turnSpeedLine(t), true) ?? null
}
function refreshTps(): void {
  if (tpsTurn.value && tpsLine.value) tpsLine.value.text = turnSpeedLine(tpsTurn.value)
}
// retargetTps 切会话后重新对准速记行:它指向的回合对象与行都在**被切换的那份** traj/metas 里。
// 不重指的后果:新会话的 usage 帧会把旧会话那行改写成不相干的数字。
function retargetTps(): void {
  const turns = traj.value.turns
  let last: TrajTurn | undefined
  for (let i = turns.length - 1; i >= 0; i--) {
    if (turns[i].endTs) {
      last = turns[i]
      break
    }
  }
  tpsTurn.value = last ?? null
  tpsLine.value = [...metas.value].reverse().find((m) => m.tps) ?? null
}

// viewCache 会话视图缓存(按会话 id;见 sessioncache.ts)。缓存上限 = 页签上限,淘汰时连带清
// 该会话的续传游标 —— **缓存没了游标必须跟着没**,否则下次连上去会拿一个“指向已丢弃模型”的
// after 要差集,服务端又不会发 baseline ⇒ 流里凭空缺一大段(第一三九批修的真缺陷)。
const viewCache = new SessionCache()
viewCache.setEvict((id) => clearSessionCursor(id))

// saveView 把当前界面上的这份视图存进缓存(离开会话前调)。
// 存的都是**同一份引用**(model/traj/changes/metas 全不拷贝):换回去时赋值同一份 → 身份不变、
// 不重复包代理,而且存完之后对它的写入(比如 rebuild 里 flush 出来的那批帧)照样落在缓存上 ——
// 拷贝就会丢掉这份更新。metas 不 slice() 的理由同此(它按会话一份,不存在两会话共享同一数组)。
function saveView(key: string): void {
  if (!key) return
  const t = tabs.get(key)
  viewCache.put(key, {
    model: model.value,
    traj: traj.value,
    changes: changes.value,
    metas: metas.value,
    scrollTop: t?.scrollTop ?? 0,
    atBottom: t?.atBottom ?? true,
  })
}

// NOND-N1 提示 toast(状态机在 notices.ts):实时 `notice` 帧 + 连接后 /api/notices 回填,
// 两路按 id 去重。提示不进会话流 —— 它是「需要人回来」的信号,不是对话内容。
const toasts = ref<ToastState>(newToasts())
function onDismissToast(id: number): void {
  dismissToast(toasts.value, id)
}
// A-5#124 通知降级:能否弹**系统通知**只看来源与级别(判定逻辑在 notify.ts);
// 页内提示与来源无关(始终由上面的 toast 承担),所以 LAN IP 访问时什么都不会丢。
// 权限只在用户手势里申请(浏览器策略),且整个会话只申请一次。
const notifier = createNotifier({
  hostname: typeof location === 'undefined' ? '' : location.hostname,
  api: typeof Notification === 'undefined' ? undefined : Notification,
  ctor: typeof Notification === 'undefined' ? undefined : (title, opts) => new Notification(title, opts),
})
function onFirstGesture(): void {
  notifier.maybeRequest()
}
// A-5#125 数据根只读提示条:只读时写入全失败(会话/配置不保存),必须显式告知;
// 关闭只在本次会话生效 —— 刷新后重判(只读通常要人去修权限,不该被一次关闭永久遮掉)。
const rootBarClosed = ref(false)
const rootReadOnly = computed(() => state.value?.data_root_writable === false && !rootBarClosed.value)
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
// prefsOpen:本会话设置面板的开关(第一百三十四批)。与 openPanel 分开是两个状态变量,
// 共用一个抽屉壳 —— 两者语义不同(一个是「插件的附加面板」,一个是内置的本会话设置),
// 混成一个会让插件一注册就把内置入口顶掉。
// 开关入口在**右上角**(第一百三十六批从输入框工具条移来,见模板里的 .gear.ses)。
const prefsOpen = ref(false)
// prefsCount 本会话独立于全局的项数(0 = 全部跟随):右上角入口的徽标与强调态据它显示。
// 判据与页签方块、状态栏前缀同源(scope.ts 的 session_prefs)。
const prefsCount = computed(() => sessionSetCount(state.value))
function toggleSessionPrefs(): void {
  prefsOpen.value = !prefsOpen.value
}
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
    // 后台不轮询(批三):页面看不见时没人看这些数字,发出去只是白耗网络与服务端 IO。
    // 回前台由 onVisibility 立即补一次(见 wakePollers)。
    planTimer = setInterval(() => { if (!hidden()) void refreshPlan() }, 15000)
  } else if (!on && planTimer) {
    clearInterval(planTimer)
    planTimer = null
  }
}
watch(view, (v) => syncPlanTimer(v === 'board'), { immediate: true })
onMounted(() => {
  window.addEventListener('resize', onViewportResize)
  void refreshJobs()
  jobsTimer = setInterval(() => { if (!hidden()) void refreshJobs() }, 5000)
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
  if (restoring) return // 程序性滚动(恢复位置):不当作"用户滚了"
  stick = el.scrollHeight - el.scrollTop - el.clientHeight < 140
  // 记进当前页签:切回来应回到原处(而不是跳到最新 —— 读历史时被拽走是真打断)
  const t = tabs.get(curTabId())
  if (t) {
    t.scrollTop = el.scrollTop
    t.atBottom = stick
  }
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
// 进行中的流式内容(v1.4,批一):此前 chunk 累进模型却从不渲染,用户只看到「正在运行…」
// 硬等到落定。三者全空时传 undefined —— 槽位插件据此知道「此刻没有进行中内容」。
const pendingView = computed<PendingView | undefined>(() => {
  const m = model.value
  if (!m.pending && !m.pendingThink && !m.pendingTool) return undefined
  return { text: m.pending, think: m.pendingThink, tool: m.pendingTool }
})

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
    pushMeta('error', '加载更早消息失败: ' + (e as Error).message + '。上滚可重试。')
  } finally {
    loadingEarlier.value = false
  }
}
// 新消息/流式文本增长/历史重放/会话切换后:若贴底则 nextTick(等 DOM 更新)后滚到底。
// 依赖式是**计数器**而非「全量文本拼接」:此前每次渲染都 map+join 整个消息数组
// (n 条 × 每条全文 ⇒ O(n) 字符串拼接),批处理一帧几百条时是几百次 O(n) 叠加。
// 现在由消费引擎在一批处理完时打一个 tick,依赖成本 O(1)。
const contentTick = ref(0)
watch(contentTick, () => {
  if (view.value !== 'stream') return // 轨迹模式下不动流视图滚动位置
  const go = stick
  void nextTick(() => {
    if (go) pinBottom()
    else showNewest.value = true
  })
})
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

// 会话帧合帧消费(批零):到达一帧即同步消费一次的话,每条都触发一次消息列表 patch,
// 而 keyed diff 仍要遍历整棵列表做 key 比对 ⇒ 追加一条是 O(n)。首屏重放几百帧就是
// 几百次 O(n) 叠加 = O(n²)(实测 800 条消息 = 单个 7.9s 主线程长任务,页面白屏到出内容)。
// 入队 + 一个动画帧内批量消费 ⇒ 一帧只 patch 一次。调度器在页面隐藏时退 setTimeout
// (rAF 在隐藏页不触发,否则后台标签页会一直攒帧,回前台再一次性爆出来)。
const sessionQueue = new FrameQueue<SessionEvent>(
  (batch) => {
    for (const se of batch) {
      // 步数(仅本窗口会话):step/start 累加,turn/end 归零;用 kind 判定,不看 payload。
      if (se.Kind === 'step/start') steps.value++
      else if (se.Kind === 'turn/end') steps.value = 0
      consume(model.value, se)
      trajPush(traj.value, se)
      changesPush(changes.value, se)
      // 回合收尾 → 追一行速记;后到的 usage 帧补正同一行(见 showTps/refreshTps)
      if (se.Kind === 'turn/end') showTps()
      if (isUsage(se)) {
        refreshStats()
        refreshTps()
      }
    }
    // 贴底阅读时裁头部:长会话持续输出时 DOM/内存不随会话长度增长(上滚可重新取回)。
    // 放在批末尾而非逐帧:一批裁一次即可,逐帧裁会把 splice 成本乘上帧数。
    if (stick && model.value.msgs.length > MAX_LIVE_MSGS) trimHead(model.value)
    // 贴底滚动的信号(计数器,见 contentTick 注释)
    if (batch.length > 0) contentTick.value++
  },
  rafScheduler(),
)

// rebuild 吃 switchPlan 的决策对象,而不是一个裸布尔 —— 裸布尔的极向被写反过一次,
// 而且“清流/清游标/清 metas”三件事必须同进同出(第一三九批 review 逮到 metas 被无条件清,
// 把刚由缓存装回的速记行又抹掉了)。决策只有一个来源,写不成一半。
function rebuild(plan: SwitchPlan): void {
  // 会话切换/连接重建:先把**积压未消费**的帧消费掉再清。为何不能直接 clear():
  // 这些帧在 transport.onmessage 里已经 markCursor 记进续传游标了(消费是延后一帧批处理的),
  // 清掉它们不会重来 —— 这段消息在任何地方都不再出现(永久缺失)。
  // 为何消费进 model.value 是对的:此刻它还属于**产出这些帧的那个会话**
  // (switchTab 已先把它存进缓存,而缓存存的是同一个对象 ⇒ 内容一并落在缓存上)。
  sessionQueue.flush()
  sessionQueue.clear()
  // 会话切换/全新连接:清流重放全量(通道按 after 游标差集重放)
  if (!plan.keepStream) {
    // 换状态本体重放;页签的草稿/滚动不动(重放的是流,不是这个页签的使用痕迹)。
    model.value = tabs.replaceState(curTabId(), newModel()).state
    traj.value = newTraj()
    // 速记行的被盯对象属旧会话;不释放的话新会话的 usage 帧会把它改写(旧回合数字变形)
    tpsTurn.value = null
    tpsLine.value = null
    changes.value = newChanges()
    // metas 属**会话**、不属窗口:命中缓存的那条路由 switchTab 从缓存装回,这里清掉就等于
    // 刚装回就抹掉(TPS 速记/命令回显/错误行全丢)。故只在「清流重放」时清(见 resetMetas)。
    if (plan.resetMetas) metas.value = []
    loadingEarlier.value = false
    baseSeen.value = false
    if (plan.clearCursor) clearSessionCursor(curTabId())
  }
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
  // 会话帧合帧消费见 sessionQueue 定义(批零:入队 + 一个动画帧内批量消费 = O(n²) → O(n))
  transport.on('session', gate((f) => {
    // 代际检查已由 gate 在入队时完成(gen 有意义的时候);此处只管入队。
    sessionQueue.push(f.payload as SessionEvent)
  }))
  transport.on('status', gate((f) => {
    const was = state.value.running
    state.value.running = f.payload === 'running'
    // NOND-N3 后台完成提醒:回合在**后台**跑完时给一条系统通知。
    // 为何在 web 侧做:宿主不发这类提示(TUI/桌面壳各自负责自己的“人不在场”信号),
    // 只按提示流通知的话,切到别的标签等结果的人什么也收不到。
    if (was && !state.value.running && hidden()) notifier.fireEvent('gah 回合已完成', '切回来看结果')
  }))
  transport.on('command', gate((f) => {
    const r = f.payload as CommandResult
    // 别的会话执行的命令:输出不能抹在本会话的流里(会对不上账)。
    // curId 必须用**本页签自己的会话**(curTabId),不能用 state.session.id ——
    // 后者是服务端「当前会话」(handleState 固定下发 CurrentSession,与 ?session= 无关),
    // 页签绑的不是当前会话时会把**自己**的帧判成外来(命令回显被改写 + 弹层不弹)。
    const fo = foreignOwner(f, curTabId())
    if (fo) {
      pushMeta('command', foreignTodoText('command', fo, r.output || r.raw))
      return
    }
    if (r.error) {
      pushMeta('error', r.raw + ': ' + r.error)
    } else if (r.output) {
      pushMeta('command', r.output)
    }
  }))
  transport.on('error', gate((f) => {
    // 后端已把 agent/error 载荷归一为文本;这里再兜一层:对象载荷不再渲染成 "[object Object]"
    const p = f.payload as unknown
    pushMeta('error', typeof p === 'string' ? p : JSON.stringify(p))
  }))
  transport.on('confirm', gate((f) => {
    const req = f.payload as ConfirmRequest
    // **别的会话的审批不在这里弹**:那一步的决定会作用在那个会话上,
    // 在本会话弹出来等于骗用户替他点头(多窗口/页签并行时真实会发生)。
    // 也不能静默丢 —— 审批默认不限时地等,没人知道就一直挂着。⇒ 记一行 + 系统通知。
    const fo = foreignOwner(f, curTabId())
    if (fo) {
      const txt = foreignTodoText('confirm', fo, req?.prompt || '')
      pushMeta('status', txt + '(请到该会话处理)')
      notifier.fireEvent('gah 需要你确认', txt.slice(0, 120))
      return
    }
    confirm.value = req
    // 审批在后台弹出来 = 回合一直阻塞到超时:这是真正“需要人回来”的时刻。
    if (hidden()) notifier.fireEvent('gah 需要你确认', (req?.prompt || '').slice(0, 120))
  }))
  transport.on('question', gate((f) => {
    const req = f.payload as QuestionRequest
    const fo = foreignOwner(f, curTabId())
    if (fo) {
      const txt = foreignTodoText('question', fo, req?.prompt || '')
      pushMeta('status', txt + '(请到该会话处理)')
      notifier.fireEvent('gah 需要你作答', txt.slice(0, 120))
      return
    }
    question.value = req
    questionMin.value = false
    if (hidden()) notifier.fireEvent('gah 需要你作答', (req?.prompt || '').slice(0, 120))
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
  // G-E5-4:审批已裁决(confirm/resolved;Confirm 无端侧 id → 按 prompt 关联)。
  // err 非空 = 未等到应答。三种结局要分开说(2026-10-03 实机反馈):
  //   - canceled:用户自己按了停止 → **静默**关弹层,不推错误行(他没做错什么);
  //   - err:配了 confirm_timeout_sec 且真的到期 → 说一声,否则用户以为界面吞了他的决定;
  //   - 都空:正常裁决(本端弹层是残留的,关掉即可)。
  transport.on('confirmdone', gate((f) => {
    const p = f.payload as { prompt?: string; err?: string; canceled?: boolean }
    if (p?.prompt && confirm.value && confirm.value.prompt === p.prompt) confirm.value = null
    if (p?.err && !p.canceled) {
      pushMeta('status', '审批未等到应答(' + p.err + '),该动作未执行')
    }
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
    const n = f.payload as Notice
    pushNotice(toasts.value, n, Date.now())
    notifier.fire(n) // 仅 warn/error 且本机来源已授权时才真弹系统通知(否那么是空操作)
  }))
  // 回合结束时未注入的转向消息(agent/steer-dropped):本端输入栏在槽位内,拿不到它的
  // 内部状态,所以不自动回填 —— 但原文必须让人看见(不静默丢),TUI 侧同帧语义是「转为待发」。
  transport.on('steer_dropped', gate((f) => {
    const msgs = Array.isArray(f.payload) ? (f.payload as string[]) : []
    if (!msgs.length) return
    pushNotice(toasts.value, {
      id: -Date.now(), // 负数避开服务端正数 id,不干扰回填去重集
      level: 'warn',
      title: `回合已结束,${msgs.length} 条转向消息未及注入`,
      body: msgs.join(' / ') + '(请重新发送)',
      source: 'web',
      ts: new Date().toISOString(),
    }, Date.now())
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

// lastRunning 上一轮快照里在跑的会话(用于「从跑变不跑 ⇒ 标未读」)。
let lastRunning: string[] = []

async function refreshStats(): Promise<void> {
  const askedForTab = curTabId() // 飞行途中可能切页签,归属按**发问时**那个算
  const gen = connGen // 代际:迟到快照不得写进新一轮(可能已是另一个会话)
  try {
    state.value = await api.state()
    // 迟到快照直接丢弃:否则会用另一会话的 model/running/session_prefs 覆盖状态栏,
    // 并把 streamSessionId 校准成旧会话 → 下一轮误判「会话被切走」而重放一次(闪一下)。
    if (gen !== connGen || askedForTab !== curTabId()) return
    lastTick = Date.now() // 活跃心跳（睡眠检测基准）
    applyCustomTo(askedForTab, state.value)
    // 会话被**命令**切走(如 `/session new`、`/session switch`)时前端收不到任何信号:
    // SSE 订阅还挂在旧会话上 → 用户后续输入的消息服务端已记录,界面上却一个帧都不来(静默丢显示)。
    // 故以服务端快照为事实:监到当前会话 id 与流所绑定的不一致 → 重放全量(与侧栏切换同一条路径)。
    // 只有主页签跟随全局当前会话;其余页签用**自己的**键(W1,见 mainTabKey 注释)。
    const sid = mainTabKey !== null && askedForTab === mainTabKey
      ? (state.value.session?.id ?? '')
      : askedForTab
    if (sid && sid !== streamSessionId) {
      if (streamSessionId === '') {
        streamSessionId = sid // 首次快照 / 本地刚切换(见 sessionChanged)→ 只校准,不重放
      } else {
        streamSessionId = sid
        rebuild(switchPlan(false))
        refreshKey.value++
      }
    }
    // 页签运行标记 + 未读:后台页签没有事件连接(每页签一条),只能靠这份快照 ——
    // 「从跑变不跑」且不是当前页签 ⇒ 标未读(切回去就知道有结果了)。
    const running = state.value.running_sessions || []
    const mainId = state.value.session?.id ?? ''
    // 首次校准**只做一次**(占位页 → 服务端当前会话):改绑而不是新建 ——
    // 新建会让用户一进来就看到两个页签(一个还是没用的占位)。
    // 必须只做一次:页签打开的可能是**非当前**会话,若每轮都校准,每轮都会把页签
    // 键强行改回全局当前会话 —— 真机手测据此逮到"两个页签标题一样、关闭失效"。
    if (!calibrated && mainId && mainId !== tabId.value) {
      // 主页签改绑后 mainTabKey 必须跟着走:否则下一轮 askedForTab !== mainTabKey,
      // 主页签从此不再跟随服务端当前会话(命令 /session new 切走后界面不翻)。
      if (mainTabKey !== null && mainTabKey === (tabId.value || 'main')) mainTabKey = mainId
      viewCache.rename(tabId.value || 'main', mainId) // 缓存跟着改绑(否则占位名那份永远取不到)
      tabs.rekey(tabId.value || 'main', mainId)
      tabId.value = mainId
      calibrated = true
      // 改绑后 api 侧必须跟进:否则 boundSession 仍是首帧的 ''(主会话),此后会话档写入
      // 全落成全局 —— 这正是「主会话无法单独设置模型/角色」的根(第一百三十八批)。
      api.bindSession(mainId)
      void syncTitles()
    }
    for (const id of lastRunning) {
      if (running.includes(id) || id === curTabId()) continue
      tabs.markUnread(id)
    }
    lastRunning = running.slice()
    tabs.markRunning(running, mainId)
    // 当前页签的角色徽标(后端按会话给的生效值;跟随全局时为空 = 不显示徽标)
    if (tabId.value) {
      const t = tabs.get(tabId.value)
      if (t) t.role = state.value.role || ''
    }
    syncTabs()
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
// wakePollers 回前台补一次轮询(批三):后台期间定时器只是**跳过**请求,回来时不会追补 ⇒
// 必须现拉一次,否则状态栏停留在后台之前的旧值(上下文占用、运行徽标都可能已变)。
function wakePollers(): void {
  void refreshStats()
  void refreshJobs()
  if (view.value === 'board') void refreshPlan()
}

// onVisibility 回到前台(桌面壳最小化恢复)同上;后台时不动(省网络)。
function onVisibility(): void {
  if (document.visibilityState !== 'visible') return
  wakePollers()
  onWake()
}

// hidden 页面是否在后台(切标签 / 最小化 / 壳窗口隐藏)。
// 后台才有系统通知的意义:前台自有界面反馈,再弹通知只是噪音(与 TUI `/notify auto` 同口径)。
function hidden(): boolean {
  return typeof document !== 'undefined' && document.visibilityState !== 'visible'
}

// stopTurn 中止运行中的回合(输入区「停止」按钮)。
// 为何必须有这个出口:审批现在默认**不限时地等**(policy-guard),没有中止入口时用户只剩
// 「拒绝」一招 —— 而拒绝只是让这一步失败,模型会换个方式再试,不是他要的「停下来」。
async function stopTurn(): Promise<void> {
  try {
    await api.control({ cancel: true })
  } catch (e) {
    pushMeta('error', '停止失败:' + (e as Error).message)
  }
}

// onSubmit 提交回合;返回 false = 未受理(离线/失败)→ 调用方**保留草稿与附件**。
// S-P1-3 纪律:断连期间不提交(宁可让用户等,也不要把输入掷进不可达的链路后丢掉)。
async function onSubmit(text: string, attachments?: string[]): Promise<boolean> {
  const t = text.trim()
  if (!t) return true
  if (!submitAllowed(conn.value)) {
    pushMeta('error', '未发送:连接不可用。草稿与附件已保留,恢复后请重新发送。')
    return false
  }
  try {
    const r = await api.input(t, attachments ?? [])
    // 回合进行中的追加消息:宿主把它**注入当前回合**(转向),模型下一次请求即可见,
    // 不是新回合。回执里说清楚,否则用户以为输入掉进了黑洞(真机反馈)。
    if (r?.accepted === 'steer') {
      pushMeta('status', '已注入当前回合(模型下一次请求即可见)')
    }
    // 提交成功即把主视图切回会话流。为何:视图是全屏切换的,而 `/diff` 会把视图永久切到
    // 「变更」且此后没有任何逻辑切回 —— 用户接着发消息,回复全进了看不见的「会话流」,
    // 屏幕上是「变更」的空态文案。真机反馈正是如此:「让它查 skill/MCP、甚至只说你好,
    // 返回的都是『本会话还没有捕获到文件改动』」。人发了消息就该看到回复。
    if (view.value !== 'stream') view.value = 'stream'
    return true
  } catch (e) {
    pushMeta('error', '未发送:' + (e as Error).message + '。草稿已保留。')
    // 提交失败往往就是链路已断:探一次并据实降级(不靠猜测)
    void probeConn()
    return false
  }
}

// 会话切换/新建(侧栏/抽屉):刷新状态 + 重建 SSE(全量重放新会话历史)+ 侧栏列表刷新
function sessionChanged(): void {
  streamSessionId = '' // 本地已切换:下一次 refreshStats 只校准 id,不重复重放
  void refreshStats()
  rebuild(switchPlan(false))
  refreshKey.value++
}

// —— 会话页签 ——
//
// 为什么侧栏点会话 = 在本页签打开,而不是全局切换:页签模式下"当前会话"必须稳定
// (它一被换掉,正在跑的回合就面临切走闸门,且其它页签的绑定全部漂移)。
// 侧栏那个「切换」按钮的直觉(换一个看)由页签满足;要真全局切换仍是 TUI/命令 `/session switch`。

/** 切换到某个页签(已打开则恢复它自己的流,首次打开则拉历史)。 */
function switchTab(id: string): void {
  const key = id || 'main'
  if (key === tabId.value) return
  // 切走前:把当前会话的流存回它的页签 + 存进会话视图缓存。
  // 只在**它还开着**时存:关页签后再走这条路会用 ensure 把刚关掉的页签重新建出来
  // (真机手测逮到:关掉一个页签后它又回来了 —— 计数一直是 2)。
  if (tabId.value && tabs.has(tabId.value)) {
    // 第一三九批后**缓存以 viewCache 为准**(跟着会话,不跟页签);这一行只是让页签自己的
    // make 回退值不落在空模型上(重建页签时 `() => t.state` 会用到)。
    tabs.get(tabId.value)!.state = model.value
    saveDraftToTab(tabId.value)
    saveView(tabId.value)
  }
  tabs.activate(key)
  tabId.value = key
  const cached = viewCache.get(key)
  const plan = switchPlan(!!cached)
  // 命中缓存:直接接上(不空白、不整窗重放)。未命中(真没开过 / 已被 LRU 淘汰)则不装任何东西 ——
  // 交给下面 rebuild(plan) 走「空模型 + 尾窗重放」,清流/清游标/清 metas 三件事同源。
  // 页签自带的那份 state 不采纳:它的游标已在淘汰时一并清掉,拿旧模型配新游标、
  // 或不配游标,都会让流里缺一段或重一段。
  if (plan.keepStream && cached) {
    model.value = cached.model
    traj.value = cached.traj
    changes.value = cached.changes
    metas.value = cached.metas
    // 滚动位置也跟会话走(页签关过又开时,页签上是 0/贴底,只有缓存知道它读到了哪)
    const t = tabs.get(key)
    if (t) {
      t.scrollTop = cached.scrollTop
      t.atBottom = cached.atBottom
    }
    retargetTps()
  }
  // 请求层与事件层一起切(甲方案:每页签一条连接 ⇒ 换绑即重连)
  // api 侧绑**真实 id**:主会话占位 'main' 解析成当前会话 id,否则会话档写入会落成全局
  // (见 sessionIdFor 注释)。事件层仍用 '' 代主会话:它的 scopeOf 归一化、且游标分桶键
  // (gah.lastSeq.main)换了会让老用户的续传游标失配一次。
  api.bindSession(sessionIdFor(key))
  setTransportSession(key === 'main' ? '' : key)
  streamSessionId = ''
  rebuild(plan) // 命中缓存 ⇒ 保留流/游标/metas(只补差集);未命中 ⇒ 清干净重放
  void refreshStats()
  syncTabs()
  beginScrollRestore(key)
  // 草稿要等 DOM 更新完再装(此时组件已是当前页签的那个)
  void nextTick(() => loadDraftFromTab(key))
}

/** 在本页签打开某个会话(侧栏点会话)。已开着就直接切过去。 */
function openTabIn(rawId: string, title?: string): void {
  const id = rawId || 'main'
  tabs.ensure(id, newModel, title || (id === 'main' ? '主会话' : shortSessionId(id) || id))
  switchTab(id)
}

/** 新建一个页签(spawn:建独立会话且**不动**当前会话)。 */
async function newTab(): Promise<void> {
  if (tabs.atLimit()) {
    pushMeta('error', '页签已达上限(' + tabs.room() + ' 个可开):先关掉一个再新建。')
    return
  }
  try {
    const r = await api.sessionSpawn()
    tabs.ensure(r.id, newModel, shortSessionId(r.id) || r.id)
    switchTab(r.id)
    void syncTitles()
  } catch (e) {
    pushMeta('error', '新建页签失败:' + (e as Error).message)
  }
}

/** 关页签。回合属于后端会话不属于窗口 —— 关掉在跑的那个会在后台继续跑。 */
function closeTab(id: string): void {
  const out = tabs.close(id)
  if (!out.closed) return
  // 关掉的是主页签 ⇒ 本窗口不再有「跟随服务端当前会话」的页签(其余页签都是显式会话)。
  if (mainTabKey !== null && mainTabKey === id) mainTabKey = null
  // 关页签**不丢会话视图**:缓存跟着会话走,不是跟着页签 —— 下次从侧栏点开这个会话
  // 直接接上(第一三九批)。要存的是当前页签的这份;后台页签的在切走时已经存过了。
  if (id === tabId.value) saveView(id)
  probed.delete(id)
  if (out.wasRunning) {
    notifier.fireEvent('gah 页签已关闭', '「' + out.closed.title + '」在后台继续运行')
  }
  if (out.nextActive) {
    // 关后台页签时 tabId 先被清空 ⇒ switchTab 的「切走前存草稿」分支被跳过,
    // 输入框会被目标页签的旧草稿覆盖(用户刚打的字静默丢失)。这里先补存一次。
    if (tabId.value && tabs.has(tabId.value)) saveDraftToTab(tabId.value)
    tabId.value = '' // 强制走完整切换流程(不再回存已关闭页签的状态)
    switchTab(out.nextActive)
  } else {
    // 不允许"零页签":回主会话页签(主会话永远存在)
    tabs.ensure('main', newModel, '主会话')
    tabId.value = ''
    switchTab('main')
  }
  syncTabs()
}

// inputRef 当前输入框组件实例(外部 UI 插件覆盖 input 槽位时可能没有 getText/setText,
// 所以下面每处调用都判空回落 —— 不假设槽位实现是谁)。
const inputRef = ref<{ getText?: () => string; setText?: (v: string) => void } | null>(null)

/** 切走前把输入框里的字存回当前页签(草稿跟着页签走,切回来还在)。 */
function saveDraftToTab(id: string): void {
  const t = tabs.get(id)
  const v = inputRef.value?.getText?.()
  if (t && typeof v === 'string') t.draft = v
}

/** 切走后把目标页签的草稿装进输入框(没有草稿就清空,别把上一个会话的字带过去)。 */
function loadDraftFromTab(id: string): void {
  inputRef.value?.setText?.(tabs.get(id)?.draft ?? '')
}


// —— 切页签后恢复滚动位置 ——
//
// 关键:**必须等内容稳定**。切换页签后事件连接才刚重连、历史还在回放,此刻 scrollHeight
// 远小于最终值 —— 这时设 scrollTop 会被后续渲染一路顶走(真机实测:目标 1200,
// 切回来落在 14364 = 贴底)。所以每帧往目标值上拉,直到 scrollHeight 连续 3 帧不变
// 或达到帧数上限(约 2s)。
//
// 中途用户自己滚了就停手:不跟人抢滚动条。
let scrollTimer = 0
// restoring 恢复进行中:期间**不采信**滚动事件。
// 为什么不能用"设完 scrollTop 后 60ms 内忽略"那种时间窗 —— 事件是异步派发的,
// 时间窗一过就漏进来,把 atBottom 改回 true,恢复循环随即"以为用户自己滚了"而让位
// (真机实测:目标 1200,切回来落在 0)。标志位覆盖整段恢复期,没有竞态窗口。
let restoring = false
function cancelScrollRestore(): void {
  restoring = false
  if (scrollTimer) {
    clearTimeout(scrollTimer)
    scrollTimer = 0
  }
}
function beginScrollRestore(id: string): void {
  cancelScrollRestore()
  const t = tabs.get(id)
  if (!t) return
  // 必须等 DOM 更新完再开始:此刻 streamEl 还指着**旧会话的那个 section**(切页签会
  // 重建它),对 detached 元素设 scrollTop 无效且 scrollHeight 恒为 0 ——
  // 真机实测正因此恢复落空(目标 1200,回来是 0)。
  void nextTick(() => startScrollRestore(t))
  stick = t.atBottom
  // 原本就贴底的页签:回到贴底(交给既有的 pinBottom 语义,不必逐帧拉)
  if (t.atBottom || !t.scrollTop) {
    void nextTick(() => pinBottom())
    return
  }
}

function startScrollRestore(t: { scrollTop: number; atBottom: boolean }): void {
  restoring = true
  const target = t.scrollTop
  let tries = 0
  // 只做一件事:把滚动位置拉到目标,拉到就收工。
  // 早先那版还判断「scrollHeight 是否稳定」「用户是否中途滚走」,结果把自己掐死 ——
  // 真机实测目标 1200、切回来落在 0;而同样"延时一下再设"的探针一次就成。
  // 现在:恢复期不采信滚动事件(restoring),拉到目标即结束;之后用户滚动立刻照常生效。
  const step = (): void => {
    const el = streamEl.value
    if (el) el.scrollTop = target
    tries++
    const reached = !!el && Math.abs(el.scrollTop - target) < 2
    if ((reached && tries > 2) || tries > 40) {
      scrollTimer = 0
      restoring = false
      return
    }
    scrollTimer = window.setTimeout(step, 50)
  }
  scrollTimer = window.setTimeout(step, 50)
}

/** 会话被删(侧栏):若有开着页签,那一页已经不存在 → 关掉它。 */
function onSessionDeleted(id: string): void {
  const key = id || 'main'
  // 会话没了,它的视图缓存也一并清(留着就是给不存在的会话占内存);顺带清续传游标,
  // 否则同名会话(极少:id 复用)重建后会拿一个指向已丢弃模型的 after 去要差集。
  viewCache.drop(key)
  clearSessionCursor(key)
  if (!tabs.has(key)) return
  if (key === curTabId()) {
    // 当前页签被删:后端已新建空会话承接 → 直接去那个新的
    const cur = state.value.session?.id ?? ''
    if (cur && cur !== key) {
      openTabIn(cur)
      return
    }
  }
  closeTab(key)
}

/** 页签标题拿真实会话名(侧栏拉过一次列表,这里只做一次性对齐;无名则用短 id)。 */
async function syncTitles(): Promise<void> {
  try {
    const list = await api.sessions()
    for (const s of list) {
      const key = s.ID || 'main'
      const t = tabs.get(key)
      if (!t) continue
      const label = tabTitle(s.Name, key)
      if (t.title !== label) {
        t.title = label
        tabs.ensure(key, () => t.state, label)
      }
    }
    syncTabs()
  } catch {
    /* 拿不到名字就用短 id(页签仍可用),不报错打断 */
  }
}

async function onQuestionAnswer(values: string[], text: string): Promise<void> {
  const req = question.value
  if (!req) return
  // S-P1-3:断开时不掷掉作答 —— 弹层/角标保留(作答只回填提问通道,丢失即只能等超时)
  if (!submitAllowed(conn.value)) {
    pushMeta('error', '作答未提交:连接已断开。弹层已保留,恢复后可重试。')
    return
  }
  try {
    await api.questionAnswer(req.id, values, text)
    question.value = null
  } catch (e) {
    pushMeta('error', '作答提交失败: ' + (e as Error).message + '。弹层已保留。')
    void probeConn()
  }
}

async function onAnswer(ok: boolean): Promise<void> {
  const req = confirm.value
  if (!req) return
  // S-P1-3:断开时不掷掉审批决定 —— 弹层保留,恢复后可重答(服务端侧超时仍按安全默认拒绝)
  if (!submitAllowed(conn.value)) {
    pushMeta('error', '审批未提交:连接已断开。弹层已保留,恢复后可重试。')
    return
  }
  try {
    await api.confirm(req.id, ok)
    confirm.value = null
  } catch (e) {
    // 未送达就不关弹层:关掉等于丢掉用户的决定(回合会一直阻塞到超时)
    pushMeta('error', '审批应答未送达:' + (e as Error).message + '。弹层已保留。')
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
  // A-5#124:系统通知权限只在**用户手势**里申请(无手势的自动请求会被浏览器静默拒),
  // 首次交互触发一次;非本机来源内部直接跳过(不弹权限条、不报错)。
  window.addEventListener('pointerdown', onFirstGesture, { once: true, capture: true })
  await refreshStats() // 首帧就探活:服务端本就不在时立刻显离线横幅(非「安静会话」误判)
  rebuild(switchPlan(false))
  // 统计节流刷新(usage 事件外,兜底上下文/缓存显示)
  statsTimer = setInterval(() => { if (!hidden()) void refreshStats() }, 3000)
})
onUnmounted(() => {
  cancelScrollRestore()
  window.removeEventListener('pointerdown', onFirstGesture, { capture: true })
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
      <component
        :is="slotComponent('statusbar') || StatusBar"
        :state="state"
        :conn="conn.state"
        :cur-steps="steps"
        :traj="traj"
        @open-about="openAboutSettings"
      />
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
      <!-- 当前会话设置(第一百三十六批移来、第一百三十七批独立):从其它 gear 按钮里**单独分出**一身
            —— 它改的是「这个页签用什么」,与右侧的全局设置/任务/侧栏/视图不是一类东西。分隔线 + 常驻强调色
            + 右对齐,解决用户反馈的「配色不够突出、难以识别」。 -->
      <span class="gear-sep" aria-hidden="true" />
      <button
        class="gear ses"
        :class="{ on: prefsOpen, scoped: prefsCount > 0 }"
        :aria-expanded="prefsOpen"
        :data-tip="
          prefsCount > 0
            ? '当前会话设置:' + prefsCount + ' 项独立于全局(点开可调模型/思考/沙箱/审批)'
            : '当前会话设置:模型/思考/沙箱/审批只作用于当前页签(全局默认在「设置」里改)'
        "
        @click="toggleSessionPrefs()"
      >
        <span class="ses-full">当前会话设置</span><span class="ses-short">会话设置</span
        ><span v-if="prefsCount" class="ses-badge">{{ prefsCount }}</span>
      </button>
    </section>

    <TabBar
      :tabs="tabList"
      :active="tabId || 'main'"
      :limit-reached="tabs.atLimit()"
      @select="switchTab"
      @close="closeTab"
      @new="newTab"
    />

    <div class="main">
      <!-- 左侧历史抽屉(可收起):会话 + 工作区 -->
      <Sidebar
        :refresh-key="refreshKey"
        :cur-session="tabId"
        :cur-key="state.session?.key"
        :running-sessions="state.running_sessions || []"
        :cur-steps="steps"
        @session-changed="sessionChanged"
        @open-tab="openTabIn"
        @new-tab="newTab"
        @session-deleted="onSessionDeleted"
        @open-panel="openPanel = $event"
      />

      <!-- 右侧列:会话流 + 输入框(左右分割;输入框只在右侧底部,不横跨侧栏) -->
      <div class="content" :class="{ empty }">
        <!-- 非会话流视图的常驻提醒(真机反馈):视图是全屏切换的,切到轨迹/变更/看板后,
             新消息与回复都落在看不见的会话流里 —— 不给提示就会被误读成「agent 什么也没返回」
             (原话:「让它查 skill 或 MCP,返回的总是『本会话还没有捕获到文件改动』」)。
             提醒必须在**任何**非流视图可见,不能只写在变更视图的空态里。 -->
        <div v-if="view !== 'stream'" class="vbar" role="status">
          <span class="vbar-text">
            当前是「{{ VIEW_LABEL[view] }}」视图 —— 新消息与回复都显示在「会话流」里
          </span>
          <button class="vbar-btn" data-tip="切回会话流视图" @click="view = 'stream'">回到会话流</button>
        </div>
        <!-- 产物栏(借鉴 4):只在**流视图**出现,且只在有落盘改动时出现;
             放在会话流上方而不是常驻右列 —— 常驻右列会把主列压窄(布局纪律)。 -->
        <ArtifactsBar
          v-if="view === 'stream'"
          :model="changes"
          :partial="partialWindow"
          @review="view = 'changes'"
        />
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
            <component
              :is="slotComponent('stream') || 'div'"
              :frames="model.msgs"
              :metas="metas"
              :running="state.running"
              :pending="pendingView"
            />
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
            :disabled="offline"
            ref="inputRef"
            :busy="state.running"
            :on-cancel="stopTurn"
            :disabled-hint="offline ? '连接已断开:草稿与附件已保留,恢复后请重新发送' : ''"
            :on-submit="onSubmit"
            :state="state"
            :centered="empty"
            @session-changed="sessionChanged"
            @changed="refreshStats"
          />
        </section>
      </div>

      <!-- S-P2-1 侧栏停靠区(单面板):内容复用既有视图组件,不另造渲染。
           显隐走 fade 过渡(style.css):宽屏下它是并排的 flex 兄弟,宽度变化仍是瞬时的
           (拖拽调宽要手感,不能加宽过渡),但内容面不再硬切。 -->
      <Transition name="fade">
        <DockView
          v-if="dock.open"
          :panels="dockPanels"
          :panel="dock.panel"
          :width="dock.width"
          :narrow="dockNarrow"
          :body-pad="dock.panel !== 'jobs'"
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
      </Transition>
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

    <!-- 本会话设置(第一百三十四批):会话级参数的编辑入口,与设置面板(全局)物理分开。
         复用 extra-panel 抽屉的壳与动效 —— 不为它再造一层浮层。 -->
    <Transition name="pane">
      <div v-if="prefsOpen" class="ext-mask" @click.self="prefsOpen = false">
        <aside class="ext-panel">
          <div class="ep-body">
            <SessionPrefsPanel :state="state" @close="prefsOpen = false" @changed="refreshStats" />
          </div>
        </aside>
      </div>
    </Transition>

    <!-- v2 扩展点:附加面板抽屉(插件声明 extra-panel) -->
    <Transition name="pane">
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
    </Transition>

    <!-- 上滚读历史时新消息到达 → 回底胶囊 -->
    <button v-if="showNewest" class="newest" data-tip="回到最新消息" @click="goNewest">
      ↓ 新消息
    </button>

    <!-- A-5#125 数据根只读:写入全失败且**只**落启动日志时用户看不到(表现=发消息没反应)。
         颜色用语义 token(--tool-* 橙系),可关闭;只读多半在壳/容器里,故文案给路径与下一步。 -->
    <div v-if="rootReadOnly" class="root-bar" role="alert" data-ui-root-readonly>
      <span class="rb-text">
        数据目录不可写:{{ state?.data_root }} —— 会话与配置修改**无法保存**,请修复权限后重启(或把数据目录挪到可写位置)
      </span>
      <button class="rb-x" data-tip="关闭本条提示" aria-label="关闭提示条" @click="rootBarClosed = true">×</button>
    </div>
  </div>
</template>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100%;
}
/* A-5#125 只读数据根提示条:贴在会话区底部(状态栏在最上、输入框在下,这条要显眼但不挡操作) */
.root-bar {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin: 0 14px 8px;
  padding: 8px 12px;
  border: 1px solid var(--tool-line);
  border-radius: var(--r-card);
  background: var(--tool-soft);
  color: var(--tool-strong);
  font-size: 12px;
  line-height: 1.5;
}
.rb-text {
  flex: 1;
  word-break: break-all;
}
.rb-x {
  flex: 0 0 auto;
  border: 0;
  background: transparent;
  color: inherit;
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
  padding: 2px 4px;
  border-radius: var(--r-input);
  transition: background var(--dur-fast) var(--ease-out);
}
.rb-x:hover {
  background: color-mix(in srgb, var(--tool) 18%, transparent);
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
/* 当前会话设置(第一百三十六批移来、第一百三十七批独立):从输入框工具条移到右上角,与其它
   gear 按钮用分隔线隔开、靠右独占。**常驻**强调色(不是只有 scoped 才变色)—— 用户反馈
   「配色不够突出、难以识别」:跟随全局时它和灰色 gear 长得一样,等于没入口。
   分两档:跟随全局 = 软底 + 强调色描边/字;本会话真压过全局 = 实心强调色 + 反白计数徽标。 */
.gear-sep {
  flex: none;
  width: 1px;
  height: 16px;
  margin: 0 2px;
  background: var(--line-strong);
}
.gear.ses {
  border-color: var(--accent-line);
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}
.gear.ses:hover {
  border-color: var(--accent);
  background: var(--sel-bg);
  color: var(--accent);
}
.gear.ses.scoped {
  border-color: var(--accent);
  background: var(--accent);
  color: var(--fg-on-accent);
}
.gear.ses.scoped:hover {
  border-color: var(--accent-hover);
  background: var(--accent-hover);
  color: var(--fg-on-accent);
}
/* 面板开着时给一圈软环(两档都适用):与实心/软底的颜色都拉开区分。 */
.gear.ses.on {
  box-shadow: 0 0 0 2px var(--accent-soft);
}
.ses-badge {
  margin-left: 5px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--accent);
  color: var(--fg-on-accent);
  font-size: 10px;
  font-weight: 600;
}
.gear.ses.scoped .ses-badge {
  background: var(--fg-on-accent);
  color: var(--accent);
}
/* 窄窗(≤900px):右上角按钮组已经吃紧,长标题会再把底栏顶出横向溢出(角色徽标护栏实测
   820×560 溢出 7px)。缩为「会话设置」(保留可识别的主体,去掉限定词),而不是整个隐掉 ——
   用户反馈的正是入口不够明显。 */
.ses-short {
  display: none;
}
@media (max-width: 900px) {
  .ses-full {
    display: none;
  }
  .ses-short {
    display: inline;
  }
}
/* 窄窗(≤760px):底栏本就贴边,右上角按钮组再宽一点就会把底栏顶出横向溢出
   (布局护栏 test:layout 700×460 实测 60px)。收紧间距与内边距,而不是隐藏入口 ——
   用户反馈的正是「入口不够明显」,窄窗下更不能把它藏掉。 */
@media (max-width: 760px) {
  .statusbar-slot {
    gap: 4px;
    padding: 4px 10px;
  }
  .gear {
    padding: 1px 5px;
  }
  .gear-sep {
    margin: 0 1px;
  }
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
/* v2 扩展点:附加面板抽屉(固定右侧,对齐 JobsPanel 几何;taste 纪律)
   显隐动效跟设置抽屉同一套(pane 过渡,见 style.css)。 */
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
  transition: color var(--dur-fast) var(--ease-out);
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
/* 视图切换入场(批四 4a):会话流 / 轨迹 / 变更 / 看板四者都是 App.vue 里的 v-if 分支
   ⇒ 切换就是**元素重建**,给直接子元素一次性入场动画即可,不必再用 <Transition> 包一层
   (那会给流视图多套一个 DOM 元素,而 .stream-slot 是滚动容器,多一层就多一处几何风险)。
   曲线与时长取会话流消息入场(msg-in)同一套 token,故另起 view-in 而不散写数值。
   注:.stream-slot > * 只命中视图根元素(消息行在 .stream 内部,不是直接子级)。 */
.stream-slot > * {
  animation: view-in var(--dur-base) var(--ease-out);
}
@keyframes view-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
/* 非会话流视图的常驻提醒条(单强调色,不抢消息流):与看板动作同色系 —— --accent 单色 */
.vbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 10px 36px 0;
  padding: 7px 12px;
  border: 1px solid var(--accent);
  border-radius: 6px;
  background: var(--accent-soft);
  font-size: 12px;
}
.vbar-text {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--fg);
}
.vbar-btn {
  margin-left: auto;
  flex: none;
  padding: 3px 10px;
  border: 1px solid var(--accent);
  border-radius: 999px;
  background: none;
  color: var(--accent);
  font-size: 12px;
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out);
}
.vbar-btn:hover {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--fg-on-accent);
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
  transition: border-color var(--dur-fast) var(--ease-out);
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
  transition: background var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
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
  transition: color var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
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
