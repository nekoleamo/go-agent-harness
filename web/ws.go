// WebSocket 通道(M7.3,第二事件载体)。零外部依赖实现 RFC 6455 最小子集:
//   - 仅文本帧(承载与 SSE 相同的 JSON Frame payload;帧 id/重放语义一致)
//   - 服务端单向推送(事件下行);握手校验 Origin 同源(防跨站连接)
//   - 无扩展/分片/二进制帧处理(客户端为本仓 transport.ts,仅发 close)
//
// 通道 seam:EventHub 即统一订阅源(SSE 与 WS 消费同一 <-chan Frame),编码载体各自实现;
// 前端 transport.ts 优先 WS,失败自动降级 EventSource。
package web

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// wsGUID RFC 6455 固定魔数。
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsAcceptKey 计算 Sec-WebSocket-Accept(握手应答)。
func wsAcceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// errNotUpgrade 非 101 升级请求(调用方据此回退 SSE 语义)。
var errNotUpgrade = errors.New("not a websocket upgrade")

// wsConn 升级后的 WebSocket 连接(写侧文本帧)。
type wsConn struct {
	rw  *bufio.ReadWriter // hijack 后读写器(底层 net.Conn 经 rw.Reader/Writer 交互)
	raw net.Conn
}

// newWSConn / 由 wsTryUpgrade 构造。

// wsTryUpgrade 处理握手:验证方法/头/Origin(同源才升级)。
// 非升级请求返回 errNotUpgrade;校验失败写 4xx 并返回 errNotUpgrade(客户端降级)。
func wsTryUpgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, errNotUpgrade
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" || r.Header.Get("Upgrade") != "websocket" || !strings.Contains(r.Header.Get("Connection"), "Upgrade") {
		http.Error(w, "bad websocket handshake", http.StatusBadRequest)
		return nil, errNotUpgrade
	}
	// 同源校验(Origin 与 Host 一致才升级;无 Origin = 非浏览器客户端放行)
	if origin := r.Header.Get("Origin"); origin != "" {
		if !sameOrigin(origin, r.Host) {
			http.Error(w, "cross-origin websocket rejected", http.StatusForbidden)
			return nil, errNotUpgrade
		}
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return nil, errNotUpgrade
	}
	raw, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	_, _ = rw.Discard(rw.Reader.Buffered()) // 忽略请求体残留(浏览器通常无)
	accept := wsAcceptKey(key)
	handshake := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(handshake); err != nil {
		raw.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		raw.Close()
		return nil, err
	}
	return &wsConn{rw: rw, raw: raw}, nil
}

// sameOrigin Origin 与 Host 同源判定(http(s)://host[:port] 与 Host 头一致;忽略默认端口差异)。
func sameOrigin(origin, host string) bool {
	o := origin
	if strings.HasPrefix(o, "https://") {
		o = strings.TrimPrefix(o, "https://")
	} else if strings.HasPrefix(o, "http://") {
		o = strings.TrimPrefix(o, "http://")
	} else {
		return false // 非 http(s) origin(如 null/file)拒绝
	}
	if i := strings.Index(o, "/"); i >= 0 {
		o = o[:i]
	}
	return stripDefaultPort(o) == stripDefaultPort(host)
}

func stripDefaultPort(h string) string {
	return strings.TrimSuffix(strings.TrimSuffix(h, ":80"), ":443")
}

// WriteText 发送一条文本帧(JSON Frame;<=125 内联长度,更大走 16 位扩展长度;服务端帧无掩码)。
func (c *wsConn) WriteText(p []byte) error {
	if len(p) > 0xFFFF {
		return errors.New("ws frame too large")
	}
	var hdr [4]byte
	hdr[0] = 0x81 // FIN + text(服务端帧,无掩码位)
	if len(p) < 126 {
		hdr[1] = byte(len(p))
		if _, err := c.rw.Write(hdr[:2]); err != nil {
			return err
		}
	} else {
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(len(p)))
		if _, err := c.rw.Write(hdr[:4]); err != nil {
			return err
		}
	}
	if _, err := c.rw.Write(p); err != nil {
		return err
	}
	return c.rw.Flush()
}

// Close 关闭底层连接。
func (c *wsConn) Close() error { return c.raw.Close() }

var _ = fmt.Sprintf // keep fmt (文档示例引用)
