// 出站预算层单测:rune 安全分块/截断提示/gap 间隔/burst 记账。
package im

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSplitTextShort 短文本单块。
func TestSplitTextShort(t *testing.T) {
	short := "你好,gah"
	if got := SplitText(short, 2000); len(got) != 1 || got[0] != short {
		t.Fatalf("短文本应单块: %v", got)
	}
}

// TestSplitTextHardCut 无边界超长 → 硬切多块;每块 ≤limit 且均合法 UTF-8。
func TestSplitTextHardCut(t *testing.T) {
	limit := 100
	long := strings.Repeat("字", limit*3+5) // 多字节字符(硬切点必须在 rune 边界)
	chunks := SplitText(long, limit)
	if len(chunks) < 3 {
		t.Fatalf("应切成 >=3 块,got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > limit {
			t.Fatalf("块 %d 超限 %d > %d", i, n, limit)
		}
		if !utf8.ValidString(c) {
			t.Fatalf("块 %d 非法 UTF-8(切点截断多字节字符)", i)
		}
	}
	joined := strings.Join(chunks, "")
	trimmed := strings.TrimSpace(strings.ReplaceAll(long, "\n", ""))
	if strings.TrimSpace(strings.ReplaceAll(joined, "\n", "")) == "" && trimmed != "" {
		t.Fatal("拼接还原异常")
	}
}

// TestSplitTextPriority 段落空行 > 换行 > 空格 优先切(块尽量落在段落边界)。
func TestSplitTextPriority(t *testing.T) {
	limit := 100
	para := strings.Repeat("甲", limit/2) + "\n\n" + strings.Repeat("乙", limit*2)
	chunks := SplitText(para, limit)
	if len(chunks) < 2 {
		t.Fatalf("段落文本应多块,got %d", len(chunks))
	}
	// 首块应止于段落边界(含空行分隔),即不含"乙"
	if strings.Contains(chunks[0], "乙") {
		t.Fatalf("段落优先切点失效: 首块含后续段: %q", chunks[0])
	}
	// 空格边界
	spaced := strings.Repeat("中", 40) + " " + strings.Repeat("文", 40) + " " + strings.Repeat("长", 40)
	cs := SplitText(spaced, 50)
	if len(cs) != 3 || cs[0] != strings.Repeat("中", 40) || cs[2] != strings.Repeat("长", 40) {
		t.Fatalf("空格边界切分不符: %q", cs)
	}
}

// TestSplitTextBlank 纯空白不 panic、安全返回。
func TestSplitTextBlank(t *testing.T) {
	if got := SplitText(strings.Repeat(" ", 300), 100); len(got) == 0 {
		t.Fatal("空白输入应安全返回非空")
	}
	if got := SplitText("", 100); got != nil {
		t.Fatalf("空输入应返回 nil,got %v", got)
	}
}

// TestSenderTruncate 超 MaxChunks → 截断 + 追加提示(作为末条)。
func TestSenderTruncate(t *testing.T) {
	s := NewSender(&Budget{MaxChunk: 10, MaxChunks: 2, TruncHint: "HINT"})
	var got []string
	if err := s.Send(t.Context(), strings.Repeat("字", 50), func(ch string) error {
		got = append(got, ch)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != "HINT" {
		t.Fatalf("应截断为 2 块+提示: %+v", got)
	}
}

// TestSenderBurst 窗口内 burst 记账:超限条数延后到窗口滑移后发送(内容不丢)。
func TestSenderBurst(t *testing.T) {
	s := NewSender(&Budget{MaxChunk: 200, MaxChunks: 0, Burst: 2, BurstWin: 100 * time.Millisecond})
	// 4 行各 150 字(总 604 > 200 → 按行切 ~4 块),Burst=2 → 前 2 块立即,后 2 块等窗口滑移
	block := func(ch rune, n int) string { return strings.Repeat(string(ch), n) }
	text := strings.Join([]string{
		block('a', 100), block('b', 100), block('c', 100), block('d', 100),
	}, "\n")
	var got []string
	start := time.Now()
	err := s.Send(t.Context(), text, func(ch string) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("burst 限流不应丢内容: %+v", got)
	}
	if time.Since(start) < 80*time.Millisecond {
		t.Fatalf("burst 超限应延后(至少跨一个窗口),耗时过短")
	}
}

// TestBudgetDefaults 默认预算表覆盖两通道(参数引用/诊断用)。
func TestBudgetDefaults(t *testing.T) {
	if w := BudgetDefaults["wechat"]; w.MaxChunk != 2000 || w.MaxChunks != 10 {
		t.Fatalf("wechat 预算默认不符: %+v", w)
	}
	if q := BudgetDefaults["qq"]; q.MaxChunk != 4000 {
		t.Fatalf("qq 预算默认不符: %+v", q)
	}
}
