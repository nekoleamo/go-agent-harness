// host-internal-commands 测试:B3 命令下沉——宿主注册集(11)与 UI 专属留 TUI(8),
// 判重跳过,命令执行经 Ctx 注入服务(thinking 经 stub llm)。
package hostintcmd

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-commands"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func buildEnv(t *testing.T) (*ctx.Ctx, sdk.CommandRegistry) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hostcommands.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	return c, cmds
}

// TestCommandsRegistered 下沉集注册进宿主;UI 专属集不在宿主(留 TUI)。
func TestCommandsRegistered(t *testing.T) {
	c, cmds := buildEnv(t)
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	down := []string{"thinking", "model", "provider", "sandbox", "approval", "plugins", "settings", "export", "compact", "workspace", "session", "reload"}
	for _, name := range down {
		if _, ok := cmds.Get(name); !ok {
			t.Fatalf("下沉命令 %s 未注册", name)
		}
	}
	ui := []string{"search", "widgets", "theme", "help", "exit", "fork", "tree", "name"}
	for _, name := range ui {
		if _, ok := cmds.Get(name); ok {
			t.Fatalf("UI 专属命令 %s 不应下沉到宿主", name)
		}
	}
}

// TestCommandsExecuteThinking 执行 /thinking 经 Ctx 注入 llm(与 TUI 等价)。
func TestCommandsExecuteThinking(t *testing.T) {
	c, cmds := buildEnv(t)
	st := &stubLLM{}
	if err := c.Provide("ctx.llm", st); err != nil {
		t.Fatal(err)
	}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	spec, ok := cmds.Get("thinking")
	if !ok {
		t.Fatal("thinking 未注册")
	}
	out, err := spec.Run([]string{"high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "high") || st.thinking != "high" {
		t.Fatalf("thinking 执行不符: out=%q set=%s", out, st.thinking)
	}
}

// TestCommandsExecuteApproval 执行 /approval 经 Ctx 注入审批服务(与 TUI 等价),并持久化偏好。
func TestCommandsExecuteApproval(t *testing.T) {
	c, cmds := buildEnv(t)
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	ap := &stubApproval{mode: sdk.ApprovalSmart}
	if err := c.Provide("ctx.approval", ap); err != nil {
		t.Fatal(err)
	}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	spec, ok := cmds.Get("approval")
	if !ok {
		t.Fatal("approval 未注册")
	}
	if _, err := spec.Run([]string{"strict"}); err != nil {
		t.Fatal(err)
	}
	if ap.mode != sdk.ApprovalStrict {
		t.Fatalf("approval 执行未切档: %s", ap.mode)
	}
	// 偏好持久化(重启恢复)
	if p := prefs.Load(); p.Approval != "strict" {
		t.Fatalf("偏好未持久化: %+v", p)
	}
}

// stubApproval 最小审批桩。
type stubApproval struct {
	mode sdk.ApprovalMode
}

func (s *stubApproval) Mode() sdk.ApprovalMode { return s.mode }
func (s *stubApproval) SetMode(m sdk.ApprovalMode) {
	s.mode = m
}

// stubLLM 最小 LLM 桩(仅实现被断言方法;其余经 embed 兜底)。
type stubLLM struct {
	sdk.LLMService
	thinking string
}

func (s *stubLLM) SetThinking(l sdk.ThinkingLevel) { s.thinking = l.String() }

// TestDupSkip 判重:宿主已注册同名再 Start → 跳过不覆盖(先到先得)。
func TestDupSkip(t *testing.T) {
	c, cmds := buildEnv(t)
	dis1, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis1()
	spec, _ := cmds.Get("thinking")
	// 二次 Start 应跳过同名(无冲突错误)
	dis2, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	dis2()
	spec2, _ := cmds.Get("thinking")
	if spec.Name != spec2.Name {
		t.Fatal("判重跳过后命令不应被替换")
	}
}
