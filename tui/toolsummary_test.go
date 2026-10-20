package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestToolResultUsesHostSummary 工具结果行优先用宿主算好的**人话摘要**(2026-10-20 B1)。
//
// 钉它的理由:旧行为是把结果 JSON 直接截断显示 —— file_read 显示成
// `{"content":"package main\n\nimport (` —— 一个字也没告诉人"它读了多少行"。
// 用户要的是**结论**,不是**开销**。
func TestToolResultUsesHostSummary(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c1", Name: "file_read",
		Content: `{"path":"/a/b.go","content":"package main\nfunc main(){}\n","lines":2}`,
		Summary: "读 2 行 · 24 B",
	}})
	if len(s.Lines) != 1 {
		t.Fatalf("应有一行结果: %+v", s.Lines)
	}
	ln := s.Lines[0]
	if !strings.Contains(ln.Text, "读 2 行") {
		t.Fatalf("结果行应显示人话摘要: %q", ln.Text)
	}
	if strings.Contains(ln.Text, `{"content"`) {
		t.Fatalf("结果行不该再显示 JSON 开头: %q", ln.Text)
	}
	// 全文仍保留(可展开看细节)
	if !strings.Contains(ln.Full, "package main") {
		t.Fatalf("展开内容应保留全文: %q", ln.Full)
	}
}

// TestToolResultSummaryMissingFallsBack 摘要缺失(旧会话日志/未知工具)⇒ 回退到原截断行为。
func TestToolResultSummaryMissingFallsBack(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c1", Name: "file_read", Content: `{"content":"package main"}`,
	}})
	ln := s.Lines[0]
	if !strings.Contains(ln.Text, `{"content"`) {
		t.Fatalf("无摘要时应回退到截断全文: %q", ln.Text)
	}
}

// TestToolResultContentErrorMarkedErr 内容级错误(工具把失败写在结果 JSON 里)必须标 ✗。
//
// 钉它的理由:此前这类失败显示成「✓ file_read: {"error":…}」—— 给一次失败打绿勾,
// 比"没信息"更误导。
func TestToolResultContentErrorMarkedErr(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c1", Name: "file_read",
		Content: `{"error":"读 /x: permission denied"}`,
		Summary: "读 /x: permission denied", SummaryErr: true,
	}})
	ln := s.Lines[0]
	if !strings.HasPrefix(ln.Text, "✗") {
		t.Fatalf("内容级错误应标 ✗: %q", ln.Text)
	}
	if ln.Kind != "error" {
		t.Fatalf("内容级错误的行类型应为 error: %q", ln.Kind)
	}
}
