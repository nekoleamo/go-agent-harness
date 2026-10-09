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
  <!-- 显隐走全局 pop 过渡(style.css):遮罩淡 + 卡片轻微落位;退场不再是硬切 -->
  <Transition name="pop">
    <!-- 点空白处 = 拒绝(与 ConfirmBar 的遮罩点击同语义):不答即拒是本弹层的安全默认,
         遮罩点击不能变成「消失但回合仍干等」 -->
    <div v-if="request" class="mask" @click.self="onAnswer(false)">
      <div class="dialog">
        <div class="title">操作确认</div>
        <pre class="prompt">{{ request.prompt }}</pre>
        <div class="hint">回合会一直等你答复(不会自动超时);想中止可按输入区的「停止」</div>
        <div class="actions">
          <button class="deny" data-tip="拒绝该操作" @click="onAnswer(false)">拒绝</button>
          <button class="allow" data-tip="允许该操作" @click="onAnswer(true)">允许</button>
        </div>
      </div>
    </div>
  </Transition>
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
  margin: 0 0 10px;
  white-space: pre-wrap;
  color: var(--fg-dim);
  font-size: 13px;
}
.hint {
  margin: 0 0 14px;
  color: var(--fg-faint);
  font-size: 12px;
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
  transition: background var(--dur-fast) var(--ease-out);
}
.allow:hover {
  background: var(--accent-hover);
}
</style>
