// 指令文件注入测试:全局/项目 AGENTS.md 按序注入、缺失跳过、优先级。
package hostsystemprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestInstructionInjection(t *testing.T) {
	// 全局:~/.gah 语义经 GAH_HOME 覆盖;项目:cwd 下 AGENTS.md(用临时目录避免污染仓库)
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("全局规则:回复用中文"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("项目规则:只改 src/"), 0o644); err != nil {
		t.Fatal(err)
	}

	prev, _ := os.Getwd()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)

	s := &Service{}
	s.globalInstr, _ = readFile(filepath.Join(home, "AGENTS.md"))
	s.projectInstr, _ = readFile(filepath.Join(proj, "AGENTS.md"))

	msgs := s.Assemble(nil, nil)
	sys := msgs[0].Content
	before := strings.Index(sys, "全局规则")
	if before < 0 {
		t.Fatal("system prompt 应包含全局指令")
	}
	if pidx := strings.Index(sys, "项目规则"); pidx < 0 || pidx < before {
		t.Fatal("项目指令应注入且位于全局指令之后(顺序即覆盖)")
	}
}

func TestMissingInstructionsSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	proj := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)

	s := &Service{} // 无任何指令文件
	msgs := s.Assemble(nil, nil)
	if strings.Contains(msgs[0].Content, "全局指令(AGENTS.md") {
		t.Fatal("缺失指令文件时不应注入空块")
	}
	if !strings.Contains(msgs[0].Content, "规则:") {
		t.Fatal("固定引导与规则应始终存在")
	}
}

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

var _ = sdk.RoleSystem

// TestToolCallDisciplineInRules 规则含工具调用纪律:结构化 tool_calls、禁止正文伪调用、禁止假装已调用。
func TestToolCallDisciplineInRules(t *testing.T) {
	s := &Service{}
	msgs := s.Assemble(nil, nil)
	sys := msgs[0].Content
	for _, want := range []string{
		"结构化 tool_calls 字段发起",
		"正文中的调用不会被 gah 执行",
		"不得假装已调用工具",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("系统规则应含 %q:\n%s", want, sys)
		}
	}
}
