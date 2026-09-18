// shell_test.go:S-P2-4 「!」shell 直通单测。
// 重点验证三条硬约束:不绕过 ctx.tools 管线、不写会话账本(只本地回显)、失败显式提示。
package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ——— stub 工具注册表:记录调用,返回可编程结果 ———

type shellRegStub struct {
	defs    []sdk.ToolDefinition
	gotName string
	gotArgs string
	res     *sdk.ToolResult
	err     error
}

func (r *shellRegStub) Register(sdk.Tool) sdk.Disposer { return func() {} }
func (r *shellRegStub) List() []sdk.ToolDefinition     { return r.defs }
func (r *shellRegStub) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range r.defs {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}
func (r *shellRegStub) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	r.gotName, r.gotArgs = name, args
	if r.err != nil {
		return nil, r.err
	}
	if r.res == nil {
		return &sdk.ToolResult{}, nil
	}
	return r.res, nil
}

func TestIsShellPassthrough(t *testing.T) {
	cases := []struct {
		in      string
		wantCmd string
		wantOK  bool
	}{
		{"!ls -la", "ls -la", true},
		{"! ls", "ls", true},
		{"   !git status  ", "git status", true},
		{"!", "", false},    // 空命令不执行
		{"!!x", "!x", true}, // 第二个 ! 属命令内容
		{"ls", "", false},
		{"/jobs", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		cmd, ok := isShellPassthrough(c.in)
		if cmd != c.wantCmd || ok != c.wantOK {
			t.Errorf("isShellPassthrough(%q) = (%q,%v), want (%q,%v)", c.in, cmd, ok, c.wantCmd, c.wantOK)
		}
	}
}

func TestShellOutput(t *testing.T) {
	cases := []struct {
		name string
		res  *sdk.ToolResult
		want string
	}{
		{"nil", nil, ""},
		{"输出", &sdk.ToolResult{Content: `{"output":"hi\n"}`}, "hi\n"},
		{"超时标记", &sdk.ToolResult{Content: `{"output":"part","timeout":true}`}, "part\n(命令超时:已终止并返回已捕获输出)"},
		{"结构化错误", &sdk.ToolResult{Content: `{"error":"shell: 缺少 command 参数"}`}, "ERROR: shell: 缺少 command 参数"},
		{"裸内容", &sdk.ToolResult{Content: "plain"}, "plain"},
		{"仅 Error 字段", &sdk.ToolResult{Error: "boom"}, "ERROR: boom"},
		{"空结果", &sdk.ToolResult{}, ""},
	}
	for _, c := range cases {
		if got := shellOutput(c.res); got != c.want {
			t.Errorf("%s: shellOutput = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestClipTail(t *testing.T) {
	if got := clipTail("abc", 10); got != "abc" {
		t.Errorf("未超限应原样: %q", got)
	}
	got := clipTail("一二三四五\n", 3)
	if !strings.HasPrefix(got, "…(前 2 字符已省略)\n") || !strings.HasSuffix(got, "三四五") {
		t.Errorf("尾部截断错误: %q", got)
	}
}

func TestAppendShellResult(t *testing.T) {
	m := &Model{state: &State{Running: true, LastTool: "shell"}}
	m.appendShellResult(shellDoneMsg{cmd: "ls", out: "a\nb\n"})
	if len(m.state.Lines) != 2 {
		t.Fatalf("应追加命令行 + 输出行: %+v", m.state.Lines)
	}
	if m.state.Lines[0].Kind != "user" || m.state.Lines[0].Text != "! ls" {
		t.Errorf("首行应为命令回显: %+v", m.state.Lines[0])
	}
	if m.state.Lines[1].Kind != "tool" || !strings.HasPrefix(m.state.Lines[1].Text, "✓") {
		t.Errorf("次行应为成功输出: %+v", m.state.Lines[1])
	}
	if m.state.Running || m.state.LastTool != "" {
		t.Errorf("结束后应回空闲态: running=%v tool=%q", m.state.Running, m.state.LastTool)
	}
	// 无输出 / 失败 两种显式态
	m.appendShellResult(shellDoneMsg{cmd: "true", out: "  \n"})
	if !strings.Contains(m.state.Lines[len(m.state.Lines)-1].Text, "(无输出)") {
		t.Errorf("无输出应显式标注: %+v", m.state.Lines[len(m.state.Lines)-1])
	}
	m.appendShellResult(shellDoneMsg{cmd: "x", err: errors.New("boom")})
	last := m.state.Lines[len(m.state.Lines)-1]
	if last.Kind != "error" || !strings.Contains(last.Text, "boom") {
		t.Errorf("失败应为 error 行且带原因: %+v", last)
	}
}

// TestShellRegistryErrors:未装配 ctx.tools / 缺 shell 工具 → 显式错误(不静默降级)。
func TestShellRegistryErrors(t *testing.T) {
	a := newTestApp(&stubCtx{svc: map[string]any{}})
	if _, err := a.shellRegistry(); err == nil || !strings.Contains(err.Error(), "ctx.tools 未装配") {
		t.Fatalf("未装配应显式报错: %v", err)
	}
	reg := &shellRegStub{defs: []sdk.ToolDefinition{{Name: "read_file"}}}
	b := newTestApp(&stubCtx{svc: map[string]any{"ctx.tools": sdk.ToolRegistry(reg)}})
	if _, err := b.shellRegistry(); err == nil || !strings.Contains(err.Error(), "tool-shell") {
		t.Fatalf("缺 shell 工具应显式报错: %v", err)
	}
	// 未装配路径:同步写入错误行(不上线程、不起进程)
	b.runShellPassthrough(context.Background(), "echo hi")
	last := b.model.state.Lines[len(b.model.state.Lines)-1]
	if last.Kind != "error" || !strings.Contains(last.Text, "tool-shell") {
		t.Fatalf("应回显显式错误: %+v", last)
	}
}

// TestExecShellThroughToolRegistry:命令经 ctx.tools.Execute 走完整管线(参数透传),
// 输出按 shell 工具结果形状解析(不伪造调用、不绕过 pre-execute)。
func TestExecShellThroughToolRegistry(t *testing.T) {
	reg := &shellRegStub{
		defs: []sdk.ToolDefinition{{Name: shellPassthroughTool}},
		res:  &sdk.ToolResult{Content: `{"output":"你好\n"}`},
	}
	a := newTestApp(&stubCtx{svc: map[string]any{"ctx.tools": sdk.ToolRegistry(reg)}})
	msg := a.execShell(context.Background(), reg, "echo 你好")
	if msg.err != nil {
		t.Fatalf("不应报错: %v", msg.err)
	}
	if reg.gotName != shellPassthroughTool {
		t.Errorf("应调用 %q,实为 %q", shellPassthroughTool, reg.gotName)
	}
	if !strings.Contains(reg.gotArgs, `"command":"echo 你好"`) {
		t.Errorf("参数应携带 command: %s", reg.gotArgs)
	}
	if !strings.Contains(msg.out, "你好") {
		t.Errorf("应回显输出: %q", msg.out)
	}
	// 工具级错误 → 回显错误原因
	reg.err = errors.New("管道炸了")
	if msg := a.execShell(context.Background(), reg, "x"); msg.err == nil {
		t.Errorf("工具错误应上抛: %+v", msg)
	}
	// 结构化错误(error 键)也回显为 ERROR 行
	reg.err = nil
	reg.res = &sdk.ToolResult{Content: `{"error":"shell: 缺少 command 参数"}`}
	if msg := a.execShell(context.Background(), reg, "x"); !strings.Contains(msg.out, "ERROR:") {
		t.Errorf("结构化错误应回显: %q", msg.out)
	}
}

// recLoop 记录 Run 调用次数(供路由断言;带锁兼容 -race)。
type recLoop struct {
	mu sync.Mutex
	n  int
}

func (l *recLoop) Run(context.Context, string) error {
	l.mu.Lock()
	l.n++
	l.mu.Unlock()
	return nil
}

func (l *recLoop) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

// TestSubmitRoutesShellPassthrough:以 ! 开头的输入在 submit 处被截获,不进 agent loop。
// (走未装配 ctx.tools 的同步失败路径,故无并发写:断言「未启动模型回合」+「显式错误回显」)
func TestSubmitRoutesShellPassthrough(t *testing.T) {
	loop := &recLoop{}
	a := NewApp(&stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry()}}, loop, stubLLM{}, "tui")
	a.submit("!echo hi")
	if loop.calls() != 0 {
		t.Fatalf("! 直通不得启动模型回合(实际 %d 次)", loop.calls())
	}
	lines := a.model.state.Lines
	if len(lines) != 2 || lines[0].Text != "! echo hi" {
		t.Fatalf("应回显命令与错误: %+v", lines)
	}
	if lines[1].Kind != "error" || !strings.Contains(lines[1].Text, "ctx.tools 未装配") {
		t.Fatalf("未装配应显式报错: %+v", lines[1])
	}
	if a.model.state.Running {
		t.Errorf("直通结束应回空闲态")
	}
}
