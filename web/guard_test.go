// 安全护栏单测:CSRF(跨站来源)、DNS rebinding(Host 白名单)、请求体类型约束、
// 安全响应头、鉴权路径(Bearer/cookie;?token= 已弃用)。
package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// guardCase 构造一个直挂完整服务栈(护栏+鉴权)的 httptest 服务。
func guardServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, _ := newTestServer()
	s.cfg.Addr = "127.0.0.1:2233"
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs
}

func guardPost(t *testing.T, url, origin, host, contentType, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/api/input", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// 跨站来源(CSRF):浏览器必带 Origin → 403,即使 Content-Type 是 JSON。
func TestGuardRejectsCrossOriginStateChange(t *testing.T) {
	_, hs := guardServer(t)
	code := guardPost(t, hs.URL, "http://evil.example", "", "application/json", `{"content":"hi"}`)
	if code != http.StatusForbidden {
		t.Fatalf("跨站 POST 应 403,得 %d", code)
	}
	// Referer 回退路径同样拦截(Origin 缺失时)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/input", strings.NewReader(`{"content":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", "http://evil.example/page")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨站 Referer 应 403,得 %d", resp.StatusCode)
	}
}

// DNS rebinding:攻击域解析到本机时 Host 为攻击域(Origin 与之同源通过 Origin 校验)→ 必须由 Host 白名单拦下。
func TestGuardRejectsForeignHost(t *testing.T) {
	_, hs := guardServer(t)
	code := guardPost(t, hs.URL, "http://evil.example", "evil.example", "application/json", `{"content":"hi"}`)
	if code != http.StatusForbidden {
		t.Fatalf("非白名单 Host 应 403,得 %d", code)
	}
}

// CORS 简单请求(text/plain)必须被拒:否则跨站无需预检即可驱动工具执行。
func TestGuardRejectsNonJSONContentType(t *testing.T) {
	_, hs := guardServer(t)
	code := guardPost(t, hs.URL, "", "", "text/plain;charset=UTF-8", `{"command":"id"}`)
	if code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain 状态变更应 415,得 %d", code)
	}
	code = guardPost(t, hs.URL, "", "", "", `{}`)
	if code != http.StatusUnsupportedMediaType {
		t.Fatalf("缺 Content-Type 应 415,得 %d", code)
	}
}

// 同源浏览器请求与非浏览器客户端(无 Origin,如 curl)应放行到业务层。
func TestGuardAllowsSameOriginAndCLI(t *testing.T) {
	_, hs := guardServer(t)
	if code := guardPost(t, hs.URL, "http://127.0.0.1:2233", "127.0.0.1:2233", "application/json", `{"content":""}`); code == http.StatusForbidden || code == http.StatusUnsupportedMediaType {
		t.Fatalf("同源 POST 不应被护栏拦截,得 %d", code)
	}
	if code := guardPost(t, hs.URL, "", "localhost:2233", "application/json", `{"content":""}`); code == http.StatusForbidden || code == http.StatusUnsupportedMediaType {
		t.Fatalf("无 Origin(CLI)POST 不应被护栏拦截,得 %d", code)
	}
}

// 显式暴露到局域网(addr=0.0.0.0)时 Host 白名单退化为放行(仍受 Origin 校验)。
func TestGuardWildcardAddrAllowsAnyHost(t *testing.T) {
	s, _ := newTestServer()
	s.cfg.Addr = "0.0.0.0:2233"
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	if code := guardPost(t, hs.URL, "", "192.168.1.9:2233", "application/json", `{"content":""}`); code == http.StatusForbidden {
		t.Fatalf("通配监听时 Host 不应被 403,得 %d", code)
	}
}

// 安全响应头:禁 iframe 嵌套(审批弹层防点击劫持)。
func TestGuardSecurityHeaders(t *testing.T) {
	_, hs := guardServer(t)
	resp, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options 应 DENY,得 %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("CSP 应含 frame-ancestors 'none',得 %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options 应 nosniff,得 %q", got)
	}
}

// SPA 静态响应的专属 CSP(切断"已安装 UI 插件把会话内容外发"的通道):
// 只作用于静态面;/api/* 仍只带全局头(不被 SPA 指令覆盖)。
func TestSPACSPOnStaticOnly(t *testing.T) {
	_, hs := authServer(t, "")
	for _, path := range []string{"/", "/index.html", "/app.js"} {
		code, _, hdr := getBody(t, hs.URL+path, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s 应 200,得 %d", path, code)
		}
		csp := hdr.Get("Content-Security-Policy")
		for _, want := range []string{
			"default-src 'none'", "script-src 'self'", "style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:", "connect-src 'self'", "object-src 'none'",
			"base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'",
			// 文档预览是同源 iframe(DocPanel 的 PDF/转换产物/HTML 预览):
			// 写成 'none' 会直接弄坏预览,这里钉住 'self'
			"frame-src 'self'",
		} {
			if !strings.Contains(csp, want) {
				t.Fatalf("%s 的 CSP 缺 %q,得 %q", path, want, csp)
			}
		}
		// 放宽项必须不存在(防后续改动无声地把外发通道开回)
		for _, bad := range []string{"unsafe-eval", "connect-src *", "connect-src http", "script-src 'unsafe-inline'", "frame-src *"} {
			if strings.Contains(csp, bad) {
				t.Fatalf("%s 的 CSP 不应含 %q:%q", path, bad, csp)
			}
		}
	}
	// API 不被 SPA CSP 替换:仍是全局头
	code, _, hdr := getBody(t, hs.URL+"/api/state", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/state 应 200,得 %d", code)
	}
	if csp := hdr.Get("Content-Security-Policy"); csp != "frame-ancestors 'none'" {
		t.Fatalf("/api/* 的 CSP 应保持全局头,得 %q", csp)
	}
	// token 模式带凭据的入口页同样走 SPA CSP(引导页有自己的 CSP,不在此断)
	_, tks := authServer(t, "tk")
	if _, _, hdr := getBody(t, tks.URL+"/", &http.Cookie{Name: "gah_token", Value: "tk"}); !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("token 模式带凭据的入口页应带 SPA CSP,得 %q", hdr.Get("Content-Security-Policy"))
	}
}

// Handler() 必须与 Start() 同栈(此前 Handler() 直返 handler(),外部挂载默认无鉴权/无护栏)。
func TestHandlerIncludesGuard(t *testing.T) {
	s := New(Config{AuthToken: "tk"}, NewHub(), NewConfirm(NewHub()), slog.Default())
	s.cfg.Addr = "127.0.0.1:2233"
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	// 未带 token → 401(鉴权在栈内);带 token 但跨站 → 403(护栏在鉴权之后)
	if code := guardPost(t, hs.URL, "", "", "application/json", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("Handler() 应含鉴权(401),得 %d", code)
	}
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/input", strings.NewReader(`{"content":"hi"}`))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Handler() 应含护栏(403),得 %d", resp.StatusCode)
	}
}

// 鉴权:cookie 通道有效;?token= 已弃用(进历史/日志)。
func TestAuthCookieAndQueryToken(t *testing.T) {
	s, _ := newTestServer()
	s.cfg.AuthToken = "tk"
	s.cfg.Addr = "127.0.0.1:2233"
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()

	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/state", nil)
	req.AddCookie(&http.Cookie{Name: "gah_token", Value: "tk"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie token 应 200,得 %d", resp.StatusCode)
	}

	resp2, err := http.Get(hs.URL + "/api/state?token=tk")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("?token= 应被拒(401),得 %d", resp2.StatusCode)
	}
}

// handleControl 取值校验:未知档位 400(此前放任写进沙箱/审批服务)。
func TestControlRejectsUnknownModes(t *testing.T) {
	_, hs := guardServer(t)
	post := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/control", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(`{"sandbox":"yolo"}`); code != http.StatusBadRequest {
		t.Fatalf("未知沙箱档位应 400,得 %d", code)
	}
	if code := post(`{"approval":"whatever"}`); code != http.StatusBadRequest {
		t.Fatalf("未知审批档位应 400,得 %d", code)
	}
}

// token 模式的浏览器引导链:入口页不再自设 cookie(旧行为=任何导航都能拿到 cookie),
// 改为「引导页 → POST /api/auth(fragment 里的 token)→ cookie → 抹 fragment 重载」。
func TestTokenBootstrapCookie(t *testing.T) {
	s, _ := newTestServer()
	s.cfg.AuthToken = "boot-tk"
	s.cfg.Addr = "127.0.0.1:2233"
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()

	// 无 cookie 时 /api/* 401
	resp, err := http.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无凭据 /api/state 应 401,得 %d", resp.StatusCode)
	}

	// 访问入口页 → 引导页(不下发 cookie;旧行为正是本项要关的洞)
	jar := &cookieJar{}
	client := &http.Client{Jar: jar}
	resp2, err := client.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(body), authPath) {
		t.Fatalf("入口页应是引导页(含 %s),得 %q", authPath, string(body))
	}
	if _, err := jar.cookie(hs.URL); err == nil {
		t.Fatal("入口页不得自设会话 cookie(凭据只能经 POST /api/auth 换取)")
	}

	// 引导页换取 cookie(等价浏览器行为:POST /api/auth + JSON body)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+authPath, strings.NewReader(`{"token":"boot-tk"}`))
	req.Header.Set("Content-Type", "application/json")
	resp3, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("正确凭据应 204,得 %d", resp3.StatusCode)
	}
	c, err := jar.cookie(hs.URL)
	if err != nil {
		t.Fatalf("POST %s 应下发会话 cookie: %v", authPath, err)
	}
	if c.Value != "boot-tk" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie 属性不符: %+v", c)
	}

	// 带 cookie 访问 /api/* → 200
	resp4, err := client.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("引导后 /api/state 应 200,得 %d", resp4.StatusCode)
	}
}

// cookieJar 最小实现(仅记录 Set-Cookie,便于断言属性)。
type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.cookies = append(j.cookies, cs...) }
func (j *cookieJar) Cookies(_ *url.URL) []*http.Cookie        { return j.cookies }

func (j *cookieJar) cookie(raw string) (*http.Cookie, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	for _, c := range j.Cookies(u) {
		if c.Name == "gah_token" {
			return c, nil
		}
	}
	return nil, errNoCookie
}

var errNoCookie = &cookieErr{}

type cookieErr struct{}

func (e *cookieErr) Error() string { return "未找到 gah_token cookie" }
