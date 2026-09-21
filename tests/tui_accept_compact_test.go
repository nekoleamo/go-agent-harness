// A-1 条目 13(P4-8 滚动摘要压缩)的 pty 真机验收。
//
// 手法:自建 **记录请求体** 的 OpenAI 兼容端点(spy provider)—— 压缩"生效"的硬证据是
// 「后续请求的模型上下文里出现滚动摘要」,只断言 TUI 回显一句话证明不了生效。
package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// spyProvider 记录每个请求体,并按固定文本流式答复。
type spyProvider struct {
	mu     sync.Mutex
	bodies []string
}

func (s *spyProvider) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func newSpyProvider(t *testing.T, reply string, delay time.Duration) (string, *spyProvider) {
	t.Helper()
	sp := &spyProvider{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sp.mu.Lock()
		sp.bodies = append(sp.bodies, string(b))
		sp.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if fl != nil {
				fl.Flush()
			}
		}
		chunk, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-spy", "object": "chat.completion.chunk", "model": "slow-model",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": reply}}},
		})
		emit(string(chunk))
		emit("[DONE]")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, sp
}

// TestTUIAcceptCompactRollingSummary 条目 13:超预算时滚动摘要压缩生效 ——
// 后续回合的**模型上下文**含「对先前对话的滚动摘要」,且 /compact 回显压缩结果。
func TestTUIAcceptCompactRollingSummary(t *testing.T) {
	reply := strings.Repeat("压缩前第一轮答复。", 12) // ≈ 108 字,足以超预算且不撑爆屏幕
	base, spy := newSpyProvider(t, reply, 0)
	bin, env, root := tuiAcceptSetupSlow(t, base)
	// 预算压小(默认 40960 需超长语料):第二轮请求前即触及自动压缩
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", `entries:
  - id: token-compress
    data:
      token_budget_chars: 80
  - id: llm-mock
    enabled: false
  - id: llm-openai-compat
    enabled: true
`)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("第一轮问题甲\r")
	if !s.waitScreen("压缩前第一轮答复", 60*time.Second) {
		t.Fatalf("条目 13:第一轮未完成;屏尾 %q", firstN(tailS(s.screen(), 300), 300))
	}
	if !s.waitIdle(30 * time.Second) {
		t.Fatalf("条目 13:第一轮未结束;屏尾 %q", firstN(tailS(s.screen(), 300), 300))
	}
	bodies := spy.snapshot()
	if len(bodies) == 0 {
		t.Fatal("条目 13:未捕获到任何模型请求")
	}
	if !strings.Contains(bodies[len(bodies)-1], "第一轮问题甲") {
		t.Errorf("条目 13:首个请求上下文里未见第一轮提问;体尾 %q", firstN(tailS(bodies[len(bodies)-1], 300), 300))
	}

	s.send("第二轮问题乙\r")
	if !s.waitScreen("第二轮问题乙", 20*time.Second) {
		t.Fatal("条目 13:第二轮未提交")
	}
	deadline := time.Now().Add(60 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		bs := spy.snapshot()
		if len(bs) >= 2 {
			last = bs[len(bs)-1]
			if strings.Contains(last, "第二轮问题乙") && strings.Contains(last, "对先前对话的滚动摘要") {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("条目 13:请求数=%d 末请求含摘要=%v", len(spy.snapshot()), strings.Contains(last, "对先前对话的滚动摘要"))
	if !strings.Contains(last, "对先前对话的滚动摘要") {
		t.Errorf("条目 13:第二轮请求的模型上下文未含滚动摘要(压缩未生效);体尾 %q", firstN(tailS(last, 400), 400))
	}
	if !strings.Contains(last, "第二轮问题乙") {
		t.Errorf("条目 13:末请求不是第二轮(未捕获到);体尾 %q", firstN(tailS(last, 300), 300))
	}

	// /compact 回显(手动路径;此时多半已被自动压缩折叠过)
	s.send("/compact 指示词甲\r")
	time.Sleep(900 * time.Millisecond)
	s.send("\r")
	shown, echo := false, ""
	for i := 0; i < 40 && !shown; i++ {
		time.Sleep(300 * time.Millisecond)
		for _, l := range strings.Split(s.screen(), "\n") {
			if strings.Contains(l, "滚动摘要") || strings.Contains(l, "无可压缩历史") {
				shown, echo = true, strings.TrimSpace(l)
			}
		}
	}
	t.Logf("条目 13:/compact 回显=%q", echo)
	if !shown {
		t.Errorf("条目 13:/compact 未回显结果;屏尾 %q", firstN(tailS(s.screen(), 300), 300))
	}
}
