// acp_e2e_test.go:S-P2-3 端到端验收(ACP agent 真的能跑一轮,且不绕开宿主账本)。
//
// 装配真实链路:host-agent-loop + host-tools + tool-files + policy-guard + host-cwd-sessions
// + host-commands + host-confirm-fusion,经 **acp-server 的 stdio 表面**驱动一轮:
//
//	不变量 1:session/new → session/prompt → 回合内工具调用与结果都经 session/update 可见;
//	不变量 2:ACP 不是旁路 —— 同一轮的事件必须落进会话账本(ctx.sessions.Replay 可回放);
//	不变量 3:审批走**真实** policy-guard → fusion → session/request_permission 反向请求;
//	          编辑器取消应答时按拒绝处理(安全默认),工具确实没执行。
package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	confirmfusionb "github.com/nekoleamo/go-agent-harness/bundles/confirm-fusion"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	acpserver "github.com/nekoleamo/go-agent-harness/plugins/mcp/acp-server"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// acpAdapter 内容驱动假适配器:按用户提示内容决定下一动作(不看请求序号,重放安全)。
type acpAdapter struct {
	mu      sync.Mutex
	prompts []string
}

func (a *acpAdapter) Name() string { return "acp-e2e" }

func (a *acpAdapter) Complete(_ context.Context, req *sdk.LLMRequest, onChunk func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	content, role := lastContent(req)
	a.mu.Lock()
	a.prompts = append(a.prompts, string(role)+":"+content)
	a.mu.Unlock()
	if role == sdk.RoleTool { // 工具已执行 → 收尾
		return emitText(onChunk, "已完成")
	}
	if strings.Contains(content, "危险") {
		return emitCall(onChunk, "shell", `{"command":"rm -rf /tmp/gah-acp-e2e-absent"}`)
	}
	return emitCall(onChunk, "file_write", `{"path":"note.txt","content":"来自 ACP\n"}`)
}

// buildACPEnv 装配 e2e 环境(工作区 = ws;不含 acp-server 自身)。
func buildACPEnv(t *testing.T, ws string) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-agent-loop"},
		{ID: "host-commands"},
		{ID: "host-cwd-sessions"},
		{ID: "host-usage-stats"},
		{ID: "host-confirm-fusion"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "tool-files"},
		{ID: "tool-shell"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	// confirm-fusion 属独立 bundle(profile-acp 声明 bundles: base, confirm-fusion)
	if err := confirmfusionb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	llm.RegisterAdapter(&acpAdapter{})
	llm.SetModel("acp-e2e-model")

	// 工作区切换(与真实路径同:事件 → 沙箱 root 同步);不动进程 cwd
	if _, err := bus.Emit(context.Background(), "cwd/workspace-switched", ws, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	return c
}

// acpIDKey 把 id(可能是数字或字符串)归一为字符串比较键。
func acpIDKey(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// acpClient ACP 客户端桩:从 stdio 读 agent 消息并可反向应答。
type acpClient struct {
	t     *testing.T
	toA   *io.PipeWriter
	inbox chan map[string]any
	seen  []map[string]any
	disp  sdk.Disposer
}

func newACPClient(t *testing.T, c sdk.Ctx) *acpClient {
	t.Helper()
	cr, aw := io.Pipe()
	ar, cw := io.Pipe()
	cl := &acpClient{t: t, toA: aw, inbox: make(chan map[string]any, 256)}
	go func() {
		defer close(cl.inbox)
		sc := bufio.NewScanner(ar)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				return
			}
			cl.inbox <- m
		}
	}()
	d, err := (&acpserver.Plugin{In: cr, Out: cw}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatalf("acp-server Start: %v", err)
	}
	cl.disp = d
	t.Cleanup(func() { d(); _ = aw.Close() })
	return cl
}

func (cl *acpClient) send(v any) {
	cl.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		cl.t.Fatal(err)
	}
	if _, err := cl.toA.Write(append(b, '\n')); err != nil {
		cl.t.Fatalf("写协议流失败: %v", err)
	}
}

func (cl *acpClient) next() map[string]any {
	cl.t.Helper()
	select {
	case m, ok := <-cl.inbox:
		if !ok {
			cl.t.Fatalf("协议流关闭(已收 %d 条)", len(cl.seen))
		}
		cl.seen = append(cl.seen, m)
		return m
	case <-time.After(15 * time.Second):
		cl.t.Fatalf("等 agent 消息超时(已收 %d 条)", len(cl.seen))
	}
	return nil
}

// await 等到满足 pred 的消息(匹配项留在 seen)。
func (cl *acpClient) await(what string, pred func(map[string]any) bool) map[string]any {
	cl.t.Helper()
	for i := 0; i < 300; i++ {
		if m := cl.next(); pred(m) {
			return m
		}
	}
	cl.t.Fatalf("未等到 %s", what)
	return nil
}

// awaitUpdate 等 session/update 的某个变体。
func (cl *acpClient) awaitUpdate(kind string) map[string]any {
	cl.t.Helper()
	m := cl.await("update "+kind, func(m map[string]any) bool {
		if m["method"] != "session/update" {
			return false
		}
		params, _ := m["params"].(map[string]any)
		upd, _ := params["update"].(map[string]any)
		return upd["sessionUpdate"] == kind
	})
	return m["params"].(map[string]any)["update"].(map[string]any)
}

func TestACPEndToEndTurnLandsInLedger(t *testing.T) {
	ws := t.TempDir()
	c := buildACPEnv(t, ws)
	cl := newACPClient(t, c)

	cl.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}})
	res := cl.await("initialize 应答", func(m map[string]any) bool { return acpIDKey(m["id"]) == "1" })
	if res["result"] == nil {
		t.Fatalf("initialize 失败: %v", res)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// cwd = 当前进程目录:同工作区只新建会话(不 chdir;沙箱根独立由事件设定)
	cl.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new",
		"params": map[string]any{"cwd": wd, "mcpServers": []any{}}})
	sess := cl.await("session/new 应答", func(m map[string]any) bool { return acpIDKey(m["id"]) == "2" })
	result, _ := sess["result"].(map[string]any)
	id, _ := result["sessionId"].(string)
	if id == "" {
		t.Fatalf("session/new 未返回 sessionId: %v", sess)
	}

	cl.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "写一个文件"}}}})

	// 工具调用可见(真实工具名/参数来自 host-agent-loop 的 tool/call 事件)
	call := cl.awaitUpdate("tool_call")
	if call["toolCallId"] == "" || call["kind"] != "edit" {
		t.Fatalf("tool_call 异常: %v", call)
	}
	// 工具结果终态
	done := cl.await("tool_call_update completed", func(m map[string]any) bool {
		if m["method"] != "session/update" {
			return false
		}
		upd, _ := m["params"].(map[string]any)["update"].(map[string]any)
		return upd["sessionUpdate"] == "tool_call_update" && upd["status"] == "completed"
	})
	if done == nil {
		t.Fatal("未见工具终态")
	}
	// 回合应答:end_turn
	final := cl.await("prompt 应答", func(m map[string]any) bool { return acpIDKey(m["id"]) == "3" })
	if result, _ := final["result"].(map[string]any); result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason 应为 end_turn: %v", final)
	}

	// 不变量 2:同一轮事件落进账本(ACP 不是旁路)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, ev := range sessions.Replay() {
		kinds[ev.Kind]++
	}
	for _, want := range []string{sdk.EventUserMessage, sdk.EventAssistantMessage, sdk.EventToolCall, sdk.EventToolResult, sdk.EventFileChange, sdk.EventTurnEnd} {
		if kinds[want] == 0 {
			t.Fatalf("账本缺 %s: %v", want, kinds)
		}
	}
	// 改动真的落盘(相对路径基准 = 工作区)
	if _, err := os.Stat(filepath.Join(ws, "note.txt")); err != nil {
		t.Fatalf("文件未落在工作区: %v", err)
	}
}

func TestACPPermissionUsesRealApprovalPipeline(t *testing.T) {
	ws := t.TempDir()
	c := buildACPEnv(t, ws)
	cl := newACPClient(t, c)
	cl.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}})
	cl.await("initialize 应答", func(m map[string]any) bool { return acpIDKey(m["id"]) == "1" })
	wd, _ := os.Getwd()
	cl.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new",
		"params": map[string]any{"cwd": wd, "mcpServers": []any{}}})
	sess := cl.await("session/new 应答", func(m map[string]any) bool { return acpIDKey(m["id"]) == "2" })
	id := sess["result"].(map[string]any)["sessionId"].(string)

	// 触发危险命令(rm)→ policy-guard smart 档发起确认 → 经 fusion 转成权限请求
	cl.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "危险操作:删掉那个目录"}}}})
	perm := cl.await("session/request_permission", func(m map[string]any) bool {
		return m["method"] == "session/request_permission"
	})
	params := perm["params"].(map[string]any)
	if params["sessionId"] != id {
		t.Fatalf("权限请求会话错: %v", params)
	}
	opts, _ := params["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("只应给 allow_once/reject_once: %v", opts)
	}
	// 编辑器取消 → 按拒绝处理:危险命令不得执行
	cl.send(map[string]any{"jsonrpc": "2.0", "id": perm["id"],
		"result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
	final := cl.await("prompt 应答", func(m map[string]any) bool { return acpIDKey(m["id"]) == "3" })
	if result, _ := final["result"].(map[string]any); result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason 应为 end_turn: %v", final)
	}

	// 不变量:被拒绝的工具在账本里是错误结果(而不是"看起来成功了")
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	// 不变量:被拒绝的危险命令在账本里是**错误结果**(而不是"看起来成功了")
	var sawReject bool
	for _, ev := range sessions.Replay() {
		if ev.Kind != sdk.EventToolResult {
			continue
		}
		r, ok := ev.Payload.(sdk.ToolResultEvent)
		if !ok || r.Name != "shell" {
			continue
		}
		if !strings.Contains(r.Error, "拒绝") {
			t.Fatalf("被拒绝的危险命令应记错误结果: %+v", r)
		}
		sawReject = true
	}
	if !sawReject {
		t.Fatal("账本缺 shell 的错误结果(拒绝未落账)")
	}
}
