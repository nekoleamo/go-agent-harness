// im 桥单元测试(同包):gate 三态 / 去重 / 确认回填 / busy / /stop / 输出聚合。
// 回合引擎用 stubLoop + 真 sessionlog(输出聚合走真实 Replay);真实 agent 链在 tests/im_e2e_test.go。
package im

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
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

// TestBusyQueue P1 忙时队列:回合进行中消息入队(回提示)→ 完成自动续跑;
// 队列满(已有 1 条)时再来的消息回"忙"提示(不丢不炸)。
func TestBusyQueue(t *testing.T) {
	b, _, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	loop := &stubLoop{}
	b.loop = loop
	loop.onRun = func(ctx context.Context, _ string) error {
		once.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		return nil // 回合无产出 → 收尾回"✅ 完成"占位
	}
	done := make(chan error, 1)
	go func() {
		done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "任务A"})
	}()
	<-entered
	// 忙时第二条 → 入队(回提示);第三条 → 队列满回"忙"
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "任务B"}); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "3", Text: "任务C"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) < 1 || !strings.Contains(sent[0], "加入队列") {
		t.Fatalf("忙时应回入队提示: %+v", sent)
	}
	if len(sent) < 2 || !strings.Contains(sent[1], "请稍候") {
		t.Fatalf("队列满应回忙提示: %+v", sent)
	}
	// 释放:回合A 完成 → 自动续跑回合B;任务C 不入回合
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		loop.mu.Lock()
		n := len(loop.inputs)
		loop.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列续跑超时: inputs=%d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	loop.mu.Lock()
	defer loop.mu.Unlock()
	if len(loop.inputs) != 2 || loop.inputs[0] != "任务A" || loop.inputs[1] != "任务B" {
		t.Fatalf("应依次跑 A/B,任务C 不排队: %+v", loop.inputs)
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

// reset 清空已记录出站(供分段断言)。
func (t *stubTransport) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sends = nil
}

// buildBridgeP1 装配真 sessionlog + host-cwd-sessions(会话绑定命令面测试底座;
// host-cwd-sessions 启动即新开会话——与宿主语义一致)。bindPath 空 = 绑定不落盘。
func buildBridgeP1(t *testing.T, bindPath string) (*Bridge, *stubLoop, *stubTransport, sdk.CwdSessions, sdk.SessionLog) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostcwdsessions.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	var cwd sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cwd); err != nil {
		t.Fatal(err)
	}
	loop := &stubLoop{}
	tr := &stubTransport{}
	b := New(c, loop, sessions, tr, Options{
		Mode:            AccessAllowlist,
		Allow:           []string{"mock\x00owner"},
		SessionBindPath: bindPath,
	})
	return b, loop, tr, cwd, sessions
}

// TestSessionCommandsP1 会话绑定命令面:/new 新建绑定 → 回合落新会话 →
// /history 回读绑定会话 → /session main 解绑回主 → 会话隔离。
func TestSessionCommandsP1(t *testing.T) {
	b, loop, tr, cwd, sessions := buildBridgeP1(t, "")
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "/new"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "已新建会话并绑定") {
		t.Fatalf("/new 应回新建绑定提示: %+v", sent)
	}
	newID := cwd.CurrentSession()
	if newID == "" {
		t.Fatal("/new 后应处于新建会话")
	}
	// 绑定会话内回合:文本 → 回合产物写入绑定会话(user+assistant,对齐 agent-loop)
	loop.onRun = func(_ context.Context, in string) error {
		if err := sessions.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: in}}); err != nil {
			return err
		}
		return sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "回答1"}})
	}
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "第一次对话"}); err != nil {
		t.Fatal(err)
	}
	// /history 应回读绑定会话内容(❯ 用户 + 🤖 助手)
	tr.reset()
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "3", Text: "/history"}); err != nil {
		t.Fatal(err)
	}
	h := strings.Join(tr.sent(), "\n")
	if !strings.Contains(h, "❯ 第一次对话") || !strings.Contains(h, "🤖 回答1") {
		t.Fatalf("/history 应回读绑定会话: %+v", h)
	}
	// /sessionlist 含新会话与主会话
	tr.reset()
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "4", Text: "/sessionlist"}); err != nil {
		t.Fatal(err)
	}
	l := strings.Join(tr.sent(), "\n")
	if !strings.Contains(l, newID) || !strings.Contains(l, "/new") {
		t.Fatalf("/sessionlist 应含新建会话与绑定指引: %+v", l)
	}
	// /session main 解绑回主会话
	tr.reset()
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "5", Text: "/session main"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tr.sent(), "\n"), "回到主会话") {
		t.Fatalf("/session main 应解绑回主: %+v", tr.sent())
	}
	if cwd.CurrentSession() != "" {
		t.Fatalf("回主会话后 CurrentSession 应为空,got %q", cwd.CurrentSession())
	}
}

// TestSessionBindRestore 绑定持久化:新桥(同 bindPath 重启)按绑定恢复会话,
// 回合前 bindSession 把宿主当前会话切回绑定会话(覆盖启动即新建的宿主会话)。
func TestSessionBindRestore(t *testing.T) {
	bindPath := filepath.Join(t.TempDir(), "im-sessions.yaml")
	time.Sleep(1100 * time.Millisecond) // host 启动 New 与 /new 的 id 秒级时间戳,需跨秒才唯一
	b, _, _, cwd, _ := buildBridgeP1(t, bindPath)
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "/new"}); err != nil {
		t.Fatal(err)
	}
	wantID := cwd.CurrentSession()
	if wantID == "" {
		t.Fatal("/new 应返回会话 id")
	}
	// 重启:全新环境(新 GAH_HOME/新宿主,启动会话 = 新 id ≠ 绑定 id)
	b2, loop2, tr2, cwd2, sessions2 := buildBridgeP1(t, bindPath)
	loop2.onRun = finishText(sessions2, "恢复后回答")
	if err := b2.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "10", Text: "重启后的消息"}); err != nil {
		t.Fatal(err)
	}
	if got := cwd2.CurrentSession(); got != wantID {
		t.Fatalf("重启后回合应落到绑定会话 %q,got %q", wantID, got)
	}
	if !strings.Contains(strings.Join(tr2.sent(), "\n"), "恢复后回答") {
		t.Fatalf("绑定会话回合应正常回复: %+v", tr2.sent())
	}
}

// TestSessionBindStaleDeleted 绑定会话被宿主删除 → 回合前自动解绑回主会话
// (防 Open 对不存在文件误新建空会话;绑定记录同步清除)。
func TestSessionBindStaleDeleted(t *testing.T) {
	b, loop, tr, cwd, sessions := buildBridgeP1(t, "")
	time.Sleep(1100 * time.Millisecond) // /new 跨秒,避免与 host 启动会话同 id
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "/new"}); err != nil {
		t.Fatal(err)
	}
	bound := cwd.CurrentSession()
	if bound == "" {
		t.Fatal("/new 应返回会话 id")
	}
	// 跑一回合落盘(首次 Append 才建 jsonl,后续才能“被外部删除”)
	loop.onRun = finishText(sessions, "占位")
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "先落盘"}); err != nil {
		t.Fatal(err)
	}
	// 宿主侧删除绑定会话文件(外部清理语义;hostcwdsessions.Sessions 枚举磁盘 → 绑定失效)
	file := filepath.Join(hostcwdsessions.SessionsRoot(), cwd.Current()+"-"+bound+".jsonl")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// 下一条消息:bindSession 发现绑定失效 → 解绑,回合照常不崩
	loop.onRun = finishText(sessions, "删除后回答")
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "3", Text: "删除后消息"}); err != nil {
		t.Fatal(err)
	}
	if b.bind.Get(mkRoute("owner").Key()) != "" {
		t.Fatal("失效绑定应已清除(自动解绑回主)")
	}
	if !strings.Contains(strings.Join(tr.sent(), "\n"), "删除后回答") {
		t.Fatalf("解绑后回合应正常: %+v", tr.sent())
	}
}

// TestSessionSwitchBusyRejected 回合进行中 /session(切换类)被拒——防 agent-loop
// 写盘中途切会话致回合内事件错乱;只读 /status 仍可用。
func TestSessionSwitchBusyRejected(t *testing.T) {
	b, _, tr, cwd, _ := buildBridgeP1(t, "")
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "/new"}); err != nil {
		t.Fatal(err)
	}
	bound := cwd.CurrentSession()
	release := make(chan struct{})
	b.loop.(*stubLoop).onRun = func(ctx context.Context, _ string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "长任务"})
	}()
	// 回合挂起期间发 /session main → 拒绝且不切换
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		busy := b.busy
		b.mu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("回合未进入 busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	tr.reset()
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "3", Text: "/session main"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tr.sent(), "\n"), "回合进行中") {
		t.Fatalf("busy 中 /session 应被拒: %+v", tr.sent())
	}
	if cwd.CurrentSession() != bound {
		t.Fatalf("busy 中切换不应生效,got %q", cwd.CurrentSession())
	}
	close(release)
	<-done
}
