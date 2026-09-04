// Package hostjobs 提供 host-jobs 插件(M6.1):ctx.jobs 后台任务服务。
// 提交(shell 命令 / 函数任务,如 workflow background)→ 列表 → 取输出 → 终止;
// 任务在独立 goroutine 执行,不阻塞回合;终止 = 取消上下文 + 杀进程。
// 凭据隔离对齐 tool-shell:子进程 env 经 sdk.SanitizedEnv 过滤;read-only 沙箱下拒绝提交。
package hostjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-jobs。requires ctx.tools(注册任务工具);ctx.sandbox 可选注入。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-jobs" }

// Start 提供 ctx.jobs 服务并注册 job_list/job_output/job_kill 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	// 沙箱可选注入:未装配(如无 policy-sandbox)时跳过 read-only 检查,不影响任务机制
	var sb sdk.Sandbox
	_ = c.Inject("ctx.sandbox", &sb)

	j := New(sb)
	if err := c.Provide("ctx.jobs", j); err != nil {
		return nil, err
	}
	d1 := tools.Register(&ListTool{j: j})
	d2 := tools.Register(&OutputTool{j: j})
	d3 := tools.Register(&KillTool{j: j})
	return func() { d1(); d2(); d3() }, nil
}

// Jobs 实现 sdk.JobService。
type Jobs struct {
	mu    sync.Mutex
	seq   uint64
	jobs  map[string]*entry
	sb    sdk.Sandbox // 可为 nil(未装配沙箱)
	order []string    // ID 顺序(末位最新),历史清理用
}

// entry 一条任务记录。
type entry struct {
	job    sdk.Job
	cancel context.CancelFunc
	done   chan struct{} // 任务结束信号(关闭即完成)
}

const keepHistory = 20 // 完成后最多保留的任务数(防内存膨胀)

// New 构造任务服务。
func New(sb sdk.Sandbox) *Jobs {
	return &Jobs{jobs: make(map[string]*entry), sb: sb}
}

// Submit 提交 shell 命令后台执行(读沙箱模式下拒绝)。
func (j *Jobs) Submit(cmdline string) (string, error) {
	if err := j.checkExec(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &entry{done: make(chan struct{}), cancel: cancel}
	e.job = sdk.Job{
		ID: j.nextID(), State: sdk.JobRunning, Command: cmdline, CreatedAt: time.Now(),
	}
	j.add(e)
	go func() {
		cmd := exec.Command("/bin/sh", "-c", cmdline)
		cmd.Env = sdk.SanitizedEnv(nil) // 凭据隔离:滤除 *_API_KEY/*_TOKEN/*_SECRET
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Start(); err != nil {
			j.finish(e, sdk.JobFailed, "", err.Error())
			return
		}
		go func() { <-ctx.Done(); _ = cmd.Process.Kill() }() // 终止 = 杀进程
		err := cmd.Wait()
		output := buf.String()
		if ctx.Err() != nil {
			j.finish(e, sdk.JobKilled, output, "任务被终止")
			return
		}
		if err != nil {
			j.finish(e, sdk.JobFailed, output, err.Error())
			return
		}
		j.finish(e, sdk.JobDone, output, "")
	}()
	return e.job.ID, nil
}

// Run 注册函数型后台任务(workflow background 对接)。
func (j *Jobs) Run(fn sdk.JobFunc) (string, error) {
	if err := j.checkExec(); err != nil {
		return "", err
	}
	if fn == nil {
		return "", fmt.Errorf("host-jobs: 函数任务为空")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &entry{done: make(chan struct{}), cancel: cancel}
	e.job = sdk.Job{ID: j.nextID(), State: sdk.JobRunning, CreatedAt: time.Now()}
	j.add(e)
	go func() {
		result, err := fn(ctx)
		if ctx.Err() != nil {
			j.finish(e, sdk.JobKilled, "", "任务被终止")
			return
		}
		if err != nil {
			j.finishWith(e, func(job *sdk.Job) {
				job.State = sdk.JobFailed
				job.Error = err.Error()
			})
			return
		}
		j.finishWith(e, func(job *sdk.Job) {
			job.State = sdk.JobDone
			job.Result = result
		})
	}()
	return e.job.ID, nil
}

// List 全部任务(末位最新)。已完成的历史保留最近 keepHistory 条。
func (j *Jobs) List() []sdk.Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]sdk.Job, 0, len(j.order))
	for _, id := range j.order {
		if e, ok := j.jobs[id]; ok {
			out = append(out, e.job)
		}
	}
	return out
}

// Output 取任务当前状态与输出。
func (j *Jobs) Output(id string) (sdk.Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.jobs[id]
	if !ok {
		return sdk.Job{}, false
	}
	return e.job, true
}

// Kill 终止运行中的任务;已完成任务拒绝。
func (j *Jobs) Kill(id string) error {
	j.mu.Lock()
	e, ok := j.jobs[id]
	if ok && e.job.State != sdk.JobRunning {
		j.mu.Unlock()
		return fmt.Errorf("host-jobs: 任务 %s 不在运行(state=%s)", id, e.job.State)
	}
	j.mu.Unlock()
	if !ok {
		return fmt.Errorf("host-jobs: 任务不存在 %s", id)
	}
	e.cancel() // 命令任务:取消 → 杀进程;函数任务:取消 ctx
	select {
	case <-e.done:
	case <-time.After(3 * time.Second): // 杀不掉也给报错,由调用方重试
		return fmt.Errorf("host-jobs: 任务 %s 终止超时", id)
	}
	return nil
}

// checkExec read-only 沙箱下拒绝提交(执行器类约束,对齐 policy-sandbox)。
func (j *Jobs) checkExec() error {
	if j.sb != nil && j.sb.Mode() == sdk.SandboxReadOnly {
		return fmt.Errorf("sandbox: read-only 拒绝提交后台任务")
	}
	return nil
}

// add 注册条目并入队。
func (j *Jobs) add(e *entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jobs[e.job.ID] = e
	j.order = append(j.order, e.job.ID)
}

// finish 通用完成(命令任务路径;回调在锁内更新状态与输出)。
func (j *Jobs) finish(e *entry, state sdk.JobState, output, errMsg string) {
	j.finishWith(e, func(job *sdk.Job) {
		job.State = state
		job.Output = output
		job.Error = errMsg
	})
}

// finishWith 标记完成并清理历史(保留最近 keepHistory 条)。
func (j *Jobs) finishWith(e *entry, mutate func(*sdk.Job)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	mutate(&e.job)
	e.job.DoneAt = time.Now()
	close(e.done)
	// 历史清理:完成的已超 keepHistory,移除最旧的
	doneCount := 0
	for _, id := range j.order {
		if ee, ok := j.jobs[id]; ok && ee.job.State != sdk.JobRunning {
			doneCount++
		}
	}
	for doneCount > keepHistory && len(j.order) > 0 {
		id := j.order[0]
		j.order = j.order[1:]
		if e2, ok := j.jobs[id]; ok && e2.job.State != sdk.JobRunning {
			delete(j.jobs, id)
			doneCount--
			continue
		}
		// 运行中的最旧条目不能删(保留前台),重置计数重扫
		j.order = append([]string{id}, j.order...) // 放回队首继续找
		break
	}
}

// nextID 任务 ID:job_<递增序号>。
func (j *Jobs) nextID() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	return fmt.Sprintf("job_%d", j.seq)
}

// —— 模型工具(job_list/job_output/job_kill)——

// ListTool job_list:列出全部后台任务。
type ListTool struct{ j *Jobs }

func (t *ListTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "job_list",
		Description: "列出全部后台任务(job_list):{过滤:all|running} 默认 all",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"filter": map[string]any{"type": "string"}},
		},
	}
}

func (t *ListTool) Execute(_ context.Context, raw string) (any, error) {
	var a struct {
		Filter string `json:"filter,omitempty"`
	}
	_ = json.Unmarshal([]byte(raw), &a)
	all := t.j.List()
	if a.Filter == "running" {
		out := make([]sdk.Job, 0, len(all))
		for _, job := range all {
			if job.State == sdk.JobRunning {
				out = append(out, job)
			}
		}
		all = out
	}
	return map[string]any{"jobs": jobsView(all)}, nil
}

// OutputTool job_output:取任务输出。
type OutputTool struct{ j *Jobs }

func (t *OutputTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "job_output",
		Description: "取后台任务状态与输出/结果:{id}",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"id"},
		},
	}
}

func (t *OutputTool) Execute(_ context.Context, raw string) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	job, ok := t.j.Output(a.ID)
	if !ok {
		return map[string]any{"error": "任务不存在: " + a.ID}, nil
	}
	return map[string]any{"job": jobView(job)}, nil
}

// KillTool job_kill:终止任务。
type KillTool struct{ j *Jobs }

func (t *KillTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "job_kill",
		Description: "终止运行中的后台任务:{id}",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"id"},
		},
	}
}

func (t *KillTool) Execute(_ context.Context, raw string) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	if err := t.j.Kill(a.ID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"killed": a.ID}, nil
}

// jobView 精简视图:模型输出友好(省略大段 output)。
func jobView(j sdk.Job) map[string]any {
	out := map[string]any{
		"id": j.ID, "state": string(j.State), "command": j.Command,
		"created_at": j.CreatedAt.Format(time.RFC3339),
	}
	if !j.DoneAt.IsZero() {
		out["done_at"] = j.DoneAt.Format(time.RFC3339)
	}
	if j.Error != "" {
		out["error"] = j.Error
	}
	if j.Result != nil {
		out["result"] = j.Result
	}
	if l := len(j.Output); l > 0 {
		if l > 2000 {
			out["output"] = j.Output[:2000] + "…(截断)"
		} else {
			out["output"] = j.Output
		}
	}
	return out
}

// jobsView 列表视图。
func jobsView(jobs []sdk.Job) []map[string]any {
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobView(j))
	}
	return out
}