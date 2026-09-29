// role_test.go:生效值派生纯函数的表驱动用例。
//
// 为何单测值得写这么细:这两个函数是"运行期真正用什么"与"三端显示什么"的**同一判据**
// (见 sdk/role.go 注释)。它们错了不会报错,只会让人看到与实际不符的界面。
package sdk

import "testing"

func TestEffectiveModel(t *testing.T) {
	cases := []struct {
		name         string
		sessionModel string
		role         *RoleSpec
		wantModel    string
		wantSrc      string
	}{
		{"角色声明了模型 → 用角色模型", "gpt-x", &RoleSpec{Model: "claude-y"}, "claude-y", SourceRole},
		{"角色未声明 → 跟随会话", "gpt-x", &RoleSpec{}, "gpt-x", SourceSession},
		{"无角色(基线)→ 跟随会话", "gpt-x", nil, "gpt-x", SourceSession},
		{"两边都空 → 空,来源记为会话", "", &RoleSpec{}, "", SourceSession},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model, src := EffectiveModel(c.sessionModel, c.role)
			if model != c.wantModel || src != c.wantSrc {
				t.Fatalf("EffectiveModel(%q, %+v) = (%q, %q),期望 (%q, %q)",
					c.sessionModel, c.role, model, src, c.wantModel, c.wantSrc)
			}
		})
	}
}

func TestEffectiveThinking(t *testing.T) {
	cases := []struct {
		name            string
		sessionThinking string
		role            *RoleSpec
		wantLevel       ThinkingLevel
		wantSrc         string
	}{
		{"角色声明 → 用角色档", "high", &RoleSpec{Thinking: "low"}, ThinkingLow, SourceRole},
		{"角色显式 off 压过会话 high", "high", &RoleSpec{Thinking: "off"}, ThinkingOff, SourceRole},
		{"角色未声明 → 跟随会话", "medium", &RoleSpec{}, ThinkingMedium, SourceSession},
		{"无角色 → 跟随会话", "medium", nil, ThinkingMedium, SourceSession},
		{"角色写了非法档 → 归 Off 但仍标注角色来源", "high", &RoleSpec{Thinking: "bogus"}, ThinkingOff, SourceRole},
		{"两边都空 → Off(会话来源)", "", &RoleSpec{}, ThinkingOff, SourceSession},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			level, src := EffectiveThinking(c.sessionThinking, c.role)
			if level != c.wantLevel || src != c.wantSrc {
				t.Fatalf("EffectiveThinking(%q, %+v) = (%v, %q),期望 (%v, %q)",
					c.sessionThinking, c.role, level, src, c.wantLevel, c.wantSrc)
			}
		})
	}
}
