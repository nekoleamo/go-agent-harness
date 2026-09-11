// WebSocket 通道单测(M7.3):握手 Accept 官方向量、文本帧编码字节断言、同源判定。
package web

import (
	"bufio"
	"bytes"
	"net"
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
