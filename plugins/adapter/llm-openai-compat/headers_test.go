// provider 自定义请求头 + UA 的透传(2026-10-06 随 OpenCode Go 接入新增)。
//
// 钉它的理由:这类头是**网关侧规则**——不发就限速或拒绝,发了就正常,本地看不出来,
// 真出问题往往表现为「莫名 429 / 连不上」,极难定位。所以必须在单测里钉死
// 「头真的上了线(两跳都发)」和「UA 自报家门」。
package llmopenai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// newTestAdapter 造一个对着测试服务器的适配器(测试内不碰真实网络)。
//
// client 必须来自 srv.Client() 而不是留 nil —— Complete 走 a.client.Do,nil 会 panic
// (与 compat_test.go 里 startServer 的构造保持一致)。
func newTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "test-model"}
	if err := a.Configure(srv.URL, "sk-test"); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestHeadersAndUAAreSentOnBothHops(t *testing.T) {
	var modelsUA, modelsSession string
	var chatUA, chatSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelsUA, modelsSession = r.Header.Get("User-Agent"), r.Header.Get("x-opencode-session")
			w.Write([]byte(`{"object":"list","data":[{"id":"m1"}]}`))
		case "/chat/completions":
			chatUA, chatSession = r.Header.Get("User-Agent"), r.Header.Get("x-opencode-session")
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("GAH_VERSION", "0.5.5")
	a := newTestAdapter(t, srv)
	a.ConfigureHeaders(sdk.ProviderHeaders{"x-opencode-session": "sess-42"})

	if _, err := a.ListModels(); err != nil {
		t.Fatal(err)
	}
	// 聊天那一跳:发一个最小的流式请求
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{
		Model: "test-model", Messages: []sdk.LLMMessage{{Role: sdk.RoleUser, Content: "hi"}},
	}, func(sdk.LLMStreamEvent) error { return nil }); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// 两跳都要带:只发一半会出现「能列模型但发不出去」这种难查的不一致
	if modelsSession != "sess-42" || chatSession != "sess-42" {
		t.Fatalf("自定义头没两跳都发: models=%q chat=%q", modelsSession, chatSession)
	}
	if modelsUA != "gah/0.5.5" || chatUA != "gah/0.5.5" {
		t.Fatalf("UA 应自报家门(gah/<版本>,不许伪装成通用 HTTP 库): models=%q chat=%q", modelsUA, chatUA)
	}
}

// TestConfigureHeadersEmptyClears 清空后不再带旧头(切 provider 的常见路径)。
func TestConfigureHeadersEmptyClears(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-old")
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv)
	a.ConfigureHeaders(sdk.ProviderHeaders{"x-old": "1"})
	if _, err := a.ListModels(); err != nil {
		t.Fatal(err)
	}
	if seen != "1" {
		t.Fatalf("头应发出: %q", seen)
	}

	// 清空后的校验**看内部状态而不是再发一次**:ListModels 有 TTL 缓存
	// (默认 10 分钟),第二次调用根本不会发请求,拿到的还是上一次的观测值 —— 那种测试会
	// 在缓存策略改动时静默变成「什么都没验」。
	a.ConfigureHeaders(nil)
	if len(a.headers) != 0 {
		t.Fatalf("清空后不应再带旧头: %+v", a.headers)
	}
}

// TestConfigureClearsHeaders 换端点时旧头必须丢 —— 端点都变了,旧头的语义未必还成立
// (例:旧的会话头发给不相干的网关,轻则被拒重则被按错误身份路由)。
func TestConfigureClearsHeaders(t *testing.T) {
	a := &Adapter{}
	if err := a.Configure("https://a.test/v1", "k"); err != nil {
		t.Fatal(err)
	}
	a.ConfigureHeaders(sdk.ProviderHeaders{"x-a": "1"})
	if err := a.Configure("https://b.test/v1", "k"); err != nil {
		t.Fatal(err)
	}
	if len(a.headers) != 0 {
		t.Fatalf("换端点后旧头应清空: %+v", a.headers)
	}
}

// TestUserAgentFallback 无 GAH_VERSION(单测/嵌入)时也要自报家门,而不是裸 http 默认 UA。
func TestUserAgentFallback(t *testing.T) {
	os.Unsetenv("GAH_VERSION")
	if got := gahUserAgent(); got != "gah" {
		t.Fatalf("未注入版本时应为 %q,得 %q", "gah", got)
	}
	t.Setenv("GAH_VERSION", "v0.6.0")
	if got := gahUserAgent(); got != "gah/0.6.0" {
		t.Fatalf("应剥掉 v 前缀: %q", got)
	}
}

var _ sdk.HeaderConfigurable = (*Adapter)(nil)
