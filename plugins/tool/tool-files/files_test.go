package toolfiles

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/policy/policy-guard"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv 装配 host-tools + tool-files,带指定沙箱(workspace-write 默认档)。
func buildEnv(t *testing.T, root string, mode sdk.SandboxMode) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	sb := policyguard.DefaultSandbox(root)
	sb.SetMode(mode)
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func call(t *testing.T, c sdk.Ctx, name, args string) (map[string]any, error) {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), name, args)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("结果应为 JSON: %s(%v)", res.Content, err)
	}
	return out, nil
}

// TestFileReadWrite workspace-write 档:写读往返(相对路径以 workspace 为根)。
func TestFileReadWrite(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)

	out, err := call(t, c, "file_write", `{"path":"a/b.txt","content":"你好文件"}`)
	if err != nil || out["error"] != nil {
		t.Fatalf("write 失败: %v %v", err, out)
	}
	// 相对路径落在 workspace 内
	p := out["path"].(string)
	// 后缀要按平台分隔符比:Windows 上落盘路径是 a\b.txt,直接比 "a/b.txt" 会失败。
	if !strings.HasPrefix(p, ws) || !strings.HasSuffix(p, filepath.FromSlash("a/b.txt")) {
		t.Fatalf("路径应解析到 workspace 内: %s", p)
	}
	out, err = call(t, c, "file_read", `{"path":"a/b.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out["content"] != "你好文件" {
		t.Fatalf("读回内容不符: %v", out)
	}
}

// TestFileEdit file_edit 片段替换(写路径,经沙箱校验)。
func TestFileEdit(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)
	if _, err := call(t, c, "file_write", `{"path":"x.txt","content":"aaa bbb ccc"}`); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, c, "file_edit", `{"path":"x.txt","old":"bbb","new":"BB"}`)
	if err != nil || out["error"] != nil {
		t.Fatalf("edit 失败: %v %v", err, out)
	}
	out, _ = call(t, c, "file_read", `{"path":"x.txt"}`)
	if out["content"] != "aaa BB ccc" {
		t.Fatalf("编辑结果不符: %v", out)
	}
	// 未匹配片段 → 结构化错误
	out, _ = call(t, c, "file_edit", `{"path":"x.txt","old":"zzz","new":"no"}`)
	if out["error"] == nil {
		t.Fatal("未匹配片段应报错")
	}
}

// TestSandboxWorkspaceEscape workspace-write:写 workspace 外被拒;读外被拒。Read-only:写被拒读放行。
func TestSandboxWorkspaceEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)

	out, _ := call(t, c, "file_write", fmt.Sprintf(`{"path":%q,"content":"x"}`, filepath.Join(outside, "o.txt")))
	if out["error"] == nil {
		t.Fatalf("写 workspace 外应被拒绝: %v", out)
	}
	out, _ = call(t, c, "file_read", fmt.Sprintf(`{"path":%q,"content":"x"}`, filepath.Join(outside, "o.txt")))
	if out["error"] == nil {
		t.Fatal("读 workspace 外应被拒绝")
	}
	// 相对路径穿越
	out, _ = call(t, c, "file_read", `{"path":"../outside.txt"}`)
	if out["error"] == nil {
		t.Fatal("../ 穿越应被拒绝")
	}
}

// TestSandboxReadOnly read-only:写全拒,读放行。
func TestSandboxReadOnly(t *testing.T) {
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "r.txt"), []byte("readable"), 0o644)
	c := buildEnv(t, ws, sdk.SandboxReadOnly)

	out, _ := call(t, c, "file_write", `{"path":"w.txt","content":"x"}`)
	if out["error"] == nil {
		t.Fatal("read-only 写应被拒")
	}
	out, _ = call(t, c, "file_edit", `{"path":"r.txt","old":"a","new":"b"}`)
	if out["error"] == nil {
		t.Fatal("read-only edit 应被拒")
	}
	out, err := call(t, c, "file_read", `{"path":"r.txt"}`)
	if err != nil || out["content"] != "readable" {
		t.Fatalf("read-only 读应放行: %v %v", err, out)
	}
}

// TestFullAccess full-access 放行任何路径(写)。
func TestFullAccess(t *testing.T) {
	outside := t.TempDir()
	c := buildEnv(t, t.TempDir(), sdk.SandboxFullAccess)
	out, err := call(t, c, "file_write", fmt.Sprintf(`{"path":%q,"content":"any"}`, filepath.Join(outside, "f.txt")))
	if err != nil || out["error"] != nil {
		t.Fatalf("full-access 写外部应放行: %v %v", err, out)
	}
}

// TestAppend file_append 追加。
func TestAppend(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)
	call(t, c, "file_write", `{"path":"a.txt","content":"1"}`)
	if _, err := call(t, c, "file_append", `{"path":"a.txt","content":"2"}`); err != nil {
		t.Fatal(err)
	}
	out, _ := call(t, c, "file_read", `{"path":"a.txt"}`)
	if out["content"] != "12" {
		t.Fatalf("追加结果不符: %v", out)
	}
}
