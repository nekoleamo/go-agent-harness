// S2.2 会话流引擎(session.go)模块单测:分层后引擎层函数归属自洽、样式分层正确。
package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestSessionStyleLayers 样式分层:四种 kind 各自独立样式(引擎层不含 user 前景污染)。
func TestSessionStyleLayers(t *testing.T) {
	cases := map[string]string{
		"user":      "81", // 绿
		"assistant": "120",
		"tool":      "220",
		"error":     "203",
	}
	for kind, want := range cases {
		st := styleForKind(kind)
		rendered := st.Render("样本")
		if !strings.Contains(rendered, want) {
			t.Fatalf("kind=%s 样式应含色 %s: %q", kind, want, rendered)
		}
	}
	// 未知 kind → 无样式(纯文本)
	if out := styleForKind("nope").Render("x"); strings.Contains(out, "\x1b[") {
		t.Fatalf("未知 kind 不应有样式: %q", out)
	}
}

// TestSessionFlattenOwnership 引擎层 flatten 归属:多段文本/物理行 first 标记正确。
func TestSessionFlattenOwnership(t *testing.T) {
	rows := flattenLines([]Line{{Kind: "assistant", Text: "a\nb"}}, 60)
	if len(rows) != 2 || rows[0].first != true || rows[1].first {
		t.Fatalf("多段展平 first 标记: %+v", rows)
	}
	// 用户首行前缀与折行
	u := flattenLines([]Line{{Kind: "user", Text: "你好世界你好世界"}}, 8)
	if len(u) < 2 || !strings.HasPrefix(u[0].text, "❯ ") {
		t.Fatalf("用户长行应前缀+折行: %+v", u)
	}
}

// TestSessionWidthGeom 引擎层列宽几何:折行不超宽、宽度函数归属一致。
func TestSessionWidthGeom(t *testing.T) {
	segs := wrapSegment("abcdefghij", 5)
	for _, seg := range segs {
		if lipgloss.Width(seg) > 5 {
			t.Fatalf("折行段不应超宽: %q", seg)
		}
	}
	if !isWideRune('中') || isWideRune('a') {
		t.Fatal("双宽判定错误")
	}
	if runeCols('中') != 2 || runeCols('a') != 1 {
		t.Fatal("列宽计算错误")
	}
}

// TestSessionBarMetrics 引擎层滚动条度量(滚动/沉底/极限)。
func TestSessionBarMetrics(t *testing.T) {
	top, thumb := scrollMetrics(10, 3, 7)
	if top != 0 || thumb != 1 {
		t.Fatalf("极限顶部: top=%d thumb=%d", top, thumb)
	}
	top, thumb = scrollMetrics(10, 3, 0)
	if thumb != 1 || top != 2 {
		t.Fatalf("沉底: top=%d thumb=%d", top, thumb)
	}
}
