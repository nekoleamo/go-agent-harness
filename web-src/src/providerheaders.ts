// provider 自定义请求头的解析(纯函数,零依赖;SettingsPanel 消费)。
//
// 为什么单独成模块:它**必须**能被单测直接覆盖 —— 这是「用户填的头到底生效没有」这条链上
// 唯一能被离线验证的一环(其余要真发请求)。塞在 .vue 里就只能靠正则读源码来测,那种测试
// 既脆弱又没意义。
//
// 为什么是「键: 值」逐行而不是两个输入框:头值里常带冒号、等号和占位符,单行输入框看不出
// 问题在哪;逐行还能在中间写 `# 注释`。

/**
 * 「键: 值」逐行 → 对象。
 *
 * 容错的边界(每一条都是"用户会因此少踩一次坑"的选择):
 * - 空行与 `#` 开头的注释行跳过 —— 让人能在中间写注释;
 * - 没有冒号 / 键为空的行**报错并指出行号** —— 静默跳过等于让用户以为头已生效,
 *   而头没生效的后果是网关限速或拒绝,现场极难定位(最坏的结局);
 * - 键与值两侧空白裁掉,值**内部**的空白保留(header 值可能有意带空格)。
 *
 * 值为空串是合法的:服务端会把它当作「删掉这个键」(与文件里的合并语义一致)。
 */
export function parseHeaderLines(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  const lines = (text ?? '').split('\n')
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i].trim()
    if (!raw || raw.startsWith('#')) continue
    const idx = raw.indexOf(':')
    if (idx <= 0) {
      throw new Error(`自定义请求头第 ${i + 1} 行格式不对(应为「键: 值」):${raw}`)
    }
    const k = raw.slice(0, idx).trim()
    const v = raw.slice(idx + 1).trim()
    if (!k) throw new Error(`自定义请求头第 ${i + 1} 行没有键: ${raw}`)
    out[k] = v
  }
  return out
}