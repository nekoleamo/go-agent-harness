// P4-11 E4 配置热更(/reload)单测:ReloadInstructions 重读文件反映外部编辑、
// 删除文件清除旧值(NotFound 语义)、读取失败保留旧值(错误回滚)、断言接口。
package hostsystemprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestReloadReflectsExternalEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	gp := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(gp, []byte("旧全局规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: instrCfg{global: true}}
	s.loadLocked()
	if s.globalInstr != "旧全局规则" {
		t.Fatalf("启动读取: %q", s.globalInstr)
	}
	// 外部编辑后 /reload
	if err := os.WriteFile(gp, []byte("新全局规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadInstructions(); err != nil {
		t.Fatalf("reload 应成功: %v", err)
	}
	if s.globalInstr != "新全局规则" {
		t.Fatalf("重载应反映外部编辑: %q", s.globalInstr)
	}
	msgs := s.Assemble(nil, nil)
	if !strings.Contains(msgs[0].Content, "新全局规则") {
		t.Fatalf("Assemble 应立即使用新值\n%s", msgs[0].Content)
	}
}

func TestReloadDeletedClears(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	gp := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(gp, []byte("将删除"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: instrCfg{global: true}}
	s.loadLocked()
	if err := os.Remove(gp); err != nil {
		t.Fatal(err)
	}
	// NotFound 语义:reload 清除旧值(不报错)
	if err := s.ReloadInstructions(); err != nil {
		t.Fatalf("删除后 reload 不应报错: %v", err)
	}
	if s.globalInstr != "" {
		t.Fatalf("删除文件应清除旧值: %q", s.globalInstr)
	}
}

func TestReloadFailureKeepsOld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	gp := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(gp, []byte("原值"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: instrCfg{global: true, extra: []string{gp}}}
	s.loadLocked()
	// 附加指令路径改为目录:ReadFile 目录必然失败(非 NotFound,应为错误)
	bad := t.TempDir()
	s.cfg.extra = []string{bad}
	if err := s.ReloadInstructions(); err == nil {
		t.Fatal("读取失败应报错(错误回滚)")
	}
	if s.globalInstr != "原值" {
		t.Fatalf("失败后旧值应保留: %q", s.globalInstr)
	}
}

func TestReloadServiceAssertion(t *testing.T) {
	s := &Service{cfg: instrCfg{}}
	var sp sdk.SystemPromptService = s
	if _, ok := sp.(sdk.ReloadableInstructions); !ok {
		t.Fatal("Service 应实现 sdk.ReloadableInstructions(/reload 依赖)")
	}
}
