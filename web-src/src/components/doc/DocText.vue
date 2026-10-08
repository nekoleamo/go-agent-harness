<script setup lang="ts">
// 会话流 markdown 渲染(D1 补齐 Web 端与 TUI 的不对等):assistant 文本经 /api/doc/render
// 转为块模型后由 DocBlocks 渲染。服务端解析 → 前端零 markdown 依赖、零 v-html。
// 失败/未装配(503)→ 自动回退纯文本(不改变既有观感)。
// 批二:请求走**模块级共享**取件器(缓存 + 并发闸),见下方 client。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import type { DocBlock } from '../../types'
import DocBlocks from './DocBlocks.vue'
import { docRenderClient as client } from '../../docclient'

const props = defineProps<{ text: string; settle?: boolean; assetBase?: string }>()

// 请求走**模块级共享**取件器(批二)。必须是单例而不是本实例新建:
// `<script setup>` 顶层每个实例都跑,在那里 new 等于「每个 DocText 一份缓存一个闸」。
// 缓存 + 并发闸收敛一处后实例间互相受益:视图往返(StreamView 卸载重挂,几百个 DocText
// 全部重建,实测往返一次额外请求 0)、会话页签来回、上滚分页再滚回。

const blocks = ref<DocBlock[] | null>(null)
const failed = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null
let lastFetched = ''
let inflight = 0

// near 视口内(含 600px 余量)。false 时先渲染纯文本兜底(用户看到的字已经是对的),
// 等快滚到它了再补 md 块模型。
//
// 为何要这一层(实测,2026-10-08):会话流里每条 assistant 消息一个 DocText,长会话首屏
// 一次性挂几百个 ⇒ 几百个 /api/doc/render。即便有缓存(首屏文本各不相同,一个都命中不了)
// 与并发闸(封顶 4,实测首屏要 7s 才全部渲染完),请求量本身也压不下来。
// 屏外的那几百条用户这一屏根本看不见,先给纯文本、滚近了再升级,是最省的一刀。
const near = ref(false)
const host = ref<HTMLElement | null>(null)
let io: IntersectionObserver | null = null

function schedule(): void {
  if (failed.value) return
  if (!near.value) return // 还没进视口:不进定时器,等 intersect 时再排
  if (timer) clearTimeout(timer)
  // 流式中(未落定)拉长防抖,减少请求;落定后立即渲染
  timer = setTimeout(() => void fetchNow(), props.settle === false ? 700 : 40)
}

async function fetchNow(): Promise<void> {
  const t = props.text
  if (!t.trim() || t === lastFetched) return
  const mine = ++inflight
  lastFetched = t
  try {
    const r = await client.render(t)
    // 仅当仍是本次文本且无更新请求时才落盘(防乱序覆盖)
    if (mine === inflight && t === props.text) blocks.value = r.blocks as DocBlock[]
  } catch {
    failed.value = true
    blocks.value = null // 回退纯文本
  }
}

watch(
  () => props.text,
  () => {
    if (props.text.trim() && props.text === lastFetched && blocks.value) return
    schedule()
  },
  { immediate: true },
)

onMounted(() => {
  // 无 IntersectionObserver(老环境)= 退化为「全都要渲染」,与改动前一致。
  if (typeof IntersectionObserver === 'undefined' || !host.value) {
    near.value = true
    schedule()
    return
  }
  io = new IntersectionObserver(
    (es) => {
      if (!es.some((e) => e.isIntersecting)) return
      near.value = true
      io?.disconnect() // 只判一次:进来过就一直要渲染,来回滚动不重复观测
      io = null
      schedule()
    },
    { rootMargin: '600px 0px' },
  )
  io.observe(host.value)
})

onUnmounted(() => {
  if (timer) clearTimeout(timer)
  io?.disconnect()
  io = null
})
</script>

<template>
  <DocBlocks v-if="blocks" :blocks="blocks" :asset-base="assetBase" />
  <div v-else ref="host" class="doc-text-fallback">{{ text }}</div>
</template>

<style scoped>
.doc-text-fallback {
  white-space: pre-wrap;
}
</style>
