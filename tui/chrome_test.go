// S2.2 装饰层(chrome)组件单测:输入行光标定位/退出武装提示/状态栏字段/提示行截断。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stripColor 去掉 ANSI SGR(仅保留可见文本断言)。
func stripColor(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			// 跳过 CSI:0x1b '[' 参数…终止字符(0x40–0x7E)
			i += 2
			for i < len(rs) && !(rs[i] >= '@' && rs[i] <= '~') {
				i++
			}
			i++ // 跳过终止字符
			continue
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

// TestInputLineCursor 输入行:块光标按 Cursor 位置(前后文本 + 光标块)。
func TestInputLineCursor(t *testing.T) {
	s := &State{Input: "abc", Cursor: 1}
	out := renderInputLine(s, 80)
	clean := stripColor(out)
	if !strings.Contains(clean, "❯ a") || !strings.Contains(clean, "bc") {
		t.Fatalf("光标前/后文本应就位: %q", clean)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("输入行应含样式(光标/前缀)")
	}
	// 光标越界钳制
	s2 := &State{Input: "hi", Cursor: 99}
	if !strings.Contains(stripColor(renderInputLine(s2, 80)), "hi") {
		t.Fatal("光标越界应正常渲染全文")
	}
}

// TestInputQuitArmed 双按退出武装:提示高亮出现;未武装无。
func TestInputQuitArmed(t *testing.T) {
	s := &State{QuitArmed: true}
	if !strings.Contains(stripColor(renderInputLine(s, 80)), "再按一次 Ctrl+C") {
		t.Fatal("武装提示应显示")
	}
	s2 := &State{}
	if strings.Contains(stripColor(renderInputLine(s2, 80)), "再按一次") {
		t.Fatal("未武装不应提示")
	}
}

// TestStatusLineFields 状态栏:空闲/运行/模型/沙箱/会话/上下文使用率。
func TestStatusLineFields(t *testing.T) {
	s := &State{Profile: "tui", Model: "deepseek", Sandbox: "workspace-write", Workspace: "proj"}
	out := stripColor(renderStatusLine(s, 60))
	for _, want := range []string{"gah", "空闲", "tui", "proj", "deepseek", "workspace-write", "上下文 -"} {
		if !strings.Contains(out, want) {
			t.Fatalf("状态栏应含 %q: %q", want, out)
		}
	}
	// 运行 + 工具名
	s2 := &State{Running: true, LastTool: "shell", SpinnerIdx: 1}
	if out := stripColor(renderStatusLine(s2, 60)); !strings.Contains(out, "执行工具: shell") || !strings.Contains(out, "Esc 取消") {
		t.Fatalf("运行态状态栏: %q", out)
	}
	// 上下文使用率(窗口已知 → 百分比)
	s3 := &State{Stats: sdk.UsageStats{Requests: 1, PromptTokens: 1024, Window: 4096}}
	if out := stripColor(renderStatusLine(s3, 60)); !strings.Contains(out, "上下文 1.0K/4.0K (25%)") {
		t.Fatalf("窗口已知应显百分比: %q", out)
	}
	// 会话显示
	s4 := &State{Session: "s1"}
	if out := stripColor(renderStatusLine(s4, 60)); !strings.Contains(out, "会话: s1") {
		t.Fatalf("会话 id 应显示: %q", out)
	}
	// 模型来源标注(同名模型跨 provider 可辨);未设置模型时不追加
	s5 := &State{Model: "deepseek-ai/DeepSeek-V3", ModelSrc: "siliconflow"}
	if out := stripColor(renderStatusLine(s5, 60)); !strings.Contains(out, "deepseek-ai/DeepSeek-V3(siliconflow)") {
		t.Fatalf("模型应带来源: %q", out)
	}
	s6 := &State{}
	if out := stripColor(renderStatusLine(s6, 60)); strings.Contains(out, "未设置(") {
		t.Fatalf("未设置模型不应追加来源: %q", out)
	}
}

// TestHintLines 提示区截断:超过 maxHintRows 截断并补 "…"。
func TestHintLines(t *testing.T) {
	items := make([]string, maxHintRows+3)
	for i := range items {
		items[i] = "sug" + string(rune('a'+i))
	}
	lines := renderHintLines(nil, items, maxHintRows)
	if len(lines) != maxHintRows+1 { // +1 = "…" 行
		t.Fatalf("应截断到 maxHintRows+… : %d", len(lines))
	}
	if !strings.Contains(stripColor(lines[len(lines)-1]), "…") {
		t.Fatalf("末行应为省略提示: %q", lines[len(lines)-1])
	}
}
