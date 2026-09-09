// Web UI 端到端(M7):base + web 服务装配(mock LLM)→ 回合经 REST 提交 →
// SSE 事件流全链路(user/message → tool/call → tool/result → assistant/message → turn/end)。
// 插件装配层冒烟见 DESIGN §14.1 M7 记录;此处验证真实宿主服务经 web 服务窗口可达。
package tests

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	webb "github.com/nekoleamo/go-agent-harness/bundles/web"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
	"github.com/nekoleamo/go-agent-harness/web"
)

// buildWebTestEnv 装配 base+web 服务(不启动监听,测试直挂 handler;等价 ui-web-app 装配路径)。
func buildWebTestEnv(t *testing.T) (*ctx.Ctx, *web.Server) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "host-cwd-sessions"},
		{ID: "host-usage-stats"},
		{ID: "llm-mock"},
		{ID: "tool-shell"},
		{ID: "tool-todo"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "host-agent-loop"},
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
	if err := webb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	// web 服务装配(插件薄壳等价步骤:注入 + 订阅 + Provide confirm)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	hub := web.NewHub()
	confirm := web.NewConfirm(hub)
	srv := web.New(web.Config{}, hub, confirm, logger)
	if err := srv.Inject(c); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Subscribe(c, sessions); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.confirm", confirm); err != nil {
		t.Fatal(err)
	}
	return c, srv
}

// TestWebEndToEndTurn 回合全链路:input 202 → SSE 帧序列完整(含工具调用与落定消息)。
func TestWebEndToEndTurn(t *testing.T) {
	_, srv := buildWebTestEnv(t)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// 1. 状态快照
	resp, err := http.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("state 应 200,得 %d", resp.StatusCode)
	}

	// 2. 回合提交(mock LLM 一步:调 shell 工具 + 收尾回复)
	resp, err = http.Post(hs.URL+"/api/input", "application/json",
		strings.NewReader(`{"content":"请运行 echo 集成测试"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("input 应 202,得 %d", resp.StatusCode)
	}

	// 3. SSE 连接读取事件流,断言帧序列
	evResp, err := http.Get(hs.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evResp.Body.Close()
	if !strings.Contains(evResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE Content-Type 不符: %q", evResp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(evResp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	kinds := map[string]bool{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (!kinds["user/message"] || !kinds["tool/call"] || !kinds["turn/end"]) {
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var f struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f); err != nil {
			continue
		}
		var se struct {
			Kind string `json:"Kind"`
		}
		_ = json.Unmarshal(f.Payload, &se)
		kinds[se.Kind] = true
		if f.Type == "status" {
			var st string
			_ = json.Unmarshal(f.Payload, &st)
			if st == "idle" {
				break // 回合结束(已见 turn/end 或超时)
			}
		}
	}
	for _, want := range []string{"user/message", "tool/call", "tool/result", "assistant/message", "turn/end"} {
		if !kinds[want] {
			t.Errorf("SSE 事件流缺 %s(已见 %v)", want, kinds)
		}
	}
}

// TestWebCommandsAndSessions web 窗的宿主服务(session/usage/command)经真实装配可见。
func TestWebCommandsAndSessions(t *testing.T) {
	c, srv := buildWebTestEnv(t)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// 命令注册表经 host-commands 可见(jobs 等宿主命令)
	resp, err := http.Get(hs.URL + "/api/commands")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cmds []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cmds); err != nil {
		t.Fatal(err)
	}
	// 命令注册表为容器(命令由 TUI/外部命令桥注册);协议可用性 = 200 + 数组即可;
	// 具体命令注册经 ctx.commands 注入路径验证(见 host-bridge command_bridge_test)。

	// 会话列表与新建(host-cwd-sessions)
	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.New(); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(hs.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sessions []struct {
		ID string `json:"ID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) == 0 {
		t.Fatal("会话列表为空(应有新建会话)")
	}

	// 状态快照含会话路径
	resp, err = http.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st webStateView
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Session == nil || st.Session.Key == "" {
		t.Fatalf("state.session 应为有效会话视图,得 %+v", st.Session)
	}

	// todo 面板数据端点(M8-T2):经 ctx.tools 代理 todo list(真实装配含 tool-todo)
	resp, err = http.Get(hs.URL + "/api/todo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("todo 应 200(真实装配含 tool-todo),得 %d", resp.StatusCode)
	}
	var todos []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Status  string `json:"status"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&todos); err != nil {
		t.Fatal(err)
	}
	if len(todos) == 1 && todos[0].Error != "" {
		t.Fatalf("todo 查询业务失败: %s", todos[0].Error)
	}
	// 命令分发路径:注册一条命令 → /api/input 经 ctx.commands 执行(web.Server 分发)
	var cmds2 sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds2); err != nil {
		t.Fatal(err)
	}
	if _, err := cmds2.Register(sdk.CommandSpec{
		Name: "echo", Usage: "/echo <词>", Desc: "回声",
		Run: func(args []string) (string, error) { return "echoed:" + strings.Join(args, " "), nil },
	}); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"/echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("命令分发应 200,得 %d", resp.StatusCode)
	}
}

// webStateView 与 web.StateView 同构(避免 import 类型名冲突,字段一致断言)。
type webStateView struct {
	Session *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Path string `json:"path"`
		Key  string `json:"key"`
	} `json:"session"`
}
