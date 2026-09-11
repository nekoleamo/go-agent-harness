// jobs_plugin_test.go host-jobs 装配与外围(覆盖率补强):
// 插件 Start 的接线(Provide ctx.jobs + 三个模型工具 + /jobs 命令级联 + job/done 事件),
// 未装配服务的显式失败,以及 /jobs 命令、job_* 工具、输出上限、Stop 拒绝新任务等
// 「已提交任务必须可查可停、卸载即撤销」的真实不变量。
package hostjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/plugins/policy/policy-guard"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 最小 sdk.Ctx / 注册表桩 ——

type jcCtx struct {
	mu       sync.Mutex
	provided map[string]any
	events   []sdk.Event
	tools    sdk.ToolRegistry
	cmds     sdk.CommandRegistry
	sb       sdk.Sandbox
	noTools  bool // 模拟宿主未装配 ctx.tools
	log      *slog.Logger
}

func newJcCtx() *jcCtx {
	return &jcCtx{provided: map[string]any{}, tools: newJcTools(), cmds: newJcCmds(),
		log: slog.New(slog.DiscardHandler)}
}

func (c *jcCtx) Provide(key string, svc any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.provided[key]; dup {
		return fmt.Errorf("jcCtx: 重复提供 %s", key)
	}
	c.provided[key] = svc
	return nil
}

func (c *jcCtx) Inject(key string, out any) error {
	switch key {
	case "ctx.tools":
		if c.noTools {
			return errors.New("jcCtx: 未装配 ctx.tools")
		}
		*(out.(*sdk.ToolRegistry)) = c.tools
		return nil
	case "ctx.commands":
		if c.cmds == nil {
			return errors.New("jcCtx: 未装配 ctx.commands")
		}
		*(out.(*sdk.CommandRegistry)) = c.cmds
		return nil
	case "ctx.sandbox":
		if c.sb == nil {
			return errors.New("jcCtx: 未装配 ctx.sandbox")
		}
		*(out.(*sdk.Sandbox)) = c.sb
		return nil
	}
	return fmt.Errorf("jcCtx: 未装配 %s", key)
}

func (c *jcCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }

func (c *jcCtx) Emit(_ context.Context, name string, payload any, _ sdk.DispatchMode) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, sdk.Event{Name: name, Payload: payload})
	return nil, nil
}

func (c *jcCtx) Logger() *slog.Logger { return c.log }

func (c *jcCtx) jobsService(t *testing.T) *Jobs {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	svc, ok := c.provided["ctx.jobs"]
	if !ok {
		t.Fatal("Start 应 Provide ctx.jobs")
	}
	j, ok := svc.(*Jobs)
	if !ok {
		t.Fatalf("ctx.jobs 应为 *Jobs: %T", svc)
	}
	return j
}

func (c *jcCtx) doneEvents() []sdk.JobDoneEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []sdk.JobDoneEvent
	for _, ev := range c.events {
		if ev.Name == sdk.EventJobDone {
			if p, ok := ev.Payload.(*sdk.JobDoneEvent); ok {
				out = append(out, *p)
			}
		}
	}
	return out
}

// jcTools 记录注册的工具(Disposer 撤销)。
type jcTools struct {
	mu sync.Mutex
	m  map[string]sdk.Tool
}

func newJcTools() *jcTools { return &jcTools{m: map[string]sdk.Tool{}} }

func (r *jcTools) Register(t sdk.Tool) sdk.Disposer {
	name := t.Definition().Name
	r.mu.Lock()
	r.m[name] = t
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.m, name)
		r.mu.Unlock()
	}
}
func (r *jcTools) List() []sdk.ToolDefinition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdk.ToolDefinition, 0, len(r.m))
	for _, t := range r.m {
		out = append(out, t.Definition())
	}
	return out
}
func (r *jcTools) Get(name string) (sdk.ToolDefinition, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.m[name]
	if !ok {
		return sdk.ToolDefinition{}, false
	}
	return t.Definition(), true
}
func (r *jcTools) Execute(ctx context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	t, ok := r.m[name]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("未注册工具 %s", name)
	}
	v, err := t.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	return &sdk.ToolResult{Content: fmt.Sprint(v)}, nil
}
func (r *jcTools) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	return out
}

var _ sdk.ToolRegistry = (*jcTools)(nil)

// jcCmds 记录注册的命令(Disposer 撤销;同名拒绝,对齐宿主注册表语义)。
type jcCmds struct {
	mu sync.Mutex
	m  map[string]sdk.CommandSpec
}

func newJcCmds() *jcCmds { return &jcCmds{m: map[string]sdk.CommandSpec{}} }

func (r *jcCmds) Register(spec sdk.CommandSpec) (sdk.Disposer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.m[spec.Name]; dup {
		return nil, fmt.Errorf("命令已存在: %s", spec.Name)
	}
	r.m[spec.Name] = spec
	return func() {
		r.mu.Lock()
		delete(r.m, spec.Name)
		r.mu.Unlock()
	}, nil
}
func (r *jcCmds) List() []sdk.CommandSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdk.CommandSpec, 0, len(r.m))
	for _, s := range r.m {
		out = append(out, s)
	}
	return out
}
func (r *jcCmds) Get(name string) (sdk.CommandSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[name]
	return s, ok
}

var _ sdk.CommandRegistry = (*jcCmds)(nil)

// —— 装配 ——

// TestPluginStartWiring Start 接线:Provide ctx.jobs(可经注入取回)、注册三个模型工具、
// 注册 /jobs 命令(两级级联)、终态事件经 job/done Emit;卸载撤销工具/命令并拒绝新任务。
func TestPluginStartWiring(t *testing.T) {
	c := newJcCtx()
	c.sb = policyguard.DefaultSandbox("/tmp/ws")
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if (&Plugin{}).Name() != "host-jobs" {
		t.Fatal("插件名不符")
	}
	j := c.jobsService(t)

	// 模型工具:三个 job_* 均已注册,定义名与描述可用
	for _, name := range []string{"job_list", "job_output", "job_kill"} {
		def, ok := c.tools.Get(name)
		if !ok {
			t.Fatalf("应注册工具 %s(实际 %v)", name, c.tools.(*jcTools).names())
		}
		if def.Description == "" {
			t.Fatalf("工具 %s 应有描述(模型可见): %+v", name, def)
		}
	}
	if len(c.tools.List()) != 3 {
		t.Fatalf("应只注册 3 个任务工具: %d", len(c.tools.List()))
	}

	// /jobs 命令:两级级联(一级 list/output/kill;二级枚举任务 ID,list 无二级)
	spec, ok := c.cmds.Get("jobs")
	if !ok {
		t.Fatal("应注册 /jobs 命令")
	}
	if !strings.Contains(spec.Usage, "list") || spec.Desc == "" || len(spec.Args) != 2 {
		t.Fatalf("/jobs 声明不符: %+v", spec)
	}
	lvl0 := spec.Args[0].Options(nil)
	if len(lvl0) != 3 || lvl0[0].Value != "list" || lvl0[2].Value != "kill" {
		t.Fatalf("一级选项不符: %+v", lvl0)
	}
	if got := spec.Args[1].Options([]string{"/jobs"}); got != nil {
		t.Fatalf("未选子命令时二级应为空: %+v", got)
	}
	if got := spec.Args[1].Options([]string{"/jobs", "list"}); got != nil {
		t.Fatalf("list 无二级(选完直接执行): %+v", got)
	}
	id, err := j.Submit(`sleep 3`)
	if err != nil {
		t.Fatal(err)
	}
	lvl1 := spec.Args[1].Options([]string{"/jobs", "output"})
	if len(lvl1) != 1 || lvl1[0].Value != id || lvl1[0].Desc != "sleep 3" {
		t.Fatalf("二级应枚举任务 ID + 命令: %+v", lvl1)
	}

	// 终态事件:函数任务完成后经 job/done 通知(Web/StatusBar 免轮询)
	doneID, err := j.Run(func(context.Context) (any, error) { return "完成", nil })
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, j, doneID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(c.doneEvents()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	evs := c.doneEvents()
	if len(evs) != 1 || evs[0].ID != doneID || evs[0].State != sdk.JobDone {
		t.Fatalf("应恰有一次 job/done(ID+State): %+v", evs)
	}

	// 卸载即撤销:工具/命令注销 + 新提交被拒(幂等)
	dis()
	if _, ok := c.tools.Get("job_list"); ok {
		t.Fatal("卸载应撤销工具注册")
	}
	if _, ok := c.cmds.Get("jobs"); ok {
		t.Fatal("卸载应撤销命令注册")
	}
	if _, err := j.Submit("echo hi"); err == nil || !strings.Contains(err.Error(), "已卸载") {
		t.Fatalf("卸载后应拒绝新任务: %v", err)
	}
	if _, err := j.Run(func(context.Context) (any, error) { return nil, nil }); err == nil {
		t.Fatal("卸载后 Run 也应被拒")
	}
	dis() // 幂等
}

// TestPluginStartOptionalAndRequiredServices 可选注入(无 sandbox/无 commands)不失败,
// 必需注入(ctx.tools)缺失显式失败。
func TestPluginStartOptionalAndRequiredServices(t *testing.T) {
	c := newJcCtx()
	c.cmds = nil // 无 TUI/无命令注册表
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatalf("可选服务缺失不应失败: %v", err)
	}
	defer dis()
	if !c.hasProvided("ctx.jobs") {
		t.Fatal("应提供 ctx.jobs")
	}
	if _, ok := c.tools.Get("job_list"); !ok {
		t.Fatal("无命令注册表时工具仍应注册")
	}

	missing := newJcCtx()
	missing.noTools = true
	if _, err := (&Plugin{}).Start(missing, &sdk.Manifest{}); err == nil ||
		!strings.Contains(err.Error(), "ctx.tools") {
		t.Fatalf("缺 ctx.tools 应显式失败: %v", err)
	}
	if missing.hasProvided("ctx.jobs") {
		t.Fatal("失败时不应留下已注册服务")
	}
}

func (c *jcCtx) hasProvided(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.provided[key]
	return ok
}

// —— /jobs 命令 ——

// TestJobsCmdBranches /jobs 全分支:用法提示、列表、取输出(命中/缺失/不存在)、
// 终止(命中/不存在)、未知子命令。
func TestJobsCmdBranches(t *testing.T) {
	c := newJcCtx()
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	j := c.jobsService(t)
	spec, _ := c.cmds.Get("jobs")

	if _, err := spec.Run(nil); err == nil || !strings.Contains(err.Error(), "/jobs list") {
		t.Fatalf("无参数应给用法: %v", err)
	}
	if _, err := spec.Run([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "/jobs list|output|kill") {
		t.Fatalf("未知子命令应给用法: %v", err)
	}

	runID, err := j.Run(func(context.Context) (any, error) { return map[string]any{"ok": true}, nil })
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, j, runID)

	out, err := spec.Run([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "后台任务:") || !strings.Contains(out, runID) || !strings.Contains(out, "map[ok:true]") {
		t.Fatalf("列表应含任务与结果摘要: %q", out)
	}

	if _, err := spec.Run([]string{"output"}); err == nil || !strings.Contains(err.Error(), "/jobs output <id>") {
		t.Fatalf("output 缺 id 应给用法: %v", err)
	}
	if _, err := spec.Run([]string{"output", "job_不存在"}); err == nil ||
		!strings.Contains(err.Error(), "任务不存在") {
		t.Fatalf("output 未知任务应报错: %v", err)
	}
	out, err = spec.Run([]string{"output", runID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, runID) || !strings.Contains(out, "map[ok:true]") {
		t.Fatalf("output 应回状态与结果: %q", out)
	}

	if _, err := spec.Run([]string{"kill"}); err == nil || !strings.Contains(err.Error(), "/jobs kill <id>") {
		t.Fatalf("kill 缺 id 应给用法: %v", err)
	}
	if _, err := spec.Run([]string{"kill", runID}); err == nil ||
		!strings.Contains(err.Error(), "不在运行") {
		t.Fatalf("kill 已完成任务应报错: %v", err)
	}
	slowID, err := j.Submit(`sleep 30`)
	if err != nil {
		t.Fatal(err)
	}
	out, err = spec.Run([]string{"kill", slowID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已终止 "+slowID) {
		t.Fatalf("kill 成功应回确认: %q", out)
	}
	if st := waitDone(t, j, slowID); st.State != sdk.JobKilled {
		t.Fatalf("被终止任务状态应为 killed: %s", st.State)
	}
	// 命令任务输出路径:命令任务有 Output 时优先回输出文本
	cmdID, err := j.Submit(`echo 命令输出`)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, j, cmdID)
	out, err = spec.Run([]string{"output", cmdID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "命令输出") || !strings.Contains(out, "命令: echo 命令输出") {
		t.Fatalf("命令任务应回输出文本: %q", out)
	}
}

// —— 模型工具 ——

// TestJobToolsListOutputKill job_list/job_output/job_kill 的过滤、命中、错误与坏参数。
func TestJobToolsListOutputKill(t *testing.T) {
	j := New(nil)
	ctx := context.Background()
	list := &ListTool{j: j}
	out := &OutputTool{j: j}
	kill := &KillTool{j: j}
	if list.Definition().Name != "job_list" || out.Definition().Name != "job_output" ||
		kill.Definition().Name != "job_kill" {
		t.Fatal("工具名应与宿主注册一致(模型按名调用)")
	}

	// 空列表:jobs 字段存在且为空数组
	v, err := list.Execute(ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if jobs, ok := m["jobs"].([]map[string]any); !ok || len(jobs) != 0 {
		t.Fatalf("空列表应回空数组: %#v", m["jobs"])
	}
	// 坏 JSON 容错(过滤器缺省 = all)
	if _, err := list.Execute(ctx, `{not json`); err != nil {
		t.Fatalf("坏参数不应报错(过滤器缺省): %v", err)
	}

	runningID, err := j.Submit(`sleep 30`)
	if err != nil {
		t.Fatal(err)
	}
	doneID, err := j.Run(func(context.Context) (any, error) { return "结果", nil })
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, j, doneID)

	v, _ = list.Execute(ctx, `{"filter":"running"}`)
	got := v.(map[string]any)["jobs"].([]map[string]any)
	if len(got) != 1 || got[0]["id"] != runningID {
		t.Fatalf("running 过滤应只剩运行中任务: %#v", got)
	}
	v, _ = list.Execute(ctx, `{"filter":"all"}`)
	if all := v.(map[string]any)["jobs"].([]map[string]any); len(all) != 2 {
		t.Fatalf("all 应含全部任务: %#v", all)
	}

	// job_output:命中 / 不存在(结构化 error)/ 坏参数(Go 错误)
	v, err = out.Execute(ctx, `{"id":"`+doneID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	jobViewMap, ok := v.(map[string]any)["job"].(map[string]any)
	if !ok || jobViewMap["id"] != doneID || jobViewMap["state"] != string(sdk.JobDone) {
		t.Fatalf("命中应回任务视图: %#v", v)
	}
	v, err = out.Execute(ctx, `{"id":"job_缺失"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(map[string]any)["error"]; !ok {
		t.Fatalf("不存在任务应回结构化错误: %#v", v)
	}
	if _, err := out.Execute(ctx, `{"id":123}`); err == nil {
		t.Fatal("非法参数应回 Go 错误(模型可见参数问题)")
	}

	// job_kill:成功 / 非运行中(结构化 error)/ 坏参数
	v, err = kill.Execute(ctx, `{"id":"`+runningID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["killed"] != runningID {
		t.Fatalf("终止成功应回 id: %#v", v)
	}
	if st := waitDone(t, j, runningID); st.State != sdk.JobKilled {
		t.Fatalf("应为 killed: %s", st.State)
	}
	v, err = kill.Execute(ctx, `{"id":"`+runningID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(map[string]any)["error"]; !ok {
		t.Fatalf("重复终止应回结构化错误: %#v", v)
	}
	if _, err := kill.Execute(ctx, `{`); err == nil {
		t.Fatal("坏参数应回 Go 错误")
	}
}

// TestJobViewShapes jobView/jobsView 的展示契约:长输出截断、可选字段按需出现。
func TestJobViewShapes(t *testing.T) {
	long := strings.Repeat("x", 2001)
	j := sdk.Job{ID: "job_1", State: sdk.JobDone, Command: "echo", Output: long,
		Error: "警告", Result: "结果", CreatedAt: time.Now(), DoneAt: time.Now()}
	v := jobView(j)
	if s, ok := v["output"].(string); !ok || !strings.HasSuffix(s, "…(截断)") || len(s) != 2000+len("…(截断)") {
		t.Fatalf("超长输出应截断标记: %#v", v["output"])
	}
	for _, k := range []string{"id", "state", "command", "created_at", "done_at", "error", "result"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("字段 %s 应存在: %#v", k, v)
		}
	}
	// 未完成/无输出:done_at 与 output 不出现(视图精简)
	bare := jobView(sdk.Job{ID: "job_2", State: sdk.JobRunning, Command: "sleep", CreatedAt: time.Now()})
	for _, k := range []string{"done_at", "error", "result", "output"} {
		if _, ok := bare[k]; ok {
			t.Fatalf("运行中任务不该有 %s: %#v", k, bare)
		}
	}
	if views := jobsView([]sdk.Job{j, j}); len(views) != 2 || views[1]["id"] != "job_1" {
		t.Fatalf("jobsView 应逐条映射: %#v", views)
	}
	if views := jobsView(nil); len(views) != 0 {
		t.Fatalf("空输入应回空切片: %#v", views)
	}
}

// TestCappedBufferTruncation 输出上限:超限后丢弃尾部并标记截断,
// 且必须继续返回成功(否则子进程会因 EPIPE 改变行为)。
func TestCappedBufferTruncation(t *testing.T) {
	b := &cappedBuffer{}
	chunk := []byte(strings.Repeat("a", 4096))
	written := 0
	for written+len(chunk) < maxJobOutput {
		if n, err := b.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("上限内写入应成功: n=%d err=%v", n, err)
		}
		written += len(chunk)
	}
	if b.truncated {
		t.Fatal("未超限不该标记截断")
	}
	// 跨越上限:部分写入 + 标记
	over := []byte(strings.Repeat("b", 8192))
	if n, err := b.Write(over); err != nil || n != len(over) {
		t.Fatalf("跨界写入应报成功(不触发 EPIPE): n=%d err=%v", n, err)
	}
	if !b.truncated {
		t.Fatal("跨界应标记截断")
	}
	// 超限后继续写:仍报成功且不再增长
	before := b.buf.Len()
	if n, err := b.Write(chunk); err != nil || n != len(chunk) {
		t.Fatalf("超限后写入仍应报成功: n=%d err=%v", n, err)
	}
	if b.buf.Len() != before {
		t.Fatalf("超限后不应继续增长: %d → %d", before, b.buf.Len())
	}
	s := b.String()
	if !strings.HasSuffix(s, "…(输出超 1048576 字节已截断)") || strings.Count(s, "已截断") != 1 {
		t.Fatalf("截断标记应恰一次且带字节数: %q", s[len(s)-40:])
	}
}

// TestStopRejectsNewTasksAndCancelsRunning Stop:取消运行中任务、拒绝新任务、幂等
// (卸载后不得残留继续跑的后台任务)。
func TestStopRejectsNewTasksAndCancelsRunning(t *testing.T) {
	j := New(nil)
	funcCancelled := make(chan struct{})
	funcID, err := j.Run(func(ctx context.Context) (any, error) {
		<-ctx.Done()
		close(funcCancelled)
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	cmdID, err := j.Submit(`sleep 30`)
	if err != nil {
		t.Fatal(err)
	}

	j.Stop()
	select {
	case <-funcCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop 应取消运行中的函数任务")
	}
	if st := waitDone(t, j, funcID); st.State != sdk.JobKilled {
		t.Fatalf("函数任务应为 killed: %s", st.State)
	}
	if st := waitDone(t, j, cmdID); st.State != sdk.JobKilled {
		t.Fatalf("命令任务应为 killed(进程组被杀): %s", st.State)
	}

	if _, err := j.Submit(`echo hi`); err == nil || !strings.Contains(err.Error(), "已卸载") {
		t.Fatalf("Stop 后应拒绝提交: %v", err)
	}
	if _, err := j.Run(func(context.Context) (any, error) { return nil, nil }); err == nil {
		t.Fatal("Stop 后应拒绝函数任务")
	}
	j.Stop() // 幂等
}

// TestConcurrentSubmitAndQuery 并发提交 + 并发 Kill/Output/List(-race 下无数据竞争,
// 状态查询始终自洽)。
func TestConcurrentSubmitAndQuery(t *testing.T) {
	j := New(nil)
	const n = 8
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id, err := j.Submit(`sleep 5`)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		// 立即并发查询
		var wg sync.WaitGroup
		wg.Add(3)
		go func(id string) {
			defer wg.Done()
			if _, ok := j.Output(id); !ok {
				t.Errorf("刚提交的任务应可查: %s", id)
			}
		}(id)
		go func() { defer wg.Done(); _ = j.List() }()
		go func() {
			defer wg.Done()
			if err := j.Kill(id); err != nil {
				t.Errorf("运行中任务应可 Kill: %v", err)
			}
		}()
		wg.Wait()
		if st := waitDone(t, j, id); st.State != sdk.JobKilled {
			t.Fatalf("应为 killed: %s", st.State)
		}
	}
	// List 保留已结束的历史(keepHistory 内),但不得残留运行中任务
	all := j.List()
	if len(all) != n {
		t.Fatalf("已结束任务应保留在历史里: got %d want %d", len(all), n)
	}
	for _, jb := range all {
		if jb.State == sdk.JobRunning {
			t.Fatalf("不应残留运行中任务: %+v", jb)
		}
	}
	// 并发只读查询(锁竞争路径)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = j.List() }()
		go func() { defer wg.Done(); _, _ = j.Output("job_不存在") }()
	}
	wg.Wait()
}
