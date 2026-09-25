package policyguard

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestModeSetGet(t *testing.T) {
	p := DefaultSandbox(t.TempDir())
	if p.Mode() != sdk.SandboxWorkspace {
		t.Fatalf("默认档应为 workspace-write,got %s", p.Mode())
	}
	p.SetMode(sdk.SandboxReadOnly)
	if p.Mode() != sdk.SandboxReadOnly {
		t.Fatal("SetMode 未生效")
	}
}

func TestCheckToolByMode(t *testing.T) {
	root := t.TempDir()
	p := DefaultSandbox(root)
	// workspace-write:执行器放行
	if err := p.CheckTool("shell"); err != nil {
		t.Fatalf("workspace-write 应放行 shell,got %v", err)
	}
	// read-only:执行器 veto,非执行器放行
	p.SetMode(sdk.SandboxReadOnly)
	if err := p.CheckTool("shell"); err == nil {
		t.Fatal("read-only 应 veto shell")
	}
	if err := p.CheckTool("read_file"); err != nil {
		t.Fatalf("read-only 应放行读类工具,got %v", err)
	}
	// full-access:全部放行
	p.SetMode(sdk.SandboxFullAccess)
	if err := p.CheckTool("shell"); err != nil {
		t.Fatalf("full-access 应放行 shell,got %v", err)
	}
}

func TestValidatePath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	os.MkdirAll(sub, 0o755)
	p := DefaultSandbox(root)

	if err := p.ValidatePath("sub/a.txt"); err != nil { // 相对 → 在 root 内
		t.Fatalf("workspace 内相对路径应放行,got %v", err)
	}
	if err := p.ValidatePath("../escape.txt"); err == nil {
		t.Fatal("相对 ../ 穿越应拒绝")
	}
	if err := p.ValidatePath(filepath.Join(root, "..", "x.txt")); err == nil {
		t.Fatal("绝对路径穿越应拒绝")
	}
	p.SetMode(sdk.SandboxReadOnly)
	if err := p.ValidatePath("sub/a.txt"); err == nil {
		t.Fatal("read-only 应拒绝一切写")
	}
	p.SetMode(sdk.SandboxFullAccess)
	if err := p.ValidatePath("../anywhere.txt"); err != nil {
		t.Fatalf("full-access 应放行,got %v", err)
	}
}

func TestValidatePathRejectsOutOfWorkspaceAbs(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	p := DefaultSandbox(root)
	if err := p.ValidatePath(filepath.Join(other, "f.txt")); err == nil {
		t.Fatal("workspace 外的绝对路径应拒绝")
	}
}

func TestDefaultRoot(t *testing.T) {
	p := DefaultSandbox("/tmp/ws")
	if !strings.HasSuffix(p.Root(), "ws") {
		t.Fatalf("root 不符: %s", p.Root())
	}
}

// —— S-P1-4 调用级工作根(隔离子代理 = 受管 worktree)——

// 写:隔离运行下写范围 = 本次工作根(worktree),主 workspace 反被拒(改动不落主工作区)。
func TestValidatePathAtIsolatedWorktree(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	p := DefaultSandbox(root)
	if err := p.ValidatePathAt(wt, "out.txt"); err != nil {
		t.Fatalf("worktree 内相对写应放行,got %v", err)
	}
	if err := p.ValidatePathAt(wt, filepath.Join(wt, "sub", "a.txt")); err != nil {
		t.Fatalf("worktree 内绝对写应放行,got %v", err)
	}
	err := p.ValidatePathAt(wt, filepath.Join(root, "foo.go"))
	if err == nil {
		t.Fatal("隔离运行写主 workspace 应拒绝(否则两子代理仍互相覆盖)")
	}
	if !strings.Contains(err.Error(), "本次工作根") {
		t.Fatalf("错误消息应指明本次工作根(否则子代理看不懂为何被拒): %v", err)
	}
	if !strings.Contains(err.Error(), wt) {
		t.Fatalf("错误消息应含工作根路径: %v", err)
	}
	// 未隔离调用(root 空)行为不变:仍按沙箱自身 root
	if err := p.ValidatePathAt("", "sub/a.txt"); err != nil {
		t.Fatalf("空工作根应退回自身 root,got %v", err)
	}
	// read-only 档位在隔离运行下同样拒绝一切写
	p.SetMode(sdk.SandboxReadOnly)
	if err := p.ValidatePathAt(wt, "out.txt"); err == nil {
		t.Fatal("read-only 应拒绝隔离写")
	}
}

// 读:隔离只收窄写范围,不缩小读范围(worktree 之外的 workspace 文件仍可读)。
func TestValidateReadAtKeepsWorkspaceReadable(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	other := t.TempDir()
	p := DefaultSandbox(root)
	if err := p.ValidateReadAt(wt, filepath.Join(wt, "in.txt")); err != nil {
		t.Fatalf("worktree 内读应放行,got %v", err)
	}
	if err := p.ValidateReadAt(wt, filepath.Join(root, "untracked.txt")); err != nil {
		t.Fatalf("主 workspace 读应放行(隔离只限写),got %v", err)
	}
	if err := p.ValidateReadAt(wt, filepath.Join(other, "x.txt")); err == nil {
		t.Fatal("workspace 与数据根之外的读应拒绝")
	}
	if _, ok := interface{}(p).(sdk.RootScoped); !ok {
		t.Fatal("SandboxPolicy 应实现 sdk.RootScoped(工具侧按调用根自检)")
	}
}

// host pre-execute 用的 CheckToolCallAt:file_write 在隔离运行下按 worktree 裁决。
func TestCheckToolCallAtIsolated(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	p := DefaultSandbox(root)
	args := func(path string) string {
		return `{"path":` + strconv.Quote(path) + `,"content":"x"}`
	}
	if err := p.CheckToolCallAt(wt, "file_write", args("a.txt"), nil); err != nil {
		t.Fatalf("隔离运行写 worktree 相对路径应放行,got %v", err)
	}
	if err := p.CheckToolCallAt(wt, "file_write", args(filepath.Join(root, "a.txt")), nil); err == nil {
		t.Fatal("隔离运行写主 workspace 应被拒")
	}
	if err := p.CheckToolCallAt("", "file_write", args("a.txt"), nil); err != nil {
		t.Fatalf("空工作根应退回自身 root,got %v", err)
	}
}

// shellAbs 把本地绝对路径写成该平台 shell 能表达的绝对形态。
// Windows 上 shell 是 git-bash(MSYS):`C:\...` 里的反斜杠是转义字符会被吃掉,
// 命令实际落到相对位置(与真实 shell 行为一致,不是裁决层缺陷)⇒ 用 `/c/...` 表达;
// 该形态是 MSYS 根相对、落点不可静态确定,同样必须被拒。
func shellAbs(p string) string {
	if runtime.GOOS != "windows" {
		return p
	}
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(filepath.ToSlash(p), vol)
	return "/" + strings.ToLower(strings.TrimSuffix(vol, ":")) + rest
}

// shell 命令:相对写目标以本次工作根为基准(与工具侧 cmd.Dir 同基准)。
func TestCheckShellCommandAtIsolated(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	p := DefaultSandbox(root)
	if err := p.CheckShellCommandAt(wt, "echo hi > out.txt"); err != nil {
		t.Fatalf("隔离运行下相对写应放行,got %v", err)
	}
	if err := p.CheckShellCommandAt(wt, "echo hi > "+shellAbs(filepath.Join(root, "out.txt"))); err == nil {
		t.Fatal("隔离运行下写主 workspace 应被拒")
	}
	if err := p.CheckShellCommand("echo hi > out.txt"); err != nil {
		t.Fatalf("未隔离调用行为不变,got %v", err)
	}
}
