// 协议层测试(不需真的起子进程:用 io.Pipe 当 stdio,假 agent 在另一端说话)。
//
// 钉四件会悄悄坏的事:
//
//	① 请求/响应按 id 对上(并发调用不能串台);
//	② 对端**通知**能进回调(session/update 的增量是最终输出的唯一来源);
//	③ 对端**请求**(权限)能被处理并回包 —— 这一条最容易写错:在读循环里处理会卡死整条连接;
//	④ 对端进程死掉时,待决调用**必须立刻失败**,不能挂在 ctx 上等到超时。
package acpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent 假 agent:读客户端发来的行,按脚本回应。
type fakeAgent struct {
	t     *testing.T
	in    *bufio.Scanner
	out   io.Writer
	mu    sync.Mutex
	lines []string
}

func (f *fakeAgent) send(v any) {
	f.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.out.Write(append(b, '\n')); err != nil {
		f.t.Fatal(err)
	}
}

// next 读客户端发来的一条消息。
func (f *fakeAgent) next() rpcMsg {
	f.t.Helper()
	if !f.in.Scan() {
		f.t.Fatal("客户端不再发消息(或已关闭)")
	}
	var m rpcMsg
	if err := json.Unmarshal(f.in.Bytes(), &m); err != nil {
		f.t.Fatalf("客户端发了非法 JSON: %v: %s", err, f.in.Text())
	}
	f.mu.Lock()
	f.lines = append(f.lines, f.in.Text())
	f.mu.Unlock()
	return m
}

// pair 建一条连接 + 假 agent。
func pair(t *testing.T, cb callbacks) (*conn, *fakeAgent) {
	t.Helper()
	c2sR, c2sW := io.Pipe() // 客户端 → agent
	s2cR, s2cW := io.Pipe() // agent → 客户端
	c := newConn(c2sW, s2cR, cb)
	fa := &fakeAgent{t: t, in: bufio.NewScanner(c2sR), out: s2cW}
	t.Cleanup(func() {
		_ = c2sW.Close()
		_ = s2cW.Close()
	})
	return c, fa
}

func done[T any](t *testing.T, ch chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("等结果超时(可能死锁)")
		var zero T
		return zero
	}
}

func TestConnCallMatchesResponseByID(t *testing.T) {
	c, fa := pair(t, callbacks{})
	errc := make(chan error, 1)
	var got struct {
		SessionID string `json:"sessionId"`
	}
	go func() { errc <- c.call(context.Background(), "session/new", map[string]any{"cwd": "/x"}, &got) }()

	m := fa.next()
	if m.Method != "session/new" || m.ID == nil {
		t.Fatalf("对端收到的不是请求: %+v", m)
	}
	// 先插一条**别的 id** 的响应证明不会串台,再回正确的。
	other := int64(999)
	fa.send(rpcMsg{ID: &other, Result: json.RawMessage(`{"sessionId":"wrong"}`)})
	fa.send(rpcMsg{ID: m.ID, Result: json.RawMessage(`{"sessionId":"s1"}`)})

	if err := done(t, errc); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "s1" {
		t.Fatalf("按 id 对上响应才是对的, got %q", got.SessionID)
	}
}

func TestConnNotificationGoesToCallback(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	c, fa := pair(t, callbacks{onNotify: func(method string, _ json.RawMessage) {
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
	}})
	_ = c

	fa.send(rpcMsg{Method: "session/update", Params: json.RawMessage(`{"sessionId":"s1"}`)})
	fa.send(rpcMsg{Method: "session/update", Params: json.RawMessage(`{"sessionId":"s1"}`)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(methods)
		mu.Unlock()
		if n == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("通知没进回调")
}

// TestConnServesPeerRequest 对端请求(权限)必须被处理并**回包**。
//
// 反向验证过:把 serveRequest 改成在读循环里同步调用(去掉 go) ⇒ 本用例在
// "onRequest 里等一个需要读循环喂的信号"那个变体上会卡死;当前实现是 goroutine。
func TestConnServesPeerRequest(t *testing.T) {
	c, fa := pair(t, callbacks{onRequest: func(_ context.Context, method string, params json.RawMessage) (any, *rpcErr) {
		if method != "session/request_permission" {
			return nil, &rpcErr{Code: -32601, Message: "nope"}
		}
		if !strings.Contains(string(params), "allow_once") {
			return nil, &rpcErr{Code: -32602, Message: "缺选项"}
		}
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}}, nil
	}})
	_ = c

	id := int64(7)
	fa.send(rpcMsg{ID: &id, Method: "session/request_permission", Params: json.RawMessage(`{
		"sessionId":"s1",
		"toolCall":{"title":"写入 a.go","kind":"edit"},
		"options":[{"optionId":"allow_once","name":"仅本次","kind":"allow_once"}]
	}`)})
	resp := fa.next()
	if resp.ID == nil || *resp.ID != id {
		t.Fatalf("回包 id 不对: %+v", resp)
	}
	if !strings.Contains(string(resp.Result), "allow_once") {
		t.Fatalf("回包内容不对: %s", string(resp.Result))
	}
}

// TestConnFailsPendingOnEOF 对端死掉 ⇒ 待决调用立即失败(不挂到超时)。
func TestConnFailsPendingOnEOF(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	c := newConn(c2sW, s2cR, callbacks{})
	// 必须有人读走客户端写出的请求:io.Pipe 的写是**阻塞**的,没人读 ⇒ call 卡在写里,
	// 根本走不到"等响应"那一步(这个坑值得留一行注释)。
	go func() { _, _ = io.Copy(io.Discard, c2sR) }()

	errc := make(chan error, 1)
	go func() { errc <- c.call(context.Background(), "initialize", map[string]any{}, nil) }()
	_ = s2cW.Close() // 模拟 agent 进程退出

	if err := done(t, errc); err == nil {
		t.Fatal("对端已断开,调用必须失败")
	}
}

// TestConnIgnoresGarbageLines 非协议行(CLI banner/日志)不该拖垮连接。
func TestConnIgnoresGarbageLines(t *testing.T) {
	c, fa := pair(t, callbacks{})
	errc := make(chan error, 1)
	go func() { errc <- c.call(context.Background(), "initialize", map[string]any{}, nil) }()
	m := fa.next()
	// 先塞两行垃圾(真实 CLI 会往 stdout 打 welcome/更新提示)
	if _, err := fa.out.Write([]byte("Welcome to fake agent v0.1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := fa.out.Write([]byte("{\"not\":\"rpc\"}\n")); err != nil {
		t.Fatal(err)
	}
	fa.send(rpcMsg{ID: m.ID, Result: json.RawMessage(`{}`)})
	if err := done(t, errc); err != nil {
		t.Fatalf("垃圾行不该让连接失败: %v", err)
	}
}
