// 错误类型:可重试错误语义(对齐设计 §11 错误处理矩阵)。

package sdk

// RetryableError 可重试错误(网络断流/5xx/瞬态故障);host-llm 对其实施指数退避重试。
// 4xx(auth/quota)等不可重试错误保持普通 error。
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return "retryable: " + e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }
