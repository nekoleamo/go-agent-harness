// 脱敏(单一事实源)的用例:两个消费方(会话概述 / 滚动摘要的模型摘要)都靠它,
// 漂移的方向是"某个凭据形态在另一处没被脱敏" ⇒ 密钥进模型上下文。
package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsCommonForms(t *testing.T) {
	cases := []string{
		"我的 key 是 sk-abcdef123456",
		"Authorization: Bearer eyJhbGciOi.J9.xx",
		"api_key=abc123def",
		"apiKey: abc123def",
		"\"app_secret\": \"abc123def\"",
		"password=abc123def",
		"token: abc123def",
	}
	for _, in := range cases {
		got := Text(in)
		if contains(got, "abc123def") || contains(got, "abcdef123456") || contains(got, "eyJhbGciOi.J9.xx") {
			t.Fatalf("凭据值未被脱敏(%q):\n%s", in, got)
		}
		if !contains(got, "[REDACTED]") {
			t.Fatalf("应有脱敏标记(%q):\n%s", in, got)
		}
	}
}

func TestTextKeepsHarmlessText(t *testing.T) {
	in := "这是一段普通的中文会话内容,没有凭据。"
	if got := Text(in); got != in {
		t.Fatalf("普通文本不应被改写:\n%s", got)
	}
}

// TestTextTerminatesOnKeyWithoutValue 键后面没有值(或是分隔符)时不能死循环。
func TestTextTerminatesOnKeyWithoutValue(t *testing.T) {
	for _, in := range []string{"api_key=", "sk-", "Bearer ", "password:"} {
		got := Text(in + in + in)
		if got == "" {
			t.Fatalf("应保留键名: %q", got)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// JSON 形态与大写形态(config dump 是最常见的泄露场面)。
// **反向验证**:把 keys 里 `api_key"` / `API_KEY=` 去掉 ⇒ 本用例红。
func TestRedactJSONAndUpperCaseKeys(t *testing.T) {
	cases := []string{
		`{"api_key": "abc123def"}`,
		`{"apiKey": "abc123def"}`,
		`OPENAI_API_KEY=abc123def`,
		`API_KEY: abc123def`,
	}
	for _, in := range cases {
		got := Text(in)
		if strings.Contains(got, "abc123def") {
			t.Fatalf("未脱敏: %q → %q", in, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("应留下占位符: %q → %q", in, got)
		}
	}
}
