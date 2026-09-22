package sdk

import "testing"

// TestLooksLikeURLPath URL 形态路径判定:真机事故是工作目录里长出 https:/host/... 空目录树。
func TestLooksLikeURLPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://feinterview.poetries.top/docs/base/x", true},
		{"https:/feinterview.poetries.top/docs/base/x", true}, // filepath.Clean 折过 // 的形态
		{"http://example.com", true},
		{"HTTPS://EXAMPLE.COM/a", true},
		{"  https://example.com  ", true},
		{"file:///tmp/x.html", true},
		{"mailto:a@b.c", false}, // 不带斜杠的 scheme:构不成 host/path,不拦
		{"https:", true},        // 无主体也算 URL 形态(拦下来比造目录强)
		// 正常本地路径:一个都不能误伤
		{"/tmp/x.html", false},
		{"./docs/a.md", false},
		{"docs/a.md", false},
		{"C:/Users/x/a.txt", false},
		{"C:\\Users\\x\\a.txt", false},
		{"file.txt:backup", false},
		{"我的文档/报告.md", false},
		{"", false},
		{"httpdocs/a.md", false},
	}
	for _, c := range cases {
		if got := LooksLikeURLPath(c.in); got != c.want {
			t.Fatalf("LooksLikeURLPath(%q) = %v,期望 %v", c.in, got, c.want)
		}
	}
}
