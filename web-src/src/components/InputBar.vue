<script setup lang="ts">
// 输入区(槽位 input):回合提交 + "/" 命令提示(经 ctx.commands 注册表)+ 会话切换 + 状态控制。
// 排版对齐 DeepSeek Harness:一体圆角外壳内嵌输入与工具条(思考/沙箱/会话在框内底部,发送圆钮右侧),
// 空状态(centered)放大居中,有会话内容后沉底常规形态。
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import type { AskConfirm, AttachmentView, CommandView, SessionInfo, StateView } from '../types'

const props = defineProps<{
  disabled: boolean
  onSubmit: (text: string, attachments: string[]) => void
  // 真实运行状态(state.thinking/sandbox 驱动按钮标签与循环起点,与设置面板/状态栏一致)
  state?: StateView
  // 空状态居中放大形态(会话前中央大输入框);有会话后为底部常规形态
  centered?: boolean
}>()
// 会话切换/新建成功后通知宿主;控制(思考/沙箱)变更后通知宿主刷新 state
const emit = defineEmits<{ (e: 'session-changed'): void; (e: 'changed'): void }>()

const text = ref('')
const cmds = ref<CommandView[]>([])
const sessions = ref<SessionInfo[]>([])
const showPicker = ref(false)
const selId = ref('')
const pickErr = ref('')
const ta = ref<HTMLTextAreaElement | null>(null)

// —— 附件(文件/图片):按钮选择 + 拖放 + 粘贴收集;提交前上传,失败保留重试 ——
interface PendingFile {
  file: File
  view: string // 本地预览(objectURL,仅图片)
  state: 'pending' | 'done' | 'err'
  path?: string
}
const attachments = ref<PendingFile[]>([])
const attErr = ref('')
const uploading = ref(false)
const dragging = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

const ATT_ACCEPT =
  'image/*,text/*,.txt,.md,.json,.csv,.log,application/json,application/pdf,.pdf,.png,.jpg,.jpeg,.gif,.webp'
const MAX_ATT = 8
const MAX_ATT_BYTES = 20 << 20

function isImageFile(f: File): boolean {
  return f.type.startsWith('image/')
}
function openPicker(): void {
  fileInput.value?.click()
}
function onFileChange(e: Event): void {
  const el = e.target as HTMLInputElement
  if (el.files) collectFiles(Array.from(el.files))
  el.value = '' // 允许重选同一文件
}
function onDrop(e: DragEvent): void {
  dragging.value = false
  if (e.dataTransfer?.files) collectFiles(Array.from(e.dataTransfer.files))
}
function onDragOver(e: DragEvent): void {
  dragging.value = true
  e.dataTransfer!.dropEffect = 'copy'
}
function onPaste(e: ClipboardEvent): void {
  const items = e.clipboardData?.items ?? []
  const imgs: File[] = []
  for (const it of items) {
    if (it.kind === 'file' && it.type.startsWith('image/')) {
      const f = it.getAsFile()
      if (f) imgs.push(f)
    }
  }
  if (imgs.length) {
    e.preventDefault() // 避免把图片二进制粘进文本
    collectFiles(imgs)
  }
}
function collectFiles(files: File[]): void {
  for (const f of files) {
    if (attachments.value.length >= MAX_ATT) {
      attErr.value = `附件最多 ${MAX_ATT} 个`
      break
    }
    if (f.size > MAX_ATT_BYTES) {
      attErr.value = `「${f.name}」超过 20MB 已跳过`
      continue
    }
    attachments.value.push({
      file: f,
      view: isImageFile(f) ? URL.createObjectURL(f) : '',
      state: 'pending',
    })
  }
}
function removeAtt(i: number): void {
  const a = attachments.value[i]
  if (a.view) URL.revokeObjectURL(a.view)
  attachments.value.splice(i, 1)
}
function fmtSize(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1 << 20) return (n / 1024).toFixed(0) + ' KB'
  return (n / (1 << 20)).toFixed(1) + ' MB'
}

// 提交前上传全部待传附件(pending/done 复用;失败停止并保留)
async function uploadAll(): Promise<string[]> {
  const paths: string[] = []
  for (const a of attachments.value) {
    if (a.state === 'done' && a.path) {
      paths.push(a.path)
      continue
    }
    try {
      const v: AttachmentView = await api.upload(a.file)
      a.state = 'done'
      a.path = v.path
      paths.push(v.path)
    } catch (e) {
      a.state = 'err'
      throw e
    }
  }
  return paths
}

// 全局二次确认(会话切换/新建经确认条防误操作)
const ask = inject<(a: AskConfirm) => void>('askConfirm')
function guard(title: string, run: () => void): void {
  if (!ask) {
    run()
    return
  }
  ask({ title, run })
}

// —— 命令逐级确认(与 TUI 选择器同一注册表声明;/api/commands/{name}/options) ——
// 选中命令后逐级拉候选:有枚举 → 列表点击继续;无枚举但有自由参数 → 提示手动输入;皆无 → 结束。
const levelCmd = ref('') // 正在逐级选择的命令名('' = 命令级)
const levelPath = ref<string[]>([]) // 已选参数值(不含命令名)
const levelItems = ref<CommandView[]>([]) // 当前级候选(复用渲染形状)
const levelFree = ref('') // 自由参数提示(无枚举时)

const hints = computed(() => {
  if (levelItems.value.length) return levelItems.value
  if (!text.value.startsWith('/')) return []
  const p = text.value.slice(1)
  if (!p) return cmds.value.slice(0, 8)
  return cmds.value.filter((c) => c.name.startsWith(p)).slice(0, 8)
})

function resetLevel(): void {
  levelCmd.value = ''
  levelPath.value = []
  levelItems.value = []
  levelFree.value = ''
}

async function loadLevel(): Promise<void> {
  const name = levelCmd.value
  if (!name) return
  try {
    const resp = await api.commandOptions(name, levelPath.value)
    levelItems.value = resp.items.map((i) => ({ name: i.value, usage: '', desc: i.desc }))
    levelFree.value = resp.items.length === 0 && !resp.done ? resp.freeArgs.join(' | ') : ''
  } catch {
    levelItems.value = []
    levelFree.value = ''
  }
}

async function pickLevel(value: string): Promise<void> {
  levelPath.value = [...levelPath.value, value]
  text.value = '/' + levelCmd.value + ' ' + levelPath.value.join(' ') + ' '
  await loadLevel()
}

// 手工改写命令文本(如换成别的命令)→ 退出逐级态,避免陈旧候选
watch(text, (v) => {
  if (levelCmd.value && !v.startsWith('/' + levelCmd.value + ' ')) resetLevel()
})

const THINK = ['off', 'low', 'medium', 'high'] as const
const THINK_LABEL: Record<string, string> = { off: '关', low: '低', medium: '中', high: '高' }
const SANDBOX = ['read-only', 'workspace-write', 'full-access'] as const
const SANDBOX_LABEL: Record<string, string> = { 'read-only': '只读', 'workspace-write': '工作区', 'full-access': '完全' }

const thinkVal = computed(() => {
  const v = props.state?.thinking || 'medium'
  return THINK_LABEL[v] ?? v
})
const sandboxVal = computed(() => {
  const v = props.state?.sandbox || 'workspace-write'
  return SANDBOX_LABEL[v] ?? v
})

// 多行生长(Shift+Enter 换行;裸 Enter 提交);封顶=40vh(至少 180px)防超高,
// 超出后在输入框内滚动(overflow-y:auto 见 .field),内容可见可删改
function autoGrow(): void {
  const el = ta.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, Math.max(180, Math.round(window.innerHeight * 0.4))) + 'px'
}
// 全选删除:浏览器原生 Cmd/Ctrl+A + Backspace 已是 textarea 默认;此处确认不拦截即可。
// 平台归一快捷键统一处理:Enter=发送(裸/Cmd/Ctrl);Esc=关会话抽屉→清空输入。
function onKeydown(e: KeyboardEvent): void {
  // 输入法组字中(中文/日文候选上屏)的 Enter 不是"提交":isComposing 或 keyCode 229
  // 时直接放行,否则 CJK 用户按 Enter 上屏即被当发送,输入内容未完成就发出去。
  if (e.isComposing || e.keyCode === 229) return
  const mod = e.ctrlKey || e.metaKey
  if (e.key === 'Enter' && !e.shiftKey && (mod || !e.altKey)) {
    // 裸 Enter(无修饰)=发送;Cmd/Ctrl+Enter=发送(mac/win 习惯);Shift+Enter=换行(原生)
    if (!props.disabled && !uploading.value) {
      e.preventDefault()
      void submit()
    }
    return
  }
  if (e.key === 'Escape') {
    e.preventDefault()
    if (showPicker.value) {
      showPicker.value = false
    } else if (text.value !== '') {
      text.value = ''
      void nextTick(() => ta.value && (ta.value.style.height = 'auto'))
    }
  }
}

async function submit(): Promise<void> {
  const t = text.value.trim()
  if (!t || props.disabled || uploading.value) return
  if (attachments.value.length && t.startsWith('/')) {
    attErr.value = '命令消息不支持附件'
    return
  }
  attErr.value = ''
  const had = attachments.value.length > 0
  if (had) {
    uploading.value = true
    try {
      const paths = await uploadAll()
      text.value = ''
      props.onSubmit(t, paths)
      for (const a of attachments.value) if (a.view) URL.revokeObjectURL(a.view)
      attachments.value = []
    } catch (e) {
      attErr.value = '附件上传失败: ' + (e as Error).message
      uploading.value = false
      return
    }
    uploading.value = false
  } else {
    text.value = ''
    props.onSubmit(t, [])
  }
  void nextTick(() => {
    if (ta.value) ta.value.style.height = 'auto'
  })
}

function pickHint(name: string): void {
  if (levelCmd.value) {
    void pickLevel(name) // 参数级:点击值继续下一级
    return
  }
  resetLevel()
  levelCmd.value = name
  text.value = '/' + name + ' '
  void loadLevel() // 命令级:拉取该命令的参数级候选
}

// 会话切换抽屉
async function togglePicker(): Promise<void> {
  showPicker.value = !showPicker.value
  if (showPicker.value) {
    pickErr.value = ''
    selId.value = ''
    try {
      sessions.value = await api.sessions()
    } catch (e) {
      pickErr.value = (e as Error).message
    }
  }
}

async function doSwitch(): Promise<void> {
  try {
    await api.sessionSwitch(selId.value)
    showPicker.value = false
    emit('session-changed')
  } catch (e) {
    pickErr.value = (e as Error).message
  }
}

async function doNew(): Promise<void> {
  try {
    await api.sessionNew()
    showPicker.value = false
    emit('session-changed')
  } catch (e) {
    pickErr.value = (e as Error).message
  }
}

// 切会话/新建前二次确认
function confirmSwitch(): void {
  if (!selId.value) return
  const s = sessions.value.find((x) => x.ID === selId.value)
  const nm = s?.Name || (s?.ID ? '#' + s.ID : '(主会话)')
  guard('切换到会话「' + nm + '」？', () => void doSwitch())
}
function confirmNew(): void {
  guard('新建会话？', () => void doNew())
}

async function cycleThinking(): Promise<void> {
  // 从真实状态循环(非本地假起点),与设置面板一致
  const cur = props.state?.thinking || 'medium'
  let i = THINK.indexOf(cur as (typeof THINK)[number])
  if (i < 0) i = THINK.indexOf('medium')
  const next = THINK[(i + 1) % THINK.length]
  await api.control({ thinking: next })
  emit('changed')
}
async function cycleSandbox(): Promise<void> {
  const cur = props.state?.sandbox || 'workspace-write'
  let i = SANDBOX.indexOf(cur as (typeof SANDBOX)[number])
  if (i < 0) i = SANDBOX.indexOf('workspace-write')
  const next = SANDBOX[(i + 1) % SANDBOX.length]
  await api.control({ sandbox: next })
  emit('changed')
}

onMounted(() => {
  api.commands().then((c) => (cmds.value = c)).catch(() => undefined)
  // 会话列表在别处变更(侧栏删除等)时关闭抽屉,避免陈旧缓存(下次打开重拉)
  window.addEventListener('gah:sessions-changed', onSessionsChanged)
})
onUnmounted(() => window.removeEventListener('gah:sessions-changed', onSessionsChanged))
function onSessionsChanged(): void {
  if (showPicker.value) showPicker.value = false
}

defineExpose({ cycleThinking, cycleSandbox })
</script>

<template>
  <div class="shell" :class="{ centered, dragging }" @dragover.prevent="onDragOver" @dragleave="dragging = false" @drop.prevent="onDrop">
    <!-- / 命令提示(浮于外壳上方) -->
    <div v-if="hints.length || levelFree" class="float">
      <div
        v-for="h in hints"
        :key="h.name"
        class="hint"
        :data-tip="levelCmd ? '选择该参数' : '继续选择参数'"
        @mousedown.prevent="pickHint(h.name)"
      >
        <span class="hn">{{ h.name }}</span>
        <span class="hd">{{ h.desc }}</span>
      </div>
      <div v-if="levelFree" class="hint free" data-tip="手动输入该参数">
        <span class="hn">…</span>
        <span class="hd">继续输入 {{ levelFree }}</span>
      </div>
    </div>

    <!-- 会话切换抽屉(浮于外壳上方) -->
    <div v-if="showPicker" class="float">
      <div v-if="pickErr" class="perr">{{ pickErr }}</div>
      <div
        v-for="s in sessions"
        :key="s.ID || '(main)'"
        class="pitem"
        :class="{ cur: selId === s.ID }"
        data-tip="选择该会话"
        @click="selId = s.ID"
      >
        <span class="pn">{{ s.Name || (s.ID ? '#' + s.ID : '(主会话)') }}</span>
        <span class="pd mono">{{ s.Path }}</span>
      </div>
      <div class="pacts">
        <span class="sel" data-tip="切换到选中会话" @click="confirmSwitch">切换到选中</span>
        <span class="sel" data-tip="新建一个空会话" @click="confirmNew">新建会话</span>
        <span class="sel" data-tip="关闭抽屉" @click="showPicker = false">取消</span>
      </div>
    </div>

    <!-- 附件 chip 区(位于输入区与工具条之间) -->
    <div v-if="attachments.length || attErr" class="atts">
      <div v-if="attErr" class="att-err">{{ attErr }}</div>
      <div v-for="(a, i) in attachments" :key="i" class="att" :class="{ up: a.state === 'done', err: a.state === 'err' }">
        <img v-if="a.view" class="thumb" :src="a.view" alt="" />
        <svg v-else class="file-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" />
          <path d="M13 2v7h7" />
        </svg>
        <span class="att-name" :data-tt="a.file.name">{{ a.file.name }}</span>
        <span class="att-size">{{ fmtSize(a.file.size) }}</span>
        <button class="att-x" data-tip="移除附件" aria-label="移除" @click="removeAtt(i)">×</button>
      </div>
    </div>

    <!-- 一体外壳:输入区 + 底部工具条(全部按键包裹在内) -->
    <textarea
      ref="ta"
      v-model="text"
      class="field"
      rows="1"
      :disabled="disabled"
      :placeholder="disabled ? '回合进行中…' : '输入消息,以 / 开头使用命令(Enter 发送 · Shift+Enter 换行 · Esc 清空)'"
      @input="autoGrow"
      @keydown="onKeydown"
      @paste="onPaste"
    />

    <input ref="fileInput" type="file" multiple class="hidden-file" :accept="ATT_ACCEPT" :disabled="disabled" @change="onFileChange" />

    <div class="bar">
      <div class="tools">
        <button class="ctl" data-tip="添加附件(文件/图片;支持拖入与 Ctrl/Cmd+V 粘贴)" :disabled="disabled || uploading" @click="openPicker">
          <svg class="att-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48" />
          </svg>
          <span class="ctl-n">附件</span>
        </button>
        <button class="ctl" :data-tip="'切换思考等级(当前 ' + thinkVal + ')'" @click="cycleThinking">
          <span class="ctl-n">思考</span>
          <span class="ctl-v">{{ thinkVal }}</span>
        </button>
        <button class="ctl" :data-tip="'切换沙箱档位(当前 ' + sandboxVal + ')'" @click="cycleSandbox">
          <span class="ctl-n">沙箱</span>
          <span class="ctl-v">{{ sandboxVal }}</span>
        </button>
        <button class="ctl" data-tip="会话管理(切换/新建)" @click="togglePicker">
          <span class="ctl-n">会话</span>
        </button>
      </div>
      <div class="sp" />
      <button class="send" data-tip="发送(Enter)" :disabled="disabled || uploading" @click="submit">
        <span class="send-arrow" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.shell {
  position: relative;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  box-shadow: var(--shadow-pop);
  max-width: 900px;
  margin: 0 auto; /* 对话态:右列内底部限宽居中(≤900),窄列自适应满宽 */
  transition: border-color var(--dur-fast) ease, box-shadow var(--dur-fast) ease;
}
.shell:focus-within {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}
.shell.centered {
  border-radius: 14px;
  box-shadow: var(--shadow-dialog);
}
/* 输入区:无内边框,高度随行自动增长;超长内容输入框内滚动(40vh 封顶后),超长行软折行兜底 */
.field {
  width: 100%;
  border: none;
  outline: none;
  resize: none;
  background: transparent;
  color: var(--fg);
  font-family: inherit;
  font-size: 14px;
  line-height: 1.6;
  padding: 12px 14px 2px;
  overflow-y: auto;
  overflow-wrap: anywhere;
}
.field:disabled {
  opacity: 0.55;
}
.shell.centered .field {
  font-size: 15px;
  padding: 15px 18px 4px;
}
/* 附件 chip 区(输入区与工具条之间) */
.atts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 2px 10px 0; /* 与输入区内边距对齐 */
}
.shell.centered .atts {
  padding: 2px 14px 0;
}
.att {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  background: var(--bg2);
  padding: 3px 8px 3px 4px;
  max-width: 200px;
}
.att.up {
  border-color: var(--accent-soft);
  background: var(--accent-soft);
}
.att.err {
  border-color: var(--err-line);
  background: var(--err-soft);
}
.thumb {
  width: 30px;
  height: 30px;
  object-fit: cover;
  border-radius: 5px;
  border: 1px solid var(--line);
}
.file-ico {
  width: 26px;
  height: 26px;
  color: var(--fg-faint);
  flex-shrink: 0;
}
.att-name {
  font-size: 12px;
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}
.att-size {
  font-size: 11px;
  color: var(--fg-faint);
  flex-shrink: 0;
}
.att-x {
  border: none;
  background: none;
  color: var(--fg-faint);
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
  padding: 2px;
  flex-shrink: 0;
}
.att-x:hover {
  color: var(--err);
}
.att-err {
  font-size: 12px;
  color: var(--err);
  width: 100%;
}
/* 拖放高亮:附件落入外壳时 accent 边框 */
.shell.dragging {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}
.hidden-file {
  display: none;
}
.att-ico {
  width: 13px;
  height: 13px;
  color: var(--fg-faint);
  vertical-align: -2px;
}
.ctl:hover .att-ico {
  color: var(--accent);
}
/* 底部工具条(与输入同框):左=思考/沙箱/会话,右=发送 */
.bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px 8px;
}
.tools {
  display: inline-flex;
  gap: 6px;
}
.sp {
  flex: 1;
}
.ctl {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  background: none;
  border: 1px solid transparent;
  border-radius: 8px;
  color: var(--fg-faint);
  cursor: pointer;
  padding: 3px 8px;
  font-size: 12px;
  transition: border-color var(--dur-fast) ease, color var(--dur-fast) ease, background var(--dur-fast) ease, transform 0.08s ease;
}
.ctl:hover {
  border-color: var(--line);
  color: var(--accent);
  background: var(--accent-soft);
}
.ctl:active {
  transform: translateY(1px);
}
.ctl-n {
  color: var(--fg-faint);
}
.ctl-v {
  color: var(--fg-dim);
  font-weight: 500;
}
.ctl:hover .ctl-n,
.ctl:hover .ctl-v {
  color: var(--accent);
}
.send {
  background: var(--accent);
  border: none;
  border-radius: 10px;
  width: 34px;
  height: 34px;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  transition: background var(--dur-fast) ease, transform 0.1s ease;
}
.send:hover {
  background: var(--accent-hover);
}
.send:active {
  transform: scale(0.96);
}
.send:disabled {
  opacity: 0.45;
  cursor: default;
}
.shell.centered .send {
  width: 40px;
  height: 40px;
  border-radius: 12px;
}
.send-arrow {
  width: 9px;
  height: 9px;
  border-top: 2px solid var(--fg-on-accent);
  border-right: 2px solid var(--fg-on-accent);
  transform: rotate(-45deg);
  margin-top: 3px;
}
.shell.centered .send-arrow {
  width: 10px;
  height: 10px;
}
/* 工具条按钮(send/ctl)贴近右缘:tooltip 右对齐向左展开,防长文案向右溢出视口 */
.bar > [data-tip]:hover::after {
  left: auto;
  right: 0;
  transform: none;
}

/* 浮层(命令提示/会话抽屉):相对外壳上缘 */
.float {
  position: absolute;
  bottom: calc(100% + 6px);
  left: 0;
  right: 0;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  box-shadow: var(--shadow-pop);
  max-height: 240px;
  overflow-y: auto;
  z-index: 10;
  padding: 4px;
}
.hint {
  padding: 6px 10px;
  cursor: pointer;
  display: flex;
  gap: 10px;
  border-radius: 6px;
}
.hint:hover {
  background: var(--bg3);
}
.hint.free {
  cursor: default;
}
.hint.free .hd {
  color: var(--fg-dim);
}
.hn {
  color: var(--accent);
  font-weight: 600;
  min-width: 80px;
}
.hd {
  color: var(--fg-dim);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pitem {
  padding: 6px 10px;
  cursor: pointer;
  display: flex;
  gap: 10px;
  border-radius: 6px;
}
.pitem:hover,
.pitem.cur {
  background: var(--accent-soft);
}
.pn {
  min-width: 120px;
  color: var(--fg);
}
.pd {
  color: var(--fg-dim);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.perr {
  color: var(--err);
  padding: 6px 10px;
  font-size: 12px;
}
.pacts {
  border-top: 1px solid var(--line-faint);
  margin-top: 4px;
  display: flex;
}
.sel {
  flex: 1;
  text-align: center;
  padding: 6px;
  cursor: pointer;
  color: var(--accent);
  font-size: 12px;
  border-radius: 6px;
}
.sel:hover {
  background: var(--bg3);
}
</style>
