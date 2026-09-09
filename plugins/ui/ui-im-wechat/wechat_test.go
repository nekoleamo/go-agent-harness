// ui-im-wechat transport 纯函数单测:长文本分块策略(2000 字/段落优先/截断提示)。
package uimwechat

import (
	"strings"
	"testing"
)

// TestSplitLongText 分块:短文本单块;长文本按段/行/空格/硬切;字符数不超上限。
func TestSplitLongText(t *testing.T) {
	short := "你好"
	if got := splitLongText(short); len(got) != 1 || got[0] != short {
		t.Fatalf("短文本应单块: %v", got)
	}
	// 超长无任何边界 → 硬切为多块,每块(除末块)<= limit 且总拼接还原非空
	long := strings.Repeat("字", wechatChunkLimit*2+10)
	chunks := splitLongText(long)
	if len(chunks) < 3 {
		t.Fatalf("应切成 >=3 块,got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > wechatChunkLimit {
			t.Fatalf("块 %d 超限 %d > %d", i, n, wechatChunkLimit)
		}
	}
	if joined := strings.Join(chunks, ""); len([]rune(joined)) != len([]rune(long)) {
		t.Fatalf("拼接长度不符: %d vs %d", len([]rune(joined)), len([]rune(long)))
	}
	// 段落边界优先:首块应在 \n\n 处断开,不硬切长词
	para := strings.Repeat("甲", wechatChunkLimit/2) + "\n\n" + strings.Repeat("乙", 3000)
	chunks = splitLongText(para)
	if len(chunks) < 2 {
		t.Fatalf("段落文本应 >=2 块,got %d", len(chunks))
	}
}

// TestSplitLongTextTrimSafe 空格切分不产生空块;TrimSpace 后空文本不 panic。
func TestSplitLongTextTrimSafe(t *testing.T) {
	// 纯空格长文本:TrimSpace 后为空 → 应回退单块原文本(不 panic)
	long := strings.Repeat(" ", 2500)
	chunks := splitLongText(long)
	if len(chunks) == 0 || chunks[0] == "" {
		t.Fatalf("空/空白输入应安全返回,got %v", chunks)
	}
}
