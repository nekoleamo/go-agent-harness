// /provider 命令辅助单测:凭据打码展示。
package tui

import "testing"

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "(未设置)"},
		{"abc", "***"},
		{"abcdef", "***"},
		{"sk-abcdef1234", "***1234"},
	}
	for _, c := range cases {
		if got := maskKey(c.in); got != c.want {
			t.Fatalf("maskKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
