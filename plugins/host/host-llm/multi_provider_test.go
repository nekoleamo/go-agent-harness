// 多 provider 并存(MultiProviderService)单测:视图/首 active/并存不切/同名 upsert 激活/
// 切换、ListAllModels 聚合(活跃走适配器缓存、非活跃直拉、单条失败不整体失败、TTL 缓存)。
package hostllm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// multiSvc 构造带通用适配器的 Service(GAH_HOME 隔离)。
func multiSvc(t *testing.T) *Service {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, order: []string{"generic"}}
	s.adapters["generic"] = &providerAdapter{name: "generic", baseURL: "https://api.siliconflow.cn/v1",
		models: []sdk.ModelInfo{{ID: "deepseek-ai/DeepSeek-V3", OwnedBy: "deepseek-ai"}, {ID: "Qwen/Qwen2.5-72B"}}}
	return s
}

func TestMultiAddFirstActiveAndConfigure(t *testing.T) {
	s := multiSvc(t)
	gen := s.adapters["generic"].(*providerAdapter)

	if err := s.AddProvider("", "https://api.siliconflow.cn/v1", "sk-a", "deepseek-ai/DeepSeek-V3"); err != nil {
		t.Fatal(err)
	}
	// 首条自动 active + 适配器同步
	if gen.configuredURL != "https://api.siliconflow.cn/v1" || gen.configuredKey != "sk-a" {
		t.Fatalf("首条应激活并同步适配器: %s %s", gen.configuredURL, gen.configuredKey)
	}
	if s.Model() != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("应同步模型: %s", s.Model())
	}
	// 视图:active 标记
	ps := s.Providers()
	if len(ps) != 1 || !ps[0].Active || ps[0].Name != "siliconflow" || ps[0].APIKey != "sk-a" {
		t.Fatalf("视图: %+v", ps)
	}

	// 新增并存:不切活跃(仍 siliconflow)
	if err := s.AddProvider("deepseek", "https://api.deepseek.com/v1", "sk-ds", "deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if gen.configuredURL != "https://api.siliconflow.cn/v1" {
		t.Fatalf("新增并存不应切运行时: %s", gen.configuredURL)
	}
	ps = s.Providers()
	if len(ps) != 2 || ps[0].Active != true || ps[1].Active != false {
		t.Fatalf("并存视图: %+v", ps)
	}
}

func TestMultiUpsertAndSwitch(t *testing.T) {
	s := multiSvc(t)
	gen := s.adapters["generic"].(*providerAdapter)
	s.AddProvider("a", "https://api.a.cn/v1", "k-a", "m-a")
	s.AddProvider("b", "https://api.b.cn/v1", "k-b", "m-b")

	// 切换 b → 运行时同步 b 端点+模型,持久化 active
	if err := s.SetActiveProvider("b"); err != nil {
		t.Fatal(err)
	}
	if gen.configuredURL != "https://api.b.cn/v1" || s.Model() != "m-b" {
		t.Fatalf("切换应同步: %s %s", gen.configuredURL, s.Model())
	}
	if providerfile.Active() != "b" {
		t.Fatalf("持久化 active 应 b: %s", providerfile.Active())
	}
	// 同名 upsert(更新并激活)
	if err := s.AddProvider("b", "https://api.b2.cn/v1", "k-b2", "m-b2"); err != nil {
		t.Fatal(err)
	}
	if gen.configuredURL != "https://api.b2.cn/v1" || s.Model() != "m-b2" {
		t.Fatalf("upsert 应激活: %s %s", gen.configuredURL, s.Model())
	}
	if providerfile.Active() != "b" {
		t.Fatalf("upsert 后 active 应 b: %s", providerfile.Active())
	}
	// 切换不存在显式报错
	if err := s.SetActiveProvider("zzz"); err == nil {
		t.Fatal("切换不存在应报错")
	}
}

func TestListAllModelsAggregate(t *testing.T) {
	s := multiSvc(t)

	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_ = json.NewEncoder(w).Encode(struct {
			Data []struct {
				ID      string `json:"id"`
				OwnedBy string `json:"owned_by"`
			} `json:"data"`
		}{Data: []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		}{{ID: "remote-model-x", OwnedBy: "remote"}}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 首条 a(active)→ 适配器 fake 列表;remote(非活跃)直拉 httptest
	if err := s.AddProvider("", "https://api.a.cn/v1", "k-a", "m-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProvider("remote", ts.URL, "k-remote", "remote-model-x"); err != nil {
		t.Fatal(err)
	}

	all := s.ListAllModels()
	if len(all) != 2 {
		t.Fatalf("应聚合 2 个 provider: %+v", all)
	}
	// 活跃 a 经适配器 fake 列表
	if all[0].Name != "a" || len(all[0].Models) != 2 || all[0].Models[0].ID != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("活跃应走适配器列表: %+v", all[0])
	}
	// 非活跃 remote 直拉 httptest
	if all[1].Name != "remote" || len(all[1].Models) != 1 || all[1].Models[0].ID != "remote-model-x" {
		t.Fatalf("非活跃应直拉端点: %+v", all[1])
	}
	// TTL 缓存:二次调用非活跃不再打端点
	_ = s.ListAllModels()
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("缓存后不应再拉端点, hits=%d", hits)
	}
}

func TestListAllSingleFailureNotFatal(t *testing.T) {
	s := multiSvc(t)
	// 活跃 ok 走适配器静态列表;dead 非活跃直拉未监听端口 → Err 记该条
	if err := s.AddProvider("ok", "https://api.ok.cn/v1", "k", "m"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProvider("dead", "http://127.0.0.1:1/v1", "k", "m"); err != nil {
		t.Fatal(err)
	}
	all := s.ListAllModels()
	byName := map[string]sdk.ProviderModelList{}
	for _, p := range all {
		byName[p.Name] = p
	}
	if dead, ok := byName["dead"]; !ok || dead.Err == nil {
		t.Fatalf("不可达端点应记 Err: %+v", dead)
	}
	if ok := byName["ok"]; ok.Err != nil {
		t.Fatalf("正常 provider 不应有 Err: %+v", ok)
	}
}

var _ = context.Background

// TestListAllModelsCacheInvalidatedOnProviderChange 新增/切换 provider 必须让聚合模型缓存失效:
// 否则 Web 首启自检(保存后立即拉一次模型列表)会在 10 分钟 TTL 内读到旧结果 ——
// 列表里没有刚保存的 provider,或还带着旧 Key 的 401。
func TestListAllModelsCacheInvalidatedOnProviderChange(t *testing.T) {
	s := multiSvc(t)

	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_ = json.NewEncoder(w).Encode(struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}{Data: []struct {
			ID string `json:"id"`
		}{{ID: "remote-x"}}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// a 首个 → 活跃(模型列表走 fake 适配器,不打网络);b 非活跃(直拉 httptest)
	if err := s.AddProvider("a", ts.URL, "k-a", "m-a"); err != nil {
		t.Fatal(err)
	}
	if all := s.ListAllModels(); len(all) != 1 {
		t.Fatalf("初态应 1 条: %+v", all)
	}
	if err := s.AddProvider("b", ts.URL, "k-b", "m-b"); err != nil {
		t.Fatal(err)
	}
	all := s.ListAllModels()
	if len(all) != 2 {
		t.Fatalf("新增后应立刻看到 2 条(缓存未失效): %+v", all)
	}
	if all[1].Name != "b" || len(all[1].Models) != 1 || all[1].Models[0].ID != "remote-x" {
		t.Fatalf("新增 provider 应直拉端点: %+v", all[1])
	}
	// 切活跃同样失效:b 变活跃(走适配器),a 变非活跃(直拉端点)
	if err := s.SetActiveProvider("b"); err != nil {
		t.Fatal(err)
	}
	all = s.ListAllModels()
	if len(all) != 2 || all[0].Name != "a" || all[1].Name != "b" {
		t.Fatalf("切换后应 2 条且顺序按文件: %+v", all)
	}
	if len(all[0].Models) != 1 || all[0].Models[0].ID != "remote-x" {
		t.Fatalf("原活跃应转为直拉: %+v", all[0])
	}
	if len(all[1].Models) != 2 || all[1].Models[0].ID != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("新活跃应走适配器列表: %+v", all[1])
	}
}
