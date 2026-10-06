// 定时计划 UI 纯逻辑单测(node --test):控件态 ↔ 表达式、文案、中文时间回显。
//
// 这里**没有** cron 形状校验用例:界面不再让用户手写表达式,那条校验器随之删除
// (留着就是没人调用的死代码)。语义校验在后端 hostschedule 包里测。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DEFAULT_FORM,
  PRESETS,
  REPEATS,
  applyPreset,
  costHint,
  formLunarMD,
  formLunarPhrase,
  lunarDayText,
  fmtAbs,
  fmtNextRun,
  fmtRel,
  isUnsetTime,
  onceDateToCron,
  previewLines,
  repeatLabel,
  skipHint,
  statusLabel,
  structToCron,
  viewToForm,
} from './schedule.ts'
import type { ScheduleForm } from './schedule.ts'

test('isUnsetTime 识别空值/Go 零时刻/坏串', () => {
  assert.equal(isUnsetTime(undefined), true)
  assert.equal(isUnsetTime(''), true)
  assert.equal(isUnsetTime('0001-01-01T00:00:00Z'), true)
  assert.equal(isUnsetTime('不是时间'), true)
  assert.equal(isUnsetTime('2026-11-15T08:00:00Z'), false)
})

test('fmtNextRun 无排期原因;有排期给绝对 + 相对', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0) // 2026-11-14 20:00 本地
  assert.equal(fmtNextRun(undefined, now), '未排期')
  assert.equal(fmtNextRun('0001-01-01T00:00:00Z', now), '未排期')

  const today = new Date(2026, 10, 14, 21, 30, 0).toISOString()
  assert.equal(fmtNextRun(today, now), '今天 21:30(2 小时后)')

  const tomorrow = new Date(2026, 10, 15, 8, 0, 0).toISOString()
  assert.equal(fmtNextRun(tomorrow, now), '明天 08:00(12 小时后)')

  const far = new Date(2026, 11, 20, 8, 0, 0).toISOString()
  assert.equal(fmtNextRun(far, now), '2026-12-20 08:00(36 天后)') // 35.5 天,四舍五入
})

test('fmtNextRun 已到点/已过点不显示为未来', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0)
  const soon = new Date(now.getTime() + 20 * 1000).toISOString()
  assert.ok(fmtNextRun(soon, now).includes('即将触发'), fmtNextRun(soon, now))
  const past = new Date(now.getTime() - 3600 * 1000).toISOString()
  assert.ok(fmtNextRun(past, now).includes('前'), fmtNextRun(past, now))
})

test('fmtAbs/fmtRel 跨天与分钟/小时/天档', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0)
  const y = new Date(2026, 10, 13, 8, 30, 0).toISOString()
  assert.equal(fmtAbs(y, now), '昨天 08:30')
  assert.equal(fmtRel(new Date(now.getTime() - 30 * 1000).toISOString(), now), '刚刚')
  assert.equal(fmtRel(new Date(now.getTime() + 5 * 60000).toISOString(), now), '5 分钟后')
  assert.equal(fmtRel(new Date(now.getTime() + 3 * 3600000).toISOString(), now), '3 小时后')
  assert.equal(fmtRel(new Date(now.getTime() + 3 * 86400000).toISOString(), now), '3 天后')
})

test('statusLabel 覆盖四种终态', () => {
  assert.equal(statusLabel('ok'), '成功')
  assert.equal(statusLabel('failed'), '失败')
  assert.equal(statusLabel('skipped'), '跳过')
  assert.equal(statusLabel(''), '未运行')
  assert.equal(statusLabel(undefined), '未运行')
})

// —— 控件化改造(NOND-W4 二次:不会 cron 是默认前提)——

test('structToCron 十档:拼出的是后端能反解回同一档位的字面量形态', () => {
  const cases: [Partial<ScheduleForm>, string][] = [
    [{ repeat: 'daily', hour: 8, minute: 0 }, '0 8 * * *'],
    [{ repeat: 'weekly', hour: 6, minute: 30, dows: [1] }, '30 6 * * 1'],
    [{ repeat: 'weekly', hour: 9, minute: 0, dows: [5, 3, 1] }, '0 9 * * 1,3,5'],
    [{ repeat: 'weekdays', hour: 9, minute: 0 }, '0 9 * * 1-5'],
    [{ repeat: 'monthly_day', hour: 0, minute: 0, day: 5 }, '0 0 5 * *'],
    [{ repeat: 'monthly_nth', hour: 9, minute: 0, nth: 2, dows: [2] }, '0 9 8-14 * 2'],
    [{ repeat: 'monthly_nth', hour: 9, minute: 0, nth: 5, dows: [3] }, '0 9 29-31 * 3'],
    [{ repeat: 'monthly_last', hour: 9, minute: 0 }, '0 9 L * *'],
    [{ repeat: 'monthly_last_workday', hour: 18, minute: 0 }, '0 18 LW * *'],
    [{ repeat: 'hourly', hour: 0, minute: 30 }, '30 * * * *'],
    [{ repeat: 'every_n_hour', hour: 0, minute: 0, every: 6 }, '0 */6 * * *'],
    [{ repeat: 'every_n_min', hour: 0, minute: 0, every: 15 }, '*/15 * * * *'],
  ]
  for (const [patch, want] of cases) {
    const got = structToCron({ ...DEFAULT_FORM, ...patch } as ScheduleForm)
    assert.equal(got, want, `${JSON.stringify(patch)} 应拼出 ${want}`)
  }
})

test('structToCron 状态不合法时返回空串(不给半成品表达式)', () => {
  const base = { ...DEFAULT_FORM }
  assert.equal(structToCron({ ...base, repeat: 'weekly', dows: [] }), '')
  assert.equal(structToCron({ ...base, repeat: 'monthly_day', day: 0 }), '')
  assert.equal(structToCron({ ...base, repeat: 'monthly_nth', dows: [] }), '')
  assert.equal(structToCron({ ...base, repeat: 'every_n_min', every: 1 }), '') // 每分钟不提供
  assert.equal(structToCron({ ...base, repeat: 'every_n_min', every: 99 }), '')
  assert.equal(structToCron({ ...base, repeat: 'once', onceDate: '' }), '')
})

test('once 的 cron 只承载时分月日,年落在 once_date 上', () => {
  assert.equal(onceDateToCron('2026-11-20', 9, 0), '0 9 20 11 *')
  assert.equal(onceDateToCron('不是日期', 9, 0), '')
})

test('viewToForm 回填;custom 一律 null(自定义走原始表达式入口)', () => {
  const f = viewToForm({ cron: '0 9 8-14 * 2', label: '每月第二个周二 09:00', next_runs: [], repeat: 'monthly_nth', hour: 9, minute: 0, nth: 2, dows: [2] })
  assert.deepEqual(f, {
    repeat: 'monthly_nth', hour: 9, minute: 0, dows: [2], day: 1, nth: 2, every: 2,
    onceDate: '', month: 10, lunarFestival: '',
  })
  assert.equal(viewToForm({ cron: '5,35 8-10 * * 1,3', label: '', next_runs: [], repeat: 'custom' }), null)
})

test('农历档:节日与自定义月日两种填法', () => {
  const base = { ...DEFAULT_FORM, repeat: 'lunar_annual' as const }
  // 节日档 → 月日取自节日表,落盘键是农历月日
  assert.equal(formLunarMD({ ...base, lunarFestival: '中秋节' }), '08-15')
  assert.equal(formLunarMD({ ...base, lunarFestival: '除夕' }), '12-00') // day=0 = 该月最后一天
  assert.equal(formLunarMD({ ...base, lunarFestival: '', month: 5, day: 5 }), '05-05')
  // 农历档的 cron 只给时分 —— 日期绝不能塞进 cron(那会变成公历 8 月 15 日)
  assert.equal(structToCron(base), '0 9 * * *')
  // 预览用一句中文走后端(前端没有农历表,算不了)
  assert.equal(formLunarPhrase({ ...base, lunarFestival: '中秋节', hour: 20, minute: 0 }), '中秋节 20:00')
  assert.equal(formLunarPhrase({ ...base, lunarFestival: '', month: 9, day: 9, hour: 9, minute: 0 }), '农历九月初九 09:00')
  // 回填:能对上节日就用节日档
  const back = viewToForm({ cron: '0 20 * * *', label: '每年中秋节(农历八月十五)20:00', next_runs: [], repeat: 'lunar_annual', hour: 20, minute: 0, month: 8, day: 15 })
  assert.equal(back?.repeat, 'lunar_annual')
  assert.equal(back?.lunarFestival, '中秋节')
  // 认不出节日 → 落自定义农历日期(而不是丢掉月日)
  const odd = viewToForm({ cron: '0 20 * * *', label: '', next_runs: [], repeat: 'lunar_annual', hour: 20, minute: 0, month: 3, day: 12 })
  assert.equal(odd?.lunarFestival, '')
  assert.equal(odd?.month, 3)
  assert.equal(odd?.day, 12)
})

test('公历年度档:cron 能表达,月日进 cron', () => {
  const f = { ...DEFAULT_FORM, repeat: 'annual_date' as const, month: 10, day: 1, hour: 9, minute: 0 }
  assert.equal(structToCron(f), '0 9 1 10 *')
  const back = viewToForm({ cron: '0 9 1 10 *', label: '每年 10 月 1 日 09:00', next_runs: [], repeat: 'annual_date', hour: 9, minute: 0, month: 10, day: 1 })
  assert.equal(back?.month, 10)
  assert.equal(back?.day, 1)
})

test('lunarDayText 覆盖初十/廿三/三十/最后一天', () => {
  assert.equal(lunarDayText(1), '初一')
  assert.equal(lunarDayText(10), '初十')
  assert.equal(lunarDayText(23), '廿三')
  assert.equal(lunarDayText(30), '三十')
  assert.equal(lunarDayText(0), '最后一天')
})

test('DEFAULT_FORM 打开即可保存(不需要先理解语法)', () => {
  assert.equal(structToCron(DEFAULT_FORM), '0 9 * * *')
})

test('costHint 把频繁排期的代价说出来', () => {
  const base = { ...DEFAULT_FORM }
  assert.ok(costHint({ ...base, repeat: 'every_n_min', every: 30 }).includes('48 次'))
  assert.ok(costHint({ ...base, repeat: 'every_n_hour', every: 4 }).includes('6 次'))
  assert.equal(costHint({ ...base, repeat: 'daily' }), '')
})

test('skipHint 每月 29-31 号把「会跳月」说出来(不靠限制可选范围)', () => {
  const base = { ...DEFAULT_FORM }
  assert.ok(skipHint({ ...base, repeat: 'monthly_day', day: 31 }).includes('跳过'))
  assert.ok(skipHint({ ...base, repeat: 'monthly_day', day: 29 }).includes('跳过'))
  assert.equal(skipHint({ ...base, repeat: 'monthly_day', day: 28 }), '')
  assert.equal(skipHint({ ...base, repeat: 'daily', day: 31 }), '')
})

test('previewLines 最多三次;无排期给空(界面显示「不会触发」而不是空白)', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0)
  // 用本地时刻构造再转 ISO:fmtAbs 按本地时区渲染,测试不能假设机器时区是 UTC。
  const ts = [15, 16, 17, 18].map((d) => new Date(2026, 10, d, 8, 0, 0).toISOString())
  assert.equal(previewLines(ts, now).length, 3)
  assert.ok(previewLines(ts, now)[0].startsWith('接下来 '))
  assert.deepEqual(previewLines([], now), [])
  assert.deepEqual(previewLines(undefined, now), [])
})

test('repeatLabel 覆盖十档与 custom', () => {
  for (const r of REPEATS) assert.equal(repeatLabel(r.kind), r.label)
  assert.equal(repeatLabel('custom'), '自定义排期')
})

// LW 只排除周六周日(见后端 cron.go 的 lastWeekdayOfMonth),而日常语境里的「工作日」
// 几乎总是指「法定工作日」—— 下拉项不说清,用户会以为调休上班日照跑。
// 同一句边界声明在 Go 的 labelOf 里也有一份(host-schedule 的 cronexpr_test.go 钉住那份);
// 哪边被改都会漂移,故此处钉住「这句不许消失」。
test('LW 档位的 hint 必须声明边界', () => {
  const lw = REPEATS.find((r) => r.kind === 'monthly_last_workday')
  assert.ok(lw, 'REPEATS 里应仍有 monthly_last_workday 档位')
  assert.ok(lw.hint.includes('不含法定节假日'), `LW hint 丢了边界声明:${lw.hint}`)
  assert.ok(lw.hint.includes('遇周末提前到周五'), `LW hint 应说明周末如何处理:${lw.hint}`)
})
