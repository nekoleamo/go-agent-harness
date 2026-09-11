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
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
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

// wsConn 升级后的 WebSocket 连接(写侧文本帧;写侧加锁以支持 ping 与事件推送并发)。
type wsConn struct {
	rw  *bufio.ReadWriter // hijack 后读写器(底层 net.Conn 经 rw.Reader/Writer 交互)
	raw net.Conn
	wmu sync.Mutex // 写侧互斥(ping 保活帧与事件帧不得交错)
}

// ws 帧操作码(RFC 6455 最小子集)。
const (
	wsOpText  = 0x1
	wsOpClose = 0x8
	wsOpPing  = 0x9
)

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

// writeFrame 写一条服务端帧(无掩码);写侧加锁 + 写超时(慢/死客户端不永久阻塞推送)。
// 长度头按 RFC 6455 §5.2 三分支实现(7 位/16 位/64 位):工具结果全文可远超 64 KiB,
// 此前仅实现 16 位分支并在 >0xFFFF 时直接报错 → 读一个大文件就让整个事件流断开、
// 前端反复重连失败后永久降级 SSE。浏览器侧对单帧长度无上限。
func (c *wsConn) writeFrame(op byte, p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(10 * time.Second))
	var hdr [10]byte
	hdr[0] = 0x80 | op // FIN + opcode
	var err error
	switch {
	case len(p) < 126:
		hdr[1] = byte(len(p))
		_, err = c.rw.Write(hdr[:2])
	case len(p) <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(len(p)))
		_, err = c.rw.Write(hdr[:4])
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(len(p)))
		_, err = c.rw.Write(hdr[:10])
	}
	if err != nil {
		return err
	}
	if _, err := c.rw.Write(p); err != nil {
		return err
	}
	return c.rw.Flush()
}

// WriteText 发送一条文本帧(JSON Frame 载体)。
func (c *wsConn) WriteText(p []byte) error { return c.writeFrame(wsOpText, p) }

// WritePing 发送 ping 保活帧(浏览器自动回 pong)。
func (c *wsConn) WritePing() error { return c.writeFrame(wsOpPing, nil) }

// drain 读泵:消费客户端帧(本仓前端仅发 close/pong),兼作断连探测。
// 读错误即返回,调用方据此关闭连接并停止推送(防 goroutine/fd/订阅泄漏)。
func (c *wsConn) drain() {
	for {
		b0, err := c.rw.Reader.ReadByte()
		if err != nil {
			return
		}
		b1, err := c.rw.Reader.ReadByte()
		if err != nil {
			return
		}
		op := b0 & 0x0F
		masked := b1&0x80 != 0
		n := int64(b1 & 0x7F)
		switch n {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.rw.Reader, ext[:]); err != nil {
				return
			}
			n = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.rw.Reader, ext[:]); err != nil {
				return
			}
			n = int64(binary.BigEndian.Uint64(ext[:]))
		}
		if n > 1<<20 {
			return // 上行帧超限:事件通道无需大帧,直接断开
		}
		if masked {
			var mask [4]byte
			if _, err := io.ReadFull(c.rw.Reader, mask[:]); err != nil {
				return
			}
		}
		if n > 0 {
			if _, err := io.CopyN(io.Discard, c.rw.Reader, n); err != nil {
				return
			}
		}
		if op == wsOpClose {
			_ = c.writeFrame(wsOpClose, nil)
			return
		}
	}
}

// Close 关闭底层连接。
func (c *wsConn) Close() error { return c.raw.Close() }

var _ = fmt.Sprintf // keep fmt (文档示例引用)
