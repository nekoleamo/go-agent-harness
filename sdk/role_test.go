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

// TestNormalizeRoleApproval / TestNormalizeRoleSandbox / TestTighten* 第九十二批:
// 角色**只能收紧**权限档 —— 值域、非法值显式报错、合成取更严者,三件事都由 sdk 定死
// (运行期、面板、Web PATCH 共用同一判据,避免三处各写一遍后漂开)。
func TestNormalizeRoleApproval(t *testing.T) {
	ok := map[string]string{"": "", "  ": "", "smart": "smart", " STRICT ": "strict"}
	for in, want := range ok {
		got, err := NormalizeRoleApproval(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRoleApproval(%q) = (%q, %v),期望 (%q, nil)", in, got, err, want)
		}
	}
	// open 不是"不认识的档",而是**放宽** —— 必须给一条能看懂原因的错,不能只说"不合法"
	if _, err := NormalizeRoleApproval("open"); err == nil {
		t.Error("open 应被拒(角色只能收紧)")
	} else if !strings.Contains(err.Error(), "收紧") {
		t.Errorf("open 的报错没说清 只能收紧: %v", err)
	}
	for _, bad := range []string{"off", "Open", "strictest", "1"} {
		if _, err := NormalizeRoleApproval(bad); err == nil {
			t.Errorf("NormalizeRoleApproval(%q) = nil,期望报错", bad)
		}
	}
}

func TestNormalizeRoleSandbox(t *testing.T) {
	ok := map[string]string{"": "", "read-only": "read-only", " Workspace-Write ": "workspace-write"}
	for in, want := range ok {
		got, err := NormalizeRoleSandbox(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRoleSandbox(%q) = (%q, %v),期望 (%q, nil)", in, got, err, want)
		}
	}
	if _, err := NormalizeRoleSandbox("full-access"); err == nil {
		t.Error("full-access 应被拒(角色只能收紧)")
	} else if !strings.Contains(err.Error(), "收紧") {
		t.Errorf("full-access 的报错没说清 只能收紧: %v", err)
	}
	for _, bad := range []string{"readonly", "ro", "rw"} {
		if _, err := NormalizeRoleSandbox(bad); err == nil {
			t.Errorf("NormalizeRoleSandbox(%q) = nil,期望报错", bad)
		}
	}
}

func TestTightenApproval(t *testing.T) {
	cases := []struct {
		role, global string
		want         string
		byRole       bool
	}{
		{"", "open", "open", false},          // 不声明 = 不收紧(全局为准)
		{"smart", "open", "smart", true},     // 收紧生效
		{"strict", "smart", "strict", true},  // 再紧一档
		{"smart", "strict", "strict", false}, // 全局更严:角色档不生效,也不能放松
		{"smart", "smart", "smart", false},   // 一致:不算"角色收紧"
		{"smart", "", "smart", true},         // 全局档不可知:让角色声明的收紧生效(故障闭合)
		{"不存在的档", "open", "strict", true},    // 非校验路径塞进来的坏值:当成最严,不放行
	}
	for _, c := range cases {
		got, byRole := TightenApproval(c.role, c.global)
		if got != c.want || byRole != c.byRole {
			t.Errorf("TightenApproval(%q, %q) = (%q, %v),期望 (%q, %v)", c.role, c.global, got, byRole, c.want, c.byRole)
		}
	}
}

func TestTightenSandbox(t *testing.T) {
	cases := []struct {
		role, global string
		want         string
		byRole       bool
	}{
		{"", "full-access", "full-access", false},
		{"read-only", "workspace-write", "read-only", true},
		{"workspace-write", "full-access", "workspace-write", true},
		{"workspace-write", "read-only", "read-only", false},
		{"read-only", "", "read-only", true},
		{"乱写的档", "full-access", "read-only", true},
	}
	for _, c := range cases {
		got, byRole := TightenSandbox(c.role, c.global)
		if got != c.want || byRole != c.byRole {
			t.Errorf("TightenSandbox(%q, %q) = (%q, %v),期望 (%q, %v)", c.role, c.global, got, byRole, c.want, c.byRole)
		}
	}
}
