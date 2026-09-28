<script setup lang="ts">
// 可视化设置抽屉(状态栏 ⚙ 入口;App 持有 open)。
// 分组:模型/推理(thinking·sandbox)/历史与压缩/Provider/插件与指令。
// 破坏性动作(删 provider、卸载插件、压缩)经全局确认条(askConfirm)。
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import { autostartState, checkUpdate, isDesktop, updateState, type UpdateSnapshot } from '../desktop'
import { currentModelValue, modelOptionValue, withCurrentModel } from '../modelsel'
import { settingSections } from '../registry'
import { uiPluginDigests, uiPluginTrustNote } from '../plugins'
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
import type { AskConfirm, McpServer, McpView, PluginInfo, ProviderInfo, ProviderModelGroup, RoleSpec, Schedule, SkillInfo, StateView } from '../types'

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

// doCheckUpdate 桌面版检查更新:壳侧走同一条 checkForUpdates(与托盘菜单同一实现,
// 结果回传到这里展示)。有更新时壳会自动下载安装并重启。
async function doCheckUpdate() {
  if (!isDesktop) {
    updOk.value = false
    updMsg.value = '当前环境不支持检查更新(需桌面版)'
    return
  }
  updBusy.value = true
  updMsg.value = '正在检查…'
  updOk.value = false
  try {
    const r = await checkUpdate()
    updOk.value = r?.status === 'upToDate' || r?.status === 'installed'
    updMsg.value = String(r?.message ?? '检查完成')
  } catch (e) {
    updOk.value = false
    updMsg.value = '检查更新失败:' + (e instanceof Error ? e.message : String(e))
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
function jumpTo(key: string, smooth = true): void {
  const el = bodyEl.value?.querySelector<HTMLElement>(`section[data-sec="${key}"]`)
  if (!el) return // 该段在当前装配下不渲染(如未装配 host-schedule):静默跳过
  activeSec.value = key
  el.scrollIntoView({ block: 'start', behavior: smooth ? 'smooth' : 'auto' })
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

async function load(): Promise<void> {
  err.value = ''
  try {
    // 模型聚合/插件/provider 任一失败降级:非核心(如未装配 MultiProviderService → 501)
    const [m, pl, pr] = await Promise.allSettled([api.models(), api.plugins(), api.providers()])
    await loadBackups() // M18 备份列表(未装配降级静默)
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
  if (hit) return hit.label
  const m = (props.state.model ?? '').trim()
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
const roleIDDraft = ref('')
const showRoleNew = ref(false)
const rf = ref({ id: '', name: '', identity: '', description: '', exclude_global: false })
const skNew = ref({ name: '', description: '', triggers: '', body: '', role: '' })
const showSkillNew = ref(false)
const skErr = ref('')
const skEdit = ref<{ name: string; role: string; content: string } | null>(null)

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
// roleIsCurrent 当前角色高亮(切换是改状态但可一键切回,故不进确认弹层)
function roleIsCurrent(r: RoleSpec): boolean {
  return roleCurrent.value === r.id
}

// selectRole 展开某角色的编辑区(列表不带正文,展开时才拉详情)/ 再点一次收起
async function selectRole(r: RoleSpec): Promise<void> {
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
    roleIDDraft.value = d.id
    skEdit.value = null
    selRole.value = r.id
  } catch (e) {
    roleErr.value = (e as Error).message
  }
}

// saveRoleDef 提交角色定义的部分更新(只传改动的字段;更新后同步列表与详情)
async function saveRoleDef(patch: Parameters<typeof api.roleUpdate>[1]): Promise<void> {
  const id = selRole.value
  if (!id) return
  roleErr.value = ''
  try {
    const d = await api.roleUpdate(id, patch)
    roleDetail.value = { ...(roleDetail.value as RoleSpec), ...d, agents: agentsDraft.value }
    await loadRoles()
    roleMsg.value = '已保存'
  } catch (e) {
    roleErr.value = (e as Error).message
  }
}

// saveAgents 保存工作规则(会逐字进系统提示 → 二次确认 + 上限提示)
function saveAgents(): void {
  const id = selRole.value
  const text = agentsDraft.value
  if (!id) return
  if (text.length > roleMax.value) {
    roleErr.value = `工作规则超上限:${text.length} > ${roleMax.value} 字节(会逐字进系统提示,请精简)`
    return
  }
  guard('保存「' + id + '」的工作规则?(写入 roles/' + id + '/AGENTS.md,下一轮系统提示生效)', false, async () => {
    roleErr.value = ''
    try {
      await api.roleSetAgents(id, text)
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
      selRole.value = d.id
      roleMsg.value = '已改标识:' + d.id
      await loadRoles()
      await selectRole(d)
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })
}

// deleteRole 删除(移入回收站;当前角色后端会拒)
function deleteRole(r: RoleSpec): void {
  guard('删除角色「' + r.name + '」?(移入 roles/.trash/,可恢复)', true, async () => {
    roleErr.value = ''
    try {
      await api.roleDelete(r.id)
      if (selRole.value === r.id) {
        selRole.value = ''
        roleDetail.value = null
      }
      roleMsg.value = '已删除「' + r.id + '」(在 roles/.trash/)'
      await loadRoles()
    } catch (e) {
      roleErr.value = (e as Error).message
    }
  })
}

// —— 技能挂载 ——
// 勾选即提交(可一键改回,不进确认弹层);「默认池 / 替换」是模式选择,同样即时。
function mounted(r: RoleSpec | null, name: string): boolean {
  return !!r?.skills?.includes(name)
}
async function toggleMount(name: string, on: boolean): Promise<void> {
  const d = roleDetail.value
  if (!d) return
  const next = new Set(d.skills ?? [])
  if (on) next.add(name)
  else next.delete(name)
  await saveRoleDef({ skills_set: true, skills: Array.from(next).sort() })
  roleMsg.value = '挂载已更新(下一轮生效)'
}
function useDefaultPool(): void {
  void saveRoleDef({ skills_set: false })
}
function useReplacePool(): void {
  void saveRoleDef({ skills_set: true, skills: roleDetail.value?.skills ?? [] })
}
function toggleInherit(on: boolean): void {
  void saveRoleDef({ skills_inherit: on })
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
// openSkill 读技能原文(编辑)
async function openSkill(name: string, role: string): Promise<void> {
  skErr.value = ''
  try {
    const r = await api.skillGet(name, role)
    skEdit.value = { name, role, content: r.content }
  } catch (e) {
    skErr.value = (e as Error).message
  }
}
function saveSkill(): void {
  const e = skEdit.value
  if (!e) return
  guard('覆盖写入技能 ' + e.name + ' 的 SKILL.md?', false, async () => {
    skErr.value = ''
    try {
      await api.skillCreate({ role: e.role, name: e.name, content: e.content, overwrite: true })
      roleMsg.value = '技能已保存:' + e.name
      skEdit.value = null
      await loadRoles()
    } catch (err) {
      skErr.value = (err as Error).message
    }
  })
}
function deleteSkill(name: string, role: string): void {
  guard('删除技能 ' + name + '?(移入技能库 .trash/,可恢复)', true, async () => {
    skErr.value = ''
    try {
      await api.skillDelete(name, role)
      roleMsg.value = '技能已删除:' + name
      await loadRoles()
    } catch (e) {
      skErr.value = (e as Error).message
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
const mcpBase = ref('') // 磁盘当前内容指纹(脏检测基准;保存成功后刷新)
const mcpErr = ref('')
const mcpMsg = ref('')
const mcpDirty = computed(() => draftKey(mcpDrafts.value) !== mcpBase.value)
// 环境变量来源的条目只读(来自 GAH_MCP_COMMAND(S),不在文件里)
const mcpEnvRows = computed<McpServer[]>(() => (mcpView.value?.servers ?? []).filter((s) => !canEdit(s)))
const mcpNotices = computed<string[]>(() => (mcpView.value ? viewNotices(mcpView.value) : []))

async function loadMcp(): Promise<void> {
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
  void loadRoles()
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
               真机上因此得出「已选了模型但当前模型显示为空」的结论。 -->
          <p class="dim" data-testid="cur-model">当前生效:{{ curModelLabel || '(未设置)' }}</p>
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
                :class="{ on: props.state.thinking === t }"
                :data-tip="'思考 ' + THINK_LABEL[t]"
                @click="applyCtl({ thinking: t })"
              >
                {{ THINK_LABEL[t] }}
              </button>
            </div>
          </div>
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
        <section v-if="roleReady" data-sec="role" class="sec">
          <h3 class="h">
            角色
            <button class="link" data-tip="新建一个角色" @click="showRoleNew = !showRoleNew">
              {{ showRoleNew ? '收起' : '＋ 新建' }}
            </button>
          </h3>
          <p class="dim">
            角色 = 人设（身份句）+ 工作规则（AGENTS.md）+ 技能挂载。切换后<strong>下一轮</strong>生效，不换会话（回合历史与工作区都不动，与切换工作区不同）。
          </p>
          <div v-if="roleErr" class="serr">{{ roleErr }}</div>
          <p v-if="roleMsg" class="dim ok">{{ roleMsg }}</p>
          <p v-if="roleProblems.length" class="dim">
            部分角色文件读不了（已跳过）：{{ roleProblems.map((p) => p.id).join('、') }}
          </p>

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

          <div class="plist">
            <div v-for="r in roles" :key="r.id" class="prow scrow" :class="{ off: !roleIsCurrent(r) }">
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
              </div>
              <div class="sacts">
                <button v-if="!roleIsCurrent(r)" class="ghost" data-tip="下一轮生效，不换会话" @click="useRole(r.id)">切换</button>
                <button class="ghost" @click="selectRole(r)">{{ selRole === r.id ? '收起' : '编辑' }}</button>
                <button class="ghost danger-text" data-tip="删除角色（需确认，移入回收站）" @click="deleteRole(r)">删除</button>
              </div>
            </div>
            <p v-if="!roles.length" class="dim">
              还没有角色：点上方「＋ 新建」建一个；首次启动会释放 5 个预置角色（助理 / 财务 / 小说家 / 编程大师 / 新闻撰稿人）。
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
                工作规则（AGENTS.md）{{ agentsDraft.length }} / {{ roleMax }} 字节
                <span v-if="agentsDraft.length > roleMax" class="err-text">超出上限</span>
              </span>
              <textarea v-model="agentsDraft" class="inp mono" rows="8" placeholder="写这个角色的做事规程（逐字进系统提示，越短越省）"></textarea>
            </label>
            <div class="form-acts">
              <button class="ghost solid" :disabled="busy || !selRole" @click="saveAgents">保存工作规则</button>
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
                <button class="seg-it" :class="{ on: !roleDetail.skills_set }" data-tip="不写 skills 键 = 用默认池（全部库技能）" @click="useDefaultPool">默认池</button>
                <button class="seg-it" :class="{ on: roleDetail.skills_set }" data-tip="写了 skills 键 = 只挂勾选的" @click="useReplacePool">替换</button>
              </div>
            </div>
            <label class="chk">
              <input type="checkbox" :checked="!!roleDetail.skills_inherit" @change="toggleInherit(($event.target as HTMLInputElement).checked)" />
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
                      @change="toggleMount(s.name, ($event.target as HTMLInputElement).checked)"
                    />
                    <span class="m-lab">{{ s.name }}</span>
                  </label>
                  <span v-if="s.description" class="dim grow">{{ s.description }}</span>
                  <button class="ghost" data-tip="看/改 SKILL.md 原文" @click="openSkill(s.name, s.role || '')">原文</button>
                  <button class="ghost danger-text" data-tip="删除技能（需确认）" @click="deleteSkill(s.name, s.role || '')">删除</button>
                </div>
              </template>
              <p v-if="!roleLib.length" class="dim">技能库为空：可在下方新建，或把技能放到 skills/&lt;名&gt;/SKILL.md。</p>
            </div>

            <!-- 技能原文编辑（覆盖写） -->
            <div v-if="skEdit" class="add-form">
              <label class="fld">
                <span class="fld-lab">编辑 {{ skEdit.name }} 的 SKILL.md（原文，保存即覆盖）</span>
                <textarea v-model="skEdit.content" class="inp mono" rows="10"></textarea>
              </label>
              <div class="form-acts">
                <button class="ghost solid" :disabled="busy" @click="saveSkill">保存技能</button>
                <button class="ghost" @click="skEdit = null">取消</button>
              </div>
            </div>
          </div>

          <!-- 新建技能（共享库 / 角色私有） -->
          <h3 class="h">
            技能库
            <button class="link" data-tip="新建一个技能" @click="showSkillNew = !showSkillNew">
              {{ showSkillNew ? '收起' : '＋ 新建技能' }}
            </button>
          </h3>
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
            <button class="ghost" :disabled="busy || !mcpDirty" @click="loadMcp">放弃修改</button>
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
          <!-- UI 插件信任模型明示(文案由后端 /api/ui-plugins 下发,单一事实源) -->
          <p v-if="uiPluginTrustNote" class="dim">UI 插件(ui-plugins):{{ uiPluginTrustNote }}</p>
          <!-- 产物完整性提示(R10 ⑤-3):sha256 覆盖范围与降级原因如实显示;值供人比对,
               本面板不做"通过/不通过"判断(同源页面自证不构成安全边界) -->
          <details v-if="uiPluginDigests.length" class="dim">
            <summary>产物校验值(sha256,{{ uiPluginDigests.length }} 个 UI 插件;可与发布方公布的比对)</summary>
            <ul class="digests">
              <li v-for="d in uiPluginDigests" :key="d.id" :title="d.sha256 || '无摘要'">{{ digestLine(d) }}</li>
            </ul>
          </details>
          <div v-if="plugins.length > PLUGIN_CAP" class="row">
            <input v-model="pluginFilter" class="inp grow" placeholder="筛选插件 ID / 类型 / 状态" />
          </div>
          <div class="plist">
            <div v-for="p in pluginShown" :key="p.ID" class="prow">
              <div class="pmain">
                <span class="pname">{{ p.ID }}</span>
                <span class="psub">{{ p.Type }} · {{ p.State }} · {{ manageMeta(p).label }}</span>
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
        </div>
        <p class="dim foot-msg" :class="{ ok: updOk }">
          {{ updMsg || '检查 GitHub Release 上的新版本,有更新会自动下载安装并重启' }}
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
/* 产物校验值列表(R10 ⑤-3):等宽字体便于逐字符比对;折行不裁剪(哈希截断会误导) */
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
