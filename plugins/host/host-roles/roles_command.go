// /role 命令:角色的查看与写操作入口(人发起;模型没有等价工具)。
package hostroles

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// newRoleCommand 构造 /role 命令(写操作二次确认不在此层:命令本身就是用户逐字输入的动作;
// Web 面板走 HTTP 侧确认弹层)。
func newRoleCommand(svc *Service) sdk.CommandSpec {
	return sdk.CommandSpec{
		Name:  "role",
		Usage: "/role list|show|use|none|new|rename|rm [id]",
		Desc:  "角色(人设+规则+技能挂载):切换/查看/新建/删除",
		Args: []sdk.ArgLevel{
			{Options: func([]string) []sdk.Option {
				return []sdk.Option{
					{Value: "list", Desc: "列出全部角色(当前角色标 ★)"},
					{Value: "show", Desc: "查看某个角色详情(身份句/挂载技能/AGENTS.md 路径)"},
					{Value: "use", Desc: "切换角色(下一轮系统提示生效;不换会话)"},
					{Value: "none", Desc: "停用角色,回到基线行为"},
					{Value: "new", Desc: "新建角色(自动生成 AGENTS.md 模板)"},
					{Value: "rename", Desc: "改 ID / 显示名"},
					{Value: "rm", Desc: "删除角色(移入 roles/.trash/,可恢复)"},
				}
			}},
			{Options: func(picked []string) []sdk.Option {
				if len(picked) < 2 {
					return nil
				}
				switch picked[1] {
				case "show", "use", "rm":
					return roleOptions(svc)
				}
				return nil
			}, FreeArgs: func(picked []string) []string {
				if len(picked) < 2 {
					return nil
				}
				switch picked[1] {
				case "new":
					return []string{"角色 ID(小写字母/数字/连字符)", "显示名?"}
				case "rename":
					return []string{"当前 ID", "新 ID(不变则填原 ID)", "新显示名?"}
				}
				return nil
			}},
		},
		Run: func(args []string) (string, error) {
			if len(args) == 0 {
				return roleListText(svc), nil
			}
			switch args[0] {
			case "list":
				return roleListText(svc), nil
			case "show":
				return roleShowText(svc, argAt(args, 1)), nil
			case "use":
				if argAt(args, 1) == "" {
					return "", fmt.Errorf("/role use <id>(可用 /role list 查看)")
				}
				return roleUseText(svc, argAt(args, 1))
			case "none", "off":
				return roleUseText(svc, "")
			case "new":
				if argAt(args, 1) == "" {
					return "", fmt.Errorf("/role new <id> [显示名]")
				}
				if err := roles.ValidateID(argAt(args, 1)); err != nil {
					return "", err
				}
				spec, err := svc.Create(sdk.RoleSpec{ID: argAt(args, 1), Name: argAt(args, 2)}, "")
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已新建角色:%s(%s)\n规则文件:%s\n下一步:用 /role use %s 启用,或在 Web 设置面板「角色」里改 AGENTS.md。",
					spec.Name, spec.ID, roles.AgentsPath(spec.ID), spec.ID), nil
			case "rename":
				if argAt(args, 1) == "" {
					return "", fmt.Errorf("/role rename <当前 ID> <新 ID|同名> [新显示名]")
				}
				newID := argAt(args, 2)
				if newID == "" {
					newID = argAt(args, 1)
				}
				spec, err := svc.Rename(argAt(args, 1), newID, argAt(args, 3))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已更新角色:%s(%s)", spec.Name, spec.ID), nil
			case "rm", "delete":
				if argAt(args, 1) == "" {
					return "", fmt.Errorf("/role rm <id>(删除即移入 roles/.trash/,可用 ls 恢复)")
				}
				if err := svc.Delete(argAt(args, 1)); err != nil {
					return "", err
				}
				return fmt.Sprintf("已删除角色 %s(移入 %s,需要时可手动恢复)", argAt(args, 1), roles.TrashDir()), nil
			default:
				// 省略 "use":/role 财务 = /role use 财务
				return roleUseText(svc, args[0])
			}
		},
	}
}

// argAt 取第 n 个参数(越界 = 空串)。
func argAt(args []string, n int) string {
	if n < len(args) {
		return args[n]
	}
	return ""
}

// roleOptions 现有角色枚举(选择器用)。
func roleOptions(svc *Service) []sdk.Option {
	specs := svc.List()
	out := make([]sdk.Option, 0, len(specs))
	cur := svc.Current()
	for _, spec := range specs {
		mark := ""
		if spec.ID == cur {
			mark = " ★当前"
		}
		out = append(out, sdk.Option{Value: spec.ID, Desc: spec.Name + mark})
	}
	return out
}

// roleListText 角色列表(当前角色标 ★;坏文件与挂载数一并呈现,不静默)。
func roleListText(svc *Service) string {
	specs := svc.List()
	cur := svc.Current()
	var sb strings.Builder
	sb.WriteString("角色($GAH_HOME/roles/):\n")
	if len(specs) == 0 {
		sb.WriteString("- (无角色;用 /role new <id> 新建)\n")
	}
	for _, spec := range specs {
		mark := "  "
		if spec.ID == cur {
			mark = "★ "
		}
		sb.WriteString(fmt.Sprintf("%s%s (%s) — %s\n", mark, spec.Name, spec.ID, firstLine(spec.Description, "无说明")))
		sb.WriteString(fmt.Sprintf("    技能 %d 个", len(spec.EffectiveSkills)))
		if spec.SkillsSet {
			sb.WriteString("(显式挂载")
			if spec.SkillsInherit {
				sb.WriteString("+继承默认池")
			}
			sb.WriteString(")")
		} else {
			sb.WriteString("(默认池)")
		}
		if spec.ExcludeGlobal {
			sb.WriteString(" 不含全局指令")
		}
		if spec.Seed {
			sb.WriteString(" [预置]")
		}
		sb.WriteString("\n")
	}
	if cur == "" {
		sb.WriteString("当前未启用角色(基线行为)。用 /role use <id> 启用。\n")
	}
	if probs := svc.Problems(); len(probs) > 0 {
		sb.WriteString("以下角色目录不可读(需人工修复):\n")
		for _, pb := range probs {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", pb.ID, pb.Err))
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// roleShowText 角色详情(含实际生效的技能清单与 AGENTS.md 路径)。
func roleShowText(svc *Service, id string) string {
	if id == "" {
		id = svc.Current()
	}
	if id == "" {
		return "当前未启用角色。用 /role list 查看可选项。"
	}
	spec, ok := svc.Get(id)
	if !ok {
		return "角色不存在:" + id
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s (%s)", spec.Name, spec.ID))
	if svc.Current() == spec.ID {
		sb.WriteString(" ★当前")
	}
	sb.WriteString("\n")
	if spec.Description != "" {
		sb.WriteString("说明:" + spec.Description + "\n")
	}
	if spec.Identity != "" {
		sb.WriteString("身份句:" + spec.Identity + "\n")
	}
	sb.WriteString(fmt.Sprintf("技能(%d 个):%s\n", len(spec.EffectiveSkills), strings.Join(spec.EffectiveSkills, ", ")))
	if len(spec.OwnSkills) > 0 {
		sb.WriteString("私有技能:" + strings.Join(spec.OwnSkills, ", ") + "(来自 " + roles.SkillsPath(spec.ID) + ")\n")
	}
	sb.WriteString("技能语义:")
	if !spec.SkillsSet {
		sb.WriteString("未写 skills 键 = 使用默认技能池\n")
	} else if spec.SkillsInherit {
		sb.WriteString("挂载清单 = " + strings.Join(spec.Skills, ", ") + " + 默认池\n")
	} else {
		sb.WriteString("挂载清单 = " + strings.Join(spec.Skills, ", ") + "(替换默认池)\n")
	}
	sb.WriteString("全局指令(AGENTS.md):")
	if spec.ExcludeGlobal {
		sb.WriteString("不注入\n")
	} else {
		sb.WriteString("照常注入\n")
	}
	sb.WriteString(fmt.Sprintf("规则文件:%s(%d 字节,上限 %d)\n", roles.AgentsPath(spec.ID), spec.AGENTSBytes, svc.MaxAgentsBytes()))
	if body := strings.TrimSpace(spec.AGENTS); body != "" {
		sb.WriteString("—— 规则正文 ——\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// roleUseText 切换回执(讲清"立即生效的是什么、不变的是什么")。
// "none"/"off" 的停用别名交给 Service.Use 统一处理(只在没有同名角色时生效)。
func roleUseText(svc *Service, id string) (string, error) {
	if id == "" {
		if svc.Current() == "" {
			return "当前已是基线(未启用角色)。", nil
		}
		if err := svc.Use(""); err != nil {
			return "", err
		}
		return "已停用角色,回到基线行为。\n生效范围:下一轮的系统提示与技能索引;**会话与历史不变**。", nil
	}
	prev := svc.Current()
	if err := svc.Use(id); err != nil {
		return "", err
	}
	spec, _ := svc.Get(id)
	if prev == id {
		return fmt.Sprintf("已是当前角色:%s(%s),无需切换。", spec.Name, spec.ID), nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("已切换到角色:%s(%s)\n", spec.Name, spec.ID))
	sb.WriteString(fmt.Sprintf("技能可见 %d 个:%s\n", len(spec.EffectiveSkills), strings.Join(topN(spec.EffectiveSkills, 8), ", ")))
	sb.WriteString("生效范围:下一轮的系统提示与技能索引(已切换即生效,无需重启)。\n")
	sb.WriteString("说明:角色切换**不换会话**、不改工作区;因此本轮 Prompt 缓存失效,首轮响应可能变慢。\n")
	sb.WriteString("需要换工作区(会换会话)请用 /workspace。")
	return sb.String(), nil
}

// topN 取前 n 项(超出补 "…")。
func topN(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	out := append([]string(nil), items[:n]...)
	return append(out, "…")
}

// firstLine 取首行/缺省值(列表展示用)。
func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
