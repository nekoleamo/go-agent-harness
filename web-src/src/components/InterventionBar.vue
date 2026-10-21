<script setup lang="ts">
// 介入条(D3 · 2026-10-21):顶部**常驻**的一条「现在轮到人了吗」。
//
// 为什么要有:待审批/待答提问/出错/「你不在时它跑完了」此前分散在弹层、角标、流里的
// meta 行、回底胶囊四处 —— 切到别的标签页再回来,提示可能已经消失(toast 会自动消失),
// 只剩「它好像没理我」的错觉。这条把四态收成一处,常驻不自动消失。
//
// 零新后端契约:四态全从前端已有状态派生(intervention.ts)。
//
// 视觉纪律:单强调色 + 语义色(与全局 token 一致);不引入新色值。
// 形态沿用既有的「顶部提醒条」(vbar / off-banner 同款位置与语气),不做花活。
import type { Intervention } from '../intervention'

const props = defineProps<{
  item: Intervention
}>()
const emit = defineEmits<{ (e: 'act', action: Intervention['action']): void }>()

// 动作按钮的文案(按态给"点了会发生什么",而不是重复状态词)。
const actionLabel = (a: Intervention['action']): string => {
  switch (a) {
    case 'approve':
      return '去确认'
    case 'answer':
      return '去作答'
    case 'error':
      return '看错误'
    case 'latest':
      return '回到最新'
    default:
      return ''
  }
}
</script>

<template>
  <div v-if="props.item.kind" class="ibar" :class="props.item.kind" role="status">
    <span class="ibar-text">{{ props.item.text }}</span>
    <button
      v-if="actionLabel(props.item.action)"
      class="ibar-act"
      data-tip="处理这个待办"
      @click="emit('act', props.item.action)"
    >
      {{ actionLabel(props.item.action) }}
    </button>
  </div>
</template>

<style scoped>
/* 位置:内容列顶部(与 vbar/off-banner 同一层);不遮挡输入区,滚流时也在。 */
.ibar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px;
  margin: 0 0 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg2);
  font-size: 12.5px;
  color: var(--fg-dim);
}
.ibar-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 语义色只用于"要人动手"的两种态;出错用错误色,跑完了用中性(不惊动人)。 */
.ibar.approve,
.ibar.question {
  border-color: var(--accent);
  color: var(--fg);
}
.ibar.error {
  border-color: var(--err);
  color: var(--err);
}
.ibar.done {
  color: var(--fg-dim);
}
.ibar-act {
  flex: none;
  border: 1px solid var(--line-strong);
  border-radius: var(--r-input);
  background: none;
  color: var(--fg);
  cursor: pointer;
  font-size: 12px;
  padding: 3px 12px;
  transition: border-color 0.15s ease, opacity 0.15s ease;
}
.ibar-act:hover {
  border-color: var(--accent);
}
</style>
