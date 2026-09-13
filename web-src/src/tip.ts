// data-tip 统一提示层(2026-09-14,Win 端反馈「提示被挡住/超出界面」)。
// 此前是 [data-tip]:hover::after 伪元素:提示挂在触发器**内部**,凡是祖先有 overflow:hidden
// (设置抽屉、侧栏滚动区、输入外壳)就会被裁掉;居中定位的提示在屏幕边缘又会横向溢出视口。
// 改为单例 fixed 层:按触发器 rect 定位 → 视口内夹取 → 上方空间不足时翻到下方。
// 文案一律经 textContent 写入(不解析 HTML,与渲染层禁 v-html 同一纪律)。
const GAP = 6 // 与触发器间距
const MARGIN = 8 // 距视口边缘最小距离
const MAX_W = 300

let layer: HTMLDivElement | null = null
let cur: HTMLElement | null = null

function ensureLayer(): HTMLDivElement {
  if (!layer) {
    layer = document.createElement('div')
    layer.className = 'gah-tip'
    layer.setAttribute('role', 'tooltip')
    document.body.appendChild(layer)
  }
  return layer
}

// place 把提示定位到触发器上方;上方不够则翻到下方;左右夹进视口。
function place(el: HTMLElement): void {
  const t = ensureLayer()
  t.style.visibility = 'hidden'
  t.style.display = 'block'
  const r = el.getBoundingClientRect()
  const tr = t.getBoundingClientRect()
  let top = r.top - tr.height - GAP
  if (top < MARGIN) {
    const below = r.bottom + GAP
    top = below + tr.height > window.innerHeight - MARGIN ? Math.max(MARGIN, window.innerHeight - tr.height - MARGIN) : below
  }
  const left = Math.max(MARGIN, Math.min(r.left + r.width / 2 - tr.width / 2, window.innerWidth - tr.width - MARGIN))
  t.style.top = Math.round(top) + 'px'
  t.style.left = Math.round(left) + 'px'
  t.style.visibility = 'visible'
}

function show(el: HTMLElement): void {
  const txt = el.getAttribute('data-tip') ?? ''
  if (!txt.trim()) {
    hide()
    return
  }
  cur = el
  const t = ensureLayer()
  t.textContent = txt
  t.style.maxWidth = Math.min(MAX_W, window.innerWidth - MARGIN * 2) + 'px'
  place(el)
}

function hide(): void {
  cur = null
  if (layer) {
    layer.style.display = 'none'
    layer.textContent = ''
  }
}

function triggerOf(node: EventTarget | null): HTMLElement | null {
  return node instanceof Element ? node.closest<HTMLElement>('[data-tip]') : null
}

// installTips 全局安装一次(幂等;main.ts 在挂载前调用)。
export function installTips(): void {
  const doc = document
  doc.addEventListener(
    'mouseover',
    (e) => {
      const el = triggerOf(e.target)
      if (el) {
        if (el !== cur) show(el)
        return
      }
      // 移到非触发器区域(且不在当前触发器内)→ 收起
      if (cur && !(e.target instanceof Node && cur.contains(e.target))) hide()
    },
    true,
  )
  doc.addEventListener('mouseout', (e) => {
    if (!cur) return
    const to = e.relatedTarget as Node | null
    if (!to || !(cur.contains(to) || cur === to)) hide()
  })
  doc.addEventListener('focusin', (e) => {
    const el = triggerOf(e.target)
    if (el) show(el)
  })
  doc.addEventListener('focusout', () => hide())
  // 滚动/尺寸变化时位置会失真:滚动就地重算,尺寸变化直接收起(等下次 hover)
  doc.addEventListener(
    'scroll',
    () => {
      if (cur) place(cur)
    },
    true,
  )
  window.addEventListener('resize', () => hide())
  // 交互开始即收起:提示不参与点按(pointer-events:none),避免视觉残影
  doc.addEventListener('mousedown', () => hide(), true)
  doc.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') hide()
  })
}
