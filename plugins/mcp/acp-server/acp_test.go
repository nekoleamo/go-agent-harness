// acp_test.go:协议层测试(双向 JSON-RPC + 会话/回合/取消/权限往返)。
//
// 测试用**内存管道**做真客户端:从 agent 输出读行、按脚本应答反向请求,因此覆盖的是
// 真实线上往返(含 session/request_permission 的回程),而不是只调内部函数。
package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 假服务(只实现本插件真正调用的面) ——

// fakeTurn 假回合控制(真实实现由 host-agent-loop 提供:每次 Run 内部派生可取消 ctx 并注册)。
type fakeTurn struct {
	mu      sync.Mutex
	cancels []context.CancelFunc
}

func (f *fakeTurn) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cancels) > 0
}

func (f *fakeTurn) Cancel() {
	f.mu.Lock()
	cs := append([]context.CancelFunc(nil), f.cancels...)
	f.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

func (f *fakeTurn) register(cancel context.CancelFunc) func() {
	f.mu.Lock()
	f.cancels = append(f.cancels, cancel)
	idx := len(f.cancels) - 1
	f.mu.Unlock()
	return func() { // 注销(索引删除;测试里最多一个在跑回合)
		f.mu.Lock()
		defer f.mu.Unlock()
		if idx < len(f.cancels) {
			f.cancels = append(f.cancels[:idx], f.cancels[idx+1:]...)
		}
	}
}

// fakeLoop 假 agent 循环:run 回调里经 emit 发会话事件(模拟 host-session-log 的广播)。
// 与真实实现同语义:Run 内部派生可取消 ctx 并注册到 turnControl(cancel 链路因此真实可测)。
type fakeLoop struct {
	c       sdk.Ctx
	tc      *fakeTurn
	run     func(ctx context.Context, input string, emit func(kind string, payload any)) error
	seen    []string
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
}

func (f *fakeLoop) Run(ctx context.Context, input string) error {
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if f.tc != nil {
		defer f.tc.register(cancel)()
	}
	f.mu.Lock()
	f.seen = append(f.seen, input)
	f.mu.Unlock()
	emit := func(kind string, payload any) {
		_, _ = f.c.Emit(context.Background(), sdk.EventSession, &sdk.SessionEvent{Kind: kind, Payload: payload}, sdk.Emit)
	}
	return f.run(ctx, input, emit)
}

func (f *fakeLoop) inputs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// fakeSessions host-cwd-sessions 桩:New 递增生成 id;Open 切当前会话;SwitchDir 记目录。
type fakeSessions struct {
	cur  string
	cwd  string
	seq  int
	dirs []string
}

func (f *fakeSessions) Current() string                             { return "proj" }
func (f *fakeSessions) Path() string                                { return "/tmp/" + f.cur + ".jsonl" }
func (f *fakeSessions) List() []string                              { return nil }
func (f *fakeSessions) Sessions() []sdk.SessionInfo                 { return nil }
func (f *fakeSessions) CurrentSession() string                      { return f.cur }
func (f *fakeSessions) SetName(string, string) error                { return nil }
func (f *fakeSessions) SetSummary(string, sdk.SessionSummary) error { return nil }
func (f *fakeSessions) SetPinned(string, bool) error                { return nil }
func (f *fakeSessions) Rename(string) error                         { return nil }
func (f *fakeSessions) Delete(string) error                         { return nil }
func (f *fakeSessions) UnrecordProject(string) error                { return nil }
func (f *fakeSessions) SessionName() string                         { return "" }
func (f *fakeSessions) RecentProjects() []sdk.ProjectInfo           { return nil }
func (f *fakeSessions) SwitchProject(key string) (string, error) {
	f.seq++
	f.cur = "p" + strconv.Itoa(f.seq)
	return f.cur, nil
}
func (f *fakeSessions) SwitchDir(dir string) (string, error) {
	f.cwd = dir
	f.dirs = append(f.dirs, dir)
	f.seq++
	f.cur = "d" + strconv.Itoa(f.seq)
	return f.cur, nil
}

// Open 切会话:未知 id 视为新建(与真实实现同语义,见 sdk.CwdSessions.Open 注释)。
func (f *fakeSessions) Open(id string) error { f.cur = id; return nil }

func (f *fakeSessions) New() (string, error) {
	f.seq++
	f.cur = "s" + strconv.Itoa(f.seq)
	return f.cur, nil
}

// fakeFusion 假融合服务:捕获注册的呈现者,便于测试直接触发一次审批/提问。
type fakeFusion struct {
	mu      sync.Mutex
	present sdk.ConfirmPresenter
	qPres   sdk.QuestionPresenter
}

func (f *fakeFusion) Register(channel string, p sdk.ConfirmPresenter) sdk.Disposer {
	f.mu.Lock()
	f.present = p
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.present = nil; f.mu.Unlock() }
}

func (f *fakeFusion) Ask(context.Context, sdk.Question) (sdk.QuestionAnswer, error) {
	return sdk.QuestionAnswer{}, errors.New("未使用")
}

func (f *fakeFusion) RegisterQuestioner(channel string, p sdk.QuestionPresenter) sdk.Disposer {
	f.mu.Lock()
	f.qPres = p
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.qPres = nil; f.mu.Unlock() }
}

func (f *fakeFusion) presenter() sdk.ConfirmPresenter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present
}

func (f *fakeFusion) questioner() sdk.QuestionPresenter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.qPres
}

// fakeUsage 假用量统计(窗口 200000)。
type fakeUsage struct{}

func (fakeUsage) Stats() sdk.UsageStats { return sdk.UsageStats{Window: 200000} }
func (fakeUsage) Reset()                {}

// fakeCommands 假命令注册表。
type fakeCommands struct{ specs []sdk.CommandSpec }

func (f *fakeCommands) Register(s sdk.CommandSpec) (sdk.Disposer, error) {
	f.specs = append(f.specs, s)
	return func() {}, nil
}
func (f *fakeCommands) List() []sdk.CommandSpec { return f.specs }
func (f *fakeCommands) Get(name string) (sdk.CommandSpec, bool) {
	for _, s := range f.specs {
		if s.Name == name {
			return s, true
		}
	}
	return sdk.CommandSpec{}, false
}

// env 装配环境(返回 ctx 与各假件;loop 的 run 由用例设置)。
type env struct {
	c        sdk.Ctx
	loop     *fakeLoop
	tc       *fakeTurn
	sess     *fakeSessions
	fusion   *fakeFusion
	cmds     *fakeCommands
	shutdown chan struct{}
}

func newEnv(t *testing.T, run func(ctx context.Context, input string, emit func(kind string, payload any)) error) *env {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	e := &env{c: c, sess: &fakeSessions{}, fusion: &fakeFusion{}, cmds: &fakeCommands{}, shutdown: make(chan struct{})}
	e.tc = &fakeTurn{}
	e.loop = &fakeLoop{c: c, tc: e.tc, run: run, started: make(chan struct{})}
	provides := []struct {
		key string
		svc any
	}{
		{"ctx.agentLoop", sdk.AgentLoop(e.loop)},
		{"ctx.turnControl", sdk.TurnControl(e.tc)},
		{"ctx.cwdSessions", sdk.CwdSessions(e.sess)},
		{"ctx.confirmFusion", sdk.ConfirmFusion(e.fusion)},
		{"ctx.question", sdk.QuestionService(e.fusion)},
		{"ctx.usageStats", sdk.UsageStatsService(fakeUsage{})},
		{"ctx.commands", sdk.CommandRegistry(e.cmds)},
	}
	for _, p := range provides {
		if err := c.Provide(p.key, p.svc); err != nil {
			t.Fatal(err)
		}
	}
	bus.Subscribe("system/shutdown", func(context.Context, *sdk.Event) error {
		select {
		case <-e.shutdown:
		default:
			close(e.shutdown)
		}
		return nil
	})
	e.cmds.specs = []sdk.CommandSpec{{Name: "echo", Usage: "/echo <文本>", Desc: "回显", Run: func(args []string) (string, error) {
		if len(args) == 0 {
			return "", errors.New("缺参数")
		}
		return "回显: " + strings.Join(args, " "), nil
	}}}
	return e
}

// —— 测试用 ACP 客户端(内存管道真往返) ——

type harness struct {
	t     *testing.T
	disp  sdk.Disposer
	toA   *io.PipeWriter
	inbox chan map[string]any
	seen  []map[string]any
}

func newHarness(t *testing.T, c sdk.Ctx) *harness {
	t.Helper()
	cr, aw := io.Pipe()
	ar, cw := io.Pipe()
	h := &harness{t: t, toA: aw, inbox: make(chan map[string]any, 512)}
	go func() {
		defer close(h.inbox)
		sc := bufio.NewScanner(ar)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				return
			}
			h.inbox <- m
		}
	}()
	d, err := (&Plugin{In: cr, Out: cw}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.disp = d
	t.Cleanup(h.close)
	return h
}

func (h *harness) close() {
	if h.disp != nil {
		h.disp()
		h.disp = nil
	}
	_ = h.toA.Close()
}

// send 发一行给 agent(请求或通知)。
func (h *harness) send(v any) {
	h.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.toA.Write(append(b, '\n')); err != nil {
		h.t.Fatalf("写协议流失败: %v", err)
	}
}

func (h *harness) req(id any, method string, params any) {
	h.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (h *harness) notify(method string, params any) {
	h.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// next 等下一条 agent → 客户端消息(超时失败并回放已收消息)。
func (h *harness) next() map[string]any {
	h.t.Helper()
	select {
	case m, ok := <-h.inbox:
		if !ok {
			h.t.Fatalf("协议流已关闭(已收 %d 条: %s)", len(h.seen), summarize(h.seen))
		}
		h.seen = append(h.seen, m)
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatalf("等 agent 消息超时(已收 %d 条: %s)", len(h.seen), summarize(h.seen))
	}
	return nil
}

// awaitID 等到指定 id 的应答(沿途消息留在 seen)。
func (h *harness) awaitID(id any) map[string]any {
	h.t.Helper()
	want := toKey(id)
	for i := 0; i < 200; i++ {
		m := h.next()
		if m["method"] == nil && toKey(m["id"]) == want {
			return m
		}
	}
	h.t.Fatalf("未等到 id=%v 的应答", id)
	return nil
}

// awaitUpdate 等到 session/update 里 kind 为 kind 的变体(返回 update 对象)。
func (h *harness) awaitUpdate(sessionID, kind string) map[string]any {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		m := h.next()
		if m["method"] != "session/update" {
			continue
		}
		params, _ := m["params"].(map[string]any)
		if sessionID != "" && params["sessionId"] != sessionID {
			continue
		}
		upd, _ := params["update"].(map[string]any)
		if upd["sessionUpdate"] == kind {
			return upd
		}
	}
	h.t.Fatalf("未等到 %s 更新", kind)
	return nil
}

// replyPermission 应答一次 session/request_permission(选中 optionID 或 cancelled)。
func (h *harness) replyPermission(id any, optionID string) {
	outcome := map[string]any{"outcome": "cancelled"}
	if optionID != "" {
		outcome = map[string]any{"outcome": "selected", "optionId": optionID}
	}
	h.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": outcome}})
}

// waitTurnStarted 等回合真正开跑(session/prompt 走独立 goroutine:
// 不等就可能在首条 update 到达前断言,或与回合结束赛跑)。
func waitTurnStarted(t *testing.T, e *env) {
	t.Helper()
	select {
	case <-e.loop.started:
	case <-time.After(3 * time.Second):
		t.Fatal("回合未在 3s 内开跑")
	}
}

func toKey(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func summarize(msgs []map[string]any) string {
	var parts []string
	for _, m := range msgs {
		if name, ok := m["method"].(string); ok {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, "resp:"+toKey(m["id"]))
	}
	return strings.Join(parts, ",")
}

// initialize 跑一次握手。
func (h *harness) initialize(t *testing.T) {
	t.Helper()
	h.req(1, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "test", "version": "0.0.1"},
	})
	res := h.awaitID(1)
	result, _ := res["result"].(map[string]any)
	if result == nil || result["protocolVersion"] != float64(protocolVersion) {
		t.Fatalf("initialize 应答异常: %v", res)
	}
}

// newSession 建一个会话,返回 sessionId。
func (h *harness) newSession(t *testing.T, cwd string) string {
	t.Helper()
	h.req(2, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	res := h.awaitID(2)
	result, _ := res["result"].(map[string]any)
	id, _ := result["sessionId"].(string)
	if id == "" {
		t.Fatalf("session/new 未返回 sessionId: %v", res)
	}
	return id
}

// —— 用例 ——

func TestInitializeNegotiationAndCapabilities(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)

	// 未 initialize 就用会话方法 → 显式错误(不假装成功)
	h.req(9, "session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	res := h.awaitID(9)
	if res["error"] == nil {
		t.Fatalf("initialize 之前调用 session/new 应报错: %v", res)
	}

	// 客户端报不支持的版本 → 回本实现版本(客户端自行决定是否继续)
	h.req(1, "initialize", map[string]any{"protocolVersion": 99, "clientCapabilities": map[string]any{}})
	result, _ := h.awaitID(1)["result"].(map[string]any)
	if result["protocolVersion"] != float64(protocolVersion) {
		t.Fatalf("版本协商应回 %d: %v", protocolVersion, result)
	}
	caps, _ := result["agentCapabilities"].(map[string]any)
	prompt, _ := caps["promptCapabilities"].(map[string]any)
	if prompt["image"] != false || prompt["embeddedContext"] != false {
		t.Fatalf("能力声明应为不支持: %v", prompt)
	}
	info, _ := result["agentInfo"].(map[string]any)
	if info["name"] != agentName {
		t.Fatalf("agentInfo.name 应为 %s: %v", agentName, info)
	}
	if _, ok := result["authMethods"]; !ok {
		t.Fatal("缺少 authMethods")
	}
}

func TestSessionNewBindsWorkspaceAndRejectsOtherCwd(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)
	h.initialize(t)
	dir := t.TempDir()
	id := h.newSession(t, dir)

	// 可用命令随 session/new 一起推(客户端斜杠菜单)
	upd := h.awaitUpdate(id, "available_commands_update")
	list, _ := upd["availableCommands"].([]any)
	if len(list) == 0 {
		t.Fatalf("应推送可用命令: %v", upd)
	}
	first, _ := list[0].(map[string]any)
	if first["name"] != "echo" || first["description"] != "回显" {
		t.Fatalf("命令映射不对: %v", first)
	}

	// 同 cwd → 新会话(多会话并存)
	other := h.newSession(t, dir)
	if other == id {
		t.Fatalf("同工作区新建会话应得新 id,got %s", other)
	}
	// 不同 cwd → 显式拒绝(单进程单工作区,不静默换工作区)
	h.req(3, "session/new", map[string]any{"cwd": dir + "/sub", "mcpServers": []any{}})
	res := h.awaitID(3)
	if res["error"] == nil {
		t.Fatalf("异 cwd 应显式报错: %v", res)
	}
}

func TestPromptStreamsUpdatesThenStopReason(t *testing.T) {
	e := newEnv(t, func(_ context.Context, input string, emit func(string, any)) error {
		emit(sdk.EventTurnStart, nil)
		emit(sdk.EventStepStart, nil)
		emit(sdk.EventAssistantChunk, sdk.LLMStreamEvent{Thinking: "想一想"})
		emit(sdk.EventAssistantChunk, sdk.LLMStreamEvent{Delta: "你好"})
		call := sdk.ToolCallEvent{ID: "c1", Name: "file_write", Arguments: `{"path":"/tmp/a.txt","content":"x"}`}
		emit(sdk.EventToolCall, call)
		emit(sdk.EventFileChange, sdk.FileChangeEvent{
			Path: "/tmp/a.txt", Op: "write", Added: 1, Diff: "@@ -0,0 +1 @@\n+x\n",
		})
		emit(sdk.EventToolResult, sdk.ToolResultEvent{CallID: "c1", Name: "file_write", Content: `{"ok":true}`})
		emit(sdk.EventAssistantMessage, sdk.AssistantMessage{Content: "你好"})
		emit(sdk.EventUsage, sdk.UsageEvent{Model: "m", Usage: sdk.Usage{PromptTokens: 1234}})
		emit(sdk.EventStepEnd, nil)
		emit(sdk.EventTurnEnd, "done")
		return nil
	})
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")

	h.req(4, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "写文件"}}})
	waitTurnStarted(t, e)

	// update 顺序即事件顺序(思考 → 文本 → 工具调用 → patch → 工具结果 → 用量)
	if upd := h.awaitUpdate(id, "agent_thought_chunk"); upd["content"].(map[string]any)["text"] != "想一想" {
		t.Fatalf("思考块异常: %v", upd)
	}
	if upd := h.awaitUpdate(id, "agent_message_chunk"); upd["content"].(map[string]any)["text"] != "你好" {
		t.Fatalf("文本块异常: %v", upd)
	}
	call := h.awaitUpdate(id, "tool_call")
	if call["toolCallId"] != "c1" || call["kind"] != "edit" || call["status"] != "in_progress" {
		t.Fatalf("tool_call 异常: %v", call)
	}
	if !strings.Contains(call["title"].(string), "a.txt") {
		t.Fatalf("标题应含文件名: %v", call["title"])
	}
	locs, _ := call["locations"].([]any)
	if len(locs) != 1 || locs[0].(map[string]any)["path"] != "/tmp/a.txt" {
		t.Fatalf("locations 异常: %v", call["locations"])
	}
	patch := h.awaitUpdate(id, "tool_call_update")
	if patch["toolCallId"] != "c1" || !strings.Contains(patch["content"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string), "@@") {
		t.Fatalf("patch 未挂到工具调用: %v", patch)
	}
	done := h.awaitUpdate(id, "tool_call_update")
	if done["status"] != "completed" {
		t.Fatalf("工具终态应为 completed: %v", done)
	}
	usage := h.awaitUpdate(id, "usage_update")
	if usage["used"] != float64(1234) || usage["size"] != float64(200000) {
		t.Fatalf("用量上报异常: %v", usage)
	}

	// assistant/message 已流过增量 → 不重复补发;应答最后到达且为 end_turn
	res := h.awaitID(4)
	result, _ := res["result"].(map[string]any)
	if result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason 应为 end_turn: %v", res)
	}
	if got := e.loop.inputs(); len(got) != 1 || got[0] != "写文件" {
		t.Fatalf("回合输入应原样传给循环: %v", got)
	}
	for _, m := range h.seen {
		if m["method"] == "session/update" {
			upd := m["params"].(map[string]any)["update"].(map[string]any)
			if upd["sessionUpdate"] == "agent_message_chunk" && upd["content"].(map[string]any)["text"] == "你好" && strings.Contains(toKey(upd), "messageId") {
				return
			}
		}
	}
	t.Fatal("应至少有一条带 messageId 的文本块")
}

func TestAssistantMessageFallbackWhenNotStreamed(t *testing.T) {
	e := newEnv(t, func(_ context.Context, _ string, emit func(string, any)) error {
		emit(sdk.EventStepStart, nil)
		// 非流式适配器:没有 chunk,只有完整消息
		emit(sdk.EventAssistantMessage, sdk.AssistantMessage{Content: "完整回复"})
		emit(sdk.EventTurnEnd, "done")
		return nil
	})
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")
	h.req(5, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "嗨"}}})
	if upd := h.awaitUpdate(id, "agent_message_chunk"); upd["content"].(map[string]any)["text"] != "完整回复" {
		t.Fatalf("非流式回合应补发完整消息: %v", upd)
	}
	h.awaitID(5)
}

func TestPromptErrorsAndProtocolErrors(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)
	h.initialize(t)

	// 未知会话
	h.req(10, "session/prompt", map[string]any{"sessionId": "nope", "prompt": []any{map[string]any{"type": "text", "text": "x"}}})
	if res := h.awaitID(10); res["error"] == nil {
		t.Fatalf("未知会话应报错: %v", res)
	}
	// 图片块(未声明能力)→ 显式报错
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")
	h.req(11, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "image", "data": "xx"}}})
	if res := h.awaitID(11); res["error"] == nil {
		t.Fatalf("图片输入应报错: %v", res)
	}
	// 空提示
	h.req(12, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{}})
	if res := h.awaitID(12); res["error"] == nil {
		t.Fatalf("空提示应报错: %v", res)
	}
	// 未知方法 → -32601
	h.req(13, "session/load", map[string]any{})
	if res := h.awaitID(13); res["error"] == nil {
		t.Fatalf("未实现方法应报错: %v", res)
	}
	// 非法 JSON → -32700(无 id 的错误应答)
	if _, err := h.toA.Write([]byte("{不是 JSON}\n")); err != nil {
		t.Fatal(err)
	}
	m := h.next()
	errObj, _ := m["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(codeParseError) {
		t.Fatalf("非法 JSON 应回 -32700: %v", m)
	}
}

func TestConcurrentPromptRejected(t *testing.T) {
	release := make(chan struct{})
	e := newEnv(t, func(ctx context.Context, _ string, _ func(string, any)) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")

	h.req(20, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "长回合"}}})
	// 等第一条 update 之外的信号:用 busy 应答确认第一轮已在跑
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.req(21, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "并发回合"}}})
		res := h.awaitID(21)
		if res["error"] != nil {
			errObj, _ := res["error"].(map[string]any)
			if errObj["code"] != float64(codeBusy) {
				t.Fatalf("并发回合应回 busy: %v", res)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("并发回合未被拒绝: %v", res)
		}
	}
	close(release)
	res := h.awaitID(20)
	_ = res // 首轮结束(end_turn;循环返回 ctx.Err 但未取消 → 只记错误消息)
}

func TestCancelTurnMarksCallsFailed(t *testing.T) {
	e := newEnv(t, func(ctx context.Context, _ string, emit func(string, any)) error {
		emit(sdk.EventStepStart, nil)
		emit(sdk.EventToolCall, sdk.ToolCallEvent{ID: "t1", Name: "shell", Arguments: `{"command":"sleep 60"}`})
		<-ctx.Done() // 等取消(真实链路:agent loop 的 ctx 由 turnControl 取消)
		return ctx.Err()
	})
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")
	h.req(30, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "跑"}}})
	call := h.awaitUpdate(id, "tool_call")
	if call["kind"] != "execute" {
		t.Fatalf("shell 应映射为 execute: %v", call)
	}
	// 取消:本用例注入的 turnControl 会取消回合 ctx(与真实 host-agent-loop 同语义)
	h.notify("session/cancel", map[string]any{"sessionId": id})
	failed := h.awaitUpdate(id, "tool_call_update")
	if failed["status"] != "failed" {
		t.Fatalf("中断后未终态调用应标 failed: %v", failed)
	}
	res := h.awaitID(30)
	result, _ := res["result"].(map[string]any)
	if result["stopReason"] != "cancelled" {
		t.Fatalf("stopReason 应为 cancelled: %v", res)
	}
}

func TestSlashCommandGoesThroughRegistry(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error {
		t.Error("斜杠命令不应进入 agent 回合")
		return nil
	})
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")
	h.req(40, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "/echo 你好 世界"}}})
	upd := h.awaitUpdate(id, "agent_message_chunk")
	if got := upd["content"].(map[string]any)["text"]; got != "回显: 你好 世界" {
		t.Fatalf("命令输出应作为助手消息回传: %v", got)
	}
	h.awaitID(40)

	// 未知命令与用法错误都显式说明(不静默空回复)
	h.req(41, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "/nope"}}})
	upd = h.awaitUpdate(id, "agent_message_chunk")
	if !strings.Contains(upd["content"].(map[string]any)["text"].(string), "未知命令") {
		t.Fatalf("未知命令应显式提示: %v", upd)
	}
	h.awaitID(41)
}

func TestPermissionRoundTrip(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")

	// 真实呈现管道要求「有在跑回合并绑定会话」:先起一个回合占住槽
	release := make(chan struct{})
	e.loop.run = func(ctx context.Context, _ string, _ func(string, any)) error {
		<-release
		return nil
	}
	h.req(50, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "危险操作"}}})
	waitTurnStarted(t, e)

	// 触发一次审批:客户端应答 allow_once
	ans, cancel, err := e.fusion.presenter().Present(context.Background(), "删除 /tmp/x?")
	if err != nil {
		t.Fatalf("Present: %v", err)
	}
	defer cancel()
	perm := h.awaitMethod("session/request_permission")
	params := perm["params"].(map[string]any)
	if params["sessionId"] != id {
		t.Fatalf("权限请求会话错: %v", params)
	}
	opts, _ := params["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("应只给 allow_once/reject_once 两项: %v", opts)
	}
	h.replyPermission(perm["id"], "allow_once")
	select {
	case ok := <-ans:
		if !ok {
			t.Fatal("allow_once 应回批准")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("审批未回")
	}

	// 第二次:客户端取消 → 拒绝(安全默认)
	ans2, cancel2, err := e.fusion.presenter().Present(context.Background(), "再删一次?")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()
	perm2 := h.awaitMethod("session/request_permission")
	h.replyPermission(perm2["id"], "")
	select {
	case ok := <-ans2:
		if ok {
			t.Fatal("cancelled 应回拒绝")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("审批未回")
	}
	close(release)
	h.awaitID(50)
}

func TestQuestionPresenterMapsOptions(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")
	release := make(chan struct{})
	e.loop.run = func(ctx context.Context, _ string, _ func(string, any)) error {
		<-release
		return nil
	}
	h.req(60, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "占回合"}}})
	waitTurnStarted(t, e)

	q := sdk.Question{Prompt: "用哪个?", Options: []sdk.QuestionOption{{Value: "a", Desc: "甲"}, {Value: "b"}}}
	ans, cancel, err := e.fusion.questioner().PresentQuestion(context.Background(), q)
	if err != nil {
		t.Fatalf("PresentQuestion: %v", err)
	}
	defer cancel()
	perm := h.awaitMethod("session/request_permission")
	opts, _ := perm["params"].(map[string]any)["options"].([]any)
	if len(opts) != 2 || opts[1].(map[string]any)["name"] != "b" {
		t.Fatalf("选项映射异常: %v", opts)
	}
	h.replyPermission(perm["id"], "1")
	select {
	case a := <-ans:
		if len(a.Values) != 1 || a.Values[0] != "b" {
			t.Fatalf("作答映射异常: %v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("提问未回")
	}

	// 自由文本/多选 → 显式拒绝(编辑器弹层无法忠实表达)
	if _, _, err := e.fusion.questioner().PresentQuestion(context.Background(), sdk.Question{Prompt: "随便说"}); err == nil {
		t.Fatal("自由文本提问应显式报错")
	}
	if _, _, err := e.fusion.questioner().PresentQuestion(context.Background(), sdk.Question{
		Prompt: "多选", Options: []sdk.QuestionOption{{Value: "a"}}, Multiple: true,
	}); err == nil {
		t.Fatal("多选提问应显式报错")
	}
	close(release)
	h.awaitID(60)
}

// awaitMethod 等到指定的服务端 → 客户端方法(请求或通知)。
func (h *harness) awaitMethod(method string) map[string]any {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		m := h.next()
		if m["method"] == method {
			return m
		}
	}
	h.t.Fatalf("未等到 %s", method)
	return nil
}

func TestShutdownOnClientDisconnect(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)
	h.initialize(t)
	if err := h.toA.Close(); err != nil { // 客户端断开(等价 stdin EOF)
		t.Fatal(err)
	}
	select {
	case <-e.shutdown:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开应请求宿主退出(否则留僵尸进程)")
	}
}

func TestStartFailsWithoutRequiredServices(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err == nil {
		t.Fatal("缺 ctx.agentLoop 应显式失败")
	}
	if err := c.Provide("ctx.agentLoop", sdk.AgentLoop(&fakeLoop{c: c})); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err == nil {
		t.Fatal("缺 ctx.cwdSessions 应显式失败")
	}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(&fakeSessions{})); err != nil {
		t.Fatal(err)
	}
	// 缺 confirm-fusion:审批无人应答 → 必须拒绝启动(不静默降级成"一律拒绝")
	_, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err == nil || !strings.Contains(err.Error(), "confirmFusion") {
		t.Fatalf("缺 ctx.confirmFusion 应显式失败: %v", err)
	}
}

func TestResourceLinkPromptAndUnknownNotification(t *testing.T) {
	var got string
	e := newEnv(t, func(_ context.Context, input string, _ func(string, any)) error { got = input; return nil })
	h := newHarness(t, e.c)
	h.initialize(t)
	id := h.newSession(t, t.TempDir())
	h.awaitUpdate(id, "available_commands_update")
	// 未知通知:忽略(不响应,不报错)
	h.notify("some/unknown", map[string]any{})
	h.req(70, "session/prompt", map[string]any{"sessionId": id, "prompt": []any{
		map[string]any{"type": "resource_link", "name": "说明.md", "uri": "file:///tmp/说明.md"},
		map[string]any{"type": "text", "text": "看看这个"},
	}})
	h.awaitID(70)
	if !strings.Contains(got, "看看这个") || !strings.Contains(got, "[资源] 说明.md") {
		t.Fatalf("resource_link 应渲染为文本引用: %q", got)
	}
}

func TestLoggerWarnDedup(t *testing.T) {
	e := newEnv(t, func(context.Context, string, func(string, any)) error { return nil })
	h := newHarness(t, e.c)
	h.initialize(t)
	// 同 cwd 之外的工作区请求两次:第二次不应重复记日志(去重),但都要报错
	dir := t.TempDir()
	h.newSession(t, dir)
	h.awaitUpdate("", "available_commands_update")
	for i, id := range []int{80, 81} {
		h.req(id, "session/new", map[string]any{"cwd": dir + "/x", "mcpServers": []any{}})
		if res := h.awaitID(id); res["error"] == nil {
			t.Fatalf("第 %d 次异 cwd 应报错", i+1)
		}
	}
}
