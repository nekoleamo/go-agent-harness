// 真实技能文件 guard:仓库自带 SKILL.md 一旦损坏/格式变动,测试立即失败。
package hostskills

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRealSkillParses(t *testing.T) {
	p := filepath.Join("..", "..", ".gah", "skills", "gah-plugin-dev", "SKILL.md")
	s, err := parseSkill(p)
	if err != nil {
		t.Fatalf("仓库自带技能解析失败: %v", err)
	}
	if s.Name != "gah-plugin-dev" {
		t.Fatalf("技能名应为 gah-plugin-dev,got %q", s.Name)
	}
	if !strings.Contains(s.Description, "插件开发规范") {
		t.Fatalf("frontmatter description 不符: %q", s.Description)
	}
	if len(s.Triggers) == 0 {
		t.Fatal("frontmatter 应含 trigger 触发词")
	}
	if !strings.Contains(s.Body, "检查清单") || !strings.Contains(s.Body, "catalogue") {
		t.Fatalf("正文应包含规范要点: %.200s", s.Body)
	}
}
