package mcpbridge

// Streamable HTTP 传输的端到端(2026-10-02)。
//
// 为什么这组用例值钱:它是**本机上真跑**的远程链路 —— 真起一个 HTTP server(httptest),
// 真走 POST / 真解析 JSON 与 SSE 两种响应形态 / 真带上 session id。远程 MCP 之前
// 完全没有覆盖,所有「能不能接上远程 server」的问题只能等真有一个远程 server 才暴露。
//
// 夹具故意做成**两种形态都支持**(用开关切),因为现实中两种都有:只实现 JSON 会漏掉
// 一半 server,而症状是「第一次能通、第二次超时」这种极难查的形态。
//
// 不覆盖(明确登记,不是漏做):OAuth 授权流(绑在已登记的 OAuth 暂缓项上)、
// 真实公网 server 的 TLS/代理/重连。这些要么需要凭据,要么需要网络。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// httpFixture 一个最小 MCP server(Streamable HTTP)。
type httpFixture struct {
	srv *httptest.Server
	// sseMode 置 true 时 initialize 走 SSE、tools/call 走 JSON(反之亦然)——
	// 逼客户端两种都读。只回一种的实现会在这里挂掉。
	sseMode bool
	// sessionID 非空时按规范回会话头,并校验后续请求带对了
	sessionID string
	// wantHeader 期望客户端必须带上的请求头(凭据那半的判据)
	wantHeader string
	gotHeader  string
	// calls 记录收到的 tools/call 次数
	calls int
	// sessions 记录收到的会话 id 序列(空串 = 没带)
	sessions []string
}

func (f *httpFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return
	}
	if f.wantHeader != "" {
		f.gotHeader = r.Header.Get(f.wantHeader)
	}
	sid := r.Header.Get("Mcp-Session-Id")
	f.sessions = append(f.sessions, sid)
	var req rpcReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	// 通知:允许 202 无 body
	if req.ID == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// 未初始化就带错会话 id ⇒ 按规范回 404(测客户端的重握手路径)
	if f.sessionID != "" && sid != f.sessionID && req.Method != "initialize" {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		if f.sessionID != "" {
			w.Header().Set("Mcp-Session-Id", f.sessionID)
		}
		result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		result = map[string]any{"tools": []map[string]any{{
			"name":        "greet",
			"description": "打个招呼",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"who": map[string]any{"type": "string"},
			}},
		}}}
	case "tools/call":
		f.calls++
		who := ""
		if m, ok := req.Params.(map[string]any); ok {
			if a, ok := m["arguments"].(map[string]any); ok {
				who, _ = a["who"].(string)
			}
		}
		result = map[string]any{"content": []map[string]any{
			{"type": "text", "text": "你好," + who},
		}}
	default:
		http.Error(w, "未知方法 "+req.Method, http.StatusNotFound)
		return
	}
	body, _ := json.Marshal(rpcResp{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(result)})
	// initialize 走 SSE、其余走 JSON(反之在另一个用例里),强制两种解析路径都被走到。
	useSSE := f.sseMode == (req.Method == "initialize")
	if useSSE {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// SSE 里先塞一条**无关的通知**(规范允许流里混多类消息)——
		// 客户端必须跳过它而不是把它当成响应。
		note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", note)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// bridgeWithHTTP 起一个真实的插件装配(mcp-bridge + host-tools),指向该 fixture。
func bridgeWithHTTP(t *testing.T, f *httpFixture, headers map[string]any) sdk.Ctx {
	t.Helper()
	c := ctx.New(slog.New(slog.DiscardHandler), event.New(slog.New(slog.DiscardHandler)))
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"transport": "http", "url": f.srv.URL + "/mcp"}
	if headers != nil {
		data["headers"] = headers
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: data}); err != nil {
		t.Fatalf("mcp-bridge(http) 装配失败: %v", err)
	}
	return c
}

// TestHTTPTransportJSONAndSSE 端到端:远程 server → 工具注册 → 真调用。
func TestHTTPTransportJSONAndSSE(t *testing.T) {
	for _, sse := range []bool{false, true} {
		name := "JSON 响应"
		if sse {
			name = "SSE 响应"
		}
		t.Run(name, func(t *testing.T) {
			f := &httpFixture{sseMode: sse, sessionID: "sess-1"}
			f.srv = httptest.NewServer(f)
			defer f.srv.Close()

			c := bridgeWithHTTP(t, f, nil)
			var tools sdk.ToolRegistry
			if err := c.Inject("ctx.tools", &tools); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, d := range tools.List() {
				names = append(names, d.Name)
			}
			if !containsName(names, "mcp_greet") {
				t.Fatalf("远程 server 的工具没注册上(拿到 %v)", names)
			}
			res, err := tools.Execute(context.Background(), "mcp_greet", `{"who":"远程"}`)
			if err != nil {
				t.Fatal(err)
			}
			if res.Error != "" {
				t.Fatalf("调用失败:%s", res.Error)
			}
			if !strings.Contains(res.Content, "你好,远程") {
				t.Fatalf("返回内容不对:%q", res.Content)
			}
			// 会话 id 必须在 initialize 之后**持续带上**(规范要求)
			if len(f.sessions) < 3 {
				t.Fatalf("应有多次请求(initialize+initialized+tools/call),got %v", f.sessions)
			}
			for i, sid := range f.sessions {
				if i == 0 {
					continue // initialize 之前没有会话
				}
				if sid != "sess-1" {
					t.Fatalf("第 %d 次请求没带对会话 id(%q),序列:%v", i+1, sid, f.sessions)
				}
			}
		})
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestHTTPTransportHeadersFromConfig 凭据那半:配置里的 header 真的发出去了,
// 且**日志/错误里不出现值**。
func TestHTTPTransportHeadersFromConfig(t *testing.T) {
	f := &httpFixture{sessionID: "s1", wantHeader: "Authorization"}
	f.srv = httptest.NewServer(f)
	defer f.srv.Close()

	const secret = "Bearer super-secret-token-1234"
	c := bridgeWithHTTP(t, f, map[string]any{"Authorization": secret})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Execute(context.Background(), "mcp_greet", `{"who":"x"}`); err != nil {
		t.Fatal(err)
	}
	if f.gotHeader != secret {
		t.Fatalf("请求头没带上:%q", f.gotHeader)
	}
	// 反向:任何可安全外传的描述里都不该出现 secret
	spec, err := parseServerSpec(map[string]any{
		"transport": "http", "url": f.srv.URL + "/mcp",
		"headers": map[string]any{"Authorization": secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(spec.Redacted(), "super-secret") {
		t.Fatalf("可安全进日志的描述泄露了凭据:%q", spec.Redacted())
	}
	tr, err := newHTTPTransport(f.srv.URL+"/mcp", map[string]string{"authorization": secret})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tr.Redacted(), "super-secret") {
		t.Fatalf("transport 描述泄露了凭据:%q", tr.Redacted())
	}
	// 也不能因为 path 带 token 就整条打进日志
	tr2, _ := newHTTPTransport("https://example.com/api/"+secret+"/mcp", nil)
	if strings.Contains(tr2.Redacted(), "super-secret") {
		t.Fatalf("path 里的 token 泄露了:%q", tr2.Redacted())
	}
}

// TestHTTPTransportSessionExpiryReinit server 不认会话 id(404)⇒ 重握手**一次**。
func TestHTTPTransportSessionExpiryReinit(t *testing.T) {
	f := &httpFixture{sessionID: "s1"}
	f.srv = httptest.NewServer(f)
	defer f.srv.Close()

	tr, err := newHTTPTransport(f.srv.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 先正常握手拿到会话
	if err := tr.Notify(context.Background(), "notifications/initialized", nil); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	tr.sessionID = "stale-session"
	tr.mu.Unlock()

	// 用陈旧会话发一次 ⇒ server 404 ⇒ 客户端重握手后应成功
	_, err = tr.RoundTrip(context.Background(), rpcReq{JSONRPC: "2.0", ID: 7, Method: "tools/list"})
	if err != nil {
		t.Fatalf("重握手后应成功:%v", err)
	}
	// **最多一次**:再人为把会话弄陈旧,这次应如实报错而不是无限重握手
	tr.mu.Lock()
	tr.sessionID = "stale-again"
	tr.mu.Unlock()
	_, err = tr.RoundTrip(context.Background(), rpcReq{JSONRPC: "2.0", ID: 8, Method: "tools/list"})
	if err == nil {
		t.Fatal("第二次会话失效应如实报错(不许无限重握手)")
	}
	if !strings.Contains(err.Error(), "会话已失效") && !strings.Contains(err.Error(), "404") {
		t.Fatalf("错误应说清是会话失效:%v", err)
	}
}

// TestHTTPTransportAuthAndErrors 401/403/超限响应体 ⇒ 可读的错误,**不泄露**。
func TestHTTPTransportAuthAndErrors(t *testing.T) {
	t.Run("401 说清只支持静态头", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer srv.Close()
		tr, err := newHTTPTransport(srv.URL+"/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tr.RoundTrip(context.Background(), rpcReq{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
		if err == nil || !strings.Contains(err.Error(), "认证") {
			t.Fatalf("应说清需要认证:%v", err)
		}
		if !strings.Contains(err.Error(), "OAuth") {
			t.Errorf("错误应点明 OAuth 未实现(免得用户以为 gah 登录失败是 bug):%v", err)
		}
	})
	t.Run("http 明文端点被拒", func(t *testing.T) {
		if _, err := newHTTPTransport("http://example.com/mcp", nil); err == nil {
			t.Fatal("明文 http 应被拒(请求头里的凭据会裸奔)")
		}
	})
	t.Run("超长响应体被闸住", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// 写一个远超上限的 JSON(用合法前缀 + 大量填充,保证服务端不报错)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"blob":"`))
			chunk := strings.Repeat("x", 64<<10)
			for i := 0; i < (maxHTTPResponse/len(chunk))+2; i++ {
				_, _ = w.Write([]byte(chunk))
			}
			_, _ = w.Write([]byte(`"}}`))
		}))
		defer srv.Close()
		tr, _ := newHTTPTransport(srv.URL+"/mcp", nil)
		_, err := tr.RoundTrip(context.Background(), rpcReq{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
		if err == nil || !strings.Contains(err.Error(), "超上限") {
			t.Fatalf("超限响应应显式报错(不 OOM):%v", err)
		}
	})
}

// TestParseServerSpec 装配期解析:两种形态 + 两种错误。
func TestParseServerSpec(t *testing.T) {
	if _, err := parseServerSpec(nil); err == nil {
		t.Error("既无 command 也无 transport ⇒ 应报错")
	}
	if _, err := parseServerSpec(map[string]any{"transport": "carrier-pigeon"}); err == nil {
		t.Error("非法 transport 应报错")
	}
	if _, err := parseServerSpec(map[string]any{"transport": "http"}); err == nil {
		t.Error("http 缺 url 应报错")
	}
	spec, err := parseServerSpec(map[string]any{
		"transport": "HTTP", "url": " https://x.example/mcp ",
		"headers": map[string]any{" Authorization ": " Bearer t "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.kind != "http" || spec.url != "https://x.example/mcp" {
		t.Fatalf("解析不对:%+v", spec)
	}
	if spec.headers["authorization"] != "Bearer t" {
		t.Fatalf("header 键应小写、值应 trim:%+v", spec.headers)
	}
}
