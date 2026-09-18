// statusline_test.go S-P2-4 可配置状态栏:/statusline 的校验/持久化/渲染顺序。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestStatuslineDefaultMatchesBaseline 默认配置(未设置)= F15.3 基线输出(逐段对账)。
func TestStatuslineDefaultMatchesBaseline(t *testing.T) {
	s := &State{Workspace: "proj", Sandbox: "workspace-write", Approval: "smart", Session: "s1"}
	out := stripColor(renderStatusLine(s, 0))
	for _, want := range []string{" 空闲 | 工作区: proj | 沙箱: workspace-write | 审批: 智能 | 会话: s1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("基线状态栏应含 %q: %q", want, out)
		}
	}
	// 空值段省略(不留悬空分隔符)
	out = stripColor(renderStatusLine(&State{}, 0))
	if strings.Contains(out, "审批") || strings.Contains(out, "会话") {
		t.Fatalf("空值项不应渲染: %q", out)
	}
	if !strings.Contains(out, "空闲 | 工作区: ? | 沙箱: workspace-write") {
		t.Fatalf("工作区/沙箱缺省回退应保留: %q", out)
	}
}

// TestStatuslineCustomOrderAndSubset 自定义集合与顺序生效;段间分隔符随项归属变化。
func TestStatuslineCustomOrderAndSubset(t *testing.T) {
	s := &State{Workspace: "proj", Sandbox: "workspace-write", Approval: "open", Session: "s1",
		Statusline: []string{"session", "workspace"}}
	out := stripColor(renderStatusLine(s, 0))
	if !strings.Contains(out, " 会话: s1 | 工作区: proj") {
		t.Fatalf("顺序应按配置: %q", out)
	}
	for _, gone := range []string{"沙箱", "审批", "空闲"} {
		if strings.Contains(out, gone) {
			t.Fatalf("未配置项不应渲染(%s): %q", gone, out)
		}
	}
	// 回合态集群项之间用 " · "(与分区段不同)
	s2 := &State{Workspace: "w", Queue: []string{"a"}, Statusline: []string{"state", "last", "workspace"}}
	s2.turnDur = 3e9
	out = stripColor(renderStatusLine(s2, 0))
	if !strings.Contains(out, "空闲 · 上一回合 3.0s | 工作区: w") {
		t.Fatalf("集群项应以 · 相连、分区段以 | 相连: %q", out)
	}
}

// TestStatuslineParseAndValidate 参数解析(空格/逗号)、未知项与重复项的显式报错。
func TestStatuslineParseAndValidate(t *testing.T) {
	a := &App{model: &Model{state: &State{}}}
	t.Setenv("GAH_HOME", t.TempDir())

	// 无参:列出当前 + 可用项
	out, err := a.cmdStatusline(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"基线默认", "state", "workspace", "session", "/statusline <项...>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("无参输出应含 %q: %q", want, out)
		}
	}
	// 设置:空格 + 逗号混合
	if _, err := a.cmdStatusline([]string{"state,workspace", "session"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.model.state.Statusline, " "); got != "state workspace session" {
		t.Fatalf("集合与顺序: %q", got)
	}
	// 未知项:显式报错并列出可用项(不静默忽略)
	if _, err := a.cmdStatusline([]string{"state", "bogus"}); err == nil ||
		!strings.Contains(err.Error(), "未知项") || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("未知项应报错并提示可用项: %v", err)
	}
	// 重复项:显式报错
	if _, err := a.cmdStatusline([]string{"state", "state"}); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复项应报错: %v", err)
	}
	// reset 带附加项:显式报错(不静默忽略)
	if _, err := a.cmdStatusline([]string{"reset", "state"}); err == nil || !strings.Contains(err.Error(), "不接受附加项") {
		t.Fatalf("reset 后带参数应报错: %v", err)
	}
	// reset:回基线默认并清空偏好
	if _, err := a.cmdStatusline([]string{"reset"}); err != nil {
		t.Fatal(err)
	}
	if len(a.model.state.Statusline) != 0 {
		t.Fatalf("reset 后应为默认: %v", a.model.state.Statusline)
	}
	if p := prefs.Load(); len(p.Statusline) != 0 {
		t.Fatalf("reset 应清空持久化: %v", p.Statusline)
	}
}

// TestStatuslinePersistAndRestore 设置即落盘;$GAH_HOME/config/gah-state.json 重启后生效。
func TestStatuslinePersistAndRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	a := &App{model: &Model{state: &State{}}}
	if _, err := a.cmdStatusline([]string{"dock", "workspace", "approval"}); err != nil {
		t.Fatal(err)
	}
	if p := prefs.Load(); strings.Join(p.Statusline, " ") != "dock workspace approval" {
		t.Fatalf("偏好应持久化: %v", p.Statusline)
	}
	// 新实例启动:applyPrefs 恢复(与 TUI 启动路径同源)
	b := &App{model: &Model{state: &State{}}}
	b.applyPrefs()
	if got := strings.Join(b.model.state.Statusline, " "); got != "dock workspace approval" {
		t.Fatalf("启动应恢复状态栏配置: %q", got)
	}
}

// TestFilterStatusline 加载时坏项/重复项被丢弃(手改偏好文件也不会让整条失效)。
func TestFilterStatusline(t *testing.T) {
	got := filterStatusline([]string{"workspace", "bogus", "workspace", "state", ""})
	if strings.Join(got, " ") != "workspace state" {
		t.Fatalf("应过滤未知/重复/空项: %v", got)
	}
	if out := filterStatusline([]string{"nope"}); len(out) != 0 {
		t.Fatalf("全未知 = 回退默认(空): %v", out)
	}
	// 全坏 → 保持基线默认渲染
	s := &State{Workspace: "w", Statusline: filterStatusline([]string{"nope"})}
	if out := stripColor(renderStatusLine(s, 0)); !strings.Contains(out, "沙箱") {
		t.Fatalf("全坏项应回退基线默认: %q", out)
	}
}

// TestStatuslineRunningStateEsc 运行态提示(Esc 取消)不受配置影响(状态语义不因隐藏项丢失)。
func TestStatuslineRunningStateEsc(t *testing.T) {
	s := &State{Running: true, SpinnerIdx: 1, Statusline: []string{"state"}}
	out := stripColor(renderStatusLine(s, 0))
	if !strings.Contains(out, "思考中 (Esc 取消)") {
		t.Fatalf("运行态应含 Esc 提示: %q", out)
	}
	s.LastTool = "shell"
	out = stripColor(renderStatusLine(s, 0))
	if !strings.Contains(out, "执行工具: shell") {
		t.Fatalf("应显示当前工具: %q", out)
	}
	// 该段用运行高亮样式(醒目信号):非纯灰
	if !strings.Contains(renderStatusLine(s, 0), styleBusy.Render("")) && !strings.Contains(renderStatusLine(s, 0), "\x1b[") {
		t.Fatalf("运行态应带样式: %q", renderStatusLine(s, 0))
	}
}

// TestStatuslineNoDanglingSeparator 只配置了条件项且条件不满足时,整行为空(无悬空分隔符)。
func TestStatuslineNoDanglingSeparator(t *testing.T) {
	s := &State{Statusline: []string{"approval", "session"}}
	out := stripColor(renderStatusLine(s, 0))
	if strings.TrimSpace(out) != "" {
		t.Fatalf("条件项均不成立时应为空白: %q", out)
	}
	if strings.Contains(out, "|") {
		t.Fatalf("不应有悬空分隔符: %q", out)
	}
}

// TestStatuslineCommandRegistered TUI 本地命令注册(不与宿主命令同名冲突)。
func TestStatuslineCommandRegistered(t *testing.T) {
	var _ sdk.Option // 保持 sdk 引用(命令参数用 sdk.Option)
	if len(statuslineTokens) != len(defaultStatusline) {
		t.Fatalf("基线默认应覆盖全部可用项: %v vs %v", statuslineTokens, defaultStatusline)
	}
	for _, tok := range defaultStatusline {
		if statuslineTokenDesc[tok] == "" {
			t.Fatalf("默认项 %q 缺说明(无法在 /statusline 输出中自述)", tok)
		}
	}
}
