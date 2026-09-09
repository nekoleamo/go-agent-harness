<script setup lang="ts">
// 状态栏(槽位 statusbar):重点突出运行状态与上下文用量,次要信息(模型/思考/沙箱/会话/版本)降级淡显。
// 对齐 TUI 状态栏语义;空值显示占位(不假精确:窗口未知仅显示量)。
import { computed } from 'vue'
import type { StateView } from '../types'

const props = defineProps<{ state: StateView; conn?: 'open' | 'reconnecting' }>()

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

const sandboxLabel = computed(() => {
  switch (props.state.sandbox) {
    case 'read-only':
      return '只读'
    case 'full-access':
      return '完全'
    default:
      return '工作区'
  }
})

// 审批档位(M17 开放/智能/严格);未知不显示(保持状态栏简洁)。
const approvalLabel = computed(() => {
  switch (props.state.approval) {
    case 'open':
      return '开放'
    case 'smart':
      return '智能'
    case 'strict':
      return '严格'
    default:
      return ''
  }
})
</script>

<template>
  <div class="bar">
    <span class="run" :class="{ busy: state.running }">
      <span v-if="state.running" class="dot" />
      {{ state.running ? '运行中' : '就绪' }}
    </span>
    <span class="it">模型 {{ state.model || '未设置' }}</span>
    <span class="it faint">[{{ state.thinking }}] 沙箱 {{ sandboxLabel }}</span>
    <span v-if="approvalLabel" class="it faint">审批 {{ approvalLabel }}</span>
    <span v-if="state.session" class="it faint">
      会话{{ state.session.name ? '「' + state.session.name + '」' : state.session.id ? '#' + state.session.id : '(主)' }}
    </span>
    <span class="spacer" />
    <span class="conn" :class="props.conn">
      <span class="conn-dot" />
      <span v-if="props.conn === 'reconnecting'" class="conn-text">重连中</span>
      <span v-else class="conn-text">已连接</span>
    </span>
    <span class="ctx mono">{{ ctx }}</span>
    <span class="it faint mono">v{{ state.version || 'dev' }}</span>
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
.conn-text {
  color: var(--fg-dim);
}
.conn.reconnecting .conn-text {
  color: var(--tool);
  font-weight: 500;
}
.mono {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
}
</style>
