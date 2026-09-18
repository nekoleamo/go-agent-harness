<script setup lang="ts">
// 变更审查视图(S-P1-1,与「会话流 / 轨迹」并列的第三种模式)。
// 数据来源是会话事件账本里的 file/change(工具写盘前后自取的 unified diff),
// 所以**不依赖 git**:工作区不是仓库、或用户还有别的未提交改动,都不影响这里的口径 ——
// 呈现的只是「本次会话经工具改过的文件与逐行改动」。
// 视觉沿用轨迹视图的工作台密度:无卡片边框,发丝线 + 缩进表达层级;等宽 + 单强调色。
import { computed, ref, watch } from 'vue'
import { changesStats, diffLines, findChange, fmtClock, type ChangesModel, type FileChange } from '../changes'

const props = defineProps<{
  model: ChangesModel
  // 命令定位目标(/diff <路径> → diff 帧的 Path):命中即展开并滚到位
  focus?: string
  focusNonce?: number
  // S-P1-2:当前会话窗口不完整(更早历史未加载)→ 概览条标注口径,
  // 不能把「窗口内改动」当成「本会话全部改动」(且与 `/diff` 命令的服务端全量口径区分开)。
  partial?: boolean
}>()

const files = computed(() => props.model.files)
const st = computed(() => changesStats(props.model))
// 默认展开第一个文件(有改动就直接看到内容;其余折叠便于扫读)
const open = ref<Record<string, boolean>>({})
const hit = ref('')

watch(
  () => files.value.length,
  () => {
    const next: Record<string, boolean> = {}
    const keys = Object.keys(open.value)
    for (const k of keys) if (open.value[k]) next[k] = true
    if (files.value.length > 0) next[files.value[0].key] = true
    open.value = next
  },
  { immediate: true },
)

// 命令定位:切到本视图后由 App 递增 nonce 触发(同一路径可重复定位)
watch(
  () => props.focusNonce,
  () => {
    const p = props.focus
    if (!p) return
    const f = findChange(props.model, p)
    if (!f) return
    open.value = { ...open.value, [f.key]: true }
    hit.value = f.key
    requestAnimationFrame(() => {
      const el = document.getElementById('chg-' + domId(f.key))
      if (el) el.scrollIntoView({ block: 'center' })
    })
  },
)

function toggle(key: string): void {
  open.value = { ...open.value, [key]: !open.value[key] }
}
function goto(f: FileChange): void {
  open.value = { ...open.value, [f.key]: true }
  document.getElementById('chg-' + domId(f.key))?.scrollIntoView({ block: 'center' })
}
// domId 把路径变成可用作 id 的串(id 不允许 '/' 等字符)
function domId(key: string): string {
  return key.replace(/[^A-Za-z0-9._-]/g, '_')
}
function lines(patch: string): { text: string; kind: string }[] {
  return diffLines(patch)
}
// 降级说明:二进制 / 超限无 diff(计数仍真实,绝不静默)
function degrade(h: FileChange['hunks'][number]): string {
  if (h.binary) return '二进制文件,未生成逐行 diff(改动已记录)'
  if (!h.diff && (h.added > 0 || h.removed > 0)) return '文件过大,写盘时未生成逐行 diff(上方增删行数为真实计数)'
  return ''
}
</script>

<template>
  <div class="chg">
    <div v-if="files.length === 0" class="empty">
      本会话还没有捕获到文件改动
      <div class="empty-sub">只记录经工具写盘的操作(file_write / file_append / file_edit);不读 git 状态</div>
    </div>
    <template v-else>
      <div v-if="props.partial" class="ov-partial">更早历史未加载(在会话流上滚加载);以下仅本次窗口捕获的改动</div>
      <div class="ov">
        <span class="ov-i"><span class="ov-k">文件</span><b class="mono">{{ st.files }}</b></span>
        <span class="ov-i"><span class="ov-k">新增</span><b class="mono add">+{{ st.added }}</b></span>
        <span class="ov-i"><span class="ov-k">删除</span><b class="mono del">−{{ st.removed }}</b></span>
        <span class="ov-i"><span class="ov-k">改动</span><b class="mono">{{ st.changes }} 次</b></span>
        <span class="ov-src">来自捕获的写操作 · 不依赖 git</span>
        <div class="chips">
          <button v-for="f in files" :key="f.key" class="chip mono" data-tip="跳到该文件" @click="goto(f)">
            {{ f.key }}
          </button>
        </div>
      </div>

      <section v-for="f in files" :id="'chg-' + domId(f.key)" :key="f.key" class="file" :class="{ hit: hit === f.key }">
        <header class="f-head" @click="toggle(f.key)">
          <span class="f-caret">{{ open[f.key] ? '▾' : '▸' }}</span>
          <span class="f-path mono">{{ f.key }}</span>
          <span v-if="f.created" class="badge">新建</span>
          <span v-if="f.binary" class="badge warn">二进制</span>
          <span v-if="f.truncated" class="badge warn">patch 已截断</span>
          <span class="f-ops mono">{{ f.ops.join(' / ') }}</span>
          <span class="f-cnt mono"><span class="add">+{{ f.added }}</span><span class="del">−{{ f.removed }}</span></span>
          <span class="f-hunks">{{ f.hunks.length }} 次改动</span>
        </header>

        <div v-if="open[f.key]" class="f-body">
          <div v-for="h in f.hunks" :key="h.seq" class="hunk">
            <div class="h-head mono">
              <span class="h-seq">#{{ h.seq }}</span>
              <span class="h-op">{{ h.op }}</span>
              <span v-if="fmtClock(h.ts)" class="h-ts">{{ fmtClock(h.ts) }}</span>
              <span class="h-cnt"><span class="add">+{{ h.added }}</span><span class="del">−{{ h.removed }}</span></span>
            </div>
            <pre v-if="h.diff" class="patch mono"><span v-for="(l, i) in lines(h.diff)" :key="i" :class="'l-' + l.kind">{{ l.text }}</span></pre>
            <div v-if="degrade(h)" class="h-note">{{ degrade(h) }}</div>
            <div v-if="h.truncated" class="h-note">该次改动的 patch 已按预算截断(增删行数不受影响)</div>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.chg {
  width: 100%;
  padding: 4px 0 12px;
}
.empty {
  color: var(--fg-faint);
  font-size: 13px;
  padding: 12px 0;
}
.empty-sub {
  color: var(--fg-faint);
  font-size: 12px;
  opacity: 0.75;
  margin-top: 4px;
}

/* —— 固定概览条(与轨迹视图同款:滚动时始终可跳文件) —— */
.ov {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 14px;
  padding: 8px 0;
  margin-bottom: 6px;
  background: var(--bg);
  border-bottom: 1px solid var(--line);
}
.ov-i {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
}
.ov-k {
  color: var(--fg-faint);
  font-size: 12px;
}
.ov b {
  color: var(--fg);
  font-size: 13px;
  font-weight: 600;
}
.ov-src {
  color: var(--fg-faint);
  font-size: 11px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-left: auto;
  max-width: 60%;
}
.chip {
  font-size: 11px;
  line-height: 1.7;
  padding: 0 7px;
  border-radius: 999px;
  border: 1px solid var(--line);
  background: var(--bg2);
  color: var(--fg-dim);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 240px;
}
.chip:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.add {
  color: var(--ok);
}
.del {
  color: var(--err);
}

/* —— 文件块(无卡片:发丝线分隔 + 缩进层级) —— */
.file {
  margin-bottom: 4px;
  scroll-margin-top: 48px;
}
.f-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 6px 8px;
  border-radius: var(--r-input);
  cursor: pointer;
  border-bottom: 1px solid var(--line-faint);
}
.f-head:hover {
  background: var(--hover-bg);
}
.f-caret {
  color: var(--fg-faint);
  font-size: 10px;
  flex-shrink: 0;
}
.f-path {
  color: var(--fg);
  font-size: 12.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.f-ops,
.f-hunks {
  color: var(--fg-faint);
  font-size: 11px;
  flex-shrink: 0;
}
.f-cnt {
  margin-left: auto;
  font-size: 12px;
  display: inline-flex;
  gap: 8px;
  flex-shrink: 0;
}
.badge {
  font-size: 10.5px;
  line-height: 1.6;
  padding: 0 6px;
  border-radius: 999px;
  border: 1px solid var(--line);
  color: var(--fg-dim);
  flex-shrink: 0;
}
.badge.warn {
  border-color: var(--err-line);
  color: var(--err);
}
.file.hit .f-head {
  box-shadow: inset 2px 0 0 var(--accent);
}

/* —— 逐行 patch(等宽,行首 +/- 轻着色;单 pre + 逐行 span,不用 v-html) —— */
.f-body {
  padding: 6px 8px 10px 22px;
  border-left: 1px solid var(--line-faint);
  margin-left: 9px;
}
.hunk + .hunk {
  margin-top: 10px;
}
.h-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 11px;
  color: var(--fg-faint);
  padding: 2px 0 4px;
}
.h-seq {
  color: var(--fg-dim);
  font-weight: 600;
}
.h-cnt {
  margin-left: auto;
  display: inline-flex;
  gap: 8px;
}
.patch {
  margin: 0;
  padding: 4px 0 4px 8px;
  border-left: 1px solid var(--line);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre;
  overflow-x: auto;
}
.patch span {
  display: block;
}
.l-add {
  color: var(--ok);
  background: var(--ok-soft);
}
.l-del {
  color: var(--err);
  background: var(--err-soft);
}
.l-hunk {
  color: var(--accent);
}
.l-file {
  color: var(--fg-faint);
}
.l-ctx {
  color: var(--fg-dim);
}
.h-note {
  color: var(--fg-faint);
  font-size: 11.5px;
  padding: 2px 0 2px 8px;
  border-left: 1px solid var(--err-line);
}
</style>
