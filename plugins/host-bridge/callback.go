// callback.go:宿主回调通道(外部进程 → 宿主服务,最小双向 IPC,M6.8 外部化扩展)。
// 方向与桥主协议相反:外部进程持 GAH_CB_ADDR,经 net/rpc Dial 宿主回调端口,
// 请求宿主服务(tools.list/execute、jobs.run/output、fanout.agent/parallel/pipeline)。
// 用途:tool-workflow/tool-mcp 等需要宿主服务的工具类插件外部化后仍可组合执行。
// 设计取舍:仅桥“纯函数服务”(工具执行/任务/子代理),不桥事件 veto(与 P2 边界一致)。
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/rpc"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 宿主侧:Callback RPC 服务 ——

// CallArgs 一次回调请求(服务 + 方法 + JSON 参数)。
type CallArgs struct {
	Service string
	Method  string
	Args    string
}

// Callback 宿主侧回调服务(tools/jobs/fanout,经 Ctx 注入)。
type Callback struct {
	tools  sdk.ToolRegistry
	jobs   sdk.JobService
	fanout sdk.FanoutService
}

// NewCallback 构造回调服务(jobs/fanout 可为 nil;对应方法返回显式错误)。
func NewCallback(tools sdk.ToolRegistry, jobs sdk.JobService, fanout sdk.FanoutService) *Callback {
	return &Callback{tools: tools, jobs: jobs, fanout: fanout}
}

// Call 执行一次宿主服务调用,结果(JSON 字符串)写入 reply。
func (cb *Callback) Call(args CallArgs, reply *string) error {
	ctx := context.Background()
	switch args.Service {
	case "tools":
		return cb.toolsCall(ctx, args.Method, args.Args, reply)
	case "jobs":
		return cb.jobsCall(ctx, args.Method, args.Args, reply)
	case "fanout":
		return cb.fanoutCall(ctx, args.Method, args.Args, reply)
	}
	return errors.New("callback: 未知服务 " + args.Service)
}

// toolsCall tools.list/tools.execute(工具定义与执行,经全流水线)。
func (cb *Callback) toolsCall(ctx context.Context, method, raw string, reply *string) error {
	switch method {
	case "list":
		b, err := json.Marshal(cb.tools.List())
		if err != nil {
			return err
		}
		*reply = string(b)
		return nil
	case "execute":
		var p struct {
			Name string
			Args string
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		res, err := cb.tools.Execute(ctx, p.Name, p.Args)
		if err != nil {
			b, _ := json.Marshal(sdk.ToolResult{Error: "回调执行失败: " + err.Error()})
			*reply = string(b)
			return nil
		}
		b, _ := json.Marshal(res)
		*reply = string(b)
		return nil
	}
	return errors.New("callback: 未知 tools 方法 " + method)
}

// jobsCall jobs.run(宿主起任务执行外部工具)/jobs.output(取状态)。
func (cb *Callback) jobsCall(ctx context.Context, method, raw string, reply *string) error {
	switch method {
	case "list":
		if cb.jobs == nil {
			return errors.New("callback: ctx.jobs 未装配(host-jobs)")
		}
		b, err := json.Marshal(cb.jobs.List())
		if err != nil {
			return err
		}
		*reply = string(b)
		return nil
	case "kill":
		if cb.jobs == nil {
			return errors.New("callback: ctx.jobs 未装配(host-jobs)")
		}
		var p struct {
			ID string
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		*reply = cb.jobs.Kill(p.ID).Error()
		return nil
	case "run":
		if cb.jobs == nil {
			return errors.New("callback: ctx.jobs 未装配(host-jobs)")
		}
		var p struct {
			Name string // 外部注册的工具名(宿主侧已存在,如 workflow)
			Args string // 工具参数 JSON
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		id, err := cb.jobs.Run(func(ctx context.Context) (any, error) {
			res, err := cb.tools.Execute(ctx, p.Name, p.Args)
			if err != nil {
				return nil, err
			}
			if res.Error != "" {
				return map[string]any{"error": res.Error}, nil
			}
			var val any
			if err := json.Unmarshal([]byte(res.Content), &val); err == nil {
				return val, nil
			}
			return res.Content, nil
		})
		if err != nil {
			return err
		}
		*reply = id
		return nil
	case "output":
		if cb.jobs == nil {
			return errors.New("callback: ctx.jobs 未装配(host-jobs)")
		}
		var p struct {
			ID string
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		job, ok := cb.jobs.Output(p.ID)
		if !ok {
			return errors.New("callback: job 不存在 " + p.ID)
		}
		b, err := json.Marshal(job)
		if err != nil {
			return err
		}
		*reply = string(b)
		return nil
	}
	return errors.New("callback: 未知 jobs 方法 " + method)
}

// fanoutCall 子代理编排回调(agent/parallel/pipeline)。
func (cb *Callback) fanoutCall(ctx context.Context, method, raw string, reply *string) error {
	if cb.fanout == nil {
		return errors.New("callback: ctx.fanout 未装配(host-fanout)")
	}
	switch method {
	case "agent":
		var p struct {
			Input string
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		res, err := cb.fanout.Agent(ctx, p.Input)
		if err != nil {
			return err
		}
		*reply = res
		return nil
	case "parallel":
		var p struct {
			Inputs []string
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		b, err := json.Marshal(cb.fanout.Parallel(ctx, p.Inputs))
		if err != nil {
			return err
		}
		*reply = string(b)
		return nil
	case "pipeline":
		var p struct {
			Steps []string
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		steps, final, err := cb.fanout.Pipeline(ctx, p.Steps)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(map[string]any{"steps": steps, "final": final})
		*reply = string(b)
		return nil
	}
	return errors.New("callback: 未知 fanout 方法 " + method)
}

// serveCallback 起回调监听端口,返回地址与关闭函数(每个 Bridge 一个端口,所有外部进程共用)。
func serveCallback(cb *Callback) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := rpc.NewServer()
	if err := srv.RegisterName("CB", cb); err != nil {
		ln.Close()
		return "", nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.ServeConn(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }, nil
}

// —— 外部侧:回调客户端与 sdk 接口代理 ——

// CallbackClient 外部进程侧回调连接(经 GAH_CB_ADDR Dial 宿主)。
type CallbackClient struct {
	cl *rpc.Client
}

// DialCallback 连接宿主回调服务(地址来自宿主注入环境变量 GAH_CB_ADDR)。
func DialCallback(addr string) (*CallbackClient, error) {
	if addr == "" {
		return nil, errors.New("callback: 缺 GAH_CB_ADDR(宿主未开启回调通道)")
	}
	cl, err := rpc.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &CallbackClient{cl: cl}, nil
}

// Call 请求宿主服务(service.method)。
func (cc *CallbackClient) Call(service, method string, args any, reply *string) error {
	raw := ""
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	return cc.cl.Call("CB.Call", CallArgs{Service: service, Method: method, Args: raw}, reply)
}

// Close 关闭回调连接。
func (cc *CallbackClient) Close() error { return cc.cl.Close() }

// cbTools sdk.ToolRegistry 的回调代理(外部引擎只读使用;Register 为 no-op)。
type cbTools struct{ cc *CallbackClient }

// CbTools 构造工具注册表回调代理(供外部进程引擎注入)。
func CbTools(cc *CallbackClient) sdk.ToolRegistry { return &cbTools{cc: cc} }

func (t *cbTools) Register(_ sdk.Tool) sdk.Disposer {
	return func() {} // 外部引擎不向宿主注册工具
}
func (t *cbTools) List() []sdk.ToolDefinition {
	var s string
	if err := t.cc.Call("tools", "list", nil, &s); err != nil {
		return nil
	}
	var defs []sdk.ToolDefinition
	if json.Unmarshal([]byte(s), &defs) != nil {
		return nil
	}
	return defs
}
func (t *cbTools) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range t.List() {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}
func (t *cbTools) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	var s string
	if err := t.cc.Call("tools", "execute", map[string]string{"Name": name, "Args": args}, &s); err != nil {
		return &sdk.ToolResult{Error: "回调工具执行失败: " + err.Error()}, err
	}
	var res sdk.ToolResult
	if json.Unmarshal([]byte(s), &res) != nil {
		return &sdk.ToolResult{Content: s}, nil
	}
	return &res, nil
}

// cbJobs sdk.JobService 回调代理(外部引擎取任务状态;提交经宿主 jobs.run)。
type cbJobs struct{ cc *CallbackClient }

// CbJobs 构造后台任务回调代理。
func CbJobs(cc *CallbackClient) sdk.JobService { return &cbJobs{cc: cc} }

func (j *cbJobs) Submit(_ string) (string, error) {
	return "", errors.New("提交由宿主 jobs.run 承担(外部进程无宿主上下文)")
}
func (j *cbJobs) Run(_ sdk.JobFunc) (string, error) {
	return "", errors.New("提交由宿主 jobs.run 承担(外部进程无宿主上下文)")
}
func (j *cbJobs) List() []sdk.Job {
	var s string
	if err := j.cc.Call("jobs", "list", nil, &s); err != nil {
		return nil
	}
	var jobs []sdk.Job
	if json.Unmarshal([]byte(s), &jobs) != nil {
		return nil
	}
	return jobs
}
func (j *cbJobs) Output(id string) (sdk.Job, bool) {
	var s string
	if err := j.cc.Call("jobs", "output", map[string]string{"ID": id}, &s); err != nil {
		return sdk.Job{}, false
	}
	var job sdk.Job
	if json.Unmarshal([]byte(s), &job) != nil {
		return sdk.Job{}, false
	}
	return job, true
}
func (j *cbJobs) Kill(id string) error {
	var s string
	return j.cc.Call("jobs", "kill", map[string]string{"ID": id}, &s)
}

// cbFanout sdk.FanoutService 回调代理。
type cbFanout struct{ cc *CallbackClient }

// CbFanout 构造子代理编排回调代理。
func CbFanout(cc *CallbackClient) sdk.FanoutService { return &cbFanout{cc: cc} }

func (f *cbFanout) Agent(_ context.Context, input string) (string, error) {
	var s string
	if err := f.cc.Call("fanout", "agent", map[string]string{"Input": input}, &s); err != nil {
		return "", err
	}
	return s, nil
}
func (f *cbFanout) Parallel(_ context.Context, inputs []string) []sdk.FanoutResult {
	var s string
	if err := f.cc.Call("fanout", "parallel", map[string]any{"Inputs": inputs}, &s); err != nil {
		return nil
	}
	var res []sdk.FanoutResult
	if json.Unmarshal([]byte(s), &res) != nil {
		return nil
	}
	return res
}
func (f *cbFanout) Pipeline(_ context.Context, steps []string) ([]sdk.FanoutResult, string, error) {
	var s string
	if err := f.cc.Call("fanout", "pipeline", map[string]any{"Steps": steps}, &s); err != nil {
		return nil, "", err
	}
	var r struct {
		Steps []sdk.FanoutResult `json:"steps"`
		Final string             `json:"final"`
	}
	if json.Unmarshal([]byte(s), &r) != nil {
		return nil, "", errors.New("回调 pipeline 解析失败")
	}
	return r.Steps, r.Final, nil
}
