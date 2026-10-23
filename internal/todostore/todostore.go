// Package todostore 任务清单的**存储格式单一事实源**(读侧)。
//
// 为什么抽出来(2026-10-23,D2 的 /todo 收口):任务清单的存储是
// `$GAH_HOME/todos/<project-key>.jsonl`(追加式,同 id 最后一行胜)。
// 写侧一直是 tool-todo(发行态由外部进程 extplugins/tool-kit 提供),
// 但**读侧**此前只有工具自己 —— 于是 Web 有看板、TUI 只能问模型"待办到哪儿了"。
//
// 让宿主也读同一份文件的最低风险做法**不是**再写一遍解析(两份解析必然漂移:
// 坏行容忍口径、同 id 最后一行胜、状态字段名……迟早对不上),而是把读侧
// (类型 + 路径派生 + 重放 + 视图排序)收进 internal/,写侧(tool-todo)与
// 只读消费方(host-internal-commands 的 /todo)共用同一份实现。
//
// 本包**只读**:不提供任何写方法(写仍归 tool-todo)。这样消费方拿不到
// "顺手改一下"的能力,也就不会把状态机从两处绕过去。
package todostore

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 任务状态(4 状态机)。变更仅经专用 action,状态字符串即持久化字段。
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusDeleted    = "deleted"
)

// Task 一条任务(jsonl 每行一个 Task;同 id 最后一行为准)。
//
// 字段名与 json tag 是**落盘契约**:外部进程写的文件由宿主读,任何一方改名
// 都会让另一侧静默读空(坏行容忍会把不认识的字段丢掉而不报错)。
type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description,omitempty"`
	ActiveForm  string         `json:"activeForm,omitempty"` // 进行时标签(in_progress 显示)
	Owner       string         `json:"owner,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	BlockedBy   []string       `json:"blockedBy,omitempty"` // 依赖任务 id(全部须 completed 才能 start)
	Status      string         `json:"status"`
	TS          int64          `json:"ts"`
	Deleted     bool           `json:"del,omitempty"` // 墓碑(deleted 状态冗余标记,便于人工核对)
}

// View 列表摘要(精简字段,description/metadata 留给详情)。
type View struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Subject    string   `json:"subject"`
	ActiveForm string   `json:"activeForm,omitempty"`
	BlockedBy  []string `json:"blockedBy,omitempty"`
	TS         int64    `json:"ts"`
}

// Dir 任务目录(root 为空 ⇒ $GAH_HOME/todos)。
func Dir(root string) string {
	if root != "" {
		return root
	}
	return filepath.Join(sdk.Home(), "todos")
}

// Path 落盘路径:root 注入 > $GAH_HOME/todos;<project-key>.jsonl。
func Path(root string) string {
	return filepath.Join(Dir(root), sdk.ProjectKeyFromCwd()+".jsonl")
}

// Load 重放文件:逐行解析,同 id 最后一行胜(墓碑替换);坏行/注释行容忍跳过。
// 无文件 = 空(不是错误:干净数据根下还没建过单)。
func Load(path string) []Task {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	byID := map[string]Task{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] == '#' {
			continue // 空行/人工注释
		}
		var tk Task
		if err := json.Unmarshal(line, &tk); err != nil || tk.ID == "" || tk.Status == "" {
			continue // 坏行:容忍跳过
		}
		if _, seen := byID[tk.ID]; !seen {
			order = append(order, tk.ID)
		}
		byID[tk.ID] = tk
	}
	out := make([]Task, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// Find 在重放结果中找任务(按 id;deleted 墓碑仍返回,便于状态判断)。
func Find(tasks []Task, id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// Views 摘要视图(status 过滤;排序:进行中 > 待办 > 已完成,组内创建倒序)。
func Views(tasks []Task, status string) []View {
	weight := map[string]int{StatusInProgress: 0, StatusPending: 1, StatusCompleted: 2}
	var out []View
	for _, t := range tasks {
		if t.Deleted {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		out = append(out, View{ID: t.ID, Status: t.Status, Subject: t.Subject,
			ActiveForm: t.ActiveForm, BlockedBy: append([]string(nil), t.BlockedBy...), TS: t.TS})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if weight[out[i].Status] != weight[out[j].Status] {
			return weight[out[i].Status] < weight[out[j].Status]
		}
		return out[i].TS > out[j].TS
	})
	return out
}

// List 读 + 过滤 + 排序(只读消费方的一步到位入口)。
func List(root, status string) []View {
	return Views(Load(Path(root)), status)
}

// StatusOf 状态显示(悬空引用/缺失任务提示用)。
func StatusOf(t Task) string {
	if t.Deleted {
		return StatusDeleted
	}
	return t.Status
}

// Trim 裁到 n 字(不切多字节;调用方展示用)。
func Trim(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
