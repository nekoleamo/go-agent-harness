package llmopenai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestWireContentMultimodal(t *testing.T) {
	img := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(img, []byte{0x89, 'P', 'N', 'G', 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	// 图片附件 → 结构化数组(text + image_url data URI)
	got := wireContent(sdk.LLMMessage{Content: "看图", Attachments: []sdk.Attachment{
		{Kind: sdk.AttachmentImage, Path: img, MimeType: "image/png"}}})
	parts, ok := got.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("应 text+image 两块,got %T(%v)", got, got)
	}
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("data URI 前缀不对: %s", url)
	}
	// 纯文本 → 原字符串(兼容)
	if v := wireContent(sdk.LLMMessage{Content: "hi"}); v != "hi" {
		t.Fatalf("纯文本应原样,got %v", v)
	}
	// file 类附件(无视觉) → 原字符串
	v := wireContent(sdk.LLMMessage{Content: "x", Attachments: []sdk.Attachment{
		{Kind: sdk.AttachmentFile, Path: img}}})
	if v != "x" {
		t.Fatalf("file 类不应构造视觉,got %v", v)
	}
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

// TestCompleteStreamsThinking B1:reasoning_content 增量解析为 Thinking 字段(与 content 互斥)。
func TestCompleteStreamsThinking(t *testing.T) {
	_, a := sseServer(t,
		`{"choices":[{"delta":{"reasoning_content":"先分析"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"reasoning_content":"再回答"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":"正文"},"finish_reason":"stop"}]}`,
	)
	var think, content strings.Builder
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		think.WriteString(ev.Thinking)
		content.WriteString(ev.Delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if think.String() != "先分析再回答" {
		t.Fatalf("思考增量应拼合: %q", think.String())
	}
	if content.String() != "正文" {
		t.Fatalf("正文增量应正常: %q", content.String())
	}
	if resp.Message.Content != "正文" {
		t.Fatalf("聚合响应仅含正文(思考不并消息): %+v", resp.Message)
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

// TestCompleteParallelToolCalls 并行 tool_calls(index 0 与 1,B 调用首块后不再带 id):
// 旧实现只看 id + lastCallID,B 的参数增量会被并进 A(工具收到坏参数)。
func TestCompleteParallelToolCalls(t *testing.T) {
	_, a := sseServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_A","type":"function","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_B","type":"function","function":{"name":"ask_user_question","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"type":"function","function":{"arguments":"{\"prompt\":\"选哪个?\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, func(ev sdk.LLMStreamEvent) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("应聚合 2 个并行调用: %+v", resp.Message.ToolCalls)
	}
	if got := resp.Message.ToolCalls[0]; got.Name != "shell" || got.Arguments != `{"command":"ls"}` {
		t.Errorf("调用 A 聚合不符: %+v", got)
	}
	if got := resp.Message.ToolCalls[1]; got.Name != "ask_user_question" || got.Arguments != `{"prompt":"选哪个?"}` {
		t.Errorf("调用 B 参数被串到 A(索引记账缺失): %+v", got)
	}
}

// TestCompleteStreamSizeCap 端点无限推流时显式中止(安全审计 F5):
// 不是静默截断,而是带可读原因的错误,免得下游把半截回复当完整回复。
func TestCompleteStreamSizeCap(t *testing.T) {
	var lines []string
	chunk := strings.Repeat("x", 64<<10)
	for i := 0; i < 80; i++ { // 80 × 64 KiB = 5 MiB > maxCompletionSize(4 MiB)
		lines = append(lines, fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, chunk))
	}
	_, a := sseServer(t, lines...)
	_, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "test-model"}, nil)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限应显式报错,实际: %v", err)
	}
}

// bodyCapture 记录发往端点的请求体(诊断兼容性字段用)。
type bodyCapture struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (b *bodyCapture) add(m map[string]any) {
	b.mu.Lock()
	b.bodies = append(b.bodies, m)
	b.mu.Unlock()
}
func (b *bodyCapture) all() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]map[string]any(nil), b.bodies...)
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("请求体不是 JSON: %v", err)
	}
	return m
}

// TestCompleteRequestsStreamUsage 流式 usage 必须**显式要求**:
// OpenAI 官方与多数兼容端点在没收到 stream_options.include_usage 时,流末块不带 usage,
// 于是 final.Usage 恒为 0 ⇒ 不记 session/usage ⇒ 界面永远显示「用量待补」。
func TestCompleteRequestsStreamUsage(t *testing.T) {
	var cap bodyCapture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.add(decodeBody(t, r))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		// usage 块按 OpenAI 形状:choices 为空数组,只有 usage
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "m"}

	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bodies := cap.all()
	if len(bodies) != 1 {
		t.Fatalf("应只发一次请求,实际 %d", len(bodies))
	}
	so, ok := bodies[0]["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("请求应带 stream_options.include_usage=true,实际: %v", bodies[0]["stream_options"])
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 3 || resp.Usage.CachedTokens != 4 {
		t.Fatalf("流末 usage 未解析: %+v", resp.Usage)
	}
}

// TestCompleteFallsBackWhenStreamOptionsRejected 严格端点点名拒 stream_options 时:
// 去掉它重发一次并记住结论,后续请求不再带(否则每一跳都要多付一次 4xx)。
func TestCompleteFallsBackWhenStreamOptionsRejected(t *testing.T) {
	var cap bodyCapture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		cap.add(body)
		if _, has := body["stream_options"]; has {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "m"}

	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "m"}, nil); err != nil {
		t.Fatalf("降级重试应成功: %v", err)
	}
	if bodies := cap.all(); len(bodies) != 2 {
		t.Fatalf("应恰好重发一次,实际 %d 次", len(bodies))
	} else if _, has := bodies[1]["stream_options"]; has {
		t.Fatal("重发时不应再带 stream_options")
	}
	// 第二次调用:结论已记住,直接不带 ⇒ 只发一次
	if _, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	bodies := cap.all()
	if len(bodies) != 3 {
		t.Fatalf("第二次调用应只发一次,累计 %d", len(bodies))
	}
	if _, has := bodies[2]["stream_options"]; has {
		t.Fatal("记住结论后不应再带 stream_options")
	}
}

// TestCompleteDoesNotRetryUnrelatedBadRequest 没点名的 400 不得重发:
// 那可能是模型名/参数错,重发既掩盖原因又多花一次请求。
func TestCompleteDoesNotRetryUnrelatedBadRequest(t *testing.T) {
	var cap bodyCapture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.add(decodeBody(t, r))
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()
	a := &Adapter{client: srv.Client(), baseURL: srv.URL, model: "m"}

	_, err := a.Complete(context.Background(), &sdk.LLMRequest{Model: "ghost"}, nil)
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("应原样报出端点错误,实际: %v", err)
	}
	if n := len(cap.all()); n != 1 {
		t.Fatalf("无关 400 不应重发,实际发了 %d 次", n)
	}
	if a.streamUsageUnsupported() {
		t.Fatal("无关 400 不应被记成「该端点不支持 stream_options」")
	}
}
