// A-1 条目 6/7/8(P4-1 消息队列)的 pty 真机验收。
//
// 要点:队列只在「回合进行中」才有意义,而 llm-mock 没有慢响应旋钮 —— 故本批自建
// **慢 SSE 的 OpenAI 兼容端点**(首 token 前延迟),把它写进 provider.yaml 制造稳定窗口。
package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// slowProvider 起一个慢 SSE 的 OpenAI 兼容端点(首 token 前延迟 delay),返回 base URL。
func slowProvider(t *testing.T, delay time.Duration) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		emit := func(s string) {
			fmt.Fprintf(w, "data: %s\n\n", s)
			if fl != nil {
				fl.Flush()
			}
		}
		for _, piece := range []string{"慢", "回复", "完成"} {
			b, _ := json.Marshal(map[string]any{
				"id": "chatcmpl-slow", "object": "chat.completion.chunk", "model": "slow-model",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": piece}}},
			})
			emit(string(b))
			time.Sleep(150 * time.Millisecond)
		}
		emit("[DONE]")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// tuiAcceptSetupSlow 与 tuiAcceptSetup 同构,但把模型指向慢端点(关 mock、开真实适配器)。
func tuiAcceptSetupSlow(t *testing.T, baseURL string) (bin string, env []string, root string) {
	t.Helper()
	bin = buildGahCurrent(t)
	root = probeDataDir(t, bin)
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAcceptFile(t, cfg, "provider.yaml",
		"active: slow\nproviders:\n  - name: slow\n    base_url: "+baseURL+"/v1\n    api_key: k\n    model: slow-model\n")
	writeAcceptFile(t, cfg, "patch-acctui.yaml", "entries:\n  - id: llm-mock\n    enabled: false\n  - id: llm-openai-compat\n    enabled: true\n")
	writeAcceptFile(t, cfg, "profile-acctui.yaml", "name: acctui\nbundles:\n  - base\n  - tui\npatches:\n  - patch-acctui.yaml\n")
	return bin, probeEnv(), root
}

// TestTUIAcceptQueueDuringTurn 条目 6:回合中输入 →「待发 N」→ 回合结束自动续发。
func TestTUIAcceptQueueDuringTurn(t *testing.T) {
	base := slowProvider(t, 3*time.Second)
	bin, env, _ := tuiAcceptSetupSlow(t, base)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("第一问\r")
	if !s.waitRaw("❯ 第一问", 8*time.Second) {
		t.Fatalf("条目 6:首轮未被提交;尾部 %s", stripANSI(tailS(s.rawText(), 400)))
	}
	time.Sleep(700 * time.Millisecond) // 回合已在跑(慢端点 3s 才回)
	s.send("排队一\r")
	if !s.waitRaw("待发 1", 6*time.Second) {
		t.Errorf("条目 6:回合中入队未显示「待发 1」;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
	// 回合结束后应自动续发:排队消息作为**用户消息**出现在会话流(前缀 ❯)
	if !s.waitRaw("❯ 排队一", 25*time.Second) {
		t.Errorf("条目 6:回合结束后未自动续发排队消息;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
}

// TestTUIAcceptQueueRecall 条目 7:Esc 取消后队列保留,Alt+Up 取回可继续编辑。
func TestTUIAcceptQueueRecall(t *testing.T) {
	base := slowProvider(t, 4*time.Second)
	bin, env, _ := tuiAcceptSetupSlow(t, base)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("第一问\r")
	if !s.waitRaw("❯ 第一问", 8*time.Second) {
		t.Fatal("首轮未提交")
	}
	time.Sleep(600 * time.Millisecond)
	s.send("排队二\r")
	if !s.waitRaw("待发 1", 6*time.Second) {
		t.Fatalf("条目 7:入队未生效")
	}
	s.send("\x1b") // Esc 取消当前回合(队列应保留)
	if !s.waitScreen("空闲", 10*time.Second) {
		t.Fatalf("条目 7:Esc 未结束回合;屏 %q", firstN(s.screen(), 300))
	}
	// 队列保留:判**实时状态栏那行**(整屏 Contains 会被就地重绘留下的旧行骗到)
	if line := s.statusLineWith("空闲"); !strings.Contains(line, "待发") {
		t.Errorf("条目 7:Esc 后队列被清空(应保留供取回);空闲行=%q", line)
	}
	// Alt+Up 取回(kitty 变体为主,xterm 变体兜底)。
	// 轮询窗口 6s:UI 循环在 Esc 取消后可能仍在同步收尾(命令跑在 UI 循环内),
	// 全量套件并发负载下实测会超过 3s —— 单跑 3/3 通过、全量偶发假阴,故放宽等待而非改产品。
	s.send("\x1b[1;3A")
	queueGone := false
	t0 := time.Now()
	for i := 0; i < 24 && !queueGone; i++ {
		time.Sleep(250 * time.Millisecond)
		queueGone = !strings.Contains(s.statusLineWith("空闲"), "待发")
	}
	if !queueGone {
		s.send("\x1b\x1b[A")
		for i := 0; i < 24 && !queueGone; i++ {
			time.Sleep(250 * time.Millisecond)
			queueGone = !strings.Contains(s.statusLineWith("空闲"), "待发")
		}
	}
	t.Logf("条目 7:取回后待发计数消失=%v(耗时 %v);空闲行=%q", queueGone, time.Since(t0).Round(time.Millisecond), s.statusLineWith("空闲"))
	if !queueGone {
		t.Errorf("条目 7:Alt+Up 未取回队列(实时状态栏仍有待发计数)")
	}
	// 取回的文本在输入行:直接回车应作为用户消息发出
	s.send("\r")
	if !s.waitRaw("❯ 排队二", 25*time.Second) {
		t.Errorf("条目 7:取回后未能作为用户消息发出;尾部 %s", stripANSI(tailS(s.rawText(), 400)))
	}
}

// TestTUIAcceptQueueClearedOnSessionSwitch 条目 8:切会话丢弃旧队列(防错发到新会话)。
func TestTUIAcceptQueueClearedOnSessionSwitch(t *testing.T) {
	base := slowProvider(t, 4*time.Second)
	bin, env, _ := tuiAcceptSetupSlow(t, base)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("第一问\r")
	if !s.waitRaw("❯ 第一问", 8*time.Second) {
		t.Fatal("首轮未提交")
	}
	time.Sleep(600 * time.Millisecond)
	s.send("排队三\r")
	if !s.waitRaw("待发 1", 6*time.Second) {
		t.Fatalf("条目 8:入队未生效")
	}
	s.send("/session new\r") // 切会话(与 /session switch 同走 ClearQueue)
	// 慢端点这一轮结束 + 一段时间后,排队消息**不应**被发出
	time.Sleep(9 * time.Second)
	if strings.Contains(s.rawText(), "❯ 排队三") {
		t.Errorf("条目 8:切会话后仍把旧队列消息发到了新会话")
	}
	t.Logf("条目 8:尾部 %s", stripANSI(tailS(s.rawText(), 300)))
}
