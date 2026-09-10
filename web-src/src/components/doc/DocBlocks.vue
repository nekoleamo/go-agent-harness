<script setup lang="ts">
// 文档块渲染器(D1):结构化块模型 → Vue 组件。
// 纪律:零 v-html(文本一律插值转义)、零 emoji、token 化色彩/圆角/边框;
// 链接仅 http(s)/相对路径,外链 target=_blank + rel="noopener noreferrer";图片只走 /api/doc/asset。
import type { DocBlock, DocCell, DocRun } from '../../types'

const props = defineProps<{
  blocks?: DocBlock[]
  // 资产端点基址(需 path 上下文;会话流渲染无资产,缺失时图片只显示占位)
  assetBase?: string
  compact?: boolean
}>()

function runsOf(b: DocBlock): DocRun[] {
  if (b.runs && b.runs.length) return b.runs
  return b.text ? [{ text: b.text }] : []
}

function assetUrl(a: { id: string }): string {
  if (!props.assetBase) return ''
  return props.assetBase + '&id=' + encodeURIComponent(a.id)
}

// 仅放行 http(s) 与相对路径(v1 安全过滤;后端已过滤,前端再兜一层)
function safeHref(link: string): string {
  const l = (link || '').trim()
  if (/^https?:\/\//i.test(l) || /^mailto:/i.test(l)) return l
  if (/^[a-z][a-z0-9+.-]*:/i.test(l)) return ''
  return l
}

function isExternal(link: string): boolean {
  return /^https?:\/\//i.test(link)
}

function cellClass(c: DocCell): Record<string, boolean> {
  return { num: !!c.numeric, [`al-${c.align || 'left'}`]: true }
}
</script>

<template>
  <div class="doc-blocks" :class="{ compact }">
    <template v-for="(b, i) in blocks || []" :key="i">
      <h1 v-if="b.kind === 'heading' && b.level === 1" class="dh">{{ b.text }}</h1>
      <h2 v-else-if="b.kind === 'heading' && b.level === 2" class="dh">{{ b.text }}</h2>
      <h3 v-else-if="b.kind === 'heading'" class="dh">{{ b.text }}</h3>
      <div v-else-if="b.kind === 'paragraph'" class="dp">
        <template v-for="(r, ri) in runsOf(b)" :key="ri">
          <a
            v-if="r.link"
            :href="safeHref(r.link)"
            :target="isExternal(r.link) ? '_blank' : undefined"
            :rel="isExternal(r.link) ? 'noopener noreferrer' : undefined"
            class="dlink"
            :class="{ b: r.bold, i: r.italic }"
            >{{ r.text }}</a
          >
          <code v-else-if="r.code" class="dcode-inline">{{ r.text }}</code>
          <strong v-else-if="r.bold && r.italic" class="b i">{{ r.text }}</strong>
          <strong v-else-if="r.bold" class="b">{{ r.text }}</strong>
          <em v-else-if="r.italic" class="i">{{ r.text }}</em>
          <s v-else-if="r.strike">{{ r.text }}</s>
          <span v-else>{{ r.text }}</span>
        </template>
      </div>
      <div v-else-if="b.kind === 'list'" class="dli" :style="{ paddingLeft: (b.level || 0) * 16 + 'px' }">
        <span class="dli-mark">·</span><span class="dli-text">{{ b.text }}</span>
      </div>
      <blockquote v-else-if="b.kind === 'quote'" class="dquote">{{ b.text }}</blockquote>
      <pre v-else-if="b.kind === 'code'" class="dpre"><code>{{ b.text }}</code></pre>
      <div v-else-if="b.kind === 'table'" class="dtable-wrap">
        <table class="dtable">
          <thead v-if="b.head && b.head.length">
            <tr>
              <th v-for="(h, hi) in b.head" :key="hi">{{ h }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, ri) in b.rows || []" :key="ri">
              <td v-for="(c, ci) in row" :key="ci" :class="cellClass(c)" :colspan="c.colSpan || 1" :rowspan="c.rowSpan || 1">
                {{ c.text }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <figure v-else-if="b.kind === 'image'" class="dimg">
        <img v-if="b.asset && assetUrl(b.asset)" :src="assetUrl(b.asset)" :alt="b.text || b.asset.name || '图片'" />
        <div v-else class="dimg-ph">
          {{ b.text || '图片' }}
          <span v-if="b.meta && b.meta.remote" class="dimg-note">远程图片不自动加载</span>
        </div>
        <figcaption v-if="b.asset && b.asset.w">{{ b.asset.name }} · {{ b.asset.w }}×{{ b.asset.h }}</figcaption>
      </figure>
      <hr v-else-if="b.kind === 'divider'" class="dhr" />
      <div v-else-if="b.kind === 'page'" class="dmark">第 {{ b.page }} 页</div>
      <div v-else-if="b.kind === 'sheet'" class="dh2">{{ b.text }}</div>
      <div v-else-if="b.kind === 'slide'" class="dh2">幻灯片 {{ b.page }}</div>
      <div v-else-if="b.kind === 'note'" class="dnote">{{ b.text }}</div>
      <div v-else-if="b.kind === 'unsupported'" class="dwarn">{{ b.text }}</div>
      <div v-else-if="b.text" class="dp">{{ b.text }}</div>
    </template>
  </div>
</template>

<style scoped>
.doc-blocks {
  color: var(--fg);
  line-height: 1.75;
  word-break: break-word;
}
.compact .dh,
.compact .dh2 {
  font-size: 15px;
}
.dh {
  font-size: 18px;
  font-weight: 650;
  margin: 14px 0 8px;
  letter-spacing: -0.01em;
}
.dh2 {
  font-size: 15px;
  font-weight: 650;
  margin: 12px 0 6px;
  color: var(--fg-dim);
}
.dp {
  margin: 0 0 8px;
  white-space: pre-wrap;
}
.b {
  font-weight: 650;
}
.i {
  font-style: italic;
}
.dlink {
  color: var(--accent);
  text-decoration: none;
  border-bottom: 1px solid var(--accent-soft);
}
.dlink:hover {
  border-bottom-color: var(--accent);
}
.dcode-inline {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.92em;
  background: var(--bg3);
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 0 4px;
}
.dli {
  display: flex;
  gap: 6px;
  margin: 0 0 4px;
}
.dli-mark {
  color: var(--fg-faint);
}
.dli-text {
  white-space: pre-wrap;
}
.dquote {
  margin: 0 0 8px;
  padding: 4px 0 4px 12px;
  border-left: 3px solid var(--line-strong);
  color: var(--fg-dim);
  white-space: pre-wrap;
}
.dpre {
  margin: 0 0 10px;
  padding: 10px 12px;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  overflow-x: auto;
  font-size: 12.5px;
  line-height: 1.6;
}
.dpre code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: pre;
}
.dtable-wrap {
  overflow-x: auto;
  margin: 0 0 10px;
}
.dtable {
  border-collapse: collapse;
  font-size: 13px;
  min-width: 100%;
}
.dtable th,
.dtable td {
  border: 1px solid var(--line);
  padding: 5px 10px;
  text-align: left;
  white-space: pre-wrap;
  max-width: 420px;
}
.dtable th {
  background: var(--bg2);
  font-weight: 600;
  color: var(--fg-dim);
}
.dtable td.num,
.dtable th.al-right {
  text-align: right;
}
.dtable td.al-center {
  text-align: center;
}
.dimg {
  margin: 0 0 10px;
}
.dimg img {
  max-width: 100%;
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  display: block;
}
.dimg-ph {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  border: 1px dashed var(--line-strong);
  border-radius: var(--r-card);
  color: var(--fg-faint);
  font-size: 12px;
}
.dimg-note {
  color: var(--tool-strong);
}
.dimg figcaption {
  margin-top: 4px;
  font-size: 11px;
  color: var(--fg-faint);
}
.dhr {
  border: none;
  border-top: 1px solid var(--line);
  margin: 12px 0;
}
.dmark {
  color: var(--fg-faint);
  font-size: 11px;
  margin: 10px 0 6px;
  border-top: 1px dashed var(--line);
  padding-top: 6px;
}
.dnote {
  margin: 0 0 8px;
  padding: 6px 10px;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  color: var(--fg-dim);
  font-size: 12.5px;
  white-space: pre-wrap;
}
.dwarn {
  margin: 0 0 8px;
  padding: 6px 10px;
  background: var(--tool-soft);
  border: 1px solid var(--tool-line);
  border-radius: var(--r-card);
  color: var(--tool-strong);
  font-size: 12.5px;
  white-space: pre-wrap;
}
</style>
