// 产物/变更聚合栏(借鉴 WorkBuddy 的「结果区」,第一百零五批)。
//
// 数据来自**已有的** `file/change` 会话事件账本(changes.ts 的投影)—— 本模块只做
// 聚合与入口,不发明数据,也不新增后端端点(与 traj/changes 同纪律)。
//
// 诚实边界(三条,都写在界面上):
//  ① 只列**已落盘**的改动。被沙箱/审批拒绝的写**根本没有 file/change 事件**
//     (它压根没写盘),所以这里不列 —— 要看被拒的写,得去会话流里的工具结果。
//  ② 口径是「本次会话经工具改过的文件」,**不含**用户自己在外面改的东西
//     (同 ChangesView 的口径,也不依赖 git)。
//  ③ 会话窗口不完整时(只加载了尾部),这个数是「窗口内」不是「全会话」——
//     由 partial 标注,与 ChangesView 同一句提醒。

import type { FileChange } from './changes'

export interface ArtifactItem {
  path: string
  /** 是否本会话新建(其它情况一律按"修改"呈现;删除单独给 created/deleted 之外的一档) */
  created: boolean
  deleted: boolean
  added: number
  removed: number
  binary: boolean
  /** 最近一次改动的时间戳(缺失时为空串) */
  ts: string
}

/**
 * 从 ChangesModel 的文件列表抽产物清单。
 *
 * 排序:**按最近改动时间倒序** —— 产物栏的用途是「这一轮做出了什么」,刚写完的
 * 排最前符合直觉;按路径排会让第二次改动的文件沉到列表中间。没时间的(理论上不会,
 * 事件都带 ts)按路径兜底,保证结果稳定(同输入必同序,便于测试与视觉稳定)。
 */
export function artifactsOf(files: FileChange[]): ArtifactItem[] {
  const items = (files || []).map((f) => {
    const last = f.hunks && f.hunks.length ? f.hunks[f.hunks.length - 1] : null
    return {
      path: f.path,
      created: !!f.created,
      // 删除的判据:该文件最后一个操作是 delete。hunks 里保留全部操作(去重保序),
      // 所以看最后一条即可;没有 hunks 的条目按"未删除"处理(不猜)。
      deleted: !!last && (last.op === 'delete' || last.op === 'deleted'),
      added: f.added || 0,
      removed: f.removed || 0,
      binary: !!f.binary,
      ts: last ? last.ts : '',
    }
  })
  // 按最近改动时间**倒序**(刚产出的排最前);时间相同或都缺失时按路径兜底,
  // 保证同输入必同序 —— 视觉稳定,测试可钉。
  return items.sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : a.path.localeCompare(b.path)))
}

/** 一行摘要:「3 个文件 · 新建 1 · +120 −8」。空清单返回空串(不显示空栏)。 */
export function artifactsBrief(items: ArtifactItem[]): string {
  if (!items.length) return ''
  let added = 0
  let removed = 0
  let created = 0
  for (const it of items) {
    added += it.added || 0
    removed += it.removed || 0
    if (it.created) created++
  }
  const parts: string[] = [`${items.length} 个文件`]
  if (created) parts.push(`新建 ${created}`)
  if (added) parts.push(`+${added}`)
  if (removed) parts.push(`−${removed}`)
  return parts.join(' · ')
}

/** 逐项一行标签(文件名 + 操作 + 行数);抽屉与窄屏共用。 */
export function artifactLabel(it: ArtifactItem): string {
  const name = it.path.split('/').pop() || it.path
  const op = it.deleted ? '删除' : it.created ? '新建' : '修改'
  const lines = it.binary ? ' · 二进制' : it.added || it.removed ? ` +${it.added} −${it.removed}` : ''
  return `${name} · ${op}${lines}`
}

/**
 * 产物栏在**第一处改动出现**后才该出现。
 *
 * 为何要这个判据而不是"有文件就显示":改动文件是**每一次任务**都会有的,而"产物"
 * 是"这一轮做出了值得看的东西"。一个空栏常驻在会话流顶部只会变成噪音 ——
 * 与既有「没有 provider 时才显示引导入口」是同一条纪律:提示要有理由才出现。
 */
export function artifactsVisible(items: ArtifactItem[]): boolean {
  return items.length > 0
}
