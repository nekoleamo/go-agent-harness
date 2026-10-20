package tests

// 工具增量输出(C1)的端到端用例:真实 shell 工具 → 进度采集器 → 宿主上报出口
// → 会话事件通道 → 界面。
//
// 为什么要有这一条:前面几条用例各测一段(采集器、上报出口、通道、日志),
// 但**接缝**最容易断 —— ctx 一路派生下来 reporter 掉了、工具拿到的 ctx 不是
// 那个 ctx、通道名对不上,每一段单测都绿,合起来却一句进度都发不出去。
// 这一条把整条链真跑一遍(真起一个 shell 进程)。

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// progressSpy 收集会话通道上的 tool/progress 事件。
type progressSpy struct {
	mu   sync.Mutex
	text []string
}

func (s *progressSpy) add(_ context.Context, ev *sdk.Event) error {
	se, ok := ev.Payload.(*sdk.SessionEvent)
	if !ok || se.Kind != sdk.EventToolProgress {
		return nil
	}
	p, ok := se.Payload.(sdk.ToolProgressEvent)
	if !ok {
		return nil
	}
	s.mu.Lock()
	s.text = append(s.text, p.Chunk)
	s.mu.Unlock()
	return nil
}

func (s *progressSpy) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.text, "")
}

// TestToolProgressEndToEnd 真跑一次 shell:输出既进结果,也以增量形式广播出去。
func TestToolProgressEndToEnd(t *testing.T) {
	c, _ := buildTestEnv(t)

	spy := &progressSpy{}
	d := c.Subscribe(sdk.EventSession, spy.add)
	defer d()

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "请运行 echo 集成测试"); err != nil {
		t.Fatalf("turn 运行失败: %v", err)
	}

	got := spy.all()
	if !strings.Contains(got, "mock-ok") {
		t.Fatalf("会话通道上应收到工具的增量输出(含 mock-ok),got %q", got)
	}

	// 进度是过程不是事实:会话日志里不得有它(重放/导出看不见)。
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	for _, ev := range sessions.Replay() {
		if ev.Kind == sdk.EventToolProgress {
			t.Fatal("进度不应进会话日志")
		}
	}
}
