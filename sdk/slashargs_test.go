package sdk

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a b", []string{"a", "b"}},
		{"a   b", []string{"a", "b"}},
		{`provider set https://x "sk a b"`, []string{"provider", "set", "https://x", "sk a b"}},
		{`x 'a b' c`, []string{"x", "a b", "c"}},
		{`x "" y`, []string{"x", "", "y"}},         // 空引号 = 显式空参数
		{`x "a\"b" y`, []string{"x", `a"b`, "y"}},  // 引号内转义
		{`x a\ b`, []string{"x", "a b"}},           // 裸转义空格
		{`x "unclosed`, []string{"x", "unclosed"}}, // 未闭合引号容错
		{`x trail\`, []string{"x", `trail\`}},      //
		{"x\ttab", []string{"x", "tab"}},           // tab 分隔
	}
	for _, c := range cases {
		got := SplitArgs(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitArgs(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

// TestJoinArgsRoundTrip JoinArgs 重建的命令行必须能被 SplitArgs 原样还原
// (补全/向导把参数拼回输入框后仍解析成同一组参数)。
func TestJoinArgsRoundTrip(t *testing.T) {
	cases := [][]string{
		{"provider", "set", "https://x", "sk a b"},
		{"x", "", "y"},
		{`a"b`, `c\d`, "e f"},
		{"中文 参数", "值"},
		{"trail\\"},
	}
	for _, args := range cases {
		line := JoinArgs(args)
		if got := SplitArgs(line); !reflect.DeepEqual(got, args) {
			t.Errorf("JoinArgs(%#v)=%q → SplitArgs=%#v", args, line, got)
		}
	}
}
