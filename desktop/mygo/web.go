// 与 gah web(sidecar)之间的最小 HTTP 通道:挑端口、探活、优雅停机(与 Wails 壳同口径)。
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// pickFreeAddr 向内核要一个空闲回环端口。
func pickFreeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	return addr, l.Close()
}

func gahClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func cookieHeader(token string) string {
	if token == "" {
		return ""
	}
	return "gah_token=" + url.QueryEscape(token)
}

// httpProbe GET 一个回环路径,返回状态码;连不上/超时返回 0。
func httpProbe(addr, path, token string, timeout time.Duration) int {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return 0
	}
	if c := cookieHeader(token); c != "" {
		req.Header.Set("Cookie", c)
	}
	cl := &http.Client{Timeout: timeout}
	resp, err := cl.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode
}

// stateAlive 服务是否还在应答(= 200,或 token 模式下的 401)。
func stateAlive(addr, token string, timeout time.Duration) bool {
	code := httpProbe(addr, "/api/state", token, timeout)
	return code == http.StatusOK || code == http.StatusUnauthorized
}

// postShutdown 请求 sidecar 优雅停机。
func postShutdown(addr, token string) error {
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/shutdown", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c := cookieHeader(token); c != "" {
		req.Header.Set("Cookie", c)
	}
	resp, err := gahClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("shutdown 返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

// waitPortReleased 等 sidecar 真的放开监听口(最多 d)。
func waitPortReleased(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if httpProbe(addr, "/api/state", "", 200*time.Millisecond) == 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// waitReady 轮询就绪(最多 d)。
func waitReady(addr, token string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if stateAlive(addr, token, 400*time.Millisecond) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
