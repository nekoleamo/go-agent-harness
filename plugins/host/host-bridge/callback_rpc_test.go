// callback_rpc_test.go 宿主回调通道全链测试(真实 TCP + net/rpc,进程内):
// 外部进程 → GAH_CB_ADDR Dial → CB.Call 分发(tools/jobs/fanout)→ 结果 JSON 回传。
// 覆盖方向为「外部插件请求宿主服务」的正向契约:鉴权、未知服务/方法、未装配服务的
// 显式错误,以及两侧代理(CbTools/CbJobs/CbFanout)与宿主服务的一一对账。
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 宿主服务替身 ——

// cbStubTools 仅记录调用并返回可预测结果的工具注册表。
type cbStubTools struct {
	defs    []sdk.ToolDefinition
	execErr error
	result  sdk.ToolResult
	order   []string // 记录实际执行过的工具名
}

func (s *cbStubTools) Register(sdk.Tool) sdk.Disposer { return func() {} }
func (s *cbStubTools) List() []sdk.ToolDefinition     { return s.defs }
func (s *cbStubTools) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range s.defs {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}
func (s *cbStubTools) Execute(_ context.Context, name, _ string) (*sdk.ToolResult, error) {
	s.order = append(s.order, name)
	if s.execErr != nil {
		return nil, s.execErr
	}
	return &sdk.ToolResult{Content: s.result.Content, Error: s.result.Error}, nil
}

var _ sdk.ToolRegistry = (*cbStubTools)(nil)

// cbStubJobs 任务服务替身:Kill 成功返回 nil error(host-jobs 契约)。
type cbStubJobs struct {
	jobs   []sdk.Job
	byID   map[string]sdk.Job
	killed []string
	killEr error
	runOut string
	runFn  sdk.JobFunc
}

func (s *cbStubJobs) Submit(string) (string, error) { return "job_submit", nil }
func (s *cbStubJobs) Run(fn sdk.JobFunc) (string, error) {
	s.runFn = fn
	return s.runOut, nil
}
func (s *cbStubJobs) List() []sdk.Job { return s.jobs }
func (s *cbStubJobs) Output(id string) (sdk.Job, bool) {
	j, ok := s.byID[id]
	return j, ok
}
func (s *cbStubJobs) Kill(id string) error {
	s.killed = append(s.killed, id)
	return s.killEr
}

var _ sdk.JobService = (*cbStubJobs)(nil)

// cbStubFanout 子代理编排替身(全接口,便于逐方法对账)。
type cbStubFanout struct {
	agentIn    string
	parallelIn []string
	pipelineIn []string
	spawnIn    string
	forkIn     string
	sentID     string
	sentMsg    string
	killAgents []string
	killEr     error
	sendEr     error
	agentEr    error
}

func (s *cbStubFanout) Agent(_ context.Context, input string) (string, error) {
	s.agentIn = input
	if s.agentEr != nil {
		return "", s.agentEr
	}
	return "子代理结果:" + input, nil
}
func (s *cbStubFanout) Parallel(_ context.Context, inputs []string) []sdk.FanoutResult {
	s.parallelIn = inputs
	out := make([]sdk.FanoutResult, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, sdk.FanoutResult{Input: in, Result: "R:" + in})
	}
	return out
}
func (s *cbStubFanout) Pipeline(_ context.Context, steps []string) ([]sdk.FanoutResult, string, error) {
	s.pipelineIn = steps
	out := make([]sdk.FanoutResult, 0, len(steps))
	for _, st := range steps {
		out = append(out, sdk.FanoutResult{Input: st, Result: "S:" + st})
	}
	return out, "最终:" + steps[len(steps)-1], nil
}
func (s *cbStubFanout) SpawnAgent(_ context.Context, input string) (string, error) {
	s.spawnIn = input
	return "ag-spawn", nil
}
func (s *cbStubFanout) Fork(_ context.Context, input string) (string, error) {
	s.forkIn = input
	return "ag-fork", nil
}
func (s *cbStubFanout) SendMessage(id, msg string) error {
	if s.sendEr != nil {
		return s.sendEr
	}
	s.sentID, s.sentMsg = id, msg
	return nil
}
func (s *cbStubFanout) ListAgents() []sdk.AgentHandle {
	return []sdk.AgentHandle{{ID: "ag1", Input: "任务一"}, {ID: "ag2", Input: "任务二"}}
}
func (s *cbStubFanout) AgentStatus(id string) (sdk.AgentHandle, bool) {
	if id != "ag1" {
		return sdk.AgentHandle{}, false
	}
	return sdk.AgentHandle{ID: "ag1", Input: "任务一", Result: "完成"}, true
}
func (s *cbStubFanout) KillAgent(id string) error {
	s.killAgents = append(s.killAgents, id)
	return s.killEr
}

var _ sdk.FanoutService = (*cbStubFanout)(nil)

// —— 通道构造 ——

// callbackEnv 起真实回调通道并返回外部侧客户端(生产路径:serveCallback + DialCallback)。
type callbackEnv struct {
	cb    *Callback
	tools *cbStubTools
	jobs  *cbStubJobs
	fan   *cbStubFanout
	cc    *CallbackClient
}

func newCallbackEnv(t *testing.T, token string) *callbackEnv {
	t.Helper()
	tools := &cbStubTools{defs: []sdk.ToolDefinition{{Name: "echo"}, {Name: "save_note"}}}
	jobs := &cbStubJobs{
		jobs:   []sdk.Job{{ID: "job_1", State: sdk.JobDone, Command: "echo hi"}},
		byID:   map[string]sdk.Job{"job_1": {ID: "job_1", State: sdk.JobDone, Command: "echo hi", Output: "hi\n"}},
		runOut: "job_run",
	}
	fan := &cbStubFanout{}
	cb := NewCallback(tools, jobs, fan, token)
	addr, stop, err := serveCallback(cb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	t.Setenv("GAH_CB_TOKEN", token) // DialCallback 从 env 取 token(宿主注入路径)
	cc, err := DialCallback(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return &callbackEnv{cb: cb, tools: tools, jobs: jobs, fan: fan, cc: cc}
}

// —— 工具回调 ——

// TestCallbackToolsProxy 外部侧 CbTools 代理 ↔ 宿主 tools.list/execute 对账。
func TestCallbackToolsProxy(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	tr := CbTools(e.cc)

	// Register 是 no-op(外部引擎不向宿主注册工具),Disposer 可安全调用
	tr.Register(nil)()

	defs := tr.List()
	if len(defs) != 2 || defs[0].Name != "echo" || defs[1].Name != "save_note" {
		t.Fatalf("List 应回传宿主工具定义: %+v", defs)
	}
	if d, ok := tr.Get("save_note"); !ok || d.Name != "save_note" {
		t.Fatalf("Get 命中失败: %+v %v", d, ok)
	}
	if _, ok := tr.Get("no_such"); ok {
		t.Fatal("Get 未命中应返回 false")
	}

	// Execute 成功:参数透传 + 结果 JSON 解码回 ToolResult
	e.tools.result = sdk.ToolResult{Content: `{"ok":true}`}
	res, err := tr.Execute(context.Background(), "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != `{"ok":true}` || res.Error != "" {
		t.Fatalf("Execute 结果不符: %+v", res)
	}
	if len(e.tools.order) != 1 || e.tools.order[0] != "echo" {
		t.Fatalf("宿主应收到工具调用: %v", e.tools.order)
	}

	// 工具业务错误:经 ToolResult.Error 回传(与宿主 execute 语义一致)
	e.tools.result = sdk.ToolResult{Error: "工具内部失败"}
	res, err = tr.Execute(context.Background(), "echo", `{}`)
	if err != nil || res.Error != "工具内部失败" {
		t.Fatalf("业务错误应经结果回传: %+v err=%v", res, err)
	}

	// 宿主侧 execute 报错(流水线 veto 等):宿主包成 ToolResult.Error 回传,
	// 外部侧拿到「有错误文本但 RPC 成功」的结果(与宿主 tools.Execute 语义一致)
	e.tools.execErr = errors.New("流水线被拒")
	res, err = tr.Execute(context.Background(), "echo", `{}`)
	if err != nil || res == nil || !strings.Contains(res.Error, "流水线被拒") {
		t.Fatalf("宿主侧错误应经结果文本回传: %+v err=%v", res, err)
	}
}

// —— 任务回调 ——

// TestCallbackJobsProxy 外部侧 CbJobs 代理 ↔ 宿主 jobs.* 对账(Kill 成功 = nil error)。
func TestCallbackJobsProxy(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	jb := CbJobs(e.cc)

	// Submit/Run 在外部进程无宿主上下文 → 显式拒绝(不静默返回空 ID)
	if _, err := jb.Submit("echo hi"); err == nil {
		t.Fatal("Submit 应显式报错")
	}
	if _, err := jb.Run(func(context.Context) (any, error) { return nil, nil }); err == nil {
		t.Fatal("Run 应显式报错")
	}

	list := jb.List()
	if len(list) != 1 || list[0].ID != "job_1" {
		t.Fatalf("List 应回传宿主任务: %+v", list)
	}
	job, ok := jb.Output("job_1")
	if !ok || job.Output != "hi\n" {
		t.Fatalf("Output 命中失败: %+v %v", job, ok)
	}
	if _, ok := jb.Output("job_missing"); ok {
		t.Fatal("Output 未命中应返回 false(宿主 jobs.output 报 job 不存在)")
	}

	// Kill 成功:宿主 reply 为空 → nil error(历史上 .Error() 解引用崩宿主的回归点)
	if err := jb.Kill("job_1"); err != nil {
		t.Fatalf("Kill 成功应返回 nil: %v", err)
	}
	if len(e.jobs.killed) != 1 || e.jobs.killed[0] != "job_1" {
		t.Fatalf("宿主应收到 kill: %v", e.jobs.killed)
	}
}

// TestCallbackJobsKillFailureReply 宿主侧 jobs.kill 业务失败经 reply 文本回传
// (客户端代理当前忽略该文本,见报告;此处锚定宿主侧契约本身)。
func TestCallbackJobsKillFailureReply(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	e.jobs.killEr = errors.New("host-jobs: 任务 job_1 不在运行")
	var reply string
	if err := e.cb.Call(CallArgs{Service: "jobs", Method: "kill", Args: `{"ID":"job_1"}`, Token: "tok"}, &reply); err != nil {
		t.Fatalf("业务失败不应是 RPC 错误: %v", err)
	}
	if !strings.Contains(reply, "不在运行") {
		t.Fatalf("宿主应把业务失败写进 reply: %q", reply)
	}
}

// TestCallbackJobsRunWrapsToolExecution jobs.run 在宿主侧包一层工具执行任务:
// 工具结果为 JSON → 结构化 Result;非 JSON → 原字符串。
func TestCallbackJobsRunWrapsToolExecution(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	var reply string
	if err := e.cb.Call(CallArgs{Service: "jobs", Method: "run", Args: `{"Name":"echo","Args":"{\"text\":\"hi\"}"}`, Token: "tok"}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply != "job_run" || e.jobs.runFn == nil {
		t.Fatalf("run 应回传任务 ID 并提交函数: reply=%q fn=%v", reply, e.jobs.runFn)
	}

	// 工具结果 JSON → 解码为结构化值
	e.tools.result = sdk.ToolResult{Content: `{"n":1}`}
	val, err := e.jobs.runFn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := val.(map[string]any); !ok || m["n"] != float64(1) {
		t.Fatalf("JSON 结果应结构化: %#v", val)
	}
	// 工具结果非 JSON → 原字符串
	e.tools.result = sdk.ToolResult{Content: "纯文本"}
	val, err = e.jobs.runFn(context.Background())
	if err != nil || val != "纯文本" {
		t.Fatalf("非 JSON 应原样回传: %#v err=%v", val, err)
	}
	// 工具业务错误 → {"error": ...}(任务视为成功,错误在内容里)
	e.tools.result = sdk.ToolResult{Error: "工具失败"}
	val, err = e.jobs.runFn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := val.(map[string]any); !ok || m["error"] != "工具失败" {
		t.Fatalf("业务错误应结构化: %#v", val)
	}
	// 流水线错误 → 任务失败(返回 Go 错误)
	e.tools.execErr = errors.New("流水线拒绝")
	if _, err := e.jobs.runFn(context.Background()); err == nil {
		t.Fatal("流水线错误应使任务失败")
	}
}

// —— 子代理回调 ——

// TestCallbackFanoutProxy 外部侧 CbFanout 代理 ↔ 宿主 fanout.* 全方法对账。
func TestCallbackFanoutProxy(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	f := CbFanout(e.cc)
	ctx := context.Background()

	if out, err := f.Agent(ctx, "写个方案"); err != nil || out != "子代理结果:写个方案" || e.fan.agentIn != "写个方案" {
		t.Fatalf("Agent 转发失败: out=%q err=%v in=%q", out, err, e.fan.agentIn)
	}
	par := f.Parallel(ctx, []string{"a", "b"})
	if len(par) != 2 || par[1].Result != "R:b" || len(e.fan.parallelIn) != 2 {
		t.Fatalf("Parallel 结果不符: %+v in=%v", par, e.fan.parallelIn)
	}
	steps, final, err := f.Pipeline(ctx, []string{"s1", "s2"})
	if err != nil || final != "最终:s2" || len(steps) != 2 || steps[0].Result != "S:s1" {
		t.Fatalf("Pipeline 结果不符: steps=%+v final=%q err=%v", steps, final, err)
	}
	if id, err := f.SpawnAgent(ctx, "后台任务"); err != nil || id != "ag-spawn" || e.fan.spawnIn != "后台任务" {
		t.Fatalf("SpawnAgent 失败: id=%q err=%v in=%q", id, err, e.fan.spawnIn)
	}
	agents := f.ListAgents()
	if len(agents) != 2 || agents[0].ID != "ag1" {
		t.Fatalf("ListAgents 不符: %+v", agents)
	}
	if h, ok := f.AgentStatus("ag1"); !ok || h.Result != "完成" {
		t.Fatalf("AgentStatus 命中失败: %+v %v", h, ok)
	}
	if _, ok := f.AgentStatus("ag_missing"); ok {
		t.Fatal("AgentStatus 未命中应返回 false")
	}
	if err := f.KillAgent("ag1"); err != nil {
		t.Fatalf("KillAgent 成功应 nil: %v", err)
	}
	if len(e.fan.killAgents) != 1 || e.fan.killAgents[0] != "ag1" {
		t.Fatalf("宿主应收到 kill: %v", e.fan.killAgents)
	}
	// 业务失败经 reply 文本 → 代理转 Go 错误
	e.fan.killEr = errors.New("已完成,不能终止")
	if err := f.KillAgent("ag1"); err == nil || !strings.Contains(err.Error(), "已完成") {
		t.Fatalf("业务失败应转 Go 错误: %v", err)
	}
	// Agent 的 Go 错误原样回传
	e.fan.agentEr = errors.New("子代理不可用")
	if _, err := f.Agent(ctx, "x"); err == nil || !strings.Contains(err.Error(), "子代理不可用") {
		t.Fatalf("Agent 错误应回传: %v", err)
	}
}

// —— 鉴权与错误面 ——

// TestCallbackAuthRejectsWrongToken 回调通道鉴权:token 不匹配/缺失即拒绝(防本机任意进程连回调)。
func TestCallbackAuthRejectsWrongToken(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	var reply string
	err := e.cb.Call(CallArgs{Service: "tools", Method: "list", Token: "bad"}, &reply)
	if err == nil || !strings.Contains(err.Error(), "鉴权失败") {
		t.Fatalf("错误 token 应拒绝: %v", err)
	}
	if err := e.cb.Call(CallArgs{Service: "tools", Method: "list"}, &reply); err == nil {
		t.Fatal("缺 token 应拒绝")
	}
	// 正确 token 放行
	if err := e.cb.Call(CallArgs{Service: "tools", Method: "list", Token: "tok"}, &reply); err != nil {
		t.Fatalf("正确 token 应放行: %v", err)
	}
	// 宿主未配 token(鉴权关闭)→ 任意 token 均放行(兼容旧外部二进制)
	open := NewCallback(e.tools, e.jobs, e.fan, "")
	if err := open.Call(CallArgs{Service: "tools", Method: "list", Token: ""}, &reply); err != nil {
		t.Fatalf("鉴权关闭时应放行: %v", err)
	}
}

// TestCallbackUnknownServiceAndMethod 未知服务/方法/坏参数一律显式报错(不静默返回空)。
func TestCallbackUnknownServiceAndMethod(t *testing.T) {
	e := newCallbackEnv(t, "tok")
	var reply string
	cases := []struct {
		name string
		args CallArgs
		want string
	}{
		{"未知服务", CallArgs{Service: "hooks", Method: "list"}, "未知服务"},
		{"tools 未知方法", CallArgs{Service: "tools", Method: "purge"}, "未知 tools 方法"},
		{"jobs 未知方法", CallArgs{Service: "jobs", Method: "purge"}, "未知 jobs 方法"},
		{"fanout 未知方法", CallArgs{Service: "fanout", Method: "purge"}, "未知 fanout 方法"},
		{"tools.execute 坏参数", CallArgs{Service: "tools", Method: "execute", Args: "{"}, "unexpected end"},
		{"jobs.kill 坏参数", CallArgs{Service: "jobs", Method: "kill", Args: "nolabel"}, "invalid character"},
		{"fanout.agent 坏参数", CallArgs{Service: "fanout", Method: "agent", Args: "[]"}, "cannot unmarshal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			args.Token = "tok"
			err := e.cb.Call(args, &reply)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应报 %q,实得 %v", tc.want, err)
			}
		})
	}
}

// TestCallbackUnassembledServices 宿主未装配 jobs/fanout 时对应回调显式报错(不静默降级)。
func TestCallbackUnassembledServices(t *testing.T) {
	cb := NewCallback(&cbStubTools{}, nil, nil, "")
	var reply string
	for _, m := range []string{"list", "kill", "run", "output"} {
		err := cb.Call(CallArgs{Service: "jobs", Method: m, Args: `{"ID":"j1"}`}, &reply)
		if err == nil || !strings.Contains(err.Error(), "未装配") {
			t.Fatalf("jobs.%s 未装配应显式报错: %v", m, err)
		}
	}
	err := cb.Call(CallArgs{Service: "fanout", Method: "agent", Args: `{"Input":"x"}`}, &reply)
	if err == nil || !strings.Contains(err.Error(), "未装配") {
		t.Fatalf("fanout 未装配应显式报错: %v", err)
	}
	if err := cb.Call(CallArgs{Service: "tools", Method: "list"}, &reply); err != nil {
		t.Fatalf("tools 已装配应可用: %v", err)
	}
	var defs []sdk.ToolDefinition
	if err := json.Unmarshal([]byte(reply), &defs); err != nil {
		t.Fatalf("tools.list 应回传 JSON 数组: %v", err)
	}
}

// TestDialCallbackRequiresAddr 缺 GAH_CB_ADDR → 显式错误(外部进程未挂回调通道时的诊断入口)。
func TestDialCallbackRequiresAddr(t *testing.T) {
	if _, err := DialCallback(""); err == nil || !strings.Contains(err.Error(), "GAH_CB_ADDR") {
		t.Fatalf("缺地址应显式报错: %v", err)
	}
	if _, err := DialCallback("127.0.0.1:1"); err == nil {
		t.Fatal("连接失败应报错")
	}
}

// —— 文件改动回传(change.record,S-P1-1 外部化补齐) ——

// cbChangesEnv 起回调通道并注入落账 sink(记录收到的 FileChangeEvent)。
type cbChangesEnv struct {
	*callbackEnv
	sink []sdk.FileChangeEvent
}

func newChangesEnv(t *testing.T, token string, fail bool) *cbChangesEnv {
	t.Helper()
	e := newCallbackEnv(t, token)
	env := &cbChangesEnv{callbackEnv: e}
	e.cb.SetChangeSink(func(ev sdk.FileChangeEvent) error {
		if fail {
			return errors.New("账本写失败")
		}
		env.sink = append(env.sink, ev)
		return nil
	})
	return env
}

// TestCallbackChangeRecordProxy 外部侧 CbChanges ↔ 宿主 change.record 对账(真 RPC 往返)。
func TestCallbackChangeRecordProxy(t *testing.T) {
	env := newChangesEnv(t, "tok", false)
	rec := CbChanges(env.cc)
	if _, ok := rec.(sdk.FileChangeRecorder); !ok {
		t.Fatal("CbChanges 应实现 sdk.FileChangeRecorder")
	}
	ev := sdk.BuildFileChange("/ws/a.txt", "", "write", "file_write", true, "", "hi\n")
	if err := rec.RecordChange(ev); err != nil {
		t.Fatalf("回传应成功: %v", err)
	}
	if len(env.sink) != 1 {
		t.Fatalf("宿主应收到 1 条,得到 %d 条", len(env.sink))
	}
	got := env.sink[0]
	if got.Path != "/ws/a.txt" || got.Op != "write" || got.Added != 1 || !strings.Contains(got.Diff, "+hi") {
		t.Fatalf("跨桥后事件内容不符: %+v", got)
	}
	if got.Rel != "" {
		t.Fatalf("Rel 由宿主侧组装层补算,回调层应原样透传: %q", got.Rel)
	}
}

// TestCallbackChangeRecordErrors 未装配 sink / 落账失败 / 方法名错 → 全部显式报错(不静默丢审计)。
func TestCallbackChangeRecordErrors(t *testing.T) {
	// 未注入 sink
	plain := newCallbackEnv(t, "")
	rec := CbChanges(plain.cc)
	if err := rec.RecordChange(sdk.FileChangeEvent{Path: "/ws/a.txt"}); err == nil || !strings.Contains(err.Error(), "未装配") {
		t.Fatalf("未装配 sink 应显式报错: %v", err)
	}
	// 落账失败:错误要经 reply 传回外部侧(不能当成功)
	failing := newChangesEnv(t, "", true)
	if err := CbChanges(failing.cc).RecordChange(sdk.FileChangeEvent{Path: "/ws/a.txt"}); err == nil || !strings.Contains(err.Error(), "落账失败") {
		t.Fatalf("落账失败应回错: %v", err)
	}
	// 方法名错 / 缺 path
	ok := newChangesEnv(t, "", false)
	var reply string
	if err := ok.cb.changeCall(context.Background(), "delete", "{}", &reply); err == nil || !strings.Contains(err.Error(), "未知方法") {
		t.Fatalf("未知方法应报错: %v", err)
	}
	if err := ok.cb.changeCall(context.Background(), "record", "{}", &reply); err == nil || !strings.Contains(err.Error(), "缺 path") {
		t.Fatalf("缺 path 应报错: %v", err)
	}
	if err := ok.cb.changeCall(context.Background(), "record", "{bad json", &reply); err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("坏 JSON 应报错: %v", err)
	}
	if len(ok.sink) != 0 {
		t.Fatalf("报错路径不应落账: %d 条", len(ok.sink))
	}
}

// TestCallbackUnknownService 未知服务仍报错(新增 change 服务后不改变既有契约)。
func TestCallbackUnknownService(t *testing.T) {
	e := newCallbackEnv(t, "")
	var reply string
	if err := e.cb.Call(CallArgs{Service: "theme", Method: "set"}, &reply); err == nil || !strings.Contains(err.Error(), "未知服务") {
		t.Fatalf("未知服务应报错: %v", err)
	}
}
