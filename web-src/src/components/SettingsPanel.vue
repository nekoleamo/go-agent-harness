<script setup lang="ts">
// 可视化设置抽屉(状态栏 ⚙ 入口;App 持有 open)。
// 分组:模型/推理(thinking·sandbox)/历史与压缩/Provider/插件与指令。
// 破坏性动作(删 provider、卸载插件、压缩)经全局确认条(askConfirm)。
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, rolePackDownloadUrl, rolePackName, skillPackDownloadUrl, skillPackName  } from '../api'
import { byteLength } from '../bytes'
import { autostartState, checkUpdate, installUpdate, isDesktop, pickDirectory, updateState, type UpdateSnapshot } from '../desktop'
import { currentModelValue, modelOptionValue, withCurrentModel } from '../modelsel'
import { settingSections } from '../registry'
import { loadUIPlugins, uiPluginDigests, uiPluginErrors, uiPluginTrustNote } from '../plugins'
import { digestLine } from '../plugininfo'
import { PROVIDER_PRESETS, explainProbeError, type ProviderPreset } from '../providers'
import { cronShapeError, fmtAbs, fmtNextRun, statusLabel } from '../schedule'
import {
  MCP_MODE_DIRECT,
  MCP_MODE_SEARCH,
  canEdit,
  draftFrom,
  draftKey,
  fmtCommand,
  modeHint,
  modeLabel,
  normalizeCommand,
  serverStateLabel,
  sourceLabel,
  validateDraft,
  viewNotices,
  type McpDraft,
} from '../mcp'
import type { AskConfirm, InstallView, McpServer, McpView, PluginInfo, ProviderInfo, ProviderModelGroup, RolePackResult, RoleSpec, Schedule, SkillInfo, StateView, ToolDef, TrashRoleEntry, TrashSkillEntry } from '../types'

const props = defineProps<{
  open: boolean
  state: StateView
  focus?: string // 打开时定位到某一段(首启引导传 'provider')
  schedTick?: number // 定时计划运行信号(App 收到 schedule/run 帧后 +1)
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'changed'): void }>()

const ask = inject<(a: AskConfirm) => void>('askConfirm')

// 桌面壳专属能力:判定与调用都收在 ../desktop(配了单测 —— 那条判定错了就会表现为
// 「桌面版里没有桌面功能」,而页面上无从自证)。
const updBusy = ref(false)
const updMsg = ref('')
const updOk = ref(false)
// updAvailable 有更新待确认:此时底栏多一个「立即升级」按钮,且**必须**先经过二次确认。
// 2026-10-03 用户要求:「检查更新后,应先提示是否需要更新,确认升级后,再下载安装」。
const updAvailable = ref('')

// —— 插件安装(2026-10-03)——
// 两段式安装:先 preview 取确认文案(与服务端 ConfirmPrompt 同一份),用户点头后才真装。
// 服务端**没有**确认服务,故必须由前端带 confirmed:true;忘了弹确认 ⇒ 400,而不会静默安装。
const installSpec = ref('')
const installBusy = ref(false)
const installRows = ref<InstallView[]>([])
const installErr = ref('')
const installOk = ref('')
// installWarn 独立于 installOk:成功回执会被下一条操作冲掉,这类长期影响必须占一行不混进去。
const installWarn = ref('')

async function refreshInstallList(): Promise<void> {
  try {
    installRows.value = await api.pluginInstallList()
  } catch (e) {
    installErr.value = '读取已装插件失败:' + (e as Error).message
  }
}

async function doInstall(): Promise<void> {
  const spec = installSpec.value.trim()
  if (!spec || installBusy.value) return
  installErr.value = ''
  installOk.value = ''
  installWarn.value = ''
  installBusy.value = true
  try {
    const pv = await api.pluginInstallPreview(spec)
    // 二次确认(与删 provider / 卸载角色同一处确认条):文案由服务端生成,
    // 三个入口(CLI / TUI / 面板)看到的是同一句话。
    await new Promise<void>((done) => {
      guard(pv.prompt, true, () => {
        done()
        void doPluginInstallNow(spec)
      })
    })
  } catch (e) {
    installErr.value = '安装失败:' + (e as Error).message
  } finally {
    installBusy.value = false
  }
}

async function doPluginInstallNow(spec: string): Promise<void> {
  try {
    const r = await api.pluginInstall(spec)
    installOk.value = `已安装 ${r.id} → ${r.dir}` + (r.hint ? '(' + r.hint + ')' : '')
    // 补依赖是**供应链面被扩宽**的事实,不能混在一句成功回执里(用户扫一眼就过)。
    installWarn.value = r.tidied
      ? '注意:仓库的 go.mod 不完整,构建前补跑过 go mod tidy —— 这个插件引入了仓库原本没声明的模块依赖。'
      : 
    installSpec.value = ''
    await refreshInstallList()
  } catch (e) {
    installErr.value = '安装失败:' + (e as Error).message
  }
}

// doUninstallPlugin 卸载(含白名单条目撤销),二次确认。
function doUninstallPlugin(id: string): void {
  guard(`卸载插件 ${id}?会删掉它的目录与白名单条目(不影响已有会话与配置)`, true, async () => {
    try {
      await api.pluginUninstall(id)
      installOk.value = '已卸载 ' + id
      await refreshInstallList()
    } catch (e) {
      installErr.value = '卸载失败:' + (e as Error).message
    }
  })
}

// doTrustPlugin 登记进哈希白名单(被完整性闸拒了、但你确认来源可信时用)。
function doTrustPlugin(name: string): void {
  guard(`把 ${name} 登记进哈希白名单?登记的是**当前盘上那一份**的 sha256`, true, async () => {
    try {
      await api.pluginTrust(name)
      installOk.value = '已登记 ' + name
      await refreshInstallList()
    } catch (e) {
      installErr.value = '登记失败:' + (e as Error).message
    }
  })
}

// doUntrustPlugin 撤销登记(之后该插件会被拒绝加载)。
function doUntrustPlugin(name: string): void {
  guard(`从白名单移除 ${name}?移除后它会被拒绝加载`, true, async () => {
    try {
      await api.pluginUntrust(name)
      installOk.value = '已移除 ' + name
      await refreshInstallList()
    } catch (e) {
      installErr.value = '撤销失败:' + (e as Error).message
    }
  })
}

// pickInstallDir 桌面端用原生选择器挑本地插件源码目录;浏览器形态返回空(手打路径)。
async function pickInstallDir(): Promise<void> {
  if (!isDesktop) return
  const dir = await pickDirectory('选择本地插件源码目录(含 plugin.yaml)')
  if (dir) installSpec.value = dir
}

// uiScanBusy UI 插件重扫进行中(按钮置灰;防连点)。
const uiScanBusy = ref(false)

// rescanUIPlugins 重扫 UI 插件并重装槽位。
//
// 为什么必须有它:插件清单是**页面启动时扫一次**的(`main.ts` 的 loadUIPlugins),
// 装完新插件只能靠刷新页面;而用户在设置面板里刚装完、看着「没变化」,
// 会以为装失败了 —— 实际只是没重扫。补这个入口把「刷新页面」变成一个按钮。
// 注意:重扫**只重装槽位覆盖**,不清空已注册的默认实现(registry 的 registerSlot
// 自带优先级比较,同优先级后注册者胜 ⇒ 重扫是幂等叠加,不是"再装一遍同样的东西")。
async function rescanUIPlugins(): Promise<void> {
  if (uiScanBusy.value) return
  uiScanBusy.value = true
  try {
    await loadUIPlugins()
  } finally {
    uiScanBusy.value = false
  }
}

// doCheckUpdate 桌面版检查更新:壳侧走同一条 checkForUpdates(与托盘菜单同一实现)。
// **只查不装**:有更新时壳返回 available,由用户点「立即升级」并确认后才下载安装。
async function doCheckUpdate() {
  if (!isDesktop) {
    updOk.value = false
    updMsg.value = '当前环境不支持检查更新(需桌面版)'
    return
  }
  updBusy.value = true
  updMsg.value = '正在检查…'
  updOk.value = false
  updAvailable.value = ''
  try {
    const r = await checkUpdate()
    updAvailable.value = r?.status === 'available' ? String(r?.version ?? '新版本') : ''
    updOk.value = r?.status === 'upToDate' || r?.status === 'installed'
    updMsg.value = String(r?.message ?? '检查完成')
  } catch (e) {
    updOk.value = false
    updAvailable.value = ''
    updMsg.value = '检查更新失败:' + (e instanceof Error ? e.message : String(e))
  } finally {
    updBusy.value = false
  }
}

// doInstallUpdate 确认升级:二次确认(有副作用的操作必须问)→ 下载安装 → 壳自动重启。
// 二次确认走既有的全局确认条 guard(与增删改前置同一处):升级会换掉整个应用本体,
// 属于必须由人点头的动作。取消只是不 run —— updAvailable 保留,按钮还在,可随时再升。
function doInstallUpdate(): void {
  const ver = updAvailable.value
  if (!ver) return
  guard(`升级到 ${ver}(会替换应用本体并重启)`, false, () => {
    void doInstallNow()
  })
}

// doInstallNow 确认之后真正下载安装的那一段(与确认弹层解耦)。
async function doInstallNow(): Promise<void> {
  updBusy.value = true
  updMsg.value = '正在下载安装…'
  try {
    const r = await installUpdate()
    updOk.value = r?.status === 'installed'
    updAvailable.value = r?.status === 'installed' ? '' : updAvailable.value
    updMsg.value = String(r?.message ?? '安装完成')
  } catch (e) {
    updOk.value = false
    updMsg.value = '安装失败:' + (e instanceof Error ? e.message : String(e))
  } finally {
    updBusy.value = false
  }
}
// 开机自启的实际状态(「关于 gah」展示)。托盘勾选态可能与系统实际状态不同步,故直接问系统。
// 2026-09-17 真机反馈:「勾选开机自启后,关于 gah 的内容未有变化」—— 因为那段里根本没有自启信息。
const autoState = ref<'' | 'on' | 'off'>('')
// lastUpdSeq 面板已经展示过的检查轮次:轮询只贴更新的轮次,不把面板自己刚给出的结论冲掉。
const lastUpdSeq = ref(0)
let stateTimer: ReturnType<typeof setInterval> | undefined

// applyUpdateSnapshot 把壳侧检查更新快照贴到面板上。进行中始终跟随(busy 由壳侧真源定);
// 结束时只认比本地见过的更新的那一轮 —— 否则轮询会把面板自己刚写的错误提示覆盖回去。
function applyUpdateSnapshot(u: UpdateSnapshot): void {
  if (u.busy) {
    updBusy.value = true
    updOk.value = false
    updMsg.value = '正在检查…'
    return
  }
  if (u.seq <= lastUpdSeq.value) return
  lastUpdSeq.value = u.seq
  updBusy.value = false
  updAvailable.value = u.status === 'available' ? String(u.version ?? '新版本') : ''
  updOk.value = u.status === 'upToDate' || u.status === 'installed'
  updMsg.value = u.message || '检查完成'
}

// refreshDesktopState 面板打开期间轮询壳侧状态(开机自启 + 检查更新)。
// 为何要轮询:托盘与设置面板是两个视图。真机反馈「设置界面开着时从托盘勾开机自启/点检查
// 更新,面板要重新打开才同步」—— 现在托盘动作改的是壳侧状态,面板 1.5 秒内跟上。
// 走已验证的同步命令通道,不用事件通道(插件事件通道在真机上静默失败过)。
async function refreshDesktopState(): Promise<void> {
  autoState.value = await autostartState()
  const u = await updateState()
  if (u) applyUpdateSnapshot(u)
}

// v2 扩展点:设置面板区段(每次渲染求值,保持实时)
const panelSections = settingSections()
const err = ref('')
const info = ref('') // 操作成功/结果提示(短时)
const busy = ref(false)

// —— 数据 ——
const models = ref<ProviderModelGroup[]>([])
const providers = ref<ProviderInfo[]>([])
const plugins = ref<PluginInfo[]>([])
const histN = ref(0) // 0 全部 / N 最近 / -1 禁止

// —— 操作表单(Provider 新增)与 W3 首启引导 ——
const showAdd = ref(false)
const pf = ref({ name: '', base_url: '', api_key: '', model: '' })
const presets = PROVIDER_PRESETS
// —— 分段导航(左栏;窄窗口退化为顶部芯片条) ——
// 面板此前是一条长滚动列:43 个插件条目把「关于 gah / 检查更新」顶到必须长滚的位置。
// 现在段与导航共用一份键(段上写 data-sec),跳转与高亮都从 DOM 反查 —— 少一份可能失配的映射表。
const bodyEl = ref<HTMLElement | null>(null)
const activeSec = ref('model')
// navItems 导航项(顺序 = 段在 DOM 里的顺序;条件渲染的段跟着一起隐藏)
const navItems = computed(() => {
  const items: { key: string; label: string }[] = [
    { key: 'model', label: '模型' },
    { key: 'reason', label: '推理' },
  ]
  if (instrReady.value) items.push({ key: 'instr', label: '指令' })
  if (memReady.value) items.push({ key: 'memory', label: '记忆' })
  if (roleReady.value) items.push({ key: 'role', label: '角色' })
  items.push(
    { key: 'history', label: '会话历史' },
    { key: 'provider', label: 'Provider' },
    { key: 'backup', label: '数据备份' },
  )
  if (schedReady.value) items.push({ key: 'schedule', label: '计划' })
  items.push({ key: 'mcp', label: 'MCP server' }, { key: 'plugin', label: '插件' })
  if (isDesktop) items.push({ key: 'about', label: '关于 gah' })
  for (const s of panelSections) items.push({ key: 'ext-' + s.key, label: s.title ?? s.key })
  return items
})
function secEls(): HTMLElement[] {
  return Array.from(bodyEl.value?.querySelectorAll<HTMLElement>('section[data-sec]') ?? [])
}
// jumpTo 跳到某段。打开面板时的定位要即时(否则与抽屉入场动画叠在一起),用户点导航要平滑。
//
// navTarget = 平滑滚动进行中的目标段:期间**只**高亮它。不锁的话滚动反查会把高亮中途
// 改到途经的段上(点「角色」会先亮一下「指令」),动画被拖慢/中断时停下来高亮还停在错的段
// —— CI 上实测踩到(macOS runner 400ms 内没滚完,断言拿到「指令」)。
let navTarget = ''
let navSettle = 0
function unlockNav(): void {
  navTarget = ''
  if (navSettle) {
    clearTimeout(navSettle)
    navSettle = 0
  }
}
function jumpTo(key: string, smooth = true): void {
  const el = bodyEl.value?.querySelector<HTMLElement>(`section[data-sec="${key}"]`)
  if (!el) return // 该段在当前装配下不渲染(如未装配 host-schedule):静默跳过
  activeSec.value = key
  if (!smooth) {
    unlockNav()
    el.scrollIntoView({ block: 'start', behavior: 'auto' })
    return
  }
  navTarget = key
  if (navSettle) clearTimeout(navSettle)
  // 兜底:目标段始终到不了容器上沿(内容不够高的末段)或滚动事件不来时,700ms 后交还反查 ——
  // 交还时补跑一次反查,否则锁在错的位置(高亮停在目标段、视图却在别处)。
  navSettle = window.setTimeout(() => {
    unlockNav()
    onBodyScroll()
  }, 700)
  el.scrollIntoView({ block: 'start', behavior: 'smooth' })
}
// onBodyScroll 滚动时反查「当前段」= 视口顶部之上最后一段。用 rAF 合并同一帧内的多次滚动事件,
// 每帧只读一次几何(读与写不交叉,不触发强制同步布局)。
let navRaf = 0
function onBodyScroll(): void {
  if (navRaf) return
  navRaf = requestAnimationFrame(() => {
    navRaf = 0
    const c = bodyEl.value
    if (!c) return
    const top = c.getBoundingClientRect().top
    const secs = secEls()
    if (navTarget) {
      // 平滑滚动进行中:高亮只认目标段(途经段不算);到位(±2px)即解锁,交还下面的反查。
      const t = secs.find((el) => el.dataset.sec === navTarget)
      if (t && Math.abs(t.getBoundingClientRect().top - top) > 2) {
        activeSec.value = navTarget
        return
      }
      unlockNav()
    }
    // 已滚到底:末段顶不到容器上沿(内容不够高),但用户点它时它就是「当前段」——
    // 不特判的话高亮会因上面的阈值判据退回倒数第二段。
    if (c.scrollTop + c.clientHeight >= c.scrollHeight - 2) {
      const last = secs[secs.length - 1]?.dataset.sec
      if (last) activeSec.value = last
      return
    }
    let cur = ''
    for (const el of secs) {
      if (el.getBoundingClientRect().top - top <= 32) cur = el.dataset.sec ?? ''
      else break // 段按 DOM 顺序排列,首个在阈值之下即收工
    }
    if (cur) activeSec.value = cur
  })
}
const keyInput = ref<HTMLInputElement | null>(null)
const applied = ref<ProviderPreset | null>(null) // 最近选中的预设(用于「本地免 Key」提示)
const lastSaved = ref('') // 刚保存的 provider(失败后「重新自检」用)
const probe = ref<{ ok: boolean; text: string; raw?: string } | null>(null)
// onboardHint 空状态引导的一句话:选中预设后给该预设的说明
const onboardHint = computed(() =>
  applied.value ? applied.value.note : '不知道选哪个就用 DeepSeek;不想花钱可选 Ollama 在本机跑模型。',
)

const THINK = ['off', 'low', 'medium', 'high'] as const
const THINK_LABEL: Record<string, string> = { off: '关闭', low: '低', medium: '中', high: '高' }
const SB = [
  { v: 'read-only', label: '只读' },
  { v: 'workspace-write', label: '工作区' },
  { v: 'full-access', label: '完全' },
] as const
const AP = [
  { v: 'open', label: '开放' },
  { v: 'smart', label: '智能' },
  { v: 'strict', label: '严格' },
] as const
const SB_ZH: Record<string, string> = { 'read-only': '只读', 'workspace-write': '工作区', 'full-access': '完全' }
const AP_ZH: Record<string, string> = { open: '开放', smart: '智能', strict: '严格' }
// 角色可声明的收紧档(第九十二批):只列**比缺省更严**的那几个。
// 不摆 open/full-access:那是放宽,后端直接 400 —— 界面摆一个点了必失败的按钮就是骗人。
const ROLE_AP = [
  { v: 'smart', label: '智能（危险命令逐条确认）' },
  { v: 'strict', label: '严格（危险命令直接拒）' },
] as const
const ROLE_SB = [
  { v: 'read-only', label: '只读（只能读，不能改）' },
  { v: 'workspace-write', label: '工作区可写' },
] as const

async function load(): Promise<void> {
  err.value = ''
  try {
    // 模型聚合/插件/provider 任一失败降级:非核心(如未装配 MultiProviderService → 501)
    const [m, pl, pr] = await Promise.allSettled([api.models(), api.plugins(), api.providers()])
    await loadBackups() // M18 备份列表(未装配降级静默)
    await loadMemory() // 记忆段(未装配 ctx.memory → memReady=false,整段隐藏)
    await loadRoles() // 角色段(未装配 ctx.roles → roleReady=false,整段隐藏)
    if (m.status === 'fulfilled') models.value = m.value.providers ?? []
    if (pl.status === 'fulfilled') plugins.value = pl.value ?? []
    if (pr.status === 'fulfilled') providers.value = pr.value ?? []
    if (!probe.value) refreshProbeFromActive() // 活跃 provider 拉不到模型时直接给出原因
  } catch (e) {
    err.value = (e as Error).message
  }
}
function activeProvider(): ProviderInfo | undefined {
  return providers.value.find((p) => p.Active)
}
function guard(title: string, danger: boolean, run: () => void): void {
  if (!ask) {
    run()
    return
  }
  ask({ title, danger, run })
}

// —— 草稿保护 ——
// 面板里的四处可编辑内容都是「写进磁盘就长期生效」的东西(角色工作规则 / 技能 SKILL.md /
// 全局指令 / MCP server 配置),而**切换目标**(换角色、换技能、重拉配置)会重新拉服务端
// 内容覆盖草稿 —— 用户的修改就这么没了,连一声响都没有。所以:① 每处都记一份「服务端上一版」
// 用于算脏;② 脏的时候切换目标前问一声;③ 面板里常驻「未保存」标记(知道自己脏,才有得选)。
// 反面:给一切都加确认弹层 = 噪音。所以只拦**真会丢内容**的入口,关面板/切分区不拦
// (组件实例常驻、草稿留在内存里,关掉再打开还在)。
type DraftKind = 'role' | 'skill' | 'mcp'
// draftLabels 未保存草稿的标签 —— **只列这次真会被丢弃的**:
//   role = 角色工作规则(切角色/换技能要重拉详情)、skill = 技能 SKILL.md、mcp = MCP server 配置。
//   全局指令**不在此列**:没有任何一条「换目标」路径会丢它(只由它自己的保存/放弃清掉),
//   列上去等于说「继续将丢弃全局指令」而实际不会 —— 白吓一跳,还让人以为已经放弃了。
function draftLabels(kinds: DraftKind[]): string[] {
  const out: string[] = []
  if (kinds.includes('role') && agentsDirty.value) out.push('角色「' + selRole.value + '」的工作规则')
  if (kinds.includes('skill') && skDirty.value) out.push('技能「' + (skEdit.value?.name ?? '') + '」的 SKILL.md')
  if (kinds.includes('mcp') && mcpDirty.value) out.push('MCP server 配置')
  return out
}
// withDrafts 有未保存草稿时先确认(丢弃是用户的决定,不是面板替他做的)。
function withDrafts(run: () => void, kinds: DraftKind[] = ['role', 'skill']): void {
  const d = draftLabels(kinds)
  if (!d.length) {
    run()
    return
  }
  guard('有未保存的修改:' + d.join('、') + '。继续将丢弃这些修改?', true, run)
}

// —— 模型/思考/沙箱 ——
const modelOptions = ref<{ label: string; value: string }[]>([])
function buildModelOptions(): void {
  const opts: { label: string; value: string }[] = []
  for (const g of models.value) {
    for (const md of g.Models) opts.push({ label: g.Name + ' · ' + md.ID, value: modelOptionValue(g.Name, md.ID) })
  }
  // 当前模型可能不在枚举里(手填/provider 未列全):补一条「(当前)」,保证高亮总有落点。
  // 真源只有一个 —— state.model(运行时真正在用的),不是 provider 配置里的 Model 默认值:
  // 旧实现两者混用,不一致时高亮永远匹配不上(见 modelsel.ts 顶部注释)。
  modelOptions.value = withCurrentModel(opts, props.state.model, activeProvider()?.Name ?? '')
}
const modelVal = ref('')
// 模型选择:输入筛选(modelFilter 实时过滤选项,provider·模型名均可匹配;命中即点选)
const modelFilter = ref('')
const filteredModels = computed(() => {
  const f = modelFilter.value.trim().toLowerCase()
  if (!f) return modelOptions.value
  return modelOptions.value.filter((o) => o.label.toLowerCase().includes(f) || o.value.toLowerCase().includes(f))
})
// curModelLabel 当前生效模型的可读标签(输入框占位 + 「当前生效」行)。
// 真源是 state.model(运行时真正在用的),不是筛选框内容 —— 真机上「当前模型」标签后面
// 就是个空筛选框,用户会读成「已选了模型但当前模型没显示」,故把当前值显式摆出来。
const curModelLabel = computed(() => {
  const hit = modelOptions.value.find((o) => o.value === modelVal.value)
  const m = (props.state.model ?? '').trim()
  // 角色指定模型时只报模型名:provider 前缀是**会话通用适配器**的域短名,
  // 角色模型可能走另一个前缀路由(claude-* 等),拼上去就是编(与 TUI 同一条纪律)。
  if (props.state.model_from === 'role') return m || hit?.value || ''
  if (hit) return hit.label
  if (!m) return ''
  const p = activeProvider()
  return p?.Name ? p.Name + ' · ' + m : m
})
// 列表点选:记录选中并应用(与原生 select @change 同语义;选中后清空筛选恢复可视)
function pickModel(o: { label: string; value: string }): void {
  modelVal.value = o.value
  modelFilter.value = ''
  void applyModel()
}
async function applyModel(): Promise<void> {
  const v = modelVal.value
  if (!v) return
  const [pname, mid] = v.split('|')
  busy.value = true
  try {
    if (pname && !activeProvider()?.Name?.includes(pname) && providers.value.some((p) => p.Name === pname)) {
      await api.providerUse(pname) // 先切所属 provider(活跃态与端点)
    }
    await api.control({ model: mid })
    emit('changed')
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
// onSyncToggle 联动开关勾选(R10 ②-2):模板里不写类型断言,取 checked 的脏活留在脚本里。
function onSyncToggle(e: Event): void {
  applyCtl({ sandbox_sync: (e.target as HTMLInputElement).checked })
}
async function applyCtl(body: { thinking?: string; sandbox?: string; approval?: string; sandbox_sync?: boolean }): Promise<void> {
  try {
    await api.control(body)
    emit('changed')
  } catch (e) {
    err.value = (e as Error).message
  }
}

// —— 全局指令(第八十一批 · 形态 A) ——
// $GAH_HOME/AGENTS.md 是"对所有角色生效"的基线层(角色未声明 exclude_global 时都注入),
// 此前只能在文件系统上手改 —— 面板有角色级规则编辑器,却没有这一份全局的。
// 纯文件读写,与 ctx.roles 是否装配无关(未装配角色服务时这一段照样能用)。
const instrReady = ref(false)
const instrPath = ref('')
const instrMax = ref(32768)
const instrExists = ref(false)
const instrOver = ref(false) // 文件本身超限(手改的大文件):能看能改,但保存会被拒
const instrDraft = ref('')
const instrSaved = ref('') // 服务端上一版正文(草稿与它比才有"改没改过")
const instrEdit = ref(false)
const instrErr = ref('')
const instrMsg = ref('')
const instrWarn = ref('') // 文件写了但本轮提示没跟着变(重载失败):成功样式会谎报"已生效'
const instrBytes = computed(() => byteLength(instrDraft.value))
// instrDirty 有未保存改动。**收起编辑区不清草稿**(留在内存里,再展开还在) —— 清掉草稿的
// 出口只有两个:「保存」与显式「放弃修改」,不留静默丢失路径。
const instrDirty = computed(() => instrDraft.value !== instrSaved.value)

// discardInstr 显式放弃草稿(回到磁盘上的内容;编辑区**留着** —— 用户可能想从磁盘版本接着改)。
function discardInstr(): void {
  guard('放弃尚未保存的全局指令修改?', true, () => {
    instrErr.value = ''
    instrDraft.value = instrSaved.value
    void loadInstructions()
  })
}

// loadInstructions 拉全局指令(读失败 → 整段隐藏,不摆空壳;不覆盖正在编辑的草稿)
async function loadInstructions(): Promise<void> {
  try {
    const v = await api.instructions()
    if (!v || typeof v.text !== 'string') {
      instrReady.value = false
      return
    }
    instrReady.value = true
    instrPath.value = v.path
    instrMax.value = v.max_bytes || 32768
    instrExists.value = !!v.exists
    instrOver.value = !!v.over
    // 先按**上一版**基准算脏(顺序要紧:先写 instrSaved 会把"初始空草稿"自己算成脏,于是永远不回填)
    const dirty = instrDirty.value
    instrSaved.value = v.text
    if (!dirty) instrDraft.value = v.text
  } catch {
    instrReady.value = false
  }
}

// saveInstructions 保存全局指令:逐字进系统提示且影响所有角色 → 二次确认 + 上限前置校验。
// 服务端回 warning = 文件写了但重载没成(本轮提示仍是旧内容),照实说,不谎报"已生效"。
function saveInstructions(): void {
  const text = instrDraft.value
  if (byteLength(text) > instrMax.value) {
    instrErr.value = `全局指令超上限:${byteLength(text)} > ${instrMax.value} 字节(会逐字进系统提示,请精简)`
    return
  }
  guard('保存全局指令?(写入 ' + instrPath.value + ',对所有角色生效)', false, () => {
    instrErr.value = ''
    instrMsg.value = ''
    instrWarn.value = ''
    void (async () => {
      try {
        const r = await api.instructionsSave(text)
        if (r.warning) {
          instrMsg.value = ''
          instrWarn.value = '文件已写入,但未生效:' + r.warning + '(面板显示的是磁盘上的内容)'
        } else {
          instrWarn.value = ''
          instrMsg.value = '全局指令已保存并生效'
        }
        instrEdit.value = false
        await loadInstructions()
      } catch (e) {
        instrErr.value = (e as Error).message
      }
    })()
  })
}

// —— 跨会话记忆(记忆治理面板) ——
// 与 `/memory` 命令**同一份实现**(两边都只调 ctx.memory):此前只能在命令里管,
// 面板没有对应段落 —— 于是“治理”这个动作对只在 Web 端的人根本不存在。
// 未装配 ctx.memory(host-memory 未启用)→ 503 → 整段隐藏(不摆空壳)。
const memReady = ref(false)
const memEnabled = ref(true)
const memBudget = ref(2048)
const memUser = ref<string[]>([])
const memProject = ref<string[]>([])
const memPath = ref('')
const memErr = ref('')
const memMsg = ref('')
const memDraft = ref('')
const memBusy = ref(false)

// memSource 拆出行尾的「(来源: 会话 x)」—— 治理的关键动作是“按来源整段删”,
// 面板得能一键删掉“某个会话写进来的全部记忆”,而不只是逐条删。
function memSource(line: string): string {
  const m = /\(来源:\s*会话\s+(\S+?)\s*\)\s*$/.exec(line)
  return m ? m[1] : ''
}
function memContent(line: string): string {
  return line.replace(/\(来源:\s*会话\s+\S+?\s*\)\s*$/, '').replace(/^\d+\.\s*/, '')
}
// memHasCandidates 该构建是否提供候选能力(字段组缺席 = 老版本;不渲染空壳)
const memHasCandidates = computed(() => Array.isArray(memCand.value))
const memCand = ref<string[]>([])

// memoryRun 之后要把候选组也同步过来(它与记忆列表在同一个响应里)
function memSync(v: { candidates?: string[]; candidate_used?: number; candidate_limit?: number; candidate_today?: number; candidate_today_max?: number }): void {
  if (Array.isArray(v.candidates)) {
    memCand.value = v.candidates
    memUsed.value = v.candidate_used ?? v.candidates.length
    memLimit.value = v.candidate_limit ?? 0
    memToday.value = v.candidate_today ?? 0
    memTodayMax.value = v.candidate_today_max ?? 0
  }
}
const memUsed = ref(0)
const memLimit = ref(0)
const memToday = ref(0)
const memTodayMax = ref(0)

// memoryPropose 提一条候选。候选**不进上下文**,要确认(accept)之后才生效 ——
// 界面上把这句话放在按钮旁边,不然用户会以为「提了就已经记住了」。
function memoryPropose(): void {
  const content = memDraft.value.trim()
  if (!content) {
    memErr.value = '候选内容为空'
    return
  }
  void memoryRun({ action: 'propose', content }, () => {
    memDraft.value = ''
    return '已放进候选池(**还没进上下文**):确认之后才生效'
  })
}

function memoryAccept(index: number): void {
  void memoryRun({ action: 'accept', index }, () => '已转正为记忆(之后每轮带进上下文)')
}

function memoryReject(index: number): void {
  void memoryRun({ action: 'reject', index }, () => '已丢弃候选(没有进记忆)')
}

function memoryAcceptAll(): void {
  guard('把候选池里**全部**候选转正为记忆?(之后每轮都会带进上下文)', true, () => {
    void memoryRun({ action: 'accept_all' }, (n) => (n ? `已转正 ${n} 条为记忆` : '候选池是空的,没东西可转正'))
  })
}

function memoryRejectAll(): void {
  guard('丢弃候选池里**全部**候选?(不进记忆)', true, () => {
    void memoryRun({ action: 'reject_all' }, (n) => `已丢弃 ${n} 条候选`)
  })
}

const memSources = computed(() => {
  const seen = new Set<string>()
  for (const l of memUser.value) {
    const s = memSource(l)
    if (s) seen.add(s)
  }
  return Array.from(seen)
})

// loadMemory 拉记忆视图(读失败 → 整段隐藏)。**不覆盖正在输入的草稿**。
async function loadMemory(): Promise<void> {
  try {
    const v = await api.memory()
    if (!v || !Array.isArray(v.user)) {
      memReady.value = false
      return
    }
    memReady.value = true
    memEnabled.value = !!v.enabled
    memBudget.value = v.budget || 2048
    memUser.value = v.user
    memProject.value = Array.isArray(v.project) ? v.project : []
    memPath.value = v.user_path
    memSync(v)
  } catch {
    memReady.value = false
  }
}

// memoryRun 发一个动作并把回执(刷新后的视图)并进状态;写动作统一走这里。
async function memoryRun(body: Parameters<typeof api.memoryAct>[0], ok: (deleted?: number) => string): Promise<void> {
  memBusy.value = true
  memErr.value = ''
  memMsg.value = ''
  try {
    const v = await api.memoryAct(body)
    memEnabled.value = !!v.enabled
    memUser.value = Array.isArray(v.user) ? v.user : []
    memProject.value = Array.isArray(v.project) ? v.project : []
    memSync(v)
    memMsg.value = ok(v.deleted)
  } catch (e) {
    memErr.value = (e as Error).message
  } finally {
    memBusy.value = false
  }
}

function memoryAdd(): void {
  const content = memDraft.value.trim()
  if (!content) {
    memErr.value = '记忆内容为空(写一句“是什么、为什么”即可)'
    return
  }
  void memoryRun({ action: 'add', content }, () => {
    memDraft.value = ''
    return '已记住'
  })
}

function memoryRemove(index: number, line: string): void {
  guard('删除这条记忆?' + memContent(line) + '(删了就没了,不留回收站)', true, () => {
    void memoryRun({ action: 'remove', index }, () => '已删除')
  })
}

function memoryRemoveSource(src: string): void {
  guard('删掉会话 ' + src + ' 写进来的**全部**记忆?', true, () => {
    void memoryRun({ action: 'remove_source', source: src }, (n) =>
      n ? '已删掉 ' + n + ' 条来自该会话的记忆' : '没有来自该会话的记忆',
    )
  })
}

// memoryToggle 一键关掉注入:关的是“注入”,不是“数据”(文件仍在,可随时再开)。
function memoryToggle(on: boolean): void {
  void memoryRun({ action: 'toggle', enabled: on }, () =>
    on ? '已开启注入(每轮按预算注入记忆)' : '已关闭注入(记忆文件保留,不再进系统提示)',
  )
}

// —— 角色(第七十九批 1b) ——
// 角色 = 人设(身份句)+ 工作规则(AGENTS.md)+ 技能挂载;后端未装配 ctx.roles 时整段隐藏。
const roles = ref<RoleSpec[]>([])
const roleLib = ref<SkillInfo[]>([])
const roleCurrent = ref('')
const roleMax = ref(32768)
const roleProblems = ref<{ id: string; error: string }[]>([])
const roleReady = ref(false) // 首次 /api/roles 拿到对象 = 该环境支持角色(503/形状不对 → 不渲染空壳)
const roleErr = ref('')
const roleMsg = ref('')
const selRole = ref('') // 展开编辑的角色 id
const roleDetail = ref<RoleSpec | null>(null)
const agentsDraft = ref('')
const agentsSaved = ref('') // 服务端上一版工作规则
const roleIDDraft = ref('')
const showRoleNew = ref(false)
const rf = ref({ id: '', name: '', identity: '', description: '', exclude_global: false })
const skNew = ref({ name: '', description: '', triggers: '', body: '', role: '' })
const showSkillNew = ref(false)
const skErr = ref('')
const skEdit = ref<{ name: string; role: string; content: string } | null>(null)
const skSaved = ref('') // 服务端上一版技能正文
// 改名/移动(第八十四批):技能身份 = 目录名 + 归属库。一次表单两件都能改
// (只改名/只换库/两件一起),提交走一条 relocate 端点。
const skMove = ref<{ name: string; role: string; toName: string; toRole: string } | null>(null)
// agentsDirty / skDirty 未保存改动(凡切目标前都要问一声:刚写的东西不能静默丢)
const agentsDirty = computed(() => !!selRole.value && agentsDraft.value !== agentsSaved.value)
const skDirty = computed(() => !!skEdit.value && (skEdit.value?.content ?? '') !== skSaved.value)
// skMoveDirty 改名/移动表单是否真有变化(无变化则提交禁用:后端也会拒)
const skMoveDirty = computed(() => {
  const m = skMove.value
  if (!m) return false
  return m.toName.trim() !== m.name || m.toRole !== m.role
})

// —— 回收站(第八十三批) ——
// 删除角色/技能都是"移进 .trash"(可恢复),但之前面板里没有列表也没恢复动作 ——
// 文案写着"可恢复"却无入口。这里补上:展开时才拉,不在每次轮询里拖大响应。
const trashOpen = ref(false)
const trashReady = ref(false) // 首次拉成功(形状对)→ 才渲染区块
const trashRoles = ref<TrashRoleEntry[]>([])
const trashSkills = ref<TrashSkillEntry[]>([])
const trashErr = ref('')
const trashCount = computed(() => trashRoles.value.length + trashSkills.value.length)

// loadRoles 拉角色列表 + 技能库(503 = 未装配 → 整段隐藏;其余错误照常提示)。
async function loadRoles(): Promise<void> {
  try {
    const v = await api.roles()
    if (!v || !Array.isArray(v.roles)) {
      roleReady.value = false // 形状不对(旧后端/未装配):不渲染
      return
    }
    roleReady.value = true
    roles.value = v.roles
    roleLib.value = v.library ?? []
    roleCurrent.value = v.current ?? ''
    roleMax.value = v.max_agents_bytes || 32768
    roleProblems.value = v.problems ?? []
  } catch {
    roleReady.value = false // 503/网络错:静默隐藏(不把「这个环境没角色功能」当故障报)
  }
}
// roleGroups 角色按 group 分节(12 个预置角色之后,平铺列表已经认不出"该挑哪个")。
// 顺序:分组按其角色在列表里的**首次出现**排序(不给分组表设死顺序 —— 新增预置角色
// 落在哪个组就出现在那个组,不用改前端)。无 group 的角色收在「其它」一节。
const roleGroups = computed(() => {
  const order: string[] = []
  const by = new Map<string, RoleSpec[]>()
  for (const r of roles.value) {
    const g = (r.group || '').trim() || '其它'
    if (!by.has(g)) {
      by.set(g, [])
      order.push(g)
    }
    by.get(g)!.push(r)
  }
  return order.map((g) => ({ group: g, items: by.get(g)! }))
})

// roleIsCurrent 当前角色高亮(切换是改状态但可一键切回,故不进确认弹层)
function roleIsCurrent(r: RoleSpec): boolean {
  return roleCurrent.value === r.id
}

// roleHint 未选过角色时的引导条(第一百零二批)。
//
// 判据只用**已有状态**:当前角色为空 = 从没挑过角色。选中之后这条自然永久消失,
// 因此不需要额外的「不再提示」偏好字段 —— 省一个持久化状态,也就省一个将来要迁移的东西。
// 刻意做成面板里的一行提示而不是启动浮层:浮层会打断第一次对话,而角色这件事
// **不挑也能用**(走基线:全局指令 + 全部技能),不该挡路。
const roleHint = computed(() => roleBaseline.value && roles.value.length > 1)

// roleBaseline 当前处于"基线态"(没有启用任何角色)= 列表顶部那条合成行是否标「当前」。
// 为什么在列表里给它一个位置:基线不是"什么都没配",而是**确定的运行形态**(全局指令 +
// 项目规则 + 默认技能池),它此前在面板里没有名字 —— 用户看不出"不启用角色"到底意味着什么,
// 也找不到一键切回去的落点(只有底部一个「停用当前角色」,而且只在有角色时才出现)。
// 它**只在展示层合成**:磁盘上没有任何对应实体(不落 role.yaml、不进 /api/roles 的 Roles),
// 切换就是 POST /use 空 id(服务端认 "-" 占位符 → 清偏好)。
const roleBaseline = computed(() => roleCurrent.value === '')

// roleDangling 偏好里的当前角色在列表里找不到(角色目录被外部删了 —— Store.Delete 拒绝删当前角色,
// 所以只可能是外部删文件)。这里**不静默改偏好**(偏好是用户状态,替他清掉下次他自己都查不出角色
// 为什么没了),只把事实说清楚 + 给出合成行的「切换」作为一键修复落点;运行侧由 host-roles 的
// BlockText 明示"本轮按基线运行"。
const roleDangling = computed(() => !!roleCurrent.value && !roles.value.some((r) => r.id === roleCurrent.value))

// selectRole 展开某角色的编辑区(列表不带正文,展开时才拉详情)/ 再点一次收起。
// confirmed=true 表示"草稿丢弃已经过用户确认"(防止重入时再问一遍)。
async function selectRole(r: RoleSpec, confirmed = false): Promise<void> {
  if (!confirmed) {
    const d = draftLabels(['role', 'skill'])
    if (d.length) {
      withDrafts(() => void selectRole(r, true), ['role', 'skill'])
      return
    }
  }
  if (selRole.value === r.id) {
    selRole.value = ''
    roleDetail.value = null
    return
  }
  roleErr.value = ''
  roleMsg.value = ''
  try {
    const d = await api.roleGet(r.id)
    roleDetail.value = d
    agentsDraft.value = d.agents ?? ''
    agentsSaved.value = d.agents ?? ''
    roleIDDraft.value = d.id
    skEdit.value = null
    skSaved.value = ''
    selRole.value = r.id
  } catch (e) {
    roleErr.value = (e as Error).message
  }
}

// discardAgents 显式放弃工作规则草稿(回到服务端已保存的那一版)。
function discardAgents(): void {
  guard('放弃尚未保存的工作规则修改?', true, () => {
    agentsDraft.value = agentsSaved.value
    roleErr.value = ''
  })
}

// —— 角色定义的**即时提交**(挂载勾选/并入/排除/模型/思考档:点一下发一次) ——
// 串行化 + 在途禁用。为何不能各发各的:挂载清单是**整份替换**(skills_set + skills),
// 连点两次会让后一个请求带着旧清单覆盖前一个(服务端的原子写只保证“单次写不丢”,救不了
// 客户端基于旧状态构造的补丁)—— 表现为“勾了两个技能只生效一个”。所以补丁改成**入队时现算**
// (builder,排队期间前面几次的写已经回填到 roleDetail),并按提交顺序逐个发;
// 在途期间相关控件 :disabled(点了不生效也要看得出来)。
const roleSaving = ref(false)
let rolePatchQueue: Promise<void> = Promise.resolve()
let roleSavingN = 0
type RolePatch = Parameters<typeof api.roleUpdate>[1]
function queueRolePatch(build: () => RolePatch): Promise<void> {
  const id = selRole.value
  if (!id) return Promise.resolve()
  roleSavingN++
  roleSaving.value = true
  const step = async (): Promise<void> => {
    // finally 必须盖住 build() 与「补丁为空」两条早退路径:计数不配对会让 roleSaving
    // 永远为真,整片控件从此点不动。
    try {
      const patch = build() // 到执行时才读当前状态
      if (!patch || Object.keys(patch).length === 0) return
      const d = await api.roleUpdate(id, patch)
      roleDetail.value = { ...(roleDetail.value as RoleSpec), ...d, agents: agentsDraft.value }
      await loadRoles()
      roleMsg.value = '已保存'
    } catch (e) {
      roleErr.value = (e as Error).message
    } finally {
      roleSavingN--
      roleSaving.value = roleSavingN > 0
    }
  }
  rolePatchQueue = rolePatchQueue.then(step, step)
  return rolePatchQueue
}
// —— 角色权限收紧(第九十二批) ——
// 角色只能把审批/沙箱**往里收**:实际档 = 全局档与角色档里更严的那个(后端合成,
// 前端不自己算 —— 算出来跟真正裁决的档位漂开就是假事实)。这里只做三件事:
// ① 下拉值域只给"更严"的档;② 把"当前实际生效"回显出来;③ 说清停用角色后回到全局档。
const roleTierNote = computed(() => {
  const st = props.state
  const parts: string[] = []
  if (st.approval_from === 'role' && st.approval_effective) {
    parts.push(`审批 全局${AP_ZH[st.approval ?? ''] ?? st.approval} → 实际${AP_ZH[st.approval_effective] ?? st.approval_effective}`)
  }
  if (st.sandbox_from === 'role' && st.sandbox_effective) {
    parts.push(`沙箱 全局${SB_ZH[st.sandbox] ?? st.sandbox} → 实际${SB_ZH[st.sandbox_effective] ?? st.sandbox_effective}`)
  }
  return parts.join(';')
})
// tierBrief 角色列表行里的收紧摘要(只列声明了什么,有效档由上面的 roleTierNote 负责)。
function tierBrief(r: RoleSpec): string {
  const a = r.approval ? '审批' + (AP_ZH[r.approval] ?? r.approval) : ''
  const b = r.sandbox ? '沙箱' + (SB_ZH[r.sandbox] ?? r.sandbox) : ''
  return a + (a && b ? ' · ' : '') + b || '跟随全局'
}

// saveRoleDef 提交角色定义的部分更新(只传改动的字段;更新后同步列表与详情)。
// 接受对象或 builder:依赖当前状态的字段(挂载清单)必须传 builder —— 否则排队期间算出的
// 旧快照会把前一次提交盖回去。
function saveRoleDef(patch: RolePatch | (() => RolePatch)): Promise<void> {
  return queueRolePatch(typeof patch === 'function' ? patch : () => patch)
}

// saveAgents 保存工作规则(会逐字进系统提示 → 二次确认 + 上限提示)
function saveAgents(): void {
  const id = selRole.value
  const text = agentsDraft.value
  if (!id) return
  if (byteLength(text) > roleMax.value) {
    roleErr.value = `工作规则超上限:${byteLength(text)} > ${roleMax.value} 字节(会逐字进系统提示,请精简)`
    return
  }
  guard('保存「' + id + '」的工作规则?(写入 roles/' + id + '/AGENTS.md,下一轮系统提示生效)', false, async () => {
    roleErr.value = ''
    try {
      await api.roleSetAgents(id, text)
      agentsSaved.value = text
      roleMsg.value = '工作规则已保存'
      await loadRoles()
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })
}

// useRole 切换(空 id = 停用回基线)。不换会话 —— 回合历史与工作区都不动。
function useRole(id: string): void {
  roleErr.value = ''
  void (async () => {
    try {
      await api.roleUse(id)
      roleCurrent.value = id
      roleMsg.value = id ? '已切换到「' + id + '」(下一轮生效,未改会话历史)' : '已停用角色(回到基线)'
      emit('changed') // App 重新拉 /api/state → 状态栏徽标同步
      await loadRoles()
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })()
}

// createRole 新建(可重复:ID 重复由后端显式报错)
function createRole(): void {
  roleErr.value = ''
  void (async () => {
    try {
      await api.roleCreate({ ...rf.value })
      roleMsg.value = '已新建角色「' + rf.value.id + '」(已给一份可改的规则模板)'
      const id = rf.value.id
      showRoleNew.value = false
      rf.value = { id: '', name: '', identity: '', description: '', exclude_global: false }
      await loadRoles()
      if (id) await selectRole({ id } as RoleSpec)
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })()
}

// renameRole 改 ID(目录改名;当前角色会跟随)
function renameRole(): void {
  const id = selRole.value
  const next = roleIDDraft.value.trim()
  if (!id || next === id) return
  guard('把角色 ' + id + ' 的标识改为 ' + next + '?(目录会改名,当前角色设置跟随)', false, async () => {
    roleErr.value = ''
    try {
      const d = await api.roleRename(id, { id: next })
      roleMsg.value = '已改标识:' + d.id
      // 改名不是换角色:工作规则草稿必须留在手里 —— 旧实现接着 selectRole(d) 会撞
      // 「同 id = 收起」分支把编辑区收掉,再展开时又用服务端版本盖掉草稿。
      selRole.value = d.id
      if (roleDetail.value) roleDetail.value = { ...roleDetail.value, id: d.id }
      roleIDDraft.value = d.id
      await loadRoles()
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })
}

// deleteRole 删除(移入回收站;当前角色后端会拒)
function deleteRole(r: RoleSpec): void {
  guard('删除角色「' + r.name + '」?(移入 roles/.trash/,可在下方回收站恢复)', true, async () => {
    roleErr.value = ''
    try {
      await api.roleDelete(r.id)
      if (selRole.value === r.id) {
        selRole.value = ''
        roleDetail.value = null
      }
      roleMsg.value = '已删除「' + r.id + '」(在回收站里,可恢复)'
      await loadRoles()
      if (trashOpen.value) await loadTrash()
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })
}

// —— 角色包(第九十三批):导出/导入 ——
// 导出:浏览器直接下载;桌面壳**没有下载通道**(Tauri 未注册 wry 的 on_download ⇒ `<a download>`
// 点了什么都不会发生,见 Sidebar.vue 同款说明),改由服务端写文件 —— 先用原生选择器挑目录,
// 再跑 /role export(与 TUI 同一个实现,界面只负责给路径)。
// —— 技能包(第一百零六批):共享技能的导出/导入。角色私有技能随**角色包**走(那边已带),
// 所以这里只管共享库那一份 —— 两条路合起来分享没有缺口。
const skPackAs = ref('') // 「导入为」目标技能名(留空 = 用包里的原名)
const skPackBusy = ref(false)
const showSkPackImport = ref(false)

// exportSkillPack 导出共享技能(浏览器直吃 /api/skillpack/<名>,含 Content-Disposition)。
// 导出的包**只有那个技能的 SKILL.md**;角色私有技能不在其中(用角色包导)。
function exportSkillPack(name: string): void {
  skErr.value = ''
  const a = document.createElement('a')
  a.href = skillPackDownloadUrl(name)
  a.download = skillPackName(name)
  document.body.appendChild(a)
  a.click()
  a.remove()
  roleMsg.value = '已开始下载 ' + skillPackName(name) + '(包内只有该技能的 SKILL.md;导入端不执行任何脚本)'
}

// onSkPackPick 导入技能包。同名已存在时后端**显式拒绝**(400),再问一次是否覆盖 ——
// 不静默覆盖别人的技能(与本地写入、角色包同款口径)。
async function onSkPackPick(e: Event): Promise<void> {
  const input = e.target as HTMLInputElement
  const file = input.files && input.files[0]
  if (!file) return
  skPackBusy.value = true
  skErr.value = ''
  roleMsg.value = ''
  const opts = skPackAs.value ? { as: skPackAs.value } : {}
  try {
    const res = await api.skillPackImport(file, opts)
    await loadRoles()
    roleMsg.value =
      '已导入技能 ' + res.name + '(' + res.bytes + ' 字节)' + (res.replaced ? '(覆盖了同名旧技能)' : '') +
      (res.warning ? '；注意:' + res.warning : '')
  } catch (err) {
    const msg = (err as Error).message
    if (/已存在/.test(msg) && confirm('同名技能已存在。要覆盖它吗?(旧份进回收站,可恢复)')) {
      try {
        const res2 = await api.skillPackImport(file, { ...opts, overwrite: true })
        await loadRoles()
        roleMsg.value = '已覆盖导入技能 ' + res2.name + '(旧份在回收站可恢复)'
      } catch (e2) {
        skErr.value = '覆盖导入失败:' + (e2 as Error).message
      }
    } else {
      skErr.value = '导入失败:' + msg
    }
  } finally {
    skPackBusy.value = false
    input.value = ''
  }
}

const packAs = ref('') // 「导入为」目标 ID(留空 = 用包里的原始标识)
const packBusy = ref(false)
const showPackImport = ref(false)

function exportRole(r: RoleSpec): void {
  roleErr.value = ''
  roleMsg.value = ''
  if (!isDesktop) {
    const a = document.createElement('a')
    a.href = rolePackDownloadUrl(r.id)
    a.download = rolePackName(r.id)
    document.body.appendChild(a)
    a.click()
    a.remove()
    roleMsg.value = '已开始下载 ' + rolePackName(r.id) + '(文件里含定义 + 工作规则 + 私有技能)'
    return
  }
  void (async () => {
    const dir = await pickDirectory('选择角色包保存目录')
    if (!dir) {
      roleMsg.value = '已取消导出'
      return
    }
    packBusy.value = true
    try {
      const path = dir.replace(/[\\/]+$/, '') + '/' + rolePackName(r.id)
      const res = await api.commandRun('role', ['export', r.id, path])
      if (res.error) roleErr.value = res.error
      else roleMsg.value = res.output || '已导出到 ' + path
    } catch (e) {
      roleErr.value = (e as Error).message
    } finally {
      packBusy.value = false
    }
  })()
}

// afterPack 导入成功后的统一收尾:说清落在哪、带了什么、旧份去哪了,再刷新列表。
async function afterPack(res: RolePackResult): Promise<void> {
  const bits = ['已导入角色「' + (res.name || res.id) + '」(' + res.id + ')']
  if (res.manifest?.id && res.manifest.id !== res.id) bits.push('包里原本是 ' + res.manifest.id)
  bits.push('技能 ' + (res.skills?.length ? res.skills.join('、') : '无'))
  if (res.replaced) bits.push('旧的那份已移入回收站(' + (res.backup_name || '') + '),可恢复')
  roleMsg.value = bits.join(' · ')
  packAs.value = ''
  showPackImport.value = false
  await loadRoles()
  if (trashOpen.value) await loadTrash()
}

// importRolePack 导入。默认**不覆盖**:后端对同名目标显式 400,这里问过用户(并说明旧份进回收站)
// 才带 overwrite 重试 —— 覆盖是有副作用的动作,不能悄悄做。
async function importRolePack(file: File): Promise<void> {
  roleErr.value = ''
  roleMsg.value = ''
  const as = packAs.value.trim()
  packBusy.value = true
  let askWho = ''
  try {
    if (!(await tryImport(file, as, false))) askWho = roleErr.value
  } catch (e) {
    askWho = (e as Error).message
    roleErr.value = ''
  } finally {
    packBusy.value = false
  }
  if (!askWho) return
  // 只对"同名已存在"问一次;别的错误(frontmatter 不一致/包坏了)直接摆出来,不该被覆盖确认掩住
  if (!askWho.includes('已存在')) {
    roleErr.value = askWho
    return
  }
  const m = /角色 ([^ ]+) 已存在/.exec(askWho)
  guard(
    (m ? '导入会覆盖已存在的角色「' + m[1] + '」？' : '导入会覆盖已存在的同名角色？') +
      '(旧的那份移入 roles/.trash/，可在下方回收站恢复。不想覆盖就改用「导入为」换一个标识)',
    true,
    () => {
      packBusy.value = true
      void (async () => {
        try {
          await tryImport(file, as, true)
        } finally {
          packBusy.value = false
        }
      })()
    },
  )
}

// tryImport 单次导入尝试:成功 → 收尾并返回 true;失败 → 把错误文案落到 roleErr 后返回 false
// (roleErr 就是给调用方看的"为什么没成",不再另设一个变量传话)。
async function tryImport(file: File, as: string, overwrite: boolean): Promise<boolean> {
  try {
    const res = await api.rolePackImport(file, { ...(as ? { as } : {}), overwrite })
    await afterPack(res)
    return true
  } catch (e) {
    roleErr.value = (e as Error).message
    return false
  }
}

function onPackPick(ev: Event): void {
  const el = ev.target as HTMLInputElement
  const f = el.files?.[0]
  el.value = '' // 清掉便于同一个文件再选一次
  if (f) void importRolePack(f)
}

// —— 技能挂载 ——
// 勾选即提交(可一键改回,不进确认弹层);「默认池 / 替换」是模式选择,同样即时。
function mounted(r: RoleSpec | null, name: string): boolean {
  return !!r?.skills?.includes(name)
}
async function toggleMount(name: string, on: boolean): Promise<void> {
  if (!roleDetail.value) return
  await saveRoleDef(() => {
    const next = new Set(roleDetail.value?.skills ?? [])
    if (on) next.add(name)
    else next.delete(name)
    return { skills_set: true, skills: Array.from(next).sort() }
  })
  roleMsg.value = '挂载已更新(下一轮生效)'
}
function useDefaultPool(): void {
  void saveRoleDef(() => ({ skills_set: false }))
}
function useReplacePool(): void {
  void saveRoleDef(() => ({ skills_set: true, skills: roleDetail.value?.skills ?? [] }))
}
function toggleInherit(on: boolean): void {
  void saveRoleDef(() => ({ skills_inherit: on }))
}
// staleMounts 挂载清单里**库里已经不存在**的技能名(技能被删/改名后残留)。
// 为什么要单列出来:下面的勾选列表只按「库里的技能」渲染,这些名字没有对应行 ⇒
// 既看不见也取消不掉(勾选提交时会带着它们一起回去),只能靠切「默认池」绕。
const staleMounts = computed(() => {
  const d = roleDetail.value
  if (!d || !d.skills_set) return []
  const known = new Set(roleLib.value.map((s) => s.name))
  return (d.skills ?? []).filter((n) => !known.has(n))
})

// —— 工具排除(第九十一批) ——
// 与技能挂载**方向相反**:技能是"默认池/替换/再并入"的挂载清单,工具是**排除清单**
// (默认全给,勾上 = 排除)。为何不做成白名单:工具面随插件装卸频繁变化,白名单会让
// 新装的插件对老角色**静默不可见**("这个角色莫名少了个工具",用户归因不了)。
const toolsOpen = ref(false)
const toolAll = ref<ToolDef[]>([])
const toolsErr = ref('')
const toolQuery = ref('')
const toolsLoaded = ref(false)
// loadTools 拉**全量**工具清单(管理面:被角色排除的也得列得出来,否则分不清
// "被角色排除"与"没装这个插件")。展开时才拉,不进每次轮询。
async function loadTools(): Promise<void> {
  toolsErr.value = ''
  try {
    const list = await api.tools(true)
    toolAll.value = Array.isArray(list) ? list : []
    toolsLoaded.value = true
  } catch (e) {
    toolsErr.value = (e as Error).message
  }
}
function toggleTools(): void {
  toolsOpen.value = !toolsOpen.value
  if (toolsOpen.value && !toolsLoaded.value) void loadTools()
}
// toolExcluded 当前角色的排除清单里有没有它(展示与提交同一判据:后端用 sdk.ToolVisible)。
function toolExcluded(name: string): boolean {
  return !!roleDetail.value?.tools_exclude?.includes(name)
}
// toolShown 搜索过滤(工具可能上百条;长清单会把面板撑得很长)。
const toolShown = computed(() => {
  const q = toolQuery.value.trim().toLowerCase()
  if (!q) return toolAll.value
  return toolAll.value.filter((t) => t.name.toLowerCase().includes(q) || (t.description ?? '').toLowerCase().includes(q))
})
// excludedCount 已排除项数(含悬空名 —— 它们同样在 role.yaml 里占着一行)。
const excludedCount = computed(() => roleDetail.value?.tools_exclude?.length ?? 0)
// staleTools 排除清单里**当前不存在**的工具名(插件卸了/名字改了)。悬空名不报错
// (后端只校验形状,存量悬空名放行),但必须看得见、清得掉 —— 否则用户只能手改 role.yaml。
const staleTools = computed(() => {
  const d = roleDetail.value
  if (!d || !toolsOpen.value || !toolsLoaded.value) return []
  const known = new Set(toolAll.value.map((t) => t.name))
  return (d.tools_exclude ?? []).filter((n) => !known.has(n))
})
// toggleTool 勾选即提交(on=true = **排除**它);builder 形式 —— 排队的多个勾选按提交序
// 逐个基于最新状态重算(整份替换的字段不能用旧快照)。
async function toggleTool(name: string, on: boolean): Promise<void> {
  if (!roleDetail.value) return
  await saveRoleDef(() => {
    const next = new Set(roleDetail.value?.tools_exclude ?? [])
    if (on) next.add(name)
    else next.delete(name)
    return { tools_exclude: Array.from(next).sort() }
  })
  roleMsg.value = '工具可见性已更新(下一轮生效)'
}
// restoreAllTools 一键全部恢复(清空排除清单)。
function restoreAllTools(): void {
  void saveRoleDef(() => ({ tools_exclude: [] }))
}
function removeStaleTool(name: string): void {
  void saveRoleDef(() => ({
    tools_exclude: (roleDetail.value?.tools_exclude ?? []).filter((n) => n !== name),
  }))
}
// removeStaleMount 把失效挂载从清单里清掉(后端只对**新增**未知名严格,存量悬空名放行)。
function removeStaleMount(name: string): void {
  void saveRoleDef(() => ({
    skills_set: true,
    skills: (roleDetail.value?.skills ?? []).filter((n) => n !== name),
  }))
}

// skillsByRole 库技能按归属分组展示(共享库在前,各自角色私有在后)
const skillsByRole = computed(() => {
  const groups = new Map<string, SkillInfo[]>()
  for (const s of roleLib.value) {
    const k = s.role ?? ''
    if (!groups.has(k)) groups.set(k, [])
    groups.get(k)!.push(s)
  }
  return Array.from(groups.entries()).sort((a, b) => (a[0] === '' ? -1 : b[0] === '' ? 1 : a[0].localeCompare(b[0])))
})
function groupLabel(role: string): string {
  return role ? `角色私有 · ${role}` : '共享技能库'
}
// createSkill 新建技能(默认落共享库;展开角色详情时可勾选存为角色私有)
function createSkill(): void {
  skErr.value = ''
  const t = skNew.value.triggers
    .split(/[,，\n]/)
    .map((x) => x.trim())
    .filter(Boolean)
  void (async () => {
    try {
      const r = await api.skillCreate({
        role: skNew.value.role,
        name: skNew.value.name.trim(),
        description: skNew.value.description,
        triggers: t,
        body: skNew.value.body,
        overwrite: false,
      })
      roleMsg.value = r.warning ? r.warning : '技能已创建:' + r.name
      showSkillNew.value = false
      skNew.value = { name: '', description: '', triggers: '', body: '', role: '' }
      await loadRoles()
      if (roleDetail.value) await selectRole({ id: roleDetail.value.id } as RoleSpec)
    } catch (e) {
      skErr.value = (e as Error).message
    }
  })()
}
// openSkill 读技能原文(编辑)。confirmed=true 表示草稿丢弃已经过用户确认。
// 注意闸门**不分“是不是同一个技能”**:同一个技能再点一次「原文」= 重读磁盘,
// 照样会把手里没保存的正文盖掉(旧实现用一个 `same` 条件把这一次跳过了)。
async function openSkill(name: string, role: string, confirmed = false): Promise<void> {
  if (!confirmed && skDirty.value) {
    withDrafts(() => void openSkill(name, role, true), ['skill'])
    return
  }
  skErr.value = ''
  try {
    const r = await api.skillGet(name, role)
    skEdit.value = { name, role, content: r.content }
    skSaved.value = r.content
  } catch (e) {
    skErr.value = (e as Error).message
  }
}
// discardSkill 显式放弃技能正文草稿。
function discardSkill(): void {
  guard('放弃尚未保存的技能修改?', true, () => {
    if (skEdit.value) skEdit.value = { ...skEdit.value, content: skSaved.value }
    skErr.value = ''
  })
}
function saveSkill(): void {
  const e = skEdit.value
  if (!e) return
  guard('覆盖写入技能 ' + e.name + ' 的 SKILL.md?', false, async () => {
    skErr.value = ''
    try {
      await api.skillCreate({ role: e.role, name: e.name, content: e.content, overwrite: true })
      roleMsg.value = '技能已保存:' + e.name
      skSaved.value = e.content
      skEdit.value = null
      await loadRoles()
    } catch (err) {
      skErr.value = (err as Error).message
    }
  })
}
function deleteSkill(name: string, role: string): void {
  guard('删除技能 ' + name + '?(移入技能库 .trash/,可在下方回收站恢复)', true, async () => {
    skErr.value = ''
    try {
      await api.skillDelete(name, role)
      roleMsg.value = '技能已删除:' + name + '(可在回收站恢复)'
      await loadRoles()
      if (trashOpen.value) await loadTrash()
    } catch (e) {
      skErr.value = (e as Error).message
    }
  })
}

// openMove 打开改名/移动表单(默认填现值);正文有未保存草稿时先问一声(与其他编辑入口同口径)。
function openMove(name: string, role: string, confirmed = false): void {
  if (!confirmed && skDirty.value) {
    withDrafts(() => openMove(name, role, true), ['skill'])
    return
  }
  skErr.value = ''
  skMove.value = { name, role, toName: name, toRole: role }
}

// refreshRoleDetail 重拉当前展开角色的详情。改技能名/库会连带改角色挂载清单,
// 不重拉就会让面板拿着旧清单,把已经被同步过的挂载误列成「已失效挂载」。
async function refreshRoleDetail(): Promise<void> {
  if (!selRole.value) return
  try {
    roleDetail.value = await api.roleGet(selRole.value)
  } catch {
    // 详情拉不到不影响已提交成功的改动
  }
}

// submitMove 提交改名/移动。为什么二次确认:目录名即身份 —— 改名会同步改掉每个挂载它的
// 角色(面板没有一键回退),且目标同名会被后端拒绝(不覆盖现役技能)。
function submitMove(): void {
  const m = skMove.value
  if (!m || !skMoveDirty.value) return
  const to = m.toName.trim()
  const fromLib = m.role ? `角色私有 · ${m.role}` : '共享技能库'
  const toLib = m.toRole ? `角色私有 · ${m.toRole}` : '共享技能库'
  const what = to !== m.name ? `改名为「${to}」` : '保留原名'
  guard(`把 ${m.name}(${fromLib})${what}并移到「${toLib}」?改名会同步改掉挂载它的角色引用;目标同名会被拒。`, false, async () => {
    skErr.value = ''
    try {
      const r = await api.skillRelocate(m.name, { role: m.role, to_name: to, to_role: m.toRole })
      const n = (r.mounts_updated ?? []).length
      roleMsg.value =
        (r.warning ? r.warning + ' · ' : '') +
        `技能已归位:${m.name} → ${r.role ? '角色私有 · ' + r.role + ' / ' : '共享库 / '}${r.name}` +
        (n ? `(已同步 ${n} 个角色的挂载)` : '')
      if (skEdit.value && skEdit.value.name === m.name && skEdit.value.role === m.role) {
        skEdit.value = null // 正在编辑的对象被挪走了:收起旧草稿(内容已随目录一起搬走)
        skSaved.value = ''
      }
      skMove.value = null
      await loadRoles()
      await refreshRoleDetail()
      if (trashOpen.value) await loadTrash()
    } catch (e) {
      skErr.value = (e as Error).message
    }
  })
}

// toggleTrash 展开/收起回收站(展开时才拉)。
async function toggleTrash(): Promise<void> {
  trashOpen.value = !trashOpen.value
  if (trashOpen.value) await loadTrash()
}

async function loadTrash(): Promise<void> {
  trashErr.value = ''
  try {
    const v = await api.trash()
    trashReady.value = true
    trashRoles.value = v.roles ?? []
    trashSkills.value = v.skills ?? []
  } catch (e) {
    trashReady.value = false // 拉不到就整块不渲染(不摆空壳)
    trashErr.value = (e as Error).message
  }
}

// fmtTrashTime 回收站目录名尾部的时间戳(20260928-153005)→ 2026-09-28 15:30。
// 认不出(手工放进来的目录)就原样回空串。
function fmtTrashTime(s: string): string {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})/.exec(s || '')
  return m ? `${m[1]}-${m[2]}-${m[3]} ${m[4]}:${m[5]}` : ''
}

// restoreTrash 恢复一条回收站记录。name 是**回收站目录名**(不是原 ID/技能名)。
// 同名已存在时后端显式拒绝(不覆盖现役角色/技能)—— 弹层里先说明这一点。
function restoreTrash(kind: 'role' | 'skill', e: { name: string; id?: string; skill?: string; role?: string }): void {
  const label = kind === 'role' ? e.id || e.name : e.skill || e.name
  guard('恢复「' + label + '」?(从回收站移回原位;已存在同名项时会被拒)', false, async () => {
    trashErr.value = ''
    try {
      const r = await api.trashRestore({ kind, name: e.name, role: e.role })
      roleMsg.value = r.warning ? r.warning : '已恢复:' + label
      await loadTrash()
      await loadRoles()
    } catch (err) {
      trashErr.value = (err as Error).message
    }
  })
}

// —— 数据备份(M18) ——
// 字段名与 sdk.BackupInfo 序列化一致(无 json tag → 大写);小写读会导致空名 + NaN KB。
const backups = ref<{ Name: string; Size: number; Time: number }[]>([])
const backupMsg = ref('')
async function loadBackups(): Promise<void> {
  try {
    backups.value = (await api.backups()) ?? [] // ?? []:后端契约空数组,兜底防 null(渲染 .length 安全)
  } catch {
    backups.value = [] // 未装配(host-backup):面板显示暂无备份
  }
}
async function doBackupNow(): Promise<void> {
  busy.value = true
  try {
    await api.backupNow()
    backupMsg.value = '已整体备份(含密钥,存于 GAH_HOME/backups)'
    showInfo('已整体备份')
    await loadBackups()
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
function doBackupRestore(): void {
  const name = backups.value[0]?.Name ?? ''
  if (!name) return
  guard('恢复备份 ' + name + '?(覆盖当前数据;恢复前自动先备份当前态)', true, async () => {
    busy.value = true
    try {
      await api.backupRestore(name)
      backupMsg.value = '已恢复 ' + name + '(重启后完全生效)'
      showInfo('已恢复备份')
      await loadBackups()
    } catch (e) {
      err.value = (e as Error).message
    } finally {
      busy.value = false
    }
  })
}
function fmtSize(n: number): string {
  if (n >= 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return Math.max(1, Math.round(n / 1024)) + ' KB'
}

// —— 历史与压缩 ——
async function applyHistory(): Promise<void> {
  try {
    const r = await api.settingsHistory(histN.value)
    showInfo('历史注入 = ' + (r.history === 0 ? '全部' : r.history === -1 ? '禁止' : '最近 ' + r.history + ' 条'))
  } catch (e) {
    err.value = (e as Error).message
  }
}
async function doCompact(): Promise<void> {
  busy.value = true
  try {
    const r = await api.compact('手动压缩')
    showInfo('已压缩 ' + r.folded + ' 块事件')
    emit('changed')
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
function compactNow(): void {
  guard('压缩当前会话历史(旧内容折叠为摘要,不可逆)?', true, () => void doCompact())
}

// —— Provider(含 W3 首启引导/自检) ——
function toggleAdd(): void {
  showAdd.value = !showAdd.value
  if (showAdd.value) void nextTick(() => keyInput.value?.focus())
}
// applyPreset 一键填入预设(只给 name/base_url:模型名会过期,保存后从实时列表里选)
function applyPreset(p: ProviderPreset): void {
  applied.value = p
  pf.value = { name: p.name, base_url: p.base_url, api_key: '', model: '' }
  showAdd.value = true
  err.value = ''
  probe.value = null
  void nextTick(() => keyInput.value?.focus())
}
// probeProvider 用刚刷新的聚合列表判断端点是否真通(失败给人话原因,不猜)
function probeProvider(name: string): void {
  const g = models.value.find((x) => x.Name === name)
  if (!g) {
    probe.value = { ok: false, text: '端点没出现在模型列表里:base_url 可能不可达或拼写有误' }
    return
  }
  if (g.Err) {
    probe.value = { ok: false, text: explainProbeError(g.Err), raw: g.Err }
    return
  }
  const n = (g.Models ?? []).length
  probe.value = {
    ok: true,
    text: n > 0 ? `已连通,拉到 ${n} 个模型:在上方「模型」下拉里选一个` : '已连通,但端点没返回模型:手动填模型名',
  }
}
// refreshProbeFromActive 当前活跃 provider 拉模型失败时把原因摆出来(打开设置就能看到为什么没模型)
function refreshProbeFromActive(): void {
  const g = models.value.find((x) => x.Name === activeProvider()?.Name)
  if (g?.Err) probe.value = { ok: false, text: explainProbeError(g.Err), raw: g.Err }
}
async function reprobe(): Promise<void> {
  if (!lastSaved.value) return
  busy.value = true
  try {
    await load()
    probeProvider(lastSaved.value)
  } finally {
    busy.value = false
  }
}
async function doProviderUse(name: string): Promise<void> {
  try {
    await api.providerUse(name)
    emit('changed')
    await load()
  } catch (e) {
    err.value = (e as Error).message
  }
}
async function doProviderDelete(p: ProviderInfo): Promise<void> {
  try {
    await api.providerDelete(p.Name)
    probe.value = null // 被删端点的自检结论一并作废(不留别人的旧结论)
    showInfo('已删除 provider「' + p.Name + '」')
    emit('changed') // 首屏/状态栏/模型下拉同步刷新
    await load()
  } catch (e) {
    err.value = (e as Error).message
  }
}
function deleteProvider(p: ProviderInfo): void {
  guard(
    '删除 Provider「' + p.Name + '」?删除后不可恢复;若它是当前活跃的,会自动切到剩下的第一个,全部删完则回退环境变量/样板配置。',
    true,
    () => void doProviderDelete(p)
  )
}
async function addProvider(): Promise<void> {
  if (!pf.value.name || !pf.value.base_url) {
    err.value = '名称与 base_url 必填'
    return
  }
  const name = pf.value.name
  busy.value = true
  try {
    await api.providerAdd({ name: pf.value.name, base_url: pf.value.base_url, api_key: pf.value.api_key, model: pf.value.model || undefined })
    showAdd.value = false
    pf.value = { name: '', base_url: '', api_key: '', model: '' }
    applied.value = null
    probe.value = null
    lastSaved.value = name
    showInfo('已保存 provider(首个自动激活)')
    emit('changed')
    await load()
    probeProvider(name) // W3 连通性自检:失败给 401/404/DNS 人话
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// —— 定时计划(NOND-W4) ——
const schedules = ref<Schedule[]>([])
const schedErr = ref('') // 计划段独立错误提示(不污染全局 err,便于定位)
const sf = ref({ name: '', cron: '', prompt: '' })
const showSchedAdd = ref(false)
const schedReady = ref(true) // 后端未装配(ctx.schedule 503)时整段只给提示
async function loadSchedules(): Promise<void> {
  try {
    schedules.value = (await api.schedules()) ?? []
    schedErr.value = ''
    schedReady.value = true
  } catch (e) {
    // 503 = 宿主未装配 host-schedule:不是错误,静默隐藏本段
    if ((e as Error).message.includes('503')) {
      schedReady.value = false
      return
    }
    schedErr.value = (e as Error).message
  }
}
function schedCron(): string {
  return sf.value.cron.trim().split(/\s+/).filter((s) => s.length > 0).join(' ')
}
async function addSchedule(): Promise<void> {
  const cron = schedCron()
  if (cronShapeError(cron)) {
    schedErr.value = cronShapeError(cron)
    return
  }
  if (!sf.value.prompt.trim()) {
    schedErr.value = '请填写到点要执行的任务描述(它会作为输入发给模型)'
    return
  }
  busy.value = true
  try {
    await api.scheduleAdd({ name: sf.value.name.trim(), cron, prompt: sf.value.prompt.trim() })
    sf.value = { name: '', cron: '', prompt: '' }
    showSchedAdd.value = false
    schedErr.value = ''
    showInfo('已创建计划')
    await loadSchedules()
  } catch (e) {
    schedErr.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
async function toggleSchedule(s: Schedule): Promise<void> {
  try {
    await api.scheduleUpdate(s.id, { enabled: !s.enabled })
    await loadSchedules()
  } catch (e) {
    schedErr.value = (e as Error).message
  }
}
async function doScheduleRun(id: string): Promise<void> {
  try {
    await api.scheduleRun(id)
    showInfo('已触发一次(结果在会话流与计划状态里)')
    await loadSchedules()
  } catch (e) {
    schedErr.value = (e as Error).message
  }
}
function deleteSchedule(s: Schedule): void {
  guard('删除计划「' + s.name + '」?(定时触发从此消失)', true, async () => {
    try {
      await api.scheduleDelete(s.id)
      await loadSchedules()
    } catch (e) {
      schedErr.value = (e as Error).message
    }
  })
}

// —— MCP server 配置(NOND-M1 第 2/3 步) ——
// 编辑态与磁盘态分离:改动全部落在本地草稿上,点「保存并重载」才写 mcp.yaml 并重启插件。
const mcpView = ref<McpView | null>(null)
const mcpDrafts = ref<McpDraft[]>([])
// 磁盘当前内容指纹(脏检测基准;保存成功后刷新)。初值必须是「空清单」的指纹 ——
// 用 '' 会和 draftKey([]) 的 '[]' 不等,首帧即脏(打开面板先弹一次丢弃确认,按钮也提前亮)。
const mcpBase = ref(draftKey([]))
const mcpErr = ref('')
const mcpMsg = ref('')
const mcpDirty = computed(() => draftKey(mcpDrafts.value) !== mcpBase.value)
// 环境变量来源的条目只读(来自 GAH_MCP_COMMAND(S),不在文件里)
const mcpEnvRows = computed<McpServer[]>(() => (mcpView.value?.servers ?? []).filter((s) => !canEdit(s)))
const mcpNotices = computed<string[]>(() => (mcpView.value ? viewNotices(mcpView.value) : []))

// loadMcp 拉 MCP 配置。`force=true` 才允许覆盖手里没保存的草稿(保存成功后/显式放弃后) ——
// 否则打开面板/切回来时会静默丢掉未保存的编辑(与另外三处文本框同口径)。
async function loadMcp(force = false): Promise<void> {
  if (!force && mcpDirty.value) {
    withDrafts(() => void loadMcp(true), ['mcp'])
    return
  }
  try {
    const v = await api.mcp()
    mcpView.value = v
    mcpDrafts.value = v.servers.filter(canEdit).map(draftFrom)
    mcpBase.value = draftKey(mcpDrafts.value)
    mcpErr.value = ''
  } catch (e) {
    mcpErr.value = (e as Error).message
  }
}
// discardMcp 显式放弃 MCP 草稿(回到磁盘那一份)。主动放弃也要问一声 —— 与另外两处一致。
function discardMcp(): void {
  guard('放弃尚未保存的 MCP server 配置修改?(回到磁盘上的那一份)', true, () => void loadMcp(true))
}
function mcpAdd(): void {
  mcpDrafts.value.push({ name: '', command: '', enabled: true, mode: MCP_MODE_DIRECT })
}
function mcpRemove(i: number): void {
  mcpDrafts.value.splice(i, 1)
}
// mcpRowState 草稿行的运行期状态(按 名称+模式 匹配当前视图;改了名字就是「未生效」)
function mcpRowState(r: McpDraft): string {
  const hit = (mcpView.value?.servers ?? []).find((s) => s.name === r.name.trim() && s.mode === r.mode)
  if (!hit) return '未生效'
  return serverStateLabel(hit)
}
function mcpRowStateClass(r: McpDraft): string {
  const hit = (mcpView.value?.servers ?? []).find((s) => s.name === r.name.trim() && s.mode === r.mode)
  if (!hit) return 'ss-none'
  if (!hit.enabled) return 'ss-none'
  return hit.loaded ? 'ss-ok' : 'ss-skipped'
}
async function saveMcp(): Promise<void> {
  const bad = validateDraft(mcpDrafts.value)
  if (bad) {
    mcpErr.value = bad
    return
  }
  busy.value = true
  mcpMsg.value = ''
  try {
    // 整行命令交给后端按引号规则拆参数(与终端一致);args 不在这里拆
    const v = await api.mcpSave(
      mcpDrafts.value.map((r) => ({
        name: r.name.trim(),
        command: normalizeCommand(r.command),
        enabled: r.enabled,
        mode: r.mode,
      })),
    )
    mcpView.value = v
    mcpDrafts.value = v.servers.filter(canEdit).map(draftFrom)
    mcpBase.value = draftKey(mcpDrafts.value)
    mcpErr.value = ''
    mcpMsg.value = v.reload_err ? v.reload_err : '已保存并重载:模型工具表已更新'
    setTimeout(() => (mcpMsg.value = ''), 8000)
  } catch (e) {
    mcpErr.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// —— 插件开关(manage=external/scenario 为只读,web 侧不给启停;由外部进程/场景装配管理)——
const MANAGE_META: Record<string, { label: string; tip: string }> = {
  host: { label: '宿主运行', tip: '' },
  external: { label: '外部进程', tip: '能力已由 host-bridge 外部进程(tool-basic 等)提供,勿在此启用' },
  scenario: { label: '场景装配', tip: '按 profile 场景启用(mock / mcp-serve / tui),勿在此启用' },
  web: { label: '未启用', tip: '' },
}
async function doPluginToggle(p: PluginInfo, on: boolean): Promise<void> {
  try {
    await api.pluginToggle(p.ID, on)
    emit('changed')
    await load()
  } catch (e) {
    err.value = (e as Error).message
  }
}
function pluginToggle(p: PluginInfo): void {
  if (p.manage === 'external' || p.manage === 'scenario') return // 只读,不触发
  const on = p.State !== 'loaded'
  guard((on ? '加载并启用插件「' : '卸载插件「') + p.ID + '」?', !on, () => void doPluginToggle(p, on))
}
function manageMeta(p: PluginInfo): { label: string; tip: string } {
  return MANAGE_META[p.manage] ?? { label: p.State === 'loaded' ? '已加载' : '未启用', tip: '' }
}
// 插件列表收纳:默认只列 5 条(≤5 条即全列 —— 折叠只在"长"的时候有意义),其余折进「展开全部」;
// 筛选框按 ID/类型/状态过滤。这是面板里最长的一段,不收起来左栏导航也救不了滚动。
const PLUGIN_CAP = 5
const pluginFilter = ref('')
const pluginExpanded = ref(false)
const pluginHits = computed(() => {
  const f = pluginFilter.value.trim().toLowerCase()
  if (!f) return plugins.value
  return plugins.value.filter((p) => `${p.ID} ${p.Type} ${p.State}`.toLowerCase().includes(f))
})
const pluginShown = computed(() =>
  pluginExpanded.value || pluginHits.value.length <= PLUGIN_CAP ? pluginHits.value : pluginHits.value.slice(0, PLUGIN_CAP),
)
const pluginHidden = computed(() => pluginHits.value.length - pluginShown.value.length)

// —— 指令热更 ——
async function doReload(): Promise<void> {
  try {
    await api.reload()
    showInfo('指令文件已重载')
  } catch (e) {
    err.value = (e as Error).message
  }
}

function showInfo(s: string): void {
  info.value = s
  setTimeout(() => (info.value = ''), 3000)
}

onMounted(() => {
  void load()
  void loadMcp()
  void loadInstructions()
  void loadRoles()
  void refreshInstallList()
  window.addEventListener('keydown', onEsc, true)
})
onUnmounted(() => {
  if (stateTimer) clearInterval(stateTimer)
  if (navRaf) cancelAnimationFrame(navRaf)
  window.removeEventListener('keydown', onEsc, true)
})
// 关闭语义(Win 端反馈):**只能手动关闭** —— 遮罩点击不再关闭(误触会丢正在编辑的表单),
// 出口只有 ✕ 按钮与 Esc。Esc 用捕获阶段并阻止冒泡,避免同时触发输入框的「Esc 清空」(草稿丢失)。
function onEsc(e: KeyboardEvent): void {
  if (!props.open || e.key !== 'Escape') return
  e.preventDefault()
  e.stopPropagation()
  emit('close')
}
// 打开时同步当前值与枚举
watch(
  () => props.open,
  (o) => {
    if (!o) return
    void load()
    void loadSchedules()
    void loadMcp()
    void loadRoles()
    // 外部定位(首启引导 'provider' / 状态栏版本号 'about' / 看板「管理计划」'schedule'):
    // 段键就是导航键(同写 data-sec),段不渲染时 jumpTo 静默跳过。
    if (props.focus) void nextTick(() => jumpTo(props.focus as string, false))
  },
)
// 定时跑完一轮(schedule/run SSE 帧)→ 刷新计划状态(上次运行/下次触发已变)
watch(
  () => props.schedTick,
  () => {
    if (props.open) void loadSchedules()
  },
)
watch(
  // state.model 必须在监听源里:选完模型后 state 才更新,漏掉它就会出现
  // 「已选了模型但当前模型不显示」(2026-09-16 真机)。
  () => [providers.value, models.value, props.state.model],
  () => {
    // 先重建选项(可能要补「(当前)」行),再算高亮 —— 顺序反了首轮就匹配不上
    buildModelOptions()
    modelVal.value = currentModelValue(modelOptions.value, props.state.model, activeProvider()?.Name ?? '')
  },
  { immediate: true, deep: true },
)
// 打开面板即对齐开机自启与检查更新状态,并在开着期间持续跟随(托盘那边随时可能改)。
watch(
  () => props.open,
  (open) => {
    if (stateTimer) {
      clearInterval(stateTimer)
      stateTimer = undefined
    }
    if (!open) return
    void refreshDesktopState()
    stateTimer = setInterval(() => void refreshDesktopState(), 1500)
  },
  { immediate: true },
)
</script>

<template>
  <!-- 遮罩点击不关闭(只能手动关闭:✕ / Esc);见 onEsc 注释 -->
  <!-- 显隐走全局 pane 过渡(style.css):遮罩淡 + 抽屉横滑;关闭不再硬切 -->
  <Transition name="pane">
    <div v-if="open" class="mask">
      <aside class="panel" role="dialog" aria-label="设置">
      <header class="head">
        <h2 class="title">设置</h2>
        <button class="x" data-tip="关闭设置" @click="emit('close')">✕</button>
      </header>

      <div v-if="err" class="err">{{ err }}</div>
      <div v-if="info" class="info">{{ info }}</div>

      <!-- 左导航 + 右内容(窄窗口由 CSS 退化为顶部芯片条):长面板里「滚到底才能看到某段」
           由导航一次点击替代;高亮跟着滚动走。 -->
      <div class="cols">
        <nav class="nav" aria-label="设置分区">
          <button
            v-for="s in navItems"
            :key="s.key"
            class="nav-it"
            :class="{ on: activeSec === s.key }"
            @click="jumpTo(s.key)"
          >
            {{ s.label }}
          </button>
        </nav>
        <div ref="bodyEl" class="body" @scroll="onBodyScroll">
        <!-- 模型(输入筛选 + 匹配列表;模型多时原生 select 难找,点选即应用) -->
        <section data-sec="model" class="sec">
          <h3 class="h">模型</h3>
          <div class="row">
            <span class="lab-inline">当前模型</span>
            <input
              id="set-model"
              v-model="modelFilter"
              class="inp grow"
              :placeholder="'筛选模型名/Provider'"
              :disabled="busy"
            />
          </div>
          <!-- 当前生效单独一行:上面那个输入框是筛选框,用户会把它当成「当前模型」的显示位,
               真机上因此得出「已选了模型但当前模型显示为空」的结论。
               这一行是「当前状态」不是说明文字 → 用强调色 + 加重,别与灰色提示同级
               (2026-10-03 用户反馈:这一行应为强调色,更显眼)。 -->
          <p class="cur-line" data-testid="cur-model">
            <span class="cur-key">当前生效</span>
            <span class="cur-val">{{ curModelLabel || '(未设置)' }}</span>
            <span v-if="props.state.model_from === 'role'" class="sstate ss-ok">角色指定</span>
            <span v-if="props.state.model_from === 'role' && props.state.model_session" class="dim">
              （会话档 {{ props.state.model_session }} 被角色覆盖，停用角色后生效）
            </span>
          </p>
          <p v-if="props.state.model_from === 'role'" class="dim" data-testid="model-role-note">
            当前角色已指定模型：这里切换只改会话档，实际仍按角色跑。
          </p>
          <div class="m-list">
            <div
              v-for="o in filteredModels"
              :key="o.value"
              class="m-item"
              :class="{ on: modelVal === o.value }"
              data-tip="点击切换模型"
              @click="pickModel(o)"
            >
              <span class="m-lab">{{ o.label }}</span>
            </div>
            <p v-if="!filteredModels.length" class="dim">无匹配模型(清空筛选或经 /model 手动设置)</p>
          </div>
          <p v-if="!models.length && !modelOptions.length" class="dim">模型列表不可用(可经 Provider 配置或 /model 手动设置)</p>
        </section>

        <!-- 推理 -->
        <section data-sec="reason" class="sec">
          <h3 class="h">推理</h3>
          <div class="row">
            <span class="lab-inline">思考</span>
            <div class="seg">
              <button
                v-for="t in THINK"
                :key="t"
                class="seg-it"
                :class="{ on: (props.state.thinking_session ?? props.state.thinking) === t }"
                :data-tip="'思考 ' + THINK_LABEL[t]"
                @click="applyCtl({ thinking: t })"
              >
                {{ THINK_LABEL[t] }}
              </button>
            </div>
          </div>
          <p v-if="props.state.thinking_from === 'role'" class="dim" data-testid="thinking-role-note">
            当前角色的思考档覆盖了会话设置：实际按「{{ THINK_LABEL[props.state.thinking] || props.state.thinking }}」跑（上面选的是会话档，停用角色后生效）。
          </p>
          <div class="row">
            <span class="lab-inline">沙箱</span>
            <div class="seg">
              <button
                v-for="s in SB"
                :key="s.v"
                class="seg-it"
                :class="{ on: props.state.sandbox === s.v }"
                :data-tip="'沙箱 ' + s.label"
                @click="applyCtl({ sandbox: s.v })"
              >
                {{ s.label }}
              </button>
            </div>
          </div>
          <p v-if="props.state.sandbox_from === 'role'" class="dim" data-testid="sandbox-role-note">
            当前角色把沙箱收紧为「{{ SB_ZH[props.state.sandbox_effective ?? ''] ?? props.state.sandbox_effective }}」：写入被拦在这里，
            上面选的是全局档（停用角色后生效）。
          </p>
          <div class="row">
            <span class="lab-inline">审批</span>
            <div class="seg">
              <button
                v-for="s in AP"
                :key="s.v"
                class="seg-it"
                :class="{ on: props.state.approval === s.v }"
                :data-tip="'审批 ' + s.label"
                @click="applyCtl({ approval: s.v })"
              >
                {{ s.label }}
              </button>
            </div>
          </div>
          <!-- 上面选的是**全局档**;角色收紧时实际按更严的那个裁决(第九十二批)。
               不说清就会变成"点了严格却不生效"的静默矛盾。 -->
          <p v-if="props.state.approval_from === 'role'" class="dim" data-testid="approval-role-note">
            当前角色把审批收紧为「{{ AP_ZH[props.state.approval_effective ?? ''] ?? props.state.approval_effective }}」：实际按它裁决（上面选的是全局档，停用角色后生效）。
          </p>
          <!-- 联动开关(R10 ②-2):默认开启时审批档会覆盖沙箱档(开放 → 完全访问、
               严格 → 只读),于是"改了沙箱档却不生效";关掉后沙箱档独立生效。
               后端不支持时该字段缺失 → 整行不显示(不摆一个点了没反应的开关)。 -->
          <div v-if="props.state.sandbox_sync !== undefined" class="row">
            <span class="lab-inline">档位联动</span>
            <label class="chk" :data-tip="'审批档联动沙箱:开启时开放档 → 完全访问、严格档 → 只读'">
              <input
                type="checkbox"
                :checked="props.state.sandbox_sync"
                :disabled="busy"
                @change="onSyncToggle"
              />
              <span>审批档覆盖沙箱档</span>
            </label>
            <span class="dim grow">{{ props.state.sandbox_sync ? '开启' : '关闭(沙箱档独立生效)' }}</span>
          </div>
        </section>

        <!-- 历史与压缩 -->
        <!-- 角色(第七十九批 1b):人设 + 工作规则(AGENTS.md)+ 技能挂载。
             未装配 ctx.roles 的环境整段不渲染(roleReady=false),导航项也一并隐藏。 -->
        <!-- 全局指令(第八十一批):角色之外的基线层 —— 对所有角色生效,除非角色声明 exclude_global -->
        <section v-if="instrReady" data-sec="instr" class="sec">
          <h3 class="h">
            指令
            <span v-if="instrDirty" class="dirty">未保存</span>
            <button class="link" data-tip="编辑全局 AGENTS.md" @click="instrEdit = !instrEdit">
              {{ instrEdit ? '收起' : '编辑' }}
            </button>
          </h3>
          <p class="dim">
            全局 <span class="mono">AGENTS.md</span>（<span class="mono">{{ instrPath }}</span>）对<strong>所有</strong>角色生效：没有角色时注入，有角色时也注入（除非该角色声明了「不注入全局 AGENTS.md」）。
          </p>
          <div v-if="instrErr" class="serr">{{ instrErr }}</div>
          <div v-if="instrWarn" class="swarn">{{ instrWarn }}</div>
          <p v-if="instrMsg" class="dim ok">{{ instrMsg }}</p>
          <p class="dim">
            {{ instrExists ? '当前 ' + instrBytes + ' 字节' : '还没有这份文件（保存即创建）' }}／上限 {{ instrMax }} 字节
            <span v-if="instrOver">· 现有文件已超上限，保存会被拒；注入时按上限截断并标注「已截断」</span>
          </p>
          <div v-if="instrEdit" class="add-form">
            <label class="fld">
              <span class="fld-lab">全局指令（原文，保存即覆盖）</span>
              <textarea v-model="instrDraft" class="inp mono" rows="12"></textarea>
            </label>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy" @click="saveInstructions">保存全局指令</button>
              <button v-if="instrDirty" class="ghost danger-text" @click="discardInstr">放弃修改</button>
              <button class="ghost" @click="instrEdit = false">收起</button>
            </div>
          </div>
        </section>

        <section v-if="memReady" data-sec="memory" class="sec">
          <h3 class="h">
            记忆
            <span v-if="!memEnabled" class="dim" data-testid="mem-off">注入已关</span>
          </h3>
          <p class="dim">
            跨会话记忆是<strong>事实性记录</strong>：每轮按预算注入系统提示，排在角色指令之后 —— 记忆
            <strong>不能覆盖</strong>指令与工作规则，冲突时以指令为准。记忆不会自动提取（要记住什么，由你
            或 <span class="mono">/memory add</span> 明确写下来）。
          </p>
          <p class="dim">
            存在纯 markdown 文件 <span class="mono">{{ memPath }}</span
            >，写错了可以直接改这个文件（记忆层最大的风险是写错，给人一个文本文件比给一套管理工具更有用）。
          </p>
          <div v-if="memErr" class="serr">{{ memErr }}</div>
          <p v-if="memMsg" class="dim ok">{{ memMsg }}</p>
          <p class="dim">
            用户级 {{ memUser.length }} 条／本项目 {{ memProject.length }} 条／注入预算 {{ memBudget }}
            字节（超预算时按时间倒序丢最旧）
          </p>
          <template v-if="memHasCandidates">
            <p class="dim" data-testid="mem-cand-head">
              候选池 {{ memUsed }}/{{ memLimit }} 条（今日已提 {{ memToday }}/{{ memTodayMax }}）——
              <strong>候选不进上下文</strong>，只有「转正」之后才生效。文件
              <span class="mono">{{ memPath.replace(/user\.md$/, 'candidates.md') }}</span> 同样可以直接改。
            </p>
            <div v-if="memCand.length" class="mem-list">
              <div v-for="(l, i) in memCand" :key="'c' + i" class="mem-item">
                <span class="mem-txt" :data-tip="memContent(l)">{{ memContent(l) }}</span>
                <button class="ghost solid" :disabled="memBusy" data-tip="转正为记忆(之后每轮带进上下文)" @click="memoryAccept(i + 1)">转正</button>
                <button class="ghost danger-text" :disabled="memBusy" data-tip="丢弃(不进记忆)" @click="memoryReject(i + 1)">丢弃</button>
              </div>
            </div>
            <div v-if="memCand.length" class="form-acts">
              <button class="ghost solid" :disabled="memBusy" data-testid="mem-accept-all" @click="memoryAcceptAll">全部转正</button>
              <button class="ghost danger-text" :disabled="memBusy" @click="memoryRejectAll">全部丢弃</button>
            </div>
            <p v-else class="dim">候选池是空的。</p>
          </template>
          <div class="add-form">
            <label class="fld">
              <span class="fld-lab">记一条（写清“是什么、为什么”）</span>
              <input
                v-model="memDraft"
                class="inp"
                :disabled="memBusy"
                placeholder="比如：报告里的图表用蓝灰配色，不要渐变"
                data-testid="mem-input"
                @keyup.enter="memoryAdd"
              />
            </label>
            <div class="form-acts">
              <button class="ghost solid" :disabled="memBusy || !memDraft.trim()" data-testid="mem-add" @click="memoryAdd">记住</button>
              <button
                v-if="memHasCandidates"
                class="ghost"
                :disabled="memBusy || !memDraft.trim()"
                data-testid="mem-propose"
                data-tip="先进候选池,确认之后才进上下文"
                @click="memoryPropose"
              >
                提候选
              </button>
              <button v-if="memEnabled" class="ghost danger-text" data-tip="只关注入，记忆文件保留" @click="memoryToggle(false)">关掉注入</button>
              <button v-else class="ghost" data-tip="重新把记忆注入系统提示" @click="memoryToggle(true)">开启注入</button>
            </div>
          </div>
          <template v-if="memUser.length">
            <div class="mem-list">
              <div v-for="(l, i) in memUser" :key="i" class="mem-item">
                <span class="mem-txt" :data-tip="memContent(l)">{{ memContent(l) }}</span>
                <span v-if="memSource(l)" class="dim mono">{{ memSource(l) }}</span>
                <button class="ghost danger-text" :disabled="memBusy" data-tip="删除这条记忆" @click="memoryRemove(i + 1, l)">删除</button>
              </div>
            </div>
            <div v-if="memSources.length" class="mem-src">
              <p class="dim">按来源整段删（某个会话写进来的记忆一次性清干净）</p>
              <div class="form-acts">
                <button
                  v-for="s in memSources"
                  :key="s"
                  class="ghost danger-text"
                  :disabled="memBusy"
                  data-tip="删掉该来源的全部记忆"
                  @click="memoryRemoveSource(s)"
                >
                  会话 {{ s }}
                </button>
              </div>
            </div>
          </template>
          <p v-else class="dim">还没有记忆。记一条试试，比如“部署前先跑一次 /verify”。</p>
          <div v-if="memProject.length" class="mem-list">
            <div v-for="(l, i) in memProject" :key="'p' + i" class="mem-item">
              <span class="mem-txt dim" :data-tip="memContent(l)">{{ memContent(l) }}</span>
            </div>
          </div>
        </section>

        <section v-if="roleReady" data-sec="role" class="sec">
          <h3 class="h">
            角色
            <button class="link" data-tip="导入角色包(单文件 zip,别人分享给你的)" @click="showPackImport = !showPackImport">
              {{ showPackImport ? '收起' : '⤒ 导入' }}
            </button>
            <button class="link" data-tip="新建一个角色" @click="showRoleNew = !showRoleNew">
              {{ showRoleNew ? '收起' : '＋ 新建' }}
            </button>
          </h3>
          <p class="dim">
            角色 = 人设（身份句）+ 工作规则（AGENTS.md）+ 技能挂载。切换后<strong>下一轮</strong>生效，不换会话（回合历史与工作区都不动，与切换工作区不同）。列表顶部「默认（基线）」= 不启用任何角色：全局指令 + 项目规则 + 默认技能池。
          </p>
          <div v-if="roleErr" class="serr">{{ roleErr }}</div>
          <p v-if="roleMsg" class="dim ok">{{ roleMsg }}</p>
          <p v-if="roleSaving" class="dim" data-testid="role-saving">提交中…</p>
          <p v-if="roleProblems.length" class="dim">
            部分角色文件读不了（已跳过）：{{ roleProblems.map((p) => p.id).join('、') }}
          </p>

          <!-- 导入角色包:一个 zip 里装着定义 + 工作规则 + 私有技能(第九十三批)。
               「导入为」留空 = 用包里的标识;同名时后端会拒,再问一次是否覆盖(旧份进回收站)。 -->
          <div v-if="showPackImport" class="add-form">
            <label class="fld">
              <span class="fld-lab">角色包文件(.zip)</span>
              <input
                class="inp"
                type="file"
                accept=".zip,application/zip"
                :disabled="packBusy"
                data-testid="pack-file"
                @change="onPackPick"
              />
            </label>
            <label class="fld">
              <span class="fld-lab">导入为（可选：留空 = 用包里的标识）</span>
              <input v-model="packAs" class="inp mono" placeholder="finance-copy" :disabled="packBusy" />
            </label>
            <p class="dim">
              包里是别人导出的角色（人设 + 工作规则 + 私有技能）；导入<strong>不会</strong>动你现有角色，
              同名时默认拒绝，确认后才覆盖（旧份进回收站可恢复）。
              想并存一份就填「导入为」换一个标识。
            </p>
            <p v-if="packBusy" class="dim" data-testid="pack-busy">导入中…</p>
          </div>

          <div v-if="showRoleNew" class="add-form">
            <label class="fld">
              <span class="fld-lab">标识（小写字母/数字/连字符）</span>
              <input v-model="rf.id" class="inp mono" placeholder="finance" />
            </label>
            <label class="fld">
              <span class="fld-lab">显示名（留空取标识）</span>
              <input v-model="rf.name" class="inp" placeholder="财务分析" />
            </label>
            <label class="fld">
              <span class="fld-lab">一句话定位（可选）</span>
              <input v-model="rf.description" class="inp" placeholder="记账与报表分析" />
            </label>
            <label class="fld">
              <span class="fld-lab">人设（身份句，一句话即可）</span>
              <input v-model="rf.identity" class="inp" placeholder="你是资深财务分析师，先对齐口径再给数。" />
            </label>
            <label class="chk">
              <input v-model="rf.exclude_global" type="checkbox" />
              <span>不注入全局 AGENTS.md（非开发角色建议勾上）</span>
            </label>
            <p class="dim">新建后会同时生成一份可改的 AGENTS.md 模板（空文件不会进系统提示）。</p>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy || !rf.id" @click="createRole">创建角色</button>
            </div>
          </div>

          <p v-if="roleDangling" class="dim">
            偏好里的当前角色 <span class="mono">{{ roleCurrent }}</span> 已经不存在了（角色目录被外部删除），本轮按基线运行（只有全局与项目指令）；点下面「默认（基线）」的「切换」即可切回来。
          </p>

          <div class="plist">
            <!-- 合成行(不落盘):不启用角色也是一种确定的形态,给它一个名字与一键切回的落点 -->
            <div class="prow scrow" :class="{ off: !roleBaseline }">
              <div class="pmain">
                <span class="sname">
                  默认（基线）
                  <span class="sstate mono">none</span>
                  <span v-if="roleBaseline" class="sstate ss-ok">当前</span>
                </span>
                <span class="psub">不注入角色设定：全局指令 + 项目规则</span>
                <span class="psub">技能：默认池（全部共享技能）</span>
              </div>
              <div class="sacts">
                <button v-if="!roleBaseline" class="ghost" data-tip="下一轮生效，不换会话" @click="useRole('')">切换</button>
              </div>
            </div>
            <p v-if="roleHint" class="dim" data-testid="role-hint">
              还没挑过角色。下面 {{ roles.length }} 个预置角色按场景分组，<strong>挑一个</strong>就能换身份、工作规则与技能面；
              不挑也能用（走默认：全局指令 + 全部技能）。选中后本提示不再出现。
            </p>
            <template v-for="sec in roleGroups" :key="sec.group">
              <div v-if="sec.items.length > 1 || sec.group !== '其它'" class="srow-label">
                {{ sec.group }}
                <span class="dim">{{ sec.items.length }}</span>
              </div>
              <div v-for="r in sec.items" :key="r.id" class="prow scrow" :class="{ off: !roleIsCurrent(r) }">
              <div class="pmain">
                <span class="sname">
                  {{ r.name || r.id }}
                  <span class="sstate mono">{{ r.id }}</span>
                  <span v-if="roleIsCurrent(r)" class="sstate ss-ok">当前</span>
                  <span v-if="r.seed" class="sstate">预置</span>
                </span>
                <span v-if="r.description" class="psub">{{ r.description }}</span>
                <span class="psub">
                  技能：{{ r.skills_set ? (r.skills?.length ? r.skills.join('、') : '未挂载') : '默认池（全部库技能）' }}
                  <span v-if="r.skills_inherit">+ 默认池</span>
                </span>
                <span v-if="r.exclude_global" class="psub">不注入全局 AGENTS.md</span>
                <span v-if="r.model || r.thinking" class="psub">
                  模型：{{ r.model || '跟随会话' }} · 思考：{{ r.thinking ? THINK_LABEL[r.thinking] || r.thinking : '跟随会话' }}
                </span>
                <span v-if="r.approval || r.sandbox" class="psub">收紧：{{ tierBrief(r) }}</span>
              </div>
              <div class="sacts">
                <button v-if="!roleIsCurrent(r)" class="ghost" data-tip="下一轮生效，不换会话" @click="useRole(r.id)">切换</button>
                <button class="ghost" @click="selectRole(r)">{{ selRole === r.id ? '收起' : '编辑' }}</button>
                <button
                  class="ghost"
                  :disabled="packBusy"
                  data-tip="导成单个 zip（定义 + 工作规则 + 私有技能），可分享给别的 gah"
                  @click="exportRole(r)"
                >
                  导出
                </button>
                <button class="ghost danger-text" data-tip="删除角色（需确认，移入回收站）" @click="deleteRole(r)">删除</button>
              </div>
            </div>
            </template>
            <p v-if="!roles.length" class="dim">
              还没有角色：点上方「＋ 新建」建一个；首次启动会释放 12 个预置角色（通用 / 工程 / 数据 / 写作 / 学习五组）。
            </p>
          </div>

          <p v-if="roleCurrent" class="row acts">
            <button class="ghost" data-tip="回到基线（不注入任何角色设定）" @click="useRole('')">停用当前角色</button>
          </p>

          <!-- 详情编辑（列表不带正文，展开时才拉） -->
          <div v-if="roleDetail" class="role-detail">
            <h3 class="h">编辑：{{ roleDetail.name || roleDetail.id }}</h3>
            <div class="row">
              <span class="lab-inline">显示名</span>
              <input class="inp grow" :value="roleDetail.name" @change="(e) => (roleDetail!.name = (e.target as HTMLInputElement).value)" />
            </div>
            <div class="row">
              <span class="lab-inline">一句话定位</span>
              <input class="inp grow" :value="roleDetail.description" @change="(e) => (roleDetail!.description = (e.target as HTMLInputElement).value)" />
            </div>
            <div class="row">
              <span class="lab-inline">人设</span>
              <input class="inp grow" :value="roleDetail.identity" @change="(e) => (roleDetail!.identity = (e.target as HTMLInputElement).value)" />
            </div>
            <label class="chk">
              <input
                type="checkbox"
                :checked="!!roleDetail.exclude_global"
                @change="saveRoleDef({ exclude_global: ($event.target as HTMLInputElement).checked })"
              />
              <span>不注入全局 AGENTS.md</span>
            </label>
            <!-- 角色携带模型/思考档(第八十六批):留空 = 跟随会话。
                 这两个输入框不是"建议值"而是真生效的值(每回合注入请求),故文案要写清。 -->
            <div class="row">
              <span class="lab-inline">模型</span>
              <input
                class="inp grow mono"
                :value="roleDetail.model || ''"
                placeholder="留空 = 跟随会话模型"
                @change="saveRoleDef({ model: ($event.target as HTMLInputElement).value })"
              />
            </div>
            <div class="row">
              <span class="lab-inline">思考</span>
              <div class="seg">
                <button
                  class="seg-it"
                  :class="{ on: !roleDetail.thinking }"
                  data-tip="跟随会话思考档"
                  @click="saveRoleDef({ thinking: '' })"
                >
                  跟随会话
                </button>
                <button
                  v-for="t in THINK"
                  :key="t"
                  class="seg-it"
                  :class="{ on: roleDetail.thinking === t }"
                  :data-tip="'角色固定为 ' + THINK_LABEL[t]"
                  @click="saveRoleDef({ thinking: t })"
                >
                  {{ THINK_LABEL[t] }}
                </button>
              </div>
            </div>
            <p class="dim">
              模型/思考档按角色生效：每回合的请求都按这里填的值走（会话档被它覆盖，停用角色后恢复）。
              模型名没有任何 provider 能接时本轮会退回会话模型并提示（不会硬失败）。
            </p>

            <!-- 权限档(第九十二批):角色只能收紧,不能放宽 —— 值域由 SDK 定死,
                 后端对 open/full-access 直接拒(400),故这里只列更严的档。 -->
            <h3 class="h">权限档（只能收紧）</h3>
            <div class="row">
              <span class="lab-inline">审批</span>
              <select
                class="sel grow"
                :value="roleDetail.approval || ''"
                :disabled="roleSaving"
                @change="saveRoleDef({ approval: ($event.target as HTMLSelectElement).value })"
              >
                <option value="">跟随全局</option>
                <option v-for="a in ROLE_AP" :key="'ra-' + a.v" :value="a.v">{{ a.label }}</option>
              </select>
            </div>
            <div class="row">
              <span class="lab-inline">沙箱</span>
              <select
                class="sel grow"
                :value="roleDetail.sandbox || ''"
                :disabled="roleSaving"
                @change="saveRoleDef({ sandbox: ($event.target as HTMLSelectElement).value })"
              >
                <option value="">跟随全局</option>
                <option v-for="s in ROLE_SB" :key="'rs-' + s.v" :value="s.v">{{ s.label }}</option>
              </select>
            </div>
            <p v-if="roleTierNote" class="dim" data-testid="role-tier-note">当前实际生效：{{ roleTierNote }}（角色收紧中）</p>
            <p class="dim">
              实际档位取「全局档」与「角色档」里更严的那个；停用角色后回到全局档。
              审批收紧到「严格」时不弹确认框，危险命令直接拒——子代理与定时任务同样按它跑。
            </p>
            <div class="row acts">
              <button
                class="ghost solid"
                :disabled="busy"
                @click="saveRoleDef({ name: roleDetail!.name, description: roleDetail!.description, identity: roleDetail!.identity })"
              >
                保存定义
              </button>
            </div>

            <label class="fld">
              <span class="fld-lab">
                工作规则（AGENTS.md）{{ byteLength(agentsDraft) }} / {{ roleMax }} 字节
                <span v-if="agentsDirty" class="dirty">未保存</span>
                <span v-if="byteLength(agentsDraft) > roleMax" class="err-text">超出上限</span>
              </span>
              <textarea v-model="agentsDraft" class="inp mono" rows="8" placeholder="写这个角色的做事规程（逐字进系统提示，越短越省）"></textarea>
            </label>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy || !selRole" @click="saveAgents">保存工作规则</button>
              <button v-if="agentsDirty" class="ghost danger-text" @click="discardAgents">放弃修改</button>
            </div>

            <div class="row">
              <span class="lab-inline">改标识</span>
              <input v-model="roleIDDraft" class="inp mono grow" />
              <button class="ghost" :disabled="roleIDDraft.trim() === roleDetail.id || !roleIDDraft.trim()" @click="renameRole">改标识</button>
            </div>
            <p class="dim">改标识 = 角色目录改名（当前角色会跟着改）。</p>

            <!-- 技能挂载 -->
            <h3 class="h">技能挂载</h3>
            <div class="row">
              <span class="lab-inline">挂载方式</span>
              <div class="seg">
                <button class="seg-it" :class="{ on: !roleDetail.skills_set }" :disabled="roleSaving" data-tip="不写 skills 键 = 用默认池（全部库技能）" @click="useDefaultPool">默认池</button>
                <button class="seg-it" :class="{ on: roleDetail.skills_set }" :disabled="roleSaving" data-tip="写了 skills 键 = 只挂勾选的" @click="useReplacePool">替换</button>
              </div>
            </div>
            <label class="chk">
              <input type="checkbox" :checked="!!roleDetail.skills_inherit" :disabled="roleSaving" @change="toggleInherit(($event.target as HTMLInputElement).checked)" />
              <span>再并入默认池（替换之外额外挂上全部库技能）</span>
            </label>
            <p class="dim">角色私有技能（roles/&lt;id&gt;/skills/）只增不减；同名技能按目录顺序首个生效，重名会在日志里告警。</p>
            <div v-if="!roleDetail.skills_set" class="dim">当前为默认池，勾选任一项即切到「替换」。</div>
            <div class="m-list">
              <template v-for="[g, list] in skillsByRole" :key="g">
                <p class="dim">{{ groupLabel(g) }}</p>
                <div v-for="s in list" :key="(s.role || '') + '/' + s.name" class="m-item">
                  <label class="chk grow">
                    <input
                      type="checkbox"
                      :checked="mounted(roleDetail, s.name)"
                      :disabled="roleSaving"
                      @change="toggleMount(s.name, ($event.target as HTMLInputElement).checked)"
                    />
                    <span class="m-lab">{{ s.name }}</span>
                  </label>
                  <span v-if="s.description" class="dim grow">{{ s.description }}</span>
                  <button class="ghost" data-tip="看/改 SKILL.md 原文" @click="openSkill(s.name, s.role || '')">原文</button>
                  <button
                    v-if="!s.role"
                    class="ghost"
                    data-tip="导出为技能包(.zip,含 SKILL.md,可分享)"
                    @click="exportSkillPack(s.name)"
                  >
                    导出包
                  </button>
                  <button class="ghost" data-tip="改目录名 / 换归属库（会同步改角色挂载；目标同名会被拒）" @click="openMove(s.name, s.role || '')">
                    改名/移动
                  </button>
                  <button class="ghost danger-text" data-tip="删除技能（需确认）" @click="deleteSkill(s.name, s.role || '')">删除</button>
                </div>
              </template>
              <p v-if="!roleLib.length" class="dim">技能库为空：可在下方新建，或把技能放到 skills/&lt;名&gt;/SKILL.md。</p>
            </div>
            <div v-if="staleMounts.length" class="m-list">
              <p class="dim">已失效挂载（技能已不存在）：保存其它改动不受影响，建议清掉。</p>
              <div v-for="n in staleMounts" :key="'stale-' + n" class="m-item">
                <span class="m-lab">{{ n }}</span>
                <button class="ghost danger-text" :disabled="roleSaving" data-tip="从挂载清单里移除这个名字" @click="removeStaleMount(n)">移除</button>
              </div>
            </div>

            <!-- 工具排除(第九十一批):默认全给,勾上 = 不给(模型看不见也调不动) -->
            <h3 class="h">
              工具
              <button class="link" data-tip="默认全部工具可用;勾上即排除" @click="toggleTools">
                {{ toolsOpen ? '收起' : '配置' }}
              </button>
            </h3>
            <p class="dim">
              默认该角色可用全部工具。勾掉即排除：模型看不见也调不动(凭记忆调用会被显式拒绝)。
              <span v-if="excludedCount">已排除 {{ excludedCount }} 项。</span>
              <button v-if="excludedCount" class="ghost" :disabled="roleSaving" data-tip="清空排除清单，恢复全部工具" @click="restoreAllTools">全部恢复</button>
            </p>
            <div v-if="toolsOpen">
              <div v-if="toolsErr" class="serr">{{ toolsErr }}</div>
              <label class="fld">
                <span class="fld-lab">筛选（名字/描述）</span>
                <input v-model="toolQuery" class="inp" placeholder="shell / file / mcp_" />
              </label>
              <p v-if="toolsLoaded && !toolAll.length" class="dim">没有已注册的工具。</p>
              <div v-if="toolShown.length" class="m-list">
                <div v-for="t in toolShown" :key="'tool-' + t.name" class="m-item">
                  <label class="chk grow">
                    <input
                      type="checkbox"
                      :checked="toolExcluded(t.name)"
                      :disabled="roleSaving"
                      @change="toggleTool(t.name, ($event.target as HTMLInputElement).checked)"
                    />
                    <span class="m-lab mono">{{ t.name }}</span>
                  </label>
                  <span v-if="t.description" class="dim grow">{{ t.description }}</span>
                  <span v-if="toolExcluded(t.name)" class="dirty">已排除</span>
                </div>
              </div>
              <p v-else-if="toolsLoaded" class="dim">没有匹配的工具。</p>
              <div v-if="staleTools.length" class="m-list">
                <p class="dim">已排除但当前不存在（插件卸载/改名后残留）：不报错，建议清掉。</p>
                <div v-for="n in staleTools" :key="'stale-tool-' + n" class="m-item">
                  <span class="m-lab mono">{{ n }}</span>
                  <span class="dim grow">该工具当前不存在</span>
                  <button class="ghost danger-text" :disabled="roleSaving" data-tip="从排除清单里移除这个名字" @click="removeStaleTool(n)">清除</button>
                </div>
              </div>
            </div>

            <!-- 技能改名/跨库移动(第八十四批)：目录名即身份，frontmatter 的 name 由服务端同步改 -->
            <div v-if="skMove" class="add-form">
              <label class="fld">
                <span class="fld-lab">
                  改名 / 移动:{{ skMove.name }}
                  <span class="dim">(目录名即身份,会同步改挂载它的角色)</span>
                </span>
              </label>
              <label class="fld">
                <span class="fld-lab">新名称（小写字母/数字/._-）</span>
                <input v-model="skMove.toName" class="inp mono" :placeholder="skMove.name" />
              </label>
              <label class="fld">
                <span class="fld-lab">移到哪个库</span>
                <select v-model="skMove.toRole" class="sel">
                  <option value="">共享技能库（所有角色可见）</option>
                  <option v-for="r in roles" :key="'mv-' + r.id" :value="r.id">角色私有 · {{ r.name || r.id }}</option>
                </select>
              </label>
              <div class="form-acts">
                <button class="ghost solid" :disabled="busy || !skMoveDirty" @click="submitMove">确认改名/移动</button>
                <button class="ghost" @click="skMove = null">取消</button>
              </div>
            </div>

            <!-- 技能原文编辑（覆盖写） -->
            <div v-if="skEdit" class="add-form">
              <label class="fld">
                <span class="fld-lab">
                  编辑 {{ skEdit.name }} 的 SKILL.md（原文，保存即覆盖）
                  <span v-if="skDirty" class="dirty">未保存</span>
                </span>
                <textarea v-model="skEdit.content" class="inp mono" rows="10"></textarea>
              </label>
              <div class="form-acts">
                <button class="ghost solid" :disabled="busy" @click="saveSkill">保存技能</button>
                <button v-if="skDirty" class="ghost danger-text" @click="discardSkill">放弃修改</button>
                <button class="ghost" @click="skEdit = null">收起</button>
              </div>
            </div>
          </div>

          <!-- 新建技能（共享库 / 角色私有） -->
          <h3 class="h">
            技能库
            <button class="link" data-tip="新建一个技能" @click="showSkillNew = !showSkillNew">
              {{ showSkillNew ? '收起' : '＋ 新建技能' }}
            </button>
            <!-- 技能包(第一百零六批):只管**共享库**;角色私有技能随角色包导出 -->
            <button
              class="link"
              data-tip="导入一个 gah 技能包(.zip,内含单个 SKILL.md)"
              @click="showSkPackImport = !showSkPackImport"
            >
              {{ showSkPackImport ? '收起' : '＋ 导入技能包' }}
            </button>
          </h3>
          <div v-if="showSkPackImport" class="add-form">
            <p class="dim">
              包里只有那个技能的 <code>SKILL.md</code>(frontmatter + 正文)—— 导入端
              <strong>不执行任何脚本</strong>。同名已存在时后端会先拒，再问你要不要覆盖(旧份进回收站)。
            </p>
            <label class="fld">
              <span class="fld-lab">技能包文件(.zip)</span>
              <input
                class="inp"
                type="file"
                accept=".zip,application/zip"
                :disabled="skPackBusy"
                data-testid="skpack-file"
                @change="onSkPackPick"
              />
            </label>
            <label class="fld">
              <span class="fld-lab">导入为（可选：留空 = 用包里的名称）</span>
              <input v-model="skPackAs" class="inp mono" placeholder="weekly-report-copy" :disabled="skPackBusy" />
            </label>
            <p v-if="skPackBusy" class="dim">导入中…</p>
          </div>
          <div v-if="skErr" class="serr">{{ skErr }}</div>
          <div v-if="showSkillNew" class="add-form">
            <label class="fld">
              <span class="fld-lab">名称（小写字母/数字/._-）</span>
              <input v-model="skNew.name" class="inp mono" placeholder="weekly-report" />
            </label>
            <label class="fld">
              <span class="fld-lab">什么时候用（触发词，逗号分隔）</span>
              <input v-model="skNew.triggers" class="inp" placeholder="写周报, 汇总进度" />
            </label>
            <label class="fld">
              <span class="fld-lab">做什么（一句话）</span>
              <input v-model="skNew.description" class="inp" placeholder="按项目汇总本周进展与风险" />
            </label>
            <label class="fld">
              <span class="fld-lab">正文（步骤）</span>
              <textarea v-model="skNew.body" class="inp" rows="4" placeholder="1. 读本周提交…"></textarea>
            </label>
            <label class="fld">
              <span class="fld-lab">放哪里</span>
              <select v-model="skNew.role" class="sel">
                <option value="">共享技能库（所有角色可见）</option>
                <option v-for="r in roles" :key="r.id" :value="r.id">角色私有 · {{ r.name || r.id }}</option>
              </select>
            </label>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy || !skNew.name" @click="createSkill">创建技能</button>
            </div>
          </div>

          <!-- 回收站(第八十三批):删除的角色/技能都能从这里恢复 -->
          <h3 class="h">
            回收站
            <button class="link" data-tip="删除的角色与技能(移入 .trash,可恢复)" @click="toggleTrash">
              {{ trashOpen ? '收起' : '查看' }}
            </button>
          </h3>
          <p v-if="!trashOpen" class="dim">删除的角色与技能不会真删:移入 <span class="mono">.trash/</span>(各自保留最近 20 份),在这里可以恢复。</p>
          <div v-if="trashOpen">
            <div v-if="trashErr" class="serr">{{ trashErr }}</div>
            <template v-if="trashReady">
              <p v-if="!trashCount" class="dim">回收站是空的。</p>
              <template v-else>
                <div v-if="trashRoles.length" class="m-list">
                  <p class="dim">角色</p>
                  <div v-for="t in trashRoles" :key="'tr-' + t.name" class="m-item">
                    <span class="m-lab">{{ t.id || t.name }}</span>
                    <span v-if="!t.id" class="dim grow">目录名不合约定,无法还原</span>
                    <span v-else-if="t.deleted_at" class="dim grow">删除于 {{ fmtTrashTime(t.deleted_at) }}</span>
                    <button v-if="t.id" class="ghost" data-tip="移回 roles/&lt;id&gt;/(同名已存在会被拒)" @click="restoreTrash('role', t)">恢复</button>
                  </div>
                </div>
                <div v-if="trashSkills.length" class="m-list">
                  <p class="dim">技能</p>
                  <div v-for="t in trashSkills" :key="'ts-' + t.role + '/' + t.name" class="m-item">
                    <span class="m-lab">{{ t.skill || t.name }}</span>
                    <span class="dim grow">{{ t.role ? '角色私有 · ' + t.role : '共享技能库' }}<template v-if="t.deleted_at"> · 删除于 {{ fmtTrashTime(t.deleted_at) }}</template></span>
                    <span v-if="!t.skill" class="dim">目录名不合约定,无法还原</span>
                    <button v-else class="ghost" data-tip="移回原技能库(同名已存在会被拒)" @click="restoreTrash('skill', t)">恢复</button>
                  </div>
                </div>
              </template>
            </template>
          </div>
        </section>

        <!-- 会话历史 -->
        <section data-sec="history" class="sec">
          <h3 class="h">会话历史</h3>
          <div class="row">
            <span class="lab-inline">历史注入</span>
            <select v-model="histN" class="sel grow" @change="applyHistory">
              <option :value="0">全部上下文</option>
              <option :value="20">最近 20 条</option>
              <option :value="10">最近 10 条</option>
              <option :value="-1">不注入历史</option>
            </select>
          </div>
          <div class="row acts">
            <button class="ghost danger-text" :disabled="busy" @click="compactNow">压缩当前会话</button>
          </div>
          <p class="dim">压缩将最旧内容折叠为摘要(节省上下文),仅影响后续回合。</p>
        </section>

        <!-- Provider(W3:首启引导 + 预设 + 保存后连通性自检) -->
        <section data-sec="provider" class="sec">
          <h3 class="h">
            Provider
            <button class="link" data-tip="新增/编辑 LLM 端点" @click="toggleAdd">{{ showAdd ? '收起' : '＋ 新增' }}</button>
          </h3>

          <!-- 一个 Key 就能开始:没有 provider 时空状态本身就是入口 -->
          <div v-if="!providers.length" class="onboard">
            <p class="ob-lead">粘贴一个 API Key 就能开始,base_url 由预设填好。</p>
            <div class="ob-row">
              <button v-for="p in presets" :key="p.name" class="chip" :data-tip="p.note" @click="applyPreset(p)">
                {{ p.label }}
              </button>
            </div>
            <p class="dim">{{ onboardHint }}</p>
          </div>

          <div v-if="showAdd" class="add-form">
            <label class="fld">
              <span class="fld-lab">名称</span>
              <input v-model="pf.name" class="inp" placeholder="如 deepseek" />
            </label>
            <label class="fld">
              <span class="fld-lab">base_url</span>
              <input v-model="pf.base_url" class="inp mono" placeholder="https://api.deepseek.com/v1" />
            </label>
            <label class="fld">
              <span class="fld-lab">api_key</span>
              <input
                ref="keyInput"
                v-model="pf.api_key"
                class="inp mono"
                type="password"
                autocomplete="off"
                :placeholder="applied?.key_optional ? '本地端点留空即可' : 'sk-…'"
              />
            </label>
            <label class="fld">
              <span class="fld-lab">默认模型(可选)</span>
              <input v-model="pf.model" class="inp mono" placeholder="留空则在保存后从模型下拉里选" />
            </label>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy" @click="addProvider">保存 Provider</button>
            </div>
          </div>

          <!-- 连通性自检:保存后自动跑;失败给人话 + 原始报错 + 重试 -->
          <div v-if="probe" class="probe" :class="probe.ok ? 'ok' : 'bad'">
            <span>{{ probe.text }}</span>
            <span v-if="probe.raw" class="probe-raw mono">{{ probe.raw }}</span>
            <button v-if="!probe.ok" class="link" :disabled="busy" @click="reprobe">重新自检</button>
          </div>

          <div class="plist">
            <div v-for="p in providers" :key="p.Name" class="prow" :class="{ active: p.Active }">
              <div class="pmain">
                <span class="pname">{{ p.Name }}</span>
                <span v-if="p.Active" class="pbadge">活跃</span>
                <span class="psub mono">{{ p.BaseURL.replace(/^https?:\/\//, '') }}</span>
                <span class="psub mono">{{ p.Model }}</span>
              </div>
              <div class="pops">
                <button v-if="!p.Active" class="ghost" data-tip="切换为活跃" @click="doProviderUse(p.Name)">启用</button>
                <button class="ghost danger-text" data-tip="删除该 Provider(不可恢复)" @click="deleteProvider(p)">删除</button>
              </div>
            </div>
            <p v-if="!providers.length" class="dim">还没有配置 Provider:点上方「＋ 新增」或直接选一个预设</p>
          </div>
        </section>

        <!-- 数据备份(M18) -->
        <section data-sec="backup" class="sec">
          <h3 class="h">数据备份</h3>
          <div class="row acts">
            <button class="ghost" :disabled="busy" @click="doBackupNow()">立即备份</button>
            <button
              v-if="backups.length"
              class="ghost danger-text"
              :disabled="busy"
              :data-tip="'恢复 ' + backups[0].Name"
              @click="doBackupRestore()"
            >
              恢复最新备份
            </button>
          </div>
          <p v-if="backups.length" class="dim">最近备份:{{ backups[0].Name }} · {{ fmtSize(backups[0].Size) }}
            <span v-if="backups.length > 1">(共 {{ backups.length }} 份)</span></p>
          <p v-else class="dim">暂无备份(/backup 或立即备份创建第一份)</p>
          <p v-if="backupMsg" class="dim ok">{{ backupMsg }}</p>
        </section>

        <!-- 定时计划(NOND-W4,host-schedule 未装配时整段隐藏) -->
        <section v-if="schedReady" data-sec="schedule" class="sec">
          <h3 class="h">
            计划
            <button class="link" data-tip="新增定时计划" @click="showSchedAdd = !showSchedAdd">
              {{ showSchedAdd ? '收起' : '＋ 新增' }}
            </button>
          </h3>
          <!-- 无人值守行为明示(方案 W4 要求:计划详情里说清楚会发生什么) -->
          <p class="dim">到点自动把描述发给模型跑一轮(走与手动输入完全相同的回合入口)。无人值守运行:需审批的动作一律拒绝(没人在场回答确认),沙箱档位沿用当前设置。</p>
          <div v-if="schedErr" class="serr">{{ schedErr }}</div>

          <div v-if="showSchedAdd" class="add-form">
            <label class="fld">
              <span class="fld-lab">名称(可选,留空取描述开头)</span>
              <input v-model="sf.name" class="inp" placeholder="每日对账" />
            </label>
            <label class="fld">
              <span class="fld-lab">cron 表达式(分 时 日 月 周)</span>
              <input v-model="sf.cron" class="inp mono" placeholder="0 8 * * *" />
            </label>
            <label class="fld">
              <span class="fld-lab">到点做什么(作为输入发给模型)</span>
              <textarea v-model="sf.prompt" class="inp" rows="2" placeholder="把昨天的订单导出成对账表"></textarea>
            </label>
            <p class="dim">每天 8 点 = 0 8 * * *;每周一 9 点 = 0 9 * * 1;每月 1 号 = 0 0 1 * *</p>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy" @click="addSchedule">保存计划</button>
            </div>
          </div>

          <div class="plist">
            <div v-for="s in schedules" :key="s.id" class="prow scrow" :class="{ off: !s.enabled }">
              <div class="pmain">
                <span class="sname">
                  {{ s.name }}
                  <span class="sstate" :class="'ss-' + (s.last_status || 'none')">{{ statusLabel(s.last_status) }}</span>
                </span>
                <span class="psub mono">{{ s.cron }}</span>
                <span class="psub">下次:{{ s.enabled ? fmtNextRun(s.next_run) : '已停用' }}</span>
                <span v-if="s.last_run_at" class="psub">上次:{{ fmtAbs(s.last_run_at) }}</span>
                <span v-if="s.last_status === 'failed' && s.last_error" class="psub err-text">{{ s.last_error }}</span>
              </div>
              <div class="sacts">
                <button class="ghost" data-tip="立即跑一次" :disabled="!s.enabled" @click="doScheduleRun(s.id)">运行</button>
                <button class="ghost" data-tip="启用或停用" @click="toggleSchedule(s)">{{ s.enabled ? '停用' : '启用' }}</button>
                <button class="ghost danger-text" data-tip="删除计划(需确认)" @click="deleteSchedule(s)">删除</button>
              </div>
            </div>
            <p v-if="!schedules.length" class="dim">还没有计划:点上方「＋ 新增」写一条,如「每天 8 点整理昨日订单」。</p>
          </div>
        </section>

        <!-- MCP server 配置(NOND-M1 第 2/3 步:配置 + 状态 + 保存即重载) -->
        <section data-sec="mcp" class="sec">
          <h3 class="h">
            MCP server
            <button class="link" data-tip="新增一个 MCP server" @click="mcpAdd">＋ 添加</button>
            <span v-if="mcpDirty" class="dirty">未保存</span>
          </h3>
          <p class="dim">
            MCP 让模型用上外部工具(记忆、代码图谱、数据库等)。每行一条启动命令,与你终端里输入的一致。
            <strong>全量注册</strong>适合工具少的 server;<strong>按需检索</strong>适合工具多的 server(工具不进每轮上下文,模型先 mcp_search 查、再 mcp_call 调)。
          </p>
          <div v-if="mcpErr" class="serr">{{ mcpErr }}</div>
          <p v-if="mcpMsg" class="dim ok">{{ mcpMsg }}</p>
          <div v-for="n in mcpNotices" :key="n" class="serr">{{ n }}</div>

          <div class="plist">
            <div v-for="(r, i) in mcpDrafts" :key="'mcp' + i" class="prow scrow" :class="{ off: !r.enabled }">
              <div class="pmain">
                <label class="fld">
                  <span class="fld-lab">名称(决定工具前缀 mcp_&lt;名称&gt;_*,留空则不加前缀)</span>
                  <input v-model="r.name" class="inp mono" placeholder="deja" />
                </label>
                <label class="fld">
                  <span class="fld-lab">启动命令</span>
                  <input v-model="r.command" class="inp mono" placeholder="npx -y @modelcontextprotocol/server-memory" />
                </label>
                <div class="mrow">
                  <label class="chk">
                    <input v-model="r.enabled" type="checkbox" />
                    <span>启用</span>
                  </label>
                  <select v-model="r.mode" class="sel">
                    <option :value="MCP_MODE_DIRECT">全量注册</option>
                    <option :value="MCP_MODE_SEARCH">按需检索</option>
                  </select>
                </div>
                <span class="psub">{{ modeHint(r.mode) }}</span>
              </div>
              <div class="sacts">
                <span class="sstate" :class="mcpRowStateClass(r)">{{ mcpRowState(r) }}</span>
                <button class="ghost danger-text" data-tip="从配置里移除" @click="mcpRemove(i)">删除</button>
              </div>
            </div>

            <div v-for="s in mcpEnvRows" :key="'env' + s.name" class="prow scrow">
              <div class="pmain">
                <span class="sname">
                  {{ s.name || '(未命名)' }}
                  <span class="sstate ss-none">{{ sourceLabel(s.source) }}</span>
                </span>
                <span class="psub mono">{{ fmtCommand(s) }}</span>
                <span class="psub">{{ modeLabel(s.mode) }} · {{ serverStateLabel(s) }}</span>
              </div>
              <div class="sacts">
                <span class="dim">来自环境变量(env.sh),改后需重启 gah</span>
              </div>
            </div>

            <p v-if="!mcpDrafts.length && !mcpEnvRows.length" class="dim">
              还没有 MCP server:点上方「＋ 添加」写一条启动命令,保存后模型立刻能用上它的工具。
            </p>
          </div>

          <div class="row acts">
            <button class="ghost solid" :disabled="busy || !mcpDirty" @click="saveMcp">保存并重载</button>
            <button class="ghost" :disabled="busy || !mcpDirty" @click="discardMcp">放弃修改</button>
          </div>
          <p v-if="mcpView" class="dim">
            配置文件:{{ mcpView.path }}
            <span v-if="!mcpView.reload_available">(当前构建不能热重载,改完需重启 gah)</span>
          </p>
        </section>

        <!-- 插件与指令 -->
        <section data-sec="plugin" class="sec">
          <h3 class="h">
            插件
            <span class="h-sub">{{ pluginShown.length }}/{{ plugins.length }}</span>
          </h3>
          <!-- 插件安装(2026-10-03):CLI 的安装内核接进面板。两条来源都支持 ——
               第三方仓库与**本地自己写的目录**(不需要先 git init + push)。
               每次安装/卸载/登记都走二次确认;审批档为「严格」时服务端直接拒。 -->
          <div class="row install-row">
            <span class="lab-inline">安装插件</span>
            <input
              v-model="installSpec"
              class="inp grow"
              placeholder="仓库地址(git@…/@版本)或本地目录绝对路径"
              :disabled="installBusy"
              @keyup.enter="doInstall()"
            />
            <button v-if="isDesktop" class="ghost" :disabled="installBusy" data-tip="选择本地插件源码目录" @click="pickInstallDir()">
              浏览…
            </button>
            <button class="ghost solid" :disabled="installBusy || !installSpec.trim()" @click="doInstall()">
              {{ installBusy ? '处理中…' : '安装' }}
            </button>
          </div>
          <p v-if="installOk" class="dim">{{ installOk }}</p>
          <p v-if="installWarn" class="warn-line">{{ installWarn }}</p>
          <p v-if="installErr" class="err-line">{{ installErr }}</p>
          <details v-if="installRows.length" class="dim">
            <summary>已装外部插件({{ installRows.length }};白名单{{ installRows[0]?.enforced ? '强制' : '未启用' }})</summary>
            <ul class="install-list">
              <li v-for="r in installRows" :key="r.id" class="irow">
                <span class="pname">{{ r.id }}</span>
                <span class="psub">{{ r.protocol || 'bridge' }} · {{ r.binary }} ·
                  {{ r.trusted ? '白名单已登记' : (r.loadable ? '未登记(会被拒)' : '二进制缺失') }}
                  <template v-if="r.audit_source"> · {{ r.audit_source }} @ {{ (r.audit_time || '').slice(0, 16) }}</template>
                </span>
                <span class="iact">
                  <button v-if="!r.trusted && r.loadable" class="ghost" @click="doTrustPlugin(r.binary)">登记</button>
                  <button v-if="r.trusted" class="ghost" @click="doUntrustPlugin(r.binary)">撤销登记</button>
                  <button class="ghost ro" @click="doUninstallPlugin(r.id)">卸载</button>
                </span>
              </li>
            </ul>
          </details>
          <!-- UI 插件(2026-10-03):信任模型 + 摘要 + **重扫入口** + 失败原因。
               重扫解决的是「装了插件但没生效、只能靠刷新页面」;失败清单解决的是
               「没生效」与「没被扫到」在界面上分不出来 —— 这两者的处置完全不同。 -->
          <div class="row ui-plugin-row">
            <span class="lab-inline">UI 插件</span>
            <button class="ghost" :disabled="uiScanBusy" @click="rescanUIPlugins()">
              {{ uiScanBusy ? '扫描中…' : '重新扫描' }}
            </button>
            <span class="dim">
              装好后点这里重扫(不必刷新页面);命令行等价物是
              <code>gah -install-ui &lt;repo|目录&gt;</code>
            </span>
          </div>
          <p v-if="uiPluginTrustNote" class="dim">UI 插件(ui-plugins):{{ uiPluginTrustNote }}</p>
          <p v-if="uiPluginErrors.length" class="err-line">
            {{ uiPluginErrors.length }} 个槽位加载失败:
            <template v-for="(e, i) in uiPluginErrors" :key="e.id + e.slot">
              {{ i ? ';' : '' }}{{ e.id }}·{{ e.slot }} — {{ e.reason }}
            </template>
          </p>
          <!-- 产物完整性提示(R10 ⑤-3):sha256 覆盖范围与降级原因如实显示;值供人比对,
               本面板不做"通过/不通过"判断(同源页面自证不构成安全边界) -->
          <details v-if="uiPluginDigests.length" class="dim">
            <summary>产物校验值(sha256,{{ uiPluginDigests.length }} 个 UI 插件;可与发布方公布的比对)</summary>
            <ul class="digests">
              <li v-for="d in uiPluginDigests" :key="d.id" :title="d.sha256 || '无摘要'">{{ digestLine(d) }}</li>
            </ul>
          </details>
          <!-- data-testid:本段新增了「安装插件」的输入框,`[data-sec="plugin"] input`
               已经**不唯一**(布局护栏按它 fill,装到安装框上,筛选就失效)。给筛选框一个
               稳定锚点,别让"谁排在前面"决定语义。 -->
          <div v-if="plugins.length > PLUGIN_CAP" class="row">
            <input
              v-model="pluginFilter"
              class="inp grow"
              data-testid="plugin-filter"
              placeholder="筛选插件 ID / 类型 / 状态"
            />
          </div>
          <div class="plist">
            <div v-for="p in pluginShown" :key="p.ID" class="prow">
              <div class="pmain">
                <span class="pname">{{ p.ID }}</span>
                <span class="psub">{{ p.Type }} · {{ p.State }} · {{ manageMeta(p).label }}</span>
                <!-- 被拒(白名单不符等):必须显示原因与补救办法 ——
                     否则表现只是「插件不见了」,用户无从判断是没装还是被拦。 -->
                <span v-if="p.rejected" class="pwhy" :title="p.rejected">被拒绝:{{ p.rejected }}</span>
              </div>
              <!-- 外部化/场景专用:只读标记,web 侧禁用启停 -->
              <button
                v-if="p.manage === 'external' || p.manage === 'scenario'"
                class="ghost"
                :class="{ ro: true }"
                :data-tip="manageMeta(p).tip"
                disabled
                @click="pluginToggle(p)"
              >
                只读
              </button>
              <button
                v-else
                class="ghost"
                :class="{ 'danger-text': p.State === 'loaded' }"
                :data-tip="p.State === 'loaded' ? '卸载(确认)' : '加载启用'"
                @click="pluginToggle(p)"
              >
                {{ p.State === 'loaded' ? '卸载' : '启用' }}
              </button>
            </div>
            <p v-if="!plugins.length" class="dim">插件管理不可用</p>
            <p v-else-if="!pluginHits.length" class="dim">没有匹配的插件</p>
            <!-- 长列表折叠(前 5 条之外的默认不渲染);≤5 条时 pluginHidden 恒为 0,不出现按钮 -->
            <button v-if="pluginHidden > 0" class="ghost more" @click="pluginExpanded = true">
              展开全部(还有 {{ pluginHidden }} 个)
            </button>
            <button
              v-else-if="pluginExpanded && pluginHits.length > PLUGIN_CAP"
              class="ghost more"
              @click="pluginExpanded = false"
            >
              收起
            </button>
          </div>
          <div class="row acts">
            <button class="ghost" @click="doReload">重载指令文件</button>
          </div>
        </section>

        <!-- 桌面壳专属:升级入口(浏览器直连时整段隐藏)。此前升级只在托盘菜单里,
             菜单弹不出来就等于没有入口,故补到界面上。 -->
        <section v-if="isDesktop" data-sec="about" class="sec">
          <h3 class="h">关于 gah</h3>
          <p class="dim" data-testid="about-autostart">
            开机自启:{{ autoState === 'on' ? '已启用' : autoState === 'off' ? '未启用' : '未知' }}
          </p>
          <p class="dim">升级入口固定在面板底部(不随内容滚动):点「检查更新」即查 GitHub Release 上的新版本,有更新会自动下载安装并重启。</p>
        </section>

        <!-- v2 扩展点:设置面板区段(插件注入,每插件一节) -->
        <section v-for="s in panelSections" :key="s.key" :data-sec="'ext-' + s.key" class="sec">
          <component :is="s.component" />
        </section>
        </div>
      </div>

      <!-- 固定底栏(仅桌面版):版本 + 检查更新常驻可见 —— 升级入口原本埋在面板最底,
           而插件列表一长就得滚到底才能点到它。 -->
      <footer v-if="isDesktop" class="foot">
        <div class="foot-row">
          <span class="mono ver">v{{ props.state.version || 'dev' }}</span>
          <button class="ghost" :disabled="updBusy" @click="doCheckUpdate()">
            {{ updBusy ? '检查中…' : '检查更新' }}
          </button>
          <!-- 有更新才出现:检查只查不装,升级必须由用户点这一下并再确认一次 -->
          <button v-if="updAvailable" class="ghost solid" :disabled="updBusy" @click="doInstallUpdate()">
            升级到 {{ updAvailable }}
          </button>
        </div>
        <p class="dim foot-msg" :class="{ ok: updOk }">
          {{ updMsg || '检查新版本;发现更新后由你确认再下载安装' }}
        </p>
      </footer>
      </aside>
    </div>
  </Transition>
</template>

<style scoped>
/* —— W3 首启引导:空状态即入口 —— */
.onboard {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 10px;
  padding: 10px;
  background: var(--accent-soft);
  border: 1px solid var(--sel-border);
  border-radius: var(--r-card);
}
.ob-lead {
  margin: 0;
  font-size: 13px;
  color: var(--fg);
}
.ob-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  border: 1px solid var(--line-strong);
  background: var(--bg);
  color: var(--fg);
  border-radius: var(--r-input);
  font-size: 12px;
  padding: 4px 8px;
  cursor: pointer;
}
.chip:hover {
  border-color: var(--accent);
  color: var(--accent);
}
/* 表单字段:标签在输入框上方(不以 placeholder 充当标签) */
.fld {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.fld-lab {
  font-size: 11px;
  color: var(--fg-dim);
}
.form-acts {
  display: flex;
  gap: 8px;
  margin-top: 2px;
}
/* 连通性自检结果 */
.probe {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 10px;
  padding: 8px 10px;
  border-radius: var(--r-card);
  font-size: 12px;
  line-height: 1.5;
}
.probe.ok {
  color: var(--ok);
  background: var(--ok-soft);
  border: 1px solid var(--ok-line);
}
.probe.bad {
  color: var(--err);
  background: var(--err-soft);
  border: 1px solid var(--err-line);
}
.probe-raw {
  color: var(--fg-faint);
  word-break: break-all;
}
/* —— MCP server(NOND-M1)—— */
.mrow {
  display: flex;
  align-items: center;
  gap: 10px;
}
.chk {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--fg-dim);
}
.chk input {
  accent-color: var(--accent);
}
/* —— 定时计划(NOND-W4) —— */
/* 警示(不是错误):文件已经写成功了,只是本轮提示还没跟上 —— 用红框会把"已保存"说成"失败"。 */
.swarn {
  color: var(--tool-strong);
  background: var(--tool-soft);
  border: 1px solid var(--tool-line);
  border-radius: 6px;
  font-size: 12px;
  padding: 6px 8px;
  margin-bottom: 8px;
  word-break: break-word;
}
/* 未保存草稿标记(amber 语义色 = 不是错,但需要你处理;与 .swarn 同色系) */
.dirty {
  margin-left: 6px;
  padding: 1px 6px;
  border-radius: 999px;
  border: 1px solid var(--tool-line);
  background: var(--tool-soft);
  color: var(--tool-strong);
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0;
}
.serr {
  color: var(--err);
  background: var(--err-soft);
  border: 1px solid var(--err-line);
  border-radius: 6px;
  font-size: 12px;
  padding: 6px 8px;
  margin-bottom: 8px;
  word-break: break-word;
}
/* —— 跨会话记忆段(第一百一十批)——
   记忆正文是**用户自己写的整句话**,长度不可控(既有技能行会碰到的长文本撑出横向滚动,
   见第七十九批的实测)。所以不用 .m-lab 的单行省略(删之前看不清自己删的是什么),
   改成最多两行 + 全文进 data-tip,并显式 overflow-wrap:anywhere(长 URL/路径不撑破)。 */
.mem-list {
  max-height: 220px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border: 1px solid var(--line-faint);
  border-radius: var(--r-input);
  background: var(--bg);
  padding: 4px;
  margin-bottom: 8px;
}
.mem-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 8px;
  border-radius: 6px;
  font-size: 12px;
}
.mem-item:hover {
  background: var(--bg3);
}
.mem-txt {
  flex: 1;
  min-width: 0;
  color: var(--fg);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  overflow-wrap: anywhere;
}
.mem-src {
  margin-bottom: 8px;
}
/* .prow.scrow 提高特异性:.prow 的 align-items:center 在样式表里更靠后,会盖掉这里的 stretch ——
   而 scrow 的堆叠布局全靠 stretch(cross 轴不被 stretch 时,flex item 宽 = 内容 min-content,
   长计划名/长错误文本会把面板撑出横向滚动)。 */
.prow.scrow {
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
}
.scrow.off .sname,
.scrow.off .psub {
  color: var(--fg-faint);
}
.sname {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--fg);
  /* 计划名可能是一整段无空格文本:不断行就会溢出面板,把设置页拉出横向滚动条。 */
  overflow-wrap: anywhere;
}
.sstate {
  font-size: 10px;
  border-radius: 999px;
  padding: 0 6px;
  line-height: 1.5;
  border: 1px solid var(--line);
  color: var(--fg-faint);
}
.sstate.ss-ok {
  color: var(--ok);
  border-color: var(--ok-line);
  background: var(--ok-soft);
}
.sstate.ss-failed {
  color: var(--err);
  border-color: var(--err-line);
  background: var(--err-soft);
}
.sstate.ss-skipped {
  color: var(--warn, var(--fg-dim));
  border-color: var(--line-strong);
}
.sacts {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.err-text {
  color: var(--err);
}
textarea.inp {
  resize: vertical;
  font-family: inherit;
  line-height: 1.5;
}

/* 显隐动效统一在 style.css 的 pane 过渡里(入场滑入 + 退场反向);此处不再写 animation,
   否则与过渡同写 opacity/transform 会互抢(动画优先级更高 ⇒ 退场仍硬切)。 */
.mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  z-index: 70;
}
.panel {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(640px, calc(100vw - 32px));
  background: var(--bg);
  border-left: 1px solid var(--line);
  box-shadow: var(--shadow-dialog);
  display: flex;
  flex-direction: column;
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid var(--line);
}
.title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
}
.x {
  background: none;
  border: none;
  color: var(--fg-faint);
  cursor: pointer;
  font-size: 13px;
  padding: 2px 6px;
  border-radius: 6px;
}
.x:hover {
  color: var(--fg);
  background: var(--bg3);
}
.body {
  flex: 1;
  min-width: 0; /* 与左导航同排:不收缩就会被长 token 顶宽(面板里禁横向滚动条) */
  overflow-y: auto;
  padding: 6px 16px 20px;
}
/* 段导航:点击跳转 + 滚动高亮。面板宽时竖排左栏,窄窗口退化成顶部横向芯片条 */
.cols {
  flex: 1;
  min-height: 0;
  display: flex;
}
.nav {
  width: 148px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 8px 14px;
  overflow-y: auto;
  border-right: 1px solid var(--line-faint);
}
.nav-it {
  text-align: left;
  border: none;
  background: none;
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 13px;
  padding: 6px 10px;
  border-radius: var(--r-input);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.nav-it:hover {
  background: var(--bg3);
  color: var(--fg);
}
.nav-it.on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 500;
}
@media (max-width: 719px) {
  .cols {
    flex-direction: column;
  }
  .nav {
    width: auto;
    flex-direction: row;
    gap: 4px;
    padding: 6px 8px;
    overflow-y: hidden;
    overflow-x: auto;
    border-right: none;
    border-bottom: 1px solid var(--line-faint);
  }
}
/* 固定底栏(桌面版):升级入口常驻可见,不随内容滚动 */
.foot {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 16px;
  border-top: 1px solid var(--line);
  background: var(--bg2);
}
.foot-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.foot-msg {
  margin: 0;
}
.foot .ver {
  font-size: 12px;
  color: var(--fg-faint);
}
.err {
  color: var(--err);
  background: var(--err-soft);
  border: 1px solid var(--err-line);
  border-radius: 6px;
  font-size: 12px;
  padding: 6px 10px;
  margin: 8px 16px 0;
  word-break: break-word;
}
.info {
  color: var(--ok);
  font-size: 12px;
  padding: 6px 10px;
  margin: 8px 16px 0;
}
.sec {
  padding: 14px 0;
  border-bottom: 1px solid var(--line-faint);
  scroll-margin-top: 8px; /* 锚点跳转后标题不离容器上沿 */
}
.sec:last-child {
  border-bottom: none;
}
.h-sub {
  font-size: 11px;
  font-weight: 400;
  letter-spacing: 0;
  text-transform: none;
}
.ghost.more {
  align-self: stretch;
  margin-top: 2px;
}
.h {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 12px;
  font-weight: 600;
  color: var(--fg-faint);
  letter-spacing: 0.04em;
  text-transform: uppercase;
  margin: 0 0 10px;
}
.lab {
  display: block;
  font-size: 12px;
  color: var(--fg-dim);
  margin-bottom: 4px;
}
.lab-inline {
  font-size: 13px;
  color: var(--fg);
  min-width: 56px;
}
.row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.row:last-child {
  margin-bottom: 0;
}
.row.acts {
  margin-top: 10px;
}
.sel {
  flex: 1;
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg);
  color: var(--fg);
  font-size: 13px;
  padding: 6px 8px;
  outline: none;
}
.sel:focus {
  border-color: var(--accent);
}
.sel.grow {
  flex: 1;
}
.seg {
  display: inline-flex;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  overflow: hidden;
}
.seg-it {
  border: none;
  background: none;
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12px;
  padding: 5px 12px;
  transition: background 0.15s ease, color 0.15s ease;
}
.seg-it:hover {
  background: var(--bg3);
}
.seg-it.on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 500;
}
.ghost {
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: none;
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12px;
  padding: 5px 12px;
  transition: border-color 0.15s ease, color 0.15s ease, background 0.15s ease;
}
.ghost:hover {
  border-color: var(--accent);
  color: var(--accent);
  background: var(--accent-soft);
}
.ghost.ro {
  color: var(--fg-faint);
  cursor: default;
}
.ghost.ro:hover {
  border-color: var(--line);
  color: var(--fg-faint);
  background: none;
}
.ghost.solid {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--fg-on-accent);
}
.ghost.solid:hover {
  background: var(--accent-hover);
}
.danger-text:hover {
  border-color: var(--err);
  color: var(--err);
  background: var(--err-soft);
}
.link {
  background: none;
  border: none;
  color: var(--accent);
  cursor: pointer;
  font-size: 12px;
  padding: 0;
}
.add-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 12px;
  padding: 10px;
  background: var(--bg2);
  border-radius: var(--r-card);
}
.inp {
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg);
  color: var(--fg);
  font-size: 13px;
  padding: 6px 8px;
  outline: none;
}
.inp.grow {
  flex: 1;
  min-width: 0; /* row 内与标签同排时伸展且可收缩(短 placeholder 完整可见) */
}
.inp:focus {
  border-color: var(--accent);
}
.plist {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.prow {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 8px 10px;
}
.prow.active {
  border-color: var(--accent);
  background: var(--accent-soft);
}
.pmain {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.pname {
  font-size: 13px;
  color: var(--fg);
  font-weight: 500;
  /* 插件 ID / 用户自定义的 provider 名同样是单段无空格文本:与 .dim 同理，不断行就溢出面板。 */
  overflow-wrap: anywhere;
}
.pbadge {
  align-self: flex-start;
  font-size: 10px;
  color: var(--accent);
  background: var(--bg);
  border: 1px solid var(--accent);
  border-radius: 999px;
  padding: 0 6px;
  line-height: 1.5;
}
.psub {
  font-size: 11px;
  color: var(--fg-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pops {
  display: inline-flex;
  gap: 6px;
  flex-shrink: 0;
}
.dim {
  font-size: 12px;
  color: var(--fg-faint);
  margin: 4px 0;
  /* 这句里的路径/URL/命令常是一整段无空格文本(如 mcp 配置文件路径):不断行就溢出面板,
     而 .body 的 overflow-y:auto 会把 overflow-x 也算成 auto ⇒ 设置页多出一条横向滚动条。 */
  overflow-wrap: anywhere;
}
/* 「当前生效」行:当前**状态**而不是说明文字 ⇒ 强调色 + 加重 + 左侧强调条。
   此前它与一排 .dim 提示同级(灰小字),用户完全扫不到
   (2026-10-03:「这一行颜色应为强调色,更显眼」)。 */
.cur-line {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 8px;
  margin: 6px 0 10px;
  padding: 6px 10px;
  border-left: 3px solid var(--accent);
  border-radius: var(--r-input);
  background: var(--accent-soft);
  font-size: 13px;
  overflow-wrap: anywhere;
}
.cur-key {
  color: var(--fg-dim);
  font-weight: 600;
}
.cur-val {
  color: var(--accent);
  font-weight: 600;
}
/* 产物校验值列表(R10 ⑤-3):等宽字体便于逐字符比对;折行不裁剪(哈希截断会误导) */
/* 安装期的长期影响提示(补依赖):警告色而非错误色 —— 装**成功了**,但供应链面变了。 */
.warn-line {
  color: var(--tool);
  font-size: 12px;
  margin: 4px 0;
}
/* 插件安装行与已装列表。
   **刻意不复用 .plist**:那个类被布局护栏的「插件列表默认只列 5 条」用例按选择器数行,
   两张表共用一个类会让护栏数到错的行数(实测 43 ≠ 1)。选择器与语义都要各自成立。 */
.install-list {
  list-style: none;
  margin: 4px 0 8px;
  padding: 0;
}
.irow {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  padding: 2px 0;
}
.iact {
  display: inline-flex;
  gap: 6px;
  margin-left: auto;
}
/* 被拒插件的原因行:错误语义色,但只到"说明"的分量(不抢主体) */
.pwhy {
  display: block;
  color: var(--err);
  font-size: 11px;
  margin-top: 2px;
  word-break: break-word;
}
/* UI 插件段的失败清单:与工具结果里的错误块同一套语义色(不用第二个色相) */
.err-line {
  color: var(--err);
  font-size: 12px;
  margin: 4px 0;
  word-break: break-word;
}
.ui-plugin-row code {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--fg-dim);
}
.digests {
  margin: 4px 0 0;
  padding-left: 18px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.digests li {
  font-family: var(--mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  font-size: 11px;
  overflow-wrap: anywhere;
}
/* 模型筛选列表:输入框下匹配项,超出滚动;当前模型高亮 */
.m-list {
  max-height: 200px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border: 1px solid var(--line-faint);
  border-radius: var(--r-input);
  background: var(--bg);
  padding: 4px;
}
.m-item {
  padding: 5px 8px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 12px;
  color: var(--fg-dim);
}
.m-item:hover {
  background: var(--bg3);
  color: var(--fg);
}
.m-item.on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 500;
}
.m-lab {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
