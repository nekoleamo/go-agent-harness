// qqbot WS gateway 单测:httptest + gorilla 服务端模拟官方 gateway——
// 握手 Hello → Identify 鉴权断言(令牌/Intents/shard)→ READY → C2C/群事件投递 →
// 心跳回显 seq 与 ACK;断线重连(close 4009 走 Resume / close 4006 重走 Identify)。
package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockWS 极简 QQ gateway 模拟(每连接独立 goroutine):
// 首帧 Hello(心跳周期 200ms)→ 读鉴权帧(op2 Identify / op6 Resume)→
// 先记录再回 READY/RESUMED + 投递预设事件 → 按预设 closeCodes 主动关闭(0 = 保持连接)。
type mockWS struct {
	mu         sync.Mutex
	identifies int
	resumes    int
	heartbeats []any // 心跳 d(JSON 解码:nil 或 float64)
	identify   map[string]any
	resume     map[string]any
	eventSets  [][]map[string]any // 每次连接鉴权后下发的帧
	closeCodes []int              // 每次连接鉴权后主动 close 的 code(0=保持)
	sessSeq    int
}

func (m *mockWS) handler() http.Handler {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		m.serve(conn)
	})
}

func (m *mockWS) serve(conn *websocket.Conn) {
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 200}})
	m.mu.Lock()
	idx := m.sessSeq // 连接序(identify/resume 前确定)
	m.sessSeq++
	var events []map[string]any
	if idx < len(m.eventSets) {
		events = m.eventSets[idx]
	}
	closeCode := 0
	if idx < len(m.closeCodes) {
		closeCode = m.closeCodes[idx]
	}
	m.mu.Unlock()

	for {
		var f map[string]any
		if err := conn.ReadJSON(&f); err != nil {
			return
		}
		switch int(f["op"].(float64)) {
		case 2: // Identify:先记录再回 READY
			m.mu.Lock()
			m.identify = f["d"].(map[string]any)
			m.identifies++
			m.mu.Unlock()
			_ = conn.WriteJSON(map[string]any{"op": 0, "t": EventReady, "s": 1,
				"d": map[string]any{"session_id": "mock-sess-" + strconv.Itoa(idx+1)}})
		case 6: // Resume:先记录再回 RESUMED
			m.mu.Lock()
			m.resume = f["d"].(map[string]any)
			m.resumes++
			m.mu.Unlock()
			_ = conn.WriteJSON(map[string]any{"op": 0, "t": EventResumed, "s": 2, "d": map[string]any{}})
		case 1: // Heartbeat → ACK
			m.mu.Lock()
			m.heartbeats = append(m.heartbeats, f["d"])
			m.mu.Unlock()
			_ = conn.WriteJSON(map[string]any{"op": 11})
			continue
		}
		for _, e := range events {
			_ = conn.WriteJSON(e)
		}
		if closeCode > 0 {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(closeCode, ""), time.Now().Add(time.Second))
			return
		}
	}
}

func newMockWS(t *testing.T, m *mockWS) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(m.handler())
	t.Cleanup(hs.Close)
	return hs
}

// startGateway 起 gateway 并返回事件通道与取消函数。
func startGateway(t *testing.T, g *Gateway, hs *httptest.Server) (chan Event, context.CancelFunc) {
	t.Helper()
	events := make(chan Event, 16)
	g.WSURL = "http://" + hs.Listener.Addr().String()
	g.Handler = func(e Event) { events <- e }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = g.Run(ctx) }()
	t.Cleanup(cancel)
	return events, cancel
}

// waitEvent 带超时的首个指定类型事件。
func waitEvent(t *testing.T, ch chan Event, want string) Event {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == want {
				return e
			}
		case <-deadline:
			t.Fatalf("等待事件 %q 超时", want)
		}
	}
}

// poll 轮询直至条件满足或超时。
func poll(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("轮询条件超时: %s", what)
}

// TestGatewayIdentifyHeartbeatEvents Identify 鉴权(tok/intents/shard)→ READY →
// C2C/群事件分派 → 心跳回显最后 seq 并收 ACK(连接保持无错误)。
func TestGatewayIdentifyHeartbeatEvents(t *testing.T) {
	c2c := map[string]any{"op": 0, "t": EventC2CMessage, "s": 2, "id": "evt-1", "d": map[string]any{
		"id": "msg-c2c", "author": map[string]any{"user_openid": "OPENID1"},
		"content": "你好,管家", "timestamp": "2026-10-01T00:00:00+08:00"}}
	grp := map[string]any{"op": 0, "t": EventGroupAtMsg, "s": 3, "id": "evt-2", "d": map[string]any{
		"id": "msg-grp", "author": map[string]any{"member_openid": "MEMBER9"},
		"group_openid": "GRP1", "content": "@管家 群消息", "timestamp": "2026-10-01T00:00:01+08:00",
		"mentions": []map[string]any{{"id": "u0", "member_openid": "BOT1"}}}}

	m := &mockWS{eventSets: [][]map[string]any{{c2c, grp}}}
	hs := newMockWS(t, m)
	g := NewGateway(func(context.Context) (string, error) { return IdentifyToken("tok-1"), nil }, nil)
	g.MinBackoff = 100 * time.Millisecond
	evChan, _ := startGateway(t, g, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := g.WaitReady(ctx); err != nil {
		t.Fatalf("等 READY 失败: %v", err)
	}
	// Identify 帧断言
	m.mu.Lock()
	ident := m.identify
	m.mu.Unlock()
	if ident == nil {
		t.Fatal("未收到 Identify 帧")
	}
	if ident["token"] != "QQBot tok-1" {
		t.Fatalf("Identify token 不符: %v", ident["token"])
	}
	if ident["intents"] != float64(IntentsGroupAndC2CEvent) {
		t.Fatalf("Identify intents 不符: %v", ident["intents"])
	}
	shard, ok := ident["shard"].([]any)
	if !ok || shard[0] != float64(0) || shard[1] != float64(1) {
		t.Fatalf("Identify shard 不符: %v", ident["shard"])
	}
	if g.SessionID() != "mock-sess-1" {
		t.Fatalf("SessionID = %q", g.SessionID())
	}
	// C2C 事件分派与 d 字段解析
	e1 := waitEvent(t, evChan, EventC2CMessage)
	if e1.ID != "evt-1" || e1.Seq != 2 {
		t.Fatalf("C2C 事件元信息不符: %+v", e1)
	}
	var c2cMsg C2CMessage
	if err := json.Unmarshal(e1.Data, &c2cMsg); err != nil {
		t.Fatal(err)
	}
	if c2cMsg.ID != "msg-c2c" || c2cMsg.Author.UserOpenID != "OPENID1" || c2cMsg.Content != "你好,管家" {
		t.Fatalf("C2C d 字段不符: %+v", c2cMsg)
	}
	// 群事件:group_openid + @ mentions
	e2 := waitEvent(t, evChan, EventGroupAtMsg)
	var grpMsg GroupAtMessage
	if err := json.Unmarshal(e2.Data, &grpMsg); err != nil {
		t.Fatal(err)
	}
	if grpMsg.GroupOpenID != "GRP1" || grpMsg.Author.MemberOpenID != "MEMBER9" ||
		len(grpMsg.Mentions) != 1 || grpMsg.Mentions[0].MemberOpenID != "BOT1" {
		t.Fatalf("群事件 d 字段不符: %+v", grpMsg)
	}
	// 心跳回显最后 seq(3)→ ACK 保持连接(gateway 未崩溃)
	waitHeartbeatSeq(t, m, 3)
	time.Sleep(300 * time.Millisecond) // 再跑 1~2 个心跳周期,确认无断线重连
	m.mu.Lock()
	n := m.identifies
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("连接应稳定(无重连),identifies=%d", n)
	}
}

// waitHeartbeatSeq 轮询等待心跳 d 等于 wantSeq(回显最后事件序号)。
func waitHeartbeatSeq(t *testing.T, m *mockWS, wantSeq float64) {
	t.Helper()
	poll(t, "心跳 seq 回显", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.heartbeats) == 0 {
			return false
		}
		return m.heartbeats[len(m.heartbeats)-1] == wantSeq
	})
}

// TestGatewayReconnectResume 断线(close 4009)→ 重连走 Resume(带 session_id+seq),不重新 Identify。
func TestGatewayReconnectResume(t *testing.T) {
	m := &mockWS{eventSets: [][]map[string]any{nil, nil}, closeCodes: []int{4009, 0}}
	hs := newMockWS(t, m)
	g := NewGateway(func(context.Context) (string, error) { return IdentifyToken("tok-2"), nil }, nil)
	g.MinBackoff = 100 * time.Millisecond
	evChan, _ := startGateway(t, g, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := g.WaitReady(ctx); err != nil {
		t.Fatalf("首连 READY 失败: %v", err)
	}
	// 第一次连接被 4009 关闭 → 重连 → 应发 Resume(不重新 Identify)
	poll(t, "Resume 重连", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.resumes == 1
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identifies != 1 {
		t.Fatalf("Resume 路径不应重新 Identify,identifies=%d", m.identifies)
	}
	res := m.resume
	if res == nil {
		t.Fatal("未收到 Resume 帧")
	}
	if res["token"] != "QQBot tok-2" {
		t.Fatalf("Resume token 不符: %v", res["token"])
	}
	if res["session_id"] != "mock-sess-1" {
		t.Fatalf("Resume session_id 不符: %v", res["session_id"])
	}
	if res["seq"] != float64(1) {
		t.Fatalf("Resume seq 不符(应回显 READY 的 s=1): %v", res["seq"])
	}
	// RESUMED 事件回调
	_ = waitEvent(t, evChan, EventResumed)
}

// TestGatewayInvalidSessionReidentify close 4006(无效 session)→ 清 session 重走 Identify。
func TestGatewayInvalidSessionReidentify(t *testing.T) {
	m := &mockWS{eventSets: [][]map[string]any{nil, nil}, closeCodes: []int{4006, 0}}
	hs := newMockWS(t, m)
	g := NewGateway(func(context.Context) (string, error) { return IdentifyToken("tok-3"), nil }, nil)
	g.MinBackoff = 100 * time.Millisecond
	_, _ = startGateway(t, g, hs)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := g.WaitReady(ctx); err != nil {
		t.Fatalf("首连 READY 失败: %v", err)
	}
	// 4006 关闭 → 应重新 Identify(而非 Resume,残留 session 已清)
	poll(t, "重新 Identify", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.identifies == 2
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumes != 0 {
		t.Fatalf("4006 后不应走 Resume,resumes=%d", m.resumes)
	}
}

// TestGatewayBannedStopRetry 机器人封禁(close 4915)→ isFatal 停止重连(不会无限循环)。
func TestGatewayBannedStopRetry(t *testing.T) {
	m := &mockWS{eventSets: [][]map[string]any{nil, nil}, closeCodes: []int{4915, 4915}}
	hs := newMockWS(t, m) // server 存活至测试结束(t.Cleanup 已注册)
	_ = hs
	g := NewGateway(func(context.Context) (string, error) { return IdentifyToken("tok-9"), nil }, nil)
	g.MinBackoff = 100 * time.Millisecond
	g.WSURL = "http://" + hs.Listener.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	if err := g.WaitReady(ctx); err != nil {
		t.Fatalf("首连 READY 失败: %v", err)
	}
	select {
	case err := <-done: // 4915 后应停止重试并返回,而非等 ctx 超时
		if err == nil {
			t.Fatal("4915 应返回错误")
		}
	case <-ctx.Done():
		t.Fatal("4915 后仍重试(等 ctx 超时),isFatal 判定失败")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identifies != 1 {
		t.Fatalf("封禁后不应重连,identifies=%d", m.identifies)
	}
}

// TestGatewayNotConfigured 未配置凭证:Run 立即返回 ErrNotConfigured(不无限重试)。
func TestGatewayNotConfigured(t *testing.T) {
	hs := httptest.NewServer(nil)
	defer hs.Close()
	g := NewGateway(func(context.Context) (string, error) { return "", ErrNotConfigured }, nil)
	g.WSURL = "http://" + hs.Listener.Addr().String()
	// 不真正拨号:token provider 失败应直接返回(不触发 WS)
	err := g.Run(context.Background())
	if err != ErrNotConfigured {
		t.Fatalf("应返回 ErrNotConfigured,got %v", err)
	}
}

// TestFetchGatewayURLAuthHeader /gateway/bot 鉴权头必须单前缀(回归:TokenProvider 返回
// 带 "QQBot " 前缀的 token 时,旧实现再拼一次 → "QQBot QQBot xxx" → 401 → 机器人永远连不上)。
func TestFetchGatewayURLAuthHeader(t *testing.T) {
	var auth string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"url":"wss://api.sgroup.qq.com/websocket","shards":1}`))
	}))
	defer hs.Close()
	g := NewGateway(func(context.Context) (string, error) { return "", nil }, func(Event) {})
	g.BaseURL = hs.URL

	// 带前缀 token(TokenProvider 常见返回形态)
	u, err := g.fetchGatewayURL(context.Background(), "QQBot tk-abc")
	if err != nil || u == "" {
		t.Fatalf("取 gateway url 失败: %q %v", u, err)
	}
	if auth != "QQBot tk-abc" {
		t.Fatalf("鉴权头应单前缀,得 %q(双前缀会导致 401)", auth)
	}
	// 裸 token 同样归一
	if _, err := g.fetchGatewayURL(context.Background(), "tk-xyz"); err != nil {
		t.Fatal(err)
	}
	if auth != "QQBot tk-xyz" {
		t.Fatalf("裸 token 应自动加前缀,得 %q", auth)
	}
}
