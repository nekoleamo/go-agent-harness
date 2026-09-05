// P4-5 C6 多级上下文文件加载单测:从 cwd 逐级向上收集(近者覆盖远者)、
// AGENTS.override.md 同级替换、缺失跳过、注入顺序与标题。
package hostsystemprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectLevelsWalkFarToNear(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("根级规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "AGENTS.md"), []byte("a 级规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("b 级规则"), 0o644); err != nil {
		t.Fatal(err)
	}

	levels := projectLevelsWalk(sub)
	if len(levels) != 3 {
		t.Fatalf("应有 3 级: %+v", levels)
	}
	if levels[0].dir != root || levels[2].dir != sub {
		t.Fatalf("顺序应为远→近(近者在后): %+v", levels)
	}
	if !strings.Contains(levels[2].content, "b 级规则") {
		t.Fatalf("最近级内容: %+v", levels[2])
	}
}

func TestProjectLevelsOverrideReplaces(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "p")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// 同级既有 AGENTS.md 又有 override:override 替换(忽略 AGENTS.md)
	if err := os.WriteFile(filepath.Join(d, "AGENTS.md"), []byte("普通规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "AGENTS.override.md"), []byte("覆盖规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	levels := projectLevelsWalk(d)
	if len(levels) != 1 {
		t.Fatalf("只有一级: %+v", levels)
	}
	if levels[0].file != "AGENTS.override.md" || !strings.Contains(levels[0].content, "覆盖规则") {
		t.Fatalf("override 应替换同级别: %+v", levels[0])
	}
}

func TestProjectLevelsMissingSkipped(t *testing.T) {
	// 空目录链:无任何指令文件 → 空列表(不注入空块)
	d := t.TempDir()
	if levels := projectLevelsWalk(d); len(levels) != 0 {
		t.Fatalf("无指令文件应空: %+v", levels)
	}
}

func TestProjectLevelsAssembleOrder(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "s")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("层 A 内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("层 B 内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{projectLevels: projectLevelsWalk(sub)}
	msgs := s.Assemble(nil, nil)
	sys := msgs[0].Content
	if !strings.Contains(sys, "层 A 内容") || !strings.Contains(sys, "层 B 内容") {
		t.Fatalf("应注入两级:\n%s", sys)
	}
	if strings.Index(sys, "层 A") > strings.Index(sys, "层 B") {
		t.Fatalf("远者应先于近者(近者在后覆盖):\n%s", sys)
	}
	if !strings.Contains(sys, "来自 "+root+"/AGENTS.md") {
		t.Fatalf("应标明来源目录:\n%s", sys)
	}
}
