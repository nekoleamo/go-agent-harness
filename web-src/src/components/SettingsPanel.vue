<script setup lang="ts">
// 可视化设置抽屉(状态栏 ⚙ 入口;App 持有 open)。
// 分组:模型/推理(thinking·sandbox)/历史与压缩/Provider/插件与指令。
// 破坏性动作(删 provider、卸载插件、压缩)经全局确认条(askConfirm)。
import { computed, inject, nextTick, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { settingSections } from '../registry'
import { uiPluginTrustNote } from '../plugins'
import { PROVIDER_PRESETS, explainProbeError, type ProviderPreset } from '../providers'
import { cronShapeError, fmtAbs, fmtNextRun, statusLabel } from '../schedule'
import type { AskConfirm, PluginInfo, ProviderInfo, ProviderModelGroup, Schedule, StateView } from '../types'

const props = defineProps<{
  open: boolean
  state: StateView
  focus?: string // 打开时定位到某一段(首启引导传 'provider')
  schedTick?: number // 定时计划运行信号(App 收到 schedule/run 帧后 +1)
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'changed'): void }>()

const ask = inject<(a: AskConfirm) => void>('askConfirm')
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
const provSec = ref<HTMLElement | null>(null)
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
    for (const md of g.Models) opts.push({ label: g.Name + ' · ' + md.ID, value: g.Name + '|' + md.ID })
  }
  // 当前活跃 provider 的模型(可能不在枚举中)
  const ap = activeProvider()
  if (ap && ap.Model && !opts.some((o) => o.value === ap.Name + '|' + ap.Model)) {
    opts.unshift({ label: ap.Name + ' · ' + ap.Model + '(当前)', value: ap.Name + '|' + ap.Model })
  }
  modelOptions.value = opts
}
const modelVal = ref('')
// 模型选择:输入筛选(modelFilter 实时过滤选项,provider·模型名均可匹配;命中即点选)
const modelFilter = ref('')
const filteredModels = computed(() => {
  const f = modelFilter.value.trim().toLowerCase()
  if (!f) return modelOptions.value
  return modelOptions.value.filter((o) => o.label.toLowerCase().includes(f) || o.value.toLowerCase().includes(f))
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
async function applyCtl(body: { thinking?: string; sandbox?: string; approval?: string }): Promise<void> {
  try {
    await api.control(body)
    emit('changed')
  } catch (e) {
    err.value = (e as Error).message
  }
}

// —— 数据备份(M18) ——
const backups = ref<{ name: string; size: number; time: number }[]>([])
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
  const name = backups.value[0]?.name ?? ''
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
    await load()
  } catch (e) {
    err.value = (e as Error).message
  }
}
function deleteProvider(p: ProviderInfo): void {
  guard('删除 provider「' + p.Name + '」?(运行时复位)', true, () => void doProviderDelete(p))
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
})
// 打开时同步当前值与枚举
watch(
  () => props.open,
  (o) => {
    if (!o) return
    void load()
    void loadSchedules()
    // 首启引导:直接滚到 Provider 段(面板内容比一屏长时否则看不到)
    if (props.focus === 'provider') void nextTick(() => provSec.value?.scrollIntoView({ block: 'start' }))
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
  () => [providers.value, models.value],
  () => {
    const ap = activeProvider()
    modelVal.value = ap && props.state.model ? ap.Name + '|' + props.state.model : ''
    buildModelOptions()
  },
  { immediate: true, deep: true },
)
</script>

<template>
  <div v-if="open" class="mask" @click.self="emit('close')">
    <aside class="panel" role="dialog" aria-label="设置">
      <header class="head">
        <h2 class="title">设置</h2>
        <button class="x" data-tip="关闭设置" @click="emit('close')">✕</button>
      </header>

      <div v-if="err" class="err">{{ err }}</div>
      <div v-if="info" class="info">{{ info }}</div>

      <div class="body">
        <!-- 模型(输入筛选 + 匹配列表;模型多时原生 select 难找,点选即应用) -->
        <section class="sec">
          <h3 class="h">模型</h3>
          <div class="row">
            <span class="lab-inline">当前模型</span>
            <input
              id="set-model"
              v-model="modelFilter"
              class="inp grow"
              placeholder="筛选模型名/Provider"
              :disabled="busy"
            />
          </div>
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
        <section class="sec">
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
        </section>

        <!-- 历史与压缩 -->
        <section class="sec">
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
        <section ref="provSec" class="sec">
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
                <button class="ghost danger-text" data-tip="删除该 Provider(确认)" @click="deleteProvider(p)">删除</button>
              </div>
            </div>
            <p v-if="!providers.length" class="dim">还没有配置 Provider:点上方「＋ 新增」或直接选一个预设</p>
          </div>
        </section>

        <!-- 数据备份(M18) -->
        <section class="sec">
          <h3 class="h">数据备份</h3>
          <div class="row acts">
            <button class="ghost" :disabled="busy" @click="doBackupNow()">立即备份</button>
            <button
              v-if="backups.length"
              class="ghost danger-text"
              :disabled="busy"
              :data-tip="'恢复 ' + backups[0].name"
              @click="doBackupRestore()"
            >
              恢复最新备份
            </button>
          </div>
          <p v-if="backups.length" class="dim">最近备份:{{ backups[0].name }} · {{ fmtSize(backups[0].size) }}
            <span v-if="backups.length > 1">(共 {{ backups.length }} 份)</span></p>
          <p v-else class="dim">暂无备份(/backup 或立即备份创建第一份)</p>
          <p v-if="backupMsg" class="dim ok">{{ backupMsg }}</p>
        </section>

        <!-- 定时计划(NOND-W4,host-schedule 未装配时整段隐藏) -->
        <section v-if="schedReady" class="sec">
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

        <!-- 插件与指令 -->
        <section class="sec">
          <h3 class="h">插件</h3>
          <!-- UI 插件信任模型明示(文案由后端 /api/ui-plugins 下发,单一事实源) -->
          <p v-if="uiPluginTrustNote" class="dim">UI 插件(ui-plugins):{{ uiPluginTrustNote }}</p>
          <div class="plist">
            <div v-for="p in plugins" :key="p.ID" class="prow">
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
          </div>
          <div class="row acts">
            <button class="ghost" @click="doReload">重载指令文件</button>
          </div>
        </section>
        <!-- v2 扩展点:设置面板区段(插件注入,每插件一节) -->
        <section v-for="s in panelSections" :key="s.key" class="sec">
          <component :is="s.component" />
        </section>
      </div>
    </aside>
  </div>
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
.scrow {
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

.mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  z-index: 70;
  animation: fade 0.15s ease;
}
@keyframes fade {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
.panel {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: 360px;
  max-width: calc(100vw - 32px);
  background: var(--bg);
  border-left: 1px solid var(--line);
  box-shadow: var(--shadow-dialog);
  display: flex;
  flex-direction: column;
  animation: slide 0.2s cubic-bezier(0.16, 1, 0.3, 1);
}
@keyframes slide {
  from {
    transform: translateX(24px);
    opacity: 0;
  }
  to {
    transform: translateX(0);
    opacity: 1;
  }
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
  overflow-y: auto;
  padding: 6px 16px 20px;
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
}
.sec:last-child {
  border-bottom: none;
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
