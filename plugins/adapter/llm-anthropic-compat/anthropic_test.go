package llmanthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestCompleteSendsImageBlock 图片附件 → 请求 payload 含 image base64 块(附件一期视觉注入)。
func TestCompleteSendsImageBlock(t *testing.T) {
	img := filepath.Join(t.TempDir(), "p.png")
	if err := os.WriteFile(img, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, a, body := sseServer(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	)
	_, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model", Messages: []sdk.LLMMessage{
		{Role: sdk.RoleUser, Content: "看图", Attachments: []sdk.Attachment{
			{Kind: sdk.AttachmentImage, Path: img, MimeType: "image/png"}}}}}, func(_ sdk.LLMStreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var reqWire struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body.Bytes(), &reqWire); err != nil {
		t.Fatal(err)
	}
	if len(reqWire.Messages) == 0 {
		t.Fatal("无 messages 入请求")
	}
	found := false
	for _, blk := range reqWire.Messages[0].Content {
		if blk["type"] == "image" {
			src := blk["source"].(map[string]any)
			if src["type"] != "base64" || src["media_type"] != "image/png" {
				t.Fatalf("image source 字段: %v", src)
			}
			if src["data"] != base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
				t.Fatalf("base64 内容不符: %v", src["data"])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("请求未含 image 块")
	}
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

// TestCompleteStreamsThinkingAndTextAfterTool 条目 41「anthropic blocks 后置」:
// ① 扩展思考块(thinking_delta)必须转成 sdk.LLMStreamEvent.Thinking(此前未解析 → 整段丢弃);
// ② 内容块**后置**:tool_use 块之后再出 text 块,正文仍应完整聚合,且 stop_reason 仍识别为工具调用。
func TestCompleteStreamsThinkingAndTextAfterTool(t *testing.T) {
	_, a, _ := sseServer(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":8,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先看需求。"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"再定方案。"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"工具之后的正文。"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":8,"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	)
	var think, text string
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		think += ev.Thinking
		text += ev.Delta
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if think != "先看需求。再定方案。" {
		t.Errorf("思维块增量应转成 Thinking: %q", think)
	}
	if text != "工具之后的正文。" {
		t.Errorf("工具块之后的文本块应仍聚合为正文: %q", text)
	}
	if resp.Message.Content != "工具之后的正文。" {
		t.Errorf("聚合响应正文不符: %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "shell" ||
		resp.Message.ToolCalls[0].Arguments != `{"command":"ls"}` {
		t.Errorf("工具调用聚合不符: %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != sdk.FinishReasonToolCalls {
		t.Errorf("stop_reason=tool_use 应映射为工具调用结束: %v", resp.FinishReason)
	}
}

// Configure 运行时切换端点/凭据(补 ProviderAdapter 前本适配器不参与 /provider:
// claude 模型仍打静态端点、base_url 被配到别的适配器上)。这里验"真打到新端点 + 新凭据 + 可恢复"。
func TestConfigureSwitchesEndpointAndKey(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"新端点"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
	}))
	defer srv.Close()

	a := &Adapter{client: srv.Client(), baseURL: "https://api.anthropic.com/v1", model: "claude-x", maxTokens: 100,
		defaultBaseURL: "https://api.anthropic.com/v1", defaultAPIKey: "default-key", defaultModel: "claude-x"}
	if err := a.Configure(srv.URL+"/v1/", "sk-new"); err != nil {
		t.Fatal(err)
	}
	if b, k := a.ProviderInfo(); b != srv.URL+"/v1" || k != "sk-new" {
		t.Fatalf("Configure 应去掉尾斜杠并生效,得 %q/%q", b, k)
	}
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "claude-x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("请求应打到配置后的端点路径,得 %q", gotPath)
	}
	if gotKey != "sk-new" {
		t.Errorf("应带新凭据,得 %q", gotKey)
	}
	if resp.Message.Content != "新端点" {
		t.Errorf("响应内容不符: %q", resp.Message.Content)
	}
	if err := a.Configure("ftp://x", "k"); err == nil {
		t.Error("非 http(s) 前缀应拒绝")
	}
	if err := a.Unset("base_url"); err != nil {
		t.Fatal(err)
	}
	if b, _ := a.ProviderInfo(); b != "https://api.anthropic.com/v1" {
		t.Errorf("Unset 应恢复启动默认,得 %q", b)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	if b, k := a.ProviderInfo(); b != "https://api.anthropic.com/v1" || k != "default-key" {
		t.Errorf("Reset 应恢复全部默认,得 %q/%q", b, k)
	}
}
