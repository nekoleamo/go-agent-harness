// 错误类型:可重试错误语义(对齐设计 §11 错误处理矩阵)+ 上下文超窗判定。

package sdk

import (
	"errors"
	"strings"
)

// RetryableError 可重试错误(网络断流/5xx/瞬态故障);host-llm 对其实施指数退避重试。
// 4xx(auth/quota)等不可重试错误保持普通 error。
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return "retryable: " + e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// HTTPStatusHint 模型端点返回非 200 时的中文提示(无通用提示返回空串)。
//
// 用途:非编程用户看不到 JSON 里的 error.message,直接展示端点原文等于没讲。
// 提示句与原始响应体**同时**保留(前者给人看,后者给排查用)。
// 两个适配器(openai 兼容 / anthropic 兼容)共用一份,避免同一句话两处漂移。
// overflowStrong 超窗错误的**强特征**(小写比对):单独出现即可判定 —— 这些措辞专指
// “输入/上下文”装不下,不含“输出上限”语义。覆盖 OpenAI 系(`context_length_exceeded`、
// `maximum context length is N tokens`)与 Anthropic 系(`prompt is too long: N tokens
// > M maximum`)的实际错误串。
var overflowStrong = []string{
	"context_length_exceeded",
	"context length",
	"context window",
	"context overflow",
	"maximum context",
	"max context",
	"input is too long",
	"prompt is too long",
	"prompt is too large",
	"reduce the length",
	"input length",
	"上下文长度",
	"上下文超",
	"输入过长",
	"提示词过长",
}

// overflowWeak 超窗错误的**弱特征**:必须与作用域词(overflowScope)同现才判定 ——
// 它们单独出现可能说的是**输出**上限(`max_tokens` 过大),那种情况压缩历史帮不上忙,
// 误判代价只是白压一次(压缩可逆,且只重试一次)。
var overflowWeak = []string{
	"too many tokens",
	"too long",
	"too large",
	"exceeds the maximum",
	"exceed the maximum",
	"maximum number of tokens",
	"token limit",
	"length exceeds",
}

// overflowScope 作用域词:与弱特征同现才说明“装不下的是输入,不是输出”。
// 刻意**不收** token/tokens 与 message:输出参数上限(`max_tokens is too large`)也带这些词,
// 而适配层的错误串包装里永远有 `"message"` 键 —— 收进来就会把两者都误判成超窗。
// 而真正的输入超窗措辞总会同时带 context/prompt/input 之一。
var overflowScope = []string{
	"context", "prompt", "input", "conversation",
	"上下文", "输入", "提示", "历史",
}

// IsContextOverflowError 报告 err 链上是否存在“上下文/输入超窗”类错误。
//
// 唯一裁决点:供应商措辞各不相同,判定必须只有一份(provider 适配层与压缩策略层都不许
// 自己抄一套)。分层依据:
//  1. 强特征直接命中(专指输入装不下);
//  2. 弱特征 + 作用域词同现(排除“输出上限”类);
//  3. HTTP 413:模型端点上等价于“请求体装不下”,误判代价仅是白压一次。
//
// 其它一律 false —— 尤其 `context.Canceled` / `context.DeadlineExceeded` 绝不能触发压缩
// (用户取消/超时与上下文无关,压了反而丢掉上下文)。
func IsContextOverflowError(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if overflowInText(strings.ToLower(e.Error())) {
			return true
		}
	}
	return false
}

// overflowInText 单条错误文本的超窗判定(纯函数,表驱动可测)。
func overflowInText(s string) bool {
	for _, m := range overflowStrong {
		if strings.Contains(s, m) {
			return true
		}
	}
	if strings.Contains(s, "http 413") {
		return true
	}
	weak := false
	for _, m := range overflowWeak {
		if strings.Contains(s, m) {
			weak = true
			break
		}
	}
	if !weak {
		return false
	}
	for _, sc := range overflowScope {
		if strings.Contains(s, sc) {
			return true
		}
	}
	return false
}

func HTTPStatusHint(code int) string {
	switch code {
	case 400:
		return "请求被端点拒绝:模型名可能不存在,或参数不被该端点支持"
	case 401:
		return "Key 无效或已过期:检查设置里 Provider 的 api_key 是否复制完整"
	case 402:
		return "余额不足:该端点要求先充值"
	case 403:
		return "Key 无权限:可能没开通这个模型,或账号受地区限制"
	case 404:
		return "端点路径不存在:多数服务要求 base_url 以 /v1 结尾"
	case 408:
		return "端点读取请求超时"
	case 413:
		return "请求体过大:上下文或附件超出端点限制"
	case 422:
		return "参数不被端点接受:检查模型名与思考等级"
	case 429:
		return "请求过频或额度用尽:稍后重试,或检查套餐额度"
	case 500, 502, 503, 504, 529:
		return "端点内部故障(已自动重试)"
	case 501:
		return "端点不支持该接口(如 /chat/completions)"
	}
	return ""
}
