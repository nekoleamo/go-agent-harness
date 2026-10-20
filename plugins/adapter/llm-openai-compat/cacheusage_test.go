package llmopenai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// streamWithUsage 起一个只发 usage 块的 SSE 端点,回放给定的 usage JSON 片段。
func streamWithUsage(t *testing.T, usageJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\n", usageJSON)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCachedTokensBothShapes 缓存命中字段的**两种形状**都要认。
//
// 为什么要两形状(2026-10-20 A3):OpenAI 形状是 usage.prompt_tokens_details.cached_tokens,
// DeepSeek 原生形状是 usage.prompt_cache_hit_tokens。经某些网关转发时 details 可能整个缺失
// ⇒ 只认一种形状会让命中率**恒显示 0%**,用户据此判断「缓存没生效」而其实命中了。
//
// **反向验证**:去掉任一分支(cachedTokensOf 里),本用例必须红。
func TestCachedTokensBothShapes(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		want  int
	}{
		{"openai 形状", `{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":40}}`, 40},
		{"deepseek 原生形状", `{"prompt_tokens":100,"completion_tokens":10,"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":36}`, 64},
		{"两形状都给(取大者)", `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40},"prompt_cache_hit_tokens":64}`, 64},
		{"都没有(0)", `{"prompt_tokens":100,"completion_tokens":10}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := streamWithUsage(t, tc.usage)
			a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "m"}
			resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "m"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Usage.CachedTokens != tc.want {
				t.Fatalf("缓存命中解析错: 期望 %d,实际 %d(%+v)", tc.want, resp.Usage.CachedTokens, resp.Usage)
			}
		})
	}
}

// TestCachedTokensOfUnit 纯函数口径:取大者;details 为 nil 不 panic。
func TestCachedTokensOfUnit(t *testing.T) {
	if got := cachedTokensOf(nil, 12); got != 12 {
		t.Fatalf("details 为 nil 时应取 deepseek 值: %d", got)
	}
	if got := cachedTokensOf(&wirePromptCache{CachedTokens: 30}, 12); got != 30 {
		t.Fatalf("应取大者: %d", got)
	}
	if got := cachedTokensOf(&wirePromptCache{CachedTokens: 0}, 0); got != 0 {
		t.Fatalf("全 0 应得 0: %d", got)
	}
}
