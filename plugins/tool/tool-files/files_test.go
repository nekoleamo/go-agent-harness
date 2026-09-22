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

// callCtx 同 call,但由调用方给定 ctx(用于 S-P1-4 隔离运行:WithWorkRoot 覆盖工作根)。
func callCtx(t *testing.T, c sdk.Ctx, ctx context.Context, name, args string) (map[string]any, error) {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(ctx, name, args)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("结果应为 JSON: %s(%v)", res.Content, err)
	}
	return out, nil
}

// TestFileToolsIsolatedWorkRoot S-P1-4:调用级工作根覆盖后,相对路径写在 worktree 内解析,
// 写主 workspace 被拒(隔离运行的核心不变量:两个并行子代理写同名文件不互相覆盖)。
func TestFileToolsIsolatedWorkRoot(t *testing.T) {
	ws, wt := t.TempDir(), t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)
	ictx := sdk.WithWorkRoot(context.Background(), wt)

	out, err := callCtx(t, c, ictx, "file_write", `{"path":"same.txt","content":"wt"}`)
	if err != nil || out["error"] != nil {
		t.Fatalf("隔离运行相对写应成功: out=%v err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "same.txt")); err != nil {
		t.Fatalf("相对写应落在工作根内: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "same.txt")); err == nil {
		t.Fatal("相对写不得落回主 workspace(命名冲突即互相覆盖)")
	}
	// 主 workspace 相对路径(默认未隔离)仍照旧
	if out, err = call(t, c, "file_write", `{"path":"same.txt","content":"ws"}`); err != nil || out["error"] != nil {
		t.Fatalf("未隔离写应成功: %v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "same.txt")); err != nil {
		t.Fatalf("未隔离写应落在 workspace: %v", err)
	}
	// 隔离运行写主 workspace 绝对路径 = 显式拒绝(不静默改道)
	out, err = callCtx(t, c, ictx, "file_write",
		fmt.Sprintf(`{"path":%q,"content":"x"}`, filepath.Join(ws, "escape.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if out["error"] == nil {
		t.Fatalf("隔离运行写 workspace 应被拒: %v", out)
	}
	if !strings.Contains(fmt.Sprint(out["error"]), "工作根") {
		t.Fatalf("拒绝消息应指明工作根: %v", out["error"])
	}
	// 读:隔离运行下仍可读主 workspace(只收窄写)
	if err := os.WriteFile(filepath.Join(ws, "readable.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = callCtx(t, c, ictx, "file_read",
		fmt.Sprintf(`{"path":%q}`, filepath.Join(ws, "readable.txt")))
	if err != nil || out["content"] != "hi" {
		t.Fatalf("隔离运行应可读主 workspace 文件: out=%v err=%v", out, err)
	}
}

// TestFileWriteRejectsURLPath 文件工具把 URL 挡在写盘之前(未装配沙箱时也要挡)。
func TestFileWriteRejectsURLPath(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	out, err := call(t, c, "file_write", `{"path":"https://example.com/docs/a.md","content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "URL") {
		t.Fatalf("应显式拒绝 URL 路径,得到: %+v", out)
	}
	if entries, _ := os.ReadDir(ws); len(entries) != 0 {
		t.Fatalf("工作区不应被写进任何东西: %v", entries)
	}
	// 未装配沙箱的独立部署路径同样要挡住
	tool := &FilesTool{}
	if _, err := tool.resolve(context.Background(), "https://example.com/a.md", true); err == nil {
		t.Fatal("无沙箱时也应拒绝 URL 路径")
	}
}
