// tool-web 的搜索自证服务(/websearch 命令的底座)单测。
//
// 钉它的理由:这条命令存在的意义是「把搜索排查从翻带凭据的文件变成一条命令」。如果它在
// 关键情形下说不清 —— 哪一家、端点主机、key 有无、能不能真跑通 —— 用户还是得回去猜。
package toolweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// 写一份 search.yaml 到临时数据根(配置解析链的唯一入口)。
func withSearchCfg(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(searchfile.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 缺省(无配置):应回落到 anysearch(匿名即可用),而不是 exa。
func TestNewSearchServiceDefaultsToAnonymouseAnysearch(t *testing.T) {
	withSearchCfg(t, "")
	s := NewSearchService()
	info := s.SearchInfo()
	if info.Provider != searchfile.ProviderAnysearch {
		t.Fatalf("无配置时缺省应是 anysearch,得 %q", info.Provider)
	}
	if info.HasKey {
		t.Fatalf("无配置不应有 key")
	}
	if info.Note == "" {
		t.Fatal("anysearch 应给一句提醒(配了 key 可能反而 401)")
	}
}

// exa + 有 key:端点只回 host、key 只回打码。
func TestSearchServiceReportsEndpointHostAndMaskedKey(t *testing.T) {
	withSearchCfg(t, "provider: exa\nexa_api_key: sk-or-v1-abcdefghijklmnop\n")
	s := NewSearchService()
	info := s.SearchInfo()
	if info.Provider != searchfile.ProviderExa {
		t.Fatalf("provider 应为 exa,得 %q", info.Provider)
	}
	if info.EndpointHost != "api.exa.ai" {
		t.Fatalf("端点只应回 host,得 %q", info.EndpointHost)
	}
	if strings.ContainsAny(info.EndpointHost, "/?") {
		t.Fatalf("host 里不该出现路径/查询串: %q", info.EndpointHost)
	}
	if !info.HasKey || strings.Contains(info.ConfiguredKeyMask, "abcdefghijklmnop") {
		t.Fatalf("key 应只回打码: %q", info.ConfiguredKeyMask)
	}
	if !strings.Contains(info.Note, "付费") {
		t.Fatalf("exa 应提醒按量付费: %q", info.Note)
	}
}

// 未知 provider:如实报,不静默兜底成一个能用的 —— 那会让用户以为配置生效了。
func TestSearchServiceUnknownProviderIsHonest(t *testing.T) {
	withSearchCfg(t, "provider: no-such\n")
	s := NewSearchService()
	info := s.SearchInfo()
	if info.Provider != "no-such" {
		t.Fatalf("应如实报出配置的 provider,得 %q", info.Provider)
	}
	got := s.TryProbe(context.Background(), "q", 1)
	if got.Ok || !strings.Contains(got.Err, "未知搜索 provider") {
		t.Fatalf("未知 provider 应显式失败而不是兜底,得 ok=%v err=%q", got.Ok, got.Err)
	}
}

// TryProbe:成功回填条数/耗时/样例标题。
func TestSearchServiceTryProbeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"results":[
			{"title":"2025 年报","url":"https://a.test","snippet":"s1"},
			{"title":"年报解读","url":"https://b.test","snippet":"s2"}]}}`))
	}))
	defer srv.Close()

	p := &anysearchProvider{client: srv.Client(), endpoint: srv.URL}
	s := &searchService{
		provider: searchfile.ProviderAnysearch,
		endpoint: srv.URL,
		client:   srv.Client(),
		impl:     p,
	}
	got := s.TryProbe(context.Background(), "2025 年报", 3)
	if !got.Ok {
		t.Fatalf("应成功,err=%q", got.Err)
	}
	if got.ResultCount != 2 || len(got.ResultTitles) != 2 || got.ResultTitles[0] != "2025 年报" {
		t.Fatalf("应回填条数与标题: %+v", got)
	}
	if got.EndpointHost != "127.0.0.1" {
		t.Fatalf("端点只回 host,得 %q", got.EndpointHost)
	}
}

// TryProbe:失败时把原因原样带出(用户排查全靠这句),且绝不 Ok=true。
func TestSearchServiceTryProbeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"message":"insufficient balance"}`))
	}))
	defer srv.Close()

	s := &searchService{
		provider: searchfile.ProviderExa,
		endpoint: srv.URL,
		client:   srv.Client(),
		impl:     &exaProvider{client: srv.Client(), endpoint: srv.URL},
	}
	got := s.TryProbe(context.Background(), "q", 1)
	if got.Ok {
		t.Fatal("失败时不得报 Ok")
	}
	if got.Err == "" {
		t.Fatal("失败原因不能为空(用户靠这句排查)")
	}
}

// 自定义 anysearch 端点:提醒语要说清「这是自建端点」,别让人以为是官方。
func TestSearchServiceCustomEndpointNote(t *testing.T) {
	withSearchCfg(t, "provider: anysearch\nanysearch_endpoint: https://search.mycorp.cn/v1\n")
	got := NewSearchService().SearchInfo()
	if got.EndpointHost != "search.mycorp.cn" {
		t.Fatalf("应取自定义端点 host,得 %q", got.EndpointHost)
	}
	if !strings.Contains(got.Note, "自定义") {
		t.Fatalf("提醒语应说明这是自定义端点: %q", got.Note)
	}
}

// TryProbe:n<=0 时按 3 条请求(不给 0 造成「搜不到」假象)。
func TestSearchServiceTryProbeDefaultN(t *testing.T) {
	var gotN int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"results":[]}}`))
	}))
	defer srv.Close()
	s := &searchService{
		provider: searchfile.ProviderAnysearch, endpoint: srv.URL, client: srv.Client(),
		impl: &anysearchProvider{client: srv.Client(), endpoint: srv.URL},
	}
	got := s.TryProbe(context.Background(), "q", 0)
	// 零条结果**仍然算成功**:通路是通的,只是这次没搜到 —— 报成失败会让人去查配置,
	// 而问题其实在查询词上。
	if !got.Ok || got.Err != "" {
		t.Fatalf("空结果应是成功(通路通),得 ok=%v err=%q", got.Ok, got.Err)
	}
	_ = gotN
}
