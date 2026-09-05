// 重试语义测试:可重试错误指数退避重试 ×3;不可重试错误不重试;取消立即返回。
package hostllm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// flaky 适配器:前 failTimes 次返回可重试错误,之后成功。
type flaky struct {
	calls     atomic.Int64
	failTimes int64
	failErr   error
}

func (f *flaky) Name() string { return "flaky" }
func (f *flaky) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	n := f.calls.Add(1)
	if n <= f.failTimes {
		return nil, f.failErr
	}
	return &sdk.LLMResponse{}, nil
}

func TestRetryableRecovers(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, model: "m", order: []string{"flaky"}}
	// 前 2 次失败,第 3 次成功;退避 1s+2s ≈ 3s
	start := time.Now()
	a := &flaky{failTimes: 2, failErr: &sdk.RetryableError{Err: errors.New("5xx")}}
	s.adapters["flaky"] = a
	resp, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil)
	if err != nil {
		t.Fatalf("重试应最终成功: %v", err)
	}
	if resp == nil || a.calls.Load() != 3 {
		t.Fatalf("应恰好调用 3 次,got %d", a.calls.Load())
	}
	if time.Since(start) < 3*time.Second {
		t.Fatalf("退避应累计 ≈3s,got %v", time.Since(start))
	}
}

func TestNonRetryableNotRetried(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, model: "m", order: []string{"flaky"}}
	a := &flaky{failTimes: 99, failErr: errors.New("401 auth")}
	s.adapters["flaky"] = a
	_, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil)
	if err == nil {
		t.Fatal("不可重试错误应失败")
	}
	var re *sdk.RetryableError
	if errors.As(err, &re) {
		t.Fatalf("4xx 不应包装为可重试错误: %v", err)
	}
	if a.calls.Load() != 1 {
		t.Fatalf("不可重试错误只应调用 1 次,got %d", a.calls.Load())
	}
}

func TestCancelStopsRetryLoop(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, model: "m", order: []string{"flaky"}}
	a := &flaky{failTimes: 99, failErr: &sdk.RetryableError{Err: errors.New("5xx")}}
	s.adapters["flaky"] = a
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := s.Complete(ctx, &sdk.LLMRequest{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应传递,got %v", err)
	}
	if a.calls.Load() > 2 {
		t.Fatalf("取消后不应继续重试,got %d 次调用", a.calls.Load())
	}
}

// TestModelPrefixRouting 模型前缀路由:claude-* 命中 ModelRouter 声明,其余回落默认。
// 路由实现(Anthropic 适配器接入的方式,见 llm-anthropic-compat)。
func TestModelPrefixRouting(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, model: "claude-sonnet-4-5", order: []string{"openai", "claude"}}
	aOpenai := &msgAdapter{name: "openai"}
	aClaude := &msgAdapter{name: "claude"}
	s.adapters["openai"] = aOpenai
	s.adapters["claude"] = aClaude

	a, err := s.completeAdapter("claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "claude" {
		t.Fatalf("claude-* 应路由到 claude 适配器,got %s", a.Name())
	}
	a, err = s.completeAdapter("deepseek-chat")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "openai" {
		t.Fatalf("非 claude 模型应回落首个注册适配器,got %s", a.Name())
	}
	a, err = s.completeAdapter("")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "openai" {
		t.Fatalf("空模型应回落默认,got %s", a.Name())
	}
}

// msgAdapter 简单适配器(带 ModelRouter,供路由测试)。
type msgAdapter struct {
	name string
}

func (m *msgAdapter) Name() string { return m.name }
func (m *msgAdapter) Models() []string {
	if m.name == "claude" {
		return []string{"claude"}
	}
	return nil
}
func (m *msgAdapter) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: m.name}}, nil
}
