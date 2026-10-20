<script setup lang="ts">
// 会话流渲染(槽位 stream):消息/工具行(折叠展开)/meta 行。
// 渲染层禁止 v-html(文本一律插值转义);代码块内等宽字体展示。
// 视觉:用户气泡轻盈化(浅蓝底 + 主色文字),工具调用为弱化胶囊,meta/pending 极淡。
import { computed, onUnmounted, ref, watch } from 'vue'
import type { Msg, ToolRow } from '../sse'
import { cleanAnswer, cleanThink } from '../sse'
import type { MetaLine, PendingView } from '../registry'
import { rowsOf, type Row } from '../streamrows'
import DocText from './doc/DocText.vue'
import { previewablePathOf, requestDoc } from '../docstore'

const props = defineProps<{
  frames: Msg[]
  metas: MetaLine[]
  running: boolean
  /** v1.4 可选:进行中的流式增量(批一)。不传 = 不渲染(旧插件/旧调用逐字不变)。 */
  pending?: PendingView
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

// hasPending:三个字段全空的 pending 对象等同于「没有进行中内容」——
// 宿主传 undefined 之外,落定后理论上还可能短暂持有一个空壳。
// 判据看**清理后**的值:只剩一个网关漏出的 `</think>` 尾巴不算「有内容」。
const liveText = computed(() => cleanAnswer(props.pending?.text ?? ''))
const liveThink = computed(() => cleanThink(props.pending?.think ?? ''))
const hasPending = computed(() => !!props.pending && (!!liveText.value || !!liveThink.value || !!props.pending.tool))

// 运行中实时耗时(C2a):工具在跑时显示已耗时(整数秒)。
//
// 为何要一个秒级节拍:秒数得**跳**,不然用户不知道它是活的 ——
// 渲染层自己算(不另收事件),节拍只为触发重绘。
// 空闲不空转:没有正在跑的工具就不挂表。
const now = ref(Date.now())
let elapsedTimer: ReturnType<typeof setInterval> | null = null
watch(
  () => props.pending?.tool?.startedAt,
  (v) => {
    if (v && !elapsedTimer) {
      now.value = Date.now()
      elapsedTimer = setInterval(() => {
        now.value = Date.now()
      }, 1000)
    } else if (!v && elapsedTimer) {
      clearInterval(elapsedTimer)
      elapsedTimer = null
    }
  },
  { immediate: true }
)
onUnmounted(() => {
  if (elapsedTimer) clearInterval(elapsedTimer)
})

// toolElapsed 运行中工具的已耗时(秒);没有起点或已结束 ⇒ 空串。
const toolElapsed = computed(() => {
  const t = props.pending?.tool
  if (!t || !t.startedAt) return ''
  const secs = Math.max(0, Math.floor((now.value - t.startedAt) / 1000))
  return `${secs}s`
})

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
            <!-- 折叠态先给**结论**(宿主算好的一行人话摘要,2026-10-20 B1);
                 摘要缺失(旧日志/未知工具)才回退到截断全文。 -->
            <pre>{{ expanded[r.m.seq] ? r.m.full : (r.m.summary || r.m.full?.slice(0, 400)) }}</pre>
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
    <!-- 进行中的流式增量(批一):宿主此前从不渲染它,只显示一句「正在运行…」直到落定。
         结构与已落定的 assistant 块同形(思维折叠 / 正文 / 工具行),只是末尾多一个光标,
         这样「正在写的这段」与「已写好的那段」看上去是同一类东西。 -->
    <div v-if="pending && hasPending" class="pending-live">
      <details v-if="liveThink" class="think" open>
        <summary>
          <span class="think-tag">思考中</span>
          <span class="think-nums">{{ liveThink.length }} 字</span>
        </summary>
        <div class="think-body">{{ liveThink }}</div>
      </details>
      <div v-if="liveText" class="assistant">
        <!-- settle=false:流式中用 700ms 防抖(见 DocText),否则每个 chunk 改一次文本
             就发一次 /api/doc/render;落定后由已落定那块走 40ms 立即渲染 -->
        <DocText :text="liveText" :settle="false" />
        <span class="caret" aria-hidden="true" />
      </div>
      <div v-if="pending.tool" class="tool-inline mono">
        <span class="tl">{{ pending.tool.name || '…' }}</span>
        <span class="toolargs">{{ pending.tool.args }}</span>
        <!-- C2a:运行中实时耗时(整数秒;结果到达后这行整体消失) -->
        <span v-if="toolElapsed" class="toolelapsed">{{ toolElapsed }}</span>
      </div>
      <!-- C1:工具执行中的实时输出(瞬时 —— 不落盘,翻历史看不到)。
           放在工具胶囊下方:同一个工具的「正在产出」,跟终端里敲命令看回显同感。 -->
      <pre v-if="pending.tool?.live" class="tool-live mono">{{ pending.tool.live }}</pre>
    </div>
    <div v-else-if="running" class="pending-indicator">
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
/* 运行中耗时(C2a):比参数更弱一档(它是过程信息,不是结论) */
.toolelapsed {
  color: var(--fg-dim);
  opacity: 0.75;
  font-variant-numeric: tabular-nums;
}
/* 工具实时输出(C1):弱化到次级信息档 —— 它是过程,不是结论 */
.tool-live {
  margin: 4px 0 0;
  padding: 4px 8px;
  max-height: 160px;
  overflow: auto;
  border-left: 2px solid var(--tool-line);
  color: var(--fg-dim);
  font-size: 12px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
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
  transition: border-color var(--dur-fast) var(--ease-out);
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
/* 进行中的流式块:入场跟已落定消息同一条动效(淡 + 上移),不另起一套 */
.pending-live {
  margin: 0 0 14px;
  animation: msg-in var(--dur-base) var(--ease-out);
}
/* 光标:打字机感只靠这一个元素(不引库、不动正文排版)。1.1s 呼吸。 */
.caret {
  display: inline-block;
  width: 7px;
  height: 15px;
  margin-left: 2px;
  vertical-align: -2px;
  border-radius: 1px;
  background: var(--accent);
  animation: caret-blink 1.1s ease-in-out infinite;
}
@keyframes caret-blink {
  0%,
  100% {
    opacity: 0.25;
  }
  50% {
    opacity: 1;
  }
}
/* 尊重系统减弱动效:光标停闪但**保留可见**(减弱动效不等于内容消失)。 */
@media (prefers-reduced-motion: reduce) {
  .caret {
    animation: none;
    opacity: 1;
  }
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
