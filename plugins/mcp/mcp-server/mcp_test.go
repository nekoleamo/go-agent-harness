// mcp-server 协议测试:装配 host-tools 提供 ctx.tools,替换 in/out 注入协议流,
// 校验 initialize/tools/list/tools/call 与错误分支(与 mcp-bridge 测试同构)。
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeTool 测试工具:greet 返回问候,boom 返回业务错误。
type fakeTool struct{ def sdk.ToolDefinition }

func (f fakeTool) Definition() sdk.ToolDefinition { return f.def }
func (f fakeTool) Execute(_ context.Context, args string) (any, error) {
	switch f.def.Name {
	case "greet":
		var a struct {
			Name string `json:"name"`
		}
		json.Unmarshal([]byte(args), &a)
		return map[string]any{"text": "你好, " + a.Name}, nil
	case "boom":
		return map[string]any{"error": "业务失败"}, nil
	}
	return nil, nil
}

// lockedBuf 线程安全输出缓冲(serve goroutine 写、测试主 goroutine 读)。
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuf) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Reset()
}

// newEnv 装配 ctx.tools 并注册 greet/boom;插件输出写入 buf(返回装配上下文与输出缓冲)。
func newEnv(t *testing.T) (sdk.Ctx, *lockedBuf) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(fakeTool{def: sdk.ToolDefinition{
		Name: "greet", Description: "测试问候工具",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
	}})
	tools.Register(fakeTool{def: sdk.ToolDefinition{Name: "boom", Description: "测试失败工具", InputSchema: map[string]any{"type": "object"}}})
	var buf lockedBuf
	out = &buf
	return c, &buf
}

// serveOnce 注入协议流(先置 in 再 Start,避免竞态),等到期望响应数后收集并 dispose。
func serveOnce(t *testing.T, c sdk.Ctx, stream string, want int, buf *lockedBuf) []string {
	t.Helper()
	in = strings.NewReader(stream)
	disp, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	lines := waitN(t, buf, want)
	disp() // serve 顺序处理完整行后才达标;close(done) 只让 EOF 分支退出,不丢已处理响应
	return lines
}

// waitN 轮询等待输出达到 want 行(全部请求已响应;响应只在处理完对应请求后写出)。
func waitN(t *testing.T, buf *lockedBuf, want int) []string {
	t.Helper()
	for i := 0; i < 5000; i++ {
		s := buf.String()
		if n := strings.Count(s, "\n"); n >= want && want > 0 {
			return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		}
		time.Sleep(time.Millisecond) // 让出调度,serve goroutine 推进
	}
	t.Fatalf("serve 未产出 %d 行响应(当前 %d)", want, strings.Count(buf.String(), "\n"))
	return nil
}

// resp 响应行解析。
type resp struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func parseResp(t *testing.T, lines []string) map[string]resp {
	t.Helper()
	m := map[string]resp{}
	for _, l := range lines {
		var r resp
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("非法响应行: %q", l)
		}
		m[string(r.ID)] = r
	}
	return m
}

func TestServeProtocol(t *testing.T) {
	c, buf := newEnv(t)
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"name":"世界"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"boom"}}`,
	}, "\n") + "\n"
	lines := serveOnce(t, c, stream, 6, buf)
	m := parseResp(t, lines)
	if len(m) != 6 {
		t.Fatalf("应 6 行响应(通知无响应),实际 %d: %q", len(m), lines)
	}
	// id=1 initialize
	var hand struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(m["1"].Result, &hand); err != nil || hand.ProtocolVersion != "2024-11-05" {
		t.Fatalf("initialize 应回协议版本: %s", m["1"].Result)
	}
	// 版本贯通(M7):GAH_VERSION 有值则回传该版本;测试内显式置空回退 dev
	if hand.ServerInfo.Name != "gah" {
		t.Fatalf("serverInfo.name 应为 gah: %s", m["1"].Result)
	}
	if hand.ServerInfo.Version != "dev" {
		t.Fatalf("GAH_VERSION 为空时应回退 dev,实际: %s", m["1"].Result)
	}
	// id=2 tools/list 应含 greet/boom
	var lst struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(m["2"].Result, &lst); err != nil || len(lst.Tools) != 2 {
		t.Fatalf("tools/list 应 2 个工具: %s", m["2"].Result)
	}
	// id=3 tools/call 回传问候
	var cl struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(m["3"].Result, &cl); err != nil || len(cl.Content) == 0 || !strings.Contains(cl.Content[0].Text, "你好, 世界") {
		t.Fatalf("tools/call 应回传结果: %s", m["3"].Result)
	}
	// id=4 ping 回 {}
	if string(m["4"].Result) != "{}" {
		t.Fatalf("ping 应回 {}: %s", m["4"].Result)
	}
	// id=5 未知方法 → -32601
	var e struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(m["5"].Error, &e); err != nil || e.Code != -32601 {
		t.Fatalf("未知方法应 -32601: %s", m["5"].Error)
	}
	// id=6 业务失败 → isError:true
	var te struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(m["6"].Result, &te); err != nil || !te.IsError {
		t.Fatalf("业务失败应 isError: %s", m["6"].Result)
	}
}

// TestServerVersionFromEnv 版本贯通正路径(M7):GAH_VERSION 注入后 initialize 回传该版本。
func TestServerVersionFromEnv(t *testing.T) {
	t.Setenv("GAH_VERSION", "v1.2.3")
	c, buf := newEnv(t)
	lines := serveOnce(t, c, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n", 1, buf)
	var hand struct {
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(parseResp(t, lines)["1"].Result, &hand); err != nil {
		t.Fatal(err)
	}
	if hand.ServerInfo.Version != "v1.2.3" {
		t.Fatalf("GAH_VERSION 应贯通到 serverInfo.version: %q", hand.ServerInfo.Version)
	}
}

func TestParseError(t *testing.T) {
	c, buf := newEnv(t)
	lines := serveOnce(t, c, "not json\n", 1, buf)
	var r struct {
		ID     json.RawMessage `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Result) > 0 || len(r.Error) == 0 || string(r.Error) == "null" {
		t.Fatalf("非法 JSON 应回 Parse error: %q", lines[0])
	}
}

func TestInvalidRequest(t *testing.T) {
	c, buf := newEnv(t)
	lines := serveOnce(t, c, `{"id":9,"method":"initialize"}`+"\n", 1, buf) // 缺 jsonrpc 字段
	var r struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil || len(r.Error) == 0 || string(r.Error) == "null" {
		t.Fatalf("缺 jsonrpc 应 Invalid Request: %q", lines[0])
	}
}

// TestRegisterUnregister: 运行期注册/卸载工具应立即反映到 tools/list。
func TestLiveToolList(t *testing.T) {
	c, buf := newEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	disp := tools.Register(fakeTool{def: sdk.ToolDefinition{Name: "live", Description: "运行期注册", InputSchema: map[string]any{"type": "object"}}})
	lines := serveOnce(t, c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n", 1, buf)
	var lst struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(parseResp(t, lines)["1"].Result, &lst); err != nil {
		t.Fatal(err)
	}
	hasLive := false
	for _, td := range lst.Tools {
		if td.Name == "live" {
			hasLive = true
		}
	}
	if !hasLive {
		t.Fatalf("运行期注册的工具应出现在 tools/list: %+v", lst.Tools)
	}
	// 卸载后不应再出现
	disp()
	buf.Reset()
	lines = serveOnce(t, c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n", 1, buf)
	if err := json.Unmarshal(parseResp(t, lines)["1"].Result, &lst); err != nil {
		t.Fatal(err)
	}
	for _, td := range lst.Tools {
		if td.Name == "live" {
			t.Fatalf("卸载后 live 不应出现: %+v", lst.Tools)
		}
	}
}
