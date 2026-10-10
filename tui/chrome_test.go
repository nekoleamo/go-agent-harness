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

// TestStatusLineFields M15 状态栏(pi 式精简):空闲/运行/工作区/沙箱/审批/会话;
// 模型/思维/上下文指标已移输入行右侧(见 TestInputLineRight)。
func TestStatusLineFields(t *testing.T) {
	s := &State{Sandbox: "workspace-write", Workspace: "proj", Session: "s1", Approval: "smart"}
	out := stripColor(renderStatusLine(s, 60))
	for _, want := range []string{"空闲", "proj", "workspace-write", "审批: 智能", "会话: s1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("状态栏应含 %q: %q", want, out)
		}
	}
	// M15:模型/上下文不在状态栏(末行指标行);状态栏不以 gah 开头(F15.3 已去)
	if strings.HasPrefix(stripColor(out), "gah") {
		t.Fatalf("状态栏不应以 gah 开头: %q", out)
	}
	for _, absent := range []string{"模型:", "上下文"} {
		if strings.Contains(out, absent) {
			t.Fatalf("状态栏不应含 %q(已移输入行右侧): %q", absent, out)
		}
	}
	// 运行 + 工具名
	s2 := &State{Running: true, LastTool: "shell", SpinnerIdx: 1}
	if out := stripColor(renderStatusLine(s2, 60)); !strings.Contains(out, "执行工具: shell") || !strings.Contains(out, "Esc 取消") {
		t.Fatalf("运行态状态栏: %q", out)
	}
	// 主会话(空 id)不显示会话段;审批档为空同样省略
	s7 := &State{}
	if out := stripColor(renderStatusLine(s7, 60)); strings.Contains(out, "会话:") || strings.Contains(out, "审批:") {
		t.Fatalf("主会话不应显示会话/审批段: %q", out)
	}
	// 审批档中文映射(open→开放, strict→严格)
	s8 := &State{Approval: "open"}
	if out := stripColor(renderStatusLine(s8, 60)); !strings.Contains(out, "审批: 开放") {
		t.Fatalf("open 档应显示 审批: 开放: %q", out)
	}
	s9 := &State{Approval: "strict"}
	if out := stripColor(renderStatusLine(s9, 60)); !strings.Contains(out, "审批: 严格") {
		t.Fatalf("strict 档应显示 审批: 严格: %q", out)
	}
}

// TestStatusLineQuestionsSegment 待答提问段(S-P0-2)的**确定性**判据。
//
// 为什么单测落在这里:pty 验收(tests/tui_accept_ask_test.go)读的是**屏幕格子**,
// 而这条段落的长度会随可达状态变化(1 条时无数字、2 条起带数字),窄 pty 折行 + 差分重绘
// 会把 `待答 2` 拆到两行/两处(2026-09-28 CI 实测:格子里只剩 `❓ 待答(Esc 退出作答)` 与
// 隔行的 `2(Esc 退出作答)`,字面断言在负载下假阴)——“计数怎么拼”在进程内可确定性判定,
// pty 侧只留行为链(两问都要能作答、且答完栈清空)。
func TestStatusLineQuestionsSegment(t *testing.T) {
	strip := func(s *State) string { return stripColor(renderStatusLine(s, 200)) }

	// 无待答 → 整段不出现(不是显示 0)
	if out := strip(&State{}); strings.Contains(out, "待答") {
		t.Fatalf("无待答时不应出现该段: %q", out)
	}
	// 1 条 → `❓ 待答` 且**不带数字**(与 2 条区分开)
	one := strip(&State{Questions: []PendingQ{{ID: "q1"}}})
	if !strings.Contains(one, "❓ 待答") {
		t.Fatalf("1 条待答应显示段: %q", one)
	}
	if strings.Contains(one, "待答 1") {
		t.Fatalf("1 条时按设计不显示数字: %q", one)
	}
	// 2 条 → 带数字(pty 验收原本断言的就是这一形态)
	two := strip(&State{Questions: []PendingQ{{ID: "q1"}, {ID: "q2"}}})
	if !strings.Contains(two, "❓ 待答 2") {
		t.Fatalf("2 条待答应显示 `待答 2`: %q", two)
	}
	// 作答中 → 后缀换成 Esc 退出
	if out := strip(&State{Questions: []PendingQ{{ID: "q1"}, {ID: "q2"}}, Answering: true}); !strings.Contains(out, "待答 2(Esc 退出作答)") {
		t.Fatalf("作答中应提示 Esc 退出: %q", out)
	}
	// 删到 1 条 → 数字同步消失(栈出栈后就地重绘)
	if out := strip(&State{Questions: []PendingQ{{ID: "q1"}}, Answering: true}); strings.Contains(out, "待答 1") || !strings.Contains(out, "待答(Esc 退出作答)") {
		t.Fatalf("出栈回 1 条应去掉数字: %q", out)
	}
}

// TestMetricLine F15.1:模型/思维/上下文独立指标行(固定于输入区上方,不随输入移动)。
func TestMetricLine(t *testing.T) {
	// 模型 + 来源 + 上下文使用率(分子 = 最近一次请求的 prompt)
	s := &State{Model: "deepseek", ModelSrc: "local", Stats: sdk.UsageStats{Requests: 1, LastPromptTokens: 1024, Window: 4096}}
	out := stripColor(renderMetricLine(s))
	// F15.4:前导空格与状态栏左缘对齐(模型不顶格)
	if !strings.HasPrefix(out, " 模型:") {
		t.Fatalf("指标行应与状态栏左缘对齐: %q", out)
	}
	for _, want := range []string{"模型: deepseek(local)", "上下文 1.0K/4.0K (25%)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("指标行应含 %q: %q", want, out)
		}
	}
	// 回归钉住(2026-09-26 真机):累计量再大也不能当水位 —— 多轮后曾显示成 5000K/128K。
	sBig := &State{Stats: sdk.UsageStats{Requests: 40, PromptTokens: 5_000_000, LastPromptTokens: 1024, Window: 4096}}
	if out := stripColor(renderMetricLine(sBig)); !strings.Contains(out, "上下文 1.0K/4.0K (25%)") {
		t.Fatalf("上下文应按最近一次请求显示: %q", out)
	}
	// 缓存命中率
	s2 := &State{Stats: sdk.UsageStats{Requests: 1, PromptTokens: 1024, CachedTokens: 512, Window: 4096}}
	if out := stripColor(renderMetricLine(s2)); !strings.Contains(out, "缓存 50%") {
		t.Fatalf("应显缓存命中率: %q", out)
	}
	// 缓存 > 0 但累计 prompt 为 0(全命中缓存的适配器口径):不得整数除零 panic
	s2z := &State{Stats: sdk.UsageStats{Requests: 1, PromptTokens: 0, CachedTokens: 512, LastPromptTokens: 512, Window: 4096}}
	if out := stripColor(renderMetricLine(s2z)); strings.Contains(out, "缓存") {
		t.Fatalf("prompt 为 0 时不应显缓存命中率: %q", out)
	}
	// 无请求:上下文 -
	s3 := &State{}
	if out := stripColor(renderMetricLine(s3)); !strings.Contains(out, "上下文 -") {
		t.Fatalf("无请求应显 上下文 -: %q", out)
	}
	// 输入多长都不会挤压:输入行不携带指标信息(独立行),长输入下指标行完整
	s4 := &State{Input: strings.Repeat("很长的输入", 20)}
	if out := stripColor(renderInputLine(s4, 80)); strings.Contains(out, "上下文") {
		t.Fatalf("指标不应在输入行: %q", out)
	}
	if out := stripColor(renderMetricLine(s4)); !strings.Contains(out, "上下文 -") {
		t.Fatalf("长输入下指标行应完整: %q", out)
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
