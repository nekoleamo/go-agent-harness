// HTTPStatusHint 单测:覆盖面按「用户看得到文案」定,未知码必须回空串(调用方据此不加括号)。
package sdk

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHTTPStatusHint(t *testing.T) {
	cases := map[int]string{
		400: "模型名",
		401: "Key 无效",
		402: "余额",
		403: "权限",
		404: "/v1",
		408: "超时",
		413: "过大",
		422: "参数",
		429: "过频",
		500: "故障",
		502: "故障",
		503: "故障",
		504: "故障",
		529: "故障",
		501: "不支持",
	}
	for code, want := range cases {
		got := HTTPStatusHint(code)
		if got == "" || !strings.Contains(got, want) {
			t.Fatalf("HTTP %d → %q(应含 %q)", code, got, want)
		}
	}
	// 未列出/非错误码:空串(调用方不得输出空括号)
	for _, code := range []int{0, 200, 204, 301, 418, 999} {
		if got := HTTPStatusHint(code); got != "" {
			t.Fatalf("HTTP %d 不应有提示,得 %q", code, got)
		}
	}
}

// TestIsContextOverflowError 超窗判定:跨供应商的实际错误串要认出来,其它错误(含用户取消/
// 超时/普通 400)一律不认 —— 误判会让用户白丢一次上下文(压缩不可逆地改写模型可见的历史)。
func TestIsContextOverflowError(t *testing.T) {
	overflowCases := []string{
		// OpenAI 系
		`llm-openai: HTTP 400: {"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 8192 tokens"}}`,
		`llm-openai: HTTP 400: {"error":{"message":"This model's maximum context length is 65536 tokens. However, your messages resulted in 90000 tokens."}}`,
		// Anthropic 系
		`llm-anthropic: HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213870 tokens > 200000 maximum"}}`,
		// 只有弱特征 + 作用域词(某些中转/自建端点)
		`request failed: input token count exceeds the maximum number of tokens allowed`,
		// HTTP 413:模型端点上等价于"装不下"
		`llm-openai: HTTP 413: 请求体过大:上下文或附件超出端点限制`,
		// 中文端点
		`请求失败:输入过长,已超出模型上下文长度`,
	}
	for _, msg := range overflowCases {
		if !IsContextOverflowError(errors.New(msg)) {
			t.Fatalf("应判定为超窗: %s", msg)
		}
	}
	// 包装链上任意一层命中都要认(host-agent-loop 包 sdk.LLMError;适配层可能再包一层)
	wrapped := fmt.Errorf("agent: %w", &LLMError{Model: "m", Err: errors.New(overflowCases[0])})
	if !IsContextOverflowError(wrapped) {
		t.Fatal("Unwrap 链上的超窗必须被识别")
	}
	notOverflow := []string{
		"llm 流中断",
		"context canceled",
		"context deadline exceeded",
		`llm-openai: HTTP 401: {"error":{"message":"Incorrect API key provided"}}`,
		`llm-openai: HTTP 429: rate limit exceeded, please retry later`,
		// 输出上限类:压缩历史帮不上忙,不该白压一次
		`llm-openai: HTTP 400: {"error":{"message":"max_tokens is too large: must be <= 8192"}}`,
		`llm-anthropic: HTTP 400: {"error":{"message":"temperature exceeds the maximum allowed value"}}`,
	}
	for _, msg := range notOverflow {
		if IsContextOverflowError(errors.New(msg)) {
			t.Fatalf("不应判定为超窗: %s", msg)
		}
	}
	if IsContextOverflowError(nil) {
		t.Fatal("nil 错误不得判定为超窗")
	}
}
