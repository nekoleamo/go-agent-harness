// 思考等级 wire 映射:low/medium/high → thinking(enabled+budget_tokens);off 不发送。
package llmanthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestThinkingBudgetMapping(t *testing.T) {
	var lastBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		clear(lastBody) // json.Unmarshal 到复用 map 不清旧键:先清再解析
		_ = json.Unmarshal(raw, &lastBody)
		if strings.Contains(string(raw), "content_block_delta") {
			return // 忽略响应侧
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer ts.Close()
	a := &Adapter{client: ts.Client(), baseURL: ts.URL, apiKey: "sk", model: "claude-model", maxTokens: 4096}
	// High → budget 16384
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{Thinking: sdk.ThinkingHigh}, nil); err != nil {
		t.Fatal(err)
	}
	th, ok := lastBody["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"].(float64) != 16384 {
		t.Fatalf("High 应发 thinking budget 16384: %v", lastBody["thinking"])
	}
	// Off → 不发 thinking 字段
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := lastBody["thinking"]; exists {
		t.Fatalf("Off 不应发送 thinking: %v", lastBody["thinking"])
	}
	// Low → budget 1024
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{Thinking: sdk.ThinkingLow}, nil); err != nil {
		t.Fatal(err)
	}
	if b := lastBody["thinking"].(map[string]any)["budget_tokens"].(float64); b != 1024 {
		t.Fatalf("Low 应发 budget 1024: %v", b)
	}
}
