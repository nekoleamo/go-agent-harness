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

// TestParseTUIAnswer 提问作答解析:编号/值/说明、多选、自由文本、非法。
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

// TestAppPresentQuestion 提问待答登记 → answerQuestion 定向回填 → 通道收到;cancel 清理。
func TestAppPresentQuestion(t *testing.T) {
	a := commandTestApp()
	ch, cancel, err := a.PresentQuestion(context.Background(), sdk.Question{ID: "q1", Prompt: "选环境"})
	if err != nil {
		t.Fatal(err)
	}
	a.askMu.Lock()
	n := len(a.qPend)
	a.askMu.Unlock()
	if n != 1 {
		t.Fatalf("应登记 1 个待答提问,got %d", n)
	}
	a.answerQuestion("q1", sdk.QuestionAnswer{Values: []string{"prod"}})
	select {
	case ans := <-ch:
		if len(ans.Values) != 1 || ans.Values[0] != "prod" {
			t.Fatalf("作答不符: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("作答未回填")
	}
	// cancel 幂等(已回填后清理无副作用)
	cancel()
	a.askMu.Lock()
	n = len(a.qPend)
	a.askMu.Unlock()
	if n != 0 {
		t.Fatalf("cancel 后应清理,got %d", n)
	}
}

// TestAnswerQuestionRoutesByID S-P0-2:多问并存时作答按 id 定向(不误答其他提问)。
func TestAnswerQuestionRoutesByID(t *testing.T) {
	a := commandTestApp()
	ch1, _, err := a.PresentQuestion(context.Background(), sdk.Question{ID: "q1", Prompt: "一问"})
	if err != nil {
		t.Fatal(err)
	}
	ch2, _, err := a.PresentQuestion(context.Background(), sdk.Question{ID: "q2", Prompt: "二问"})
	if err != nil {
		t.Fatal(err)
	}
	a.answerQuestion("q2", sdk.QuestionAnswer{Text: "答二"})
	select {
	case ans := <-ch2:
		if ans.Text != "答二" {
			t.Fatalf("q2 作答不符: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("q2 未回填")
	}
	select { // q1 不得提前收到作答
	case ans := <-ch1:
		t.Fatalf("q1 不应被误答: %+v", ans)
	case <-time.After(50 * time.Millisecond):
	}
	a.askMu.Lock()
	rest := len(a.qPend)
	a.askMu.Unlock()
	if rest != 1 {
		t.Fatalf("定向回填后应仅剩 q1,got %d", rest)
	}
}

// TestAppPresentQuestionNoID 无 id 提问(直调 presenter):本地兜底编号 → 仍可定向回填。
func TestAppPresentQuestionNoID(t *testing.T) {
	a := commandTestApp()
	ch, _, err := a.PresentQuestion(context.Background(), sdk.Question{Prompt: "无 id"})
	if err != nil {
		t.Fatal(err)
	}
	a.askMu.Lock()
	id := a.qPend[0].id
	a.askMu.Unlock()
	if id == "" {
		t.Fatal("无 id 提问应生成兜底编号")
	}
	a.answerQuestion(id, sdk.QuestionAnswer{Text: "ok"})
	select {
	case ans := <-ch:
		if ans.Text != "ok" {
			t.Fatalf("作答不符: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("未回填")
	}
}

// TestModelSubmitQuestionAnswer 作答态下输入作为作答(不发起回合);Esc 退出后输入回归普通消息。
func TestModelSubmitQuestionAnswer(t *testing.T) {
	a := commandTestApp()
	got := make(chan sdk.QuestionAnswer, 1)
	a.model.onQuestion = func(_ string, ans sdk.QuestionAnswer) { got <- ans }
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q1", Prompt: "部署到哪?", Options: []sdk.QuestionOption{{Value: "dev", Desc: "开发"}, {Value: "prod", Desc: "生产"}}})
	if !a.model.state.Answering {
		t.Fatal("输入框为空时到达提问应自动进入作答态")
	}
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
	if a.model.state.ActiveQuestion() != nil {
		t.Fatal("作答后应出栈")
	}
	// 无法识别:提示且保留待答态
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q2", Prompt: "再选", Options: []sdk.QuestionOption{{Value: "x"}}})
	a.model.state.Input = "乱输入"
	a.model.submit()
	if a.model.state.ActiveQuestion() == nil {
		t.Fatal("无法识别时应保留待答态")
	}
}

// TestQuestionStackDraftNotStolen S-P0-2:输入框有草稿时提问到达不劫持输入(草稿可照常发出)。
func TestQuestionStackDraftNotStolen(t *testing.T) {
	a := commandTestApp()
	sent := make(chan string, 1)
	a.model.onSubmit = func(s string) { sent <- s }
	a.model.state.Input = "草稿:先问这个"
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q1", Prompt: "选环境", Options: []sdk.QuestionOption{{Value: "a"}}})
	if a.model.state.Answering {
		t.Fatal("有草稿时不应抢占输入框进入作答态")
	}
	if n := len(a.model.state.Questions); n != 1 {
		t.Fatalf("提问应入栈等待,got %d", n)
	}
	a.model.submit() // 草稿照常作为回合消息发出
	select {
	case s := <-sent:
		if s != "草稿:先问这个" {
			t.Fatalf("草稿应原样发出,got %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("草稿未发出(被提问劫持)")
	}
	if a.model.state.ActiveQuestion() == nil {
		t.Fatal("提问应仍在栈内(等待 /answer)")
	}
}

// TestQuestionStackFIFO S-P0-2:多问入栈不互相覆盖,按到达顺序逐个作答。
func TestQuestionStackFIFO(t *testing.T) {
	a := commandTestApp()
	var got []string
	a.model.onQuestion = func(id string, _ sdk.QuestionAnswer) { got = append(got, id) }
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q1", Prompt: "一问", Options: []sdk.QuestionOption{{Value: "a"}}})
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q2", Prompt: "二问", Options: []sdk.QuestionOption{{Value: "b"}}})
	if n := len(a.model.state.Questions); n != 2 {
		t.Fatalf("两问应并存,got %d", n)
	}
	if p := a.model.state.ActiveQuestion(); p == nil || p.ID != "q1" {
		t.Fatalf("栈首应为最早到达的 q1: %+v", p)
	}
	a.model.state.Input = "1"
	a.model.submit() // 答 q1
	a.model.state.Input = "1"
	a.model.submit() // 答 q2
	if len(got) != 2 || got[0] != "q1" || got[1] != "q2" {
		t.Fatalf("应按到达顺序逐个作答,got %v", got)
	}
	if n := len(a.model.state.Questions); n != 0 {
		t.Fatalf("答完应清空栈,got %d", n)
	}
	if a.model.state.Answering {
		t.Fatal("栈空应退出作答态")
	}
}

// TestAnswerCommand /answer:无参进入作答态、编号/内容/skip 作答、无待答报错。
func TestAnswerCommand(t *testing.T) {
	a := commandTestApp()
	if msg, err := a.cmdAnswer(nil); err != nil || msg == "" {
		t.Fatalf("无待答提问应给提示(非错误),got %q %v", msg, err)
	}
	got := make(chan sdk.QuestionAnswer, 1)
	a.model.onQuestion = func(_ string, ans sdk.QuestionAnswer) { got <- ans }
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q1", Prompt: "选", Options: []sdk.QuestionOption{{Value: "a", Desc: "甲"}, {Value: "b", Desc: "乙"}}})
	if _, err := a.cmdAnswer(nil); err != nil {
		t.Fatalf("/answer 无参应进入作答态: %v", err)
	}
	if !a.model.state.Answering {
		t.Fatal("应处于作答态")
	}
	if _, err := a.cmdAnswer([]string{"2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ans := <-got:
		if len(ans.Values) != 1 || ans.Values[0] != "b" {
			t.Fatalf("编号 2 应解析为 b: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("未回填")
	}
	// skip:回填空作答(不强迫作答)
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q2", Prompt: "再选"})
	if _, err := a.cmdAnswer([]string{"skip"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ans := <-got:
		if !ans.Empty() {
			t.Fatalf("skip 应回填空作答: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("skip 未回填")
	}
	// 无法识别作答:报错且不出栈
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q3", Prompt: "三", Options: []sdk.QuestionOption{{Value: "x"}}})
	if _, err := a.cmdAnswer([]string{"乱"}); err == nil {
		t.Fatal("不允许自由文本时应报错")
	}
	if a.model.state.ActiveQuestion() == nil {
		t.Fatal("报错时提问应保留")
	}
}

// TestAnswerOptions 选择器选项:编号 + 跳过;无待答返回空。
func TestAnswerOptions(t *testing.T) {
	a := commandTestApp()
	if opts := a.answerOptions(nil); len(opts) != 0 {
		t.Fatalf("无待答应无选项,got %v", opts)
	}
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q1", Prompt: "选", Options: []sdk.QuestionOption{{Value: "a", Desc: "甲"}, {Value: "b"}}})
	opts := a.answerOptions(nil)
	if len(opts) != 3 || opts[0].Value != "1" || opts[0].Desc != "甲" || opts[1].Desc != "b" || opts[2].Value != answerSkipValue {
		t.Fatalf("选项不符: %+v", opts)
	}
}

// G-E5-4:交互审计行(interactionMsg → meta 行;NoteInteraction 无 program 时安全忽略)。
func TestInteractionMsgAppendsMetaLine(t *testing.T) {
	a := commandTestApp()
	a.model.Update(interactionMsg{text: "提问已由其它渠道(web)处理: prod"})
	lines := a.model.state.Lines
	if len(lines) == 0 {
		t.Fatal("应有审计行")
	}
	last := lines[len(lines)-1]
	if last.Kind != "meta" || last.Text != "提问已由其它渠道(web)处理: prod" {
		t.Fatalf("审计行异常: %+v", last)
	}
	// 空文本不入流
	n := len(lines)
	a.model.Update(interactionMsg{text: ""})
	if len(a.model.state.Lines) != n {
		t.Fatal("空文本不应追加行")
	}
	// 未启动(program=nil)时 NoteInteraction 安全忽略
	a.NoteInteraction("x")
	if len(a.model.state.Lines) != n {
		t.Fatal("未启动时 NoteInteraction 不应入流")
	}
}
