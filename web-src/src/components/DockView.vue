<script setup lang="ts">
// S-P2-1 轻量版:侧栏停靠区外壳(标签行 + 拖拽调宽 + 内容插槽)。
//
// 本组件只管「壳」:标签行、拖拽手柄、收起按钮、窄屏退化。面板内容由 App.vue 经默认插槽
// 提供(复用既有视图组件,不为停靠再造一套渲染)。布局状态与容错语义全在 dock.ts(纯函数,
// 可 node --test 直跑),组件不含持久化逻辑。
//
// 交互约束:
// - 拖拽调宽只在**并排模式**提供(!narrow):窄屏是覆盖式抽屉,挤压宽度没有意义。
// - 拖拽用 pointer events + setPointerCapture(鼠标出窗口也不丢事件);
//   指向 dock.ts 的 clampDockWidth 收敛,落盘由 App 在 pointerup 时做一次。
// - 手柄是 role="separator" 且支持 ←/→ 键(±16px),不只服务鼠标。
import { ref } from 'vue'
import type { DockPanel } from '../dock'

const props = withDefaults(
  defineProps<{
    panels: DockPanel[]
    panel: string
    width: number
    /** 窄屏:退化为覆盖式抽屉(无拖拽手柄)。 */
    narrow: boolean
    /**
     * 内容区是否由停靠区提供内边距(缺省 true)。
     * 内容型面板(变更/看板)自己只有纵向内距,横向 gutter 归容器;而列表型面板(后台任务)
     * 的分隔线与悬停底色必须通宽,内距自带 ⇒ 由调用方显式关掉。
     */
    bodyPad?: boolean
  }>(),
  { bodyPad: true },
)

const emit = defineEmits<{
  (e: 'select', id: string): void
  (e: 'close'): void
  /** 拖拽中的实时宽度(未持久化)。 */
  (e: 'resize', width: number): void
  /** 拖拽结束:调用方在此落盘一次。 */
  (e: 'commit'): void
}>()

const dragging = ref(false)
let startX = 0
let startW = 0

function onDown(ev: PointerEvent): void {
  dragging.value = true
  startX = ev.clientX
  startW = props.width
  ;(ev.target as HTMLElement).setPointerCapture?.(ev.pointerId)
  ev.preventDefault()
}
function onMove(ev: PointerEvent): void {
  if (!dragging.value) return
  // 停靠区在右侧:指针左移 = 变宽
  emit('resize', startW + (startX - ev.clientX))
}
function onUp(ev: PointerEvent): void {
  if (!dragging.value) return
  dragging.value = false
  ;(ev.target as HTMLElement).releasePointerCapture?.(ev.pointerId)
  emit('commit')
}
// 键盘调宽:与拖拽同一收敛路径(每次一步 16px)
function onKey(ev: KeyboardEvent): void {
  if (ev.key !== 'ArrowLeft' && ev.key !== 'ArrowRight') return
  ev.preventDefault()
  emit('resize', props.width + (ev.key === 'ArrowLeft' ? 16 : -16))
  emit('commit')
}
</script>

<template>
  <aside class="dock" :class="{ narrow }" :style="{ width: width + 'px' }" data-ui-dock>
    <!-- 拖拽手柄(仅并排模式):左侧 1px 分隔线,悬停/拖拽时变强调色 -->
    <div
      v-if="!narrow"
      class="dk-handle"
      :class="{ on: dragging }"
      role="separator"
      aria-orientation="vertical"
      aria-label="调整侧栏宽度(拖动,或用左右方向键)"
      data-tip="拖动调整宽度(双击复位)"
      @pointerdown="onDown"
      @pointermove="onMove"
      @pointerup="onUp"
      @pointercancel="onUp"
      @dblclick="emit('resize', 380), emit('commit')"
      @keydown="onKey"
      tabindex="0"
    />
    <div class="dk-head">
      <button
        v-for="p in panels"
        :key="p.id"
        class="dk-tab"
        :class="{ on: p.id === panel }"
        :aria-pressed="p.id === panel"
        :data-tip="'在侧栏显示「' + p.label + '」'"
        @click="emit('select', p.id)"
      >
        {{ p.label }}
      </button>
      <span class="dk-close" data-tip="收起侧栏(对话流恢复满宽)" @click="emit('close')">收起</span>
    </div>
    <div class="dk-body" :class="{ 'dk-body-flush': !bodyPad }">
      <slot />
    </div>
  </aside>
</template>

<style scoped>
/* 并排模式:位于 .main 的最后一段,与对话流共享高度(内滚各自独立) */
.dock {
  position: relative;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-left: 1px solid var(--line);
  background: var(--bg2);
}
/* 窄屏:覆盖式抽屉(不挤压对话流;与任务面板抽屉同款视觉层级) */
.dock.narrow {
  position: fixed;
  top: 33px;
  right: 0;
  bottom: 0;
  z-index: 30;
  box-shadow: var(--shadow-dialog);
  animation: dk-in var(--dur-base) var(--ease-out);
}
@keyframes dk-in {
  from {
    transform: translateX(20px);
    opacity: 0;
  }
  to {
    transform: translateX(0);
    opacity: 1;
  }
}
@media (prefers-reduced-motion: reduce) {
  .dock.narrow {
    animation: none;
  }
}
.dk-handle {
  position: absolute;
  left: -3px;
  top: 0;
  bottom: 0;
  width: 7px;
  cursor: col-resize;
  z-index: 2;
}
.dk-handle::after {
  content: '';
  position: absolute;
  left: 3px;
  top: 0;
  bottom: 0;
  width: 1px;
  background: transparent;
  transition: background var(--dur-fast) var(--ease-out);
}
.dk-handle:hover::after,
.dk-handle.on::after,
.dk-handle:focus-visible::after {
  background: var(--accent);
}
.dk-head {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--line-faint);
  flex-shrink: 0;
}
.dk-tab {
  background: none;
  border: 1px solid transparent;
  border-radius: var(--r-input);
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12px;
  padding: 2px 9px;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.dk-tab:hover {
  background: var(--hover-bg);
  color: var(--fg);
}
.dk-tab.on {
  background: var(--sel-bg);
  border-color: var(--sel-border);
  color: var(--fg);
  font-weight: 600;
}
.dk-close {
  margin-left: auto;
  color: var(--fg-faint);
  cursor: pointer;
  font-size: 12px;
  padding: 2px 6px;
  border-radius: var(--r-input);
}
.dk-close:hover {
  background: var(--hover-bg);
  color: var(--fg);
}
.dk-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  /* 内容型面板(变更/看板)的横向 gutter 由容器给:贴边会让文字顶到面板边框上 */
  padding: 8px 12px 16px;
}
/* 列表型面板(后台任务)自带内距,且分隔线要通宽 ⇒ 容器不留 gutter */
.dk-body-flush {
  padding: 0;
}
</style>
