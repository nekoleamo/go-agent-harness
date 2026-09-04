// 思考等级 wire 序列化:low/medium/high → reasoning_effort;off 不发送(omitempty)。
package llmopenai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestThinkingWireSerialization(t *testing.T) {
	var lastBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lastBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n"))
	}))
	defer ts.Close()

	a := &Adapter{client: ts.Client(), baseURL: ts.URL, apiKey: "sk"}
	// High → reasoning_effort=high
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{Thinking: sdk.ThinkingHigh}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastBody, `"reasoning_effort":"high"`) {
		t.Fatalf("High 应发送 reasoning_effort: %s", lastBody)
	}
	// Off → 不发送
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lastBody, "reasoning_effort") {
		t.Fatalf("Off 不应发送 reasoning_effort: %s", lastBody)
	}
	// Medium → medium
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{Thinking: sdk.ThinkingMedium}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastBody, `"reasoning_effort":"medium"`) {
		t.Fatalf("Medium 应发送 medium: %s", lastBody)
	}
}
