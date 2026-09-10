<script setup lang="ts">
// 会话流 markdown 渲染(D1 补齐 Web 端与 TUI 的不对等):assistant 文本经 /api/doc/render
// 转为块模型后由 DocBlocks 渲染。服务端解析 → 前端零 markdown 依赖、零 v-html。
// 失败/未装配(503)→ 自动回退纯文本(不改变既有观感)。
import { onUnmounted, ref, watch } from 'vue'
import { api } from '../../api'
import type { DocBlock } from '../../types'
import DocBlocks from './DocBlocks.vue'

const props = defineProps<{ text: string; settle?: boolean; assetBase?: string }>()

const blocks = ref<DocBlock[] | null>(null)
const failed = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null
let lastFetched = ''
let inflight = 0

function schedule(): void {
  if (failed.value) return
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
    const v = await api.docRender(t)
    // 仅当仍是本次文本且无更新请求时才落盘(防乱序覆盖)
    if (mine === inflight && t === props.text) blocks.value = v.blocks ?? []
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
onUnmounted(() => {
  if (timer) clearTimeout(timer)
})
</script>

<template>
  <DocBlocks v-if="blocks" :blocks="blocks" :asset-base="assetBase" />
  <div v-else class="doc-text-fallback">{{ text }}</div>
</template>

<style scoped>
.doc-text-fallback {
  white-space: pre-wrap;
}
</style>
