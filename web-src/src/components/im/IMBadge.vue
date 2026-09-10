<script setup lang="ts">
// 侧栏 IM 通道徽标(E3):未连接显著提示,已连接显示语义点;点击打开连接卡抽屉。
import { computed } from 'vue'
import { imStatus, phaseLabel, requestPanel } from '../../imstore'

const connected = computed(() => imStatus.value?.phase === 'done')
const label = computed(() => phaseLabel(imStatus.value?.phase))
</script>

<template>
  <span class="im-badge" data-tip="IM 通道连接与配置" @click="requestPanel('im-connect')">
    <span class="nm">IM 通道</span>
    <span class="st" :class="{ on: connected, off: !connected }">{{ label }}</span>
  </span>
</template>

<style scoped>
.im-badge {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  width: 100%;
}
.nm {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.st {
  font-size: 10.5px;
  padding: 1px 6px;
  border-radius: 999px;
  flex-shrink: 0;
}
.st.on {
  background: var(--ok-soft);
  color: var(--ok);
}
.st.off {
  background: var(--tool-soft);
  color: var(--tool-strong);
}
</style>
