// Command ws-smoke M7.3 WS 通道冒烟(CI 用;零外部依赖):POST 一条 input 制造事件,
// 经 /api/events/ws 建立 WebSocket(手写最小客户端:握手校验 Accept 官方向量 + 读首帧),
// 断言收到 user/message 会话帧。失败 exit 1(go run 路径即可,无需预编译)。
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func main() {
	// 1. 制造事件:POST /api/input(回合由 mock/真实 LLM 异步执行;事件流即时可见)
	if resp, err := http.Post("http://127.0.0.1:2233/api/input", "application/json",
		strings.NewReader(`{"content":"ping"}`)); err != nil {
		fatal("input POST: %v", err)
	} else {
		resp.Body.Close()
	}
	deadline := time.Now().Add(8 * time.Second)

	// 2. WebSocket 握手(最小客户端)
	conn, err := net.Dial("tcp", "127.0.0.1:2233")
	if err != nil {
		fatal("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	h := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h[:])
	req := "GET /api/events/ws HTTP/1.1\r\nHost: 127.0.0.1:2233\r\nUpgrade: websocket\r\n" +
		"Connection: Upgrade\r\nSec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		fatal("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	var hdr strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			fatal("read handshake: %v", err)
		}
		hdr.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	if !strings.Contains(hdr.String(), "101") || !strings.Contains(hdr.String(), accept) {
		fatal("握手不符(应 101 + Accept 官方向量):\n%s", hdr.String())
	}

	// 3. 读帧,直到命中 user/message(历史重放或实时;input 后事件必达)
	for {
		var hb [2]byte
		if _, err := br.Read(hb[:2]); err != nil {
			fatal("read frame: %v", err)
		}
		if hb[0]&0x0f != 1 { // 非文本帧忽略
			continue
		}
		ln := int(hb[1])
		if ln == 126 {
			var ext [2]byte
			if _, err := br.Read(ext[:]); err != nil {
				fatal("read ext len: %v", err)
			}
			ln = int(binary.BigEndian.Uint16(ext[:]))
		}
		payload := make([]byte, ln)
		if _, err := br.Read(payload); err != nil {
			fatal("read payload: %v", err)
		}
		if strings.Contains(string(payload), "user/message") {
			fmt.Printf("WS-SMOKE-OK: %s\n", trunc(payload, 100))
			return
		}
	}
}

func trunc(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ws-smoke: "+format+"\n", args...)
	os.Exit(1)
}
