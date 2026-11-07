<script setup lang="ts">
// 输入区(槽位 input):回合提交 + "/" 命令提示(经 ctx.commands 注册表)+ 会话切换 + 状态控制。
// 排版对齐 DeepSeek Harness:一体圆角外壳内嵌输入与工具条(思考/沙箱/会话在框内底部,发送圆钮右侧),
// 空状态(centered)放大居中,有会话内容后沉底常规形态。
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import type { AskConfirm, AttachmentView, CommandView, SessionInfo, StateView } from '../types'

const props = defineProps<{
  disabled: boolean
  // onSubmit 返回 false = 未受理(断连/提交失败):输入框**保留草稿与附件**供重发
  // (S-P1-3);返回 true/undefined 视为已受理,清空草稿(旧插件不返回值 → 行为不变)。
  onSubmit: (text: string, attachments: string[]) => unknown
  // 真实运行状态(state.thinking/sandbox 驱动按钮标签与循环起点,与设置面板/状态栏一致)
  state?: StateView
  // 空状态居中放大形态(会话前中央大输入框);有会话后为底部常规形态
  centered?: boolean
  // disabledHint 禁用原因(如断连):优先于「回合进行中」占位文案,不改变任何状态
  disabledHint?: string
  // busy 回合进行中。**不是 disabled**:输入框照旧可编辑、回车即提交,宿主把消息
  // 注入当前回合(转向,模型下一次请求即可见);只是不能新挂附件(转向通道不带附件)。
  // 为何分开:此前把运行中当作 disabled 传给输入框,运行时 textarea 直接锁死,
  // 用户按 Enter 什么也不会发生 —— 而宿主侧早已支持注入(真机反馈)。
  busy?: boolean
  // onCancel 中止运行中的回合(可选;不传则不显示「停止」按钮)
  onCancel?: () => void
}>()
// 会话切换/新建成功后通知宿主;控制(思考/沙箱)变更后通知宿主刷新 state。
// 「本会话设置」入口**不在这里** —— 第一百三十六批已移到右上角状态栏(用户反馈工具条里不够明显)。
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
  path?: string // 上传返回的绝对路径(仅展示/诊断)
  ref?: string // 提交时回传的附件标识 /attachments/<rel>(跨平台免路径语义)
}
const attachments = ref<PendingFile[]>([])
const attErr = ref('')
// 附件错误提示是瞬时态(超限/类型/上传失败):10s 自动消失,也可点 × 立即关闭。
// 此前常驻在输入区上方,按提示换了文件后旧提示仍挂着。
let attErrTimer: ReturnType<typeof setTimeout> | null = null
function setAttErr(msg: string): void {
  attErr.value = msg
  if (attErrTimer) clearTimeout(attErrTimer)
  attErrTimer = setTimeout(() => {
    attErr.value = ''
    attErrTimer = null
  }, 10000)
}
function clearAttErr(): void {
  if (attErrTimer) clearTimeout(attErrTimer)
  attErrTimer = null
  attErr.value = ''
}
const uploading = ref(false)
// noAttach 附件入口统一闸门:断连(disabled)或回合进行中(busy)都不放行。
// 为何运行时也不放附件:宿主转向通道只带文本(带附件的提交会回落 409),
// 若这里放行,用户会以为附件发出去了。
const noAttach = computed(() => props.disabled || !!props.busy)
const placeholder = computed(() => {
  if (props.disabled) return props.disabledHint || '输入不可用'
  if (props.busy) return '回合进行中:Enter 把消息注入当前回合(模型下一次请求可见);附件等回合结束'
  return '输入消息,以 / 开头使用命令(Enter 发送 · Shift+Enter 换行 · Esc 清空)'
})
const dragging = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

const ATT_ACCEPT =
  // heic/heif/avif:手机照片常是 HEIC —— 选得到才能进附件链路。
  // 两处配套(2026-10-11 补齐,口径与后端 attachExtAllow 一致):
  //  ① 服务端按**扩展名兜底**:这些格式在 Windows 上没有注册 MIME,浏览器会送空类型,
  //     只认 Content-Type 会把用户"选得中却传不上"的东西拒掉;
  //  ② 读它时**显式提示**:桌面无 HEIC 解码器 ⇒ 文档预览会给出"无法解码 + 先用系统工具转码",
  //     不会退化成十六进制 dump 或一声不吭(见 host-docview extractImage)。
  'image/*,text/*,.txt,.md,.json,.csv,.log,application/json,application/pdf,.pdf,.png,.jpg,.jpeg,.gif,.webp,.heic,.heif,.avif'
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

// —— 全窗拖放落点 ——
// 此前只有落在输入外壳上才收附件;拖到窗口别处(会话流/侧栏/停靠区)时交给浏览器默认动作
// —— 它直接把该文件**导航打开**,当前会话界面被顶掉,用户得退回来。现在任意落点都收。
const winDrag = ref(false)

// dataTransfer.types 是拖拽期唯一可用的清单(files 列表要到 drop 才填);
// 只有含 'Files' 才算文件拖拽 —— 拖选中文本进来不该弹附件提示。
function dragHasFiles(e: DragEvent): boolean {
  const t = e.dataTransfer?.types
  return !!t && Array.from(t).includes('Files')
}
function onWinDragOver(e: DragEvent): void {
  if (!dragHasFiles(e)) return
  // defaultPrevented = 输入外壳已经处理过(它有自己的一圈高亮),不叠全窗提示
  if (e.defaultPrevented) {
    winDrag.value = false
    return
  }
  e.preventDefault() // 缺它浏览器会在 drop 时执行默认动作(打开文件)
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
  winDrag.value = true
}
function onWinDragLeave(e: DragEvent): void {
  // relatedTarget 为 null / 已不在文档内 = 指针真离开了窗口(窗口内元素间移动时不为 null)
  const to = e.relatedTarget as Node | null
  if (!to || !document.contains(to)) winDrag.value = false
}
function onWinDrop(e: DragEvent): void {
  winDrag.value = false
  if (e.defaultPrevented) return // 输入外壳已收(它的 drop 处理器阻止了冒泡到这里的重复收集)
  if (!dragHasFiles(e)) return
  e.preventDefault()
  if (e.dataTransfer?.files) collectFiles(Array.from(e.dataTransfer.files))
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
  if (noAttach.value) {
    setAttErr(props.disabled ? props.disabledHint || '当前不可添加附件' : '回合进行中:附件要等回合结束(追加消息只支持文本)')
    return
  }
  for (const f of files) {
    if (attachments.value.length >= MAX_ATT) {
      setAttErr(`附件最多 ${MAX_ATT} 个`)
      break
    }
    if (f.size > MAX_ATT_BYTES) {
      setAttErr(`「${f.name}」超过 20MB 已跳过(单文件上限 20MB)`)
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
  clearAttErr() // 移除后旧提示不再适用(用户已在改附件)
}
function fmtSize(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1 << 20) return (n / 1024).toFixed(0) + ' KB'
  return (n / (1 << 20)).toFixed(1) + ' MB'
}

// 提交前上传全部待传附件(pending/done 复用;失败停止并保留)
async function uploadAll(): Promise<string[]> {
  const ids: string[] = []
  for (const a of attachments.value) {
    if (a.state === 'done' && a.ref) {
      ids.push(a.ref)
      continue
    }
    try {
      const v: AttachmentView = await api.upload(a.file)
      a.state = 'done'
      a.path = v.path
      // 提交用相对标识 /attachments/<rel>:由服务端按自己的附件根解析,
      // 免去跨平台路径分隔符/大小写/数据根漂移带来的「附件路径非法」误判
      a.ref = v.url
      ids.push(v.url)
    } catch (e) {
      a.state = 'err'
      throw e
    }
  }
  return ids
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
const levelAll = ref<CommandView[]>([]) // 当前级全量候选(过滤前;退格/清词恢复用)
const levelFilter = ref('') // 参数级过滤词(继续输入即筛选;只在输入框里可见,**不进提交的命令正文**)
const levelFree = ref('') // 自由参数提示(无枚举时)
// levelItems 当前级可见候选 = 全量按过滤词筛出(子串、大小写不敏感;值或说明任一命中)。
// 为什么过滤词要跟命令正文分开算:正文是「提交时原样执行」的字符串,
// 把筛选词当参数带上去就是 /memory list --kind 里的非法参数。
// TUI 选择器同一口径(tui/picker.go 的 Pick.Filter + filterOptions)。
const levelItems = computed(() => {
  const q = levelFilter.value.trim().toLowerCase()
  if (!q) return levelAll.value
  return levelAll.value.filter((i) => i.name.toLowerCase().includes(q) || i.desc.toLowerCase().includes(q))
})
// levelText = 逐级流程自己写下的命令文本:只有它才算「仍在逐级」——
// 用户手工改写命令文本(如从“备份到自定义路径”改成“/backup restore”)必须退出逐级态,
// 否则会留一个陈旧的“继续输入…”提示,把命令当成的参数级候选(提示与实际参数不符)。
const levelText = ref('')

const hints = computed(() => {
  if (levelItems.value.length) return levelItems.value
  if (!text.value.startsWith('/')) return []
  const p = text.value.slice(1)
  if (!p) return cmds.value.slice(0, 8)
  return cmds.value.filter((c) => c.name.startsWith(p)).slice(0, 8)
})

// —— 候选的键盘选择(上下移动 + 回车选中) ——
//
// 为什么必须有:此前候选只能鼠标点。键盘用户打 /mem 之后既不能用上下键选,回车也只会把
// "/mem" 原样发出去 —— 命令根本没执行,而他看不出为什么。
//
// 三条口径:
//  1. **回车是"选中",不是"发送"**:只要还有候选,回车就选当前高亮项;
//  2. **打全命令名后回车 = 执行**(不进入参数级):否则 /status 要在选一次候选后才生效,
//     多一次没有反馈的车回;
//  3. 选中命令后自动拉参数级;该命令**没有可选参数**时直接执行(浮层是空的,再按回车
//     只会让人以为卡住)。
const hintIdx = ref(-1)
const hintDismissed = ref(false) // Esc 关掉候选(文本一变就恢复)
const hintBox = ref<HTMLElement | null>(null)
const hintList = computed(() => (hintDismissed.value ? [] : hints.value))

// 候选变化(打字/切到参数级)⇒ 高亮回到第一项:n/p 之外不需要"记住上次选到哪"。
watch(
  hints,
  (list) => {
    hintIdx.value = list.length ? 0 : -1
  },
  { flush: 'post' },
)
// 文本一变就重新允许候选(否则 Esc 之后再也弹不出来)。
watch(text, () => {
  hintDismissed.value = false
})

// moveHint 上下移动高亮(循环);顺带把它滚进可视区(候选最多 8 条,窄窗会截)。
function moveHint(delta: number): void {
  const n = hintList.value.length
  if (!n) return
  const cur = hintIdx.value
  hintIdx.value = cur < 0 ? (delta > 0 ? 0 : n - 1) : (cur + delta + n) % n
  void nextTick(() => {
    // 只数候选行:过滤状态行(.filt)不是候选,直接拿 children[i] 会错位到它身上
    const rows = hintBox.value?.querySelectorAll('.hint:not(.filt)')
    ;(rows?.[hintIdx.value] as HTMLElement | undefined)?.scrollIntoView({ block: 'nearest' })
  })
}

// isCompleteCommand 文本是否已经是"某个命令的全名"(不含参数)。
// 这类文本回车应当**直接执行**,而不是被当成"还要再选一次候选"。
function isCompleteCommand(): boolean {
  // 已在参数级(levelCmd 非空)⇒ **永远不是"完整命令"**:此时浮层是参数候选,
  // 回车就该选参数。少了这个判断,`/sample `(选中命令后带尾空格)trim 后正好等于命令名,
  // 于是最后一级参数还没选就被当成"命令已完整"直接提交了 —— 护栏用例逮到的就是这个。
  if (levelCmd.value) return false
  const t = text.value.trim()
  if (!t.startsWith('/') || t.includes(' ')) return false
  return cmds.value.some((c) => '/' + c.name === t)
}

function resetLevel(): void {
  levelCmd.value = ''
  levelPath.value = []
  levelAll.value = []
  levelFilter.value = ''
  levelFree.value = ''
  levelText.value = ''
}

interface LevelState {
  items: number
  free: boolean
  done: boolean
}

async function loadLevel(): Promise<LevelState> {
  const name = levelCmd.value
  if (!name) return { items: 0, free: false, done: true }
  levelFilter.value = '' // 换到新一级:上一级的过滤词不跟过来(否则新级可能一进来就是空的)
  try {
    const resp = await api.commandOptions(name, levelPath.value)
    // 空枚举/空自由参数可能是 [] 也可能是 null(旧后端/未归零的响应)→ 一律按数组消费,
    // 否则 items.map 抛错被 catch 吞掉 → 自由参数提示整级消失(命令链静默断掉)。
    const items = resp.items ?? []
    levelAll.value = items.map((i) => ({ name: i.value, usage: '', desc: i.desc }))
    levelFree.value = items.length === 0 && !resp.done ? (resp.freeArgs ?? []).join(' | ') : ''
    return { items: items.length, free: !!levelFree.value, done: !!resp.done }
  } catch {
    // 拉不到参数级(旧后端/网络抖动)按"该级没有可选项"处理 —— 命令本身仍可执行,
    // 比卡在逐级态里出不去好。
    levelAll.value = []
    levelFree.value = ''
    return { items: 0, free: false, done: true }
  }
}

async function pickLevel(value: string): Promise<void> {
  levelPath.value = [...levelPath.value, value]
  levelText.value = '/' + levelCmd.value + ' ' + levelPath.value.join(' ') + ' '
  text.value = levelText.value
  const st = await loadLevel()
  // 参数已选完且没有下一级可选 ⇒ 直接执行:此时浮层是空的,再按回车只会让人以为卡住。
  if (st.items === 0 && !st.free && levelText.value === text.value) void submit()
}

// 手工改写命令文本(换成别的命令/别的参数)→ 退出逐级态,避免陈旧候选。
// 例外:参数级且本级**有枚举候选**时,紧跟在命令文本后多打的字 = 过滤词(不是改命令)。
// 关键细节:过滤词就**留在输入框里**(文本 = levelText + 过滤词)—— 不回写命令文本。
// 回写会把刚敲的字抹掉:下一次按键合到的是 levelText 而不是 levelText+过滤词,
// 于是过滤词永远只剩最后一个字符(实测、“be” 只剩 “e”,候选停在 2 项)。
// 提交/选中时再用 levelText 作命令正文,过滤词不进命令(tui/picker.go 的 Pick.Filter 同口径)。
watch(text, (v) => {
  if (!levelCmd.value) return
  if (v === levelText.value) {
    levelFilter.value = '' // 退格删到只剩命令正文 = 过滤词删空(不清就会留个陈旧过滤词卡住候选)
    return
  }
  if (levelAll.value.length && v.startsWith(levelText.value)) {
    levelFilter.value = v.slice(levelText.value.length)
    return
  }
  resetLevel()
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
// 本按钮改的是**声明档**(点一下循环);实际生效档可能被审批档联动覆盖,
// 故在图说里补一句,避免"设了工作区却仍可写"的静默矛盾。
const sandboxTip = computed(() => {
  // 第一百三十四批:先说作用域再说档位。原话只讲档位与联动,**完全不提作用域** ——
  // 于是「点一下就改了」这件事看起来像全局改动,而实际只作用于当前会话。
  const cur = '切换本会话的沙箱档位(全局默认在「设置」里改;当前 ' + sandboxVal.value
  const eff = props.state?.sandbox_effective
  if (!props.state?.sandbox_derived || !eff) return cur + ')'
  const label = SANDBOX_LABEL[eff] ?? eff
  if (props.state?.sandbox_from === 'role') return cur + ';实际 ' + label + ',角色收紧)'
  return cur + ';实际 ' + label + ',随审批联动)'
})

// 思考档按钮的图说同上:先说「本会话」,再说当前值;被角色覆盖时补一句(那层在设置面板)。
const thinkTip = computed(() => {
  const cur = '切换本会话的思考等级(全局默认在「设置」里改;当前 ' + thinkVal.value
  if (props.state?.thinking_from === 'role') return cur + ';被当前角色覆盖)'
  return cur + ')'
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
  // 上下键在候选里移动(有候选才拦截:否则要保留 textarea 自己的光标/滚动行为)。
  if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && hintList.value.length) {
    e.preventDefault()
    moveHint(e.key === 'ArrowDown' ? 1 : -1)
    return
  }
  if (e.key === 'Enter' && !e.shiftKey && (mod || !e.altKey)) {
    // 裸 Enter(无修饰)=发送;Cmd/Ctrl+Enter=发送(mac/win 习惯);Shift+Enter=换行(原生)
    if (!props.disabled && !uploading.value) {
      // 还有候选、且用户没打出完整命令名 ⇒ 回车是"选中候选",不发送。
      if (hintList.value.length && !isCompleteCommand()) {
        e.preventDefault()
        pickHintAt(hintIdx.value)
        return
      }
      e.preventDefault()
      void submit()
    }
    return
  }
  if (e.key === 'Escape') {
    // 参数级过滤中:Esc 先清过滤词恢复全量候选(与 TUI pickFilterEsc 同口径)。
    if (levelCmd.value && levelFilter.value) {
      e.preventDefault()
      levelFilter.value = ''
      text.value = levelText.value // 过滤词在输入框里,一并抹掉
      return
    }
    // 候选开着时 Esc 先关候选(第一步),再按才清空输入 —— 与抽屉同款的分层返回。
    if (hintList.value.length || levelFree.value) {
      e.preventDefault()
      hintDismissed.value = true
      resetLevel()
      return
    }
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
  // 参数级过滤词是输入框里的尾巴(见 watch(text)),不属于命令文本 —— 提交前剪掉,
  // 否则 /memory list 加个筛选词就会变成带非法参数的 /memory list --k
  const raw = levelCmd.value && levelFilter.value ? levelText.value : text.value
  const t = raw.trim()
  if (!t || props.disabled || uploading.value) return
  if (attachments.value.length && t.startsWith('/')) {
    setAttErr('命令消息不支持附件')
    return
  }
  clearAttErr()
  const had = attachments.value.length > 0
  let paths: string[] = []
  if (had) {
    uploading.value = true
    try {
      paths = await uploadAll()
    } catch (e) {
      setAttErr('附件上传失败: ' + (e as Error).message)
      uploading.value = false
      return // 草稿与附件保留(上传失败可原地重试)
    }
    uploading.value = false
  }
  // 受理判定:宿主返回 false 表示未送达(断连/上游失败)→ 草稿与附件原样保留
  let ok: unknown = true
  try {
    ok = await props.onSubmit(t, paths)
  } catch {
    ok = false
  }
  if (ok === false) {
    setAttErr('未发送:草稿与附件已保留,可直接重试')
    void nextTick(() => ta.value && ta.value.focus())
    return
  }
  text.value = ''
  if (had) {
    for (const a of attachments.value) if (a.view) URL.revokeObjectURL(a.view)
    attachments.value = []
  }
  void nextTick(() => {
    if (ta.value) ta.value.style.height = 'auto'
  })
}

function pickHint(name: string): void {
  if (levelCmd.value) {
    void pickLevel(name) // 参数级:选中值继续下一级
    return
  }
  resetLevel()
  levelCmd.value = name
  text.value = '/' + name + ' '
  levelText.value = text.value // 命令级文本也属逐级流程(否则 watch 立刻把 levelCmd 清掉)
  // 命令级:拉参数级;该命令没有参数级(如 /status)⇒ 直接执行,不再多要一次回车。
  void loadLevel().then((st) => {
    if (st.items === 0 && !st.free && levelText.value === text.value) void submit()
  })
}

// pickHintAt 选中当前高亮的候选(回车用;越界回落到第一项)。
function pickHintAt(idx: number): void {
  const list = hintList.value
  if (!list.length) return
  const i = idx >= 0 && idx < list.length ? idx : 0
  pickHint(list[i].name)
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
  // 全窗拖放落点(输入区常驻,故监听挂在它身上即可)
  window.addEventListener('dragover', onWinDragOver)
  window.addEventListener('dragleave', onWinDragLeave)
  window.addEventListener('drop', onWinDrop)
})
onUnmounted(() => window.removeEventListener('gah:sessions-changed', onSessionsChanged))
onUnmounted(() => {
  window.removeEventListener('dragover', onWinDragOver)
  window.removeEventListener('dragleave', onWinDragLeave)
  window.removeEventListener('drop', onWinDrop)
})
onUnmounted(() => {
  if (attErrTimer) clearTimeout(attErrTimer)
})
function onSessionsChanged(): void {
  if (showPicker.value) showPicker.value = false
}

// getText/setText:会话页签用来**命令式**存取草稿(切页签时保存旧页签、载入新页签)。
//
// 为何用命令式而不是 v-model 双向:输入框内容要存在一个**普通 JS 对象**里(TabSet),
// 那个对象不是响应式的 —— 靠父组件重渲染把草稿"推"下来,在真机上不成立
// (切页签后输入框不会跟着换)。命令式读写没有这个依赖,行为也可预期。
// 外部 UI 插件覆盖 input 槽位时可能没有这两个方法,宿主调用前会判空。
defineExpose({ cycleThinking, cycleSandbox, getText: () => text.value, setText: (v: string) => { text.value = v } })
</script>

<template>
  <div class="shell" :class="{ centered, dragging }" @dragover.prevent="onDragOver" @dragleave="dragging = false" @drop.prevent="onDrop">
    <!-- 全窗拖放提示(Teleport 到 body:面板的 overflow/transform 包含块裁不掉它;拖动中才挂载) -->
    <Teleport to="body">
      <div v-if="winDrag" class="windrop" aria-hidden="true">
        <div class="wd-card">
          <div class="wd-title">松开即添加为附件</div>
          <div class="wd-hint">图片或文件 · 最多 {{ MAX_ATT }} 个 · 单个不超过 {{ fmtSize(MAX_ATT_BYTES) }}</div>
        </div>
      </div>
    </Teleport>
    <!-- / 命令提示(浮于外壳上方) -->
    <div v-if="hintList.length || levelFree || levelFilter" ref="hintBox" class="float" data-cmd-hints>
      <!-- 参数级过滤状态行:告诉用户「多打的字被当成筛选」,以及怎么退回去 -->
      <div v-if="levelFilter" class="hint filt" data-cmd-filter>
        <span class="hn">过滤</span>
        <span class="hd">{{ levelFilter }} → {{ levelItems.length }}/{{ levelAll.length }} 项 {{ levelItems.length ? '(退格收窄/Esc 恢复)' : '无匹配(退格清除)' }}</span>
      </div>
      <div
        v-for="(h, i) in hintList"
        :key="h.name"
        class="hint"
        :class="{ sel: i === hintIdx }"
        :data-tip="levelCmd ? '选择该参数' : '继续选择参数'"
        @mouseenter="hintIdx = i"
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
      <div v-if="attErr" class="att-err">
        <span class="att-err-t">{{ attErr }}</span>
        <button class="att-err-x" data-tip="关闭提示" aria-label="关闭提示" @click="clearAttErr">×</button>
      </div>
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
      :placeholder="placeholder"
      @input="autoGrow"
      @keydown="onKeydown"
      @paste="onPaste"
    />

    <input ref="fileInput" type="file" multiple class="hidden-file" :accept="ATT_ACCEPT" :disabled="noAttach" @change="onFileChange" />

    <div class="bar">
      <div class="tools">
        <button class="ctl" data-tip="添加附件(文件/图片;支持拖入与 Ctrl/Cmd+V 粘贴)" :disabled="noAttach || uploading" @click="openPicker">
          <svg class="att-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48" />
          </svg>
          <span class="ctl-n">附件</span>
        </button>
        <button class="ctl" :data-tip="thinkTip" @click="cycleThinking">
          <span class="ctl-n">思考</span>
          <span class="ctl-v">{{ thinkVal }}</span>
        </button>
        <button class="ctl" :data-tip="sandboxTip" @click="cycleSandbox">
          <span class="ctl-n">沙箱</span>
          <span class="ctl-v">{{ sandboxVal }}</span>
        </button>
        <button class="ctl" data-tip="会话管理(切换/新建)" @click="togglePicker">
          <span class="ctl-n">会话</span>
        </button>
      </div>
      <div class="sp" />
      <button
        v-if="busy && props.onCancel"
        class="stop"
        data-tip="停止当前回合(会一并中止正在等待的审批与工具执行)"
        @click="props.onCancel?.()"
      >
        停止
      </button>
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
  transition: border-color var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out);
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
  transition: color var(--dur-fast) var(--ease-out);
}
.att-x:hover {
  color: var(--err);
}
.att-err {
  font-size: 12px;
  color: var(--err);
  width: 100%;
  display: flex;
  align-items: center;
  gap: 6px;
  line-height: 1.5;
}
.att-err-t {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
/* 提示可手动关闭(另有 10s 自动消失);按钮区不抢眼 */
.att-err-x {
  flex: none;
  border: 0;
  background: none;
  color: inherit;
  font-size: 14px;
  line-height: 1;
  padding: 2px 4px;
  border-radius: 4px;
  opacity: 0.7;
  transition: background var(--dur-fast) var(--ease-out), opacity var(--dur-fast) var(--ease-out);
}
.att-err-x:hover {
  opacity: 1;
  background: var(--accent-soft);
}
/* 拖放高亮:附件落入外壳时 accent 边框 */
.shell.dragging {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}
/* 全窗拖放提示(z-index 75:压住普通面板 70,让位于审批条 80 / 弹层 90;
   pointer-events:none 保证 drop 的落点判定不受它干扰;不给它加入场动效 ——
   拖动中要的是即时反馈,过渡会让「松手前看得见」变成看运气) */
.windrop {
  position: fixed;
  inset: 0;
  z-index: 75;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--overlay);
  pointer-events: none;
}
.wd-card {
  padding: 16px 26px;
  border: 1px dashed var(--accent);
  border-radius: var(--r-card);
  background: var(--surface);
  box-shadow: var(--shadow-pop);
  text-align: center;
}
.wd-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--fg);
}
.wd-hint {
  margin-top: 6px;
  font-size: 12px;
  color: var(--fg-dim);
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
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out), transform var(--dur-fast) var(--ease-out);
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
/* 「停止」只在回合进行中出现(M15 以后的回合控制):审批/长工具执行时的唯一出口 */
.stop {
  height: 34px;
  padding: 0 14px;
  border: 1px solid var(--line-strong);
  border-radius: 10px;
  background: none;
  color: var(--fg-dim);
  font-size: 13px;
  cursor: pointer;
  transition: color var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
}
.stop:hover {
  border-color: var(--err);
  color: var(--err);
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
  transition: background var(--dur-fast) var(--ease-out), transform var(--dur-fast) var(--ease-out);
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
  transition: background var(--dur-fast) var(--ease-out);
}
.hint:hover,
.hint.sel {
  background: var(--accent-soft);
}
.hint.sel .hn {
  text-decoration: underline;
}
.hint.free,
.hint.filt {
  cursor: default;
}
/* 过滤状态行压一行、不参与高亮(hintIdx 只数候选,故用 filt 单独一类) */
.hint.filt {
  padding: 2px 10px 4px;
}
.hint.filt .hn {
  min-width: 0;
  font-weight: 500;
  color: var(--fg-dim);
  text-decoration: none;
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
  transition: background var(--dur-fast) var(--ease-out);
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
  transition: background var(--dur-fast) var(--ease-out);
}
.sel:hover {
  background: var(--bg3);
}
</style>
