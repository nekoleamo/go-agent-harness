// 自动摘要标记(HasAutoSummary)单测:自动压缩置位、手动 /compact 不置位。
//
// 为什么这条要单测:标记的**语义**决定了界面会不会说假话 ——
// 手动压缩是用户自己按的,标成"自动"会让人误以为模型那边出了问题;
// 而自动压缩(预算触发/溢出兜底)必须标,否则"模型忘了前面说过什么"无从解释。
package sessionlog

import (
	"testing"

	tokencompress "github.com/nekoleamo/go-agent-harness/plugins/host/token-compress"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestHasAutoSummaryFalseBeforeCompression 还没压缩过 ⇒ 不标。
func TestHasAutoSummaryFalseBeforeCompression(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 3; i++ {
		appendTurn(l, i)
	}
	l.RegisterCompressor(200, &tokencompress.Engine{})
	if l.HasAutoSummary() {
		t.Fatal("未发生自动压缩时不应标记")
	}
}

// TestHasAutoSummaryTrueAfterAutoFold 预算触发的自动折叠 ⇒ 标记。
func TestHasAutoSummaryTrueAfterAutoFold(t *testing.T) {
	l := newLog("")
	l.RegisterCompressor(200, &tokencompress.Engine{})
	for i := 1; i <= 8; i++ {
		appendTurn(l, i)
		l.DeriveMessages() // 触发投影 → 超预算 → 自动折叠
	}
	if !l.HasAutoSummary() {
		t.Fatal("自动压缩过后应标记(模型看到的已经不是原话)")
	}
	// 标记必须与投影一致:投影里确实有摘要 system 消息
	msgs := l.DeriveMessages()
	if len(msgs) == 0 || msgs[0].Role != sdk.RoleSystem {
		t.Fatalf("投影首条应是摘要 system 消息: %+v", msgs)
	}
}

// TestHasAutoSummaryManualCompactDoesNotMark 手动 /compact **不**标自动。
func TestHasAutoSummaryManualCompactDoesNotMark(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 6; i++ {
		appendTurn(l, i)
	}
	l.RegisterCompressor(200, &tokencompress.Engine{})
	if _, _, err := l.Compact("手动压缩"); err != nil {
		t.Fatalf("Compact 应成功: %v", err)
	}
	if l.HasAutoSummary() {
		t.Fatal("手动 /compact 不应标成自动摘要(那是用户自己按的)")
	}
}
