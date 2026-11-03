// 首帧门控(2026-11-03):页面首帧画好之前不显窗 —— 修「启动先闪一下黑底」。
//
// 这里钉两条:
//  1. `/shell/ui-ready` 这条路由**在 guard 之内**(回环 Host + 一次性 token + 只放行
//     gah 那个源):漏一条就等于让本机任意网页把我们的窗口弹出来;
//  2. 就绪信号幂等:多条路径/重复请求/并发上报都只关一次通道(close 两次 = panic)。
package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newReadyShell 造一个只带首帧通道的壳(其余字段这些用例用不到)。
func newReadyShell() *shell { return &shell{uiReady: make(chan struct{})} }

// TestUIReadyRouteGuarded 三道闸 + 方法约束 + 正常路径全走一遍 routes()。
// 反向验证:把 routes() 里那条 ui-ready 改成不带 c.guard,前两条必红。
func TestUIReadyRouteGuarded(t *testing.T) {
	sh := newReadyShell()
	cp := &controlPlane{token: "tok", origin: "http://127.0.0.1:9", sh: sh}
	h := cp.routes()

	do := func(method, host, token, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/shell/ui-ready", nil)
		req.Host = host
		if token != "" {
			req.Header.Set("X-Shell-Token", token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if got := do(http.MethodPost, "evil.example", "tok", "").Code; got != http.StatusForbidden {
		t.Fatalf("非回环 Host 必须拒: got %d", got)
	}
	if got := do(http.MethodPost, "127.0.0.1:9", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("缺 token 必须拒: got %d", got)
	}
	if got := do(http.MethodPost, "127.0.0.1:9", "tok", "http://evil.example").Code; got != http.StatusForbidden {
		t.Fatalf("非法 Origin 必须拒: got %d", got)
	}
	if got := do(http.MethodGet, "127.0.0.1:9", "tok", "").Code; got != http.StatusMethodNotAllowed {
		t.Fatalf("只接受 POST: got %d", got)
	}
	select {
	case <-sh.uiReady:
		t.Fatal("被拒的请求不该触发首帧就绪")
	default:
	}

	if got := do(http.MethodPost, "127.0.0.1:9", "tok", "").Code; got != http.StatusNoContent {
		t.Fatalf("正常上报应 204: got %d", got)
	}
	select {
	case <-sh.uiReady:
	default:
		t.Fatal("上报后应关掉首帧通道(否则窗口永不显示,直到 1.2s 兜底)")
	}
}

// TestUIReadySignalIsIdempotent 并发/重复上报只关一次通道,且不 panic。
// 反向验证:去掉 markUIFirstFrame 里的 sync.Once(直接 close),-race 下本用例必 panic。
func TestUIReadySignalIsIdempotent(t *testing.T) {
	sh := newReadyShell()
	cp := &controlPlane{token: "tok", origin: "http://127.0.0.1:9", sh: sh}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cp.handleUIReady(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/shell/ui-ready", nil))
		}()
	}
	wg.Wait()
	// 迟到的重复上报(例如页面重载后又报一次)同样不能炸。
	cp.handleUIReady(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/shell/ui-ready", nil))

	select {
	case <-sh.uiReady:
	case <-time.After(time.Second):
		t.Fatal("首帧通道应已关闭")
	}
}
