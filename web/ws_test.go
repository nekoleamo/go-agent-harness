// WebSocket 通道单测(M7.3):握手 Accept 官方向量、文本帧编码字节断言、同源判定。
package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 6455 4.2.2/5.1 官方示例向量。
func TestWSAcceptKey(t *testing.T) {
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := wsAcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != want {
		t.Fatalf("Accept 键不符: 得 %q, 期望 %q", got, want)
	}
}

func TestWSWriteTextFrames(t *testing.T) {
	// 短帧(内联长度)
	var b bytes.Buffer
	c := &wsConn{rw: bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(nil)), bufio.NewWriter(&b)), raw: nilConn{}}
	if err := c.WriteText([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	got := b.Bytes()
	if len(got) != 4 || got[0] != 0x81 || got[1] != 0x02 || !bytes.Equal(got[2:], []byte("hi")) {
		t.Fatalf("短帧字节不符: % x", got)
	}
	// 长帧(16 位扩展长度;>125 字节)
	b.Reset()
	long := bytes.Repeat([]byte("a"), 200)
	if err := c.WriteText(long); err != nil {
		t.Fatal(err)
	}
	got = b.Bytes()
	if got[0] != 0x81 || got[1] != 126 || got[2] != 0 || got[3] != 200 || !bytes.Equal(got[4:], long) {
		t.Fatalf("长帧字节不符: 头=% x", got[:4])
	}
	// 64 位扩展长度(RFC 6455 §5.2):工具结果可远超 64 KiB,不得再直接报错
	// (报错会断开整条事件流 → 前端反复重连失败后永久降级 SSE)。
	b.Reset()
	huge := bytes.Repeat([]byte("b"), 0x10002)
	if err := c.WriteText(huge); err != nil {
		t.Fatalf("超 64 KiB 应走 64 位长度头: %v", err)
	}
	got = b.Bytes()
	want := []byte{0x81, 127, 0, 0, 0, 0, 0, 1, 0, 2}
	if !bytes.Equal(got[:10], want) {
		t.Fatalf("64 位长度头不符: 头=% x want=% x", got[:10], want)
	}
	if !bytes.Equal(got[10:], huge) {
		t.Fatal("64 位长帧载荷不符")
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:2233", "127.0.0.1:2233", true},
		{"http://127.0.0.1", "127.0.0.1:80", true}, // 默认端口归一
		{"https://example.com", "example.com:443", true},
		{"http://evil.example", "127.0.0.1:2233", false}, // 跨源拒绝
		{"null", "127.0.0.1:2233", false},                // 非 http(s) origin 拒绝
		{"ftp://x.y", "x.y", false},
	}
	for _, c := range cases {
		if got := sameOrigin(c.origin, c.host); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v, 期望 %v", c.origin, c.host, got, c.want)
		}
	}
}

// nilConn WriteText 测试用的哑连接(Close 无操作)。
type nilConn struct{ net.Conn }

func (nilConn) Close() error { return nil }

// SetWriteDeadline 哑连接无真实超时(写帧路径会调用)。
func (nilConn) SetWriteDeadline(time.Time) error { return nil }

var _ = strings.TrimSpace // keep strings referenced

// —— 端到端:握手三判据 + 首帧契约(此前 ws.go 的升级路径 0 覆盖)——
//
// 为什么补:上面的字节断言只覆盖了编码函数;`wsTryUpgrade` 的成功路径与 `handleEventsWS`
// 的推送循环没人测过,而它们才是「页面到底收不收得到事件」的那个接缝。

// wsHandshake 以裸 TCP 完成一次握手(不引客户端库:主模块没有 websocket 依赖)。
func wsHandshake(t *testing.T, addr, origin string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连不上 %s: %v", addr, err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /api/events/ws HTTP/1.1\r\nHost: " + addr + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	req += "\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatalf("写握手失败: %v", err)
	}
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读握手应答失败: %v", err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读应答头失败: %v", err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	return c, br, strings.TrimSpace(status)
}

// readServerFrame 读一条服务端帧(无掩码),返回 opcode 与载荷。
func readServerFrame(t *testing.T, br *bufio.Reader) (byte, []byte) {
	t.Helper()
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		t.Fatalf("读帧头失败: %v", err)
	}
	op := hdr[0] & 0x0F
	n := int(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			t.Fatalf("读 16 位长度失败: %v", err)
		}
		n = int(ext[0])<<8 | int(ext[1])
	case 127:
		t.Fatalf("本用例的帧不会到 64 位长度分支")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("读帧体失败(期望 %d 字节): %v", n, err)
	}
	return op, body
}

// TestEventsWSHandshakeAndFirstFrame 同源握手 101、首帧必须是 baseline(前端契约)、
// 之后实例级帧经同一条连接下发;跨源握手 403(否则任意网页都能连上本机事件流)。
func TestEventsWSHandshakeAndFirstFrame(t *testing.T) {
	s, _ := newTestServer()
	s.cfg.Addr = "127.0.0.1:2233"
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	addr := strings.TrimPrefix(hs.URL, "http://")

	c, _, status := wsHandshake(t, addr, "http://evil.example")
	_ = c.Close()
	if !strings.Contains(status, "403") {
		t.Fatalf("跨源握手应 403,得到 %q", status)
	}

	c, br, status := wsHandshake(t, addr, "http://"+addr)
	defer func() { _ = c.Close() }()
	if !strings.Contains(status, "101") {
		t.Fatalf("同源握手应 101,得到 %q", status)
	}

	op, body := readServerFrame(t, br)
	if op != wsOpText {
		t.Fatalf("首帧 opcode = %#x,应文本帧", op)
	}
	var f Frame
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatalf("首帧不是 JSON: %v(%s)", err, body)
	}
	if f.Type != FrameBaseline {
		t.Fatalf("首帧应为 baseline,得到 %q", f.Type)
	}

	s.hub.Push(Frame{Type: FrameNotice, Payload: map[string]any{"title": "t", "level": "warn"}})
	op, body = readServerFrame(t, br)
	if op != wsOpText {
		t.Fatalf("推送帧 opcode = %#x", op)
	}
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatalf("推送帧不是 JSON: %v(%s)", err, body)
	}
	if f.Type != FrameNotice {
		t.Fatalf("推送帧应为 notice,得到 %q", f.Type)
	}
}

// TestWSRejectsBadHandshake 非 GET / 缺升级头都不升级(且不 panic)。
func TestWSRejectsBadHandshake(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/events/ws", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	// 路由是 `GET /api/events/ws`:非 GET 落不到升级分支(ServeMux 让它落到静态处理器 404
	// 或直接 405)。这里只钉住「没有升级」。
	if resp.StatusCode == http.StatusSwitchingProtocols || resp.StatusCode < 400 {
		t.Fatalf("非 GET 不该升级,得到 %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, hs.URL+"/api/events/ws", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺升级头应 400,得到 %d", resp.StatusCode)
	}
}
