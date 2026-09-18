// shell.go:S-P2-4 「!」shell 直通(TUI)。
//
// 语义(对标 Hermes `!` shell 直通 / Codex shell 直通):
//
//	输入框以 ! 开头 → 立即在**沙箱与策略管线内**执行该 shell 命令,输出就地追加到会话流,
//	不进模型上下文(不产生 turn、不调用 LLM)。
//
// 三条硬约束(实现取舍都由此推出):
//  1. **不绕过安全管线**:执行经 ctx.tools.Execute("shell", …) 走 host-tools 完整流水线
//     (tools/pre-execute 瀑布 → policy-guard 审批裁决 → 有效沙箱档位提示 → 内核级约束),
//     与模型调用 shell 工具是同一条路径,不存在「用户手敲就免检」的旁路。
//  2. **不写会话账本**:会话 jsonl 的 tool 事件由 agent-loop 成对写入(call+result);
//     这里若只追加 result 会留下**孤立 tool 消息**,投影给模型时 API 侧通常直接报错
//     (tool 结果必须对应前一条 assistant 的 tool_calls)。故 ! 直通只做本地回显,
//     不落盘、不进上下文(需要留痕时用模型工具或 /export)。
//  3. **不阻塞 UI**:执行在 goroutine 中跑,完成经 shellDoneMsg 回投 UI 线程改状态。
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// shellPassthroughTool 直通使用的工具名(tool-shell 提供;插件缺失时显式报错,不静默降级)。
const shellPassthroughTool = "shell"

// shellOutMaxRunes 回显上限(超长输出保留尾部;完整输出请用模型工具或重定向到文件)。
const shellOutMaxRunes = 4000

// shellDoneMsg 「!」命令结束(输出就地回显;err 非空 = 启动/执行失败)。
type shellDoneMsg struct {
	cmd string
	out string
	err error
}

// isShellPassthrough 是否为 shell 直通输入(前缀 !;单独一个 ! 视为普通输入,不执行空命令)。
func isShellPassthrough(input string) (string, bool) {
	t := strings.TrimSpace(input)
	if !strings.HasPrefix(t, "!") {
		return "", false
	}
	cmd := strings.TrimSpace(strings.TrimPrefix(t, "!"))
	return cmd, cmd != ""
}

// shellRegistry 解析工具注册表并校验 shell 工具存在(缺失时显式失败,不静默降级)。
func (a *App) shellRegistry() (sdk.ToolRegistry, error) {
	var reg sdk.ToolRegistry
	if err := a.c.Inject("ctx.tools", &reg); err != nil || reg == nil {
		return nil, fmt.Errorf("ctx.tools 未装配:无法执行 ! 命令(host-tools/tool-shell 未加载)")
	}
	if _, ok := reg.Get(shellPassthroughTool); !ok {
		return nil, fmt.Errorf("工具 %q 未注册:无法执行 ! 命令(请启用 tool-shell)", shellPassthroughTool)
	}
	return reg, nil
}

// execShell 同步执行一次直通(goroutine 与单测共用;经 ctx.tools 完整流水线)。
func (a *App) execShell(ctx context.Context, reg sdk.ToolRegistry, cmd string) shellDoneMsg {
	args, _ := json.Marshal(map[string]any{"command": cmd})
	res, err := reg.Execute(ctx, shellPassthroughTool, string(args))
	if err != nil {
		return shellDoneMsg{cmd: cmd, err: err}
	}
	out := ""
	if res != nil {
		out = shellOutput(res)
	}
	return shellDoneMsg{cmd: cmd, out: out}
}

// runShellPassthrough 执行直通命令(异步;结果经 shellDoneMsg 回 UI)。
// ctx 由调用方给出并在取消链上注册(Esc 中断 = 杀进程)。
func (a *App) runShellPassthrough(ctx context.Context, cmd string) {
	reg, err := a.shellRegistry()
	if err != nil {
		a.sendShellDone(shellDoneMsg{cmd: cmd, err: err})
		return
	}
	go func() {
		msg := a.execShell(ctx, reg, cmd)
		a.cancelFn.Store(nil) // 执行结束即摘除取消句柄(Esc 不再作用于已结束的命令)
		a.sendShellDone(msg)
	}()
}

// sendShellDone 回投结果到 UI 线程(未启动时直接写状态,兼容测试/装配期)。
func (a *App) sendShellDone(msg shellDoneMsg) {
	if a.started.Load() {
		a.program.Send(msg)
		return
	}
	a.model.appendShellResult(msg)
}

// shellOutput 从工具结果提取可读输出:shell 工具返回 {"output":…,"timeout":…,"error":…}。
func shellOutput(res *sdk.ToolResult) string {
	if res == nil {
		return ""
	}
	if res.Content != "" {
		var m map[string]any
		if json.Unmarshal([]byte(res.Content), &m) == nil {
			var sb strings.Builder
			if s, ok := m["output"].(string); ok {
				sb.WriteString(s)
			}
			if t, ok := m["timeout"].(bool); ok && t {
				sb.WriteString("\n(命令超时:已终止并返回已捕获输出)")
			}
			// 错误两个来源:结构化结果里的 error 键(tool-shell 的契约)、结果对象的 Error 字段
			// (host-tools 会把 error 键提升到该字段)。两者都认,避免直接调用时漏渲染错误。
			es := res.Error
			if es == "" {
				if e, ok := m["error"].(string); ok {
					es = e
				}
			}
			if es != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString("ERROR: " + es)
			}
			if sb.Len() > 0 {
				return sb.String()
			}
		}
		if res.Error != "" {
			return "ERROR: " + res.Error
		}
		return res.Content
	}
	if res.Error != "" {
		return "ERROR: " + res.Error
	}
	return ""
}

// appendShellResult 把 ! 命令与其输出追加到会话流(仅本地回显;见文件头约束 2)。
func (m *Model) appendShellResult(msg shellDoneMsg) {
	m.state.Lines = append(m.state.Lines, Line{Kind: "user", Text: "! " + msg.cmd})
	switch {
	case msg.err != nil:
		m.state.Lines = append(m.state.Lines, Line{Kind: "error", Text: "! 执行失败: " + msg.err.Error()})
	case strings.TrimSpace(msg.out) == "":
		m.state.Lines = append(m.state.Lines, Line{Kind: "tool", Text: "✓ (无输出)"})
	default:
		m.state.Lines = append(m.state.Lines, Line{Kind: "tool", Text: "✓ " + clipTail(msg.out, shellOutMaxRunes)})
	}
	m.state.Running = false
	m.state.LastTool = ""
	m.skipView = false
}

// clipTail 超长输出保留尾部(最近内容更有用;与 /jobs 的 tail 同口径)。
func clipTail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return fmt.Sprintf("…(前 %d 字符已省略)\n", len(r)-n) + string(r[len(r)-n:])
}

// handleShellMsg 处理 shellDoneMsg(Update 分支调用)。
func (m *Model) handleShellMsg(msg shellDoneMsg) tea.Cmd {
	m.appendShellResult(msg)
	return nil
}
