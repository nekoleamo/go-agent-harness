// Package hostllm 提供 host-llm 插件:ctx.llm 服务(适配器注册 + 路由)。
// 域模型与适配器 seam 定义在 sdk;本插件仅做注册表与默认路由。
package hostllm

import (
	"context"
	"fmt"
	"sync"

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

// completeAdapter 取当前默认适配器:首个注册者(M3 起支持按模型路由)。
func (s *Service) completeAdapter() (sdk.LLMAdapter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.order) == 0 {
		return nil, fmt.Errorf("llm: no adapter registered")
	}
	return s.adapters[s.order[0]], nil
}

// Complete 以默认适配器发起请求。
func (s *Service) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	a, err := s.completeAdapter()
	if err != nil {
		return nil, err
	}
	if req.Model == "" {
		s.mu.RLock()
		req.Model = s.model
		s.mu.RUnlock()
	}
	if req.Model == "" {
		return nil, fmt.Errorf("llm: model not set (SetModel before first request)")
	}
	return a.Complete(ctx, req, onChunk)
}

// SetModel 设置当前模型名。
func (s *Service) SetModel(model string) {
	s.mu.Lock()
	s.model = model
	s.mu.Unlock()
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
