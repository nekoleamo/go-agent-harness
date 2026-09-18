<script setup lang="ts">
// 轨迹视图(S-P0-1,槽位 stream 的并列模式):回合 → 步骤 → 工具的观测面。
// 数据全部来自会话事件账本(traj.ts 聚合);时长只取事件 TS,进行中不给时长。
// 视觉:工作台密度(发丝行 + 等宽数字),不重复会话流的内容呈现 —— 这里看的是过程与成本。
import { computed, ref, watch } from 'vue'
import type { TrajModel, TrajTool, TrajTurn } from '../traj'
import { clip, fmtBytes, fmtMs, fmtTok, stepMs, toolMs, turnStats, trajOverview } from '../traj'
import { argsSummary } from '../sse'

const props = defineProps<{
  model: TrajModel
  running: boolean
  // S-P1-2:当前窗口不完整(还有更早历史未加载/已折叠)→ 概览条标注口径,
  // 不能把「窗口内合计」说成「会话全程累计」。
  partial?: boolean
}>()

const turns = computed(() => props.model.turns)
const ov = computed(() => trajOverview(props.model))
const cachePct = computed(() => {
  const total = ov.value.tokens
  if (total <= 0) return 0
  // 缓存命中率口径:缓存 token / 提示 token(不含补全)
  let prompt = 0
  for (const t of turns.value) prompt += t.usage.prompt
  return prompt > 0 ? Math.round((ov.value.cached / prompt) * 100) : 0
})

// 默认展开进行中的回合与最后一个已结束回合(其余折叠:长会话可扫读)
const open = ref<Record<number, boolean>>({})
watch(
  () => [turns.value.length, props.model.cur?.index] as const,
  () => {
    const next: Record<number, boolean> = {}
    const last = turns.value.length
    if (last > 0) next[last] = true
    for (const k of Object.keys(open.value)) {
      const i = Number(k)
      if (open.value[i] && i !== last) next[i] = true // 保留用户手动展开的历史回合
    }
    open.value = next
  },
  { immediate: true },
)
function toggle(i: number): void {
  open.value = { ...open.value, [i]: !open.value[i] }
}

function reasonLabel(t: TrajTurn): string {
  switch (t.reason) {
    case undefined:
      return '进行中'
    case 'done':
      return '完成'
    case 'cancelled':
      return '已取消'
    case 'max_steps':
      return '步数上限'
    default:
      return t.reason
  }
}
function reasonClass(t: TrajTurn): string {
  if (t.reason === undefined) return 'run'
  return t.reason === 'done' ? 'ok' : 'warn'
}

// 概览条点击定位:回合区块按 id 锚点滚动(只在轨迹视图内,不动窗口滚动)
function goto(i: number): void {
  const el = document.getElementById('traj-turn-' + i)
  if (el) el.scrollIntoView({ block: 'start' })
  open.value = { ...open.value, [i]: true }
}
function toolArgs(t: TrajTool): string {
  return argsSummary(t.args)
}
function toolStatus(t: TrajTool): string {
  return t.status === 'err' ? '失败' : t.status === 'ok' ? '成功' : '进行中'
}
</script>

<template>
  <div class="traj">
    <div v-if="turns.length === 0" class="empty">本会话尚无回合记录</div>
    <template v-else>
      <div v-if="props.partial" class="ov-partial">更早回合未加载(在会话流上滚加载);以下统计仅覆盖当前窗口</div>
      <div class="ov">
        <span class="ov-i"><span class="ov-k">回合</span><b class="mono">{{ ov.turns }}</b></span>
        <span class="ov-i"><span class="ov-k">总时长</span><b class="mono">{{ fmtMs(ov.ms) || '进行中' }}</b></span>
        <span class="ov-i"><span class="ov-k">{{ props.partial ? '窗口内 token' : '累计 token' }}</span><b class="mono">{{ fmtTok(ov.tokens) }}</b></span>
        <span v-if="ov.cached > 0" class="ov-i"><span class="ov-k">缓存</span><b class="mono">{{ fmtTok(ov.cached) }} / {{ cachePct }}%</b></span>
        <span v-if="props.partial" class="ov-note" data-tip="更早历史未加载:在会话流里上滚加载">仅当前窗口</span>
        <div class="chips">
          <button v-for="t in turns" :key="t.index" class="chip mono" :class="reasonClass(t)" data-tip="跳到该回合" @click="goto(t.index)">
            #{{ t.index }} {{ fmtMs(turnStats(t).ms) || '进行中' }}
          </button>
        </div>
      </div>

      <section v-for="t in turns" :id="'traj-turn-' + t.index" :key="t.index" class="turn">
        <header class="t-head" :class="{ open: open[t.index] }" @click="toggle(t.index)">
          <span class="t-idx mono">#{{ t.index }}</span>
          <span class="t-reason" :class="reasonClass(t)">{{ reasonLabel(t) }}</span>
          <span class="t-user">{{ clip(t.user, 48) || '(无用户消息)' }}</span>
          <span class="t-metrics mono">
            <span>{{ turnStats(t).steps }} 步</span>
            <span>{{ turnStats(t).tools }} 工具<template v-if="turnStats(t).failed"> · {{ turnStats(t).failed }} 失败</template></span>
            <span v-if="turnStats(t).tokens > 0">{{ fmtTok(turnStats(t).tokens) }} tok</span>
            <span>{{ fmtMs(turnStats(t).ms) || '进行中' }}</span>
          </span>
          <span class="t-caret">{{ open[t.index] ? '▲' : '▼' }}</span>
        </header>

        <div v-if="open[t.index]" class="t-body">
          <div v-if="t.assistant" class="t-ans">
            <span class="k">收尾</span>
            <span class="v">{{ clip(t.assistant, 160) }}</span>
          </div>
          <div v-if="t.usage.requests > 0" class="t-usage mono">
            <span class="k">模型</span><span class="v">{{ t.usage.model || '未知' }}</span>
            <span class="k">请求</span><span class="v">{{ t.usage.requests }}</span>
            <span class="k">提示</span><span class="v">{{ fmtTok(t.usage.prompt) }}</span>
            <span class="k">补全</span><span class="v">{{ fmtTok(t.usage.completion) }}</span>
            <span v-if="t.usage.cached > 0" class="k">缓存</span>
            <span v-if="t.usage.cached > 0" class="v">{{ fmtTok(t.usage.cached) }}</span>
          </div>

          <div v-for="(s, si) in t.steps" :key="s.seq" class="step">
            <div class="s-head mono">
              <span class="s-idx">步骤 {{ si + 1 }}</span>
              <span class="s-ms">{{ fmtMs(stepMs(s)) || '进行中' }}</span>
              <span class="s-cnt">{{ s.toolIds.length }} 次工具</span>
            </div>
            <div v-if="s.toolIds.length === 0" class="s-none">本步骤无工具调用(直接作答)</div>
            <div v-for="id in s.toolIds" :key="id" class="tool">
              <div class="to-head">
                <span class="to-name mono">{{ t.tools[id]?.name }}</span>
                <span class="to-args mono">{{ toolArgs(t.tools[id]) }}</span>
                <span class="to-ms mono">{{ fmtMs(toolMs(t.tools[id])) || '进行中' }}</span>
                <span v-if="t.tools[id]?.outBytes !== undefined" class="to-io mono">出 {{ fmtBytes(t.tools[id]?.outBytes) }}</span>
                <span class="to-st" :class="t.tools[id]?.status">{{ toolStatus(t.tools[id]) }}</span>
              </div>
              <div v-if="t.tools[id]?.error" class="to-err">{{ t.tools[id]?.error }}</div>
            </div>
          </div>
        </div>
      </section>
    </template>
    <div v-if="running" class="pending-indicator"><span class="dot" /> 正在运行…</div>
  </div>
</template>

<style scoped>
.traj {
  width: 100%;
  padding: 4px 0 12px;
}
.empty {
  color: var(--fg-faint);
  font-size: 13px;
  padding: 12px 0;
}

/* —— 固定概览条(贴在内容顶部,长会话滚动时可随时跳回合) —— */
.ov {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 14px;
  padding: 8px 0;
  margin-bottom: 6px;
  background: var(--bg);
  border-bottom: 1px solid var(--line);
}
.ov-i {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
}
.ov-k {
  color: var(--fg-faint);
  font-size: 12px;
}
/* S-P1-2 窗口口径标注:概览条徽标 + 顶部说明行(偏中性色,不抢主信息) */
.ov-note {
  border: 1px solid var(--line);
  border-radius: 999px;
  color: var(--fg-faint);
  font-size: 11px;
  padding: 1px 8px;
}
.ov-partial {
  color: var(--fg-faint);
  font-size: 12px;
  padding: 2px 0 6px;
}
.ov b {
  color: var(--fg);
  font-size: 13px;
  font-weight: 600;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-left: auto;
}
.chip {
  font-size: 11px;
  line-height: 1.7;
  padding: 0 7px;
  border-radius: 999px;
  border: 1px solid var(--line);
  background: var(--bg2);
  color: var(--fg-dim);
  cursor: pointer;
}
.chip:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.chip.run {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--accent);
}
.chip.warn {
  border-color: var(--err-line);
  color: var(--err);
}

/* —— 回合块(无卡片边框:发丝线 + 缩进层级表示归属) —— */
.turn {
  margin-bottom: 4px;
}
.t-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 7px 8px;
  border-radius: var(--r-input);
  cursor: pointer;
  border-bottom: 1px solid var(--line-faint);
}
.t-head:hover {
  background: var(--hover-bg);
}
.t-head.open {
  border-bottom-color: transparent;
}
.t-idx {
  color: var(--fg-dim);
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}
.t-reason {
  flex-shrink: 0;
  font-size: 11px;
  line-height: 1.7;
  padding: 0 6px;
  border-radius: 4px;
  border: 1px solid var(--line);
  color: var(--fg-dim);
  background: var(--bg2);
}
.t-reason.ok {
  border-color: var(--ok-line);
  background: var(--ok-soft);
  color: var(--ok);
}
.t-reason.warn {
  border-color: var(--err-line);
  background: var(--err-soft);
  color: var(--err);
}
.t-reason.run {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--accent);
}
.t-user {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--fg);
  font-size: 13px;
}
.t-metrics {
  flex-shrink: 0;
  display: inline-flex;
  gap: 10px;
  color: var(--fg-faint);
  font-size: 11.5px;
}
.t-caret {
  flex-shrink: 0;
  color: var(--fg-faint);
  font-size: 10px;
}
.t-body {
  padding: 4px 8px 10px 22px; /* 左缩进表达归属层级 */
  border-left: 1px solid var(--line-faint);
  margin-left: 8px;
}
.t-ans,
.t-usage {
  display: flex;
  gap: 8px;
  align-items: baseline;
  font-size: 12px;
  margin: 2px 0 6px;
}
.t-usage {
  gap: 6px;
  flex-wrap: wrap;
}
.t-ans .k,
.t-usage .k {
  color: var(--fg-faint);
  flex-shrink: 0;
}
.t-ans .v {
  color: var(--fg-dim);
  min-width: 0;
}
.t-usage .v {
  color: var(--fg);
  margin-right: 6px;
}
.step {
  margin-top: 6px;
}
.s-head {
  display: flex;
  gap: 10px;
  align-items: baseline;
  color: var(--fg-dim);
  font-size: 12px;
  font-weight: 600;
}
.s-ms,
.s-cnt {
  color: var(--fg-faint);
  font-weight: 400;
  font-size: 11.5px;
}
.s-none {
  color: var(--fg-faint);
  font-size: 12px;
  padding: 2px 0 2px 12px;
}
.tool {
  padding: 3px 0 3px 12px;
  border-left: 1px solid var(--line-faint);
  margin: 3px 0 3px 3px;
}
.to-head {
  display: flex;
  gap: 8px;
  align-items: baseline;
  flex-wrap: wrap;
  font-size: 12px;
}
.to-name {
  color: var(--tool-strong);
  font-weight: 600;
  flex-shrink: 0;
}
.to-args {
  flex: 1;
  min-width: 0;
  color: var(--fg-dim);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.to-ms,
.to-io {
  flex-shrink: 0;
  color: var(--fg-faint);
  font-size: 11px;
}
.to-st {
  flex-shrink: 0;
  font-size: 11px;
  padding: 0 6px;
  border-radius: 4px;
  border: 1px solid var(--line);
  background: var(--bg2);
  color: var(--fg-dim);
}
.to-st.ok {
  border-color: var(--ok-line);
  background: var(--ok-soft);
  color: var(--ok);
}
.to-st.err {
  border-color: var(--err-line);
  background: var(--err-soft);
  color: var(--err);
}
.to-st.called {
  border-color: var(--accent);
  background: var(--accent-soft);
  color: var(--accent);
}
.to-err {
  margin-top: 3px;
  font-size: 12px;
  color: var(--err);
  border: 1px solid var(--err-line);
  background: var(--err-soft);
  border-radius: 6px;
  padding: 4px 8px;
  white-space: pre-wrap;
  word-break: break-word;
}
.pending-indicator {
  color: var(--fg-faint);
  font-size: 12px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding-top: 6px;
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
</style>
