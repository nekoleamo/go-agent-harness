<script setup lang="ts">
// 产物栏(借鉴 WorkBuddy「结果区」的第一百零五批)。
//
// 形态刻意是**一条栏 + 可展开抽屉**,不是常驻右列:常驻右列会把会话流压窄,
// 而"整页不滚 + 主列不被挤"是既有布局纪律。栏只在**有产物时**出现(见 artifactsVisible)。
//
// 三处复用,零新增通道:预览走既有的 OPEN_DOC_EVENT(→ 文档面板定位该文件);
// 审查走既有的「变更」视图(/diff 命令是同一条路);数据走 changes.ts 的同一份账本。
import { computed, ref } from 'vue'
import { OPEN_DOC_EVENT } from '../docstore'
import { artifactLabel, artifactsBrief, artifactsOf, artifactsVisible, type ArtifactItem } from '../artifacts'
import type { ChangesModel } from '../changes'

const props = defineProps<{
  model: ChangesModel
  // partial 会话窗口不完整(只加载了尾部)→ 概览要标注口径,不能把"窗口内"说成"全会话"
  partial?: boolean
}>()

const emit = defineEmits<{ (e: 'review'): void }>()

const open = ref(false)
const items = computed<ArtifactItem[]>(() => artifactsOf(props.model.files))
const brief = computed(() => artifactsBrief(items.value))
const show = computed(() => artifactsVisible(items.value))

// preview 就地预览:走既有文档面板通道(与模型 doc_open 工具、`/preview` 命令同一条路)。
function preview(path: string): void {
  window.dispatchEvent(new CustomEvent(OPEN_DOC_EVENT, { detail: path }))
}
</script>

<template>
  <div v-if="show" class="abar" data-testid="artifacts-bar">
    <button
      class="abar-toggle"
      :aria-expanded="open"
      data-tip="展开本轮产物清单(只看已落盘的改动)"
      @click="open = !open"
    >
      <span class="abar-caret" :class="{ on: open }" aria-hidden="true">›</span>
      <span class="abar-title">产物</span>
      <span class="abar-brief">{{ brief }}</span>
      <span v-if="partial" class="abar-note">（仅本窗口已加载的部分）</span>
    </button>
    <button class="abar-act" data-tip="打开变更审查视图(逐行 diff)" @click="emit('review')">审查</button>

    <div v-if="open" class="abar-list" role="list">
      <p class="abar-tip">
        只列**已落盘**的改动;被拒绝的写不会出现在这里(它没写盘),请看会话流里的工具结果。
      </p>
      <div v-for="it in items" :key="it.path" class="abar-item" role="listitem">
        <button class="abar-name" :title="it.path" data-tip="在文档面板里预览这个文件" @click="preview(it.path)">
          {{ artifactLabel(it) }}
        </button>
      </div>
      <p v-if="!items.length" class="abar-empty">本轮还没有落盘的改动</p>
    </div>
  </div>
</template>

<style scoped>
/* 单强调色 + 发丝线:与轨迹/变更视图同一套工作台密度(无卡片边框)。 */
.abar {
  border-bottom: 1px solid var(--line);
  padding: 4px 12px;
  font-size: 12px;
}
.abar-toggle {
  all: unset;
  cursor: pointer;
  display: inline-flex;
  gap: 6px;
  align-items: baseline;
  color: var(--fg-dim);
  max-width: calc(100% - 64px);
  transition: color var(--dur-fast) var(--ease-out);
}
.abar-toggle:hover {
  color: var(--fg);
}
.abar-caret {
  display: inline-block;
  transition: transform 0.16s ease;
  color: var(--fg-faint);
}
.abar-caret.on {
  transform: rotate(90deg);
}
.abar-title {
  color: var(--fg);
  font-weight: 600;
}
.abar-brief {
  color: var(--fg-dim);
  font-family: var(--mono, ui-monospace, monospace);
}
.abar-note {
  color: var(--fg-faint);
}
.abar-act {
  all: unset;
  cursor: pointer;
  margin-left: 8px;
  color: var(--accent);
}
.abar-act:hover {
  text-decoration: underline;
}
.abar-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 0 4px;
  max-height: 34vh;
  overflow: auto; /* 抽屉自己滚,主列不受影响 */
}
.abar-tip {
  margin: 0 0 4px;
  color: var(--fg-faint);
  font-size: 11px;
}
.abar-item {
  display: flex;
}
.abar-name {
  all: unset;
  cursor: pointer;
  color: var(--fg-dim);
  font-family: var(--mono, ui-monospace, monospace);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition: color var(--dur-fast) var(--ease-out);
}
.abar-name:hover {
  color: var(--accent);
}
.abar-empty {
  margin: 0;
  color: var(--fg-faint);
}
@media (prefers-reduced-motion: reduce) {
  .abar-caret {
    transition: none;
  }
}
</style>
