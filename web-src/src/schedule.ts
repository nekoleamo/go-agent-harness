// 定时计划 UI 纯逻辑(NOND-W4):cron 形状校验(仅形状,完整语义以后端为准)+
// 中文时间回显。不自研 cron 构造器 —— 用户直接写 5 字段表达式。

// cronSpec 五段顺序(与后端一致:cron 需 5 字段「分 时 日 月 周」)。
export const CRON_FIELDS = ['分', '时', '日', '月', '周'] as const

// 月份/星期的三字英文缩写(POSIX cron 允许;后端同样接受)。
const NAMES: Record<string, number> = {
  jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6,
  jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12,
  sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6,
}

// cronShapeError 形状校验:段数 + 每段字符集。返回空串表示形状像样
// (是否真的能匹配到未来时刻由后端判定 —— 前端不做完整 parser,避免两套语义漂移)。
export function cronShapeError(expr: string): string {
  const parts = expr.trim().split(/\s+/).filter((s) => s.length > 0)
  if (parts.length === 0) return '请填写 cron 表达式,如 0 8 * * *'
  if (parts.length !== 5) {
    return `cron 需要 5 段「分 时 日 月 周」,收到 ${parts.length} 段,如 0 8 * * *`
  }
  for (let i = 0; i < parts.length; i++) {
    if (!validField(parts[i])) {
      return `第 ${i + 1} 段「${CRON_FIELDS[i]}」含非法字符:${parts[i]}(只支持数字、* / , - 与三字母缩写)`
    }
  }
  return ''
}

// validField 单段:数字/通配/区间/步长/列表 的字符集 + 三字母名。
function validField(f: string): boolean {
  for (const item of f.split(',')) {
    if (item === '') return false
    const [range, step] = item.split('/')
    if (step !== undefined && !/^\d+$/.test(step)) return false
    if (step !== undefined && item.split('/').length !== 2) return false
    if (range === '*') continue
    const segs = range.split('-')
    if (segs.length > 2) return false
    for (const seg of segs) {
      if (seg === '*') continue
      if (/^\d+$/.test(seg)) continue
      if (NAMES[seg.toLowerCase()] !== undefined) continue
      return false
    }
  }
  return true
}

// isUnsetTime Go time.Time 零值("0001-01-01T00:00:00Z")/ 空串 = 无该时间。
export function isUnsetTime(iso?: string): boolean {
  if (!iso) return true
  const d = new Date(iso)
  if (isNaN(d.getTime())) return true
  return d.getUTCFullYear() <= 1
}

// fmtAbs 绝对时间(本地时区;今天/明天用中文词,再远给日期)。
export function fmtAbs(iso?: string, now: Date = new Date()): string {
  if (isUnsetTime(iso)) return ''
  const d = new Date(iso as string)
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const day = dayDiff(d, now)
  if (day === 0) return `今天 ${hm}`
  if (day === 1) return `明天 ${hm}`
  if (day === -1) return `昨天 ${hm}`
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`
}

// fmtRel 中文相对时间(给人「还有多久」的直觉,绝对值由 fmtAbs 承担)。
export function fmtRel(iso?: string, now: Date = new Date()): string {
  if (isUnsetTime(iso)) return ''
  const d = new Date(iso as string)
  const sec = Math.round((d.getTime() - now.getTime()) / 1000)
  const abs = Math.abs(sec)
  const suffix = sec < 0 ? '前' : '后'
  if (abs < 60) return sec < 0 ? '刚刚' : '不到 1 分钟' + suffix
  const min = Math.round(abs / 60)
  if (min < 60) return `${min} 分钟${suffix}`
  const hour = Math.round(min / 60)
  if (hour < 24) return `${hour} 小时${suffix}`
  const day = Math.round(hour / 24)
  return `${day} 天${suffix}`
}

// fmtNextRun 「下次运行时间」回显:中文绝对时间 + 相对时间;无排期给出原因文案。
export function fmtNextRun(iso?: string, now: Date = new Date()): string {
  if (isUnsetTime(iso)) return '未排期'
  const abs = fmtAbs(iso, now)
  const rel = fmtRel(iso, now)
  return rel.startsWith('不到 1 分钟') || rel === '刚刚' ? `${abs}(即将触发)` : `${abs}(${rel})`
}

// statusLabel 最近一次运行的终态中文名。
export function statusLabel(s?: string): string {
  switch (s) {
    case 'ok':
      return '成功'
    case 'failed':
      return '失败'
    case 'skipped':
      return '跳过'
    default:
      return '未运行'
  }
}

function pad(n: number): string {
  return n < 10 ? '0' + n : String(n)
}

// dayDiff 日历天差(本地时区:0=同一天,1=明天,-1=昨天)。
function dayDiff(d: Date, now: Date): number {
  const a = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  const b = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
  return Math.round((a - b) / 86400000)
}
