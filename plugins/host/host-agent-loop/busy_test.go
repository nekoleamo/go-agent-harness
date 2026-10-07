package hostagentloop

// 「有回合在写这份会话日志」的标记测试。
//
// 为什么重要:host-cwd-sessions 的切走闸门判的就是这个标记。当前打开的会话用的是
// **ctx.sessions 单例**,切走会对同一对象 Load 新路径 —— 标记若不置位,正在跑的回合会把
// 后半截写进新会话的文件(跨会话串写,且事后无法分辨)。
//
// 因此这里钉两件事:
//   ① acquire/release 成对置位/清零(主单例与非当前会话两个分支都要);
//   ② 回合**真的在跑**的那段时间里标记为 busy(不是只有 acquire 那一刻)。

import (
	"context"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type busyGetter interface{ IsBusy() bool }

// waitBusy 轮询等标记置位(回合已在跑 ⇒ 置位早于任何模型调用,极快;留足余量给慢 CI)。
func waitBusy(t *testing.T, m busyGetter) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.IsBusy() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("回合进行中日志应标记 busy")
}

func TestAcquireMarksMainSingletonBusyUntilRelease(t *testing.T) {
	e := buildEnv(t, `[{"text":"ok","finish":"stop"}]`)
	lg, release, err := e.loop.acquire("")
	if err != nil {
		t.Fatal(err)
	}
	lm, ok := lg.(busyGetter)
	if !ok {
		t.Fatal("主单例应实现 busy 标记")
	}
	if !lm.IsBusy() {
		t.Fatal("acquire 后应标记 busy")
	}
	release()
	if lm.IsBusy() {
		t.Fatal("release 后应清除标记")
	}
}

func TestAcquireMarksOtherSessionBusyUntilRelease(t *testing.T) {
	e := buildEnv(t, `[{"text":"ok","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	e.loop.cs = nil
	lg, release, err := e.loop.acquire("sA")
	if err != nil {
		t.Fatal(err)
	}
	lm, ok := lg.(busyGetter)
	if !ok {
		t.Fatal("注册表实例应实现 busy 标记")
	}
	if !lm.IsBusy() {
		t.Fatal("非当前会话 acquire 后也应标记 busy")
	}
	release()
	if lm.IsBusy() {
		t.Fatal("release 后应清除标记")
	}
}

// TestBusyWhileTurnRunning 回合进行中标记为 busy,回合结束后清零(端到端)。
func TestBusyWhileTurnRunning(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"ok"})
	// 包一层:只改 Complete 的阻塞,其余能力转发原服务(不想为一个测试实现整个 LLMService)。
	blk := &gateLLM{LLMService: e.loop.llm, gate: make(chan struct{})}
	e.loop.llm = blk
	_ = llm

	done := make(chan error, 1)
	go func() { done <- e.loop.Run(context.Background(), "任务") }()

	waitBusy(t, e.log) // 回合在跑
	close(blk.gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("回合应正常结束: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("回合超时未结束")
	}
	if e.log.IsBusy() {
		t.Fatal("回合结束后必须清除 busy 标记(否则会话再也切不走)")
	}
}

// gateLLM 把 Complete 卡在 gate 上:制造一个可观察的「回合进行中」窗口(其余方法转发)。
type gateLLM struct {
	sdk.LLMService
	gate chan struct{}
}

func (g *gateLLM) Complete(ctx context.Context, _ *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: "ok"}, FinishReason: sdk.FinishReasonStop}, nil
}

// 反向验证:若 loop 不再置位标记,TestBusyWhileTurnRunning 必红(标记永远为 false,
// waitBusy 超时失败)—— 也就是说这条测试真的在验「回合进行中标记为 busy」,不是摆设。
func TestBusyMarkerSurvivesCancelledTurn(t *testing.T) {
	e := buildEnv(t, `[{"text":"ok","finish":"stop"}]`)
	ctxc, cancel := context.WithCancel(context.Background())
	cancel() // 回合立刻被取消:错误路径也必须清标记
	if err := e.loop.Run(ctxc, "任务"); err == nil {
		t.Fatal("取消的回合应报错")
	}
	if e.log.IsBusy() {
		t.Fatal("取消的回合也必须清标记(否则取消后永远切不走会话)")
	}
}
