package llmanthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sseServer 返回模拟 Anthropic Messages 端点的 SSE 流。
func sseServer(t *testing.T, lines ...string) (*httptest.Server, *Adapter, *bytes.Buffer) {
	t.Helper()
	var lastBody bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.Error(w, "bad path", 404)
			return
		}
		lastBody.Reset()
		_, _ = io.Copy(&lastBody, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprint(w, "event: "+eventName(l)+"\n"+"data: "+l+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &Adapter{client: srv.Client(), baseURL: srv.URL, model: "test-model", maxTokens: 4096}, &lastBody
}

// eventName 从事件 JSON 中取 type(测试用简化解析)。
func eventName(data string) string {
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(data), &ev)
	return ev.Type
}

// TestCompleteStreamsText 文本流:增量聚合 + end_turn 结束。
func TestCompleteStreamsText(t *testing.T) {
	_, a, _ := sseServer(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"世界"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	)
	var deltas []string
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		if ev.Delta != "" {
			deltas = append(deltas, ev.Delta)
		}
		if ev.Done && ev.Message.Content == "" {
			t.Error("Done 事件应携带聚合内容")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "你好世界" {
		t.Fatalf("增量应聚合为 你好世界,got %q", strings.Join(deltas, ""))
	}
	if resp.FinishReason != sdk.FinishReasonStop || resp.Message.Content != "你好世界" {
		t.Fatalf("聚合响应不符: %+v", resp.Message)
	}
	if resp.Usage.CompletionTokens != 4 {
		t.Fatalf("usage 应回填: %+v", resp.Usage)
	}
}

// TestCompleteAggregatesToolCalls 工具调用:input_json_delta 聚合 + tool_use 结束。
func TestCompleteAggregatesToolCalls(t *testing.T) {
	_, a, _ := sseServer(t,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"comman"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"d\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`{"type":"message_stop"}`,
	)
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != sdk.FinishReasonToolCalls || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("应为 tool_use 结束且 1 个调用: %+v", resp)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "shell" || tc.Arguments != `{"command":"ls"}` {
		t.Fatalf("工具调用聚合不符: %+v", tc)
	}
}

// TestRequestMapping 请求结构映射:system 提取、tool_result 入 user 消息、tools 携带。
func TestRequestMapping(t *testing.T) {
	_, a, lastBody := sseServer(t,
		`{"type":"message_start","message":{"usage":{}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	)
	req := &sdk.LLMRequest{Model: "test-model",
		Messages: []sdk.LLMMessage{
			{Role: sdk.RoleSystem, Content: "你是助手"},
			{Role: sdk.RoleUser, Content: "你好"},
			{Role: sdk.RoleAssistant, Content: "", ToolCalls: []sdk.ToolCall{{ID: "toolu_1", Name: "shell", Arguments: `{"command":"ls"}`}}},
			{Role: sdk.RoleTool, ToolCallID: "toolu_1", Content: "输出内容"},
		},
		Tools: []sdk.ToolDefinition{{Name: "shell", Description: "执行命令"}},
	}
	if _, err := a.Complete(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var wire wireReq
	if err := json.Unmarshal(lastBody.Bytes(), &wire); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if wire.System != "你是助手" {
		t.Fatalf("system 应提取到顶层: %q", wire.System)
	}
	if len(wire.Messages) != 3 {
		t.Fatalf("消息数不符(2 条请求 + 1 条 tool_result): %d", len(wire.Messages))
	}
	// 第二条 = 工具调用结果的 user 消息(tool_result block)
	last := wire.Messages[len(wire.Messages)-1]
	blocks := last.Content
	if len(blocks) != 1 {
		t.Fatalf("tool_result 应为 1 个 block: %+v", blocks)
	}
	tr, ok := blocks[0].(map[string]any)
	if !ok || tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool_result block 不符: %+v", blocks)
	}
	if len(wire.Tools) != 1 || wire.Tools[0].Name != "shell" {
		t.Fatalf("tools 应携带: %+v", wire.Tools)
	}
	if !wire.Stream {
		t.Fatal("应为流式请求")
	}
}

// TestHTTPErrorSurfaced 401 显式报错(不可重试)。
func TestHTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "m", maxTokens: 4096}
	_, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "m"}, nil)
	if err == nil {
		t.Fatal("401 应显式报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误应包含状态码: %v", err)
	}
}