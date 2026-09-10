<script setup lang="ts">
// 首启引导(G-E4-R):桌面壳首次启动且 IM 渠道未连接时,提示连接远程控制。
// 纪律:只在桌面壳(?shell=desktop)出现;「不再提示」落宿主共享偏好(gah-state.json,
// TUI/Web 同一份),只提示一次;文案零 emoji、单强调色、CTA 单行。
import { computed } from 'vue'
import { imSpec, imStatus, phaseLabel } from '../imstore'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'connect'): void; (e: 'dismiss'): void; (e: 'later'): void }>()

const channel = computed(() => {
  const ch = imSpec.value?.channel
  if (ch === 'wechat') return '微信'
  if (ch === 'qq') return 'QQ'
  return ch || 'IM'
})
const kind = computed(() => (imSpec.value?.kind === 'qr' ? '扫码连接' : '填写凭证连接'))
const phase = computed(() => phaseLabel(imStatus.value?.phase))
</script>

<template>
  <div v-if="props.open" class="guide-mask" role="dialog" aria-modal="true" aria-label="首启引导">
    <div class="guide">
      <div class="g-head">
        <span class="g-title">连接 IM 远程控制</span>
        <span class="g-badge">{{ channel }} · {{ kind }} · {{ phase }}</span>
      </div>
      <p class="g-lead">
        连接后可在手机上给 gah 派活:消息转发到当前会话,危险操作在手机侧二次确认。
      </p>
      <ol class="g-steps">
        <li>点「去连接」:{{ channel }} {{ kind }},凭证只落在本机 gah-data/</li>
        <li>授权自己:发 <code>/im pair</code> 配对码,群聊用 <code>/im allowg</code> 授权整群</li>
        <li>手机侧直接发消息即可驱动当前工作区</li>
      </ol>
      <p class="g-note">未授权用户默认静默丢弃;密钥不回显、不入日志。</p>
      <div class="g-actions">
        <button class="g-btn primary" @click="emit('connect')">去连接</button>
        <button class="g-btn" @click="emit('later')">稍后</button>
        <button class="g-btn ghost" @click="emit('dismiss')">不再提示</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.guide-mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 60;
}
.guide {
  width: min(520px, calc(100vw - 48px));
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  box-shadow: var(--shadow-dialog);
  padding: 20px 22px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.g-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}
.g-title {
  font-size: 15px;
  font-weight: 650;
  color: var(--fg);
}
.g-badge {
  font-size: 11.5px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: 6px;
  padding: 2px 8px;
  white-space: nowrap;
}
.g-lead {
  margin: 0;
  font-size: 13px;
  color: var(--fg-dim);
  line-height: 1.6;
}
.g-steps {
  margin: 0;
  padding-left: 18px;
  font-size: 12.5px;
  color: var(--fg-dim);
  line-height: 1.9;
}
.g-steps code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  background: var(--bg3);
  border-radius: 5px;
  padding: 1px 5px;
  color: var(--fg);
}
.g-note {
  margin: 0;
  font-size: 11.5px;
  color: var(--fg-faint);
}
.g-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  margin-top: 4px;
}
.g-btn {
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12.5px;
  padding: 6px 14px;
  white-space: nowrap;
}
.g-btn:hover {
  border-color: var(--line-strong);
  color: var(--fg);
}
.g-btn.primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--fg-on-accent);
}
.g-btn.primary:hover {
  background: var(--accent-hover);
  color: var(--fg-on-accent);
}
.g-btn.ghost {
  background: transparent;
  border-color: transparent;
  color: var(--fg-faint);
}
.g-btn.ghost:hover {
  color: var(--fg-dim);
}
</style>
