<script setup lang="ts">
// 文档预览工作台(D1,D 组):左侧文件树 + 右侧预览面板(全屏浮层抽屉)。
// 数据全经 /api/doc/*(块模型),渲染零 v-html;未装配(503)时入口已在前端探测中隐藏。
// 纪律:工作区切换必须整树重置(对齐 unidoc 踩坑);Esc 关浮层优先于清空输入(capture 拦截)。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '../../api'
import type { DocEntry, DocView } from '../../types'
import { docRequest } from '../../docstore'
import DocBlocks from './DocBlocks.vue'

const rootPath = ref('.')
const tree = ref<DocEntry[]>([])
const treeWarn = ref<string[]>([])
const filter = ref('')
const curPath = ref('')
const view = ref<DocView | null>(null)
const loadErr = ref('')
const loading = ref(false)
const page = ref(0)
const sheet = ref(0)
const wsKey = ref('')

let stateTimer: ReturnType<typeof setInterval> | null = null

function rawUrl(path: string, dl = false): string {
  return '/api/doc/raw?path=' + encodeURIComponent(path) + (dl ? '&dl=1' : '')
}
function assetBase(path: string): string {
  return '/api/doc/asset?path=' + encodeURIComponent(path)
}

// 树渲染:仅按 path 段数缩进(前端不做二次目录模型,保持单一事实源在后端)
function depthOf(e: DocEntry): number {
  const segs = e.path.split('/').filter((s) => s && s !== '.')
  return Math.max(0, segs.length - 1)
}

const visible = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return tree.value
  return tree.value.filter((e) => e.name.toLowerCase().includes(q))
})

async function loadTree(): Promise<void> {
  try {
    const t = await api.docTree(rootPath.value, 3)
    tree.value = t.entries || []
    treeWarn.value = [...(t.truncated || []), ...(t.warnings || [])]
  } catch (e) {
    treeWarn.value = ['文件树加载失败: ' + (e as Error).message]
  }
}

async function openFile(e: DocEntry): Promise<void> {
  if (e.dir) {
    // 展开:以该目录为根重列(懒加载,避免一次拉全树)
    rootPath.value = e.path
    filter.value = ''
    await loadTree()
    return
  }
  curPath.value = e.path
  page.value = 0
  sheet.value = 0
  await loadPreview()
}

async function loadPreview(): Promise<void> {
  if (!curPath.value) return
  loading.value = true
  loadErr.value = ''
  try {
    const opts: { page?: number; sheet?: number } = {}
    if (page.value > 0) opts.page = page.value
    if (sheet.value > 0) opts.sheet = sheet.value
    view.value = await api.docPreview(curPath.value, opts)
  } catch (e) {
    loadErr.value = (e as Error).message
    view.value = null
  } finally {
    loading.value = false
  }
}

function goRoot(): void {
  rootPath.value = '.'
  curPath.value = ''
  view.value = null
  filter.value = ''
  void loadTree()
}

// 工作区切换 → 整树与预览重置(对齐 unidoc:否则永远显示旧根)
async function pollWorkspace(): Promise<void> {
  try {
    const st = await api.state()
    const key = st.session?.key ?? ''
    if (wsKey.value === '' ) {
      wsKey.value = key
      return
    }
    if (key !== wsKey.value) {
      wsKey.value = key
      rootPath.value = '.'
      curPath.value = ''
      view.value = null
      await loadTree()
    }
  } catch {
    /* 状态不可得时忽略(不影响预览本身) */
  }
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    // capture 阶段拦截 → 优先于 InputBar 的 Esc 清空输入
    e.stopPropagation()
    e.preventDefault()
    close()
  }
}

const emit = defineEmits<{ (e: 'close'): void }>()

function close(): void {
  emit('close') // App 监听 @close 关闭抽屉
}

onMounted(async () => {
  await loadTree()
  await pollWorkspace()
  stateTimer = setInterval(() => void pollWorkspace(), 5000)
  document.addEventListener('keydown', onKey, true)
})
onUnmounted(() => {
  if (stateTimer) clearInterval(stateTimer)
  document.removeEventListener('keydown', onKey, true)
})

watch(page, () => void loadPreview())
watch(sheet, () => void loadPreview())

// 外部预览意图(工具行/命令行):定位到该文件并加载
watch(
  docRequest,
  async (p) => {
    if (!p) return
    curPath.value = p
    page.value = 0
    sheet.value = 0
    await loadPreview()
  },
  { immediate: true },
)

const isPDF = computed(() => view.value?.format === 'pdf')
const isHTML = computed(() => view.value?.format === 'html')
const isImage = computed(() => view.value?.format === 'image')
const fmtSize = (n?: number): string => {
  if (!n) return ''
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}
</script>

<template>
  <div class="doc-panel">
    <div class="dp-head">
      <span class="dp-ws" :title="'路径相对当前工作区'">工作区 {{ wsKey || '(默认)' }}</span>
      <input v-model="filter" class="dp-filter" type="search" placeholder="过滤文件名(/ 聚焦)" aria-label="过滤文件" />
      <button class="dp-btn" data-tip="回到工作区根" @click="goRoot">根目录</button>
      <button class="dp-btn" data-tip="刷新文件树" @click="loadTree()">刷新</button>
    </div>

    <div class="dp-body">
      <div class="dp-tree">
        <div class="dp-root mono">{{ rootPath }}</div>
        <div v-if="treeWarn.length" class="dp-warn">{{ treeWarn.join(' · ') }}</div>
        <ul class="dp-list">
          <li
            v-for="e in visible"
            :key="e.path"
            class="dp-item"
            :class="{ dir: e.dir, cur: e.path === curPath }"
            :style="{ paddingLeft: 6 + depthOf(e) * 14 + 'px' }"
            :title="e.path"
            @click="openFile(e)"
          >
            <span class="dp-name">{{ e.name }}</span>
            <span v-if="!e.dir" class="dp-meta mono">{{ e.format || '?' }}<template v-if="e.size"> · {{ fmtSize(e.size) }}</template></span>
          </li>
        </ul>
        <div v-if="!visible.length" class="dp-empty">没有可列出的文件</div>
      </div>

      <div class="dp-view">
        <div v-if="loadErr" class="dp-err">{{ loadErr }}</div>
        <template v-else-if="view">
          <div class="dp-title">
            <span class="t-name">{{ view.name }}</span>
            <span class="t-meta mono">
              {{ view.format }}<template v-if="view.pages"> · {{ view.pages }} 页</template>
              <template v-if="view.kind"> · {{ view.kind }}</template>
              <template v-if="view.size"> · {{ fmtSize(view.size) }}</template>
            </span>
            <a class="t-dl" :href="rawUrl(view.path || curPath, true)" download>下载</a>
          </div>
          <!-- xlsx 工作表标签(多表切换;隐藏表标注) -->
          <div v-if="view.sheets && view.sheets.length > 1" class="dp-sheets">
            <button
              v-for="(s, si) in view.sheets"
              :key="si"
              class="dp-sheet"
              :class="{ on: si === sheet }"
              :data-tip="s.rows + ' 行 × ' + s.cols + ' 列'"
              @click="sheet = si"
            >
              {{ s.name }}<span v-if="s.hidden" class="h">隐藏</span>
            </button>
          </div>
          <div v-if="view.truncated && view.truncated.length" class="dp-bar trunc">
            已按预算截断:{{ view.truncated.join(' , ') }}
          </div>
          <div v-if="view.warnings && view.warnings.length" class="dp-bar warn">
            <div v-for="(w, i) in view.warnings" :key="i">{{ w }}</div>
          </div>

          <!-- PDF:浏览器原生查看器(Range);挂载失败/不支持时下方下载入口恒在 -->
          <template v-if="isPDF">
            <iframe class="dp-frame" :src="rawUrl(view.path || curPath)" title="PDF 预览"></iframe>
            <div class="dp-note">若此处空白(Linux 桌面壳 WebKitGTK 不支持内嵌 PDF),请用「下载」以本地查看器打开。</div>
          </template>
          <!-- HTML:沙箱 iframe(CSP 已禁脚本;此处再禁 allow-scripts) -->
          <template v-else-if="isHTML">
            <iframe class="dp-frame" sandbox="" :src="'/api/doc/html?path=' + encodeURIComponent(view.path || curPath)" title="HTML 预览"></iframe>
            <div class="dp-note">HTML 以沙箱呈现(脚本/外联均被禁止);需要源码请查看下载文件。</div>
          </template>
          <template v-else-if="isImage">
            <img class="dp-img" :src="rawUrl(view.path || curPath)" :alt="view.name" />
            <DocBlocks :blocks="view.blocks" :asset-base="assetBase(view.path || curPath)" compact />
          </template>
          <template v-else>
            <div v-if="loading" class="dp-loading">加载中…</div>
            <DocBlocks :blocks="view.blocks" :asset-base="assetBase(view.path || curPath)" />
          </template>
        </template>
        <div v-else class="dp-empty">从左侧选择文件预览</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.doc-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}
.dp-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 0 10px;
  border-bottom: 1px solid var(--line);
}
.dp-ws {
  font-size: 12px;
  color: var(--fg-dim);
  background: var(--bg3);
  border-radius: 6px;
  padding: 2px 8px;
}
.dp-filter {
  flex: 1;
  min-width: 0;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  color: var(--fg);
  font-size: 12.5px;
  padding: 5px 10px;
}
.dp-filter:focus {
  outline: none;
  border-color: var(--accent);
}
.dp-btn {
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  color: var(--fg-dim);
  font-size: 12px;
  padding: 5px 10px;
  cursor: pointer;
}
.dp-btn:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.dp-body {
  display: flex;
  gap: 14px;
  flex: 1;
  min-height: 0;
  padding-top: 10px;
}
.dp-tree {
  width: 280px;
  flex: 0 0 280px;
  overflow: auto;
  border-right: 1px solid var(--line);
  padding-right: 8px;
}
.dp-root {
  font-size: 11px;
  color: var(--fg-faint);
  padding: 0 0 6px;
}
.dp-warn {
  font-size: 11px;
  color: var(--tool-strong);
  margin-bottom: 6px;
}
.dp-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.dp-item {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 3px 6px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 12.5px;
}
.dp-item:hover {
  background: var(--bg2);
}
.dp-item.cur {
  background: var(--accent-soft);
  color: var(--accent);
}
.dp-item.dir .dp-name {
  font-weight: 600;
}
.dp-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dp-meta {
  margin-left: auto;
  font-size: 10.5px;
  color: var(--fg-faint);
  flex-shrink: 0;
}
.dp-view {
  flex: 1;
  min-width: 0;
  overflow: auto;
}
.dp-title {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
}
.t-name {
  font-weight: 650;
  font-size: 14px;
}
.t-meta {
  font-size: 11px;
  color: var(--fg-faint);
}
.t-dl {
  margin-left: auto;
  font-size: 12px;
  color: var(--accent);
  text-decoration: none;
}
.t-dl:hover {
  text-decoration: underline;
}
.dp-sheets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 8px;
}
.dp-sheet {
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12px;
  padding: 3px 10px;
}
.dp-sheet:hover {
  border-color: var(--line-strong);
  color: var(--fg);
}
.dp-sheet.on {
  background: var(--accent-soft);
  border-color: var(--accent);
  color: var(--accent);
}
.dp-sheet .h {
  margin-left: 4px;
  font-size: 10px;
  color: var(--fg-faint);
}
.dp-bar {
  font-size: 12px;
  border-radius: var(--r-card);
  padding: 6px 10px;
  margin-bottom: 8px;
  white-space: pre-wrap;
}
.dp-bar.trunc {
  background: var(--bg2);
  border: 1px solid var(--line);
  color: var(--fg-dim);
}
.dp-bar.warn {
  background: var(--tool-soft);
  border: 1px solid var(--tool-line);
  color: var(--tool-strong);
}
.dp-frame {
  width: 100%;
  height: 70vh;
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  background: var(--bg2);
}
.dp-img {
  max-width: 100%;
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  margin-bottom: 10px;
}
.dp-note {
  margin-top: 8px;
  font-size: 11.5px;
  color: var(--fg-faint);
}
.dp-err {
  border: 1px solid var(--err-line);
  background: var(--err-soft);
  color: var(--err);
  border-radius: var(--r-card);
  padding: 8px 12px;
  font-size: 12.5px;
}
.dp-empty,
.dp-loading {
  color: var(--fg-faint);
  font-size: 12.5px;
  padding: 12px 2px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
