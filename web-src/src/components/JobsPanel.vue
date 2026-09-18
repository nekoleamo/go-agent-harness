<script setup lang="ts">
// 后台任务面板(B1):运行中/历史任务列表(状态徽标)+ 输出展开 + 终止。
// 开窗时 3s 轮询(关窗停止,组件常驻、v-if 控显);状态色:running=强调蓝、done=成功绿、failed/killed=错误红。
import { onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import { jobStateLabel } from '../board'
import type { Job } from '../types'

// docked = 在侧栏停靠区里渲染(S-P2-1):去掉固定定位与自身标题行(标签由停靠区提供),
// 其余行为(3s 轮询/展开输出/终止)完全一致 —— 同一份任务视图,不另造一套。
const props = defineProps<{ open: boolean; docked?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const jobs = ref<Job[]>([])
const openId = ref('')
const err = ref('')
let timer: ReturnType<typeof setInterval> | null = null

async function refresh(): Promise<void> {
  try {
    jobs.value = await api.jobs()
    if (!jobs.value.some((j) => j.id === openId.value)) openId.value = ''
  } catch (e) {
    err.value = (e as Error).message
  }
}
function toggle(j: Job): void {
  openId.value = openId.value === j.id ? '' : j.id
}
function fmtTime(ts: string): string {
  const d = new Date(ts)
  return isNaN(d.getTime()) ? '' : d.toLocaleTimeString('zh-CN', { hour12: false })
}
async function kill(j: Job): Promise<void> {
  try {
    await api.jobKill(j.id)
    void refresh()
  } catch (e) {
    err.value = (e as Error).message
  }
}
watch(
  () => props.open,
  (open) => {
    if (open) {
      void refresh()
      timer = setInterval(() => void refresh(), 3000)
    } else if (timer) {
      clearInterval(timer)
      timer = null
    }
  },
  { immediate: true },
)
onUnmounted(() => {
  if (timer) clearInterval(timer)
  timer = null
})
</script>

<template>
  <div v-if="open" class="jobs-panel" :class="{ docked }">
    <div v-if="!docked" class="jp-head">
      <span class="jp-title">后台任务</span>
      <span class="jp-close" data-tip="关闭" @click="emit('close')">×</span>
    </div>
    <div v-if="err" class="jp-err">{{ err }}</div>
    <div v-if="!jobs.length" class="jp-empty">暂无后台任务</div>
    <div v-for="j in jobs" :key="j.id" class="jp-item">
      <div class="jp-row1">
        <span class="jp-state" :class="'st-' + j.state">{{ jobStateLabel(j.state) }}</span>
        <span class="jp-cmd mono">{{ j.command || j.id }}</span>
        <span v-if="j.state === 'running'" class="jp-kill" data-tip="终止任务(需确认)" @click="kill(j)">终止</span>
      </div>
      <div class="jp-time mono">{{ fmtTime(j.created_at) }}</div>
      <pre v-if="openId === j.id" class="jp-out">{{ j.output || '(暂无输出)' }}{{ j.error ? '\n[错误] ' + j.error : '' }}</pre>
      <div class="jp-toggle" data-tip="展开/收起输出" @click="toggle(j)">{{ openId === j.id ? '收起 ▲' : '展开 ▼' }}</div>
    </div>
  </div>
</template>

<style scoped>
.jobs-panel {
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
  animation: jp-in var(--dur-base) var(--ease-out);
}
/* 停靠模式:填满停靠区(无固定定位/无阴影/无入场动画,边框由停靠区提供) */
.jobs-panel.docked {
  position: static;
  width: 100%;
  height: 100%;
  border-left: none;
  box-shadow: none;
  animation: none;
}
@keyframes jp-in {
  from {
    transform: translateX(24px);
    opacity: 0;
  }
  to {
    transform: translateX(0);
    opacity: 1;
  }
}
.jp-head {
  display: flex;
  align-items: center;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line);
}
.jp-title {
  flex: 1;
  font-weight: 600;
  color: var(--fg);
}
.jp-close {
  cursor: pointer;
  color: var(--fg-faint);
  font-size: 16px;
  padding: 2px 6px;
}
.jp-close:hover {
  color: var(--fg);
}
.jp-err {
  color: var(--err);
  font-size: 12px;
  padding: 8px 14px;
}
.jp-empty {
  color: var(--fg-faint);
  font-size: 13px;
  padding: 24px 14px;
  text-align: center;
}
.jp-item {
  padding: 10px 14px;
  border-bottom: 1px solid var(--line-faint);
}
.jp-row1 {
  display: flex;
  align-items: center;
  gap: 8px;
}
.jp-state {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  padding: 1px 7px;
  border-radius: 999px;
}
.jp-state.st-running {
  background: var(--accent-soft);
  color: var(--accent);
}
.jp-state.st-done {
  background: var(--ok-soft);
  color: var(--ok);
}
.jp-state.st-failed,
.jp-state.st-killed {
  background: var(--err-soft);
  color: var(--err);
}
.jp-cmd {
  flex: 1;
  min-width: 0;
  color: var(--fg);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.jp-kill {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--err);
  cursor: pointer;
  border: 1px solid var(--err-line);
  border-radius: 6px;
  padding: 1px 8px;
}
.jp-kill:hover {
  background: var(--err-soft);
}
.jp-time {
  color: var(--fg-faint);
  font-size: 11px;
  margin-top: 3px;
}
.jp-out {
  margin: 8px 0 0;
  border: 1px solid var(--line);
  border-radius: 6px;
  background: var(--bg2);
  color: var(--fg-dim);
  font-size: 12px;
  padding: 6px 10px;
  white-space: pre-wrap;
  word-break: break-word;
}
.jp-toggle {
  margin-top: 6px;
  font-size: 12px;
  color: var(--accent);
  cursor: pointer;
}
.jp-toggle:hover {
  text-decoration: underline;
}
</style>
