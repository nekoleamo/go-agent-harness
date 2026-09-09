// im 桥单元测试(同包):gate 三态 / 去重 / 确认回填 / busy / /stop / 输出聚合。
// 回合引擎用 stubLoop + 真 sessionlog(输出聚合走真实 Replay);真实 agent 链在 tests/im_e2e_test.go。
package im

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubLoop 可编程回合引擎(onRun 注入行为)。
type stubLoop struct {
	mu     sync.Mutex
	inputs []string
	onRun  func(ctx context.Context, input string) error
}

func (l *stubLoop) Run(ctx context.Context, input string) error {
	l.mu.Lock()
	l.inputs = append(l.inputs, input)
	on := l.onRun
	l.mu.Unlock()
	if on != nil {
		return on(ctx, input)
	}
	return nil
}

// stubTransport 记录出站消息。
type stubTransport struct {
	mu    sync.Mutex
	sends []string
}

func (t *stubTransport) Name() string { return "mock" }
func (t *stubTransport) SendText(_ context.Context, _ Route, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sends = append(t.sends, text)
	return nil
}

func (t *stubTransport) sent() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.sends))
	copy(out, t.sends)
	return out
}

// stubTurn 记录 Cancel 调用。
type stubTurn struct {
	mu    sync.Mutex
	calls int
}

func (t *stubTurn) Running() bool { return false }
func (t *stubTurn) Cancel() {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
}

// buildTestBridge 装配真 sessionlog + stub transport;opt 覆写。
func buildTestBridge(t *testing.T, opt Options) (*Bridge, *stubLoop, *stubTransport, sdk.SessionLog) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	loop := &stubLoop{}
	tr := &stubTransport{}
	b := New(nil, loop, sessions, tr, opt)
	return b, loop, tr, sessions
}

func mkRoute(user string) Route {
	return Route{Channel: "mock", UserID: user, ChatID: user}
}

// finishText onRun 助手:回合"产生"一条 assistant 文本(聚合走真实 Replay)。
func finishText(sessions sdk.SessionLog, text string) func(context.Context, string) error {
	return func(_ context.Context, _ string) error {
		return sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: text}})
	}
}

// TestGateSilentDrop disabled 默认:未授权消息静默丢弃(不回、不开回合)。
func TestGateSilentDrop(t *testing.T) {
	b, loop, tr, _ := buildTestBridge(t, Options{Mode: AccessDisabled})
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("stranger"), MsgID: "1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if n := len(tr.sent()); n != 0 {
		t.Fatalf("disabled 模式应静默,发送了 %d 条", n)
	}
	loop.mu.Lock()
	defer loop.mu.Unlock()
	if len(loop.inputs) != 0 {
		t.Fatal("不应开回合")
	}
}

// TestAllowlistAggregate allowlist 放行 → 回合 → 聚合最终 assistant 文本回推。
func TestAllowlistAggregate(t *testing.T) {
	b, loop, tr, sessions := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	loop.onRun = finishText(sessions, "你好,我是 gah。")
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) != 1 || sent[0] != "你好,我是 gah。" {
		t.Fatalf("应回推聚合文本: %+v", sent)
	}
}

// TestBusyReply 忙时普通消息回提示(不排队、不开第二回合)。
func TestBusyReply(t *testing.T) {
	b, _, tr, sessions := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	b.loop = &stubLoop{onRun: func(ctx context.Context, _ string) error {
		once.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		_ = sessions // 回合挂起不产出
		return nil
	}}
	done := make(chan error, 1)
	go func() {
		done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "任务A"})
	}()
	<-entered
	// 第二用户消息 → busy 提示
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "任务B"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) < 1 || !strings.Contains(sent[0], "请稍候") {
		t.Fatalf("应回忙提示: %+v", sent)
	}
	// 回合随后结束(无产出)→ 完成占位(第二条)
	if len(sent) < 2 || !strings.Contains(sent[len(sent)-1], "完成") {
		t.Fatalf("回合结束后应回完成占位: %+v", sent)
	}
}

// typingTransport 实现 TypingAware 的记录 transport(断言 show/stop 时序)。
type typingTransport struct {
	stub *stubTransport
	mu   sync.Mutex
	seq  []string
}

func (t *typingTransport) Name() string { return "typing" }
func (t *typingTransport) SendText(ctx context.Context, r Route, text string) error {
	return t.stub.SendText(ctx, r, text)
}
func (t *typingTransport) ShowTyping(context.Context, Route) error {
	t.mu.Lock()
	t.seq = append(t.seq, "show")
	t.mu.Unlock()
	return nil
}
func (t *typingTransport) StopTyping(context.Context, Route) error {
	t.mu.Lock()
	t.seq = append(t.seq, "stop")
	t.mu.Unlock()
	return nil
}
func (t *typingTransport) typedSeq() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.seq))
	copy(out, t.seq)
	return out
}

// TestTurnTypingIndicator typing 指示:回合开始 show、结束 stop(成功与错误路径均 stop)。
// 长回合期间用户凭“正在输入”判断仍工作 vs 断联(真机反馈)。
func TestTurnTypingIndicator(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{"成功路径", false},
		{"错误路径", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, loop, _, sessions := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
			tt := &typingTransport{stub: &stubTransport{}}
			b.tr = tt
			loop.onRun = func(_ context.Context, _ string) error {
				if tc.fail {
					return errors.New("boom")
				}
				return sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "结果"}})
			}
			if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "hi"}); tc.fail {
				if err == nil {
					t.Fatal("错误路径应返回回合错误")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			seq := tt.typedSeq()
			if len(seq) != 2 || seq[0] != "show" || seq[1] != "stop" {
				t.Fatalf("typing 时序应 show→stop,got %v", seq)
			}
			// 成功路径有回复;错误路径有错误提示——均发生在 stop 前(best-effort 不影响主流程)
			if n := len(tt.stub.sent()); n != 1 {
				t.Fatalf("应有一条出站消息,got %v", tt.stub.sent())
			}
		})
	}
}

// TestConfirmApproveAndReject IM 确认闭环:回合内审批 → 推确认 → 用户回 y/n → 回填。
func TestConfirmApproveAndReject(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  bool
	}{
		{"approve-y", "y", true},
		{"approve-yes", "yes", true},
		{"approve-批准", "批准", true},
		{"reject-n", "n", false},
		{"reject-拒绝", "拒绝", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, loop, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
			result := make(chan bool, 1)
			loop.onRun = func(ctx context.Context, _ string) error {
				ok, err := b.Confirm(ctx, "确认执行危险命令?")
				if err != nil {
					return err
				}
				result <- ok
				return nil
			}
			done := make(chan error, 1)
			go func() {
				done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "跑一下"})
			}()
			// 等待确认消息推送,再以用户身份回答
			deadline := time.Now().Add(3 * time.Second)
			for len(tr.sent()) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: tc.reply}); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got := <-result; got != tc.want {
				t.Fatalf("确认结果 = %v,want %v", got, tc.want)
			}
		})
	}
}

// TestConfirmUnknownReply 未识别回答 → 提示继续等待(不消费回合)。
func TestConfirmUnknownReply(t *testing.T) {
	b, loop, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	result := make(chan bool, 1)
	loop.onRun = func(ctx context.Context, _ string) error {
		ok, err := b.Confirm(ctx, "确认?")
		if err != nil {
			return err
		}
		result <- ok
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "go"})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(tr.sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "?"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) < 2 || !strings.Contains(sent[len(sent)-1], "请回复 y 批准") {
		t.Fatalf("未识别回答应提示继续等待: %+v", sent)
	}
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "3", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := <-result; !got {
		t.Fatal("未识别回答后应仍能批准")
	}
}

// TestStopCancelsBusyTurn 回合中 /stop → ctx.turnControl.Cancel 被调;/stop 转发的取消语义
// 由 host-agent-loop TestTurnControlCancel 覆盖,此处验证桥的转发与取消提示。
func TestStopCancelsBusyTurn(t *testing.T) {
	b, _, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	turn := &stubTurn{}
	b.SetTurnControl(turn)
	entered := make(chan struct{})
	release := make(chan struct{})
	b.loop = &stubLoop{onRun: func(_ context.Context, _ string) error {
		close(entered)
		<-release // 回合挂起;release = 取消已生效的回合返回
		return context.Canceled
	}}
	done := make(chan error, 1)
	go func() {
		done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "慢任务"})
	}()
	<-entered
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "/stop"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("回合应被取消返回错误")
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.calls != 1 {
		t.Fatalf("Cancel 应被调 1 次,got %d", turn.calls)
	}
	sent := tr.sent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "已取消") {
		t.Fatalf("应回取消提示: %+v", sent)
	}
}

// TestPairingFlow pairing 模式:陌生用户收到配对码 → 主机批准 → 放行。
func TestPairingFlow(t *testing.T) {
	b, loop, tr, sessions := buildTestBridge(t, Options{Mode: AccessPairing})
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("newbie"), MsgID: "1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "配对码") {
		t.Fatalf("pairing 应回配对提示: %+v", sent)
	}
	// 提取配对码并批准
	code := ""
	for _, f := range strings.Fields(sent[0]) {
		if len(f) == 6 && strings.Trim(f, "0123456789abcdef") == "" {
			code = f
			break
		}
	}
	if code == "" {
		t.Fatalf("未能从提示提取配对码: %q", sent[0])
	}
	if !b.acc.ApprovePair(code) {
		t.Fatal("批准应成功")
	}
	// 批准后放行
	loop.onRun = finishText(sessions, "ok")
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("newbie"), MsgID: "2", Text: "再来"}); err != nil {
		t.Fatal(err)
	}
	if n := len(tr.sent()); n != 2 {
		t.Fatalf("批准后应开回合并回复: %+v", tr.sent())
	}
}

// TestDedupRepeatedMsgID 同一通道消息 id 只处理一次。
func TestDedupRepeatedMsgID(t *testing.T) {
	b, loop, tr, sessions := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	loop.onRun = finishText(sessions, "回复1")
	in := Inbound{Route: mkRoute("owner"), MsgID: "dup-1", Text: "hi"}
	if err := b.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	loop.mu.Lock()
	defer loop.mu.Unlock()
	if len(loop.inputs) != 1 {
		t.Fatalf("重复 id 应只开一次回合,got %d", len(loop.inputs))
	}
	if len(tr.sent()) != 1 {
		t.Fatalf("重复 id 应只回一次: %+v", tr.sent())
	}
}

// TestCommandImStatus /im status 命令可用(宿主 ctx.commands 未装配也自处理)。
func TestCommandImStatus(t *testing.T) {
	b, _, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "/im status"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "模式=allowlist") {
		t.Fatalf("应回 im 状态: %+v", sent)
	}
}

// TestParseConfirmReply 词表覆盖。
func TestParseConfirmReply(t *testing.T) {
	cases := []struct {
		text  string
		ok    bool
		known bool
	}{
		{"y", true, true}, {"Y", true, true}, {"yes", true, true}, {"ok", true, true},
		{"是", true, true}, {"批准", true, true}, {"同意", true, true},
		{"n", false, true}, {"no", false, true}, {"拒绝", false, true}, {"取消", false, true},
		{"随便", false, false}, {"", false, false}, {"在吗", false, false},
	}
	for _, c := range cases {
		ok, known := parseConfirmReply(c.text)
		if ok != c.ok || known != c.known {
			t.Errorf("parseConfirmReply(%q) = (%v,%v),want (%v,%v)", c.text, ok, known, c.ok, c.known)
		}
	}
}
