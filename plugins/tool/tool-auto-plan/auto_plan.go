// Package toolautoplan 提供 tool-auto-plan 插件(M11):规划模式。
// 与 tool-todo 分工:plan = 规划期产物(先探索→结构化规划→等用户确认→逐步骤执行),
// todo = 执行期任务追踪(确认后执行期由 todo 承接)。
//
// 工具 auto_plan(单 schema 多 action):
//
//	create  —— 模型先做只读探索,生成结构化规划(目标/关键约束与风险/线性检查清单)并落盘;
//	get/list —— 读取(当前项目全部);
//	step    —— 步骤状态推进(待→进行中→完成,线性清单,不上依赖图防过度工程);
//	confirm —— 用户明确"确认/执行/开始"后标记已确认,允许进入执行阶段;
//	complete—— 全部完成后归档。
//
// 规划模式规则注入(enable_rule 开关,默认开):装配进宿主时经 ctx.systemPrompt
// AddSection 注入——用户请求要求先规划后执行或为复杂任务(3+ 步)时,模型须先只读
// 探索并 auto_plan.create 输出规划,**用户确认前不执行任何有副作用操作**(纯查询可做)。
// 意图判定而非裸关键词包含:请求本身已含明确执行指令(如"将规划写入文档""仅/只 <动词>"
// 的命令式)时不视为规划请求,按其指令执行(参考 pi auto-plan 误判教训)。
//
// 存储:$GAH_HOME/plans/<project-key>.jsonl —— 追加式 jsonl(同 id 最后一行胜)、
// 坏行容忍、人工可编辑(session/memory/todo 同三特性);项目 key 用 sdk.ProjectKey
// (共享派生)。锁内重放文件(权威状态)→ 状态机校验 → 追加新版本行。
package toolautoplan

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

// 计划生命周期状态。
const (
	StatusProposed  = "proposed"  // 已落盘,等待用户确认
	StatusConfirmed = "confirmed" // 用户已确认,允许进入执行阶段
	StatusCompleted = "completed" // 已归档
)

// 步骤状态。
const (
	StepPending    = "pending"
	StepInProgress = "in_progress"
	StepCompleted  = "completed"
)

// stepAllowedFrom 步骤状态可达集(pending→in_progress/completed,in_progress→pending/completed)。
var stepFrom = map[string]bool{StepPending: true, StepInProgress: true}

// Plan 一份规划。jsonl 每行一个 Plan 快照;同 id 以最后一行(最新)为准。
type Plan struct {
	ID          string   `json:"id"`
	Request     string   `json:"request"`               // 原始用户请求(规划来源)
	Objective   string   `json:"objective"`             // 目标
	Constraints []string `json:"constraints,omitempty"` // 关键约束与风险
	Steps       []Step   `json:"steps"`                 // 线性检查清单(顺序执行)
	Status      string   `json:"status"`
	TS          int64    `json:"ts"` // 最后变更时间
}

// Step 检查清单一步。
type Step struct {
	Index  int    `json:"index"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// View list 摘要(精简字段,objective/constraints 留 get)。
type View struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Request   string `json:"request"`
	Objective string `json:"objective,omitempty"`
	Done      int    `json:"done"` // 已完成步骤数 / 总步骤数
	Total     int    `json:"total"`
	TS        int64  `json:"ts"`
}

// Tool 实现 sdk.Tool(单工具多 action)。
type Tool struct {
	store *Store
}

// NewTool 工厂。
func NewTool() sdk.Tool {
	return &Tool{store: NewStore("")}
}

// Plugin 实现 tool-auto-plan(进程内装配:注册工具 + 规则注入,同 host-skills 先例)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-auto-plan" }

// Start 注册 auto_plan 工具;data.enable_rule(默认 true)开则注入规划模式规则。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	d1 := tools.Register(NewTool())
	enable := true
	if m != nil && m.Data != nil {
		if v, ok := m.Data["enable_rule"].(bool); ok {
			enable = v
		}
	}
	if !enable {
		return d1, nil
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		d1()
		return nil, err
	}
	d2 := sp.AddSection(sdk.SystemPromptSection{
		Name:    "规划模式",
		Content: RuleSectionText,
	})
	return func() { d1(); d2() }, nil
}

// RuleSectionText 规划模式系统提示片段(纯函数,便于单测断言)。
func RuleSectionText() string {
	return `规划模式:用户请求要求“先规划后执行”,或属于复杂任务(3+ 步)时,先只读探索(查询类工具),` +
		`再用 auto_plan.create 输出结构化规划(目标/关键约束与风险/检查清单步骤)并等待用户明确确认。` +
		`用户确认(如“确认/执行/开始”)前,不得执行任何有副作用操作(文件写/命令/网络等);纯查询可做。` +
		`确认后 auto_plan.confirm;执行期任务追踪交由 todo 承接(M11-T2 分工:auto_plan 管规划期产物与确认门,` +
		`todo 管确认后的执行任务状态机):把规划检查清单逐项 todo.create(blockedBy 串依赖),按 todo 状态机推进执行;` +
		`执行完成后回 auto_plan.step/complete 归档规划(或仅 complete 整体归档)。` +
		`意图判定:请求本身已含明确执行指令(命令式,如“将规划写入文档”“仅/只 <动词>”)时不视为规划请求,直接按其指令执行。`
}

func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "auto_plan",
		Description: "规划模式工具(规划期产物与确认门;执行期任务追踪用 todo)。模型面对'先规划后执行'或复杂任务(3+ 步)时," +
			"先只读探索再 create 落盘结构化规划,等用户确认后才执行。action:" +
			"create(request 必填,objective/constraints[]/steps[] 由探索后生成;steps=线性检查清单标题数组)→ 返回 id(状态 proposed);" +
			"get(id) 看详情;list(status?) 当前项目全部(摘要);" +
			"confirm(id) 用户明确确认后标记(proposed→confirmed,允许执行);" +
			"step(id,index,status) 步骤推进(status: pending|in_progress|completed;仅 confirmed 后可改,completed 步骤锁定;执行期主要状态机用 todo,plan step 供轻量/收尾同步);" +
			"complete(id) 全部完成后归档(confirmed→completed)。M11-T2 联动:确认后把检查清单转 todo.create 逐项追踪执行(todo 状态机)," +
			"执行完回 complete 归档规划。跨会话保留、人工可编辑 jsonl。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action":      map[string]any{"type": "string", "enum": []string{"create", "get", "list", "step", "confirm", "complete"}},
				"request":     map[string]any{"type": "string", "description": "create:原始用户请求(必填)"},
				"objective":   map[string]any{"type": "string", "description": "create:目标"},
				"constraints": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "create:关键约束与风险"},
				"steps":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "create:线性检查清单步骤标题(顺序)"},
				"id":          map[string]any{"type": "string", "description": "get/step/confirm/complete 目标规划"},
				"status":      map[string]any{"type": "string", "description": "list 过滤(proposed|confirmed|completed);step 目标步骤状态(pending|in_progress|completed)"},
				"index":       map[string]any{"type": "integer", "description": "step:步骤序号(0 起)"},
			},
		},
	}
}

// args 统一入参。
type args struct {
	Action      string   `json:"action"`
	Request     string   `json:"request"`
	Objective   string   `json:"objective"`
	Constraints []string `json:"constraints"`
	Steps       []string `json:"steps"`
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	Index       int      `json:"index"`
}

// Execute 执行一个 action;业务失败以 map{"error":...} 回传(不中断 turn)。
func (t *Tool) Execute(_ context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("auto_plan: args: %w", err)
	}
	st := t.store
	switch a.Action {
	case "create":
		if strings.TrimSpace(a.Request) == "" {
			return map[string]any{"error": "auto_plan: create 需 request(原始请求)"}, nil
		}
		p, err := st.Create(strings.TrimSpace(a.Request), strings.TrimSpace(a.Objective), a.Constraints, a.Steps)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": p.ID, "status": p.Status, "steps": len(p.Steps)}, nil
	case "get":
		p, ok := st.Get(a.ID)
		if !ok {
			return map[string]any{"error": fmt.Sprintf("auto_plan: 无此规划 %q", a.ID)}, nil
		}
		return p, nil
	case "list":
		return st.List(a.Status), nil
	case "step":
		if err := st.Step(a.ID, a.Index, a.Status); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"ok": true}, nil
	case "confirm":
		if err := st.Confirm(a.ID); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": a.ID, "status": StatusConfirmed}, nil
	case "complete":
		if err := st.Complete(a.ID); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"id": a.ID, "status": StatusCompleted}, nil
	default:
		return map[string]any{"error": fmt.Sprintf("auto_plan: 未知 action %q(create|get|list|step|confirm|complete)", a.Action)}, nil
	}
}

// Store 规划存储:追加式 jsonl + 同 id 最后一行胜,坏行容忍,人工可编辑。
type Store struct {
	mu   sync.Mutex
	root string // 空 = 默认 $GAH_HOME/plans
}

// NewStore 构造存储。
func NewStore(root string) *Store { return &Store{root: root} }

// Create 落盘一份新规划(状态 proposed)。steps 标题数组 → 步骤序列(初始 pending)。
func (s *Store) Create(request, objective string, constraints, stepTitles []string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Plan{}, err
	}
	steps := make([]Step, 0, len(stepTitles))
	for i, t := range stepTitles {
		steps = append(steps, Step{Index: i, Title: strings.TrimSpace(t), Status: StepPending})
	}
	p := Plan{ID: s.genID(path), Request: request, Objective: objective,
		Constraints: append([]string(nil), constraints...), Steps: steps,
		Status: StatusProposed, TS: time.Now().Unix()}
	if err := appendPlan(path, p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Step 步骤状态推进。仅 confirmed 后可改;completed 步骤锁定不可回退。
func (s *Store) Step(id string, index int, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return err
	}
	plans := s.loadLocked(path)
	p, ok := findPlan(plans, id)
	if !ok {
		return fmt.Errorf("auto_plan: 无此规划 %q", id)
	}
	if p.Status == StatusCompleted {
		return fmt.Errorf("auto_plan: 规划 %q 已归档,不可再改", id)
	}
	if p.Status != StatusConfirmed {
		return fmt.Errorf("auto_plan: 规划 %q 未确认,先等用户确认再 auto_plan.confirm", id)
	}
	if index < 0 || index >= len(p.Steps) {
		return fmt.Errorf("auto_plan: 步骤序号 %d 越界(共 %d 步)", index, len(p.Steps))
	}
	switch status {
	case StepPending, StepInProgress, StepCompleted:
	default:
		return fmt.Errorf("auto_plan: 非法步骤状态 %q(pending|in_progress|completed)", status)
	}
	st := &p.Steps[index]
	if st.Status == StepCompleted {
		return fmt.Errorf("auto_plan: 步骤 %d 已完成,不可回退", index)
	}
	if st.Status == StepPending && !stepFrom[status] {
		// pending → in_progress/completed 均合法;此处 status 已过滤,保留结构清晰
	}
	st.Status = status
	p.TS = time.Now().Unix()
	return s.append(p)
}

// Confirm 用户确认:proposed → confirmed。
func (s *Store) Confirm(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return err
	}
	p, ok := findPlan(s.loadLocked(path), id)
	if !ok {
		return fmt.Errorf("auto_plan: 无此规划 %q", id)
	}
	if p.Status == StatusConfirmed {
		return fmt.Errorf("auto_plan: 规划 %q 已确认", id)
	}
	if p.Status == StatusCompleted {
		return fmt.Errorf("auto_plan: 规划 %q 已归档", id)
	}
	p.Status = StatusConfirmed
	p.TS = time.Now().Unix()
	return s.append(p)
}

// Complete 归档:confirmed → completed。
func (s *Store) Complete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return err
	}
	p, ok := findPlan(s.loadLocked(path), id)
	if !ok {
		return fmt.Errorf("auto_plan: 无此规划 %q", id)
	}
	if p.Status == StatusCompleted {
		return fmt.Errorf("auto_plan: 规划 %q 已归档", id)
	}
	if p.Status != StatusConfirmed {
		return fmt.Errorf("auto_plan: 规划 %q 未确认,不可归档", id)
	}
	p.Status = StatusCompleted
	p.TS = time.Now().Unix()
	return s.append(p)
}

// Get 规划详情(含 objective/constraints;已归档也返回供核对)。
func (s *Store) Get(id string) (Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return Plan{}, false
	}
	return findPlan(s.loadLocked(path), id)
}

// List 摘要(status 过滤;排序:proposed/confirmed 在前按时间倒序,completed 排后)。
func (s *Store) List(status string) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path()
	if err != nil {
		return nil
	}
	plans := s.loadLocked(path)
	weight := map[string]int{StatusProposed: 0, StatusConfirmed: 1, StatusCompleted: 2}
	var out []View
	for _, p := range plans {
		if status != "" && p.Status != status {
			continue
		}
		done, total := 0, len(p.Steps)
		for _, st := range p.Steps {
			if st.Status == StepCompleted {
				done++
			}
		}
		out = append(out, View{ID: p.ID, Status: p.Status, Request: p.Request,
			Objective: p.Objective, Done: done, Total: total, TS: p.TS})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if weight[out[i].Status] != weight[out[j].Status] {
			return weight[out[i].Status] < weight[out[j].Status]
		}
		return out[i].TS > out[j].TS
	})
	return out
}

// loadLocked 重放文件(调用方持锁):逐行解析,同 id 最后一行胜;坏行/注释行容忍。
func (s *Store) loadLocked(path string) []Plan {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	byID := map[string]Plan{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] == '#' {
			continue // 空行/人工注释
		}
		var p Plan
		if err := json.Unmarshal(line, &p); err != nil || p.ID == "" || p.Status == "" {
			continue // 坏行:容忍跳过
		}
		if _, seen := byID[p.ID]; !seen {
			order = append(order, p.ID)
		}
		byID[p.ID] = p
	}
	out := make([]Plan, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// append 追加规划最新快照行。
func (s *Store) append(p Plan) error {
	path, err := s.path()
	if err != nil {
		return err
	}
	return appendPlan(path, p)
}

// genID 时间戳 + 文件追加序号(唯一性)。
func (s *Store) genID(path string) string {
	return fmt.Sprintf("p%d-%d", time.Now().UnixNano(), countLines(path)+1)
}

// path 落盘路径:root 注入 > $GAH_HOME/plans > ~/.gah/plans;<project-key>.jsonl。
func (s *Store) path() (string, error) {
	root := s.root
	if root == "" {
		home := os.Getenv("GAH_HOME")
		if home == "" {
			uh, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			home = filepath.Join(uh, ".gah")
		}
		root = filepath.Join(home, "plans")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(root, sdk.ProjectKeyFromCwd()+".jsonl"), nil
}

// appendPlan O_APPEND 追加一行(单行 JSON,含换行)。
func appendPlan(path string, p Plan) error {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("auto_plan: 编码: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("auto_plan: 追加: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("auto_plan: 写入: %w", err)
	}
	return nil
}

// findPlan 在重放结果中找规划(按 id)。
func findPlan(plans []Plan, id string) (Plan, bool) {
	for _, p := range plans {
		if p.ID == id {
			return p, true
		}
	}
	return Plan{}, false
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
