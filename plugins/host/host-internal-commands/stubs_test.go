// 命令执行补测用的最小桩(sandbox/session/log/cwd/prompt/turnControl)。
package hostintcmd

import (
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubSandbox 最小沙箱桩。
type stubSandbox struct {
	mode sdk.SandboxMode
	root string
}

func (s *stubSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *stubSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *stubSandbox) Root() string              { return s.root }
func (s *stubSandbox) ValidatePath(string) error { return nil }

// stubLog 会话日志桩(实现 CompactService:fold/summary/cerr 驱动压缩分支)。
type stubLog struct {
	events  []sdk.SessionEvent
	history *int
	fold    int
	summary string
	cerr    error
}

func newStubLog() *stubLog { return &stubLog{} }

func (s *stubLog) Append(ev sdk.SessionEvent) error              { s.events = append(s.events, ev); return nil }
func (s *stubLog) DeriveMessages() []sdk.LLMMessage              { return nil }
func (s *stubLog) Replay() []sdk.SessionEvent                    { return s.events }
func (s *stubLog) Flush() error                                  { return nil }
func (s *stubLog) SetPath(string)                                {}
func (s *stubLog) Load(string) error                             { return nil }
func (s *stubLog) SetHistory(n int)                              { s.history = &n }
func (s *stubLog) RegisterCompressor(int, sdk.SessionCompressor) {}
func (s *stubLog) Compact(string) (string, int, error)           { return s.summary, s.fold, s.cerr }

// stubLogOnly 仅满足 SessionLog(内嵌 nil 接口 → 不实现 CompactService):
// 验证 /compact 在会话日志未实现压缩能力时显式报不可用。
type stubLogOnly struct{ sdk.SessionLog }

// stubCwd 会话桩:内嵌接口(本测试只覆盖命令用到的子集,其余方法 nil panic)。
type stubCwd struct {
	sdk.CwdSessions
	cur        string
	list       []string
	curSession string
	path       string
	opened     string
	name       string
}

func (s *stubCwd) Current() string        { return s.cur }
func (s *stubCwd) Path() string           { return s.path }
func (s *stubCwd) List() []string         { return s.list }
func (s *stubCwd) Open(id string) error   { s.opened = id; s.curSession = id; return nil }
func (s *stubCwd) CurrentSession() string { return s.curSession }
func (s *stubCwd) SessionName() string    { return s.name }
func (s *stubCwd) New() (string, error)   { return "n1", nil }

// stubPrompt 系统提示桩(实现 ReloadableInstructions)。
type stubPrompt struct {
	reloaded bool
	err      error
}

func (s *stubPrompt) AddSection(sec sdk.SystemPromptSection) sdk.Disposer { return func() {} }
func (s *stubPrompt) Assemble(history []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	return history
}
func (s *stubPrompt) ReloadInstructions() error {
	if s.err != nil {
		return s.err
	}
	s.reloaded = true
	return nil
}

// stubPromptPlain 未实现 ReloadableInstructions 的系统提示桩。
type stubPromptPlain struct{}

func (s *stubPromptPlain) AddSection(sec sdk.SystemPromptSection) sdk.Disposer { return func() {} }
func (s *stubPromptPlain) Assemble(history []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	return history
}

// stubTurn 回合控制桩。
type stubTurn struct {
	running   bool
	cancelled bool
}

func (s *stubTurn) Running() bool { return s.running }
func (s *stubTurn) Cancel()       { s.cancelled = true }
