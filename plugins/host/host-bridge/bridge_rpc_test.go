// bridge_rpc_test.go 宿主侧(R10 覆盖率补强):不拉起真实外部进程,用进程内 net/rpc
// 服务端扮演「外部插件」,直调宿主侧代理(toolRPCClient/commandRPCClient)——
// 覆盖协议回退、结果上限、超时/取消、连接断裂节流、命令参数级联等跨进程契约分支;
// 另含热重载/卸载(真实二进制)与外部插件凭据隔离的端到端锚定。
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 最小工具注册表(记录注册/撤销,Execute 直调已注册工具)——

type recRegistry struct {
	mu sync.Mutex
	m  map[string]sdk.Tool
}

func newRecRegistry() *recRegistry { return &recRegistry{m: map[string]sdk.Tool{}} }

func (r *recRegistry) Register(t sdk.Tool) sdk.Disposer {
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
func (r *recRegistry) List() []sdk.ToolDefinition {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdk.ToolDefinition, 0, len(r.m))
	for _, t := range r.m {
		out = append(out, t.Definition())
	}
	return out
}
func (r *recRegistry) Get(name string) (sdk.ToolDefinition, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.m[name]
	if !ok {
		return sdk.ToolDefinition{}, false
	}
	return t.Definition(), true
}
func (r *recRegistry) Execute(ctx context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	t, ok := r.m[name]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("未注册工具 %s", name)
	}
	v, err := t.Execute(ctx, args)
	if err != nil {
		return &sdk.ToolResult{Error: err.Error()}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return &sdk.ToolResult{Content: fmt.Sprint(v)}, nil
	}
	return &sdk.ToolResult{Content: string(b)}, nil
}

var _ sdk.ToolRegistry = (*recRegistry)(nil)

// —— 进程内「外部插件」RPC 服务端 ——

// fakeMultiPlugin 新协议(Definitions/ExecuteNamed/…)替身。
type fakeMultiPlugin struct {
	mu       sync.Mutex
	calls    []string
	defs     []defDTO
	named    ExecReply
	legacy   ExecReply
	opts     string
	cmds     []CommandDTO
	cmdReply ExecReply
	sleep    time.Duration
	cancels  []string
	lastArgs ExecNamedArgs
}

func (p *fakeMultiPlugin) note(name string) {
	p.mu.Lock()
	p.calls = append(p.calls, name)
	p.mu.Unlock()
}
func (p *fakeMultiPlugin) called(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.calls {
		if c == name {
			return true
		}
	}
	return false
}
func (p *fakeMultiPlugin) Definitions(_ struct{}, reply *string) error {
	p.note("Definitions")
	b, err := json.Marshal(p.defs)
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}
func (p *fakeMultiPlugin) ExecuteNamed(args *ExecNamedArgs, reply *ExecReply) error {
	p.note("ExecuteNamed")
	p.mu.Lock()
	p.lastArgs = *args
	sleep := p.sleep
	p.mu.Unlock()
	if sleep > 0 {
		time.Sleep(sleep)
	}
	*reply = p.named
	return nil
}
func (p *fakeMultiPlugin) Execute(_ *ExecArgs, reply *ExecReply) error {
	p.note("Execute")
	*reply = p.legacy
	return nil
}
func (p *fakeMultiPlugin) Cancel(args *CancelArgs, reply *bool) error {
	p.note("Cancel")
	p.mu.Lock()
	p.cancels = append(p.cancels, args.CallID)
	p.mu.Unlock()
	*reply = true
	return nil
}
func (p *fakeMultiPlugin) Commands(_ struct{}, reply *string) error {
	p.note("Commands")
	b, err := json.Marshal(p.cmds)
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}
func (p *fakeMultiPlugin) CommandOptions(_ *CmdOptionsArgs, reply *string) error {
	p.note("CommandOptions")
	*reply = p.opts
	return nil
}
func (p *fakeMultiPlugin) RunCommand(_ *RunCommandArgs, reply *ExecReply) error {
	p.note("RunCommand")
	*reply = p.cmdReply
	return nil
}

// fakeLegacyPlugin 旧单工具协议替身(只有 Definition/Execute):覆盖宿主侧协议回退。
type fakeLegacyPlugin struct {
	mu      sync.Mutex
	calls   []string
	def     sdk.ToolDefinition
	reply   ExecReply
	lastArg ExecArgs
}

func (p *fakeLegacyPlugin) note(name string) {
	p.mu.Lock()
	p.calls = append(p.calls, name)
	p.mu.Unlock()
}
func (p *fakeLegacyPlugin) Definition(_ struct{}, reply *string) error {
	p.note("Definition")
	b, err := json.Marshal(p.def)
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}
func (p *fakeLegacyPlugin) Execute(args *ExecArgs, reply *ExecReply) error {
	p.note("Execute")
	p.mu.Lock()
	p.lastArg = *args
	p.mu.Unlock()
	*reply = p.reply
	return nil
}

// serveFake 起进程内 net/rpc 服务端(注册名 Plugin,与桥协议一致)。
func serveFake(t *testing.T, rcvr any) *rpc.Client {
	t.Helper()
	srv := rpc.NewServer()
	if err := srv.RegisterName("Plugin", rcvr); err != nil {
		t.Fatal(err)
	}
	cli, srvConn := net.Pipe()
	go srv.ServeConn(srvConn)
	cl := rpc.NewClient(cli)
	t.Cleanup(func() {
		_ = cl.Close()
		_ = cli.Close()
		_ = srvConn.Close()
	})
	return cl
}

// fakeBridgeEnv 构造 Bridge + 注册表 + 一个指向进程内 rpc 的条目。
type fakeBridgeEnv struct {
	b    *Bridge
	reg  *recRegistry
	path string
	e    *extEntry
}

func newFakeBridgeEnv(t *testing.T, cl *rpc.Client, name string, def sdk.ToolDefinition) *fakeBridgeEnv {
	t.Helper()
	reg := newRecRegistry()
	b := &Bridge{dir: t.TempDir(), tools: reg, lg: slog.New(slog.DiscardHandler), entries: map[string]*extEntry{}}
	path := filepath.Join(b.dir, "tool-fake")
	e := &extEntry{client: cl, kill: func() {}, unreg: func() {}, tools: map[string]*toolRPCClient{}}
	e.tools[name] = &toolRPCClient{br: b, path: path, name: name, def: def}
	e.unreg = b.registerAll(e)
	b.mu.Lock()
	b.entries[path] = e
	b.mu.Unlock()
	return &fakeBridgeEnv{b: b, reg: reg, path: path, e: e}
}

// toolOf 取出已注册的工具(测试直调 sdk.Tool,避免再经注册表 JSON 包装)。
func (e *fakeBridgeEnv) toolOf(t *testing.T, name string) sdk.Tool {
	t.Helper()
	e.reg.mu.Lock()
	defer e.reg.mu.Unlock()
	tool, ok := e.reg.m[name]
	if !ok {
		t.Fatalf("工具 %s 未注册", name)
	}
	return tool
}

// exec 直调已注册工具(默认 ctx)。
func (e *fakeBridgeEnv) exec(t *testing.T, name, args string) (any, error) {
	t.Helper()
	return e.toolOf(t, name).Execute(context.Background(), args)
}

// errText 取结构化错误文本(外部调用失败一律回 {"error": ...},不中断回合)。
func errText(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["error"].(string); ok {
			return s
		}
	}
	return ""
}

// TestToolRPCClientResponseShapes 结果解码三态:JSON → 结构化;非 JSON → 原字符串;
// 插件业务错误 → 结构化 {"error"}。
func TestToolRPCClientResponseShapes(t *testing.T) {
	p := &fakeMultiPlugin{}
	env := newFakeBridgeEnv(t, serveFake(t, p), "echo", sdk.ToolDefinition{Name: "echo"})

	p.named = ExecReply{Content: `{"text":"你好","n":2}`}
	v, err := env.exec(t, "echo", `{"text":"你好"}`)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok || m["text"] != "你好" || m["n"] != float64(2) {
		t.Fatalf("JSON 结果应解码为结构化值: %#v", v)
	}
	if p.lastArgs.JSONArgs != `{"text":"你好"}` {
		t.Fatalf("参数应原样下发: %+v", p.lastArgs)
	}

	p.named = ExecReply{Content: "纯文本结果"}
	v, err = env.exec(t, "echo", `{}`)
	if err != nil || v != "纯文本结果" {
		t.Fatalf("非 JSON 结果应原样回传: %#v err=%v", v, err)
	}

	p.named = ExecReply{Error: "外部插件内部失败"}
	v, err = env.exec(t, "echo", `{}`)
	if err != nil || !strings.Contains(errText(v), "外部插件内部失败") {
		t.Fatalf("插件业务错误应结构化: %#v err=%v", v, err)
	}
}

// TestToolRPCClientOversizeResultRejected 结果尺寸上限:gob 解码不受限,
// 超大结果必须结构化拒绝而不是全量进宿主内存。
func TestToolRPCClientOversizeResultRejected(t *testing.T) {
	p := &fakeMultiPlugin{named: ExecReply{Content: strings.Repeat("x", maxBridgeResult+1)}}
	env := newFakeBridgeEnv(t, serveFake(t, p), "big", sdk.ToolDefinition{Name: "big"})
	v, err := env.exec(t, "big", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errText(v), "结果超限") || !strings.Contains(errText(v), fmt.Sprint(maxBridgeResult)) {
		t.Fatalf("超限结果应显式报错: %#v", v)
	}
}

// TestToolRPCClientLegacyProtocolFallback 旧单工具插件(无 ExecuteNamed):
// "can't find method" → 自动回退 Plugin.Execute,调用成功且参数/CallID 照常携带。
func TestToolRPCClientLegacyProtocolFallback(t *testing.T) {
	p := &fakeLegacyPlugin{
		def:   sdk.ToolDefinition{Name: "legacy"},
		reply: ExecReply{Content: `"旧协议结果"`},
	}
	env := newFakeBridgeEnv(t, serveFake(t, p), "legacy", sdk.ToolDefinition{Name: "legacy"})
	v, err := env.exec(t, "legacy", `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "旧协议结果" {
		t.Fatalf("结果应来自旧协议 Execute: %#v", v)
	}
	if len(p.calls) != 1 || p.calls[0] != "Execute" {
		t.Fatalf("旧插件只应收到 Execute(ExecuteNamed 方法不存在): %v", p.calls)
	}
	if p.lastArg.JSONArgs != `{"a":1}` || p.lastArg.CallID == "" {
		t.Fatalf("回退路径也应携带参数与 CallID: %+v", p.lastArg)
	}
}

// TestToolRPCClientTimeoutCancelAndDeadline 超时/取消三条路径:
// 工具声明超时 → 结构化「应声明 timeout_ms」提示(且不触发崩溃重建);
// ctx 已取消 → 立即返回取消;ctx 截止时间收紧超时 → 不拖住调用方。
func TestToolRPCClientTimeoutCancelAndDeadline(t *testing.T) {
	p := &fakeMultiPlugin{sleep: 400 * time.Millisecond, named: ExecReply{Content: `"ok"`}}
	env := newFakeBridgeEnv(t, serveFake(t, p), "slow", sdk.ToolDefinition{Name: "slow", TimeoutMs: 40})

	v, err := env.exec(t, "slow", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errText(v), "超时") || !strings.Contains(errText(v), "timeout_ms") {
		t.Fatalf("声明超时应回提示: %#v", v)
	}
	env.b.mu.RLock()
	at := env.e.respawnAt
	env.b.mu.RUnlock()
	if !at.IsZero() {
		t.Fatal("超时不是连接错误,不应触发崩溃重建(节流时间戳应保持零值)")
	}

	// ctx 已取消:直接返回取消(不发 RPC)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	v, err = env.toolOf(t, "slow").Execute(canceled, `{}`)
	if err != nil || !strings.Contains(errText(v), "取消") {
		t.Fatalf("已取消 ctx 应回取消: %#v err=%v", v, err)
	}

	// ctx 截止时间紧于工具超时:按 ctx 收紧,快速返回
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	start := time.Now()
	v, err = env.toolOf(t, "slow").Execute(ctx, `{}`)
	if err != nil || !strings.Contains(errText(v), "超时") {
		t.Fatalf("ctx 截止应回超时: %#v err=%v", v, err)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("应按 ctx 截止快速返回,实际 %s", d)
	}
}

// TestToolRPCClientConnLossThrottled 连接断裂 → 结构化错误 + 崩溃自动拉起(60s 节流:
// 第二次调用不得重复排重建),卸载后(onDead)不再拉起。
func TestToolRPCClientConnLossThrottled(t *testing.T) {
	p := &fakeMultiPlugin{named: ExecReply{Content: `"ok"`}}
	cl := serveFake(t, p)
	env := newFakeBridgeEnv(t, cl, "echo", sdk.ToolDefinition{Name: "echo"})

	if err := cl.Close(); err != nil {
		t.Fatal(err)
	}
	v, err := env.exec(t, "echo", `{}`)
	if err != nil || !strings.Contains(errText(v), "自动重建中") {
		t.Fatalf("断连应回崩溃重建提示: %#v err=%v", v, err)
	}
	env.b.mu.RLock()
	first := env.e.respawnAt
	env.b.mu.RUnlock()
	if first.IsZero() || !first.After(time.Now()) {
		t.Fatalf("应设置重建节流时间戳: %v", first)
	}
	if _, err := env.exec(t, "echo", `{}`); err != nil {
		t.Fatal(err)
	}
	env.b.mu.RLock()
	second := env.e.respawnAt
	env.b.mu.RUnlock()
	if !second.Equal(first) {
		t.Fatalf("节流内不应重复排重建: %v → %v", first, second)
	}

	// 卸载后:onDead 直接返回,不再拉起(不留「复活」进程)
	env.b.closed = true
	env.e.respawnAt = time.Time{}
	v, _ = env.exec(t, "echo", `{}`)
	if !strings.Contains(errText(v), "自动重建中") {
		t.Fatalf("卸载后仍应回结构化错误: %#v", v)
	}
	env.b.mu.RLock()
	after := env.e.respawnAt
	env.b.mu.RUnlock()
	if !after.IsZero() {
		t.Fatal("卸载后不应再排重建")
	}
}

// TestToolRPCClientNoClientDuringRebuild 条目缺失(重建中)→ 结构化提示,不 panic。
func TestToolRPCClientNoClientDuringRebuild(t *testing.T) {
	p := &fakeMultiPlugin{}
	env := newFakeBridgeEnv(t, serveFake(t, p), "echo", sdk.ToolDefinition{Name: "echo"})
	env.b.mu.Lock()
	delete(env.b.entries, env.path)
	env.b.mu.Unlock()
	v, err := env.exec(t, "echo", `{}`)
	if err != nil || !strings.Contains(errText(v), "重建中") {
		t.Fatalf("条目缺失应回重建提示: %#v err=%v", v, err)
	}
}

// TestCommandRPCClientArgsAndEnumOptions 外部命令参数级联:
// Enum 级经 CommandOptions RPC 求值;FreeArgs 级静态返回;未声明级两者皆空;
// RPC 失败/空回复/非法 JSON → nil(选择器退化为直接执行,不阻塞 UI)。
func TestCommandRPCClientArgsAndEnumOptions(t *testing.T) {
	opts, _ := json.Marshal([]sdk.Option{{Value: "a", Desc: "选项A"}, {Value: "b"}})
	p := &fakeMultiPlugin{opts: string(opts)}
	env := newFakeBridgeEnv(t, serveFake(t, p), "cmdtool", sdk.ToolDefinition{Name: "cmdtool"})
	c := &commandRPCClient{
		br: env.b, path: env.path, name: "c",
		def: CommandDTO{Name: "c", TimeoutMs: 0, Args: []CommandArgDTO{
			{Enum: true}, {FreeArgs: []string{"文本", "目标?"}}, {},
		}},
	}
	levels := c.args()
	if len(levels) != 3 {
		t.Fatalf("应生成 3 级: %+v", levels)
	}
	if levels[0].Options == nil || levels[1].FreeArgs == nil {
		t.Fatalf("级类型应保真: %+v", levels)
	}
	if levels[2].Options != nil || levels[2].FreeArgs != nil {
		t.Fatalf("未声明级应两者皆空: %+v", levels[2])
	}
	got := levels[0].Options([]string{"x"})
	if len(got) != 2 || got[0].Value != "a" || got[0].Desc != "选项A" {
		t.Fatalf("枚举选项应来自 RPC: %+v", got)
	}
	if names := levels[1].FreeArgs(nil); len(names) != 2 || names[1] != "目标?" {
		t.Fatalf("自由级参数名应从声明回填: %v", names)
	}
	if !p.called("CommandOptions") {
		t.Fatal("枚举级应经 CommandOptions RPC 求值")
	}

	// 空回复 / 非法 JSON / 连接断裂 → nil
	p.opts = ""
	if opts := levels[0].Options(nil); opts != nil {
		t.Fatalf("空回复应回 nil: %+v", opts)
	}
	p.opts = "{not json"
	if opts := levels[0].Options(nil); opts != nil {
		t.Fatalf("非法 JSON 应回 nil: %+v", opts)
	}
	_ = env.e.client.Close()
	if opts := levels[0].Options(nil); opts != nil {
		t.Fatalf("连接断裂应回 nil: %+v", opts)
	}
}

// TestRpcCallGuards rpcCall/rpcCallCtx 无连接时显式报错(不 panic)。
func TestRpcCallGuards(t *testing.T) {
	var out string
	if err := rpcCall(nil, "Plugin.Definitions", struct{}{}, &out, time.Second); err == nil ||
		!strings.Contains(err.Error(), "连接不存在") {
		t.Fatalf("nil 连接应显式报错: %v", err)
	}
	if err := rpcCallCtx(context.Background(), nil, "Plugin.Definitions", struct{}{}, &out, time.Second, nil); err == nil {
		t.Fatal("nil 连接应显式报错")
	}
	// 无超时(timeout<=0)+ ctx 取消:走 ctx.Done 分支
	cl, srvConn := net.Pipe()
	go func() { _ = srvConn.Close() }()
	cl2 := rpc.NewClient(cl)
	defer func() { _ = cl2.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := rpcCallCtx(ctx, cl2, "Plugin.Definitions", struct{}{}, &out, 0, nil); err == nil {
		t.Fatal("连接已关闭应报错")
	}
}

// TestBridgeReloadAllReplacesEntries 工作区切换语义:reloadAll 逐个重启外部进程
// (宿主持新 cwd),工具保持可用;closeAll 撤销注册且幂等(卸载即撤销)。
func TestBridgeReloadAllReplacesEntries(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	reg := newRecRegistry()
	b := &Bridge{dir: dir, tools: reg, lg: slog.New(slog.DiscardHandler), entries: map[string]*extEntry{}}
	t.Cleanup(b.closeAll)
	if err := b.loadEntries(); err != nil {
		t.Fatal(err)
	}
	if len(b.entries) != 1 {
		t.Fatalf("应加载 1 个外部插件: %d", len(b.entries))
	}
	var path string
	for p := range b.entries {
		path = p
	}
	old := b.entries[path]

	b.reloadAll()
	newEntry := b.entries[path]
	if newEntry == nil {
		t.Fatal("reloadAll 后条目应存在(软降级不该丢插件)")
	}
	if newEntry == old {
		t.Fatal("reloadAll 应替换为新建的进程实例")
	}
	if _, err := reg.Execute(context.Background(), "echo", `{"text":"reload 后"}`); err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("echo")
	if !ok || def.Name != "echo" {
		t.Fatalf("重载后工具应仍注册: %+v %v", def, ok)
	}

	b.closeAll()
	if len(b.entries) != 0 {
		t.Fatalf("closeAll 应清空条目: %d", len(b.entries))
	}
	if _, ok := reg.Get("echo"); ok {
		t.Fatal("closeAll 应撤销工具注册(卸载即撤销)")
	}
	if _, err := reg.Execute(context.Background(), "echo", `{}`); err == nil {
		t.Fatal("撤销后工具不应可执行")
	}
	b.closeAll() // 幂等
	if !b.closed {
		t.Fatal("closeAll 应标记 closed(阻断后续 respawn)")
	}
}

// TestBridgeReloadAddsAndIgnoresPaths reload 对「新出现的插件二进制」加载、
// 对「非插件命名」忽略(热更新只认 tool-*/cmd-*)。
func TestBridgeReloadAddsAndIgnoresPaths(t *testing.T) {
	dir := t.TempDir()
	reg := newRecRegistry()
	b := &Bridge{dir: dir, tools: reg, lg: slog.New(slog.DiscardHandler), entries: map[string]*extEntry{}}
	t.Cleanup(b.closeAll)
	if err := b.loadEntries(); err != nil {
		t.Fatal(err)
	}
	if len(b.entries) != 0 {
		t.Fatal("空目录不应有条目")
	}

	buildExternalPlugin(t, dir) // 运行期新落地的 tool-echo
	path := filepath.Join(dir, testutil.ExeName("tool-echo"))
	b.reload(path)
	if b.entries[path] == nil {
		t.Fatal("reload 应加载新出现的插件二进制")
	}
	first := b.entries[path]
	b.reload(path) // 已存在 → 替换实例
	if b.entries[path] == nil || b.entries[path] == first {
		t.Fatal("reload 已加载插件应替换实例")
	}

	// 非插件命名:忽略(不产生条目、不影响已有)
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.reload(other)
	if _, ok := b.entries[other]; ok {
		t.Fatal("非 tool-*/cmd-* 文件不应被当作插件加载")
	}
	if _, ok := reg.Get("echo"); !ok {
		t.Fatal("忽略无关路径不应影响已注册工具")
	}
}

// TestExternalEnvPassAllowList 凭据放行名单(GAH_EXT_ENV_PASS)解析:
// 未设/空白 = 不放行任何键;逐项 trim、跳过空项与宿主未设的键(不回传空值)。
func TestExternalEnvPassAllowList(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-值")
	t.Setenv("SOME_PASS", "口令值")

	cases := []struct {
		name string
		raw  string
		set  bool
		want []string
	}{
		{"未设置", "", false, nil},
		{"空串", "", true, nil},
		{"纯空白", "   ", true, nil},
		{"单个键", "EXA_API_KEY", true, []string{"EXA_API_KEY=exa-值"}},
		{"逗号与空白", " EXA_API_KEY , SOME_PASS ", true, []string{"EXA_API_KEY=exa-值", "SOME_PASS=口令值"}},
		{"跳过空项与未设键", "EXA_API_KEY,,MISSING_KEY,", true, []string{"EXA_API_KEY=exa-值"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("GAH_EXT_ENV_PASS", tc.raw)
			} else {
				if err := os.Unsetenv("GAH_EXT_ENV_PASS"); err != nil {
					t.Fatal(err)
				}
			}
			got := externalEnvPass()
			if len(got) != len(tc.want) {
				t.Fatalf("放行名单不符: got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("放行名单不符: got %v want %v", got, tc.want)
				}
			}
		})
	}
}

// assertNoCredentialLeak 凭据隔离断言(回归保护;不跳过,泄漏即失败)。
// 历史:2026-09-12 本断言曾以「已知缺陷」跳过 —— startPlugin 构造 go-plugin ClientConfig
// 时未设 SkipHostEnv,而 go-plugin 会 `cmd.Env = append(cmd.Env, os.Environ()...)`,把整份
// 宿主环境接在 sdk.SanitizedEnv 之后(同名后者胜)→ 凭据过滤被静默覆盖。已改为
// `SkipHostEnv: true`(见 bridge.go startPlugin),本断言随之生效。
func assertNoCredentialLeak(t *testing.T, out string, leaked ...string) {
	t.Helper()
	for _, v := range leaked {
		if strings.Contains(out, v) {
			t.Fatalf("宿主凭据不该下传外部插件进程: 命中 %q\n环境快照:\n%s", v, out)
		}
	}
}

// TestExternalPluginCredentialIsolation 端到端:外部插件进程的凭据隔离 ——
// GAH_CB_* 只注入给插件本体(回调通道必需),宿主凭据(*_API_KEY/*_TOKEN)默认不下传,
// 经 GAH_EXT_ENV_PASS 点名的键才放行。以插件启动时写下的环境快照为证。
func TestExternalPluginCredentialIsolation(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "env.txt")
	bin := t.TempDir()
	body := `package main

import (
	"context"
	"os"
	"strings"

	hostbridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type dumpTool struct{}

func (dumpTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "dump", InputSchema: map[string]any{"type": "object"}}
}
func (dumpTool) Execute(context.Context, string) (any, error) { return "ok", nil }

func main() {
	keys := []string{"GAH_CB_ADDR", "GAH_CB_TOKEN", "EXA_API_KEY", "MY_API_TOKEN", "HOME"}
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + os.Getenv(k) + "\n")
	}
	_ = os.WriteFile(os.Getenv("ENV_DUMP"), []byte(b.String()), 0o600)
	hostbridge.ServeTools(map[string]sdk.Tool{"dump": dumpTool{}}, nil)
}`
	buildTempCmdPlugin(t, bin, "tool-envdump", body)

	// run 复制插件到独立目录后加载,返回插件写下的环境快照。
	run := func(t *testing.T, pass string) string {
		t.Helper()
		dir := t.TempDir()
		src, err := os.ReadFile(filepath.Join(bin, testutil.ExeName("tool-envdump")))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, testutil.ExeName("tool-envdump")), src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(dump); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		t.Setenv("ENV_DUMP", dump)
		t.Setenv("GAH_EXT_ENV_PASS", pass)
		buildEnv(t, dir)
		if !waitFile(t, dump, 10*time.Second) {
			t.Fatal("插件未写出环境快照(插件加载失败?)")
		}
		out, err := os.ReadFile(dump)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	t.Run("回调地址注入插件本体", func(t *testing.T) {
		t.Setenv("EXA_API_KEY", "leak-exa")
		out := run(t, "")
		if !strings.Contains(out, "GAH_CB_ADDR=127.0.0.1:") {
			t.Fatalf("回调地址应注入插件本体: %q", out)
		}
		if !strings.Contains(out, "GAH_CB_TOKEN=") {
			t.Fatalf("回调 token 应注入插件本体: %q", out)
		}
		if !strings.Contains(out, "HOME=") {
			t.Fatalf("基础环境(HOME)应保留: %q", out)
		}
	})

	t.Run("默认不放行凭据", func(t *testing.T) {
		t.Setenv("EXA_API_KEY", "leak-exa")
		t.Setenv("MY_API_TOKEN", "leak-my")
		out := run(t, "")
		assertNoCredentialLeak(t, out, "leak-exa", "leak-my")
		if !strings.Contains(out, "EXA_API_KEY=\n") || !strings.Contains(out, "MY_API_TOKEN=\n") {
			t.Fatalf("凭据键应被滤除(取到空值): %q", out)
		}
	})

	t.Run("点名放行的键才下传", func(t *testing.T) {
		t.Setenv("EXA_API_KEY", "pass-exa")
		t.Setenv("MY_API_TOKEN", "leak-my")
		out := run(t, "EXA_API_KEY")
		if !strings.Contains(out, "EXA_API_KEY=pass-exa") {
			t.Fatalf("点名放行的键应下传: %q", out)
		}
		// 未点名的键仍不得下传(SkipHostEnv 修复后此断言为回归保护)
		assertNoCredentialLeak(t, out, "leak-my")
	})
}

// TestPureHelpers 纯函数边界(判定类逻辑集中在此,防止回归悄悄放宽)。
func TestPureHelpers(t *testing.T) {
	if isTimeoutErr(nil) {
		t.Fatal("nil 非超时")
	}
	if !isTimeoutErr(context.DeadlineExceeded) || !isTimeoutErr(fmt.Errorf("rpc 调用 X 超时(>3s)")) {
		t.Fatal("超时应被识别")
	}
	if isTimeoutErr(errors.New("connection is shut down")) {
		t.Fatal("连接错误不该判为超时(否则不触发崩溃重建)")
	}
	if !isMethodMissing(errors.New(`rpc: can't find method Plugin.ExecuteNamed`)) {
		t.Fatal("旧插件回退信号应被识别")
	}
	if isMethodMissing(nil) || !isConnErr(rpc.ErrShutdown) || !isConnErr(fmt.Errorf("connection is shut down")) {
		t.Fatal("连接错误判定不符")
	}
	if isConnErr(nil) || isConnErr(errors.New("工具业务错误")) {
		t.Fatal("业务错误不该判为连接错误")
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"tool-echo", true}, {"cmd-notify", true}, {"echo", false}, {"notes.txt", false}, {"tool-", true},
	} {
		if got := isExternalPluginBin(tc.name); got != tc.want {
			t.Errorf("isExternalPluginBin(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
	if rpcTimeoutOf(0) != rpcTimeout || rpcTimeoutOf(1500) != 1500*time.Millisecond {
		t.Fatal("超时换算不符")
	}
	if rpcTimeoutFor(sdk.ToolDefinition{TimeoutMs: 200}) != 200*time.Millisecond {
		t.Fatal("工具级超时应覆盖全局")
	}
	if got := pluginSideTimeoutMs(3 * time.Second); got != 2400 {
		t.Fatalf("插件侧超时应为宿主 80%%: %d", got)
	}
	if id := nextCallID("/x/tool-echo"); !strings.HasPrefix(id, "tool-echo#") {
		t.Fatalf("CallID 应含插件名与序号: %q", id)
	}
	if a, b := nextCallID("/x/tool-a"), nextCallID("/x/tool-a"); a == b {
		t.Fatal("CallID 应逐次递增")
	}
	tok1, tok2 := randomToken(), randomToken()
	if len(tok1) != 32 || tok1 == tok2 {
		t.Fatalf("回调 token 应为随机 16 字节 hex: %q %q", tok1, tok2)
	}
	if (&Plugin{}).Name() != "host-bridge" {
		t.Fatal("插件名不符")
	}
	if pluginHome() != sdk.Home() {
		t.Fatal("pluginHome 必须走 sdk.Home()(GAH_HOME 单根)")
	}
	t.Setenv("GAH_HOME", "/tmp/gah-home-测试")
	if pluginHome() != "/tmp/gah-home-测试" {
		t.Fatalf("pluginHome 应取 GAH_HOME: %q", pluginHome())
	}
}
