// 会话导出的**单一实现**:侧栏会话行的 ⤓ 与顶栏「导出」共用这一份。
//
// 为什么要抽出来(2026-10-30):侧栏行内导出在**空会话**上没有行、图标又小,
// 用户报「侧栏未见导出按钮」;顶栏补一个入口时若各写一份导出流程,
// 「浏览器下载 / 桌面壳交壳落盘」这两条路迟早走偏(桌面壳那条是踩过坑的:
// WebView 没有下载通道,`<a download>` 点了什么都不会发生)。
import { sessionExportName, sessionExportUrl } from './api'
import { isDesktop, saveExport, shellLog } from './desktop'

export type ExportFmt = 'html' | 'jsonl'

// exportSession 导出指定会话(id 空 = 主会话)。
//   - 浏览器:交给 `<a download>`(文件名由后端 Content-Disposition 定);
//   - 桌面壳:取内容 → 壳侧落盘(网页额外用系统默认程序打开,对齐 TUI /export)。
// 返回桌面壳的落盘路径;浏览器下载返回空串。失败一律抛错,由调用方决定怎么说。
export async function exportSession(id: string, fmt: ExportFmt): Promise<string> {
  const url = sessionExportUrl(id, fmt)
  const name = sessionExportName(id, fmt)
  if (!isDesktop) {
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    return ''
  }
  // 桌面壳:WebView 没有下载通道 ⇒ 取内容交壳落盘(见 desktop.ts 的 saveExport)。
  const resp = await fetch(url)
  if (!resp.ok) throw new Error('HTTP ' + resp.status)
  const path = await saveExport(name, await resp.text(), fmt === 'html')
  if (!path) throw new Error('壳未能落盘(详见壳日志 gah-shell.log)')
  shellLog('导出 ' + name + ' → ' + path)
  return path
}
