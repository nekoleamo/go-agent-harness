<script setup lang="ts">
// NOND-N1 提示 toast 层:ctx.notices 的上行提示在 Web 端的落点。
// 纯呈现 + 转发:状态机在 notices.ts(可单测),本组件只画。
// 语义(与 notices.ts 一致):info 自动消失;warn/error 常驻直到用户关闭 ——
// 「需要人回来的时刻」的提示不该自己溜走。溢出条数显式展示,不假装只有这几条。
import { onMounted, onUnmounted } from 'vue'
import type { Notice } from '../types'
import { expire, type ToastState } from '../notices'

const props = defineProps<{
  state: ToastState
  onDismiss: (id: number) => void
}>()

// 到期清理节拍(1s 一拍的粗粒度足够:info 存活 6s,不必精确到毫秒)
let timer: ReturnType<typeof setInterval> | null = null
onMounted(() => {
  timer = setInterval(() => expire(props.state, Date.now()), 1000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

// lvClass 级别 → 类名(与 notices.ts 的口径一致:未知按 info)
function lvClass(n: Notice): string {
  return n.level === 'warn' || n.level === 'error' ? 'lv-' + n.level : 'lv-info'
}

// sourceLabel 来源标签(空/unknown 不显示,免得占宽度又说不出是谁发的)
function sourceLabel(n: Notice): string {
  return n.source && n.source !== 'unknown' ? n.source : ''
}
</script>

<template>
  <div v-if="state.items.length || state.overflow" class="toasts" role="status" aria-live="polite">
    <div v-for="t in state.items" :key="t.id" class="toast" :class="lvClass(t)">
      <div class="head">
        <span class="title">{{ t.title }}</span>
        <button class="x" data-tip="关闭提示" aria-label="关闭提示" @click="onDismiss(t.id)">×</button>
      </div>
      <div v-if="t.body" class="body">{{ t.body }}</div>
      <div v-if="sourceLabel(t)" class="src">{{ sourceLabel(t) }}</div>
    </div>
    <div v-if="state.overflow" class="more">另有 {{ state.overflow }} 条提示未显示(可在日志与提示接口查)</div>
    <div v-if="state.gap" class="more">更早的提示已过时(服务端缓冲已丢弃,回填不完整)</div>
  </div>
</template>

<style scoped>
/* 右下浮层:压在输入区之上、弹窗之下(不与审批/提问抢焦点) */
.toasts {
  position: fixed;
  right: 18px;
  bottom: 76px;
  z-index: 15;
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 320px;
  max-width: calc(100vw - 36px);
  pointer-events: none; /* 容器不吃点击,只有卡片本身可交互 */
}
.toast {
  pointer-events: auto;
  background: var(--bg);
  border: 1px solid var(--line);
  border-left-width: 3px;
  border-radius: var(--r-card);
  box-shadow: var(--shadow-pop);
  padding: 9px 10px 8px 11px;
  animation: toast-in var(--dur-base) var(--ease-out);
}
/* 形状/语义一致:左侧竖条 = 级别色,info 用中性色(不把信息态伪装成强调) */
.toast.lv-info {
  border-left-color: var(--line-strong);
}
.toast.lv-warn {
  border-left-color: var(--tool);
  background: var(--tool-soft);
  border-color: var(--tool-line);
}
.toast.lv-error {
  border-left-color: var(--err);
  background: var(--err-soft);
  border-color: var(--err-line);
}
.head {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.title {
  flex: 1;
  font-size: 13px;
  font-weight: 600;
  color: var(--fg);
  word-break: break-word;
}
.x {
  flex: none;
  border: 0;
  background: transparent;
  color: var(--fg-faint);
  font-size: 15px;
  line-height: 1;
  padding: 0 2px;
  cursor: pointer;
  border-radius: var(--r-input);
}
.x:hover {
  color: var(--fg);
  background: var(--hover-bg);
}
.body {
  margin-top: 3px;
  font-size: 12px;
  line-height: 1.45;
  color: var(--fg-dim);
  word-break: break-word;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
}
.src {
  margin-top: 4px;
  font-size: 11px;
  color: var(--fg-faint);
}
.more {
  font-size: 11px;
  color: var(--fg-faint);
  padding: 0 2px;
}
@keyframes toast-in {
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
