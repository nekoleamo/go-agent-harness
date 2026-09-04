// 思考等级注入测试:请求未显式设置时用会话级;显式设置优先(不被会话级覆盖)。
package hostllm

import (
	"context"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// thinkingCapture 捕获请求思考等级的适配器。
type thinkingCapture struct{ got sdk.ThinkingLevel }

func (c *thinkingCapture) Name() string { return "thinking-cap" }
func (c *thinkingCapture) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	c.got = req.Thinking
	return &sdk.LLMResponse{}, nil
}

func TestThinkingInjection(t *testing.T) {
	cap := &thinkingCapture{}
	s := &Service{adapters: map[string]sdk.LLMAdapter{"cap": cap}, order: []string{"cap"}, model: "m", thinking: sdk.ThinkingHigh}
	// 请求未显式设置:注入会话级
	if _, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if cap.got != sdk.ThinkingHigh {
		t.Fatalf("应注入会话级 High: %v", cap.got)
	}
	// 请求显式设置:优先,不被会话级覆盖
	if _, err := s.Complete(context.Background(), &sdk.LLMRequest{Thinking: sdk.ThinkingLow}, nil); err != nil {
		t.Fatal(err)
	}
	if cap.got != sdk.ThinkingLow {
		t.Fatalf("显式 Low 应优先: %v", cap.got)
	}
}
