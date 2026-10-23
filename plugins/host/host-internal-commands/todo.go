package hostintcmd

// /todo 命令(D2 的剩余一半 · 2026-10-23):把"待办到哪儿了"从聊天记录里搬到看得见的地方。
//
// 现状:Web 有任务看板(`GET /api/todo`),TUI **没有入口** —— 人只能问模型"还有几条没做完"。
// 计划(D2)当时把这一半留着没做,理由是"宿主要读待办就得共享文件格式,那是独立一批的
// 机械重构"。**这个判断现在只需最小实现**:存储本来就是追加式 jsonl
// (`$GAH_HOME/todos/<project-key>.jsonl`),读侧已收口到 internal/todostore
// (tool-todo 也改成用它)—— 宿主只需**读同一份文件**。
//
// 三条口径(与 /plan 同款):
//   - **只读**:状态机仍在工具侧;本命令不改任何东西,也不经 ctx.tools.Execute
//     走工具流水线(那会触发审批、并往会话里塞一条"模型没请求过的"工具结果);
//   - **不静默**:没建过单 ⇒ 明确说"暂无待办";读文件失败 ⇒ 报错(不装作空);
//   - **不发模型请求**:纯本地读文件。

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/todostore"
)

// cmdTodo /todo [pending|in_progress|completed|all] | /todo <任务id>
func (h *Host) cmdTodo(args []string) (string, error) {
	path := todostore.Path("")
	tasks := todostore.Load(path)
	// 详情:参数不是状态词 ⇒ 当作任务 id。
	if len(args) > 0 && !isTodoStatus(args[0]) {
		tk, ok := todostore.Find(tasks, args[0])
		if !ok {
			return "", errString("待办 " + args[0] + " 不存在(工作项目录:" + todostore.Dir("") + ")")
		}
		return todoDetailText(tk), nil
	}
	status := ""
	if len(args) > 0 {
		status = args[0]
		if status == "all" {
			status = ""
		}
	}
	views := todostore.Views(tasks, status)
	if len(views) == 0 {
		if status != "" {
			return "暂无「" + todoStatusText(status) + "」的待办", nil
		}
		return "暂无待办(模型建单后会出现在这里;存储:" + path + ")", nil
	}
	var b strings.Builder
	for i, v := range views {
		if i > 0 {
			b.WriteString("\n")
		}
		// 进行中优先显示 activeForm(它写的是"正在做什么",比标题更像进度)
		label := v.Subject
		if v.Status == todostore.StatusInProgress && v.ActiveForm != "" {
			label = v.ActiveForm
		}
		fmt.Fprintf(&b, "%s [%s] %s", todoMark(v.Status), todoStatusText(v.Status), todostore.Trim(label, 80))
		if len(v.BlockedBy) > 0 && v.Status != todostore.StatusCompleted {
			fmt.Fprintf(&b, " · 等 %d 项前置", len(v.BlockedBy))
		}
	}
	// 收尾给完成度:一眼看出还剩多少(与状态栏「计划 n/m」同款信息密度)
	done, total := todoCounts(views)
	fmt.Fprintf(&b, "\n%d/%d 已完成 · 详情 /todo <id>", done, total)
	return b.String(), nil
}

// todoDetailText 一条任务的明细(描述/依赖/元数据)。
func todoDetailText(tk todostore.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %s\n", tk.ID, todoStatusText(todostore.StatusOf(tk)), todostore.Trim(tk.Subject, 200))
	if tk.ActiveForm != "" {
		fmt.Fprintf(&b, "进行时:%s\n", tk.ActiveForm)
	}
	if tk.Description != "" {
		fmt.Fprintf(&b, "描述:%s\n", todostore.Trim(tk.Description, 400))
	}
	if tk.Owner != "" {
		fmt.Fprintf(&b, "负责人:%s\n", tk.Owner)
	}
	if len(tk.BlockedBy) > 0 {
		fmt.Fprintf(&b, "依赖:%s\n", strings.Join(tk.BlockedBy, ", "))
	}
	if len(tk.Metadata) > 0 {
		fmt.Fprintf(&b, "元数据:%d 项", len(tk.Metadata))
	}
	return strings.TrimRight(b.String(), "\n")
}

// todoCounts 已完成/总数(墓碑不计)。
func todoCounts(views []todostore.View) (done, total int) {
	for _, v := range views {
		total++
		if v.Status == todostore.StatusCompleted {
			done++
		}
	}
	return done, total
}

// isTodoStatus 参数是否是合法状态词(否则当任务 id)。
func isTodoStatus(s string) bool {
	switch s {
	case "all", todostore.StatusPending, todostore.StatusInProgress, todostore.StatusCompleted:
		return true
	}
	return false
}

// todoStatusText 状态的人话(与 Web 看板同款词)。
func todoStatusText(s string) string {
	switch s {
	case todostore.StatusPending:
		return "待办"
	case todostore.StatusInProgress:
		return "进行中"
	case todostore.StatusCompleted:
		return "已完成"
	case todostore.StatusDeleted:
		return "已删除"
	case "":
		return "未知"
	default:
		return s
	}
}

// todoMark 状态标记(与 /plan 的步骤标记同族)。
func todoMark(s string) string {
	switch s {
	case todostore.StatusCompleted:
		return "✓"
	case todostore.StatusInProgress:
		return "→"
	default:
		return "·"
	}
}
