// command.go:/schedule 命令(list / add / rm / on / off / run)。
//
// 参数设计:命令行里的空格会把 cron 拆散,故 add 的解析对两种形态都容忍 ——
// 「cron 作为一个参数」(选择器逐步向导给的是整行)与「cron 被空格拆成 5 个参数」
// (用户直接手输整条命令)。两种情况都能正确还原,见 parseAddArgs。
package hostschedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// scheduleCommand 构造 /schedule 命令(注册进 ctx.commands;卸载随 Disposer 撤销)。
func scheduleCommand(s *Scheduler) sdk.CommandSpec {
	return sdk.CommandSpec{
		Name:  "schedule",
		Usage: "/schedule list|add <cron> <任务描述>|rm|on|off|run <id>",
		Desc:  "定时任务(到点自动跑一轮;无人值守,危险动作一律拒绝)",
		Run:   func(args []string) (string, error) { return scheduleCmd(s, args) },
		Args: []sdk.ArgLevel{
			{Options: func([]string) []sdk.Option {
				return []sdk.Option{
					{Value: "list", Desc: "列出全部计划"},
					{Value: "add", Desc: "新增计划(cron 5 字段 + 任务描述)"},
					{Value: "rm", Desc: "删除计划"},
					{Value: "on", Desc: "启用计划"},
					{Value: "off", Desc: "停用计划"},
					{Value: "run", Desc: "立即运行一次"},
				}
			}},
			{
				// 二级:rm/on/off/run 枚举计划 ID;add/list 该级留空(add 走 FreeArgs)
				Options: func(picked []string) []sdk.Option {
					if len(picked) < 2 {
						return nil
					}
					switch picked[1] {
					case "rm", "on", "off", "run":
						var out []sdk.Option
						for _, p := range s.List() {
							out = append(out, sdk.Option{Value: p.ID, Desc: p.Name})
						}
						return out
					}
					return nil
				},
				FreeArgs: func(picked []string) []string {
					if len(picked) >= 2 && picked[1] == "add" {
						return []string{"cron 表达式(分 时 日 月 周)", "任务描述"}
					}
					return nil
				},
			},
		},
	}
}

// scheduleCmd /schedule 命令实现(输出多行文本由 TUI/Web 显示)。
func scheduleCmd(s *Scheduler, args []string) (string, error) {
	if len(args) == 0 {
		return scheduleList(s), nil
	}
	switch args[0] {
	case "list":
		return scheduleList(s), nil
	case "add":
		cron, prompt, err := parseAddArgs(args[1:])
		if err != nil {
			return "", err
		}
		p, err := s.Add(sdk.Schedule{Name: autoName(prompt), Cron: cron, Prompt: prompt, Enabled: true})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已创建计划 %s(%s)\n下次触发:%s\n到点自动运行;无人值守:需审批的危险动作一律拒绝。",
			p.ID, cronLabel(p.Cron), formatTime(p.NextRun)), nil
	case "rm":
		id, err := needID(args[1:], "rm")
		if err != nil {
			return "", err
		}
		if err := s.Remove(id); err != nil {
			return "", err
		}
		return "已删除计划 " + id, nil
	case "on", "off":
		id, err := needID(args[1:], args[0])
		if err != nil {
			return "", err
		}
		enabled := args[0] == "on"
		cur, ok := findPlan(s, id)
		if !ok {
			return "", fmt.Errorf("计划不存在: %s", id)
		}
		cur.Enabled = enabled
		upd, err := s.Update(cur)
		if err != nil {
			return "", err
		}
		if !enabled {
			return "已停用计划 " + id, nil
		}
		return fmt.Sprintf("已启用计划 %s\n下次触发:%s", id, formatTime(upd.NextRun)), nil
	case "run":
		id, err := needID(args[1:], "run")
		if err != nil {
			return "", err
		}
		if err := s.RunNow(id); err != nil {
			return "", err
		}
		return "已触发 " + id + "(后台运行,产出见会话流)", nil
	default:
		return "", fmt.Errorf("未知子命令 %q;用法:/schedule list|add|rm|on|off|run", args[0])
	}
}

// parseAddArgs 还原 add 的 cron 与任务描述(兼容「cron 一个参数」与「被空格拆散」两种形态)。
func parseAddArgs(args []string) (cron, prompt string, err error) {
	const usage = "用法:/schedule add <cron 5 字段> <任务描述>,如 /schedule add 0 8 * * * 生成昨日对账"
	if len(args) == 0 {
		return "", "", fmt.Errorf("%s(缺少 cron 与任务描述)", usage)
	}
	// 形态一:cron 作为一个参数(选择器逐步向导的整行输入)
	if _, perr := parseCron(args[0]); perr == nil {
		if len(args) < 2 || strings.TrimSpace(strings.Join(args[1:], " ")) == "" {
			return "", "", fmt.Errorf("%s(缺少任务描述)", usage)
		}
		return args[0], strings.Join(args[1:], " "), nil
	}
	// 形态二:cron 被空格拆成 5 个参数(用户直接手输整条命令)
	if len(args) >= 6 {
		c := strings.Join(args[:5], " ")
		if _, perr := parseCron(c); perr == nil {
			return c, strings.Join(args[5:], " "), nil
		}
	}
	if len(args) < 6 {
		return "", "", fmt.Errorf("%s(cron 要 5 个字段:分 时 日 月 周)", usage)
	}
	return "", "", fmt.Errorf("%s(cron 表达式非法:%s)", usage, strings.Join(args[:5], " "))
}

// needID 取子命令的计划 ID。
func needID(args []string, sub string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("用法:/schedule %s <id>(id 见 /schedule list)", sub)
	}
	return args[0], nil
}

// findPlan 按 ID 取计划快照。
func findPlan(s *Scheduler, id string) (sdk.Schedule, bool) {
	for _, p := range s.List() {
		if p.ID == id {
			return p, true
		}
	}
	return sdk.Schedule{}, false
}

// scheduleList 计划列表文本。
func scheduleList(s *Scheduler) string {
	plans := s.List()
	if len(plans) == 0 {
		return "还没有定时计划。\n新增:/schedule add <cron 5 字段> <任务描述>,如 /schedule add 0 8 * * * 生成昨日对账\n(或在 Web 设置面板「计划」里添加)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "定时计划 %d 条(时间按本机时区;无人值守:需审批的危险动作一律拒绝):", len(plans))
	for _, p := range plans {
		state := "启用"
		if !p.Enabled {
			state = "停用"
		}
		fmt.Fprintf(&b, "\n  [%s] %s  · %s\n    cron %s(%s)  下次 %s",
			p.ID, p.Name, state, p.Cron, cronLabel(p.Cron), formatTime(p.NextRun))
		if !p.LastRunAt.IsZero() {
			fmt.Fprintf(&b, "  上次 %s", formatTime(p.LastRunAt))
			if p.LastStatus != "" {
				fmt.Fprintf(&b, " %s", p.LastStatus)
			}
			if p.LastError != "" {
				fmt.Fprintf(&b, "(%s)", truncateRunes(p.LastError, 60))
			}
		}
		fmt.Fprintf(&b, "\n    %s", truncateRunes(p.Prompt, 60))
	}
	return b.String()
}

// cronLabel 表达式 + 人话简写(无法简写则只有表达式)。
func cronLabel(expr string) string {
	if h := cronHuman(expr); h != "" {
		return expr + "," + h
	}
	return expr
}

// autoName 从任务描述派生默认计划名(前 20 字)。
func autoName(prompt string) string {
	return truncateRunes(strings.TrimSpace(prompt), 20)
}

// truncateRunes 按 rune 截断(超长加省略号)。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// formatTime 人读时间(本机时区);零值 = 「—」(无下次触发)。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}
