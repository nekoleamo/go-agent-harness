// A-1 条目 41(anthropic 内容块后置)端到端:真 pty + **假 Anthropic Messages 端点**。
//
// 一条响应里同时出现 thinking 块 → tool_use 块 → **后置 text 块**,验:
// ① 思维增量进思维块(此前适配器不解析 thinking_delta → 整段丢);
// ② 工具照常执行(工具块之后的文本块不得覆盖工具调用);
// ③ 后置文本仍进正文;
// ④ 第二趟回复追加在正文(块后置不留残影/不吞文本)。
//
// 端点走 **provider.yaml**(而非插件 data.base_url):这正是本轮修的运行期 provider 配置路径
// (anthropic 适配器此前不参与 /provider → claude 模型仍打静态端点)。
package tests

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTUIAcceptAnthropicBlocksAfterTool(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(l string) { _, _ = fmt.Fprintf(w, "data: %s\n\n", l) }
		n := atomic.AddInt32(&calls, 1)
		write(`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`)
		if n == 1 {
			write(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
			write(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先想一下甲。"}}`)
			write(`{"type":"content_block_stop","index":0}`)
			write(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_41","name":"shell"}}`)
			write(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo 甲四十一\"}"}}`)
			write(`{"type":"content_block_stop","index":1}`)
			write(`{"type":"content_block_start","index":2,"content_block":{"type":"text"}}`)
			write(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"工具之后的正文乙。"}}`)
			write(`{"type":"content_block_stop","index":2}`)
			write(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":5,"output_tokens":9}}`)
		} else {
			write(`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`)
			write(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"第四十一项完成丙。"}}`)
			write(`{"type":"content_block_stop","index":0}`)
			write(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":6,"output_tokens":3}}`)
		}
		write(`{"type":"message_stop"}`)
	}))
	defer srv.Close()

	bin := buildGahCurrent(t)
	root := probeDataDir(t, bin)
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	// active provider = 假 Anthropic 端点(适配器经 provider.yaml Configure,不写插件 data)
	writeAcceptFile(t, cfg, "provider.yaml", "active: a\nproviders:\n  - name: a\n    base_url: "+srv.URL+"/v1\n    api_key: ak-41\n    model: claude-acc\n")
	writeAcceptFile(t, cfg, "patch-accant.yaml", `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: false
  - id: policy-guard
    data:
      approval: open
      sandbox: workspace-write
      sync: true
`)
	writeAcceptFile(t, cfg, "profile-accant.yaml", "name: accant\nbundles:\n  - base\n  - tui\npatches:\n  - patch-accant.yaml\n")

	s := newTuiSess(t, bin, probeEnv(), "--profile", "accant")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("跑一下\r")
	for _, want := range []string{"先想一下甲。", "工具之后的正文乙。", "甲四十一", "第四十一项完成丙。"} {
		if !s.waitRaw(want, 60*time.Second) {
			t.Fatalf("未出现 %q(尾段 %q)", want, stripANSI(tailS(s.rawText(), 500)))
		}
	}
	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Errorf("工具执行后应有第二趟请求,实际 %d 次", n)
	}
}
