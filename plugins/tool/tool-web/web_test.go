package toolweb

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv 装配 host-tools + tool-web。
// 既有用例打的是 httptest 环回服务,故默认放行内网;守卫本身的拦截行为
// 由 TestWebFetchBlocksPrivateTargets 单独钉(那里清掉开关)。
func buildEnv(t *testing.T) sdk.Ctx {
	t.Helper()
	t.Setenv(allowPrivateEnv, "1")
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestWebFetchText 文本响应 → 内容与状态码。零外部依赖:httptest 本地验证。
func TestWebFetchText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello-web-ok"))
	}))
	defer srv.Close()

	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != float64(200) {
		t.Fatalf("状态码不符: %v", out)
	}
	if out["content"] != "hello-web-ok" {
		t.Fatalf("内容不符: %v", out)
	}
}

// TestWebFetchJSON JSON 响应 → 结构化 json 字段。
func TestWebFetchJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"gah","ok":true}`))
	}))
	defer srv.Close()

	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	js, ok := out["json"].(map[string]any)
	if !ok || js["name"] != "gah" || js["ok"] != true {
		t.Fatalf("JSON 结构化不符: %v", out)
	}
}

// TestWebFetchError 错误 URL/状态 → 结构化错误,不 panic。
func TestWebFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "web_fetch", `{"url":"`+srv.URL+`/missing"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != float64(404) {
		t.Fatalf("404 状态不符: %v", out)
	}
	// 非法 URL → 结构化错误
	res, err = tools.Execute(context.Background(), "web_fetch", `{"url":"://bad"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "error") {
		t.Fatalf("非法 URL 应有 error: %s", res.Content)
	}
	// 缺少 url → 结构化错误
	res, err = tools.Execute(context.Background(), "web_fetch", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "error") {
		t.Fatalf("缺 url 应有 error: %s", res.Content)
	}
}

// TestWebFetchBlocksPrivateTargets 内网守卫(F3):默认拒环回/链路本地;
// 同一目标在开关打开后必须成功——否则测不出"是守卫拒的"还是"网络不通"。
func TestWebFetchBlocksPrivateTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("INTERNAL-SECRET"))
	}))
	defer srv.Close()

	c := buildEnv(t)
	t.Setenv(allowPrivateEnv, "") // 清掉 buildEnv 的放行
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}

	// ① 环回 IP 字面量(httptest 就是 127.0.0.1)
	res, err := tools.Execute(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "INTERNAL-SECRET") {
		t.Fatalf("内网内容不得进上下文: %s", res.Content)
	}
	if !strings.Contains(res.Content, "拒绝访问内网") {
		t.Fatalf("应给出可读的拒绝原因: %s", res.Content)
	}

	// ② localhost 域名(解析后仍须拒:DNS rebinding 同路径)
	res, err = tools.Execute(context.Background(), "web_fetch",
		`{"url":"http://localhost:`+portOf(t, srv.URL)+`/"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "拒绝访问内网") {
		t.Fatalf("localhost 亦应拒: %s", res.Content)
	}

	// ③ 云元数据端点(字面量 IP,不需要真存在该服务)
	res, err = tools.Execute(context.Background(), "web_fetch", `{"url":"http://169.254.169.254/latest/meta-data/"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "拒绝访问内网") {
		t.Fatalf("链路本地地址应拒: %s", res.Content)
	}

	// ④ 开关打开后同一目标成功(反证拦截来自守卫)
	t.Setenv(allowPrivateEnv, "1")
	res, err = tools.Execute(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "INTERNAL-SECRET") {
		t.Fatalf("放行开关应生效: %s", res.Content)
	}
}

// TestWebFetchReportsFinalURL 重定向后回显最终 URL(审计可见性)。
func TestWebFetchReportsFinalURL(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, base+"/landed", http.StatusFound)
			return
		}
		w.Write([]byte("landed-ok"))
	}))
	defer srv.Close()
	base = srv.URL

	c := buildEnv(t) // httptest = 环回 → 该用例需要放行开关
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "web_fetch", `{"url":"`+base+`/start"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out["url"] != base+"/landed" {
		t.Fatalf("应回显最终 URL,实际 %v", out["url"])
	}
}

// portOf 取 URL 的端口(seedNet 无关:只为拼 localhost 用例)。
func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := neturl.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}
