package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoopbackHost 钉住 DNS rebinding 防护:只有回环原名/回环 IP 才放行。
// 反向验证:把 loopbackHost 改成恒 true,本用例的 evil.example 一条必红。
func TestLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"127.0.0.2:1", true},
		{"evil.example:8080", false},
		{"evil.example", false},
		{"10.0.0.1:8080", false},
		{"localhost.evil.example:1", false},
	}
	for _, c := range cases {
		if got := loopbackHost(c.host); got != c.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestGuardGates 钉住控制面三道闸:token 缺失 → 401;Origin 非本页面 → 403;
// Host 非回环 → 403;都满足 → 200 + CORS 头。
func TestGuardGates(t *testing.T) {
	cp := &controlPlane{token: "tok", origin: "http://127.0.0.1:2233", sh: &shell{addr: "127.0.0.1:2233"}}
	h := cp.guard(cp.handlePing)

	do := func(method, host, token, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+host+"/shell/ping", nil)
		req.Host = host
		if token != "" {
			req.Header.Set("X-Shell-Token", token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}

	if rec := do(http.MethodGet, "127.0.0.1:9999", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401,得到 %d", rec.Code)
	}
	if rec := do(http.MethodGet, "127.0.0.1:9999", "tok", "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("外部 Origin 应 403,得到 %d", rec.Code)
	}
	if rec := do(http.MethodGet, "evil.example:9999", "tok", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("非回环 Host 应 403,得到 %d", rec.Code)
	}
	rec := do(http.MethodGet, "127.0.0.1:9999", "tok", "http://127.0.0.1:2233")
	if rec.Code != http.StatusOK {
		t.Fatalf("合法请求应 200,得到 %d(%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:2233" {
		t.Fatalf("CORS 头 = %q", got)
	}
	// 预检不应要求 token(浏览器预检带不了自定义头),但 Origin 仍要校验。
	if rec := do(http.MethodOptions, "127.0.0.1:9999", "", "http://127.0.0.1:2233"); rec.Code != http.StatusNoContent {
		t.Fatalf("预检应 204,得到 %d", rec.Code)
	}
}

// TestShellURLTokenLast 钉住 fragment 顺序:gah 引导页的正则 `[#&]token=` 会取到
// fragment **末尾**,所以 gah 自己的 token 必须是最后一个参数,否则会被吞掉。
func TestShellURLTokenLast(t *testing.T) {
	cp := &controlPlane{token: "ctltok", origin: "http://127.0.0.1:1"}
	cp.ln = fakeListener{addr: "127.0.0.1:5555"}
	sh := &shell{addr: "127.0.0.1:2233", gahToken: "gahtok", ctl: cp}
	u := sh.shellURL()
	if !strings.HasSuffix(u, "&token=gahtok") {
		t.Fatalf("gah token 必须在 fragment 末尾: %s", u)
	}
	if !strings.Contains(u, "shellport=5555") || !strings.Contains(u, "shelltoken=ctltok") {
		t.Fatalf("控制面凭据缺失: %s", u)
	}
	if !strings.Contains(u, "/?shell=desktop#") {
		t.Fatalf("桌面壳路径标记缺失: %s", u)
	}
	// 无 token 模式(gah 未开鉴权)时不应出现 token 参数。
	sh.gahToken = ""
	if strings.Contains(sh.shellURL(), "token=gahtok") {
		t.Fatalf("无 token 时不该带 token 参数: %s", sh.shellURL())
	}
}

// TestRedactURLHidesToken 日志里的 URL 会随用户反馈流转,凭据必须打码。
func TestRedactURLHidesToken(t *testing.T) {
	got := redactURL("http://127.0.0.1:2233/?shell=desktop#shellport=5555&shelltoken=SECRET&token=SECRET2")
	if strings.Contains(got, "SECRET") {
		t.Fatalf("凭据未打码: %s", got)
	}
	if !strings.Contains(got, "shellport=%3Cctl%3E") && !strings.Contains(got, "shellport=<ctl>") {
		t.Fatalf("保留结构信息有助排查: %s", got)
	}
}

// TestLooksLikeLogLine sidecar 的 stdout 是 go-plugin 的二进制 RPC 通道:
// 协议帧不能进日志。反向验证:把阈值改成恒 true,二进制帧一条必红。
func TestLooksLikeLogLine(t *testing.T) {
	if !looksLikeLogLine("time=2026 level=INFO msg=\"gah booted\"") {
		t.Fatal("正常日志行被误判")
	}
	if looksLikeLogLine("\x01\x02\x00\x00binary\x00frame\x03") {
		t.Fatal("二进制帧被当成日志")
	}
}

// TestCookieHeader token 模式的请求头形状(空 token = 不带 cookie)。
func TestCookieHeader(t *testing.T) {
	if cookieHeader("") != "" {
		t.Fatal("空 token 不该产生 cookie")
	}
	if got := cookieHeader("a b"); got != "gah_token=a+b" {
		t.Fatalf("cookie 需转义,得到 %q", got)
	}
}

// fakeListener 只为在单测里给出固定端口(shellURL 只读 Addr())。
type fakeListener struct{ addr string }

func (f fakeListener) Accept() (net.Conn, error) { return nil, nil }
func (f fakeListener) Close() error              { return nil }
func (f fakeListener) Addr() net.Addr            { return fakeAddr(f.addr) }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// TestSessionURLTokenLast 会话窗口地址:带 &session=,且 gah token 仍在 fragment 末尾
// (引导页正则取到 fragment 末尾,顺序错了 token 会被吞掉 → 会话窗口拿不到 cookie)。
func TestSessionURLTokenLast(t *testing.T) {
	cp := &controlPlane{token: "ctltok", origin: "http://127.0.0.1:1"}
	cp.ln = fakeListener{addr: "127.0.0.1:5555"}
	sh := &shell{addr: "127.0.0.1:2233", gahToken: "gahtok", ctl: cp}
	u := sh.sessionURL("20261029-101010")
	if !strings.Contains(u, "/?shell=desktop&session=20261029-101010#") {
		t.Fatalf("会话窗口地址形状不对: %s", u)
	}
	if !strings.HasSuffix(u, "&token=gahtok") {
		t.Fatalf("gah token 必须在 fragment 末尾: %s", u)
	}
	if !strings.Contains(u, "shellport=5555") {
		t.Fatalf("控制面凭据缺失: %s", u)
	}
}

// TestPostOnlyRejectsGET 会改状态的端点只接 POST:GET 带副作用会让「预取/预览」
// 类请求也能触发动作(CSRF 的另一半)。
// 反向验证:把 postOnly 换成直通,GET 断言必红。
func TestPostOnlyRejectsGET(t *testing.T) {
	cp := &controlPlane{token: "tok", origin: "http://127.0.0.1:2233", sh: &shell{addr: "127.0.0.1:2233"}}
	h := cp.guard(cp.postOnly(cp.handleLog))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9/shell/log", nil)
	req.Host = "127.0.0.1:9"
	req.Header.Set("X-Shell-Token", "tok")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405,得到 %d", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9/shell/log", strings.NewReader(`{"line":"x"}`))
	req2.Host = "127.0.0.1:9"
	req2.Header.Set("X-Shell-Token", "tok")
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST 应 200,得到 %d(%s)", rec2.Code, rec2.Body.String())
	}
}
