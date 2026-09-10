// TUI 融合 Present 单测(P3):多待答通道广播 + cancel 幂等 + Confirm 走 Present。
package tui

import (
	"context"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestAppPresentBroadcast Present 登记待答 → confirmResult 广播全部通道。
func TestAppPresentBroadcast(t *testing.T) {
	a := commandTestApp()
	ch1, c1, err := a.Present(context.Background(), "确认1")
	if err != nil {
		t.Fatal(err)
	}
	defer c1()
	ch2, c2, err := a.Present(context.Background(), "确认2")
	if err != nil {
		t.Fatal(err)
	}
	defer c2()
	a.confirmResult(true)
	for i, ch := range []<-chan bool{ch1, ch2} {
		select {
		case ok := <-ch:
			if !ok {
				t.Fatalf("通道 %d 应收到批准", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("通道 %d 未收到应答(广播失效)", i)
		}
	}
	// 广播后 pending 清空(cancel 幂等无副作用)
	a.pendMu.Lock()
	n := len(a.pending)
	a.pendMu.Unlock()
	if n != 0 {
		t.Fatalf("应答后 pending 应清空,got %d", n)
	}
	c1()
	c2()
}

// TestAppConfirmViaPresenter Confirm 经 Present 路径(融合改造后)仍可用。
func TestAppConfirmViaPresenter(t *testing.T) {
	a := commandTestApp()
	done := make(chan bool, 1)
	go func() {
		ok, err := a.Confirm(context.Background(), "危险操作")
		if err != nil {
			done <- false
			return
		}
		done <- ok
	}()
	// 等 Present 登记(Confirm 内),再回填拒绝
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.pendMu.Lock()
		n := len(a.pending)
		a.pendMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Confirm 未登记待答")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.confirmResult(false)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("应返回拒绝")
		}
	case <-time.After(time.Second):
		t.Fatal("Confirm 未返回")
	}
}

// TestAppPresentCancel 无应答时 cancel 清理待答(超时/放弃路径)。
func TestAppPresentCancel(t *testing.T) {
	a := commandTestApp()
	_, cancel, err := a.Present(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	a.pendMu.Lock()
	n := len(a.pending)
	a.pendMu.Unlock()
	if n != 1 {
		t.Fatalf("Present 后应 1 个待答,got %d", n)
	}
	cancel()
	a.pendMu.Lock()
	n = len(a.pending)
	a.pendMu.Unlock()
	if n != 0 {
		t.Fatalf("cancel 后待答应清理,got %d", n)
	}
}

// TestParseTUIAnswer 提问作答解析(与 IM 侧同语义):编号/值/说明、多选、自由文本、非法。
func TestParseTUIAnswer(t *testing.T) {
	q := sdk.Question{Prompt: "选", Options: []sdk.QuestionOption{{Value: "a", Desc: "甲"}, {Value: "b", Desc: "乙"}}}
	if ans, ok := parseTUIAnswer(q, "2"); !ok || len(ans.Values) != 1 || ans.Values[0] != "b" {
		t.Fatalf("编号解析失败: %+v %v", ans, ok)
	}
	if ans, ok := parseTUIAnswer(q, "甲"); !ok || ans.Values[0] != "a" {
		t.Fatalf("说明匹配失败: %+v %v", ans, ok)
	}
	if _, ok := parseTUIAnswer(q, "随便"); ok {
		t.Fatal("不允许自由文本时应不可用")
	}
	multi := sdk.Question{Prompt: "选", Multiple: true, Options: q.Options}
	if ans, ok := parseTUIAnswer(multi, "1,2"); !ok || len(ans.Values) != 2 {
		t.Fatalf("多选解析失败: %+v %v", ans, ok)
	}
	free := sdk.Question{Prompt: "写", FreeText: true, Options: q.Options}
	if ans, ok := parseTUIAnswer(free, "自定义"); !ok || ans.Text != "自定义" {
		t.Fatalf("自由文本失败: %+v %v", ans, ok)
	}
	plain := sdk.Question{Prompt: "名字?"}
	if ans, ok := parseTUIAnswer(plain, "阿黄"); !ok || ans.Text != "阿黄" {
		t.Fatalf("无选项提问失败: %+v %v", ans, ok)
	}
}

// TestAppPresentQuestion 提问待答登记 → answerQuestion 广播 → 通道收到;cancel 清理。
func TestAppPresentQuestion(t *testing.T) {
	a := commandTestApp()
	ch, cancel, err := a.PresentQuestion(context.Background(), sdk.Question{Prompt: "选环境"})
	if err != nil {
		t.Fatal(err)
	}
	a.askMu.Lock()
	n := len(a.qPend)
	a.askMu.Unlock()
	if n != 1 {
		t.Fatalf("应登记 1 个待答提问,got %d", n)
	}
	a.answerQuestion(sdk.QuestionAnswer{Values: []string{"prod"}})
	select {
	case ans := <-ch:
		if len(ans.Values) != 1 || ans.Values[0] != "prod" {
			t.Fatalf("作答不符: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("作答未广播")
	}
	// cancel 幂等(已广播后清理无副作用)
	cancel()
	a.askMu.Lock()
	n = len(a.qPend)
	a.askMu.Unlock()
	if n != 0 {
		t.Fatalf("cancel 后应清理,got %d", n)
	}
}

// TestModelSubmitQuestionAnswer 提交拦截:待答提问时输入作为作答,不发起回合。
func TestModelSubmitQuestionAnswer(t *testing.T) {
	a := commandTestApp()
	got := make(chan sdk.QuestionAnswer, 1)
	a.model.onQuestion = func(ans sdk.QuestionAnswer) { got <- ans }
	a.model.state.ApplyQuestionPrompt(sdk.Question{Prompt: "部署到哪?", Options: []sdk.QuestionOption{{Value: "dev", Desc: "开发"}, {Value: "prod", Desc: "生产"}}})
	a.model.state.Input = "2"
	a.model.submit()
	select {
	case ans := <-got:
		if len(ans.Values) != 1 || ans.Values[0] != "prod" {
			t.Fatalf("应解析编号 2 → prod: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("作答未回填")
	}
	if a.model.state.PendingQuestion != nil {
		t.Fatal("作答后应清除待答态")
	}
	// 无法识别:提示且保留待答态
	a.model.state.ApplyQuestionPrompt(sdk.Question{Prompt: "再选", Options: []sdk.QuestionOption{{Value: "x"}}})
	a.model.state.Input = "乱输入"
	a.model.submit()
	if a.model.state.PendingQuestion == nil {
		t.Fatal("无法识别时应保留待答态")
	}
}
