<script setup lang="ts">
// S-P2-2 看板视图(第四种投影,与会话流 / 轨迹 / 变更并列)。
// 只做「把已在手的数据聚合成一屏信息面」:卡片内容由 board.ts 从既有投影派生,
// 本组件只管渲染、排序/隐藏交互与跳转动作(数据请求与持久化都在 App.vue)。
// 视觉沿用工作台密度:发丝边框、无阴影、数字等宽、单强调色,语义色只给真正的状态信号。
import type { BoardCard, BoardLayout, BoardTarget } from '../board'

const props = defineProps<{
  cards: BoardCard[]
  layout: BoardLayout
  /** 规范卡数(用于「已隐藏 N 项」与「全部隐藏」判定;不随隐藏变化)。 */
  total: number
}>()

const emit = defineEmits<{
  (e: 'move', id: string, dir: -1 | 1): void
  (e: 'toggle', id: string): void
  (e: 'reset'): void
  (e: 'go', target: BoardTarget): void
}>()

/** 排序按钮的可用性按布局顺序判定(隐藏卡片仍占位,不与可见位置混为一谈)。 */
function canMove(id: string, dir: -1 | 1): boolean {
  const i = props.layout.order.indexOf(id)
  if (i < 0) return false
  const j = i + dir
  return j >= 0 && j < props.layout.order.length
}
</script>

<template>
  <div class="board">
    <!-- 固定概览条(与其他视图同款:滚动时始终可复位布局)。吸附与背景由外层承担:
         内层做成与卡片同形的块(圆角/边框/内距),外层用同色方块盖住圆角缺口与容器内距 ——
         否则内容滚过圆角时会在缺口里露头。 -->
    <div class="ov-bar">
      <div class="ov">
        <span class="ov-t">看板</span>
        <span class="ov-src">全会话聚合 · 数据来自事件账本与既有接口</span>
        <span class="spacer" />
        <span v-if="layout.hidden.length > 0" class="ov-hidden">已隐藏 {{ layout.hidden.length }}/{{ total }}</span>
        <button class="ov-btn" data-tip="恢复默认:全部卡片按默认顺序显示" @click="emit('reset')">恢复默认</button>
      </div>
    </div>

    <p v-if="cards.length === 0" class="empty">
      全部卡片已隐藏。
      <button class="link" @click="emit('reset')">恢复默认</button>
    </p>

    <div v-else class="grid">
      <section v-for="c in cards" :key="c.id" class="card" :data-card="c.id">
        <header class="ch">
          <h3 class="ct">{{ c.title }}</h3>
          <div class="acts">
            <button
              class="ib"
              :disabled="!canMove(c.id, -1)"
              :data-tip="canMove(c.id, -1) ? '上移' : '已在最前'"
              :aria-label="'上移 ' + c.title"
              @click="emit('move', c.id, -1)"
            >
              ↑
            </button>
            <button
              class="ib"
              :disabled="!canMove(c.id, 1)"
              :data-tip="canMove(c.id, 1) ? '下移' : '已在最后'"
              :aria-label="'下移 ' + c.title"
              @click="emit('move', c.id, 1)"
            >
              ↓
            </button>
            <button class="ib" data-tip="隐藏这张卡片" :aria-label="'隐藏 ' + c.title" @click="emit('toggle', c.id)">
              隐藏
            </button>
          </div>
        </header>

        <dl class="rows">
          <div v-for="r in c.rows" :key="r.label" class="row">
            <dt>{{ r.label }}</dt>
            <dd class="mono" :class="r.level">{{ r.value }}</dd>
          </div>
        </dl>

        <p v-if="c.note" class="note">{{ c.note }}</p>
        <button v-if="c.action" class="act" @click="emit('go', c.action.target)">{{ c.action.label }}</button>
      </section>
    </div>
  </div>
</template>

<style scoped>
.board {
  width: 100%;
  padding: 4px 0 16px;
}
.empty {
  color: var(--fg-faint);
  font-size: 13px;
  padding: 12px 0;
}
.link {
  border: 0;
  background: none;
  color: var(--accent);
  font: inherit;
  cursor: pointer;
  padding: 0 2px;
}

/* —— 固定概览条:吸附层 + 与卡片同形的标题块(其他视图仍是发丝线横条:那是列表型
     工作台密度的口径,看板是卡片型,标题跟着卡片走) —— */
.ov-bar {
  position: sticky;
  top: 0;
  z-index: 2;
  margin-bottom: 10px; /* 与 .grid 卡片间距一致 */
  /* 内层是圆角块 ⇒ 两层遮挡都由外层出:同色背景 + 向上 12px 的同色方块
     (吸附时 scrollport 内距带里会露出被卷上去的内容,而 sticky 被夹在内容盒顶,
      只能用阴影带往上补;阴影不参与布局,所以静止位置与原来一致) */
  background: var(--bg);
  box-shadow: 0 -12px 0 0 var(--bg);
}
.ov {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  background: var(--bg);
}
.ov-t {
  font-size: 13px;
  font-weight: 600;
}
.ov-src {
  color: var(--fg-faint);
  font-size: 11px;
}
.spacer {
  flex: 1;
}
.ov-hidden {
  color: var(--fg-faint);
  font-size: 11px;
}
.ov-btn {
  border: 1px solid var(--line);
  background: var(--bg);
  color: var(--fg-dim);
  font: inherit;
  font-size: 11px;
  line-height: 1.7;
  padding: 0 8px;
  border-radius: var(--r-input);
  cursor: pointer;
}
.ov-btn:hover {
  border-color: var(--line-strong);
  color: var(--fg);
}

/* —— 卡片网格:auto-fill + 顶部对齐,避免等高卡片被拉成模板感 —— */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(236px, 1fr));
  gap: 10px;
  align-items: start;
}
.card {
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 10px 12px 12px;
  background: var(--bg);
}
.ch {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ct {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--fg-dim);
  letter-spacing: 0.02em;
}
.acts {
  display: flex;
  gap: 2px;
  margin-left: auto;
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease-out);
}
.card:hover .acts,
.card:focus-within .acts {
  opacity: 1;
}
.ib {
  border: 0;
  background: none;
  color: var(--fg-faint);
  font: inherit;
  font-size: 11px;
  line-height: 1.6;
  padding: 0 4px;
  border-radius: 4px;
  cursor: pointer;
}
.ib:hover:not(:disabled) {
  color: var(--fg);
  background: var(--hover-bg);
}
.ib:disabled {
  opacity: 0.35;
  cursor: default;
}

.rows {
  margin: 8px 0 0;
}
.row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 3px 0;
  border-top: 1px solid var(--line-faint);
}
.row:first-child {
  border-top: 0;
}
dt {
  color: var(--fg-faint);
  font-size: 12px;
}
dd {
  margin: 0 0 0 auto;
  font-size: 13px;
  text-align: right;
}
dd.ok {
  color: var(--ok);
}
dd.warn {
  color: var(--tool);
}
dd.err {
  color: var(--err);
}
dd.off {
  color: var(--fg-faint);
}

.note {
  margin: 8px 0 0;
  color: var(--fg-faint);
  font-size: 11px;
  line-height: 1.5;
}
.act {
  margin-top: 8px;
  border: 0;
  background: none;
  color: var(--accent);
  font: inherit;
  font-size: 12px;
  padding: 0;
  cursor: pointer;
}
.act:hover {
  color: var(--accent-hover);
  text-decoration: underline;
}
</style>
