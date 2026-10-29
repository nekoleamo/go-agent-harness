// 桌面壳控制面代理(shellbridge.go)的安全性单测。
//
// 这一层是「页面 → 壳」的唯一通道,它的三道判据错了不会有显式报错:
//
//	① 只在 env 齐备时挂载(缺一个就该完全不挂,而不是挂一个半残的代理);
//	② 目标必须是**回环**(否则就是页面可指向任意主机的 SSRF);
//	③ 转发时补 X-Shell-Token、抹掉 Cookie/Origin/Referer(否则把 gah 的凭据递给壳)。
//
// 反向验证:把 ② 的回环判断改成恒真,TestShellBridgeRejectsNonLoopbackTarget 必红;
// 把 ③ 的 Header.Del 删掉,TestShellBridgeInjectsTokenAndStripsCredentials 必红。
package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestShellBridgeNotMountedWithoutEnv env 缺任何一个都不挂载(显式判 nil,不做半残代理)。
func TestShellBridgeNotMountedWithoutEnv(t *testing.T) {
	cases := []struct{ addr, token string }{
		{"", ""},
		{"127.0.0.1:1234", ""},
		{"", "tok"},
	}
	for _, c := range cases {
		t.Setenv("GAH_SHELL_CTL_ADDR", c.addr)
		t.Setenv("GAH_SHELL_CTL_TOKEN", c.token)
		if h := shellBridgeHandler(); h != nil {
			t.Fatalf("addr=%q token=%q 不该挂载(得到 %T)", c.addr, c.token, h)
		}
	}
}

// TestShellBridgeRejectsNonLoopbackTarget 非回环目标一律不挂载(SSRF 防线)。
func TestShellBridgeRejectsNonLoopbackTarget(t *testing.T) {
	for _, addr := range []string{"10.1.2.3:9000", "example.com:443", "0.0.0.0:2234"} {
		t.Setenv("GAH_SHELL_CTL_ADDR", addr)
		t.Setenv("GAH_SHELL_CTL_TOKEN", "tok")
		if h := shellBridgeHandler(); h != nil {
			t.Fatalf("目标 %q 不是回环,不该挂载", addr)
		}
	}
	// 回环几种写法都要认(localhost / IPv4 / [::1]),否则整台机器的桌面功能会静默消失。
	for _, addr := range []string{"127.0.0.1:9000", "localhost:9000", "[::1]:9000"} {
		t.Setenv("GAH_SHELL_CTL_ADDR", addr)
		t.Setenv("GAH_SHELL_CTL_TOKEN", "tok")
		if h := shellBridgeHandler(); h == nil {
			t.Fatalf("回环目标 %q 该挂载", addr)
		}
	}
}

// TestShellBridgeInjectsTokenAndStripsCredentials 转发语义:补令牌、抹凭据、路径原样。
func TestShellBridgeInjectsTokenAndStripsCredentials(t *testing.T) {
	var got struct {
		path, token, cookie, origin, referer, host string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.token = r.Header.Get("X-Shell-Token")
		got.cookie = r.Header.Get("Cookie")
		got.origin = r.Header.Get("Origin")
		got.referer = r.Header.Get("Referer")
		got.host = r.Host
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	t.Setenv("GAH_SHELL_CTL_ADDR", strings.TrimPrefix(upstream.URL, "http://"))
	t.Setenv("GAH_SHELL_CTL_TOKEN", "ctl-token")
	h := shellBridgeHandler()
	if h == nil {
		t.Fatal("应挂载")
	}

	req := httptest.NewRequest(http.MethodPost, "/shell/pick-folder", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Cookie", "gah_token=secret")
	req.Header.Set("Origin", "http://127.0.0.1:2233")
	req.Header.Set("Referer", "http://127.0.0.1:2233/")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d,应 200(错误体:%s)", rec.Code, rec.Body.String())
	}
	if got.path != "/shell/pick-folder" {
		t.Fatalf("转发路径 = %q", got.path)
	}
	if got.token != "ctl-token" {
		t.Fatalf("上游收到的控制面令牌 = %q(页面不该能改它,也不该缺)", got.token)
	}
	if got.cookie != "" || got.origin != "" || got.referer != "" {
		t.Fatalf("凭据/来源头不该被带进壳:cookie=%q origin=%q referer=%q", got.cookie, got.origin, got.referer)
	}
	if got.host != strings.TrimPrefix(upstream.URL, "http://") {
		t.Fatalf("上游 Host = %q", got.host)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("应答体没透传: %q", rec.Body.String())
	}
}

// TestShellBridgeUpstreamDownIs502 壳没起/已退出:给一句能查的人话 + 502,不是空连接。
func TestShellBridgeUpstreamDownIs502(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	t.Setenv("GAH_SHELL_CTL_ADDR", addr)
	t.Setenv("GAH_SHELL_CTL_TOKEN", "tok")
	h := shellBridgeHandler()
	if h == nil {
		t.Fatal("应挂载")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shell/ping", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d,应 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "控制面不可达") {
		t.Fatalf("错误文案该说明原因: %q", rec.Body.String())
	}
}

// TestIsLoopbackHost 回环判定的边界(与控制面側 loopbackHost 同口径,但这里只认 host 部分)。
func TestIsLoopbackHost(t *testing.T) {
	yes := []string{"localhost", "127.0.0.1", "::1", "[::1]", "127.1.2.3"}
	no := []string{"", "example.com", "10.0.0.1", "0.0.0.0", "gah.local"}
	for _, h := range yes {
		if !isLoopbackHost(h) {
			t.Fatalf("%q 应判回环", h)
		}
	}
	for _, h := range no {
		if isLoopbackHost(h) {
			t.Fatalf("%q 不该判回环", h)
		}
	}
}

// TestShellExportBodyLimit 桌面壳导出落盘的请求体上限单独放宽。
//
// 为什么:导出内容整份在 body 里(Sidebar.vue 先 fetch 导出文本,再 saveExport 交给壳写盘),
// 长会话的 HTML/JSONL 轻松超过普通 API 的 1 MiB 上限 —— 一旦套上,「导出会话」这个功能
// 在**最需要它的时候**(会话很长)失败,而且表现为一句看不出原因的 413。
// 反向验证:把 guard.go 里 /shell/export-save 的分支删掉,本用例第一条必红。
func TestShellExportBodyLimit(t *testing.T) {
	s, _ := newTestServer()
	s.cfg.Addr = "127.0.0.1:2233"

	gotLen := int64(-1)
	h := s.guardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		gotLen = int64(len(b))
	}))

	body := bytes.Repeat([]byte("x"), 3<<20) // 3 MiB:明显超过 1 MiB,又远小于 64 MiB
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2233/shell/export-save", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotLen != int64(len(body)) {
		t.Fatalf("导出体被截断:code=%d 收到=%d 期望=%d", rec.Code, gotLen, len(body))
	}

	// 对照:普通 API 仍然是 1 MiB 上限(不能为了导出把全站上限一起抬高)。
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2233/api/input", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("普通 API 请求体上限应仍生效,得到 code=%d", rec.Code)
	}
}
