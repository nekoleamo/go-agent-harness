<script setup lang="ts">
// 导出菜单(两项:自包含网页 / 原始 jsonl)。侧栏会话行与顶栏「导出」共用。
//
// 定位用 **fixed**:侧栏滚动区与抽屉的 overflow 会把行内绝对定位弹层裁掉(tip.ts 踩过同坑);
// 所以由打开方给出 (top, right) —— 顶栏按钮在右上、行内图标在左侧,两者都靠 right 对齐。
//
// 关闭通道(点别处 / Esc / 任何滚动)在本组件里,打开按钮必须写 `@click.stop`,
// 否则那次点击会冒泡到 document 把刚打开的菜单立刻关掉。
import { onMounted, onUnmounted } from 'vue'

import type { ExportFmt } from '../sessionexport'

defineProps<{ label: string; top: number; right: number }>()
const emit = defineEmits<{ (e: 'pick', fmt: ExportFmt): void; (e: 'close'): void }>()

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape') emit('close')
}
// 命名后才能在卸载时摘干净(匿名箭头函数 add/remove 不是同一个引用)。
function close(): void {
  emit('close')
}
onMounted(() => {
  document.addEventListener('click', close)
  document.addEventListener('keydown', onKey)
  window.addEventListener('scroll', close, true)
})
onUnmounted(() => {
  document.removeEventListener('click', close)
  document.removeEventListener('keydown', onKey)
  window.removeEventListener('scroll', close, true)
})
</script>

<template>
  <div class="xmenu" role="menu" :style="{ top: top + 'px', right: right + 'px' }">
    <div class="xm-t">{{ label }}</div>
    <div class="xm-i" role="menuitem" @click.stop="emit('pick', 'html')">
      导出网页<span class="xm-s">.html 自包含</span>
    </div>
    <div class="xm-i" role="menuitem" @click.stop="emit('pick', 'jsonl')">
      导出 jsonl<span class="xm-s">原始事件</span>
    </div>
  </div>
</template>

<style scoped>
.xmenu {
  position: fixed;
  z-index: 40;
  min-width: 148px;
  padding: 4px;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  box-shadow: var(--shadow-pop);
}
.xm-t {
  color: var(--fg-faint);
  font-size: 11px;
  padding: 2px 6px 4px;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.xm-i {
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding: 5px 6px;
  border-radius: var(--r-input);
  color: var(--fg);
  font-size: 12px;
  cursor: pointer;
}
.xm-i:hover {
  background: var(--bg2);
  color: var(--accent);
}
.xm-s {
  color: var(--fg-faint);
  font-size: 11px;
}
</style>
