// REST 客户端(上行)+ 工具函数。核心交互纯 REST,不绕模板渲染。
import type { AttachmentView, CommandOptionsResp, CommandView, DocTree, DocView, Job, ModelsAllResp, PluginInfo, ProviderInfo, Schedule, SessionInfo, StateView, WorkspaceInfo } from './types'

const BASE = ''
const json = {
  'Content-Type': 'application/json',
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(BASE + path, init)
  if (!resp.ok) {
    let msg = resp.statusText
    try {
      const body = await resp.text()
      if (body) msg = body
    } catch {
      /* ignore */
    }
    throw new Error(`HTTP ${resp.status}: ${msg}`)
  }
  return (await resp.json()) as T
}

export const api = {
  state(): Promise<StateView> {
    return req('/api/state')
  },
  commands(): Promise<CommandView[]> {
    return req('/api/commands')
  },
  // 命令参数级(逐级确认;与 TUI 选择器同一注册表声明):picked 不含命令名
  commandOptions(name: string, picked: string[]): Promise<CommandOptionsResp> {
    return req('/api/commands/' + encodeURIComponent(name) + '/options', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ picked }),
    })
  },
  sessions(): Promise<SessionInfo[]> {
    return req('/api/sessions')
  },
  // F 组 F2:置顶/取消置顶(经 action 分派,不新增端点)
  sessionPin(id: string, pinned: boolean): Promise<void> {
    return req('/api/sessions', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ action: pinned ? 'pin' : 'unpin', id }),
    })
  },
  // F 组 F3:生成/取回会话概述(会调用模型;force = 忽略缓存)
  sessionSummary(id: string, force = false): Promise<{ summary: { text: string; topics?: string[] }; auto: boolean }> {
    return req('/api/sessions/summary', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ id, force }),
    })
  },
  workspaces(): Promise<WorkspaceInfo[]> {
    return req('/api/workspaces')
  },
  // 提交输入(回合)或斜杠命令;running 时后端 409 拒绝;attachments=附件本地路径(/api/attachments 返回)
  input(content: string, attachments?: string[]): Promise<void> {
    return req('/api/input', {
      method: 'POST',
      headers: json,
      body: JSON.stringify({ content, attachments: attachments ?? [] }),
    })
  },
  // 附件上传(multipart;返回落盘视图;后端大小/类型/数量白名单)
  upload(file: File): Promise<AttachmentView> {
    const fd = new FormData()
    fd.append('file', file)
    return req('/api/attachments', { method: 'POST', body: fd })
  },
  // 状态栏级控制(模型/思考/沙箱/审批/工作区;M17 审批档位 open|smart|strict)
  control(body: { model?: string; thinking?: string; sandbox?: string; approval?: string; workspace?: string }): Promise<void> {
    return req('/api/control', { method: 'POST', headers: json, body: JSON.stringify(body) })
  },

  // —— 整体备份(M18) ——
  backups(): Promise<{ name: string; size: number; time: number }[]> {
    return req('/api/backup')
  },
  backupNow(dest?: string): Promise<{ ok: true; name: string }> {
    return req('/api/backup', { method: 'POST', headers: json, body: JSON.stringify({ action: 'backup', dest: dest ?? '' }) })
  },
  backupRestore(name: string): Promise<{ ok: true; restored: string }> {
    return req('/api/backup', { method: 'POST', headers: json, body: JSON.stringify({ action: 'restore', name }) })
  },
  confirm(id: string, ok: boolean): Promise<void> {
    return req('/api/confirm', { method: 'POST', headers: json, body: JSON.stringify({ id, ok }) })
  },
  // 结构化提问作答(P3;values=选项值,text=自由文本,二者可并存)
  questionAnswer(id: string, values: string[], text: string): Promise<void> {
    return req('/api/question', { method: 'POST', headers: json, body: JSON.stringify({ id, values, text }) })
  },
  sessionSwitch(id: string): Promise<void> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'switch', id }) })
  },
  sessionNew(): Promise<{ id: string }> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'new' }) })
  },
  // 删除会话记录(仅删该会话 jsonl 与名字索引,不动任何目录/文件夹)
  sessionDelete(id: string): Promise<{ session?: unknown }> {
    return req('/api/sessions', { method: 'POST', headers: json, body: JSON.stringify({ action: 'delete', id }) })
  },
  // 会话改名(仅作用于当前会话;改名即切换目标会话)
  sessionRename(name: string): Promise<void> {
    return req('/api/sessions/rename', { method: 'POST', headers: json, body: JSON.stringify({ name }) })
  },
  // 删除工作区记录(仅移除记录,不删除对应文件夹)
  workspaceForget(key: string): Promise<void> {
    return req('/api/workspaces/' + encodeURIComponent(key), { method: 'DELETE' })
  },
  // —— 设置面板 ——
  models(): Promise<ModelsAllResp> {
    return req('/api/models?all=1')
  },
  providers(): Promise<ProviderInfo[]> {
    return req('/api/providers')
  },
  providerAdd(p: { name: string; base_url: string; api_key: string; model?: string }): Promise<void> {
    return req('/api/providers', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  providerUse(name: string): Promise<void> {
    return req('/api/providers/' + encodeURIComponent(name) + '/use', { method: 'POST', headers: json, body: '{}' })
  },
  providerDelete(name: string): Promise<void> {
    return req('/api/providers/' + encodeURIComponent(name), { method: 'DELETE' })
  },
  plugins(): Promise<PluginInfo[]> {
    return req('/api/plugins')
  },
  pluginToggle(id: string, on: boolean): Promise<void> {
    return req('/api/plugins/' + encodeURIComponent(id) + (on ? '/load' : '/unload'), { method: 'POST', headers: json, body: '{}' })
  },
  compact(prompt?: string): Promise<{ summary: string; folded: number }> {
    return req('/api/compact', { method: 'POST', headers: json, body: JSON.stringify({ prompt: prompt ?? '' }) })
  },
  settingsHistory(n: number): Promise<{ history: number }> {
    return req('/api/settings/history', { method: 'POST', headers: json, body: JSON.stringify({ n }) })
  },
  reload(): Promise<void> {
    return req('/api/reload', { method: 'POST', headers: json, body: '{}' })
  },
  // —— 文档预览(D1;/api/doc/*;未装配 503,前端据首次探测隐藏入口) ——
  docPreview(path: string, opts?: { page?: number; sheet?: number; max?: number }): Promise<DocView> {
    const q = new URLSearchParams({ path })
    if (opts?.page) q.set('page', String(opts.page))
    if (opts?.sheet) q.set('sheet', String(opts.sheet))
    if (opts?.max) q.set('max', String(opts.max))
    return req('/api/doc/preview?' + q.toString())
  },
  docTree(path: string, depth = 2): Promise<DocTree> {
    const q = new URLSearchParams({ path, depth: String(depth) })
    return req('/api/doc/tree?' + q.toString())
  },
  docRender(text: string): Promise<DocView> {
    return req('/api/doc/render', { method: 'POST', headers: json, body: JSON.stringify({ text }) })
  },
  // —— 后台任务 ——
  jobs(): Promise<Job[]> {
    return req('/api/jobs')
  },
  jobKill(id: string): Promise<void> {
    return req('/api/jobs/' + encodeURIComponent(id) + '/kill', { method: 'POST', headers: json, body: '{}' })
  },
  // —— 定时计划(NOND-W4) ——
  schedules(): Promise<Schedule[]> {
    return req('/api/schedules')
  },
  scheduleAdd(p: { name: string; cron: string; prompt: string; enabled?: boolean }): Promise<Schedule> {
    return req('/api/schedules', { method: 'POST', headers: json, body: JSON.stringify(p) })
  },
  // 仅覆盖传入字段(未传 = 不改;enabled 走 undefined 区分 false)
  scheduleUpdate(id: string, p: { name?: string; cron?: string; prompt?: string; enabled?: boolean }): Promise<Schedule> {
    return req('/api/schedules/' + encodeURIComponent(id), { method: 'PATCH', headers: json, body: JSON.stringify(p) })
  },
  scheduleDelete(id: string): Promise<void> {
    return req('/api/schedules/' + encodeURIComponent(id), { method: 'DELETE', headers: json, body: '{}' })
  },
  scheduleRun(id: string): Promise<void> {
    return req('/api/schedules/' + encodeURIComponent(id) + '/run', { method: 'POST', headers: json, body: '{}' })
  },
}

// 工具结果摘要:截断长文本(对齐 TUI 折叠语义)
export function summarize(text: string, max = 600): string {
  if (text.length <= max) return text
  return text.slice(0, max) + '\n…'
}
