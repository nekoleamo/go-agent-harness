<script setup lang="ts">
// 本会话设置面板(第一百三十四批 · 设置作用域收敛)。
//
// 为何要有这个面板:会话级参数(模型/思考/沙箱/审批)在设置面板里也有一份,但那一栏
// 从此只改**全局默认**;要改「这个页签用什么」必须有另一个入口 —— 作用域与对象一一对应,
// 才不会「点了一下以为是全局、实际只动了这个页签」。
//
// 位置:输入框工具条上的按钮。改的就是**这个会话**的运行参数,那里本来就有思考/沙箱的
// 循环按钮,就近原则成立(侧栏离得远,页签右键又太隐蔽)。
//
// 每项**二态**:跟随全局 / 独立。这是本面板的核心 ——
// 「跟随」意味着不写会话档,后端合成时回落全局(前端把这种留空画成一种状态,而不是
// 假装自己设了全局值);「独立」才写会话档。
import { computed } from 'vue'
import { api } from '../api'
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

type Field = 'model' | 'thinking' | 'sandbox' | 'approval'

// indep:该项是否**独立于全局**。判据见 scope.ts —— 只看 session_prefs。
// 第一版在这里读 `*_from === 'session'`,那是错的:model_from/thinking_from 在没有角色时
// 恒为 'session',会把「跟随全局」也标成独立(真 bug,由 scope.test.ts 的回归钉子盯着)。
function indep(field: Field): boolean {
  return isSessionSet(props.state, field)
}

function independentCount(): number {
  return (['model', 'thinking', 'sandbox', 'approval'] as const).filter(indep).length
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
  emit('changed')
}

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
      <span class="scp-title">本会话设置</span>
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

    <!-- 模型 -->
    <section class="scp-sec">
      <h4 class="scp-h">
        模型
        <span v-if="indep('model')" class="scp-tag">独立</span>
        <span v-else class="scp-tag scp-tag-off">跟随全局</span>
      </h4>
      <p class="scp-now">本会话在用：<b>{{ state.model }}</b></p>
      <p v-if="state.model_from === 'role'" class="scp-note">当前角色指定了模型，实际按角色那份跑。</p>
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