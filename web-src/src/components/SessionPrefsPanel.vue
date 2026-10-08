<script setup lang="ts">
// 本会话设置面板(第一百三十四批 · 设置作用域收敛;第一百三十六批入口移到右上角状态栏)。
//
// 为何要有这个面板:会话级参数(角色/模型/思考/沙箱/审批)在设置面板里也有一份,但那一栏
// 从此只改**全局默认**;要改「这个页签用什么」必须有另一个入口 —— 作用域与对象一一对应,
// 才不会「点了一下以为是全局、实际只动了这个页签」。
//
// 位置:状态栏右上角「当前会话设置」(第一百三十六批从输入框工具条移来;用户反馈那儿不够明显);
// 单独靠右、常驻强调色,本会话真压过全局时带计数徽标。
//
// 每项**二态**:跟随全局 / 独立。这是本面板的核心 ——
// 「跟随」意味着不写会话档,后端合成时回落全局(前端把这种留空画成一种状态,而不是
// 假装自己设了全局值);「独立」才写会话档。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import { currentModelValue, modelOptionValue, modelRank, withCurrentModel } from '../modelsel'
import type { ModelOption } from '../modelsel'
import { isSessionSet } from '../scope'
import type { StateView } from '../types'

const props = defineProps<{ state: StateView }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'changed'): void }>()

const THINK = [
  { v: 'off', label: '关闭' },
  { v: 'low', label: '低' },
  { v: 'medium', label: '中' },
  { v: 'high', label: '高' },
] as const
const SANDBOX = [
  { v: 'read-only', label: '只读' },
  { v: 'workspace-write', label: '工作区' },
  { v: 'full-access', label: '完全' },
] as const
const APPROVAL = [
  { v: 'open', label: '开放' },
  { v: 'smart', label: '智能' },
  { v: 'strict', label: '严格' },
] as const

const SB_ZH: Record<string, string> = { 'read-only': '只读', 'workspace-write': '工作区', 'full-access': '完全' }
const AP_ZH: Record<string, string> = { open: '开放', smart: '智能', strict: '严格' }

// —— 本会话模型选择(第一百三十六批) ——
//
// 为什么面板里必须有它:会话级模型此前**根本没有设置入口** —— 设置面板的模型段只写全局默认
// (session:""),而这里只显示当前值 + 「改为跟随全局」。于是「只让这个页签用另一个模型」
// 在界面层完全做不到(第一百三十四/五批修好了「清除覆盖」,却漏了「设置覆盖」)。
//
// 只列**当前 provider** 的模型:会话档存的只是模型名(与「角色只能选模型名」同一条边界),
// 换 provider 仍是全局动作。把别的 provider 的模型也摆上 = 点了不生效的假按钮。
const rawModels = ref<ModelOption[]>([])
const activeProviderName = ref('')
const modelFilter = ref('')
const modelOptions = computed(() =>
  withCurrentModel(rawModels.value, props.state.model, activeProviderName.value),
)
const filteredModels = computed(() => {
  const f = modelFilter.value.trim().toLowerCase()
  if (!f) return modelOptions.value
  return modelOptions.value.filter(
    (o) => o.label.toLowerCase().includes(f) || o.value.toLowerCase().includes(f),
  )
})
const curModelValue = computed(() =>
  currentModelValue(modelOptions.value, props.state.model, activeProviderName.value),
)

async function loadModels(): Promise<void> {
  const [m, pr] = await Promise.allSettled([api.models(), api.providers()])
  const groups = m.status === 'fulfilled' ? (m.value.providers ?? []) : []
  const active =
    pr.status === 'fulfilled' ? ((pr.value ?? []).find((p) => p.Active)?.Name ?? '') : ''
  activeProviderName.value = active
  const g = groups.find((x) => x.Name === active)
  const opts: ModelOption[] = (g?.Models ?? []).map((md) => {
    const v = md.Verdict
    const tags = v?.Tags ?? []
    return {
      label: md.ID + (tags.length ? ' · ' + tags.join(' · ') : ''),
      value: modelOptionValue(active, md.ID),
      // 没有 verdict(旧后端/枚举失败)时不当成不可用 —— 那会把列表清空(与设置面板同一条纪律)
      usable: v ? v.Usable : true,
      free: v ? v.Free : false,
      autoRouter: v?.AutoRouter ?? false,
      contextWindow: v?.ContextWindow ?? 0,
      tags,
    }
  })
  opts.sort((a, b) => modelRank(a) - modelRank(b))
  rawModels.value = opts
}

// pickModel 写**本会话**的模型名(显式带 session;换 provider 不做 —— 那是全局动作)。
async function pickModel(o: ModelOption): Promise<void> {
  const mid = o.value.split('|').slice(1).join('|')
  if (!mid) return
  await pick('model', mid)
}

// —— 本会话角色(第一百三十八批) ——
//
// 用户反馈「当前会话仍然无法独立选择角色」:角色此前**只在设置面板里能选**,而那一栏按
// 作用域纪律应当只改全局默认(与模型/思考/沙箱/审批一致);本会话要另选角色,缺的正是这里。
//
// 会话档存的只是**角色 id**(人设/技能/工具可见性都在角色定义里);「默认(基线)」= 不启用角色。
type RoleItem = { id: string; name: string; group?: string }
const roles = ref<RoleItem[]>([])
const roleReady = ref(false)
async function loadRoles(): Promise<void> {
  try {
    const v = await api.roles()
    roles.value = (v.roles ?? []).map((r) => ({ id: r.id, name: r.name || r.id, group: r.group }))
    roleReady.value = true
  } catch {
    roleReady.value = false // 未装配 ctx.roles(503):整段不渲染(不摆空壳,与设置面板同口径)
  }
}
// currentRoleLabel 本会话**实际生效**的角色(后端按作用域合成:会话档优先,否则全局当前)。
const currentRoleLabel = computed(() => {
  const id = props.state.role
  if (!id) return '默认(基线)'
  const name = props.state.role_name
  return name && name !== id ? `${name}(${id})` : id
})
// 角色按分组铺(与设置面板同一份 group):平铺十几个角色时认不出该挑哪个。
const roleGroups = computed(() => {
  const by = new Map<string, RoleItem[]>()
  for (const r of roles.value) {
    const g = r.group || '其它'
    if (!by.has(g)) by.set(g, [])
    by.get(g)!.push(r)
  }
  return [...by.entries()].map(([group, items]) => ({ group, items }))
})
// pickRole 写**本会话**的角色(id 空 = 回基线)。与设置面板的全局切换分开,各写各的。
async function pickRole(id: string): Promise<void> {
  await api.roleUse(id, { session: api.boundSession() })
  emit('changed')
}

type Field = 'role' | 'model' | 'thinking' | 'sandbox' | 'approval'

// indep:该项是否**独立于全局**。判据见 scope.ts —— 只看 session_prefs。
// 第一版在这里读 `*_from === 'session'`,那是错的:model_from/thinking_from 在没有角色时
// 恒为 'session',会把「跟随全局」也标成独立(真 bug,由 scope.test.ts 的回归钉子盯着)。
function indep(field: Field): boolean {
  return isSessionSet(props.state, field)
}

function independentCount(): number {
  return (['role', 'model', 'thinking', 'sandbox', 'approval'] as const).filter(indep).length
}
const anyIndependent = computed(() => independentCount() > 0)

// thinkLabel:后端给的是档位值(off/low/medium/high),这里翻成中文;
// 认不出来的值原样透出 —— 界面宁可难看也不能说错话。
const thinkLabel = computed(() => THINK.find((t) => t.v === props.state.thinking)?.label ?? props.state.thinking)

// pick 写**本会话**档:显式传 session,不受 api.control 的默认绑定影响。
async function pick(field: Field, v: string): Promise<void> {
  await api.control({ session: api.boundSession(), [field]: v })
  emit('changed')
}

// reset 回到「跟随全局」:空串 = 清掉该会话的会话档,后端合成时回落到全局。
async function reset(field: Field): Promise<void> {
  await api.control({ session: api.boundSession(), [field]: '' })
  emit('changed')
}

async function resetAll(): Promise<void> {
  for (const f of ['model', 'thinking', 'sandbox', 'approval'] as const) {
    await api.control({ session: api.boundSession(), [f]: '' })
  }
  if (roleReady.value) await api.roleUse('', { session: api.boundSession() })
  emit('changed')
}

// onEsc:Esc 关面板(与设置面板同一手势 —— 一个只能靠点遮罩关的抽屉,用户会以为坏了)。
// capture:抢在输入框/文档面板的 Esc 之前 —— 本面板开着时它们不该先响应。
function onEsc(e: KeyboardEvent): void {
  if (e.key !== 'Escape') return
  e.stopPropagation()
  emit('close')
}
onMounted(() => {
  window.addEventListener('keydown', onEsc, true)
  void loadModels()
  void loadRoles()
})
onUnmounted(() => window.removeEventListener('keydown', onEsc, true))

// effectiveXxx 显示**本会话实际生效**的值(已含角色收紧与审批联动的合成结果)。
function eff(field: 'sandbox' | 'approval', zh: Record<string, string>): string {
  const s = props.state
  // 先收窄成 string 再查表:这几个字段都是 optional,直接索引会把 undefined 带进
  // Record<string,string> 的键位(TS 报错,运行时也确实会拿到 "undefined")。
  const raw = field === 'sandbox' ? (s.sandbox_effective ?? s.sandbox) : (s.approval_effective ?? s.approval)
  const v = raw ?? ''
  return zh[v] ?? v
}
</script>

<template>
  <div class="scp">
    <header class="scp-head">
      <span class="scp-title">当前会话设置</span>
      <button class="scp-x" data-tip="关闭" @click="emit('close')">×</button>
    </header>

    <p class="scp-lede">
      这里只改<b>当前页签</b>（随页签走，换页签就是另一套）；全局默认在状态栏「设置」里改。
    </p>

    <p v-if="anyIndependent" class="scp-sum" data-testid="scp-summary">
      <b>{{ independentCount() }}</b> 项独立于全局，其余跟随。
      <button class="scp-reset-all" @click="resetAll">全部改为跟随全局</button>
    </p>
    <p v-else class="scp-sum scp-sum-off" data-testid="scp-summary">全部跟随全局。</p>

    <!-- 角色(第一百三十八批):会话级角色选择。放在最前 —— 角色一改,模型/思考档与
         技能面都跟着变,它比其它四项更“上游”。 -->
    <section v-if="roleReady" class="scp-sec">
      <h4 class="scp-h">
        角色
        <span v-if="indep('role')" class="scp-tag">独立</span>
        <span v-else class="scp-tag scp-tag-off">跟随全局</span>
      </h4>
      <p class="scp-now">本会话在用：<b>{{ currentRoleLabel }}</b></p>
      <ul class="scp-list" data-testid="scp-roles">
        <li>
          <button class="scp-opt" :class="{ on: !state.role }" @click="pickRole('')">
            <span class="scp-opt-name">默认（基线）· 不启用角色</span>
          </button>
        </li>
        <template v-for="g in roleGroups" :key="g.group">
          <li class="scp-group">{{ g.group }}</li>
          <li v-for="r in g.items" :key="r.id">
            <button class="scp-opt" :class="{ on: state.role === r.id }" @click="pickRole(r.id)">
              <span class="scp-opt-name">{{ r.name }}</span>
              <span class="scp-opt-id">{{ r.id }}</span>
            </button>
          </li>
        </template>
      </ul>
      <p class="scp-hint">角色改人设、工作规则与技能面；下一轮生效，不换会话。</p>
      <button v-if="indep('role')" class="scp-reset" @click="pickRole('')">改为跟随全局</button>
    </section>

    <!-- 模型 -->
    <section class="scp-sec">
      <h4 class="scp-h">
        模型
        <span v-if="indep('model')" class="scp-tag">独立</span>
        <span v-else class="scp-tag scp-tag-off">跟随全局</span>
      </h4>
      <p class="scp-now">本会话在用：<b>{{ state.model }}</b></p>
      <p v-if="state.model_from === 'role'" class="scp-note">当前角色指定了模型，实际按角色那份跑。</p>
      <template v-if="activeProviderName && modelOptions.length">
        <input
          v-model="modelFilter"
          class="scp-input"
          data-testid="scp-model-filter"
          placeholder="筛选模型（点一下设为本会话专用）"
        />
        <ul class="scp-list" data-testid="scp-models">
          <li v-for="o in filteredModels" :key="o.value">
            <button class="scp-opt" :class="{ on: o.value === curModelValue }" @click="pickModel(o)">
              <span class="scp-opt-name">{{ o.label }}</span>
              <span v-if="o.free" class="scp-free">免费</span>
            </button>
          </li>
          <li v-if="!filteredModels.length" class="scp-empty">没有匹配的模型</li>
        </ul>
        <p class="scp-hint">只列当前 provider（{{ activeProviderName }}）的模型；换 provider 是全局设置。</p>
      </template>
      <p v-else class="scp-note">模型列表拉不到，可到状态栏「设置」里改全局默认。</p>
      <button v-if="indep('model')" class="scp-reset" @click="reset('model')">改为跟随全局</button>
    </section>

    <!-- 思考 -->
    <section class="scp-sec">
      <h4 class="scp-h">
        思考等级
        <span v-if="indep('thinking')" class="scp-tag">独立</span>
        <span v-else class="scp-tag scp-tag-off">跟随全局</span>
      </h4>
      <p class="scp-now">本会话在用：<b>{{ thinkLabel }}</b></p>
      <p v-if="state.thinking_from === 'role'" class="scp-note">被当前角色覆盖。</p>
      <div class="scp-seg">
        <button
          v-for="t in THINK"
          :key="t.v"
          class="scp-it"
          :class="{ on: state.thinking === t.v }"
          :data-tip="'本会话的思考等级设为 ' + t.label"
          @click="pick('thinking', t.v)"
        >
          {{ t.label }}
        </button>
      </div>
      <button v-if="indep('thinking')" class="scp-reset" @click="reset('thinking')">改为跟随全局</button>
    </section>

    <!-- 沙箱 -->
    <section class="scp-sec">
      <h4 class="scp-h">
        沙箱
        <span v-if="indep('sandbox')" class="scp-tag">独立</span>
        <span v-else class="scp-tag scp-tag-off">跟随全局</span>
      </h4>
      <p class="scp-now">
        实际生效：<b>{{ eff('sandbox', SB_ZH) }}</b>
        <span v-if="state.sandbox_from === 'role'" class="scp-src">（角色收紧）</span>
        <span v-else-if="state.sandbox_derived" class="scp-src">（随审批联动）</span>
      </p>
      <div class="scp-seg">
        <button
          v-for="s in SANDBOX"
          :key="s.v"
          class="scp-it"
          :class="{ on: state.sandbox === s.v }"
          :data-tip="'本会话的沙箱档设为 ' + s.label"
          @click="pick('sandbox', s.v)"
        >
          {{ s.label }}
        </button>
      </div>
      <button v-if="indep('sandbox')" class="scp-reset" @click="reset('sandbox')">改为跟随全局</button>
    </section>

    <!-- 审批 -->
    <section class="scp-sec">
      <h4 class="scp-h">
        审批
        <span v-if="indep('approval')" class="scp-tag">独立</span>
        <span v-else class="scp-tag scp-tag-off">跟随全局</span>
      </h4>
      <p class="scp-now">
        实际生效：<b>{{ eff('approval', AP_ZH) }}</b>
        <span v-if="state.approval_from === 'role'" class="scp-src">（角色收紧）</span>
      </p>
      <div class="scp-seg">
        <button
          v-for="a in APPROVAL"
          :key="a.v"
          class="scp-it"
          :class="{ on: state.approval === a.v }"
          :data-tip="'本会话的审批档设为 ' + a.label"
          @click="pick('approval', a.v)"
        >
          {{ a.label }}
        </button>
      </div>
      <button v-if="indep('approval')" class="scp-reset" @click="reset('approval')">改为跟随全局</button>
    </section>
  </div>
</template>

<style scoped>
.scp {
  font-size: 13px;
  color: var(--fg);
}
.scp-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.scp-title {
  font-size: 14px;
  font-weight: 600;
}
.scp-x {
  background: none;
  border: none;
  color: var(--fg-faint);
  cursor: pointer;
  font-size: 14px;
  padding: 2px 6px;
  border-radius: 6px;
  transition: color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.scp-x:hover {
  color: var(--fg);
  background: var(--bg3);
}
.scp-lede {
  margin: 0 0 10px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--fg-faint);
}
.scp-lede b {
  color: var(--fg-dim);
}
.scp-sum {
  margin: 0 0 12px;
  padding: 6px 10px;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg2);
  font-size: 12px;
  color: var(--fg-dim);
}
.scp-sum b {
  color: var(--accent);
}
.scp-sum-off {
  color: var(--fg-faint);
}
.scp-reset-all {
  background: none;
  border: 1px solid var(--line);
  border-radius: 5px;
  color: var(--accent);
  cursor: pointer;
  font-size: 11px;
  margin-left: 8px;
  padding: 1px 7px;
  transition: border-color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.scp-reset-all:hover {
  border-color: var(--accent);
  background: var(--accent-soft);
}
.scp-sec {
  margin: 0 0 14px;
}
.scp-h {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 4px;
  font-size: 13px;
  font-weight: 600;
  color: var(--fg);
}
.scp-tag {
  padding: 0 6px;
  border-radius: 4px;
  background: var(--accent-soft);
  border: 1px solid var(--accent-line);
  color: var(--accent);
  font-size: 11px;
  font-weight: 500;
}
.scp-tag-off {
  background: var(--bg3);
  border-color: var(--line);
  color: var(--fg-faint);
}
.scp-now {
  margin: 0 0 6px;
  font-size: 12px;
  color: var(--fg-dim);
}
.scp-now b {
  color: var(--fg);
}
.scp-src,
.scp-note {
  font-size: 12px;
  color: var(--fg-faint);
}
.scp-note {
  margin: 0 0 6px;
}
.scp-seg {
  display: inline-flex;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  overflow: hidden;
}
.scp-it {
  border: none;
  background: var(--bg);
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12px;
  padding: 4px 12px;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.scp-it + .scp-it {
  border-left: 1px solid var(--line);
}
.scp-it.on {
  background: var(--accent-soft);
  color: var(--fg);
  font-weight: 600;
}
.scp-input {
  width: 100%;
  box-sizing: border-box;
  margin: 4px 0 6px;
  padding: 5px 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg);
  color: var(--fg);
  font-size: 12px;
}
.scp-input:focus {
  outline: none;
  border-color: var(--accent);
}
.scp-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 220px;
  overflow-y: auto;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
}
.scp-opt {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  background: none;
  border: none;
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12px;
  padding: 5px 9px;
  text-align: left;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.scp-opt + .scp-opt {
  border-top: 1px solid var(--line-faint);
}
.scp-opt:hover {
  background: var(--bg2);
  color: var(--fg);
}
.scp-opt.on {
  background: var(--accent-soft);
  color: var(--fg);
  font-weight: 600;
}
.scp-opt-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.scp-opt-id {
  flex: none;
  color: var(--fg-faint);
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 10px;
}
.scp-group {
  padding: 4px 9px 2px;
  color: var(--fg-faint);
  font-size: 11px;
}
.scp-free {
  flex: none;
  padding: 0 5px;
  border: 1px solid var(--accent-line);
  border-radius: 4px;
  color: var(--accent);
  font-size: 10px;
  font-weight: 500;
}
.scp-empty {
  padding: 6px 9px;
  color: var(--fg-faint);
  font-size: 12px;
}
.scp-hint {
  margin: 6px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--fg-faint);
}
.scp-reset {
  display: block;
  margin-top: 6px;
  background: none;
  border: none;
  color: var(--accent);
  cursor: pointer;
  font-size: 11px;
  padding: 0;
}
.scp-reset:hover {
  text-decoration: underline;
}
</style>