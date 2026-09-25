// A-1 条目 6/7/8 pty 真机验收:回合运行中 Enter 的两条落点。
//
// 语义(第五十八批):宿主提供转向能力时,回合中 Enter **注入当前回合**(「转向 N」,
// 模型下一次请求就能看到);无法注入时回落排队(「待发 N」,回合结束后续发)。
// 回合结束时仍未注入的插话由宿主 agent/steer-dropped 回吐 → TUI 转为待发。
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
	"regexp"
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

// TestTUIAcceptSteerDuringTurn 条目 6:回合中输入 →「转向 N」→ 消息注入**本回合**
// (作为用户消息落账并出现在会话流),不是排到回合结束后另起一回合。
func TestTUIAcceptSteerDuringTurn(t *testing.T) {
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
	s.send("插话一\r")
	if !s.waitRaw("转向 1", 6*time.Second) {
		t.Errorf("条目 6:回合中插话未显示「转向 1」;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
	// 注入即落账(不变量:模型可见即已记录):插话作为用户消息出现在会话流。
	if !s.waitRaw("❯ 插话一", 25*time.Second) {
		t.Errorf("条目 6:插话未注入当前回合;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
}

// TestTUIAcceptQueueRecall 条目 7:回合中插话后 Esc 取消 → 未注入的插话**回吐**为待发
// (不静默丢,也不冒充历史),Alt+Up 取回可继续编辑并重发。
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
	s.send("插入三\r")
	if !s.waitRaw("转向 1", 6*time.Second) {
		t.Fatalf("条目 7:插话未生效")
	}
	s.send("\x1b") // Esc 取消:插话尚未注入(慢端点还没回)→ 回吐为待发
	if !s.waitRaw("已转为待发", 10*time.Second) {
		t.Fatalf("条目 7:未回吐未注入的插话;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
	if !s.waitScreen("空闲", 10*time.Second) {
		t.Fatalf("条目 7:Esc 未结束回合;屏 %q", firstN(s.screen(), 300))
	}
	// 队列保留:判**状态栏的计数**。三个坑都会让断言骗人(2026-09-25 本地与 CI 同时
	// 红在这句):
	//   ① 状态栏按段渲染,pty 宽度不够时「待发」会落到「空闲」以外的行 —— 查单行会漏判;
	//   ② 回吐提示行本身就含「已转为待发(Alt+Up 取回)」,只查「待发」两字会被它骗成通过;
	//   ③ 回吐事件先出提示行,状态栏那一帧可能还没画出来,必须等而不是立即断言。
	// 所以用「待发 + 数字」匹配(提示行是「已转为待发(」,不匹配),并轮询等它出现。
	pendingCount := regexp.MustCompile(`待发 \d`)
	queued := false
	for i := 0; i < 40 && !queued; i++ {
		queued = pendingCount.MatchString(s.screen())
		if !queued {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !queued {
		t.Errorf("条目 7:Esc 后回吐消息未进待发队列;屏 %q", firstN(s.screen(), 400))
	}
	// Alt+Up 取回(kitty 变体为主,xterm 变体兜底)。
	// 轮询窗口 6s:UI 循环在 Esc 取消后可能仍在同步收尾(命令跑在 UI 循环内),
	// 全量套件并发负载下实测会超过 3s —— 单跑 3/3 通过、全量偶发假阴,故放宽等待而非改产品。
	s.send("\x1b[1;3A")
	queueGone := false
	t0 := time.Now()
	for i := 0; i < 24 && !queueGone; i++ {
		time.Sleep(250 * time.Millisecond)
		queueGone = !pendingCount.MatchString(s.screen())
	}
	if !queueGone {
		s.send("\x1b\x1b[A")
		for i := 0; i < 24 && !queueGone; i++ {
			time.Sleep(250 * time.Millisecond)
			queueGone = !pendingCount.MatchString(s.screen())
		}
	}
	t.Logf("条目 7:取回后待发计数消失=%v(耗时 %v);屏尾=%q", queueGone, time.Since(t0).Round(time.Millisecond), firstN(s.screen(), 300))
	if !queueGone {
		t.Errorf("条目 7:Alt+Up 未取回队列(实时状态栏仍有待发计数)")
	}
	// 取回的文本在输入行:直接回车应作为用户消息发出
	s.send("\r")
	if !s.waitRaw("❯ 插入三", 25*time.Second) {
		t.Errorf("条目 7:取回后未能作为用户消息发出;尾部 %s", stripANSI(tailS(s.rawText(), 400)))
	}
}

// TestTUIAcceptQueueClearedOnSessionSwitch 条目 8:切会话丢弃旧队列(防错发到新会话)。
// 队列经「插话 + Esc 回吐」产生(与条目 7 同路)。
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
	s.send("插入四\r")
	if !s.waitRaw("转向 1", 6*time.Second) {
		t.Fatalf("条目 8:插话未生效")
	}
	s.send("\x1b")
	if !s.waitRaw("已转为待发", 10*time.Second) {
		t.Fatalf("条目 8:未回吐未注入的插话;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
	if !s.waitScreen("空闲", 10*time.Second) {
		t.Fatalf("条目 8:Esc 未结束回合;屏 %q", firstN(s.screen(), 300))
	}
	s.send("/session new\r") // 切会话(与 /session switch 同走 ClearQueue)
	// 慢端点这一轮结束 + 一段时间后,队列消息**不应**被发出
	time.Sleep(9 * time.Second)
	if strings.Contains(s.rawText(), "❯ 插入四") {
		t.Errorf("条目 8:切会话后仍把旧队列消息发到了新会话")
	}
	t.Logf("条目 8:尾部 %s", stripANSI(tailS(s.rawText(), 300)))
}
