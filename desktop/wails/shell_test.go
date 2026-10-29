package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestShellURL 主窗口地址:带 ?shell=desktop;token 模式下 fragment 只剩 gah 自己的令牌。
//
// **控制面凭据不得出现在 URL 里**(2026-10-29 改):它经 env 交给 sidecar,由 /shell/*
// 同源代理转发 —— 页面里的 UI 插件 JS 拿不到它。反向验证:把 ctl token 拼回 URL,
// 第二条断言必红。
func TestShellURL(t *testing.T) {
	cp := &controlPlane{token: "ctltok", origin: "http://127.0.0.1:1"}
	cp.ln = fakeListener{addr: "127.0.0.1:5555"}
	sh := &shell{addr: "127.0.0.1:2233", gahToken: "gahtok", ctl: cp}

	u := sh.shellURL()
	if !strings.Contains(u, "/?shell=desktop") {
		t.Fatalf("缺桌面壳标记: %s", u)
	}
	if strings.Contains(u, "ctltok") || strings.Contains(u, "shelltoken") || strings.Contains(u, "shellport") {
		t.Fatalf("控制面凭据不该进 URL: %s", u)
	}
	if !strings.HasSuffix(u, "#token=gahtok") {
		t.Fatalf("gah token 必须在 fragment 末尾: %s", u)
	}

	sh.gahToken = ""
	if u2 := sh.shellURL(); strings.Contains(u2, "#") {
		t.Fatalf("无 token 时不该有 fragment: %s", u2)
	}
}

// TestRedactURLHidesToken 日志里的 URL 会随用户反馈流转,凭据必须打码。
func TestRedactURLHidesToken(t *testing.T) {
	got := redactURL("http://127.0.0.1:2233/?shell=desktop#token=SECRET2")
	if strings.Contains(got, "SECRET2") {
		t.Fatalf("凭据未打码: %s", got)
	}
	if !strings.Contains(got, "shell=desktop") {
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

// TestSessionURL 会话窗口地址:主线同源 + &session=,凭据同样不进 URL。
func TestSessionURL(t *testing.T) {
	cp := &controlPlane{token: "ctltok", origin: "http://127.0.0.1:1"}
	cp.ln = fakeListener{addr: "127.0.0.1:5555"}
	sh := &shell{addr: "127.0.0.1:2233", gahToken: "gahtok", ctl: cp}
	u := sh.sessionURL("20261029-101010")
	if !strings.Contains(u, "/?shell=desktop&session=20261029-101010") {
		t.Fatalf("会话窗口地址形状不对: %s", u)
	}
	if strings.Contains(u, "ctltok") {
		t.Fatalf("控制面凭据不该进 URL: %s", u)
	}
	if !strings.HasSuffix(u, "#token=gahtok") {
		t.Fatalf("gah token 必须在 fragment 末尾: %s", u)
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

// TestShellNoticesStore 提示存储:整体替换、空列表清空、返回快照(调用方改不到内部)。
// 反向验证:把 getShellNotices 改成直接返回内部切片,第三条断言必红。
func TestShellNoticesStore(t *testing.T) {
	setShellNotices([]string{"便携模式:数据在本目录"})
	got := getShellNotices()
	if len(got) != 1 || got[0] != "便携模式:数据在本目录" {
		t.Fatalf("存储读回不对: %v", got)
	}
	// 快照:外部改切片不影响内部。
	got[0] = "被篡改"
	if again := getShellNotices(); again[0] != "便携模式:数据在本目录" {
		t.Fatalf("应返回副本,内部被改成了 %q", again[0])
	}
	setShellNotices(nil)
	if len(getShellNotices()) != 0 {
		t.Fatal("空列表应清空")
	}
}

// TestLostTracker 连续 N 次不可达才报,且只报一次;恢复后重新武装。
// 反向验证:去掉 reported 守卫,第三条断言(不重复)必红。
func TestLostTracker(t *testing.T) {
	var tr lostTracker
	for i := 1; i < sidecarLostStreak; i++ {
		if notify, _ := tr.observe(false); notify {
			t.Fatalf("第 %d 次就报失联(太少)", i)
		}
	}
	if notify, _ := tr.observe(false); !notify {
		t.Fatalf("%d 次连续不可达应报失联", sidecarLostStreak)
	}
	if notify, _ := tr.observe(false); notify {
		t.Fatal("同一次失联不该反复提示")
	}
	if _, recovered := tr.observe(true); !recovered {
		t.Fatal("恢复应报 recovered")
	}
	// 再次掉线要能重新报警。
	for i := 0; i < sidecarLostStreak-1; i++ {
		tr.observe(false)
	}
	if notify, _ := tr.observe(false); !notify {
		t.Fatal("恢复后再次失联应重新报警")
	}
}

// TestWantsNewInstance --new-instance 是「另一个进程」的开关(不注册单实例)。
func TestWantsNewInstance(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"gah-desktop"}
	if wantsNewInstance() {
		t.Fatal("默认不该是新实例")
	}
	os.Args = []string{"gah-desktop", "--new-instance"}
	if !wantsNewInstance() {
		t.Fatal("带 --new-instance 应识别")
	}
	if singleInstanceOptions(true, &shell{}) != nil {
		t.Fatal("新实例必须不注册单实例(否则第二个进程会自己退出)")
	}
	if singleInstanceOptions(false, &shell{}) == nil {
		t.Fatal("默认必须注册单实例")
	}
}

// TestUpdateAutoCheckSeconds 升级冒烟入口:>0 才生效,非法值忽略。
func TestUpdateAutoCheckSeconds(t *testing.T) {
	t.Setenv("GAH_SHELL_UPDATE_AUTOCHECK", "")
	if updateAutoCheckSeconds() != 0 {
		t.Fatal("未设置应为 0")
	}
	t.Setenv("GAH_SHELL_UPDATE_AUTOCHECK", "3")
	if updateAutoCheckSeconds() != 3 {
		t.Fatal("应解析为 3")
	}
	t.Setenv("GAH_SHELL_UPDATE_AUTOCHECK", "abc")
	if updateAutoCheckSeconds() != 0 {
		t.Fatal("非法值应为 0(不猜)")
	}
}
