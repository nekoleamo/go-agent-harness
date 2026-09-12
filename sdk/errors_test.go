// HTTPStatusHint 单测:覆盖面按「用户看得到文案」定,未知码必须回空串(调用方据此不加括号)。
package sdk

import (
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
