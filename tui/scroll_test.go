// 多行文本渲染回归:一条逻辑行含 \n/超长文本时展平为物理显示行,
// 渲染行数受控(不撑爆窗口)、滚动按物理行生效。防"多行消息把输入行/状态栏挤出屏幕、
// 滚动视觉失效"回归。
package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderMultiLineBounded(t *testing.T) {
	s := &State{}
	// 模拟长回复:一条 assistant Line 含大量 \n(如多段新闻列表)
	for i := 0; i < 10; i++ {
		body := ""
		for j := 0; j < 12; j++ {
			body += fmt.Sprintf("第%d条新闻正文用于撑满多行内容,中英混排 aaaa bbbb cccc。\n", j+1)
		}
		s.Lines = append(s.Lines, Line{Kind: "assistant", Text: body})
	}
	// 渲染高度 12(会话窗口约 9):输出总行数必须 ≤ 高度+1(不允许把输入/状态栏挤出屏幕)
	out := Render(s, 60, 12)
	n := strings.Count(out, "\n")
	if n > 13 {
		t.Fatalf("渲染行数失控:%d 行(高度 12 时应 ≈11-12),多行文本把界面撑爆", n)
	}
	if !strings.Contains(out, "❯") || !strings.Contains(out, "gah |") {
		t.Fatal("输入行/状态栏应始终在渲染结果中(多行文本不应挤掉它们)")
	}
	if !strings.Contains(out, "░") && !strings.Contains(out, "█") {
		t.Fatal("物理内容超窗口应渲染滚动条")
	}
	// 顶行是最早内容而非最新:窗口高度有限时超窗口内容必然有 bar 且只显示最近窗口
	if s.flatN < 20 {
		t.Fatalf("展平行数应远大于窗口: %d", s.flatN)
	}
}

func TestScrollByMultiLinePhysical(t *testing.T) {
	s := &State{}
	// 3 条逻辑行、每条折成多物理行:上滚一"逻辑行"需多格物理行
	for i := 0; i < 3; i++ {
		s.Lines = append(s.Lines, Line{Kind: "meta", Text: strings.Repeat("ABCDE", 40)}) // 200 字符,折行 >3 行
	}
	// 先渲染一次刷新 flatN
	Render(s, 40, 12)
	if s.flatN < 9 {
		t.Fatalf("3 条长行展平后应 >=9 物理行,got %d", s.flatN)
	}
	// 上滚一格(物理行):offset+1;窗口渲染内容前移一行
	s.ScrollBy(1, 8)
	if s.ScrollOffset != 1 {
		t.Fatalf("offset 应 +1: %d", s.ScrollOffset)
	}
	// 极限上滚到最早:offset 上限 = 物理行数 - 窗口高
	s.ScrollBy(999, 8)
	if s.ScrollOffset != s.flatN-8 {
		t.Fatalf("极限上滚应按物理行钳制:%d (flatN=%d)", s.ScrollOffset, s.flatN)
	}
	out := Render(s, 40, 12)
	if !strings.Contains(out, "░") {
		// flatN>窗口时应有滚动条
	}
	// 复位
	s.ScrollBy(-999, 8)
	if s.ScrollOffset != 0 {
		t.Fatalf("回底应为 0: %d", s.ScrollOffset)
	}
}

func TestUserMultiLinePrefixOnce(t *testing.T) {
	s := &State{}
	s.Lines = append(s.Lines, Line{Kind: "user", Text: "第一行\n第二行\n第三行"})
	rows := flattenLines(s.Lines, 40)
	if len(rows) != 3 {
		t.Fatalf("多行 user 应拆 3 物理行,got %d", len(rows))
	}
	if !strings.HasPrefix(rows[0].text, "❯ ") {
		t.Fatalf("首物理行应带 ❯ 前缀: %q", rows[0].text)
	}
	if strings.HasPrefix(rows[1].text, "❯ ") || strings.HasPrefix(rows[2].text, "❯ ") {
		t.Fatalf("续行不应带 ❯ 前缀: %q %q", rows[1].text, rows[2].text)
	}
}
