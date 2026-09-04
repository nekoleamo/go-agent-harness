// Package hostllm 提供 host-llm 插件:ctx.llm 服务(适配器注册 + 路由)。
// 域模型与适配器 seam 定义在 sdk;本插件仅做注册表与默认路由。
package hostllm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
		return nil, fmt.Errorf("llm: 无可用 LLM 适配器(适配器插件 llm-* 已卸载或未启用,回合无法继续)")
	}
	return s.adapters[s.order[0]], nil
}

// Complete 以默认适配器发起请求;对可重试错误实施指数退避重试(§11 矩阵:网络断流/5xx 可重试,4xx 不可重试)。
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
