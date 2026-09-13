<script setup lang="ts">
// 左侧抽屉:工作区固定展示(顶部独立区,不归类到历史)+ 会话历史(内容省略版预览)。
// 全部增删改操作(切换会话/工作区、新建、改名、删除)经全局确认条二次确认(inject askConfirm)。
// 切换/删除/改名后 emit session-changed(宿主重建 SSE 重放);列表经 refreshKey 或手动刷新重拉。
import { inject, nextTick, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { extraPanels, sidebarActions } from '../registry'
import type { ExtensionReg } from '../registry'
import type { AskConfirm, SessionInfo, WorkspaceInfo } from '../types'

const props = defineProps<{
  refreshKey: number
  curSession?: string
  curKey?: string
}>()

const emit = defineEmits<{ (e: 'session-changed'): void; (e: 'open-panel', key: string): void }>()

const open = ref(true)
const sessions = ref<SessionInfo[]>([])
const workspaces = ref<WorkspaceInfo[]>([])
const err = ref('')
// 会话名内联编辑态({ id, val });保存仍走确认条
const editing = ref<{ id: string; val: string } | null>(null)
const ask = inject<(a: AskConfirm) => void>('askConfirm')

async function refresh(): Promise<void> {
  err.value = ''
  const [ss, ws] = await Promise.all([api.sessions(), api.workspaces()])
  sessions.value = ss
  workspaces.value = ws
}

function label(s: SessionInfo): string {
  if (s.Name) return s.Name
  return s.ID ? '#' + s.ID : '主会话'
}

// detailOf 展示行降级链(F3 §5.4):概述 → 内容预览 → （空会话）
function detailOf(s: SessionInfo): string {
  if (s.Summary) return s.Summary
  return s.Preview || '（空会话）'
}

// togglePin 置顶/取消置顶(F2;无需二次确认——可逆且无副作用,列表即时刷新)
async function togglePin(s: SessionInfo): Promise<void> {
  try {
    await api.sessionPin(s.ID, !s.Pinned)
    await refresh()
  } catch (e) {
    err.value = (e as Error).message
  }
}

// summarize 生成/更新概述(F3;会调用模型 → 二次确认提示隐私)
function summarize(s: SessionInfo): void {
  guard('生成会话概述会把该会话内容发送给当前模型,继续？', false, () => {
    void (async () => {
      try {
        await api.sessionSummary(s.ID, true)
        await refresh()
      } catch (e) {
        err.value = (e as Error).message
      }
    })()
  })
}
function fmtDir(dir: string): string {
  const i = dir.lastIndexOf('/')
  return i >= 0 ? dir.slice(i + 1) : dir
}
function fmtTime(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' })
}

// guard 统一二次确认入口(无注入兜底直执行,防御性)
function guard(title: string, danger: boolean, run: () => void): void {
  if (!ask) {
    run()
    return
  }
  ask({ title, danger, run })
}

async function doSwitchSession(id: string): Promise<void> {
  try {
    await api.sessionSwitch(id)
    emit('session-changed')
  } catch (e) {
    err.value = (e as Error).message
  }
}
function switchSession(s: SessionInfo): void {
  guard('切换到会话「' + label(s) + '」？', false, () => void doSwitchSession(s.ID))
}
async function doNewSession(): Promise<void> {
  try {
    await api.sessionNew()
    emit('session-changed')
  } catch (e) {
    err.value = (e as Error).message
  }
}
function newSession(): void {
  guard('新建会话？', false, () => void doNewSession())
}

// —— 会话改名(内联编辑,保存时二次确认;后端 rename 仅作用当前会话 → 先切目标会话再改名)—
function startEdit(s: SessionInfo): void {
  editing.value = { id: s.ID, val: s.Name }
}
async function doSaveName(s: SessionInfo): Promise<void> {
  const v = editing.value?.val.trim() ?? ''
  editing.value = null
  if (!v) return
  try {
    await api.sessionSwitch(s.ID) // 同名/当前会话幂等
    await api.sessionRename(v)
    emit('session-changed')
    void refresh()
  } catch (e) {
    err.value = (e as Error).message
  }
}
// onNameEnter 改名输入框 Enter 保存:输入法组字期间不触发(isComposing/keyCode 229),
// 否则中文会话名上屏即被当保存。
function onNameEnter(e: KeyboardEvent, s: SessionInfo): void {
  if (e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  saveName(s)
}
function saveName(s: SessionInfo): void {
  const v = editing.value?.val.trim() ?? ''
  if (!v) {
    editing.value = null
    return
  }
  const cur = label(s)
  guard('重命名会话「' + cur + '」为「' + v + '」？', false, () => void doSaveName(s))
}

// —— 删除(仅删记录,不删文件夹;删除类标红)—
async function doDeleteSession(s: SessionInfo): Promise<void> {
  try {
    await api.sessionDelete(s.ID)
    emit('session-changed') // 删除当前会话后端已新建空会话承接
    window.dispatchEvent(new Event('gah:sessions-changed'))
    void refresh()
  } catch (e) {
    err.value = (e as Error).message
  }
}
function exportSession(s: SessionInfo): void {
  // 会话导出:后端原始 jsonl 下载(a.download 配合 Content-Disposition)
  const a = document.createElement('a')
  a.href = '/api/sessions/' + encodeURIComponent(s.ID || '') + '/export'
  a.download = 'session-' + (s.ID || 'main') + '.jsonl'
  document.body.appendChild(a)
  a.click()
  a.remove()
}
function deleteSession(s: SessionInfo): void {
  // 删除当前会话时后端自动新建空会话承接(删除后界面干净无旧内容回放)
  const isCur = (s.ID || '') === (props.curSession || '')
  guard('删除会话「' + label(s) + '」？' + (isCur ? '删除后开启新会话' : '仅删该会话记录'), true, () => void doDeleteSession(s))
}

// —— 工作区切换/删除 —
async function doSwitchWorkspace(w: WorkspaceInfo): Promise<void> {
  try {
    await api.control({ workspace: w.dir }) // dir 语义:后端 SwitchDir(Chdir + key 派生)
    emit('session-changed')
  } catch (e) {
    err.value = (e as Error).message
  }
}
function switchWorkspace(w: WorkspaceInfo): void {
  guard('切换到工作区「' + (fmtDir(w.dir) || w.key) + '」？', false, () => void doSwitchWorkspace(w))
}
async function doForgetWorkspace(w: WorkspaceInfo): Promise<void> {
  try {
    await api.workspaceForget(w.key)
    void refresh()
  } catch (e) {
    err.value = (e as Error).message
  }
}
function forgetWorkspace(w: WorkspaceInfo): void {
  guard('删除工作区记录「' + (fmtDir(w.dir) || w.key) + '」？(不删文件夹)', true, () => void doForgetWorkspace(w))
}

// —— 打开文件夹作为工作区(桌面/Web 通用) ——
// 浏览器安全模型下 <input type=file> 只能拿到文件名拿不到绝对路径,所以这里用路径输入;
// 后端 SwitchDir 会 os.Chdir + 记入工作区历史 + 新建空会话 + 通知宿主同步沙箱 root。
const addingWs = ref(false)
const wsPath = ref('')
const wsInput = ref<HTMLInputElement | null>(null)
function startAddWs(): void {
  addingWs.value = true
  wsPath.value = ''
  void nextTick(() => wsInput.value?.focus())
}
function cancelAddWs(): void {
  addingWs.value = false
  wsPath.value = ''
}
function submitAddWs(): void {
  const dir = wsPath.value.trim().replace(/^"(.*)"$/, '$1') // 资源管理器「复制路径」带引号
  if (!dir) return
  guard('打开工作区「' + dir + '」？(会切换到该目录并新建会话)', false, () => void doAddWs(dir))
}
async function doAddWs(dir: string): Promise<void> {
  try {
    await api.control({ workspace: dir })
    cancelAddWs()
    await refresh()
    emit('session-changed')
  } catch (e) {
    err.value = (e as Error).message
  }
}

onMounted(() => {
  void refresh()
})
watch(() => props.refreshKey, () => void refresh())
defineExpose({ refresh })
</script>

<template>
  <div class="sidebar" :class="{ closed: !open }">
    <div v-if="open" class="panel">
      <div class="head">
        <span class="title">会话</span>
        <button class="toggle" data-tip="收起侧栏" @click="open = false">⇤</button>
      </div>

      <!-- 工作区:固定展示(独立区,不归入历史) -->
      <div class="sec ws-sec">
        <div class="sec-h">
          <span>工作区</span>
          <span class="acts">
            <span class="act" data-tip="打开文件夹作为工作区(输入绝对路径)" @click="startAddWs">＋ 打开</span>
            <span class="act" data-tip="刷新列表" @click="refresh">↻</span>
          </span>
        </div>
        <!-- 打开文件夹:输入绝对路径 → 切换工作区(浏览器拿不到文件夹路径,故不用选择器) -->
        <div v-if="addingWs" class="ws-add">
          <input
            ref="wsInput"
            v-model="wsPath"
            class="ws-input"
            type="text"
            spellcheck="false"
            placeholder="文件夹绝对路径,如 D:\work\proj"
            @keydown.enter="submitAddWs"
            @keydown.esc="cancelAddWs"
          />
          <div class="ws-add-ops">
            <button class="ws-btn" @click="submitAddWs">打开</button>
            <button class="ws-btn ghost" @click="cancelAddWs">取消</button>
          </div>
        </div>
        <div class="items">
          <div
            v-for="w in workspaces"
            :key="w.key"
            class="item ws-item"
            :class="{ cur: w.key === curKey }"
            :aria-current="w.key === curKey ? 'location' : undefined"
            data-tip="切换到该工作区(需确认)"
            @click="switchWorkspace(w)"
          >
            <div class="row1">
              <span class="nm">{{ fmtDir(w.dir) || w.key }}</span>
              <span v-if="w.key === curKey" class="ws-dot" aria-hidden="true" />
              <span class="ops">
                <span class="op del" data-tip="删除记录(不动文件夹)" @click.stop="forgetWorkspace(w)">×</span>
              </span>
            </div>
            <div class="sub mono">{{ w.key }}</div>
          </div>
          <div v-if="!workspaces.length" class="empty">暂无记录</div>
        </div>
      </div>

      <!-- 会话历史(内容省略版预览;独立滚动区) -->
      <div class="sec session-sec">
        <div class="sec-h">
          <span>历史会话</span>
          <span class="act" data-tip="新建会话(需确认)" @click="newSession">＋ 新建</span>
        </div>
        <div class="items">
          <div
            v-for="s in sessions"
            :key="s.ID || '(main)'"
            class="item session-item"
            :class="{ cur: (s.ID || '') === (curSession || ''), pinned: s.Pinned }"
            :aria-current="(s.ID || '') === (curSession || '') ? 'true' : undefined"
            data-tip="切换到该会话(需确认)"
            @click="switchSession(s)"
          >
            <div class="row1">
              <input
                v-if="editing && editing.id === s.ID"
                v-model="editing.val"
                class="name-input"
                placeholder="会话名"
                data-tip="Enter 保存(需确认)"
                @click.stop
                @keydown.enter="onNameEnter($event, s)"
                @keydown.esc="editing = null"
                @blur="editing = null"
              />
              <span v-else class="nm">
                <span v-if="s.Pinned" class="pin-mark" data-tip="已置顶">★</span>{{ label(s) }}
              </span>
              <span class="ops">
                <span v-if="editing && editing.id === s.ID" class="op" data-tip="保存改名(需确认)" @click.stop="saveName(s)">✓</span>
                <template v-else>
                  <span class="op" :data-tip="s.Pinned ? '取消置顶' : '置顶会话'" @click.stop="togglePin(s)">{{ s.Pinned ? '☆' : '★' }}</span>
                  <span class="op" data-tip="生成/更新概述(调用模型)" @click.stop="summarize(s)">⟳</span>
                  <span class="op" data-tip="修改会话名(需确认)" @click.stop="startEdit(s)">✎</span>
                  <span class="op" data-tip="导出会话 jsonl" @click.stop="exportSession(s)">⤓</span>
                  <span class="op del" data-tip="删除会话(仅删记录,需确认)" @click.stop="deleteSession(s)">×</span>
                </template>
              </span>
            </div>
            <div class="preview" :class="{ empty: !detailOf(s) }">{{ detailOf(s) }}</div>
            <div class="sub mono">
              {{ fmtTime(s.MTime) }}<template v-if="s.Frames >= 0"> · {{ s.Frames }} 条</template>
              <span v-if="s.SummaryState === 'stale'" class="stale"> · 待更新</span>
              <span v-else-if="s.SummaryState === 'unavailable'" class="stale"> · 概述不可用</span>
            </div>
          </div>
          <div v-if="!sessions.length" class="empty">无会话</div>
        </div>
      </div>

      <!-- v2 扩展点:侧栏动作(插件注入) -->
      <div v-if="sidebarActions().length" class="sec">
        <div class="sec-h">
          <span>插件动作</span>
        </div>
        <div class="items">
          <div v-for="a in sidebarActions()" :key="a.key" class="item act-item">
            <component :is="a.component" />
          </div>
        </div>
      </div>

      <!-- v2 扩展点:附加面板入口(插件声明 extra-panel;点击开抽屉) -->
      <div v-if="extraPanels().length" class="sec">
        <div class="sec-h">
          <span>附加面板</span>
        </div>
        <div class="items">
          <div
            v-for="p in (extraPanels() as ExtensionReg[])"
            :key="p.key"
            class="item act-item"
            data-tip="打开面板"
            @click="emit('open-panel', p.key)"
          >
            <span class="nm">{{ p.title || '面板' }}</span>
          </div>
        </div>
      </div>

      <div v-if="err" class="err">{{ err }}</div>
    </div>

    <button v-else class="handle" data-tip="展开侧栏" @click="open = true">☰</button>
  </div>
</template>

<style scoped>
.sidebar {
  flex-shrink: 0;
  border-right: 1px solid var(--line);
  background: var(--bg2);
  transition: width 0.18s ease;
}
.sidebar.closed {
  width: 28px;
  border-right: none;
}
.panel {
  width: 264px;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding: 10px 6px 8px;
  overflow: hidden; /* 整体不再滚:工作区与历史会话各自独立滚动 */
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 2px 8px 10px;
  flex-shrink: 0;
}
.title {
  font-weight: 600;
  color: var(--fg);
  font-size: 13px;
}
.toggle,
.handle {
  background: none;
  border: none;
  color: var(--fg-faint);
  cursor: pointer;
  font-size: 13px;
  padding: 2px 6px;
  border-radius: 6px;
  transition: color 0.15s ease, background 0.15s ease;
}
.toggle:hover,
.handle:hover {
  color: var(--accent);
  background: var(--accent-soft);
}
.handle {
  width: 100%;
  height: 100%;
  font-size: 13px;
  writing-mode: vertical-rl;
}
.sec {
  margin-bottom: 12px;
}
/* 工作区:顶部独立区,超出自行滚动(flex-shrink 0 + 内滚),不占会会话滚动 */
.ws-sec {
  flex-shrink: 0;
  max-height: 250px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--line-faint);
}
.ws-sec .items {
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}
/* 历史会话:占满剩余高度,独立滚动 */
.session-sec {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.session-sec .items {
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}
.sec-h {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 2px 8px 6px;
  color: var(--fg-faint);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
}
.act {
  color: var(--accent);
  cursor: pointer;
  font-weight: 400;
  font-size: 12px;
  letter-spacing: 0;
}
/* 分组标题右侧动作区(多个 act 并排,不再被 space-between 拆到两端) */
.acts {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: auto;
}
/* 打开文件夹:路径输入 + 打开/取消(内联在「工作区」区内) */
.ws-add {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 2px 8px 8px;
}
.ws-input {
  width: 100%;
  box-sizing: border-box;
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--line);
  border-radius: var(--r-sm, 6px);
  padding: 5px 8px;
  font-size: 12px;
  font-family: inherit;
}
.ws-input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px var(--accent-soft);
}
.ws-add-ops {
  display: flex;
  gap: 6px;
}
.ws-btn {
  border: 1px solid var(--accent);
  background: var(--accent);
  color: #fff;
  border-radius: var(--r-sm, 6px);
  font-size: 12px;
  padding: 3px 10px;
  cursor: pointer;
}
.ws-btn.ghost {
  background: none;
  color: var(--fg-faint);
  border-color: var(--line);
}
.items {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.item {
  position: relative;
  padding: 7px 9px 7px 11px;
  border-radius: 8px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border: 1px solid transparent;
  transition: background 0.15s ease;
}
.item:hover {
  background: var(--hover-bg);
}
/* 会话:当前项 = 卡片(填充 + 描边)+ 圆角左边条;名称加粗(排版参与选中表达) */
.session-item.cur {
  background: var(--sel-bg);
  border-color: var(--sel-border);
  padding-left: 10px;
}
.session-item.cur::before,
.ws-item.cur::before {
  content: '';
  position: absolute;
  left: -1px;
  top: 8px;
  bottom: 8px;
  width: 3px;
  background: var(--sel-rail);
  border-radius: 0 2px 2px 0;
}
.session-item.cur .nm {
  font-weight: 600;
}
/* 工作区:当前项 = 身份标识(竖条 + 强调色 + 圆点),不铺底(与会话选中语义分离) */
.ws-item.cur .nm {
  color: var(--accent);
  font-weight: 600;
}
.ws-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--sel-rail);
  flex-shrink: 0;
}
/* 置顶:★ 常显(表达状态而非操作) */
.pin-mark {
  color: var(--tool-strong);
  margin-right: 4px;
}
.stale {
  color: var(--tool-strong);
}
.row1 {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  min-width: 0;
}
.nm {
  font-size: 13px;
  color: var(--fg);
  word-break: break-all;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.name-input {
  flex: 1;
  min-width: 0;
  background: var(--bg);
  border: 1px solid var(--accent);
  border-radius: 6px;
  color: var(--fg);
  font-size: 12px;
  padding: 2px 6px;
  outline: none;
}
.preview {
  font-size: 12px;
  color: var(--fg-dim);
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
}
.preview.empty {
  color: var(--fg-faint);
  font-style: italic;
}
.sub {
  font-size: 11px;
  color: var(--fg-faint);
  word-break: break-all;
}
.ops {
  display: inline-flex;
  gap: 2px;
  flex-shrink: 0;
  opacity: 0;
  transition: opacity 0.12s ease;
}
.item:hover .ops,
.item:focus-within .ops,
.name-input + .ops {
  opacity: 1;
}
/* 选中行不再常显操作图标(F1:行内噪声收敛;hover/focus 才出) */
.op {
  background: none;
  border: none;
  color: var(--fg-faint);
  cursor: pointer;
  font-size: 12px;
  padding: 0 3px;
  border-radius: 4px;
  line-height: 1.6;
}
.op:hover {
  color: var(--accent);
  background: var(--accent-soft);
}
.op.del:hover {
  color: var(--err);
  background: var(--err-soft);
}
.empty {
  color: var(--fg-faint);
  font-size: 12px;
  padding: 4px 8px;
}
.err {
  color: var(--err);
  font-size: 12px;
  padding: 4px 8px;
}
</style>
