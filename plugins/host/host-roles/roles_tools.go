// 角色只读工具:模型可自查角色,但**没有**任何写工具(见包注释的安全边界)。
package hostroles

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// listRoles 工具:列出角色与当前角色。
type listRoles struct {
	svc *Service
}

func (t *listRoles) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "list_roles",
		Description: "列出可用的角色(人设+工作规则+技能挂载)与当前角色。角色由用户在 /role 或设置面板切换,你不能自行切换。",
		InputSchema: map[string]any{"type": "object"},
		// 无路径参数:显式声明,免被宿主按参数名/值推断误判为路径调用。
		PathParamsDeclared: true,
	}
}

func (t *listRoles) Execute(ctx context.Context, args string) (any, error) {
	cur := t.svc.Current()
	items := []map[string]any{}
	for _, spec := range t.svc.List() {
		items = append(items, map[string]any{
			"id": spec.ID, "name": spec.Name, "description": spec.Description,
			"current": spec.ID == cur,
		})
	}
	out := map[string]any{"current": cur, "roles": items}
	if len(t.svc.Problems()) > 0 {
		bad := make([]string, 0, len(t.svc.Problems()))
		for _, pb := range t.svc.Problems() {
			bad = append(bad, pb.ID+": "+pb.Err)
		}
		out["unreadable"] = bad
	}
	return out, nil
}

// readRole 工具:读一个角色的完整定义(身份句 + 工作规则 + 技能挂载)。
type readRole struct {
	svc *Service
}

func (t *readRole) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "read_role",
		Description: "读取一个角色的完整设定(身份句、工作规则正文、技能挂载)。不给 id 时读当前角色。只读。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "角色 ID(缺省 = 当前角色)"},
			},
		},
		PathParamsDeclared: true, // id 是角色标识,不是文件路径
	}
}

func (t *readRole) Execute(ctx context.Context, args string) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return nil, fmt.Errorf("read_role: args: %w", err)
		}
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		id = t.svc.Current()
		if id == "" {
			return map[string]any{"error": "当前未启用角色(可用 list_roles 查看有哪些角色)"}, nil
		}
	}
	spec, ok := t.svc.Get(id)
	if !ok {
		return map[string]any{"error": "角色不存在: " + id}, nil
	}
	return map[string]any{
		"id": spec.ID, "name": spec.Name, "description": spec.Description,
		"identity": spec.Identity, "rules": spec.AGENTS,
		"skills": spec.EffectiveSkills, "private_skills": spec.OwnSkills,
		"exclude_global_instructions": spec.ExcludeGlobal,
		"rules_path":                  roles.AgentsPath(spec.ID),
		"current":                     t.svc.Current() == spec.ID,
	}, nil
}
