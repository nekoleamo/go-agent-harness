// Package tooltodo 提供 tool-todo 插件(M8):模型向任务清单工具。
// 复杂多步任务(3+ 步/并行修改/评审校验)先建单、执行中推进、完成立即销单。
//
// 状态机(4 态):pending → in_progress → completed + deleted 坟墓;
// 状态变更仅 start/complete/pend/delete 四专用 action(禁 update 改状态)。
// 约束:同一时刻仅一个 in_progress;blockedBy 依赖成环/悬空引用被拒,
// start 前全部依赖须 completed;delete 被依赖引用时拒绝(防悬空)。
//
// 存储:$GAH_HOME/todos/<project-key>.jsonl —— 追加式 jsonl(变更即追加该任务
// 新版本行,同 id 最后一行胜;delete 追加墓碑),坏行容忍,人工可编辑
// (与 memory/session 同三特性)。项目 key 用 sdk.ProjectKey(共享派生)。
package tooltodo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 任务状态(4 状态机)。变更仅经专用 action,状态字符串即持久化字段。
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusDeleted    = "deleted"
)

// validTransitions action → 允许的起始状态(start 前置状态机)。
var startableFrom = map[string]string{
	"start":    StatusPending, // 仅 pending 可 start(已在做/已完成/已删除均拒绝)
	"pend":     StatusInProgress,
	"complete": StatusPending, // complete 从 pending(快速销单)或 in_progress
}

// Task 一条任务(jsonl 每行一个 Task;同 id 最后一行为准)。
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

// View list 摘要(精简字段,description/metadata 留给 get)。
type View struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Subject    string   `json:"subject"`
	ActiveForm string   `json:"activeForm,omitempty"`
	BlockedBy  []string `json:"blockedBy,omitempty"`
	TS         int64    `json:"ts"`
}

// Tool 实现 sdk.Tool(单工具多 action)。
type Tool struct {
	store *Store
}

// NewTool 外部化工厂。
func NewTool() sdk.Tool {
	return &Tool{store: NewStore("")}
}

// Plugin 实现 tool-todo(双轨:装配进宿主时经 ctx.tools 注册)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-todo" }

func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(NewTool()), nil
}

func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "todo",
		Description: "任务清单(复杂多步任务先建单、执行中推进、完成立即销单)。状态机 pending→in_progress→completed," +
			"deleted 为坟墓;状态变更只用专用 action:start(id,activeForm?)/complete(id)/pend(id)/delete(id),update 不改状态。" +
			"约束:同一时刻仅一个 in_progress(start 前须先 pend/complete 当前进行项);blockedBy 依赖(create/update 可带," +
			"update 用 addBlockedBy/removeBlockedBy 增删)——成环与悬空引用被拒,start 前全部依赖须 completed," +
			"被依赖任务不可 delete。list(status?)看摘要(get 详情)。落盘跨会话保留、人工可编辑。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action":          map[string]any{"type": "string", "enum": []string{"create", "start", "complete", "pend", "delete", "update", "list", "get"}},
				"id":              map[string]any{"type": "string", "description": "start/complete/pend/delete/update/get 目标任务"},
				"subject":         map[string]any{"type": "string", "description": "create:祈使句短行(必填)"},
				"description":     map[string]any{"type": "string"},
				"activeForm":      map[string]any{"type": "string", "description": "进行时标签(如 '编写单测')"},
				"owner":           map[string]any{"type": "string"},
				"status":          map[string]any{"type": "string", "description": "list 过滤"},
				"blockedBy":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "create:初始依赖"},
				"addBlockedBy":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "update:新增依赖"},
				"removeBlockedBy": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "update:移除依赖"},
			},
		},
	}
}

// args 统一入参。
type args struct {
	Action          string         `json:"action"`
	ID              string         `json:"id"`
	Subject         string         `json:"subject"`
	Description     string         `json:"description"`
	ActiveForm      string         `json:"activeForm"`
	Owner           string         `json:"owner"`
	Status          string         `json:"status"`
	BlockedBy       []string       `json:"blockedBy"`
	AddBlockedBy    []string       `json:"addBlockedBy"`
	RemoveBlockedBy []string       `json:"removeBlockedBy"`
	Metadata        map[string]any `json:"metadata"`
}

// Execute 执行一个 action;业务失败以 map{"error":...} 回传模型(不中断 turn)。
func (t *Tool) Execute(_ context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("todo: args: %w", err)
	}
	st := t.store
	switch a.Action {
	case "create":
		if strings.TrimSpace(a.Subject) == "" {
			return map[string]any{"error": "todo: create 需 subject(祈使句短行)"}, nil
		}
		tk, err := st.Create(strings.TrimSpace(a.Subject), a.Description, a.ActiveForm, a.Owner, a.Metadata, a.BlockedBy)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": tk.ID, "status": tk.Status}, nil
	case "start":
		tk, err := st.Transit("start", a.ID, a.ActiveForm)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": tk.ID, "status": tk.Status}, nil
	case "complete":
		if tk, err := st.Transit("complete", a.ID, ""); err != nil {
			return map[string]any{"error": err.Error()}, nil
		} else {
			return map[string]any{"id": tk.ID, "status": tk.Status}, nil
		}
	case "pend":
		if tk, err := st.Transit("pend", a.ID, ""); err != nil {
			return map[string]any{"error": err.Error()}, nil
		} else {
			return map[string]any{"id": tk.ID, "status": tk.Status}, nil
		}
	case "delete":
		if err := st.Delete(a.ID); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"deleted": a.ID}, nil
	case "update":
		tk, err := st.Update(a.ID, a.Subject, a.Description, a.ActiveForm, a.Owner, a.Metadata, a.AddBlockedBy, a.RemoveBlockedBy)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": tk.ID, "status": tk.Status}, nil
	case "list":
		return st.List(a.Status), nil
	case "get":
		tk, ok := st.Get(a.ID)
		if !ok {
			return map[string]any{"error": fmt.Sprintf("todo: 无此任务 %q", a.ID)}, nil
		}
		return tk, nil
	default:
		return map[string]any{"error": fmt.Sprintf("todo: 未知 action %q(create|start|complete|pend|delete|update|list|get)", a.Action)}, nil
	}
}

// Store 任务存储:追加式 jsonl + 同 id 最后一行胜,坏行容忍,人工可编辑。
// 每写操作:锁内重放文件(权威状态)→ 状态机/依赖校验 → 追加变更行。
type Store struct {
	mu   sync.Mutex
	root string // 空 = 默认 $GAH_HOME/todos
}

// NewStore 构造存储。
func NewStore(root string) *Store { return &Store{root: root} }

// Create 建单:subject 必填,blockedBy 引用必须存在且未删除。返回新任务。
func (s *Store) Create(subject, desc, activeForm, owner string, meta map[string]any, blockedBy []string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Task{}, err
	}
	tasks := s.loadLocked(path)
	if err := s.validateRefs(tasks, "", blockedBy); err != nil {
		return Task{}, err
	}
	tk := Task{ID: s.genID(path), Subject: subject, Description: desc,
		ActiveForm: activeForm, Owner: owner, Metadata: meta,
		BlockedBy: append([]string(nil), blockedBy...), Status: StatusPending,
		TS: time.Now().Unix()}
	if err := appendLine(path, tk); err != nil {
		return Task{}, err
	}
	return tk, nil
}

// Transit 状态机推进:start(pending→in_progress,依赖须全 completed、单 in_progress)、
// complete(pending/in_progress→completed)、pend(in_progress→pending)。
func (s *Store) Transit(action, id, activeForm string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Task{}, err
	}
	tasks := s.loadLocked(path)
	tk, ok := findTask(tasks, id)
	if !ok {
		return Task{}, fmt.Errorf("todo: 无此任务 %q", id)
	}
	if from, _ := startableFrom[action]; action != "complete" && tk.Status != from {
		return Task{}, fmt.Errorf("todo: %s 需任务处于 %s(当前 %s)", action, from, tk.Status)
	}
	switch action {
	case "start":
		// 依赖检查:全部须 completed
		for _, b := range tk.BlockedBy {
			bt, ok := findTask(tasks, b)
			if !ok || bt.Deleted || bt.Status != StatusCompleted {
				return Task{}, fmt.Errorf("todo: 依赖 %q 未完成(%s),不可 start", b, statusOf(bt))
			}
		}
		// 单 in_progress:存在其它进行项则拒绝
		for _, o := range tasks {
			if o.Deleted || o.ID == id {
				continue
			}
			if o.Status == StatusInProgress {
				return Task{}, fmt.Errorf("todo: 已有进行项 %q,先 pend/complete 它再 start", o.ID)
			}
		}
		tk.Status = StatusInProgress
		if activeForm != "" {
			tk.ActiveForm = activeForm
		}
	case "pend":
		tk.Status = StatusPending
	case "complete":
		if tk.Status != StatusPending && tk.Status != StatusInProgress {
			return Task{}, fmt.Errorf("todo: complete 需 pending/in_progress(当前 %s)", tk.Status)
		}
		tk.Status = StatusCompleted
	}
	if !tk.Deleted { // 状态机操作不复活墓碑(上面 switch 前已对 deleted 任务拒绝)
		tk.TS = time.Now().Unix()
	}
	return tk, s.appendTask(path, tk)
}

// Delete 墓碑删除;被其它任务依赖时拒绝(防悬空)。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return err
	}
	tasks := s.loadLocked(path)
	tk, ok := findTask(tasks, id)
	if !ok {
		return fmt.Errorf("todo: 无此任务 %q", id)
	}
	if tk.Deleted {
		return fmt.Errorf("todo: 任务 %q 已删除", id)
	}
	for _, o := range tasks {
		if o.Deleted || o.ID == id {
			continue
		}
		for _, b := range o.BlockedBy {
			if b == id {
				return fmt.Errorf("todo: 任务 %q 仍被 %q 依赖,先解除再删除", id, o.ID)
			}
		}
	}
	tomb := Task{ID: id, Subject: tk.Subject, Status: StatusDeleted, Deleted: true, TS: time.Now().Unix()}
	return appendLine(path, tomb)
}

// Update 修改字段(subject/description/activeForm/owner/metadata)与 blockedBy 增删;
// 不改状态(id/status 只读)。变更后校验引用存在且无环。
func (s *Store) Update(id, subject, desc, activeForm, owner string, meta map[string]any, add, remove []string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Task{}, err
	}
	tasks := s.loadLocked(path)
	tk, ok := findTask(tasks, id)
	if !ok {
		return Task{}, fmt.Errorf("todo: 无此任务 %q", id)
	}
	if tk.Deleted {
		return Task{}, fmt.Errorf("todo: 任务 %q 已删除", id)
	}
	next := tk
	if subject != "" {
		next.Subject = subject
	}
	if desc != "" {
		next.Description = desc
	}
	if activeForm != "" {
		next.ActiveForm = activeForm
	}
	if owner != "" {
		next.Owner = owner
	}
	if meta != nil {
		next.Metadata = meta
	}
	// blockedBy 增删(操作 = 差集应用,去掉 remove 中的、并入 add 中的)
	for _, b := range add {
		if b == id {
			return Task{}, fmt.Errorf("todo: 任务不可依赖自身")
		}
	}
	has := func(bs []string, x string) bool {
		for _, b := range bs {
			if b == x {
				return true
			}
		}
		return false
	}
	var merged []string
	for _, b := range next.BlockedBy {
		if !has(remove, b) {
			merged = append(merged, b)
		}
	}
	for _, b := range add {
		if !has(merged, b) {
			merged = append(merged, b)
		}
	}
	next.BlockedBy = merged
	if err := s.validateDeps(tasks, id, next.BlockedBy); err != nil {
		return Task{}, err
	}
	next.TS = time.Now().Unix()
	return next, s.appendTask(path, next)
}

// List 摘要视图(status 过滤;排序:进行中 > 待办 > 已完成,组内创建倒序)。
func (s *Store) List(status string) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _ := s.path()
	tasks := s.loadLocked(path)
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

// Get 任务详情(含 description/metadata;deleted 也返回供核对)。
func (s *Store) Get(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Task{}, false
	}
	return findTask(s.loadLocked(path), id)
}

// validateRefs create 初始依赖:引用须存在且非 deleted。
func (s *Store) validateRefs(tasks []Task, self string, refs []string) error {
	for _, r := range refs {
		if r == self {
			return fmt.Errorf("todo: 任务不可依赖自身")
		}
		rt, ok := findTask(tasks, r)
		if !ok || rt.Deleted {
			return fmt.Errorf("todo: 依赖 %q 不存在或已删除(悬空引用)", r)
		}
	}
	return nil
}

// validateDeps update 后依赖:引用存在 + 无环(沿 blockedBy 自 self 可达检测)。
func (s *Store) validateDeps(tasks []Task, self string, deps []string) error {
	if err := s.validateRefs(tasks, self, deps); err != nil {
		return err
	}
	// 环检测:从 self 出发沿依赖链能否回到 self(deps 中新边已并入)。
	byID := map[string]Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	visiting := map[string]bool{}
	var dfs func(id string) bool
	dfs = func(id string) bool {
		if id == self {
			return true
		}
		if visiting[id] {
			return false
		}
		visiting[id] = true
		defer delete(visiting, id)
		for _, b := range byID[id].BlockedBy {
			if dfs(b) {
				return true
			}
		}
		return false
	}
	for _, d := range deps {
		if dfs(d) {
			return fmt.Errorf("todo: blockedBy 成环(%q 可达自身)", d)
		}
	}
	return nil
}

// loadLocked 重放文件(调用方持锁):逐行解析,同 id 最后一行胜(墓碑替换);
// 坏行/注释行容忍跳过。无文件 = 空。
func (s *Store) loadLocked(path string) []Task {
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

// appendTask 追加任务最新版本行。
func (s *Store) appendTask(path string, tk Task) error {
	return appendLine(path, tk)
}

// genID 时间戳 + 文件追加序号(唯一性)。
func (s *Store) genID(path string) string {
	return fmt.Sprintf("t%d-%d", time.Now().UnixNano(), countLines(path)+1)
}

// path 落盘路径:root 注入 > $GAH_HOME/todos > ~/.gah/todos;<project-key>.jsonl。
func (s *Store) path() (string, error) {
	root := s.root
	if root == "" {
		root = filepath.Join(sdk.Home(), "todos")
	}
	return filepath.Join(root, sdk.ProjectKeyFromCwd()+".jsonl"), nil
}

// appendLine 追加一行单行 JSON(经 sdk.AppendJSONLine:含尾部残行修复,防止断电
// 半写后与新记录粘成坏行导致两条记录同时被读侧跳过)。
func appendLine(path string, Task Task) error {
	// 干净数据根下 $GAH_HOME/todos/ 尚不存在:sdk.AppendJSONLine 只追加不建目录
	// (与 tool-memory 同规矩:调用方负责建父目录),漏掉会让首次建单报
	// "no such file or directory"(2026-09-19 本机验收逮到),/api/todo 空态也随之是 null。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("todo: 创建目录: %w", err)
	}
	return sdk.AppendJSONLine(path, Task)
}

// findTask 在重放结果中找任务(按 id;deleted 墓碑仍返回,便于状态判断)。
func findTask(tasks []Task, id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// statusOf 状态显示(悬空引用/缺失任务提示用)。
func statusOf(t Task) string {
	if t.Deleted {
		return "deleted"
	}
	return t.Status
}

// countLines 当前文件行数(id 后缀,仅唯一性辅助)。
func countLines(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var n int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	return n
}
