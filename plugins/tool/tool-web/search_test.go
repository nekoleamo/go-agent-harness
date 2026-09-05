// web_search 单测(T1):httptest 本地模拟 Exa API——成功/缺字段/401/429/5xx/截断/env 隔离。
package toolweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// searchTool 装配:provider 指向本地 fake server(同包可注入 endpoint)。apiKey "" = 未配置 EXA_API_KEY。
// searchArgs Exa 请求形态(测试断言)。
type searchArgs struct {
	Query      string `json:"query"`
	NumResults int    `json:"numResults"`
}

func searchTool(t *testing.T, srv *httptest.Server, apiKey string) *SearchTool {
	t.Helper()
	p := NewExaProvider(nil)
	p.endpoint = srv.URL
	p.apiKey = apiKey
	return &SearchTool{provider: p}
}

func TestWebSearchSuccess(t *testing.T) {
	longText := strings.Join([]string{"Go 文档正文", strings.Repeat("很长", 300), "正文"}, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req searchArgs
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析请求: %v", err)
		}
		if req.Query != "golang" {
			t.Errorf("query 不符: %q", req.Query)
		}
		if req.NumResults != 2 {
			t.Errorf("numResults 不符: %d", req.NumResults)
		}
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"results":[
			{"title":"Go 官网","url":"https://go.dev","publishedDate":"2024-01-01","highlight":"Go 是开源编程语言"},
			{"title":"Go 文档","url":"https://go.dev/doc","text":%q}
		]}`, longText)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "test-key")
	resRaw, err := tool.Execute(context.Background(), `{"query":"golang","num_results":2}`)
	if err != nil {
		t.Fatal(err)
	}
	out := resRaw.(map[string]any)
	if out["query"] != "golang" {
		t.Fatalf("query 不符: %v", out)
	}
	results := out["results"].([]SearchResult)
	if len(results) != 2 {
		t.Fatalf("结果数不符: %d", len(results))
	}
	if results[0].URL != "https://go.dev" || results[0].Snippet != "Go 是开源编程语言" {
		t.Fatalf("首条不符: %+v", results[0])
	}
	// 摘要截断 ~300 字(第 2 条长 text 走截断逻辑)
	if n := len([]rune(results[1].Snippet)); n > 301 {
		t.Fatalf("snippet 未截断: %d", n)
	}
	if !strings.HasSuffix(results[1].Snippet, "…") {
		t.Fatalf("超限应带省略号: %q", results[1].Snippet)
	}
}

// TestWebSearchMissingFields 结果缺 title/url → 空字符串容错,不 panic。
func TestWebSearchMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"highlight":"只有摘要"}]}`))
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "")
	resRaw, err := tool.Execute(context.Background(), `{"query":"测试"}`)
	if err != nil {
		t.Fatal(err)
	}
	results := resRaw.(map[string]any)["results"].([]SearchResult)
	if len(results) != 1 || results[0].Title != "" || results[0].URL != "" {
		t.Fatalf("缺字段容错不符: %+v", results)
	}
}

// TestWebSearchUnauthorized 401 → 结构化错误(提示换 key)。
func TestWebSearchUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "bad-key")
	resRaw, err := tool.Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	msg := resRaw.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "未授权") || !strings.Contains(msg, "EXA_API_KEY") {
		t.Fatalf("401 文案不符: %s", msg)
	}
}

// TestWebSearchRateLimited 429 → 结构化错误(提示稍后重试)。
func TestWebSearchRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "test-key")
	resRaw, err := tool.Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	msg := resRaw.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "限流") {
		t.Fatalf("429 文案不符: %s", msg)
	}
}

// TestWebSearchServerError 5xx → 结构化错误(可稍后重试)。
func TestWebSearchServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "test-key")
	resRaw, err := tool.Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	msg := resRaw.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "不可用") {
		t.Fatalf("5xx 文案不符: %s", msg)
	}
}

// TestWebSearchTruncated num_results 超上限 → 钳到 10 并标记 truncated。
func TestWebSearchTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ NumResults int }
		json.NewDecoder(r.Body).Decode(&req)
		if req.NumResults != maxResults {
			t.Errorf("应钳到 %d: %d", maxResults, req.NumResults)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "test-key")
	resRaw, err := tool.Execute(context.Background(), `{"query":"x","num_results":20}`)
	if err != nil {
		t.Fatal(err)
	}
	out := resRaw.(map[string]any)
	if out["truncated"] != true {
		t.Fatalf("应标记 truncated: %v", out)
	}
}

// TestWebSearchDefaultNum 未传 num_results → 默认 5。
func TestWebSearchDefaultNum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ NumResults int }
		json.NewDecoder(r.Body).Decode(&req)
		if req.NumResults != 5 {
			t.Errorf("默认应为 5: %d", req.NumResults)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "test-key")
	if _, err := tool.Execute(context.Background(), `{"query":"x"}`); err != nil {
		t.Fatal(err)
	}
}

// TestWebSearchEnvIsolation EXA_API_KEY 为空(env 隔离)→ 不带 Authorization,功能仍走通。
func TestWebSearchEnvIsolation(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"t","url":"u","highlight":"h"}]}`))
	}))
	defer srv.Close()

	tool := searchTool(t, srv, "") // apiKey 空 = 未配置 EXA_API_KEY 场景
	resRaw, err := tool.Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth {
		t.Fatal("无 key 时不应带 Authorization")
	}
	if _, ok := resRaw.(map[string]any)["results"]; !ok {
		t.Fatalf("缺 key 也应返回结果: %v", resRaw)
	}
}

// TestWebSearchMissingQuery 缺 query → 结构化错误。
func TestWebSearchMissingQuery(t *testing.T) {
	tool := &SearchTool{provider: NewExaProvider(nil)}
	resRaw, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resRaw.(map[string]any)["error"].(string), "缺少 query") {
		t.Fatalf("缺 query 文案不符: %v", resRaw)
	}
}

// TestWebSearchNoProvider provider 未配置 → 结构化错误。
func TestWebSearchNoProvider(t *testing.T) {
	tool := &SearchTool{}
	resRaw, err := tool.Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resRaw.(map[string]any)["error"].(string), "未配置") {
		t.Fatalf("无 provider 文案不符: %v", resRaw)
	}
}

// TestWebSearchRegistered 装配后 web_search 在工具列表(内置 Plugin 缺省 exa 注册)。
func TestWebSearchRegistered(t *testing.T) {
	c := buildEnv(t)
	var tools interface {
		List() []sdk.ToolDefinition
	}
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	for _, d := range tools.List() {
		if d.Name == "web_search" {
			return
		}
	}
	t.Fatal("web_search 未注册")
}
