// WebSocket 通道单测(M7.3):握手 Accept 官方向量、文本帧编码字节断言、同源判定。
package web

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
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
	// 超限拒绝
	if err := c.WriteText(make([]byte, 0x10000)); err == nil {
		t.Fatal("超 0xFFFF 应拒绝")
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

var _ = strings.TrimSpace // keep strings referenced
