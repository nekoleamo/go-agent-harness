// 定时计划 UI 纯逻辑(NOND-W4)。
//
// **前端不做 cron 解析器** —— 这是纪律,不是偷懒:后端 hostschedule 的 parseCron 是唯一
// 权威,前端再判一次就是两套语义,漂移的后果是「界面显示的排期和实际触发的排期不一样」。
// 这里的职责边界:
//   - 控件态 ↔ 七种字面量形态的拼装/拆解(窄形态,不含任何匹配语义)
//   - 中文描述、时间回显、文案
// 反解一律问后端(POST /api/schedules/resolve),回填用返回的 repeat/参数。

import type { RepeatKind, ScheduleView } from './types'

// REPEATS 十档排期(顺序即下拉框顺序:从最常用到最不常用)。
// hint 是这一档的副标题,说清它到底多久跑一次 —— 每 N 分钟这类必须说,否则用户
// 存下一条每分钟烧一轮模型的计划都不知道。
export const REPEATS: { kind: RepeatKind; label: string; hint: string }[] = [
  { kind: 'daily', label: '每天', hint: '每天一次' },
  { kind: 'weekly', label: '每周', hint: '选周几,可多选' },
  { kind: 'weekdays', label: '每个工作日', hint: '周一到周五' },
  { kind: 'monthly_day', label: '每月几号', hint: '没有这天就跳过该月' },
  { kind: 'monthly_nth', label: '每月第几个周几', hint: '如「每月第二个周二」' },
  { kind: 'monthly_last', label: '每月最后一天', hint: '月度结算常用' },
  { kind: 'monthly_last_workday', label: '每月最后一个工作日', hint: '遇周末提前到周五;不含法定节假日' },
  { kind: 'hourly', label: '每小时', hint: '每小时一次' },
  { kind: 'every_n_hour', label: '每 N 小时', hint: '适合轮询' },
  { kind: 'every_n_min', label: '每 N 分钟', hint: '每分钟等于每分钟烧一轮模型' },
  { kind: 'annual_date', label: '每年某天', hint: '每年固定公历日期' },
  { kind: 'lunar_annual', label: '农历节日/农历某天', hint: '中秋、春节这类农历日期(公历上每年都在变)' },
  { kind: 'once', label: '只跑一次', hint: '到点跑一轮就结束' },
]

// LUNAR_FESTIVALS 常用农历节日(月日按农历;day = 0 表示「该月最后一天」,只有除夕用)。
// 给选择器用 —— 让用户选「中秋」比让他填「八月十五」稳得多。
export const LUNAR_FESTIVALS: { name: string; month: number; day: number }[] = [
  { name: '春节', month: 1, day: 1 },
  { name: '除夕', month: 12, day: 0 },
  { name: '元宵节', month: 1, day: 15 },
  { name: '龙抬头', month: 2, day: 2 },
  { name: '端午节', month: 5, day: 5 },
  { name: '七夕', month: 7, day: 7 },
  { name: '中元节', month: 7, day: 15 },
  { name: '中秋节', month: 8, day: 15 },
  { name: '重阳节', month: 9, day: 9 },
  { name: '腊八', month: 12, day: 8 },
]

// LUNAR_MONTHS 农历月的中文名(自定义农历日期时用)。
export const LUNAR_MONTHS = ['正月', '二月', '三月', '四月', '五月', '六月', '七月', '八月', '九月', '十月', '冬月', '腊月']

// lunarMDKey 农历月日 → 落盘键 "MM-DD"(day = 0 表示该月最后一天,写成 "MM-00")。
export function lunarMDKey(month: number, day: number): string {
  return String(month).padStart(2, '0') + '-' + String(day).padStart(2, '0')
}

// lunarDayText 农历日的中文写法(与后端一致:初一/十五/廿三/三十)。
export function lunarDayText(d: number): string {
  if (d === 0) return '最后一天'
  const a = ['', '初一', '初二', '初三', '初四', '初五', '初六', '初七', '初八', '初九', '初十',
    '十一', '十二', '十三', '十四', '十五', '十六', '十七', '十八', '十九', '二十',
    '廿一', '廿二', '廿三', '廿四', '廿五', '廿六', '廿七', '廿八', '廿九', '三十']
  return a[d] ?? String(d)
}

// RepeatLabel 档位的中文名(custom 单独处理:那不是档位,是「控件表达不了」)。
export function repeatLabel(kind: string): string {
  if (kind === 'custom') return '自定义排期'
  return REPEATS.find((r) => r.kind === kind)?.label ?? kind
}

// ScheduleForm 控件态(界面与「控件 ↔ 表达式」之间的唯一中介)。
export interface ScheduleForm {
  repeat: RepeatKind
  hour: number
  minute: number
  dows: number[] // weekly 选中的周几(0=周日)
  day: number // monthly_day;annual_date/lunar_annual 的日(0 = 该月最后一天)
  nth: number // monthly_nth(1-5)
  every: number // every_n_min / every_n_hour
  onceDate: string // once:YYYY-MM-DD
  month: number // annual_date / lunar_annual:月
  // lunarFestival 非空 = 农历用节日名(而非自己选月日);'', 表示选了「自定义农历日期」
  lunarFestival: string
}

// DEFAULT_FORM 默认值:打开表单就能直接保存。
// 「每天 09:00」是最不意外的一档 —— 空白输入框等于要求用户先理解语法才能建第一条计划。
export const DEFAULT_FORM: ScheduleForm = {
  repeat: 'daily',
  hour: 9,
  minute: 0,
  dows: [1],
  day: 1,
  nth: 1,
  every: 2,
  onceDate: '',
  month: 10,
  lunarFestival: '中秋节',
}

// PRESETS 预设卡片:常见意图一键预填。
// 零解析、零失败 —— 比自然语言更划算:「下班前」「早会前」这类模糊意图映射是确定的。
export const PRESETS: { label: string; form: Partial<ScheduleForm> }[] = [
  { label: '每天下班前', form: { repeat: 'daily', hour: 17, minute: 30 } },
  { label: '每天上班前', form: { repeat: 'daily', hour: 8, minute: 30 } },
  { label: '每周一早会前', form: { repeat: 'weekly', dows: [1], hour: 8, minute: 45 } },
  { label: '每周五收尾', form: { repeat: 'weekly', dows: [5], hour: 17, minute: 0 } },
  { label: '每月最后一天结算', form: { repeat: 'monthly_last', hour: 18, minute: 0 } },
  { label: '每 30 分钟查一次', form: { repeat: 'every_n_min', every: 30 } },
]

// structToCron 控件态 → cron 表达式(七种字面量形态的拼装,**不含任何匹配语义**)。
// 返回空串 = 当前状态还不合法(缺周几/缺日期…),界面据此禁用保存并说明原因。
export function structToCron(f: ScheduleForm): string {
  const hm = `${f.minute} ${f.hour}`
  switch (f.repeat) {
    case 'daily':
      return `${hm} * * *`
    case 'weekly':
      return f.dows.length ? `${hm} * * ${[...f.dows].sort((a, b) => a - b).join(',')}` : ''
    case 'weekdays':
      return `${hm} * * 1-5`
    case 'monthly_day':
      return f.day >= 1 && f.day <= 31 ? `${hm} ${f.day} * *` : ''
    case 'monthly_nth':
      if (f.dows.length !== 1) return ''
      // 第 N 个周几 = 该 7 天窗口;连续 7 天必含每个周几各一次 ⇒ 窗口内唯一命中。
      // N=5 时窗口 29-35 被钳到 31(该月没有第 5 个就跳过,与直觉一致)。
      return `${hm} ${(f.nth - 1) * 7 + 1}-${Math.min(f.nth * 7, 31)} * ${f.dows[0]}`
    case 'monthly_last':
      return `${hm} L * *`
    case 'monthly_last_workday':
      return `${hm} LW * *`
    case 'hourly':
      return `${f.minute} * * * *`
    case 'every_n_hour':
      return f.every >= 2 && f.every <= 23 ? `${f.minute} */${f.every} * * *` : ''
    case 'every_n_min':
      return f.every >= 2 && f.every <= 59 ? `*/${f.every} * * * *` : ''
    case 'annual_date':
      // 公历年度排期 cron 就能表达(`M H D Mon *`)
      return `${hm} ${f.day} ${f.month} *`
    case 'lunar_annual':
      // 农历**只能给时分**:cron 的日/月/周必须留 `*`,日期在 lunar_date 上(后端据此排期)。
      // 把农历月日塞进 cron 是错的 —— 那会变成「每年公历 8 月 15 日」。
      return `${hm} * * *`
    case 'once':
      return onceDateToCron(f.onceDate, f.hour, f.minute)
    default:
      return ''
  }
}

// formLunarMD 表单的农历月日 → 落盘键(节日优先,自定义则用月/日)。
export function formLunarMD(f: ScheduleForm): string {
  const fest = LUNAR_FESTIVALS.find((x) => x.name === f.lunarFestival)
  if (fest) return lunarMDKey(fest.month, fest.day)
  return lunarMDKey(f.month, f.day)
}

// formLunarPhrase 农历表单 → 一句中文。
// 预览走后端的自然语言解析(**唯一懂农历的地方**),前端不自己算农历 ——
// 它没有农历表,自己算不了,硬写一份就是两套实现。
export function formLunarPhrase(f: ScheduleForm): string {
  const hm = `${String(f.hour).padStart(2, '0')}:${String(f.minute).padStart(2, '0')}`
  const fest = LUNAR_FESTIVALS.find((x) => x.name === f.lunarFestival)
  if (fest) return `${fest.name} ${hm}`
  return `农历${LUNAR_MONTHS[f.month - 1]}${lunarDayText(f.day)} ${hm}`
}

// onceDateToCron 一次性:日期落到日/月字段,时分落到分/时;**年无处可放**,真正的
// 年份在 once_date 字段里(后端据此排期,cron 只是时刻与月日的载体)。
export function onceDateToCron(date: string, hour: number, minute: number): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
  if (!m) return ''
  return `${minute} ${hour} ${Number(m[3])} ${Number(m[2])} *`
}

// viewToForm 后端 ScheduleView → 控件态(编辑时回填)。
// repeat=custom 时返回 null:调用方应按只读展示,而不是硬塞进某个档位。
export function viewToForm(v: ScheduleView): ScheduleForm | null {
  if (!v.repeat || v.repeat === 'custom') return null
  const f: ScheduleForm = {
    repeat: v.repeat,
    hour: v.hour ?? DEFAULT_FORM.hour,
    minute: v.minute ?? 0,
    dows: v.dows && v.dows.length ? [...v.dows].sort((a, b) => a - b) : [...DEFAULT_FORM.dows],
    day: v.day ?? DEFAULT_FORM.day,
    nth: v.nth ?? DEFAULT_FORM.nth,
    every: v.every ?? DEFAULT_FORM.every,
    onceDate: v.once_date ?? '',
    month: v.month ?? DEFAULT_FORM.month,
    lunarFestival: '',
  }
  if (v.repeat === 'lunar_annual') {
    // 农历:能对上已知节日就用节日档,否则落回「自定义农历日期」
    const m = v.month ?? 0
    const d = v.day ?? 0
    const fest = LUNAR_FESTIVALS.find((x) => x.month === m && x.day === d)
    f.lunarFestival = fest ? fest.name : ''
  }
  return f
}

// todayStr 本地今天(YYYY-MM-DD):once 的日期输入框下限。
export function todayStr(now: Date = new Date()): string {
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
}

// applyPreset 把预设覆盖到当前控件态上(只覆盖预设里给了的字段)。
export function applyPreset(f: ScheduleForm, preset: Partial<ScheduleForm>): ScheduleForm {
  return { ...f, ...preset }
}

// previewLines 预览文案:接下来最多三次触发时刻。
// 这是不会 cron 的用户**唯一的验收手段** —— 看不见就会盲存一条每分钟烧 token 的计划。
export function previewLines(isoTimes: string[] | undefined, now: Date = new Date()): string[] {
  if (!isoTimes || !isoTimes.length) return []
  // 首行带「接下来」,其余只留间隔点 —— 「然后」重复三次读起来像卡带了。
  return isoTimes.slice(0, 3).map((t, i) => (i === 0 ? `接下来 ${fmtAbs(t, now)}` : `· ${fmtAbs(t, now)}`))
}

// costHint 频繁排期的代价提示(每 N 分钟 / 每 N 小时这类必须说,否则用户会
// 存下一条自己完全没意识到的花钱计划)。
export function costHint(f: ScheduleForm): string {
  if (f.repeat === 'every_n_min' && f.every > 0) {
    return `每 ${f.every} 分钟跑一轮,一天约 ${Math.round((24 * 60) / f.every)} 次`
  }
  if (f.repeat === 'every_n_hour' && f.every > 0) return `每 ${f.every} 小时跑一轮,一天约 ${Math.round(24 / f.every)} 次`
  return ''
}

// skipHint 每月几号的跳月提示:选 29-31 号时把「没有这一天的月份会跳过」说出来。
//
// 与其用「限制可选范围」把用户挡在门外(会让人觉得莫名其妙),不如把事实告诉他。
// 刻意做成**静态规则**而不是从 next_runs 里推断:cron 的下一次触发必然落在有该日的
// 那个月(没有就自动跳过),所以从预览的首条永远看不出跳月 —— 换句话说,靠预览推断
// 等于永远提示不出来。想看实际会跑哪天,看预览的「接下来三次」本身就够了。
export function skipHint(f: ScheduleForm): string {
  if (f.repeat !== 'monthly_day' || f.day <= 28) return ''
  return `没有 ${f.day} 号的月份会跳过这个月(如 2 月),实际会跑哪天看下面的预览`
}

// —— 时间回显(本地时区)——

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
