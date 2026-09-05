// ListModels 单测:mock /models 解析(id/owned_by)+ TTL 缓存命中 + 切换端点后失效。
package llmopenai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestListModelsParseAndCache(t *testing.T) {
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("应带 Bearer 凭据: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "deepseek-ai/DeepSeek-V3", "owned_by": "deepseek-ai"},
				{"id": "Qwen/Qwen2.5-72B-Instruct", "owned_by": "Qwen"},
				{"id": "", "owned_by": "skip-me"}, // 空 ID 应跳过
			},
		})
	}))
	defer ts.Close()

	a := &Adapter{client: ts.Client(), baseURL: ts.URL, apiKey: "sk-test"}
	infos, err := a.ListModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("空 ID 应跳过,期望 2 个: %+v", infos)
	}
	if infos[0].ID != "deepseek-ai/DeepSeek-V3" || infos[0].OwnedBy != "deepseek-ai" {
		t.Fatalf("第一个模型不符: %+v", infos[0])
	}
	// TTL 缓存:再次调用不再打端点
	if _, err := a.ListModels(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("缓存命中应只打一次端点,got %d", calls.Load())
	}
	// 切换端点:缓存失效,重新拉取
	if err := a.Configure(ts.URL, "sk-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListModels(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("Configure 后缓存应失效,got %d", calls.Load())
	}
}

func TestListModelsHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()
	a := &Adapter{client: ts.Client(), baseURL: ts.URL, apiKey: "bad"}
	if _, err := a.ListModels(); err == nil {
		t.Fatal("401 应显式报错(不静默空列表,防误以为无模型)")
	}
}

var _ sdk.ModelLister = (*Adapter)(nil)
var _ context.Context // 占位防误删 import(适配器 Complete 需用)
var _ = http.MethodGet
