<script setup lang="ts">
// 状态栏(槽位 statusbar):只放**别处没有的只读环境事实** —— 运行状态 / 沙箱生效档 / 连接 /
// 上下文用量 / 版本。2026-09-17 去重:模型、思考档、审批档各自已有唯一交互位(输入框工具条、
// 设置面板),底栏再镜像一遍既是重复显示又把一行挤变形;命名会话也不在这里重复(看侧栏高亮),
// 版本号改成「关于 gah」入口。对齐 TUI 状态栏语义;空值显示占位(不假精确:窗口未知仅显示量)。
import { computed } from 'vue'
import { connLabel } from '../conn'
import type { StateView } from '../types'

const props = defineProps<{ state: StateView; conn?: 'open' | 'reconnecting' | 'offline' }>()
// 版本号点击 → 「关于 gah」。用插槽替换底栏的一方不发这个事件也不影响(监听是可选的)。
const emit = defineEmits<{ (e: 'open-about'): void }>()
// connText 连接状态短标签:口径来自 conn.ts(宿主与插件覆盖槽位共用同一定义)
const connText = computed(() => connLabel(props.conn ?? 'open'))

function k(n: number): string {
  if (n < 1024) return String(n)
  return (n / 1024).toFixed(1) + 'K'
}

const ctx = computed(() => {
  const s = props.state.stats
  const used = s.PromptTokens + s.CompletionTokens
  if (s.Window > 0) {
    const pct = Math.min(100, Math.round((used / s.Window) * 100))
    const cachePct = s.PromptTokens > 0 ? Math.round((s.CachedTokens / s.PromptTokens) * 100) : 0
    return `${k(used)}/${k(s.Window)} ${pct}% · 缓存 ${cachePct}%`
  }
  return used > 0 ? `${k(used)} (窗口未知)` : '–'
})

const SANDBOX_ZH: Record<string, string> = { 'read-only': '只读', 'full-access': '完全', 'workspace-write': '工作区' }
// 审批档位中文名:只服务下面的 sandboxLabel(拼"随审批联动"的出处);审批档本身不在底栏显示
// —— 它已有唯一交互位(设置面板「审批」分段按钮)。
const APPROVAL_ZH: Record<string, string> = { open: '开放', smart: '智能', strict: '严格' }
// sandboxLabel 显示**实际生效**档(approval 联动时后端给 sandbox_effective):
// 只读 state.sandbox(声明档)会在 approval=open 时把"完全"显示成"工作区",与实际拦截行为不符。
// 保留原因:输入框工具条显示的是**声明档**(生效档只在它的图说里),底栏是唯一常驻显示生效档的位置,
// 删掉会让「随审批联动」在界面上不可见 —— 去重不能以丢掉安全相关状态为代价。
const sandboxLabel = computed(() => {
  const eff = props.state.sandbox_effective || props.state.sandbox
  const base = SANDBOX_ZH[eff] ?? '工作区'
  if (!props.state.sandbox_derived || !props.state.sandbox_effective) return base
  const src = APPROVAL_ZH[props.state.approval ?? '']
  return src ? `${base}(随审批${src})` : `${base}(随审批联动)`
})

// 会话标识只在**未命名**时显示(命名会话侧栏有高亮、输入框也有「会话」按钮,底栏再显一次纯重复,
// 且长会话名会把底栏一行挤变形);它唯一的真实价值是"这条消息进的是哪条"。
const anonSession = computed(() => {
  const s = props.state.session
  if (!s || s.name) return ''
  return s.id ? ' #' + s.id : ' (主)'
})
</script>

<template>
  <div class="bar">
    <span class="run" :class="{ busy: state.running }">
      <span v-if="state.running" class="dot" />
      {{ state.running ? '运行中' : '就绪' }}
    </span>
    <span class="it faint">沙箱 {{ sandboxLabel }}</span>
    <span v-if="anonSession" class="it faint">未命名会话{{ anonSession }}</span>
    <span class="spacer" />
    <span class="conn" :class="props.conn">
      <span class="conn-dot" />
      <span class="conn-text">{{ connText }}</span>
    </span>
    <span class="ctx mono">{{ ctx }}</span>
    <button class="ver mono" data-tip="关于 gah(版本 / 检查更新)" @click="emit('open-about')">
      v{{ state.version || 'dev' }}
    </button>
  </div>
</template>

<style scoped>
.bar {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 22px;
}
.run {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--fg-dim);
  font-size: 12px;
}
.run.busy {
  color: var(--accent);
  font-weight: 500;
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--accent);
  animation: pulse 1.1s ease-in-out infinite;
}
@keyframes pulse {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}
.it {
  color: var(--fg-dim);
}
.it.faint {
  color: var(--fg-faint);
  font-size: 12px;
}
.ctx {
  color: var(--fg-dim);
  font-size: 12px;
}
.spacer {
  flex: 1;
}
.conn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--fg-dim);
  font-size: 12px;
}
.conn-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--ok);
}
.conn.reconnecting .conn-dot {
  background: var(--tool);
  animation: pulse 1.1s ease-in-out infinite;
}
.conn.offline .conn-dot {
  background: var(--err);
}
.conn-text {
  color: var(--fg-dim);
}
.conn.reconnecting .conn-text {
  color: var(--tool);
  font-weight: 500;
}
.conn.offline .conn-text {
  color: var(--err);
  font-weight: 600;
}
.mono {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
}
/* 版本号 = 「关于 gah」入口:清掉按钮默认样式,只留底栏的淡显文本外观 */
.ver {
  padding: 0;
  border: 0;
  background: none;
  color: var(--fg-faint);
  font-size: 12px;
  cursor: pointer;
}
.ver:hover {
  color: var(--fg-dim);
  text-decoration: underline;
}
</style>
