package llmopenai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sseServer 返回模拟 OpenAI 兼容端点的 SSE 流。
func sseServer(t *testing.T, lines ...string) (*httptest.Server, *Adapter) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "bad path", 404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprint(w, "data: "+l+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &Adapter{client: srv.Client(), baseURL: srv.URL, model: "test-model"}
}

func TestCompleteStreamsText(t *testing.T) {
	_, a := sseServer(t,
		`{"choices":[{"delta":{"content":"你"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"好"},"finish_reason":"stop"}]}`,
	)
	var deltas []string
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		if ev.Delta != "" {
			deltas = append(deltas, ev.Delta)
		}
		if ev.Done {
			if ev.Message.Content == "" {
				t.Errorf("Done 事件应携带聚合内容")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "你好" {
		t.Fatalf("流式增量应拼成 你好,got %q", strings.Join(deltas, ""))
	}
	if resp.FinishReason != sdk.FinishReasonStop || resp.Message.Content != "你好" {
		t.Fatalf("聚合响应不符: %+v", resp.Message)
	}
}

func TestCompleteAggregatesToolCalls(t *testing.T) {
	_, a := sseServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_1","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_1","function":{"arguments":"{\"comman"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_1","function":{"arguments":"d\":\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	var got []sdk.ToolCall
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		if ev.ToolCallID != "" {
			found := false
			for i := range got {
				if got[i].ID == ev.ToolCallID {
					got[i].Name += ev.ToolCallName
					got[i].Arguments += ev.ToolCallArgs
					found = true
				}
			}
			if !found {
				got = append(got, sdk.ToolCall{ID: ev.ToolCallID, Name: ev.ToolCallName, Arguments: ev.ToolCallArgs})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != sdk.FinishReasonToolCalls || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("应为 tool_calls 结束且 1 个调用: %+v", resp)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "shell" || tc.Arguments != `{"command":"ls"}` {
		t.Fatalf("工具调用聚合不符: %+v", tc)
	}
	_ = got
}

// TestCompleteToolCallsIDOnlyFirstChunk 推理模型流(如 DeepSeek):首个 chunk 带 id,
// 后续 chunk 不带 id(只带 index/tool 类型)——arguments 增量若按 id 找条目会全部丢失,
// 工具收到空参数。修复后应沿用首个 id 聚合完整参数。
func TestCompleteToolCallsIDOnlyFirstChunk(t *testing.T) {
	_, a := sseServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_7","type":"function","function":{"name":"web_fetch","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"url\":\"https://"}}]},"finish_reason":null}]}`,
		`{"choices": [{"delta": {"tool_calls": [{"index": 0, "type": "function", "function": {"arguments": "wttr.in/宜兴?format=3\"}"}}]}, "finish_reason": "tool_calls"}]}`,
	)
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != sdk.FinishReasonToolCalls || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("应聚合 1 个完整调用: %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "web_fetch" || tc.Arguments != `{"url":"https://wttr.in/宜兴?format=3"}` {
		t.Fatalf("无 id 后续 chunk 的参数增量应聚合完整: %+v", tc)
	}
}

func TestHTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "m"}
	_, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "m"}, nil)
	if err == nil {
		t.Fatal("401 应显式报错(不可重试类,见设计 §11)")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误应包含状态码,got %v", err)
	}
}
