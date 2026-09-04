// Package toolworkflow 提供 tool-workflow 插件:workflow 工具(PTC 程序化工具调用)。
// 模型写一段受限 starlark 程序一把过组合多步工具调用(对齐 dsc PTC/DSH tool-workflow):
//   - 脚本内每个可用工具以同名函数暴露(shell{...});无标准库/系统调用 → 天然沙箱;
//   - 结果约定:顶层变量 result 即结果(可赋值 dict/list);
//   - background: true 异步执行,结果经 workflow_collect 取回。
package toolworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 tool-workflow。requires ctx.tools。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-workflow" }

// Start 注册 workflow 与 workflow_collect 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	w := &WorkflowTool{tools: tools, logger: c.Logger()}
	// 后台任务经 ctx.jobs(host-jobs,M6.1);未装配时 background 调用显式报错
	_ = c.Inject("ctx.jobs", &w.jobsSvc)
	d1 := tools.Register(w)
	d2 := tools.Register(&Collector{w: w})
	return func() { d1(); d2() }, nil
}

// WorkflowTool 执行 starlark 工作流脚本。
type WorkflowTool struct {
	tools   sdk.ToolRegistry
	logger  *slog.Logger
	jobsSvc sdk.JobService // 可为 nil:host-jobs 未装配时 background 报错
}

func (w *WorkflowTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "workflow",
		Description: "用受限 starlark 程序一把过组合多步工具调用;每个可用工具以同名函数暴露;顶层变量 result 即结果;background:true 异步。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"script":     map[string]any{"type": "string"},
				"background": map[string]any{"type": "boolean"},
			},
			"required": []any{"script"},
		},
	}
}

func (w *WorkflowTool) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		Script     string `json:"script"`
		Background bool   `json:"background,omitempty"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("workflow: args: %w", err)
	}
	if strings.TrimSpace(a.Script) == "" {
		return nil, fmt.Errorf("workflow: 缺少 script")
	}
	if a.Background {
		// 真异步(M6.1):提交到 ctx.jobs 立即返回,不阻塞回合;结果经 workflow_collect 轮询
		if w.jobsSvc == nil {
			return map[string]any{"error": "后台任务需要 host-jobs 插件(ctx.jobs 未装配)"}, nil
		}
		id, err := w.jobsSvc.Run(func(ctx context.Context) (any, error) {
			return w.run(ctx, a.Script)
		})
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"background": true, "job_id": id}, nil
	}
	result, err := w.run(ctx, a.Script)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return result, nil
}

// run 解释执行脚本:工具以同名函数暴露,result 变量即结果。
func (w *WorkflowTool) run(ctx context.Context, script string) (any, error) {
	predeclared := starlark.StringDict{}
	for _, t := range w.tools.List() {
		name := t.Name
		if name == "workflow" || name == "workflow_collect" {
			continue // 嵌套/收集禁用
		}
		predeclared[name] = starlark.NewBuiltin("tool_"+name, func(th *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return w.callTool(th, ctx, name, args, kwargs)
		})
	}

	thread := &starlark.Thread{Name: "workflow"}
	thread.Print = func(th *starlark.Thread, msg string) {
		w.logger.Info("workflow print", "msg", msg)
	}

	opts := &syntax.FileOptions{Set: true, While: true}
	globals, err := starlark.ExecFileOptions(opts, thread, "workflow.star", script, predeclared)
	if err != nil {
		return nil, err
	}
	if v, ok := globals["result"]; ok {
		return valueToGo(v), nil
	}
	return nil, fmt.Errorf("workflow: 脚本未定义 result 变量")
}

// callTool 脚本内的工具函数:kwargs 即工具字段;单位置字符串按 command 处理。
func (w *WorkflowTool) callTool(th *starlark.Thread, ctx context.Context, name string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "workflow" || name == "workflow_collect" {
		return nil, fmt.Errorf("workflow: 嵌套调用 %q 被禁用", name)
	}
	obj := map[string]any{}
	for _, k := range kwargs {
		obj[k[0].(starlark.String).GoString()] = valueToGo(k[1])
	}
	if len(args) == 1 {
		switch v := args[0].(type) {
		case starlark.String:
			obj["command"] = v.GoString()
		default:
			if m, ok := valueToGo(v).(map[string]any); ok {
				mergeInto(obj, m)
			} else {
				obj["value"] = valueToGo(v)
			}
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	res, err := w.tools.Execute(ctx, name, string(b))
	if err != nil {
		return goToValue(map[string]any{"error": err.Error()}), nil
	}
	if res.Error != "" {
		return goToValue(map[string]any{"error": res.Error}), nil
	}
	var out any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		return starlark.String(res.Content), nil
	}
	return goToValue(out), nil
}

// Collector 收集异步结果。
type Collector struct {
	w *WorkflowTool
}

func (c *Collector) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "workflow_collect",
		Description: "取回 background workflow 的结果:{job_id}",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"job_id"},
		},
	}
}

func (c *Collector) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	if c.w.jobsSvc == nil {
		return map[string]any{"error": "ctx.jobs 未装配(host-jobs)"}, nil
	}
	job, ok := c.w.jobsSvc.Output(a.JobID)
	if !ok {
		return map[string]any{"error": "job 不存在: " + a.JobID}, nil
	}
	if job.State == sdk.JobRunning {
		return map[string]any{"job_id": a.JobID, "state": "running"}, nil
	}
	if job.State != sdk.JobDone {
		return map[string]any{"job_id": a.JobID, "state": string(job.State), "error": job.Error}, nil
	}
	return map[string]any{"job_id": a.JobID, "result": job.Result}, nil
}

// —— 值转换(starlark ↔ go) ——

func valueToGo(v starlark.Value) any {
	switch v := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(v)
	case starlark.Int:
		if i, ok := v.Int64(); ok {
			return i
		}
		return v.String()
	case starlark.Float:
		return float64(v)
	case starlark.String:
		return v.GoString()
	case starlark.Tuple:
		out := make([]any, 0, len(v))
		for _, e := range v {
			out = append(out, valueToGo(e))
		}
		return out
	case *starlark.List:
		out := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			out = append(out, valueToGo(v.Index(i)))
		}
		return out
	case *starlark.Dict:
		out := map[string]any{}
		for _, k := range v.Keys() {
			if x, found, err := v.Get(k); found && err == nil {
				out[keyOf(k)] = valueToGo(x)
			}
		}
		return out
	default:
		return v.String()
	}
}

func goToValue(x any) starlark.Value {
	switch v := x.(type) {
	case nil:
		return starlark.None
	case bool:
		return starlark.Bool(v)
	case string:
		return starlark.String(v)
	case int:
		return starlark.MakeInt(v)
	case int64:
		return starlark.MakeInt64(v)
	case float64:
		return starlark.Float(v)
	case []any:
		out := make([]starlark.Value, 0, len(v))
		for _, e := range v {
			out = append(out, goToValue(e))
		}
		return starlark.NewList(out)
	case map[string]any:
		d := starlark.NewDict(len(v))
		for k, e := range v {
			d.SetKey(starlark.String(k), goToValue(e))
		}
		return d
	default:
		return starlark.String(fmt.Sprint(v))
	}
}

// keyOf dict 键转字符串(dict 键多为主 starlark.String;用 GoString 取真实内容)。
func keyOf(k starlark.Value) string {
	if s, ok := k.(starlark.String); ok {
		return s.GoString()
	}
	return k.String()
}

func mergeInto(base map[string]any, extra map[string]any) {
	for k, v := range extra {
		base[k] = v
	}
}
