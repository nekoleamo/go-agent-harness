package hostagentloop

// 工具进度的**通道归属**用例(2026-10-20 C1)。
//
// 要钉的是三件事:
//  1. 进度走**该会话的事件通道**(界面据此按会话归位,不必各自实现归属过滤);
//  2. 进度**不落盘**(过程不是事实:进日志会灌满会话文件、重放再吐一遍、白占 token 预算);
//  3. 进度**不占会话序号**(瞬时事件 Seq 恒 0;否则已落盘事件的编号会被顶掉,
//     断线续传游标 Last-Event-ID 随之错位)。

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// progTool 上报进度的工具(走 sdk.ReportToolProgress 这条唯一上报口)。
type progTool struct {
	chunks []string
}

func (p *progTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "prog", Description: "上报进度", InputSchema: map[string]any{"type": "object"}}
}

func (p *progTool) Execute(ctx context.Context, _ string) (any, error) {
	for _, c := range p.chunks {
		sdk.ReportToolProgress(ctx, "prog", c)
	}
	return map[string]any{"ok": true}, nil
}

// progressCollector 订阅会话事件通道,收集 tool/progress 事件。
type progressCollector struct {
	mu  sync.Mutex
	evs []sdk.SessionEvent
}

func (c *progressCollector) add(_ context.Context, ev *sdk.Event) error {
	se, ok := ev.Payload.(*sdk.SessionEvent)
	if !ok {
		return nil
	}
	if se.Kind != sdk.EventToolProgress {
		return nil
	}
	c.mu.Lock()
	c.evs = append(c.evs, *se)
	c.mu.Unlock()
	return nil
}

func (c *progressCollector) all() []sdk.SessionEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sdk.SessionEvent(nil), c.evs...)
}

// TestProgressOnSessionChannelNotPersisted 进度广播在会话通道上,且**不进会话日志**。
func TestProgressOnSessionChannelNotPersisted(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"", "收尾"})
	e.tools.Register(&progTool{chunks: []string{"编译中", "链接中"}})
	llm.calls = [][]sdk.ToolCall{{{ID: "toolu_1", Name: "prog", Arguments: `{}`}}}

	col := &progressCollector{}
	d := e.c.Subscribe(sdk.EventSession, col.add)
	defer d()

	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}

	evs := col.all()
	if len(evs) == 0 {
		t.Fatal("会话通道上应收到 tool/progress 事件(界面靠它显示过程)")
	}
	var text strings.Builder
	for _, ev := range evs {
		p, ok := ev.Payload.(sdk.ToolProgressEvent)
		if !ok {
			t.Fatalf("载荷应为 sdk.ToolProgressEvent,got %T", ev.Payload)
		}
		if p.CallID != "toolu_1" {
			t.Fatalf("CallID 应为模型侧调用 id,got %q", p.CallID)
		}
		if p.Name != "prog" {
			t.Fatalf("工具名应为 prog,got %q", p.Name)
		}
		if p.Seq <= 0 {
			t.Fatalf("同一调用内序号应递增,got %d", p.Seq)
		}
		text.WriteString(p.Chunk)
	}
	if got := text.String(); got != "编译中链接中" {
		t.Fatalf("增量文本应完整,got %q", got)
	}

	// 不落盘:会话日志里不得出现 tool/progress。
	for _, ev := range e.log.Replay() {
		if ev.Kind == sdk.EventToolProgress {
			t.Fatal("进度是过程不是事实,不应进会话日志")
		}
	}
}

// TestProgressDoesNotConsumeSeq 瞬时事件不占会话序号:它广播之后,下一条落盘事件的 Seq 不受影响。
func TestProgressDoesNotConsumeSeq(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"", "收尾"})
	e.tools.Register(&progTool{chunks: []string{"x"}})
	llm.calls = [][]sdk.ToolCall{{{ID: "toolu_1", Name: "prog", Arguments: `{}`}}}

	col := &progressCollector{}
	d := e.c.Subscribe(sdk.EventSession, col.add)
	defer d()

	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}

	evs := col.all()
	if len(evs) == 0 {
		t.Fatal("应有进度事件")
	}
	for _, ev := range evs {
		if ev.Seq != 0 {
			t.Fatalf("瞬时事件不占序号,Seq 应恒为 0,got %d", ev.Seq)
		}
	}
	// 落盘事件的序号必须连续(没有洞):进度若占了序号,这里会出现跳号。
	seen := map[uint64]bool{}
	for _, ev := range e.log.Replay() {
		if ev.Seq == 0 {
			t.Fatal("落盘事件应有序号")
		}
		if seen[ev.Seq] {
			t.Fatalf("序号重复:%d", ev.Seq)
		}
		seen[ev.Seq] = true
	}
	for i := uint64(1); i <= uint64(len(seen)); i++ {
		if !seen[i] {
			t.Fatalf("落盘序号应连续:缺 %d", i)
		}
	}
}

// TestProgressCoalescedTextKept 合并只推迟发送,不许丢文本:
// 同一窗口内多块 ⇒ 帧数变少,但拼起来的内容一字不少。
func TestProgressCoalescedTextKept(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"", "收尾"})
	e.tools.Register(&progTool{chunks: []string{"a", "b", "c", "d", "e"}})
	llm.calls = [][]sdk.ToolCall{{{ID: "toolu_1", Name: "prog", Arguments: `{}`}}}

	col := &progressCollector{}
	d := e.c.Subscribe(sdk.EventSession, col.add)
	defer d()

	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}

	evs := col.all()
	if len(evs) == 0 {
		t.Fatal("应有进度事件")
	}
	var text strings.Builder
	for _, ev := range evs {
		p := ev.Payload.(sdk.ToolProgressEvent)
		text.WriteString(p.Chunk)
	}
	if got := text.String(); got != "abcde" {
		t.Fatalf("合并后文本应完整,got %q", got)
	}
	if len(evs) >= 5 {
		t.Fatalf("同窗口内的块应合并成少数几帧,got %d 帧", len(evs))
	}
}
