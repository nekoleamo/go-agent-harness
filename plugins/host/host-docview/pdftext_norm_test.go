// PDF 文本码位修正单测(依据 E-D 真实语料发现的 CUPS PDF 实例)。
package hostdocview

import (
	"strings"
	"testing"
)

func TestNormalizeCJKRadicals(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"真实实例(康熙部首 SCRIPT→文)", "中\u2F42测试", "中文测试"},
		{"部首一(U+2F00)→一", "\u2F00", "一"},
		{"无部首原样", "中文 ABC 123", "中文 ABC 123"},
		{"空串", "", ""},
		{"无兼容分解的部首补充保留", "\u2E80", "\u2E80"},
		{"混排多部首", "a\u2F42b\u2F00c", "a文b一c"},
	}
	for _, c := range cases {
		if got := normalizeCJKRadicals(c.in); got != c.want {
			t.Fatalf("%s: normalizeCJKRadicals(%q)=%q want %q", c.name, c.in, got, c.want)
		}
	}
	// 关键副作用护栏:全角标点绝不能被改(全局 NFKC 会改成 ASCII)
	const punct = "，。；：（）！？"
	if got := normalizeCJKRadicals(punct); got != punct {
		t.Fatalf("全角标点不应被改动: %q", got)
	}
	// 幂等
	once := normalizeCJKRadicals("中\u2F42")
	if again := normalizeCJKRadicals(once); again != once {
		t.Fatalf("应幂等: %q", again)
	}
	// 真实语料文件级验证(CUPS PDF 文本抽取应含「文」而非部首)
	if !strings.Contains("中文", normalizeCJKRadicals("中\u2F42")) {
		t.Fatal("修正后应可被常规「中文」检索命中")
	}
}
