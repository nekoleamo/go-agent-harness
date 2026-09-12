// W3 首启引导:provider 预设表 + 端点探测错误翻译(纯函数,零第三方依赖,SettingsPanel 消费)。
//
// 为什么不写模型名:模型名几个月一变(改名/下线/预览版到期),硬编码在预设里必然过期,
// 用户照着预设保存反而得到「模型不存在」。预设只给 name + base_url(OpenAI 兼容端点),
// 保存后由 /api/models 实时拉取,用户从「模型」下拉里选一个,零过期风险。
//
// 预设全部是 OpenAI 兼容端点(gah 的通用适配器即 OpenAI 协议);Anthropic 原生协议不在列。

export interface ProviderPreset {
  name: string // 写入 provider 的标识(域短名)
  label: string // 按钮文案
  base_url: string // OpenAI 兼容端点(不带尾斜杠)
  note: string // 一句说明(来源/是否本地)
  key_optional?: boolean // 本地端点(Ollama)不需要 Key
}

export const PROVIDER_PRESETS: ProviderPreset[] = [
  {
    name: 'deepseek',
    label: 'DeepSeek',
    base_url: 'https://api.deepseek.com/v1',
    note: '国内直连,价格低,推荐先用它',
  },
  {
    name: 'kimi',
    label: 'Kimi',
    base_url: 'https://api.moonshot.cn/v1',
    note: '月之暗面,长上下文',
  },
  {
    name: 'glm',
    label: '智谱 GLM',
    base_url: 'https://open.bigmodel.cn/api/paas/v4',
    note: '国内直连',
  },
  {
    name: 'qwen',
    label: '通义千问',
    base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
    note: '阿里云百炼的兼容模式端点',
  },
  {
    name: 'siliconflow',
    label: '硅基流动',
    base_url: 'https://api.siliconflow.cn/v1',
    note: '聚合多家开源模型',
  },
  {
    name: 'openrouter',
    label: 'OpenRouter',
    base_url: 'https://openrouter.ai/api/v1',
    note: '一个 Key 用多家模型',
  },
  {
    name: 'openai',
    label: 'OpenAI',
    base_url: 'https://api.openai.com/v1',
    note: '需要能直连的网络环境',
  },
  {
    name: 'ollama',
    label: 'Ollama(本地)',
    base_url: 'http://localhost:11434/v1',
    note: '本机跑模型,不需要 Key',
    key_optional: true,
  },
]

/** presetByName 按 name 取预设(未知返回 undefined)。 */
export function presetByName(name: string): ProviderPreset | undefined {
  return PROVIDER_PRESETS.find((p) => p.name === name)
}

// PROBE_RULES 探测错误 → 人话提示(顺序敏感:先匹配具体状态码,再匹配网络层字样)。
const PROBE_RULES: Array<{ re: RegExp; text: string }> = [
  {
    re: /401|unauthorized|invalid[_ -]?api[_ -]?key|incorrect api key|authentication/i,
    text: 'Key 被拒绝(401):检查是否复制完整、是否已过期',
  },
  {
    re: /403|forbidden|permission denied|insufficient/i,
    text: 'Key 无权限(403):该 Key 可能未开通这个模型,或受地区限制',
  },
  {
    re: /404|not found/i,
    text: '路径不对(404):多数端点要求 base_url 以 /v1 结尾,检查是否填错',
  },
  {
    re: /429|rate ?limit|too many requests|quota|balance/i,
    text: '被限流或余额不足(429):稍后重试或先充值',
  },
  {
    re: /no such host|server misbehaving|dial tcp: lookup|dns/i,
    text: '域名解析失败:检查 base_url 拼写与本机网络、DNS',
  },
  {
    re: /connection refused/i,
    text: '连接被拒绝:本地端点没启动?(Ollama 需要先运行 ollama serve)',
  },
  {
    re: /connection reset|broken pipe|unexpected eof/i,
    text: '连接被中断:网络或代理不稳,也可能是该端点不支持 /models 接口',
  },
  {
    re: /timeout|timed out|deadline exceeded|i\/o timeout/i,
    text: '连接超时:网络不可达,或需要配置代理',
  },
  {
    re: /x509|certificate|tls|https proxy/i,
    text: 'TLS 证书校验失败:代理拦截或自签证书',
  },
  {
    re: /unsupported protocol scheme|missing protocol|invalid url/i,
    text: 'base_url 格式不对:必须以 http:// 或 https:// 开头',
  },
]

/** explainProbeError 把端点返回的原始错误翻译成一句中文(认不出时给通用提示)。 */
export function explainProbeError(raw?: string | null): string {
  const s = (raw ?? '').trim()
  if (!s) return '端点没有返回模型列表:检查 base_url 与 Key'
  for (const r of PROBE_RULES) {
    if (r.re.test(s)) return r.text
  }
  return '端点不可达:检查 base_url、Key 与网络(下方是原始报错)'
}
