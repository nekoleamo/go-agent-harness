<script setup lang="ts">
// 可视化设置抽屉(状态栏 ⚙ 入口;App 持有 open)。
// 分组:模型/推理(thinking·sandbox)/历史与压缩/Provider/插件与指令。
// 破坏性动作(删 provider、卸载插件、压缩)经全局确认条(askConfirm)。
import { computed, inject, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { settingSections } from '../registry'
import { uiPluginTrustNote } from '../plugins'
import type { AskConfirm, PluginInfo, ProviderInfo, ProviderModelGroup, StateView } from '../types'

const props = defineProps<{
  open: boolean
  state: StateView
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

// —— 操作表单(Provider 新增)——
const showAdd = ref(false)
const pf = ref({ name: '', base_url: '', api_key: '', model: '' })

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

// —— Provider ——
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
  busy.value = true
  try {
    await api.providerAdd({ name: pf.value.name, base_url: pf.value.base_url, api_key: pf.value.api_key, model: pf.value.model || undefined })
    showAdd.value = false
    pf.value = { name: '', base_url: '', api_key: '', model: '' }
    showInfo('已新增 provider(首个自动激活)')
    emit('changed')
    await load()
  } catch (e) {
    err.value = (e as Error).message
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

        <!-- Provider -->
        <section class="sec">
          <h3 class="h">
            Provider
            <button class="link" data-tip="新增/编辑 LLM 端点" @click="showAdd = !showAdd">{{ showAdd ? '收起' : '＋ 新增' }}</button>
          </h3>
          <div v-if="showAdd" class="add-form">
            <input v-model="pf.name" class="inp" placeholder="名称(如 deepseek)" />
            <input v-model="pf.base_url" class="inp mono" placeholder="base_url https://…" />
            <input v-model="pf.api_key" class="inp mono" type="password" placeholder="api_key(可选)" />
            <input v-model="pf.model" class="inp mono" placeholder="默认模型(可选)" />
            <button class="ghost solid" :disabled="busy" @click="addProvider">保存 Provider</button>
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
            <p v-if="!providers.length" class="dim">未配置 Provider(新增后首个自动激活)</p>
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
/* 扫码登录二维码(白底保证可扫) */
.qr-wrap {
  display: flex;
  justify-content: center;
  margin: 10px 0 6px;
}
.qr {
  width: 220px;
  height: 220px;
  padding: 6px;
  background: #fff;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  image-rendering: pixelated;
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
