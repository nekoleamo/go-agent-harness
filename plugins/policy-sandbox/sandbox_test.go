package policysandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestModeSetGet(t *testing.T) {
	p := Default(t.TempDir())
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
	p := Default(root)
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
	p := Default(root)

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
	p := Default(root)
	if err := p.ValidatePath(filepath.Join(other, "f.txt")); err == nil {
		t.Fatal("workspace 外的绝对路径应拒绝")
	}
}

func TestDefaultRoot(t *testing.T) {
	p := Default("/tmp/ws")
	if !strings.HasSuffix(p.Root(), "ws") {
		t.Fatalf("root 不符: %s", p.Root())
	}
}
