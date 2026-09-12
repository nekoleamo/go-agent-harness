// 错误类型:可重试错误语义(对齐设计 §11 错误处理矩阵)。

package sdk

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
