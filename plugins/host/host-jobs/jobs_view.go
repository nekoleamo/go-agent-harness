// jobs_view.go S-P0-3:/jobs 统一视图(后台任务 + 子代理)。
//
// 归属说明:/jobs 命令的单一事实源在 host-jobs(它拥有 ctx.jobs),本文件把视图
// 扩展到**可选**注入的 ctx.fanout(host-fanout 未装配时自动退化为纯任务视图)。
// 拆成独立文件是为了:纯函数(排序/耗时/截断/id 枚举)可单测,不依赖具体 Jobs 实现。
//
// 关联:TUI 状态栏常驻折叠行见 tui/dock.go(同一数据源,只读计数);
// Web 端 JobsPanel 仍按原样读 ctx.jobs(任务明细),子代理面随后续里程碑接入。
package hostjobs

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// jobRow 统一视图的一行(任务或子代理)。
type jobRow struct {
	kind    string // job | agent
	id      string
	state   string
	summary string
	created time.Time
	done    time.Time
}

// collectRows 汇总任务与子代理:运行中优先,其次按创建时间倒序(新在前)。
// fo 可为 nil(host-fanout 未装配)→ 仅任务。
func collectRows(js sdk.JobService, fo sdk.FanoutService) []jobRow {
	var rows []jobRow
	if js != nil {
		for _, j := range js.List() {
			sum := j.Command
			if sum == "" {
				// 函数任务(workflow background 等):无命令行,用结果做摘要更有信息量
				if j.Result != nil {
					sum = fmt.Sprintf("%v", j.Result)
				} else {
					sum = "(函数任务)"
				}
			}
			if j.Error != "" {
				sum += " ← " + j.Error
			}
			rows = append(rows, jobRow{
				kind: "job", id: j.ID, state: string(j.State), summary: sum,
				created: j.CreatedAt, done: j.DoneAt,
			})
		}
	}
	if fo != nil {
		for _, a := range fo.ListAgents() {
			sum := a.Input
			if a.Error != "" {
				sum += " ← " + a.Error
			}
			rows = append(rows, jobRow{
				kind: "agent", id: a.ID, state: string(a.State), summary: sum,
				created: a.CreatedAt,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rows[i].state == "running", rows[j].state == "running"
		if ri != rj {
			return ri
		}
		return rows[i].created.After(rows[j].created)
	})
	return rows
}

// rowDuration 行耗时:已完成用 Done-Created,运行中用 now-Created,不可知为 "-"。
func rowDuration(r jobRow) string {
	if r.created.IsZero() {
		return "-"
	}
	end := r.done
	if end.IsZero() {
		if r.state != "running" {
			return "-"
		}
		end = time.Now()
	}
	if end.Before(r.created) {
		return "-"
	}
	return humanDur(end.Sub(r.created))
}

// humanDur 紧凑时长(与 TUI 状态栏/会话速览同口径)。
func humanDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// jobsListView /jobs 命令的列表视图文本(running 优先;超 12 行走省略行;附用法提示)。
// (模型工具 job_list 的结构化视图另见 jobsView([]sdk.Job),两者不重叠)
func jobsListView(js sdk.JobService, fo sdk.FanoutService) string {
	rows := collectRows(js, fo)
	if len(rows) == 0 {
		return "后台任务与子代理:均无记录。"
	}
	running := 0
	for _, r := range rows {
		if r.state == "running" {
			running++
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "后台任务与子代理:%d 运行中 / %d 条记录\n", running, len(rows))
	const maxRows = 12
	for i, r := range rows {
		if i >= maxRows {
			fmt.Fprintf(&sb, "  …另 %d 条\n", len(rows)-maxRows)
			break
		}
		fmt.Fprintf(&sb, "  [%s] %-14s %-7s %6s  %s\n",
			r.kind, clip(r.id, 14), r.state, rowDuration(r), clip(r.summary, 60))
	}
	sb.WriteString("用法:/jobs output <id> 看输出;/jobs kill <id> 终止(running 才有意义);TUI 中 F6 展开实时坞(↑/↓ 选择、Enter 看输出、s 定向、x 停止)")
	return sb.String()
}

// jobsOutputView 单个任务/子代理输出(任务优先,再回退子代理)。
func jobsOutputView(js sdk.JobService, fo sdk.FanoutService, id string) (string, error) {
	if js != nil {
		if j, ok := js.Output(id); ok {
			var sb strings.Builder
			fmt.Fprintf(&sb, "任务 %s(状态 %s", j.ID, j.State)
			if d := rowDuration(jobRow{state: string(j.State), created: j.CreatedAt, done: j.DoneAt}); d != "-" {
				fmt.Fprintf(&sb, ",耗时 %s", d)
			}
			sb.WriteString(")\n")
			if j.Command != "" {
				fmt.Fprintf(&sb, "命令: %s\n", clip(j.Command, 200))
			}
			if j.Error != "" {
				fmt.Fprintf(&sb, "错误: %s\n", clip(j.Error, 400))
			}
			text := j.Output
			if text == "" && j.Result != nil {
				text = fmt.Sprintf("%v", j.Result)
			}
			if strings.TrimSpace(text) == "" {
				sb.WriteString("(暂无输出)")
			} else {
				sb.WriteString(tailRunes(text, 6000))
			}
			return sb.String(), nil
		}
	}
	if fo != nil {
		if a, ok := fo.AgentStatus(id); ok {
			var sb strings.Builder
			fmt.Fprintf(&sb, "子代理 %s(状态 %s)\n", a.ID, a.State)
			fmt.Fprintf(&sb, "任务: %s\n", clip(a.Input, 200))
			if a.Error != "" {
				fmt.Fprintf(&sb, "错误: %s\n", clip(a.Error, 400))
			}
			if strings.TrimSpace(a.Result) != "" {
				fmt.Fprintf(&sb, "结果:\n%s\n", tailRunes(a.Result, 4000))
			}
			const maxMsg = 8
			if n := len(a.Messages); n > 0 {
				shown := n
				if shown > maxMsg {
					shown = maxMsg
				}
				fmt.Fprintf(&sb, "对话(%d 条,末 %d):\n", n, shown)
				for _, m := range a.Messages[n-shown:] {
					fmt.Fprintf(&sb, "  %s: %s\n", m.From, clip(m.Content, 160))
				}
			}
			if strings.TrimSpace(a.Result) == "" && len(a.Messages) == 0 {
				sb.WriteString("(暂无输出)")
			}
			return sb.String(), nil
		}
	}
	return "", errString("未找到任务/子代理:" + id)
}

// jobsKillView 终止任务(任务优先,再回退子代理;两者皆无此 id → 显式报错)。
func jobsKillView(js sdk.JobService, fo sdk.FanoutService, id string) (string, error) {
	if js != nil {
		if _, ok := js.Output(id); ok {
			if err := js.Kill(id); err != nil {
				return "", errString(err.Error())
			}
			return "已终止任务 " + id, nil
		}
	}
	if fo != nil {
		if _, ok := fo.AgentStatus(id); ok {
			if err := fo.KillAgent(id); err != nil {
				return "", errString(err.Error())
			}
			return "已终止子代理 " + id, nil
		}
	}
	return "", errString("未找到任务/子代理:" + id)
}

// jobsIDOptions 二级枚举:output 列全部(含子代理),kill 只列运行中。
func jobsIDOptions(js sdk.JobService, fo sdk.FanoutService, picked []string) []sdk.Option {
	if len(picked) < 2 || picked[1] == "list" {
		return nil // list 无二级 → 选完直接执行
	}
	rows := collectRows(js, fo)
	opts := make([]sdk.Option, 0, len(rows))
	for _, r := range rows {
		if picked[1] == "kill" && r.state != "running" {
			continue
		}
		desc := r.summary
		if r.kind == "agent" {
			desc = "子代理 · " + desc
		}
		opts = append(opts, sdk.Option{Value: r.id, Desc: desc})
	}
	return opts
}

// clip 按 rune 截断(状态/汇总列;超长加省略号)。
func clip(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// tailRunes 取末尾 n 个 rune(输出可能很长,保留最近内容更有用)。
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return fmt.Sprintf("…(截断前 %d 字符)\n", len(r)-n) + string(r[len(r)-n:])
}
