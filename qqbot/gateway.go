// WS gateway 客户端:连接 → Hello → Identify/Resume → 心跳 → Dispatch 事件分派;
// 断线按官方语义重连(可 Resume 则 Resume 补发,否则重新 Identify),带退避与 lastError 诊断。
// 生命周期仿 ilink.pollLoop(心跳/退避/诊断模式复用);事件经 Handler 回调(独立 goroutine,panic 隔离)。
package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	errReconnect      = errors.New("qqbot: 服务端要求重连(opcode 7)")
	errInvalidSession = errors.New("qqbot: session 失效(opcode 9),需重新鉴权")
)

// TokenProvider 鉴权 token 提供者:每次 Identify/Resume 时调用(access_token 2h 过期,
// 长连接重连后需新 token);返回完整 token 串(如 "QQBot <access_token>")。
type TokenProvider func(ctx context.Context) (string, error)

// Gateway QQ WS gateway 客户端(无共享可变状态竞争;Run 单实例)。
type Gateway struct {
	TokenProvider TokenProvider
	Intents       int         // 默认 IntentsGroupAndC2CEvent(1<<25)
	WSURL         string      // 空则先 GET /gateway/bot 取(测试传 mock 地址)
	BaseURL       string      // 默认 DefaultBaseURL(gateway/bot 解析用)
	Handler       func(Event) // 事件回调(reader goroutine 内,drain 需快或自起 goroutine)
	Dialer        *websocket.Dialer
	MinBackoff    time.Duration // 断线重连初始退避(默认 2s;测试可缩小)

	mu        sync.Mutex
	sessionID string    // Ready 后记录;Resume 用
	seq       int64     // 最后收到的事件序号(心跳回显)
	lastAck   time.Time // 最后心跳 ACK 时间(超 3×interval 无 ack 判死连,主动重连)
	lastError string

	readyCh chan struct{} // 首次 Ready 后 close(WaitReady)
}

// NewGateway 构造 gateway。tokenProvider 返回 "QQBot <access_token>" 形式即可。
func NewGateway(tp TokenProvider, handler func(Event)) *Gateway {
	return &Gateway{
		TokenProvider: tp, Intents: IntentsGroupAndC2CEvent, BaseURL: DefaultBaseURL,
		Handler: handler, Dialer: &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
		readyCh: make(chan struct{}),
	}
}

// LastError 最近错误诊断(线程安全)。
func (g *Gateway) LastError() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastError
}

// Online 是否已鉴权上线(session 有效)。
func (g *Gateway) Online() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessionID != ""
}

// SessionID 当前会话 ID(诊断用)。
func (g *Gateway) SessionID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessionID
}

// WaitReady 等待首次 Ready(ctx 取消/超时返回错误)。
func (g *Gateway) WaitReady(ctx context.Context) error {
	select {
	case <-g.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setError 写诊断(空串 = 清空)。
func (g *Gateway) setError(s string) {
	g.mu.Lock()
	g.lastError = s
	g.mu.Unlock()
}

// seqAndAck 取最后 seq 与最后 ack。
func (g *Gateway) seqAndAck() (int64, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seq, g.lastAck
}

// clearSession 清 session(下次重连走 Identify)。
func (g *Gateway) clearSession() {
	g.mu.Lock()
	g.sessionID = ""
	g.mu.Unlock()
}

// Run 主循环(阻塞):连接→鉴权→读事件;断线退避重连直至 ctx 取消或不可恢复错误。
func (g *Gateway) Run(ctx context.Context) error {
	minBackoff := g.MinBackoff
	if minBackoff <= 0 {
		minBackoff = 2 * time.Second
	}
	backoff := minBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := g.runOnce(ctx)
		if err == nil {
			return ctx.Err() // 仅 ctx 取消正常返回 nil
		}
		if isFatal(err) {
			return err
		}
		g.setError("gateway 断线: " + err.Error())
		// 存活超过一个长周期后断开 → 退避重置(短连抖动才退避递增)
		if time.Since(start) > 60*time.Second {
			backoff = 2 * time.Second
		}
		g.mu.Lock()
		g.lastAck = time.Time{}
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
	return ctx.Err()
}

// runOnce 单次连接生命周期:握手 → Hello → Identify/Resume → 心跳+读事件,直至断开。
func (g *Gateway) runOnce(ctx context.Context) error {
	token, err := g.TokenProvider(ctx)
	if err != nil {
		return err
	}
	url := g.WSURL
	if url == "" {
		if url, err = g.fetchGatewayURL(ctx, token); err != nil {
			return err
		}
	}
	conn, err := g.dial(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close()
	g.setError("")

	// 1. 首帧必为 Hello(心跳周期)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello WSFrame
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("qqbot: 握手等待 Hello 失败: %w", err)
	}
	if hello.Op != OpHello {
		return fmt.Errorf("qqbot: 首帧非 Hello(op=%d)", hello.Op)
	}
	var hd Hello
	if err := json.Unmarshal(hello.D, &hd); err != nil || hd.HeartbeatInterval <= 0 {
		return fmt.Errorf("qqbot: Hello 载荷异常: %v", err)
	}
	interval := time.Duration(hd.HeartbeatInterval) * time.Millisecond
	conn.SetReadDeadline(time.Time{})

	// 2. 鉴权:有 session 走 Resume(补发遗漏事件),否则 Identify
	g.mu.Lock()
	hasSession := g.sessionID != ""
	seq := g.seq
	g.lastAck = time.Now()
	g.mu.Unlock()
	if hasSession {
		if err := g.writeFrame(conn, map[string]any{"op": OpResume, "d": map[string]any{
			"token": token, "session_id": g.sessionID, "seq": seq}}); err != nil {
			return err
		}
	} else if err := g.writeFrame(conn, map[string]any{"op": OpIdentify, "d": map[string]any{
		"token": token, "intents": g.Intents, "shard": [2]int{0, 1},
		"properties": map[string]string{"$os": "linux", "$browser": "GoAgentHarness"},
	}}); err != nil {
		return err
	}

	// 3. 读循环(独立 goroutine)+ 心跳(ticker)并行
	readDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil { // 事件回调 panic 不拖死网关
				readDone <- fmt.Errorf("qqbot: 事件回调 panic: %v", r)
			}
		}()
		readDone <- g.readLoop(conn)
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-readDone:
			return err
		case <-ticker.C:
			if !g.heartbeat(conn, interval) {
				return errors.New("qqbot: 心跳超时(3×interval 无 ACK)")
			}
		case <-ctx.Done():
			_ = conn.Close()
			return ctx.Err()
		}
	}
}

// readLoop 读帧循环:Dispatch 分派/心跳 ACK/Reconnect/InvalidSession 处理。
func (g *Gateway) readLoop(conn *websocket.Conn) error {
	for {
		var f WSFrame
		if err := conn.ReadJSON(&f); err != nil {
			ce := classifyClose(err)
			if errors.Is(ce, errInvalidSession) {
				g.clearSession() // 4006/4007:resume 不可用,下次重连走 Identify
			}
			return ce
		}
		switch f.Op {
		case OpDispatch:
			if f.S != nil {
				g.mu.Lock()
				g.seq = *f.S
				g.mu.Unlock()
			}
			g.dispatch(f)
		case OpHeartbeatAck:
			g.mu.Lock()
			g.lastAck = time.Now()
			g.mu.Unlock()
		case OpReconnect:
			return errReconnect
		case OpInvalidSession:
			g.clearSession()
			return errInvalidSession
		}
	}
}

// dispatch READY/RESUMED/消息事件分派给 Handler(READY 额外记录 session_id 并通知 WaitReady)。
func (g *Gateway) dispatch(f WSFrame) {
	if f.T == EventReady {
		var rd Ready
		if err := json.Unmarshal(f.D, &rd); err == nil && rd.SessionID != "" {
			g.mu.Lock()
			g.sessionID = rd.SessionID
			g.mu.Unlock()
		}
		select {
		case <-g.readyCh: // 已 close,忽略
		default:
			close(g.readyCh)
		}
	}
	if g.Handler != nil {
		var seq int64
		if f.S != nil {
			seq = *f.S
		}
		g.Handler(Event{Type: f.T, ID: f.ID, Seq: seq, Data: f.D})
	}
}

// heartbeat 发一次心跳(d=最后 seq;超 3×interval 无 ACK 返回 false 触发重连)。
func (g *Gateway) heartbeat(conn *websocket.Conn, interval time.Duration) bool {
	seq, lastAck := g.seqAndAck()
	if !lastAck.IsZero() && time.Since(lastAck) > 3*interval {
		return false
	}
	var d any
	if seq > 0 {
		d = seq
	}
	if err := g.writeFrame(conn, map[string]any{"op": OpHeartbeat, "d": d}); err != nil {
		return false
	}
	return true
}

// writeFrame 带写超时的帧发送。
func (g *Gateway) writeFrame(conn *websocket.Conn, frame any) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(frame); err != nil {
		return fmt.Errorf("qqbot: 写帧失败: %w", err)
	}
	return nil
}

// dial 建立 WS 连接(http(s):// 转 ws(s):// 以便测试复用 httptest)。
func (g *Gateway) dial(ctx context.Context, url string) (*websocket.Conn, error) {
	wsURL := url
	if strings.HasPrefix(url, "http://") {
		wsURL = "ws://" + strings.TrimPrefix(url, "http://")
	} else if strings.HasPrefix(url, "https://") {
		wsURL = "wss://" + strings.TrimPrefix(url, "https://")
	}
	conn, resp, err := g.Dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("qqbot: WS 建立失败(%s): %v", resp.Status, truncate(string(body), 200))
		}
		return nil, fmt.Errorf("qqbot: WS 建立失败: %w", err)
	}
	return conn, nil
}

// fetchGatewayURL GET /gateway/bot(带鉴权头;response.url 为 wss 地址)。
func (g *Gateway) fetchGatewayURL(ctx context.Context, token string) (string, error) {
	base := g.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/gateway/bot", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qqbot: /gateway/bot http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.URL == "" {
		return "", fmt.Errorf("qqbot: /gateway/bot 响应缺 url: %s", truncate(string(raw), 200))
	}
	return out.URL, nil
}

// classifyClose 解析断线原因:CloseFrame 按官方 code 分类(4006/4007 清 session 重走 Identify)。
// 一律保留原始 CloseError 链(isFatal 需 errors.As 识别 4914/4915)。
func classifyClose(err error) error {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 4006, 4007: // 无效 session/seq 错误:resume 不可用,下次应 identify
			return fmt.Errorf("qqbot: WS 关闭 code=%d,%w", ce.Code, errInvalidSession)
		case 4914, 4915: // 机器人下架/封禁,不可连接(isFatal 据此停止重试)
			return fmt.Errorf("qqbot: WS 关闭 code=%d(机器人不可连接),不可重试: %w", ce.Code, ce)
		default: // 4009 连接过期等:保留 session 走 resume
			return fmt.Errorf("qqbot: WS 关闭 code=%d: %s: %w", ce.Code, ce.Text, ce)
		}
	}
	return err
}

// isFatal 不可恢复错误(重试无意义):未配置或机器人下架/封禁;其余一律退避重连。
func isFatal(err error) bool {
	if errors.Is(err, ErrNotConfigured) {
		return true
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code == 4914 || ce.Code == 4915
	}
	return false
}

// IdentifyToken 便捷:从 RawToken 拼 WS 鉴权 token("QQBot "+t;空则不再拼接)。
func IdentifyToken(t string) string {
	if t == "" || strings.HasPrefix(t, "QQBot ") {
		return t
	}
	return "QQBot " + t
}
