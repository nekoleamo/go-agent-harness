// role_test.go:生效值派生纯函数的表驱动用例。
//
// 为何单测值得写这么细:这两个函数是"运行期真正用什么"与"三端显示什么"的**同一判据**
// (见 sdk/role.go 注释)。它们错了不会报错,只会让人看到与实际不符的界面。
package sdk

import (
	"strings"
	"testing"
)

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

// TestToolVisible 角色工具可见性的唯一判据(运行期过滤与展示端共用)。
func TestToolVisible(t *testing.T) {
	cases := []struct {
		name string
		role *RoleSpec
		tool string
		want bool
	}{
		{"无角色(基线)→ 全放行", nil, "shell", true},
		{"未声明排除清单 → 全放行", &RoleSpec{}, "shell", true},
		{"在排除清单里 → 不可见", &RoleSpec{ToolsExclude: []string{"shell"}}, "shell", false},
		{"不在排除清单里 → 可见", &RoleSpec{ToolsExclude: []string{"shell"}}, "file_read", true},
		{"空串排除清单(长度为 0)→ 全放行", &RoleSpec{ToolsExclude: []string{}}, "shell", true},
		{"名字区分大小写(不做模糊匹配)", &RoleSpec{ToolsExclude: []string{"shell"}}, "Shell", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToolVisible(c.role, c.tool); got != c.want {
				t.Fatalf("ToolVisible(%+v, %q) = %v,期望 %v", c.role, c.tool, got, c.want)
			}
		})
	}
}

// TestNormalizeToolNames 形状校验:坏值显式失败(不静默丢弃),重复去重保序。
func TestNormalizeToolNames(t *testing.T) {
	got, err := NormalizeToolNames([]string{" shell ", "file_read", "shell", "mcp_deja_search"})
	if err != nil {
		t.Fatalf("NormalizeToolNames 正常输入报错: %v", err)
	}
	want := []string{"shell", "file_read", "mcp_deja_search"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeToolNames = %v,期望 %v(去空白 + 去重保序)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizeToolNames = %v,期望 %v", got, want)
		}
	}
	if empty, err := NormalizeToolNames(nil); err != nil || empty != nil {
		t.Fatalf("NormalizeToolNames(nil) = (%v, %v),期望 (nil, nil)", empty, err)
	}
	bad := [][]string{
		{""},
		{"   "},
		{"a b"},
		{"a\tb"},
		{"a\nb"},
		{strings.Repeat("x", MaxToolNameLen+1)},
	}
	for _, in := range bad {
		if _, err := NormalizeToolNames(in); err == nil {
			t.Errorf("NormalizeToolNames(%q) = nil,期望报错(坏值不能静默)", in)
		}
	}
}
