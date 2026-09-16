// 工作区目录比较:判断「后端记下的工作区目录」与「用户输入/选择器给的那个」是不是同一个。
//
// 为什么要单独抽出来并配单测:同一目录在不同平台/来源下写法不同 —— Windows 大小写不敏感、
// 两种分隔符都合法(用户从资源管理器复制的路径还常带尾部分隔符),而后端 SwitchDir 还会做
// EvalSymlinks 归一化(Windows 8.3 短名、macOS /var → /private/var)。界面上「切换后工作区
// 列表里有没有它」就靠这个判断,判错会让输入面板一直不收(真机 2026-09-17 的现象之一)。
//
// 只用于界面确认,不做安全判断 —— 大小写折叠在区分大小写的文件系统上极小概率误判,可接受。
export function sameDir(a: string, b: string): boolean {
  if (a.length === 0 || b.length === 0) return false
  const norm = (s: string): string => s.replace(/[\\/]+$/, '').replace(/\\/g, '/').toLowerCase()
  return norm(a) === norm(b)
}
