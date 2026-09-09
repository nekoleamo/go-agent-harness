<script setup lang="ts">
// 审批弹层(槽位 confirm):confirm 帧出现时展示 prompt,允许/拒绝经 REST 回传;
// 无请求时隐藏(透明层不拦截交互)。安全默认:未应答 → 后端按拒绝。
import type { ConfirmRequest } from '../types'

defineProps<{
  request: ConfirmRequest | null
  onAnswer: (ok: boolean) => void
}>()
</script>

<template>
  <div v-if="request" class="mask">
    <div class="dialog">
      <div class="title">操作确认</div>
      <pre class="prompt">{{ request.prompt }}</pre>
      <div class="actions">
        <button class="deny" data-tip="拒绝该操作" @click="onAnswer(false)">拒绝</button>
        <button class="allow" data-tip="允许该操作" @click="onAnswer(true)">允许</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 20;
}
.dialog {
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 18px 20px;
  max-width: 560px;
  width: 90%;
  box-shadow: var(--shadow-dialog);
}
.title {
  color: var(--fg);
  font-weight: 600;
  margin-bottom: 10px;
}
.prompt {
  margin: 0 0 16px;
  white-space: pre-wrap;
  color: var(--fg-dim);
  font-size: 13px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
button {
  padding: 6px 18px;
  border-radius: var(--r-input);
  border: 1px solid var(--line);
  background: none;
  color: var(--fg);
  cursor: pointer;
  font-size: 13px;
  transition: border-color 0.15s ease, background 0.15s ease, transform 0.1s ease;
}
button:active {
  transform: translateY(1px);
}
.deny:hover {
  border-color: var(--err);
  color: var(--err);
}
.allow {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--fg-on-accent);
}
.allow:hover {
  background: var(--accent-hover);
}
</style>
