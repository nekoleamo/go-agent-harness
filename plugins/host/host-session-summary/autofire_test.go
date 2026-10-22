// 「回合后自动生成概述」**真的会跑**的用例(2026-10-21 修死代码)。
//
// 原实现订阅的是总线事件 `sdk.EventTurnEnd`("turn/end"),而那个事件**全仓从未被
// emit 过**:host-agent-loop 只把 turn/end 作为**会话事件**追加进会话日志
// (`appendEvents(sess, {Kind: sdk.EventTurnEnd})` ⇒ 广播在会话事件通道上)。
// ⇒ 这条自动档(K3,**缺省开**)一直是**死代码**:用户从来看不到自动生成的概述,
// 只能手动 `/session summary`。
//
// 修法:改订阅**会话事件通道**(`sdk.EventSession`)+ 按 Kind 过滤,并从事件名反解
// 会话 id(多会话并行时按会话归位,不把 A 会话的回合结束当成 B 的)。
//
// **反向验证**:把订阅改回 `sdk.EventTurnEnd`(总线事件)⇒ 本用例必须红。
package hostsummary

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestAutoSummaryFiresOnTurnEnd 会话事件通道上出现 turn/end ⇒ 自动概述真的生成。
func TestAutoSummaryFiresOnTurnEnd(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)

	// 会话文件:4 轮(≥ MinTurns=2)⇒ 满足自动档的最小轮数
	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8}}}
	ll := &stubLLM{reply: `{"title":"标题","summary":"概述","topics":["t"]}`}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(cs)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.llm", sdk.LLMService(ll)); err != nil {
		t.Fatal(err)
	}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()

	// 回合结束(会话事件通道;与 host-agent-loop 追加的同款载荷)
	ev := &sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"}
	if _, err := c.Emit(context.Background(), sdk.SessionEventName(""), ev, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	// 自动生成是异步的(不阻塞回合收尾)⇒ 等它
	deadline := time.Now().Add(3 * time.Second)
	for {
		ll.mu.Lock()
		calls := ll.calls
		ll.mu.Unlock()
		if calls > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("回合结束后应自动生成概述(自动档是死代码,从没跑过)")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 摘要应落到会话的 meta 缓存(不是凭空生成)
	if cs.summaryCount() == 0 {
		t.Fatal("概述应写入会话缓存")
	}
}

// TestAutoSummaryIgnoresOtherEvents 非 turn/end 的会话事件不该触发自动生成。
func TestAutoSummaryIgnoresOtherEvents(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)

	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8}}}
	ll := &stubLLM{reply: `{"title":"标题","summary":"概述","topics":["t"]}`}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(cs)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.llm", sdk.LLMService(ll)); err != nil {
		t.Fatal(err)
	}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()

	// 别的会话事件(工具结果等)⇒ 不该触发
	ev := &sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{Name: "shell"}}
	if _, err := c.Emit(context.Background(), sdk.SessionEventName(""), ev, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	ll.mu.Lock()
	calls := ll.calls
	ll.mu.Unlock()
	if calls != 0 {
		t.Fatalf("非 turn/end 的事件不该触发自动概述: %d 次", calls)
	}
}

// TestAutoSummaryDisabledByData data.auto_summary=false ⇒ 不订阅、不生成。
func TestAutoSummaryDisabledByData(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)

	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8}}}
	ll := &stubLLM{reply: `{"title":"标题","summary":"概述","topics":["t"]}`}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(cs)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.llm", sdk.LLMService(ll)); err != nil {
		t.Fatal(err)
	}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"auto_summary": false}})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()

	ev := &sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"}
	if _, err := c.Emit(context.Background(), sdk.SessionEventName(""), ev, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	ll.mu.Lock()
	calls := ll.calls
	ll.mu.Unlock()
	if calls != 0 {
		t.Fatalf("关闭自动档后不该生成: %d 次", calls)
	}
}
