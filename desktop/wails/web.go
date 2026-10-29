// 与 gah web(sidecar)之间的最小 HTTP 通道:挑端口、探活、优雅停机。
//
// 与 Rust 壳同口径:
//   - 就绪 = GET /api/state 返回 200,token 模式下 401 也算就绪(服务在应答即可);
//   - 停机 = POST /api/shutdown(Windows 无 SIGTERM,这是唯一的跨平台优雅退出通道);
//   - token 模式(data.auth_token 非空)= 每次请求带 gah_token cookie。
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

// pickFreeAddr 向内核要一个空闲回环端口(绑 127.0.0.1:0 读回实际地址后立即释放)。
// 与真正绑定之间有极小的竞态窗口,可接受:真被抢也只是这次启动失败、重开一次。
// 固定 2233 不能用作默认值 —— 撞上别人的实例(孤儿 gah web / 用户自己开的)时,
// 壳会连上去显示旧版本的界面(2026-09-16 真机就是这么踩的)。
func pickFreeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	return addr, l.Close()
}

// gahClient 对 sidecar 的 HTTP 客户端(不跟随外部重定向,回环短超时)。
func gahClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// cookieHeader token 模式的请求头(空 token 返回空串 = 不带 cookie)。
func cookieHeader(token string) string {
	if token == "" {
		return ""
	}
	return "gah_token=" + url.QueryEscape(token)
}

// httpProbe GET 一个回环路径,返回 HTTP 状态码;连不上/超时返回 0。
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

// stateAlive 服务是否还在应答(= 200 或 401 token 模式)。
func stateAlive(addr, token string, timeout time.Duration) bool {
	code := httpProbe(addr, "/api/state", token, timeout)
	return code == http.StatusOK || code == http.StatusUnauthorized
}

// postShutdown 请求 sidecar 优雅停机(回收插件与外部进程后自己退出)。
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

// waitReady 轮询就绪(最多 d),返回是否就绪。
// 期间顺带记一条「还在等」的日志:真机上白屏时,日志里有没有这行是判断
// 「服务没起来」还是「壳根本没走这条路」的唯一依据。
func waitReady(addr, token string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if stateAlive(addr, token, 400*time.Millisecond) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
