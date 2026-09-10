// im 桥单元测试(同包):gate 三态 / 去重 / 确认回填 / busy / /stop / 输出聚合。
// 回合引擎用 stubLoop + 真 sessionlog(输出聚合走真实 Replay);真实 agent 链在 tests/im_e2e_test.go。
package im

import (
	"context"
	"errors"
	"fmt"
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

// TestTurnAsyncAfter P2 长回合自动转后台:AsyncAfter 阈值内未完成 → 先回
// "转入后台"通知(HandleInbound 提前返回),回合完成后结果仍自动回推(不丢)。
func TestTurnAsyncAfter(t *testing.T) {
	b, loop, tr, sessions := buildTestBridge(t, Options{
		Mode:       AccessAllowlist,
		Allow:      []string{"mock\x00owner"},
		AsyncAfter: 60 * time.Millisecond,
	})
	loop.onRun = func(ctx context.Context, _ string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond): // 超过阈值
		}
		return sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "长任务结果"}})
	}
	start := time.Now()
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "长任务"}); err != nil {
		t.Fatal(err)
	}
	// busy 槽保持到回合完成(agent-loop 单飞写会话,防并发回合错乱);
	// AsyncAfter 语义 = 阈值时先通知"转后台",完成结果仍自动回推(主动投递路径)。
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("回合应完整执行至完成,实际 %v", elapsed)
	}
	sent := tr.sent()
	if len(sent) < 1 || !strings.Contains(sent[0], "转入后台") {
		t.Fatalf("应先回转后台通知: %+v", sent)
	}
	found := false
	for _, x := range sent {
		if strings.Contains(x, "长任务结果") {
			found = true
		}
	}
	if !found {
		t.Fatalf("完成结果应自动回推: %+v", sent)
	}
}

// stubJobs 后台任务记录(host-jobs 语义简化:异步执行 fn;记录输入)。
type stubJobs struct {
	mu  sync.Mutex
	rns []sdk.JobFunc
}

func (j *stubJobs) Submit(cmdline string) (string, error) {
	return "j" + fmt.Sprintf("%d", len(j.rns)), nil
}
func (j *stubJobs) Run(fn sdk.JobFunc) (string, error) {
	j.mu.Lock()
	id := fmt.Sprintf("job-%d", len(j.rns)+1)
	j.rns = append(j.rns, fn)
	j.mu.Unlock()
	go func() { _, _ = fn(context.Background()) }() // 异步执行
	return id, nil
}
func (j *stubJobs) List() []sdk.Job { return nil }
func (j *stubJobs) Output(id string) (sdk.Job, bool) {
	return sdk.Job{}, false
}
func (j *stubJobs) Kill(id string) error { return nil }
func (j *stubJobs) ran() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.rns)
}

// TestBgCommand /bg 后台执行(P2):提交回执 → job 异步跑 loop → 完成聚合主动回推;
// busy 槽在 job 完成后释放(期间普通消息排队)。
func TestBgCommand(t *testing.T) {
	b, loop, tr, sessions := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	b.jobs = &stubJobs{}
	// job 异步跑 loop;完成自动回推(onRun 先设,防与 job goroutine 竞态)
	loop.onRun = func(_ context.Context, in string) error {
		return sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "后台结果:" + in}})
	}
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "/bg 帮我跑个长任务"}); err != nil {
		t.Fatal(err)
	}
	sent := tr.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "转入后台执行") {
		t.Fatalf("应回提交回执: %+v", sent)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		sent = tr.sent()
		found := false
		for _, x := range sent {
			if strings.Contains(x, "后台结果") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台完成未回推: %+v", sent)
		}
		time.Sleep(10 * time.Millisecond)
	}
	loop.mu.Lock()
	defer loop.mu.Unlock()
	if len(loop.inputs) != 1 || loop.inputs[0] != "帮我跑个长任务" {
		t.Fatalf("后台应执行任务文本: %+v", loop.inputs)
	}
}

// TestBgCommandBusy 回合进行中 /bg 被拒(防并发双 loop 写会话)。
func TestBgCommandBusy(t *testing.T) {
	b, _, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	b.jobs = &stubJobs{}
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
		done <- b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "任务"})
	}()
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
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "/bg 并发任务"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tr.sent(), "\n"), "回合进行中") {
		t.Fatalf("busy 中 /bg 应被拒: %+v", tr.sent())
	}
	close(release)
	<-done
}

// TestParseQuestionAnswer 提问作答解析:编号/值/说明匹配、多选、自由文本、非法输入。
func TestParseQuestionAnswer(t *testing.T) {
	q := sdk.Question{Prompt: "选哪个?", Options: []sdk.QuestionOption{
		{Value: "plan-a", Desc: "方案A"}, {Value: "plan-b", Desc: "方案B"}, {Value: "plan-c", Desc: "方案C"},
	}}
	cases := []struct {
		in      string
		values  []string
		text    string
		matched bool
	}{
		{"2", []string{"plan-b"}, "", true},
		{"方案C", []string{"plan-c"}, "", true},
		{"plan-a", []string{"plan-a"}, "", true},
		{"九", nil, "", false},   // 非法编号且不允许自由文本
		{"1,3", nil, "", false}, // 单选回多个 → 提示重答
		{"", nil, "", false},
	}
	for _, c := range cases {
		got, ok := parseQuestionAnswer(q, c.in)
		if ok != c.matched || strings.Join(got.Values, ",") != strings.Join(c.values, ",") || got.Text != c.text {
			t.Errorf("parse(%q) = (%v,%v),want (%v,%v)", c.in, got.Values, ok, c.values, c.matched)
		}
	}
	// 多选
	multi := sdk.Question{Prompt: "选多项", Multiple: true, Options: q.Options}
	if got, ok := parseQuestionAnswer(multi, "1, 3"); !ok || strings.Join(got.Values, ",") != "plan-a,plan-c" {
		t.Fatalf("多选应解析 1,3 → plan-a,plan-c: %v %v", got.Values, ok)
	}
	// 自由文本(有选项且 FreeText)
	free := sdk.Question{Prompt: "选或写", FreeText: true, Options: q.Options}
	if got, ok := parseQuestionAnswer(free, "我自己想的第二种做法"); !ok || got.Text != "我自己想的第二种做法" {
		t.Fatalf("FreeText 应整段作答: %+v %v", got, ok)
	}
	// 纯自由文本提问(无选项)
	plain := sdk.Question{Prompt: "叫什么名字?"}
	if got, ok := parseQuestionAnswer(plain, "阿黄"); !ok || got.Text != "阿黄" {
		t.Fatalf("无选项提问应整段作答: %+v %v", got, ok)
	}
}

// TestQuestionPresentAndAnswer 桥级:提问推送(编号提示)→ 用户回编号 → 作答回填;
// 无法识别时提示且消费该条(不误入回合)。
func TestQuestionPresentAndAnswer(t *testing.T) {
	b, loop, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	b.mu.Lock()
	b.curRoute = mkRoute("owner") // 模拟回合中(提问发生在回合内)
	b.mu.Unlock()
	ch, cancel, err := b.PresentQuestion(context.Background(), sdk.Question{
		Prompt:  "部署到哪个环境?",
		Options: []sdk.QuestionOption{{Value: "dev", Desc: "开发"}, {Value: "prod", Desc: "生产"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	sent := tr.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "❓ 部署到哪个环境?") || !strings.Contains(sent[0], "1) 开发") {
		t.Fatalf("提问推送应含问题与编号选项: %+v", sent)
	}
	// 用户先回无法识别的内容 → 提示重答,不开回合
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "随便"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tr.sent(), "\n"), "请按提示回复编号") {
		t.Fatalf("无法识别应提示重答: %+v", tr.sent())
	}
	loop.mu.Lock()
	n := len(loop.inputs)
	loop.mu.Unlock()
	if n != 0 {
		t.Fatal("作答消息不应开回合")
	}
	// 回编号 → 作答回填
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "2", Text: "2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ans := <-ch:
		if len(ans.Values) != 1 || ans.Values[0] != "prod" {
			t.Fatalf("应回填 prod: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("作答未回填")
	}
	// cancel 后 pending 清理
	cancel()
	b.askMu.Lock()
	n2 := len(b.qPending)
	b.askMu.Unlock()
	if n2 != 0 {
		t.Fatalf("cancel 后应清理 pending,got %d", n2)
	}
}

// TestImCommandGroupAllow /im allowg|revokeg|list:群维度授权经 IM 命令面生效。
func TestImCommandGroupAllow(t *testing.T) {
	b, loop, tr, sessions := buildTestBridge(t, Options{Mode: AccessPairing})
	grp := Route{Channel: "mock", UserID: "member-1", ChatID: "GROUP-1"}
	// 主机侧执行授权(IM 内未授权用户不能自助授权——命令面在 gate 之后,安全语义)
	if out := b.imCmd(context.Background(), []string{"allowg", "GROUP-1"}); !strings.Contains(out, "已授权群") {
		t.Fatalf("allowg 应回授权成功: %q", out)
	}
	// 群内成员消息放行(无需各自配对)
	loop.onRun = finishText(sessions, "群回复")
	if err := b.HandleInbound(context.Background(), Inbound{Route: grp, MsgID: "2", Text: "大家好"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tr.sent(), "\n"), "群回复") {
		t.Fatalf("群授权后应开回合: %+v", tr.sent())
	}
	// list 显示群
	tr.reset()
	if err := b.HandleInbound(context.Background(), Inbound{Route: grp, MsgID: "3", Text: "/im list"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tr.sent(), "\n"); !strings.Contains(got, "已授权群") || !strings.Contains(got, "GROUP-1") {
		t.Fatalf("list 应含群: %q", got)
	}
	// revokeg 撤销(主机侧)后回到配对(群消息回配对提示+群授权指引)
	if out := b.imCmd(context.Background(), []string{"revokeg", "GROUP-1"}); !strings.Contains(out, "已撤销群授权") {
		t.Fatalf("revokeg 应回撤销成功: %q", out)
	}
	tr.reset()
	if err := b.HandleInbound(context.Background(), Inbound{Route: grp, MsgID: "5", Text: "再来"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tr.sent(), "\n"); !strings.Contains(got, "未授权") || !strings.Contains(got, "allowg") {
		t.Fatalf("撤销后群消息应回配对+群授权指引: %q", got)
	}
}

// TestBridgeDiagCallback 入站诊断回调(真机排障):未授权丢弃 / 配对提示 / 回合启动均上报。
func TestBridgeDiagCallback(t *testing.T) {
	var mu sync.Mutex
	var got []string
	diag := func(s string) { mu.Lock(); got = append(got, s); mu.Unlock() }
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(got, "\n")
	}
	reset := func() { mu.Lock(); got = nil; mu.Unlock() }

	// 未授权(allowlist 静默丢弃)→ 诊断含"入站丢弃:未授权"
	b, _, _, _ := buildTestBridge(t, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00OK"}, Diag: diag})
	_ = b.HandleInbound(context.Background(), Inbound{Route: mkRoute("UNAUTH"), MsgID: "m1", Text: "hi"})
	if !strings.Contains(snapshot(), "入站丢弃:未授权") {
		t.Fatalf("未授权应上报诊断: %q", snapshot())
	}

	// 放行一条 + 重复 msgID → 诊断含"入站放行"与"重复消息"
	reset()
	_ = b.HandleInbound(context.Background(), Inbound{Route: mkRoute("OK"), MsgID: "m2", Text: "hi"})
	_ = b.HandleInbound(context.Background(), Inbound{Route: mkRoute("OK"), MsgID: "m2", Text: "hi"})
	j := snapshot()
	if !strings.Contains(j, "入站放行") || !strings.Contains(j, "重复消息") {
		t.Fatalf("应同时上报放行与重复丢弃: %q", j)
	}

	// 配对模式 → 诊断含"已回配对码提示"
	reset()
	b2, _, _, _ := buildTestBridge(t, Options{Mode: AccessPairing, Diag: diag})
	_ = b2.HandleInbound(context.Background(), Inbound{Route: mkRoute("STRANGER"), MsgID: "m3", Text: "hi"})
	if !strings.Contains(snapshot(), "已回配对码提示") {
		t.Fatalf("配对模式应上报提示诊断: %q", snapshot())
	}

	// nil Diag 不应 panic
	b3, _, _, _ := buildTestBridge(t, Options{Mode: AccessPairing})
	if err := b3.HandleInbound(context.Background(), Inbound{Route: mkRoute("STRANGER"), MsgID: "m4", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
}

// TestImGroupOptions 群参数逐级确认:/im allowg 枚举最近活动群(未授权也列,免手抄 openid)、
// revokeg 枚举已授权群;/im list 同时给出未授权群提示。
func TestImGroupOptions(t *testing.T) {
	b, _, _, _ := buildTestBridge(t, Options{Mode: AccessPairing})
	// 私聊不应被记录为群
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("U1"), MsgID: "d1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if got := b.groupOptions(false); len(got) != 0 {
		t.Fatalf("私聊不应记入群选项: %+v", got)
	}
	// 未授权群入站(被拒也记录)→ allowg 选项可见
	grp := Route{Channel: "mock", UserID: "U1", ChatID: "G1"}
	if err := b.HandleInbound(context.Background(), Inbound{Route: grp, MsgID: "g1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	opts := b.groupOptions(false)
	if len(opts) != 1 || opts[0].Value != "G1" {
		t.Fatalf("allowg 应列出最近活动群: %+v", opts)
	}
	if got := b.groupOptions(true); len(got) != 0 {
		t.Fatalf("未授权时 revokeg 不应有选项: %+v", got)
	}
	if out := b.imCmd(context.Background(), []string{"list"}); !strings.Contains(out, "最近活动群(未授权") {
		t.Fatalf("未授权群应在 /im list 列出: %q", out)
	}
	// 群授权后:allowg 标注已授权、revokeg 可撤销、list 不再提示未授权
	b.acc.AllowGroup(b.chanKey("G1"))
	if opts := b.groupOptions(false); len(opts) != 1 || !strings.Contains(opts[0].Desc, "已授权") {
		t.Fatalf("授权后 allowg 选项应标注已授权: %+v", opts)
	}
	if rv := b.groupOptions(true); len(rv) != 1 || rv[0].Value != "G1" {
		t.Fatalf("revokeg 应列出已授权群: %+v", rv)
	}
	if out := b.imCmd(context.Background(), []string{"list"}); !strings.Contains(out, "已授权群") || strings.Contains(out, "最近活动群") {
		t.Fatalf("授权后 list 不应再提示未授权群: %q", out)
	}
	// 新群出现 → 最新在前
	if err := b.HandleInbound(context.Background(), Inbound{Route: Route{Channel: "mock", UserID: "U1", ChatID: "G2"}, MsgID: "g2", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if got := b.groupOptions(false); len(got) != 2 || got[0].Value != "G2" {
		t.Fatalf("最近活动群应最新在前: %+v", got)
	}
}
