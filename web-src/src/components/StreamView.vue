<script setup lang="ts">
// 会话流渲染(槽位 stream):消息/工具行(折叠展开)/meta 行。
// 渲染层禁止 v-html(文本一律插值转义);代码块内等宽字体展示。
// 视觉:用户气泡轻盈化(浅蓝底 + 主色文字),工具调用为弱化胶囊,meta/pending 极淡。
import { computed, ref } from 'vue'
import type { Msg, ToolRow } from '../sse'
import type { MetaLine } from '../registry'
import { rowsOf, type Row } from '../streamrows'
import DocText from './doc/DocText.vue'
import { previewablePathOf, requestDoc } from '../docstore'

const props = defineProps<{
  frames: Msg[]
  metas: MetaLine[]
  running: boolean
}>()

const expanded = ref<Record<number, boolean>>({})

// 行序模型:把 meta 行按 MetaLine.after(产生时的最后一条消息 seq)织回消息流。
// 为何必须织(2026-10-03):meta 原来统一渲染在**所有消息之后**,回合被停止的报错
// 就永远钉在最底下,后续回答全显示在它上方。交织逻辑在 streamrows.ts(纯函数、带单测)。
const rows = computed<Row[]>(() => rowsOf(props.frames, props.metas ?? []))

function toggle(seq: number): void {
  expanded.value = { ...expanded.value, [seq]: !expanded.value[seq] }
}

function toolCallsOf(m: Msg): ToolRow[] {
  return m.tools ?? []
}

const META_TAG: Record<string, string> = {
  command: '命令',
  error: '错误',
}
// 仅 command/error 带类型标签(摘要/状态为过程信息,斜体灰即可不带标签)
function metaTag(kind: string): string {
  return META_TAG[kind] ?? ''
}

function fmtTs(ts: string): string {
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ''
  return d.toLocaleTimeString('zh-CN', { hour12: false })
}

// 附件预览 URL:/attachments/<Rel>,路径段编码(文件名可含空格等)
function attUrl(rel: string): string {
  const segs = (rel || '').split('/').map((s) => encodeURIComponent(s))
  return '/attachments/' + segs.join('/')
}
</script>

<template>
  <div class="stream">
    <template v-for="(r, ri) in rows" :key="r.t === 'msg' ? 'm' + r.m.seq : 'x' + ri">
      <div v-if="r.t === 'msg'" class="msg" :class="r.m.kind">
        <span class="ts mono">{{ fmtTs(r.m.ts) }}</span>
        <div v-if="r.m.kind === 'user'" class="user mono">
          {{ r.m.text }}
          <div v-if="r.m.atts && r.m.atts.some(a => a.Kind === 'image')" class="atts">
            <img
              v-for="(a, i) in r.m.atts.filter(x => x.Kind === 'image')"
              :key="i"
              class="att-img"
              :src="attUrl(a.Rel)"
              :alt="a.Name"
            />
          </div>
        </div>
        <div v-else-if="r.m.kind === 'assistant'" class="assistant">
          <!-- 思维过程与最终答复**分区块**渲染(2026-10-03 用户反馈:区分度不够):
               左侧竖线 + 淡底 + 弱色斜体 = 「过程」;正文保持正常前景色 = 「结论」。
               默认折叠:思维往往很长,展开与否由用户决定。 -->
          <details v-if="r.m.think" class="think">
            <summary>
              <span class="think-tag">思考过程</span>
              <span class="think-nums">{{ r.m.think.length }} 字</span>
            </summary>
            <div class="think-body">{{ r.m.think }}</div>
          </details>
          <div class="text" :class="{ 'pending': r.m.text === '' }">
            <DocText v-if="r.m.text" :text="r.m.text" />
          </div>
          <div v-for="t in toolCallsOf(r.m)" :key="t.id" class="tool-inline mono">
            <span class="tl">{{ t.name }}</span>
            <span class="toolargs">{{ t.args }}</span>
            <button
              v-if="previewablePathOf(t.args)"
              class="t-prev"
              data-tip="在文档预览工作台打开"
              @click="requestDoc(previewablePathOf(t.args))"
            >
              预览
            </button>
          </div>
        </div>
        <div v-else-if="r.m.kind === 'tool'" class="tool mono">
          <span class="tl">{{ r.m.text }}</span>
          <div class="result" :class="{ err: r.m.err }">
            <pre>{{ expanded[r.m.seq] ? r.m.full : r.m.full?.slice(0, 400) }}</pre>
          </div>
          <button
            v-if="r.m.full && r.m.full.length > 400"
            class="fold"
            data-tip="展开/收起工具结果"
            @click="toggle(r.m.seq)"
          >
            {{ expanded[r.m.seq] ? '收起 ▲' : '展开 ▼' }}
          </button>
        </div>
        <div v-else-if="r.m.kind === 'meta'" class="meta mono">{{ r.m.text }}</div>
      </div>
      <div v-else class="meta mono" :class="r.mm.kind">
        <span v-if="metaTag(r.mm.kind)" class="m-tag" :class="r.mm.kind">{{ metaTag(r.mm.kind) }}</span>
        <span class="m-text">{{ r.mm.text }}</span>
      </div>
    </template>
    <div v-if="running" class="pending-indicator">
      <span class="dot" /> 正在运行…
    </div>
  </div>
</template>

<!-- eslint-disable-next-line vue/no-unused-components -->
<style scoped>
.stream {
  /* 会话内容占满右侧可用宽度(配合 .stream-slot 左右对称留白,不缩窄居中) */
  width: 100%;
  padding: 4px 0 12px;
}
/* 消息入场:一次性 fade + 上移(仅 transform/opacity;流式内文本更新不重触发) */
.msg,
.meta.command,
.meta.error {
  animation: msg-in var(--dur-base) var(--ease-out);
}
.meta {
  animation: msg-in var(--dur-base) var(--ease-out);
}
@keyframes msg-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
.msg {
  margin: 0 0 14px;
  word-break: break-word;
}
.ts {
  font-size: 11px;
  color: var(--fg-faint);
  margin-right: 8px;
  vertical-align: 1px;
}
.user {
  display: inline-block;
  max-width: 92%;
  padding: 7px 14px;
  border-radius: var(--r-bubble) var(--r-bubble) 4px var(--r-bubble);
  background: var(--accent-soft);
  color: var(--fg);
  white-space: pre-wrap;
}
.atts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 6px;
}
.att-img {
  max-width: 180px;
  max-height: 140px;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--line);
  display: block;
}
.assistant .text {
  white-space: pre-wrap;
  color: var(--fg);
  line-height: 1.75;
}
/* 思考过程:左侧竖线 + 淡底,与正文在**形状**上就分开(taste 纪律:形状一致性
   用同一套圆角/边框 token,不用散写裸色)。 */
.think {
  margin: 0 0 10px;
  border-left: 2px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg2);
  padding: 4px 0 4px 10px;
}
.thick summary {
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--fg-faint);
  font-size: 12px;
  list-style: none;
}
.think summary::-webkit-details-marker {
  display: none;
}
.thick-tag {
  font-weight: 600;
  color: var(--fg-dim);
}
.think-nums {
  color: var(--fg-faint);
}
.think-body {
  margin-top: 6px;
  white-space: pre-wrap;
  color: var(--fg-dim);
  font-size: 13px;
  font-style: italic;
  line-height: 1.7;
  word-break: break-word;
}
.assistant .text.pending {
  color: var(--fg-faint);
}
.tool-inline {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  margin-top: 6px;
  background: var(--tool-soft);
  border: 1px solid var(--tool-line);
  border-radius: 6px;
  padding: 1px 8px;
  font-size: 12px;
}
.tl {
  color: var(--tool-strong);
  font-weight: 600;
}
.toolargs {
  color: var(--fg-dim);
}
.tool {
  margin-top: 4px;
}
.tool .tl {
  color: var(--tool-strong);
  font-size: 12px;
  font-weight: 600;
}
.tool .result {
  margin: 6px 0 0;
  border: 1px solid var(--line);
  border-radius: 6px;
  background: var(--bg2);
  color: var(--fg-dim);
  font-size: 12px;
  padding: 6px 10px;
}
.tool .result.err {
  color: var(--err);
  border-color: var(--err-line);
  background: var(--err-soft);
}
.tool .result pre {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  overflow-wrap: anywhere;
}
.fold {
  margin: 4px 0 0;
  background: none;
  border: none;
  color: var(--accent);
  cursor: pointer;
  font-size: 12px;
  padding: 0;
}
.fold:hover {
  text-decoration: underline;
}
.t-prev {
  background: none;
  border: 1px solid var(--line);
  border-radius: 5px;
  color: var(--accent);
  cursor: pointer;
  font-size: 11px;
  padding: 0 6px;
  line-height: 1.6;
}
.t-prev:hover {
  border-color: var(--accent);
}
.meta {
  color: var(--fg-faint);
  font-size: 12px;
  font-style: italic;
}
/* meta 行按类型分色块(命令=代码面板,错误=红块,摘要/状态=灰斜体) */
.meta {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 0 0 8px;
}
.meta.command {
  border: 1px solid var(--line);
  background: var(--bg2);
  border-radius: 6px;
  padding: 6px 10px;
  color: var(--fg-dim);
  font-style: normal;
  white-space: pre-wrap;
  word-break: break-word;
}
.meta.error {
  border: 1px solid var(--err-line);
  background: var(--err-soft);
  border-radius: 6px;
  padding: 6px 10px;
  color: var(--err);
  font-style: normal;
  word-break: break-word;
}
.meta.summary,
.meta.status {
  color: var(--fg-faint);
}
.m-tag {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  font-style: normal;
  padding: 0 6px;
  border-radius: 4px;
  line-height: 1.7;
}
.m-tag.command {
  background: var(--bg3);
  color: var(--fg-dim);
}
.m-tag.error {
  background: var(--err-line);
  color: var(--err);
}
.m-tag.summary,
.m-tag.status {
  background: transparent;
  color: var(--fg-faint);
  padding: 0;
}
.m-text {
  flex: 1;
  min-width: 0;
}
.pending-indicator {
  color: var(--fg-faint);
  font-size: 12px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--accent);
  animation: pulse 1.1s ease-in-out infinite;
}
@keyframes pulse {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}
</style>
