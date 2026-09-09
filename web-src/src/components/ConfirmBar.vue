<script setup lang="ts">
// 全局二次确认弹层(App 挂载;Sidebar/InputBar 经 inject askConfirm 触发)。
// 居中展示:遮罩 + 中央对话框(确认弹层醒目,防误操作)。danger = 删除类(红色确认钮)。
defineProps<{
  pending: { title: string; danger?: boolean } | null
}>()
const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()
</script>

<template>
  <div v-if="pending" class="mask" role="alertdialog" aria-label="操作确认" @click.self="emit('cancel')">
    <div class="dialog">
      <div class="t">{{ pending.title }}</div>
      <div class="acts">
        <button class="btn cancel" data-tip="取消" @click="emit('cancel')">取消</button>
        <button class="btn ok" :class="{ danger: pending.danger }" data-tip="确认执行" @click="emit('confirm')">确认</button>
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
  z-index: 80;
  animation: fade-in 0.15s ease;
}
@keyframes fade-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
.dialog {
  display: flex;
  flex-direction: column;
  gap: 16px;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  box-shadow: var(--shadow-dialog);
  padding: 20px 22px 16px;
  max-width: min(420px, calc(100vw - 48px));
  animation: pop 0.18s cubic-bezier(0.16, 1, 0.3, 1);
}
@keyframes pop {
  from {
    opacity: 0;
    transform: translateY(6px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: translateY(0) scale(1);
  }
}
.t {
  color: var(--fg);
  font-size: 14px;
  line-height: 1.6;
  word-break: break-word;
}
.acts {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: none;
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 13px;
  padding: 6px 18px;
  transition: border-color 0.15s ease, color 0.15s ease, background 0.15s ease, transform 0.1s ease;
}
.btn:active {
  transform: translateY(1px);
}
.cancel:hover {
  border-color: var(--line-strong);
  color: var(--fg);
}
.ok {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--fg-on-accent);
}
.ok:hover {
  background: var(--accent-hover);
}
.ok.danger {
  background: var(--err);
  border-color: var(--err);
}
.ok.danger:hover {
  opacity: 0.92;
}
</style>
