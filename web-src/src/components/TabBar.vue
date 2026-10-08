<script setup lang="ts">
// 会话页签条:一个窗口内同时开多个会话,各跑各的。
//
// 纪律(与既有布局一致,不因为"多了个条"就破例):
//   - **横向通栏、固定高度**,不参与主列宽度分配 —— 页签多窄都不挤压会话流;
//   - **不制造滚动**:条本身 overflow-x 横向滚,纵向只占一行;
//   - 色彩/圆角/边框全走 style.css token(组件内不散写裸色值);
//   - 无 emoji 图标;运行状态用**脉冲点**、未读用**计数**,不靠动画堆砌。
import type { TabMeta } from '../tabset'

const props = defineProps<{
  tabs: TabMeta[]
  active: string
  limitReached: boolean
}>()

const emit = defineEmits<{
  (e: 'select', id: string): void
  (e: 'close', id: string): void
  (e: 'new'): void
}>()

// 关闭按钮的可达名(不给文字只有 ×,读屏要有说法)。
function closeLabel(t: TabMeta): string {
  return t.running ? '关闭页签「' + t.title + '」(该会话在后台继续运行)' : '关闭页签「' + t.title + '」'
}
</script>

<template>
  <nav class="tabbar" role="tablist" aria-label="会话页签">
    <div
      v-for="t in props.tabs"
      :key="t.id"
      class="tab"
      :class="{ on: t.id === props.active, run: t.running }"
      role="tab"
      tabindex="0"
      :aria-selected="t.id === props.active"
      :aria-current="t.id === props.active ? 'page' : undefined"
      :data-tip="t.running ? '运行中 · ' + t.title : t.title"
      @click="emit('select', t.id)"
      @keydown.enter="emit('select', t.id)"
      @keydown.space.prevent="emit('select', t.id)"
    >
      <!-- 运行中脉冲点:一眼看出哪个会话在跑(底栏只说本会话,这里说全部) -->
      <span v-if="t.running" class="dot" aria-hidden="true" />
      <!-- 会话角色徽标(该会话自己设了角色才显示;跟随全局的不显示 —— 那是基线) -->
      <span v-if="t.role" class="role" :data-tip="'角色:' + t.role">@</span>
      <!-- 会话级覆盖标记(第一百三十四批):该页签的模型/思考/沙箱/审批至少一项没跟随全局。
           形状选**方块**而不是圆点:条上已有两个圆点(运行脉冲 / 未读),换颜色在单强调色
           纪律下分不开,换形状才能一眼分清三件事。 -->
      <span
        v-if="t.custom"
        class="custom"
        aria-label="本会话有独立于全局的设置"
        data-tip="本会话有独立于全局的设置(模型/思考/沙箱/审批),在输入框旁的「本会话设置」里改"
      />
      <span class="t-title">{{ t.title }}</span>
      <span v-if="t.unread" class="badge" aria-label="有未读">·</span>
      <!-- 关闭是真 button(不塞在 button 里嵌套交互元素):嵌套会被浏览器重组,
           关闭事件随之失效;外层改用 role=tab 的 div 以保持语义与键盘可达。 -->
      <button
        class="x"
        :aria-label="closeLabel(t)"
        :data-tip="closeLabel(t)"
        @click.stop="emit('close', t.id)"
      >×</button>
    </div>
    <button
      class="add"
      :disabled="props.limitReached"
      :data-tip="props.limitReached ? '页签已达上限(8):先关掉一个' : '新建会话页签'"
      @click="emit('new')"
    >
      ＋
    </button>
  </nav>
</template>

<style scoped>
.tabbar {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 10px;
  background: var(--bg2);
  border-bottom: 1px solid var(--line);
  overflow-x: auto; /* 页签多了横向滚,不换行、不推高页面 */
  flex: 0 0 auto;
}
.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 220px;
  padding: 4px 6px 4px 10px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: var(--fg-dim);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
  /* 新页签入场(批四 4b):v-for keyed ⇒ 已有页签不重播,只有新建那个淡入。
     与消息入场同一条曲线与时长。 */
  animation: tab-in var(--dur-base) var(--ease-out);
}
@keyframes tab-in {
  from {
    opacity: 0;
    transform: translateY(-3px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
.tab:hover {
  background: var(--hover-bg);
}
.tab.on {
  background: var(--surface);
  border-color: var(--sel-border);
  color: var(--fg);
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  animation: tab-pulse 1.4s var(--ease-out) infinite;
}
@keyframes tab-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .dot { animation: none; }
}
.role {
  color: var(--accent);
  font-weight: 600;
  font-size: 12px;
}
/* 会话级覆盖标记:实心小方块。尺寸略小于运行点(6px),不抢它的注意力。 */
.custom {
  width: 4px;
  height: 4px;
  flex-shrink: 0;
  border-radius: 1px;
  background: var(--accent);
}
.t-title {
  overflow: hidden;
  text-overflow: ellipsis;
}
.badge {
  color: var(--accent);
  font-weight: 600;
}
.x {
  padding: 0 4px;
  border: none;
  background: transparent;
  border-radius: 4px;
  color: var(--fg-faint);
  font: inherit;
  line-height: 1;
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.x:hover {
  background: var(--bg3);
  color: var(--fg);
}
.add {
  padding: 2px 8px;
  border: 1px solid var(--line);
  border-radius: 6px;
  background: var(--surface);
  color: var(--fg-dim);
  font: inherit;
  cursor: pointer;
}
.add:disabled {
  color: var(--fg-faint);
  cursor: not-allowed;
}
</style>