// token 模式「全表面鉴权」单测(R10 未闭环 ③ 收口):
//   - 缺凭据时,静态资源 / 附件 / UI 插件产物一律拿不到内容(只得到引导页);
//   - POST /api/auth 是唯一豁免路径:fragment 里的 token 换 HttpOnly+SameSite=Strict cookie;
//   - 未配置 token 时行为与收口前完全一致(回归保护);?token= 仍不生效。
package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// authServer token 模式的完整服务栈(护栏 + 鉴权 + 路由)+ 三份带哨兵内容的静态目录。
func authServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("<html>INDEX_MARKER</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(static, "app.js"), []byte("APP_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachments := t.TempDir()
	if err := os.WriteFile(filepath.Join(attachments, "pic.txt"), []byte("ATTACH_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	uiplugins := t.TempDir()
	plugDir := filepath.Join(uiplugins, "demo")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.js"), []byte("PLUGIN_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	s := New(Config{
		Addr:           "127.0.0.1:2233",
		AuthToken:      token,
		StaticDir:      static,
		AttachmentsDir: attachments,
		UIPluginsDir:   uiplugins,
	}, hub, NewConfirm(hub), slog.Default())
	// 与 newTestServer 同款最小桩:/api/* 端点(如 /api/state)在无 token 模式下要能正常应答
	s.loop = &stubLoop{}
	s.sessions = &memLog{}
	s.llm = &stubLLM{}
	s.sb = &stubSB{mode: sdk.SandboxWorkspace}
	s.us = &stubStats{v: sdk.UsageStats{PromptTokens: 10, Requests: 1, Window: 65536}}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs
}

func getBody(t *testing.T, url string, cookie *http.Cookie) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// 缺凭据:静态资源/附件/UI 插件产物一律不泄漏内容,只给引导页。
func TestTokenModeHidesStaticSurface(t *testing.T) {
	_, hs := authServer(t, "tk")
	for _, tc := range []struct{ path, marker string }{
		{"/", "INDEX_MARKER"},
		{"/index.html", "INDEX_MARKER"},
		{"/app.js", "APP_MARKER"},
		{"/attachments/pic.txt", "ATTACH_MARKER"},
		{"/ui-plugins/demo/plugin.js", "PLUGIN_MARKER"},
	} {
		code, body, hdr := getBody(t, hs.URL+tc.path, nil)
		if code != http.StatusOK {
			t.Fatalf("缺凭据 %s 应返回引导页(200),得 %d", tc.path, code)
		}
		if strings.Contains(body, tc.marker) {
			t.Fatalf("缺凭据 %s 泄漏了内容(%s):token 模式必须全表面鉴权", tc.path, tc.marker)
		}
		if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("缺凭据 %s 应为引导页 text/html,得 %q", tc.path, ct)
		}
		if !strings.Contains(body, authPath) || !strings.Contains(body, "location.hash") {
			t.Fatalf("缺凭据 %s 的引导页应自取 fragment 并 POST %s", tc.path, authPath)
		}
		// 换取 cookie 后必须回到原请求(path+query;如桌面壳 ?shell=desktop),否则深链/壳布局丢失
		if !strings.Contains(body, "location.pathname + window.location.search") {
			t.Fatalf("引导页应保留原 path+query 回跳:%s", body)
		}
		if strings.Contains(body, `replace("/")`) {
			t.Fatal("引导页不得硬编码回到 /(会丢 query)")
		}
		if cc := hdr.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Fatalf("引导页应 no-store,得 %q", cc)
		}
		if rp := hdr.Get("Referrer-Policy"); rp != "no-referrer" {
			t.Fatalf("引导页 Referrer-Policy 应 no-referrer,得 %q", rp)
		}
		if xf := hdr.Get("X-Frame-Options"); xf != "DENY" {
			t.Fatalf("引导页 X-Frame-Options 应 DENY,得 %q", xf)
		}
		// 引导页自身不得引用 embed/web 应用资源(否则等于把 UI 摊给未鉴权请求)
		if strings.Contains(body, "assets/") || strings.Contains(body, "<script src") {
			t.Fatalf("引导页不得引用外部资源:%s", body)
		}
	}
}

// 带凭据:同一批路径正常返回真实内容(收口不能把正常访问也挡掉)。
func TestTokenModeServesSurfaceWithCookie(t *testing.T) {
	_, hs := authServer(t, "tk")
	cookie := &http.Cookie{Name: "gah_token", Value: "tk"}
	for _, tc := range []struct{ path, marker string }{
		{"/", "INDEX_MARKER"},
		{"/app.js", "APP_MARKER"},
		{"/attachments/pic.txt", "ATTACH_MARKER"},
		{"/ui-plugins/demo/plugin.js", "PLUGIN_MARKER"},
	} {
		code, body, _ := getBody(t, hs.URL+tc.path, cookie)
		if code != http.StatusOK || !strings.Contains(body, tc.marker) {
			t.Fatalf("带凭据 %s 应返回真实内容,得 %d %q", tc.path, code, body)
		}
	}
	// 凭据错误(值不匹配)同样只给引导页
	code, body, _ := getBody(t, hs.URL+"/app.js", &http.Cookie{Name: "gah_token", Value: "wrong"})
	if code != http.StatusOK || strings.Contains(body, "APP_MARKER") {
		t.Fatalf("错误凭据不得拿到资源,得 %d %q", code, body)
	}
}

// POST /api/auth:唯一豁免路径;正确 token → 204 + HttpOnly/SameSite=Strict cookie。
func TestAuthEndpoint(t *testing.T) {
	_, hs := authServer(t, "tk")
	post := func(headers map[string]string, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, hs.URL+authPath, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	resp := post(map[string]string{"Content-Type": "application/json"}, `{"token":"tk"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("正确凭据应 204,得 %d", resp.StatusCode)
	}
	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "gah_token" {
			found = c
		}
	}
	if found == nil || found.Value != "tk" || !found.HttpOnly || found.SameSite != http.SameSiteStrictMode || found.Path != "/" {
		t.Fatalf("cookie 属性不符: %+v", found)
	}

	if code := post(map[string]string{"Content-Type": "application/json"}, `{"token":"nope"}`).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("错误凭据应 401,得 %d", code)
	}
	if code := post(map[string]string{"Content-Type": "application/json"}, `{"token":""}`).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("空凭据应 401,得 %d", code)
	}
	// 仅 Bearer 头(无 body):引导端点豁免 Content-Type 约束,body 可缺
	if code := post(map[string]string{"Authorization": "Bearer tk"}, "").StatusCode; code != http.StatusNoContent {
		t.Fatalf("Bearer 头引导应 204,得 %d", code)
	}
	// GET → 405(ServeMux 方法不匹配)
	if code, _, _ := getBody(t, hs.URL+authPath, nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s 应 405,得 %d", authPath, code)
	}
	// 跨站 POST 被护栏拦下(即使 Content-Type 是 JSON)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+authPath, strings.NewReader(`{"token":"tk"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	evil, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	evil.Body.Close()
	if evil.StatusCode != http.StatusForbidden {
		t.Fatalf("跨站引导应 403,得 %d", evil.StatusCode)
	}
}

// 未配置 token:行为与收口前一致(静态可访问、/api 可访问、?token= 无意义)。
func TestNoTokenModeUnchanged(t *testing.T) {
	_, hs := authServer(t, "")
	if code, body, _ := getBody(t, hs.URL+"/app.js", nil); code != 200 || !strings.Contains(body, "APP_MARKER") {
		t.Fatalf("无 token 配置时静态应直出,得 %d %q", code, body)
	}
	if code, body, _ := getBody(t, hs.URL+"/", nil); code != 200 || !strings.Contains(body, "INDEX_MARKER") {
		t.Fatalf("无 token 配置时入口页应直出,得 %d %q", code, body)
	}
	if code, _, _ := getBody(t, hs.URL+"/api/state", nil); code != 200 {
		t.Fatalf("无 token 配置时 /api/state 应 200,得 %d", code)
	}
	// 未启用鉴权 → 引导通道显式 404(不需要引导)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+authPath, strings.NewReader(`{"token":"tk"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未启用鉴权时 %s 应 404,得 %d", authPath, resp.StatusCode)
	}
}

// token 模式下 ?token= 依旧不生效(会进浏览器历史/日志);非 GET 的非 API 路径一律 401。
func TestTokenModeQueryTokenRejected(t *testing.T) {
	_, hs := authServer(t, "tk")
	code, body, _ := getBody(t, hs.URL+"/app.js?token=tk", nil)
	if strings.Contains(body, "APP_MARKER") {
		t.Fatal("?token= 不得作为凭据通道")
	}
	if code != http.StatusOK || !strings.Contains(body, authPath) {
		t.Fatalf("?token= 应只得到引导页,得 %d %q", code, body)
	}
	if code, _, _ := getBody(t, hs.URL+"/api/state?token=tk", nil); code != http.StatusUnauthorized {
		t.Fatalf("/api/* 带 ?token= 应 401,得 %d", code)
	}
	// 非 GET/HEAD 的非 API 路径:不给引导页,401(引导页只服务浏览器导航)
	req, _ := http.NewRequest(http.MethodPut, hs.URL+"/app.js", strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("非 GET 非 API 路径应 401,得 %d", resp.StatusCode)
	}
}

// FragmentURL:token 模式把凭据放 fragment(不会发往服务端);无 token 原样。
func TestFragmentURL(t *testing.T) {
	cases := []struct{ url, token, want string }{
		{"http://127.0.0.1:2233", "tk", "http://127.0.0.1:2233/#token=tk"},
		{"http://127.0.0.1:2233/", "tk", "http://127.0.0.1:2233/#token=tk"},
		{"http://127.0.0.1:2233/?shell=desktop", "a b", "http://127.0.0.1:2233/?shell=desktop#token=a%20b"},
		// `&`/`#` 由浏览器原样保留在 fragment 中:`&` 不做转义(见下 "引导页解析"),`#` 转义
		{"http://127.0.0.1:2233", "a&b", "http://127.0.0.1:2233/#token=a&b"},
		{"http://127.0.0.1:2233", "a#b", "http://127.0.0.1:2233/#token=a%23b"},
		{"http://127.0.0.1:2233", "", "http://127.0.0.1:2233"},
		{"", "tk", ""},
	}
	for _, tc := range cases {
		if got := FragmentURL(tc.url, tc.token); got != tc.want {
			t.Errorf("FragmentURL(%q,%q)=%q, want %q", tc.url, tc.token, got, tc.want)
		}
	}
}

// 引导页解析:token 必须取到 fragment 末尾 —— Go 侧不转义 `&`,
// 若用 `[^&]*` 捕获则含 `&` 的 token 会被截断(回归保护:见 bootstrap.go 内注释)。
func TestBootstrapTokenCaptureToEnd(t *testing.T) {
	if !strings.Contains(bootstrapHTML, `([\s\S]*)$`) {
		t.Fatal("引导页未使用「捕获到 fragment 末尾」的 token 正则:含 & 的 token 会被截断")
	}
	if strings.Contains(bootstrapHTML, `([^&]*)`) {
		t.Fatal("引导页仍存在 [^&]* 形式的 token 捕获")
	}
	// 端到端对账:FragmentURL 产物(未转义 &)→ 页面正则捕获结果 = 原 token
	tok := "a&b"
	frag := FragmentURL("http://127.0.0.1:2233", tok)
	i := strings.Index(frag, "#")
	if i < 0 {
		t.Fatal("FragmentURL 未产出 fragment")
	}
	hash := frag[i:]
	m := regexp.MustCompile(`(?:^#token=|[#&]token=)([\s\S]*)$`).FindStringSubmatch(hash)
	if m == nil || m[1] != tok {
		t.Fatalf("引导页正则从 %q 解出的 token = %v, want %q", hash, m, tok)
	}
}
