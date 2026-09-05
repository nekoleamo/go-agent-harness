// Package hostllm 提供 host-llm 插件:ctx.llm 服务(适配器注册 + 路由)。
// 域模型与适配器 seam 定义在 sdk;本插件仅做注册表与默认路由。
package hostllm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-llm。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-llm" }

// Start 注册 ctx.llm 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	s := &Service{adapters: make(map[string]sdk.LLMAdapter)}
	if err := c.Provide("ctx.llm", s); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Service 实现 sdk.LLMService。
type Service struct {
	mu       sync.RWMutex
	adapters map[string]sdk.LLMAdapter
	order    []string // 注册顺序(首个为默认候选)
	model    string
	thinking sdk.ThinkingLevel // 会话级思考等级(Tab 循环;默认 Off)

	provModelsCache []sdk.ProviderModelList // ListAllModels TTL 缓存
	provModelsAt    time.Time               // 缓存写入时刻
}

// RegisterAdapter 注册适配器。
func (s *Service) RegisterAdapter(a sdk.LLMAdapter) sdk.Disposer {
	s.mu.Lock()
	if _, ok := s.adapters[a.Name()]; ok {
		s.mu.Unlock()
		return func() {} // 重名不致命:返回 no-op
	}
	s.adapters[a.Name()] = a
	s.order = append(s.order, a.Name())
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.adapters, a.Name())
			for i, n := range s.order {
				if n == a.Name() {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
		})
	}
}

// completeAdapter 路由当前模型到适配器:模型名精确/前缀命中 ModelRouter 声明优先;
// 无命中 → 首个注册者(默认回退,对齐原语义)。
func (s *Service) completeAdapter(model string) (sdk.LLMAdapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.order) == 0 {
		return nil, fmt.Errorf("llm: 无可用 LLM 适配器(适配器插件 llm-* 已卸载或未启用,回合无法继续)")
	}
	if model != "" {
		for _, a := range s.adapters {
			if r, ok := a.(sdk.ModelRouter); ok {
				for _, m := range r.Models() {
					if model == m || strings.HasPrefix(model, m+"-") {
						return a, nil
					}
				}
			}
		}
	}
	// 默认回退:首个未声明模型前缀的通用适配器(与注册顺序解耦;
	// 全为前缀声明型时才落 order[0])
	for _, n := range s.order {
		a := s.adapters[n]
		if _, ok := a.(sdk.ModelRouter); !ok {
			return a, nil
		}
	}
	return s.adapters[s.order[0]], nil
}

// Complete 以默认适配器发起请求;对可重试错误实施指数退避重试(§11 矩阵:网络断流/5xx 可重试,4xx 不可重试)。
func (s *Service) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	model := req.Model
	if model == "" {
		s.mu.RLock()
		model = s.model
		s.mu.RUnlock()
	}
	if model == "" {
		return nil, fmt.Errorf("llm: model not set (SetModel before first request)")
	}
	// 思考等级注入:请求未显式设置时用会话级(Tab 切换的等级;agent-loop 无需感知)
	if req.Thinking == sdk.ThinkingOff {
		s.mu.RLock()
		req.Thinking = s.thinking
		s.mu.RUnlock()
	}
	a, err := s.completeAdapter(model)
	if err != nil {
		return nil, err
	}
	req.Model = model
	return retry(ctx, a, req, onChunk)
}

// retry 指数退避重试:最多 3 次,间隔 1s/2s/4s;仅重试 sdk.RetryableError;ctx 取消立即返回。
func retry(ctx context.Context, a sdk.LLMAdapter, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	const maxAttempts = 3
	var last error
	for i := 0; i < maxAttempts; i++ {
		resp, err := a.Complete(ctx, req, onChunk)
		if err == nil {
			return resp, nil
		}
		var retryable *sdk.RetryableError
		if !errors.As(err, &retryable) || ctx.Err() != nil {
			return nil, err // 不可重试或已取消
		}
		last = err
		if i == maxAttempts-1 {
			break
		}
		backoff := time.Duration(1<<i) * time.Second // 1s, 2s, 4s
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, last
}

// SetModel 设置当前模型名。
func (s *Service) SetModel(model string) {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()
}

// —— 多 provider 并存(sdk.MultiProviderService;provider.yaml 为单一事实源,add/use/set
// 先持久化再由运行时按活跃同步适配器端点与模型;env 显式仍最高——用户显式操作覆盖) ——

// provModelsTTL ListAllModels 缓存时长(与适配器模型缓存同量级)。
const provModelsTTL = 10 * time.Minute

// Providers 全部 provider 运行时视图(文件活跃标记)。
func (s *Service) Providers() []sdk.ProviderProfile {
	f, err := providerfile.LoadFile()
	if err != nil {
		return nil
	}
	out := make([]sdk.ProviderProfile, 0, len(f.Providers))
	for _, p := range f.Providers {
		out = append(out, sdk.ProviderProfile{Name: p.Name, BaseURL: p.BaseURL,
			APIKey: p.APIKey, Model: p.Model, Active: p.Name == f.Active})
	}
	return out
}

// AddProvider 新增/更新 provider(同名 upsert 并激活;首个自动激活)。
// 成为活跃时立即同步适配器端点与模型(运行时即生效)。
func (s *Service) AddProvider(name, baseURL, apiKey, model string) error {
	if name == "" {
		name = providerfile.ShortNameOf(baseURL)
	}
	if err := providerfile.Add(providerfile.Provider{Name: name, BaseURL: baseURL, APIKey: apiKey, Model: model}); err != nil {
		return err
	}
	f, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	if f.Active != name {
		return nil // 新增非活跃:仅并存,不切运行时
	}
	p, ok := findProvider(f, name)
	if !ok {
		return nil
	}
	return s.switchActive(p)
}

// SetActiveProvider 切换活跃 provider(校验存在;立即同步适配器端点与模型并持久化 active)。
func (s *Service) SetActiveProvider(name string) error {
	if err := providerfile.SetActive(name); err != nil {
		return err
	}
	f, err := providerfile.LoadFile()
	if err != nil {
		return err
	}
	p, ok := findProvider(f, name)
	if !ok {
		return fmt.Errorf("provider: 不存在 %q", name)
	}
	return s.switchActive(p)
}

// findProvider 从文件视图按名取 provider。
func findProvider(f providerfile.File, name string) (providerfile.Provider, bool) {
	for _, p := range f.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return providerfile.Provider{}, false
}

// switchActive 同步通用适配器到活跃 provider 端点/模型(Configure + SetModel;零重启)。
func (s *Service) switchActive(p providerfile.Provider) error {
	if p.BaseURL == "" {
		return fmt.Errorf("provider: %s 未配置 base_url(请 /provider set 补齐)", p.Name)
	}
	pa, err := s.genericProvider()
	if err != nil {
		return err
	}
	if err := pa.Configure(p.BaseURL, p.APIKey); err != nil {
		return err
	}
	if p.Model != "" {
		s.SetModel(p.Model)
	}
	return nil
}

// ListAllModels 聚合所有 provider 端点 /models(TTL 缓存;单条失败记入其 Err,不整体失败)。
// 活跃 provider 走通用适配器 ListModels(复用其 TTL 缓存),非活跃经 sdk.OpenAIFetchModels 直拉。
func (s *Service) ListAllModels() []sdk.ProviderModelList {
	s.mu.RLock()
	if s.provModelsCache != nil && time.Since(s.provModelsAt) < provModelsTTL {
		out := append([]sdk.ProviderModelList(nil), s.provModelsCache...)
		s.mu.RUnlock()
		return out
	}
	s.mu.RUnlock()

	f, err := providerfile.LoadFile()
	if err != nil {
		return nil
	}
	out := make([]sdk.ProviderModelList, 0, len(f.Providers))
	for _, p := range f.Providers {
		if p.Name == f.Active {
			out = append(out, s.listActiveModels(p))
		} else {
			m, e := sdk.OpenAIFetchModels(p.BaseURL, p.APIKey)
			out = append(out, sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Models: m, Err: e})
		}
	}
	s.mu.Lock()
	s.provModelsCache = out
	s.provModelsAt = time.Now()
	s.mu.Unlock()
	return out
}

// listActiveModels 活跃 provider 的模型列表(经通用适配器 ModelLister,复用其端点缓存)。
func (s *Service) listActiveModels(p providerfile.Provider) sdk.ProviderModelList {
	pa, err := s.genericProvider()
	if err != nil {
		return sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Err: err}
	}
	if ml, ok := pa.(sdk.ModelLister); ok {
		m, e := ml.ListModels()
		return sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Models: m, Err: e}
	}
	m, e := sdk.OpenAIFetchModels(p.BaseURL, p.APIKey)
	return sdk.ProviderModelList{Name: p.Name, BaseURL: p.BaseURL, Models: m, Err: e}
}

// genericProvider 当前通用适配器(非 ModelRouter 声明型,即 openai 兼容类)。
func (s *Service) genericProvider() (sdk.ProviderAdapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var fallback sdk.LLMAdapter
	for _, n := range s.order {
		a := s.adapters[n]
		if _, ok := a.(sdk.ModelRouter); ok {
			continue
		}
		if pa, ok := a.(sdk.ProviderAdapter); ok {
			return pa, nil
		}
		if fallback == nil {
			fallback = a
		}
	}
	if fallback != nil {
		return nil, fmt.Errorf("llm: 通用适配器 %s 不支持运行时 provider 配置(未实现 sdk.ProviderAdapter)", fallback.Name())
	}
	return nil, fmt.Errorf("llm: 无通用 LLM 适配器(adapter 插件未装配),无法配置 provider")
}

// SetProvider 运行时切换端点与凭据(TUI /provider set;零重启)。
func (s *Service) SetProvider(baseURL, apiKey string) error {
	pa, err := s.genericProvider()
	if err != nil {
		return err
	}
	return pa.Configure(baseURL, apiKey)
}

// UnsetProvider 逐项删除配置(TUI /provider unset;该项恢复启动默认)。
func (s *Service) UnsetProvider(field string) error {
	pa, err := s.genericProvider()
	if err != nil {
		return err
	}
	return pa.Unset(field)
}

// ResetProvider 恢复全部字段为启动默认(TUI /provider clear)。
func (s *Service) ResetProvider() error {
	pa, err := s.genericProvider()
	if err != nil {
		return err
	}
	return pa.Reset()
}

// ListModels 当前通用适配器端点可用模型列表(TUI /model 动态枚举)。
func (s *Service) ListModels() ([]sdk.ModelInfo, error) {
	pa, err := s.genericProvider()
	if err != nil {
		return nil, err
	}
	if ml, ok := pa.(sdk.ModelLister); ok {
		return ml.ListModels()
	}
	return nil, fmt.Errorf("llm: 通用适配器 %s 不支持模型列举(未实现 sdk.ModelLister)", paName(pa))
}

// SetThinking 设置会话级思考等级(思考等级注入未显式设置的请求)。
func (s *Service) SetThinking(t sdk.ThinkingLevel) {
	s.mu.Lock()
	s.thinking = t
	s.mu.Unlock()
}

// Thinking 当前会话级思考等级。
func (s *Service) Thinking() sdk.ThinkingLevel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.thinking
}

// paName ProviderAdapter 的名称(展示用)。
func paName(pa sdk.ProviderAdapter) string {
	if n, ok := pa.(interface{ Name() string }); ok {
		return n.Name()
	}
	return fmt.Sprintf("%T", pa)
}

// ProviderInfo 当前通用适配器的端点与凭据(展示用;key 由调用方打码)。
func (s *Service) ProviderInfo() (string, string, bool) {
	pa, err := s.genericProvider()
	if err != nil {
		return "", "", false
	}
	u, k := pa.ProviderInfo()
	return u, k, true
}

// Model 返回当前模型名。
func (s *Service) Model() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// List 列出已注册适配器。
func (s *Service) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.order...)
}
