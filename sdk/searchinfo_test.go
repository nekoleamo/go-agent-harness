// SearchEndpointHost / MaskSearchKey 的单测。
//
// 这两个函数是 /websearch「不说漏、也不说多」的边界:端点 URL 与 key 都可能夹带凭据
// (自建端点常见 `?key=…`、路径里带 token),所以「显示出来」这件事本身要有口径,
// 而不是把原始字符串往界面上一贴。
package sdk

import (
	"strings"
	"testing"
)

func TestSearchEndpointHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"https://api.anysearch.com/v1/search", "api.anysearch.com"},
		{"http://127.0.0.1:11434/v1", "127.0.0.1"},                     // 端口剥掉
		{"https://search.mycorp.cn/v1?key=secret", "search.mycorp.cn"}, // 查询串剥掉
		{"https://user:pass@gw.example.com/v1", "gw.example.com"},      // userinfo 剥掉
		{"api.exa.ai/search", "api.exa.ai"},                            // 无 scheme
	}
	for _, c := range cases {
		if got := SearchEndpointHost(c.in); got != c.want {
			t.Errorf("SearchEndpointHost(%q) = %q, want %q", c.in, got, c.want)
		}
		// 凭据绝不能出现在结果里
		if strings.Contains(SearchEndpointHost(c.in), "secret") ||
			strings.Contains(SearchEndpointHost(c.in), "pass") {
			t.Errorf("结果泄漏了凭据: %q", SearchEndpointHost(c.in))
		}
	}
}

func TestMaskSearchKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", "*****"},
		{"12345678", "********"},              // 8 位及以下整条盖掉
		{"sk-or-v1-abcdefghij", "sk-****hij"}, // 长 key 只留首 3 尾 3
	}
	for _, c := range cases {
		if got := MaskSearchKey(c.in); got != c.want {
			t.Errorf("MaskSearchKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 长 key 的中间必须被盖住,不能留下可辨认的片段
	got := MaskSearchKey("sk-or-v1-verysecretpart")
	if strings.Contains(got, "secret") || strings.Contains(got, "ver") {
		t.Fatalf("打码后仍泄漏明文片段: %q", got)
	}
}
