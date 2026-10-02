// web_search 的 anysearch provider 与「搜索引擎结果页」清洗单测(2026-10-03)。
package toolweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// anySearchTool 装配 anysearch provider 指向本地 fake server(同包可注入 endpoint)。
func anySearchTool(t *testing.T, srv *httptest.Server, apiKey string) *SearchTool {
	t.Helper()
	p := &anysearchProvider{client: srv.Client(), endpoint: srv.URL, apiKey: apiKey}
	return &SearchTool{provider: p}
}

// TestAnySearchSuccess 正常路径:请求体字段名(snake_case)、code==0 才算成功、结果映射。
func TestAnySearchSuccess(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"results":[
			{"title":"Go 官网","url":"https://go.dev","snippet":"Go 是开源编程语言","content":"c"}
		]}}`))
	}))
	defer srv.Close()

	res, err := anySearchTool(t, srv, "").provider.Search(context.Background(), "golang", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got["query"] != "golang" || got["num_results"].(float64) != 2 {
		t.Fatalf("请求体字段不符: %v", got)
	}
	if len(res) != 1 || res[0].URL != "https://go.dev" || res[0].Snippet != "Go 是开源编程语言" {
		t.Fatalf("结果映射不符: %+v", res)
	}
}

// TestAnySearchBusinessErrorOn200 业务错也回 HTTP 200:不查 code 就会把「额度用完」当成功。
func TestAnySearchBusinessErrorOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":-1,"message":"Query is required.","data":{"results":[]}}`))
	}))
	defer srv.Close()

	_, err := anySearchTool(t, srv, "").provider.Search(context.Background(), "x", 1)
	if err == nil {
		t.Fatal("code!=0 必须报错(哪怕 HTTP 200)")
	}
	se, ok := err.(*SearchError)
	if !ok || se.Kind != "quota" {
		t.Fatalf("业务错应归一为 quota 类: %+v", err)
	}
	if !strings.Contains(se.Msg, "anysearch") {
		t.Fatalf("文案应给出换 provider 的可执行下一步: %s", se.Msg)
	}
}

// TestAnySearchPaymentRequired 402 → 明确指向「换 provider」。
func TestAnySearchPaymentRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	out, err := anySearchTool(t, srv, "").Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	msg := out.(map[string]any)["error"].(string)
	if !strings.Contains(msg, "402") || !strings.Contains(msg, "GAH_SEARCH_PROVIDER") {
		t.Fatalf("402 文案不符: %s", msg)
	}
}

// TestDropSearchEngineResults 剔除搜索引擎结果页(境内不可达),保留正常站点。
func TestDropSearchEngineResults(t *testing.T) {
	in := []SearchResult{
		{Title: "Go", URL: "https://go.dev/blog"},
		{Title: "DDG 搜索", URL: "https://duckduckgo.com/?q=golang+中文"},
		{Title: "lite DDG", URL: "https://lite.duckduckgo.com/lite/?q=x"},
		{Title: "Google", URL: "https://www.google.com/search?q=x"},
		{Title: "搜狗", URL: "https://www.sogou.com/web?query=x"}, // 境内可达:保留
		{Title: "知乎", URL: "https://www.zhihu.com/question/1"},  // 正常站点:保留
		{Title: "像 google 的域名", URL: "https://notgoogle.com/a"}, // 不能被后缀误伤
	}
	kept, dropped := dropSearchEngineResults(in)
	if dropped != 3 {
		t.Fatalf("应剔 3 条,got %d(%+v)", dropped, kept)
	}
	for _, r := range kept {
		if isSearchEngineResult(r) {
			t.Fatalf("引擎结果页未被剔除: %+v", r)
		}
	}
	// notgoogle.com 是**合法站点**,不能被 "google.com" 的后缀匹配误伤。
	var found bool
	for _, r := range kept {
		if r.URL == "https://notgoogle.com/a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notgoogle.com 被误伤: %+v", kept)
	}
	if len(kept) != 4 {
		t.Fatalf("保留条数不符: %d", len(kept))
	}
}

// TestSearchToolReportsDroppedCount 清洗后的条数要回给模型(否则它以为只有 2 条结果)。
func TestSearchToolReportsDroppedCount(t *testing.T) {
	tool := &SearchTool{provider: &stubSearchProvider{rs: []SearchResult{
		{Title: "a", URL: "https://go.dev"},
		{Title: "b", URL: "https://duckduckgo.com/?q=a"},
		{Title: "c", URL: "https://bing.com/search?q=a"},
	}}}
	out, err := tool.Execute(context.Background(), `{"query":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["dropped_search_engine_results"].(int) != 2 {
		t.Fatalf("应回 dropped=2: %+v", m)
	}
	if len(m["results"].([]SearchResult)) != 1 {
		t.Fatalf("清洗后应剩 1 条: %+v", m)
	}
}

// TestSearchToolAllDroppedExplains 全部被剔要给模型一句可执行的话(否则它只会以为「搜不到」)。
func TestSearchToolAllDroppedExplains(t *testing.T) {
	tool := &SearchTool{provider: &stubSearchProvider{rs: []SearchResult{
		{Title: "b", URL: "https://duckduckgo.com/?q=a"},
	}}}
	out, _ := tool.Execute(context.Background(), `{"query":"x"}`)
	m := out.(map[string]any)
	if len(m["results"].([]SearchResult)) != 0 {
		t.Fatalf("应全剔: %+v", m)
	}
	if msg, _ := m["error"].(string); !strings.Contains(msg, "搜索引擎") {
		t.Fatalf("全剔时应给出说明: %+v", m)
	}
}

// stubSearchProvider 固定结果的桩 provider。
type stubSearchProvider struct{ rs []SearchResult }

func (s *stubSearchProvider) Search(context.Context, string, int) ([]SearchResult, error) {
	return s.rs, nil
}

// TestAnySearchAuthAndServerErrors 鉴权/限流/5xx/其他状态码各归一到自己的结构化文案。
func TestAnySearchAuthAndServerErrors(t *testing.T) {
	for _, c := range []struct {
		code int
		want string // 期望的错误文案片段
	}{
		{http.StatusUnauthorized, "拒绝鉴权"},
		{http.StatusForbidden, "拒绝鉴权"},
		{http.StatusTooManyRequests, "限流"},
		{http.StatusInternalServerError, "暂时不可用"},
		{http.StatusBadGateway, "暂时不可用"},
		{http.StatusTeapot, "返回 418"},
	} {
		code := c.code
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		_, err := anySearchTool(t, srv, "k").provider.Search(context.Background(), "x", 1)
		srv.Close()
		if err == nil {
			t.Fatalf("%d 应报错", code)
		}
		se, ok := err.(*SearchError)
		if !ok {
			t.Fatalf("%d 应为 *SearchError: %T", code, err)
		}
		if !strings.Contains(se.Msg, c.want) {
			t.Fatalf("%d 文案不含 %q: %s", code, c.want, se.Msg)
		}
	}
}

// TestAnySearchSnippetFallsBackToContent snippet 空时回落到 content(不同源字段习惯不同)。
func TestAnySearchSnippetFallsBackToContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"results":[{"title":"t","url":"https://go.dev","content":"正文兜底"}]}}`))
	}))
	defer srv.Close()
	res, err := anySearchTool(t, srv, "").provider.Search(context.Background(), "x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Snippet != "正文兜底" {
		t.Fatalf("snippet 回落失败: %+v", res[0])
	}
}

// TestNewAnysearchProviderReadsConfig 构造器从生效配置取端点与 key(缺省走官方端点、匿名)。
func TestNewAnysearchProviderReadsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv(searchfile.EnvProvider, searchfile.ProviderAnysearch)
	t.Setenv(searchfile.EnvEndpoint, "http://127.0.0.1:9/search")
	t.Setenv(searchfile.EnvAPIKey, "")
	t.Setenv(searchfile.EnvAnysearchAPIKey, "anon-key")
	p := NewAnysearchProvider(nil)
	if p.endpoint != "http://127.0.0.1:9/search" {
		t.Fatalf("端点应取生效配置: %q", p.endpoint)
	}
	if p.apiKey != "anon-key" {
		t.Fatalf("key 应取生效配置: %q", p.apiKey)
	}
	// 缺省端点 = 官方
	t.Setenv(searchfile.EnvEndpoint, "")
	q := NewAnysearchProvider(nil)
	if q.endpoint != anysearchDefaultEndpoint {
		t.Fatalf("缺省端点应为官方: %q", q.endpoint)
	}
}

// TestAnysearchProviderBadConfig 配置读失败时构造器不 panic,错误延后到 Search 显式失败。
func TestAnysearchProviderBadConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "search.yaml"), []byte("\tnot: [yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewAnysearchProvider(nil)
	if _, err := p.Search(context.Background(), "x", 1); err == nil {
		t.Fatal("坏配置应在 Search 时显式失败")
	}
}

// TestNumResultsClamped n 越界收敛到 [1, maxResults](上游给多少都不该打爆请求)。
func TestNumResultsClamped(t *testing.T) {
	var got float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got, _ = m["num_results"].(float64)
		_, _ = w.Write([]byte(`{"code":0,"data":{"results":[]}}`))
	}))
	defer srv.Close()
	p := anySearchTool(t, srv, "").provider
	if _, err := p.Search(context.Background(), "x", 0); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("n=0 应收到 1,got %v", got)
	}
	if _, err := p.Search(context.Background(), "x", 9999); err != nil {
		t.Fatal(err)
	}
	if got != float64(maxResults) {
		t.Fatalf("n 过大应收敛到 %d,got %v", maxResults, got)
	}
}

// TestHostOfBadURL 解析不出的 URL 不当成引擎结果(保守:宁可留着,别误删)。
func TestHostOfBadURL(t *testing.T) {
	if hostOf("://坏 url") != "" {
		t.Fatal("坏 URL 应取不到 host")
	}
	if isSearchEngineResult(SearchResult{URL: "://坏 url"}) {
		t.Fatal("坏 URL 不该被判成引擎结果页")
	}
}

// TestAnysearchIgnoresExaEndpointAnysearch 的请求端点只认 anysearch_endpoint ——
// 通用 endpoint 里留着 Exa 地址时,静默 POST 到 Exa 是最难查的一类故障。
func TestAnysearchIgnoresExaEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv(searchfile.EnvProvider, searchfile.ProviderAnysearch)
	t.Setenv(searchfile.EnvExaEndpoint, "http://127.0.0.1:1/exa")
	t.Setenv(searchfile.EnvAnysearchEndpoint, "")
	t.Setenv(searchfile.EnvEndpoint, "")
	p := NewAnysearchProvider(nil)
	if p.endpoint != anysearchDefaultEndpoint {
		t.Fatalf("anysearch 不该用 exa 的端点,got %q", p.endpoint)
	}
}

// TestExaIgnoresAnysearchEndpoint exa 侧同口径(反向)。
func TestExaIgnoresAnysearchEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv(searchfile.EnvProvider, searchfile.ProviderExa)
	t.Setenv(searchfile.EnvAnysearchEndpoint, "http://127.0.0.1:1/any")
	t.Setenv(searchfile.EnvExaEndpoint, "")
	t.Setenv(searchfile.EnvEndpoint, "")
	// 端点缺省由构造器补官方地址(resolveFileEndpoint 只负责"取生效值",不猜缺省)。
	if got := NewExaProvider(nil).endpoint; got != "https://api.exa.ai/search" {
		t.Fatalf("exa 不该用 anysearch 的端点,got %q", got)
	}
}
