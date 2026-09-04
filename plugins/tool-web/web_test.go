package toolweb

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv 装配 host-tools + tool-web。
func buildEnv(t *testing.T) sdk.Ctx {
	t.Helper()
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
