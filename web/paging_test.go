// S-P1-2 会话窗口分页单测:窗口回合对齐(拼接不重复不丢)、hasMore 边界、
// 回扩上限、以及 /api/session/events 端点的参数语义(limit 可调小不可调大)。
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// turnEvents 构造 n 个回合,每回合 4 条事件(user/assistant/tool-call/tool-result),
// Seq 从 1 连续递增 —— 便于断言窗口边界落在 user/message 上。
func turnEvents(n int) []sdk.SessionEvent {
	out := make([]sdk.SessionEvent, 0, n*4)
	seq := uint64(1)
	for i := 0; i < n; i++ {
		out = append(out,
			sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: seq},
			sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Seq: seq + 1},
			sdk.SessionEvent{Kind: sdk.EventToolCall, Seq: seq + 2},
			sdk.SessionEvent{Kind: sdk.EventToolResult, Seq: seq + 3},
		)
		seq += 4
	}
	return out
}

func kindsSeq(evs []sdk.SessionEvent) []uint64 {
	out := make([]uint64, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}

// 首连窗口:取尾部 limit 条后**向前扩到回合起点** —— 窗口第一条必须是 user/message,
// 否则前端拼接时同一回合会被两份状态各建一次(用户/助手消息在界面上重复)。
func TestPageEventsTailAlignsToTurnStart(t *testing.T) {
	evs := turnEvents(10) // seq 1..40
	page, hasMore := pageEvents(evs, 0, 10)
	if !hasMore {
		t.Fatal("40 条取 10 条:前面还有历史,hasMore 应为 true")
	}
	if page[0].Kind != sdk.EventUserMessage {
		t.Fatalf("窗口应从回合起点开始,得 %s(seq %d)", page[0].Kind, page[0].Seq)
	}
	// 原始尾部 10 条 = seq 31..40 → 起点 31 不是 user/message → 应收敛到 seq 29
	if page[0].Seq != 29 {
		t.Fatalf("窗口应从 seq 29(回合起点)开始,得 %d:%v", page[0].Seq, kindsSeq(page))
	}
	if page[len(page)-1].Seq != 40 {
		t.Fatalf("窗口尾部应为最新事件 40,得 %d", page[len(page)-1].Seq)
	}
	if len(page) < 12 {
		t.Fatalf("回合对齐会多取几条(不能反过来变少): %d", len(page))
	}
}

// 分页拼接不重不漏:反复以 before=本页最老 seq 上滚,直到 hasMore=false,
// 各页首尾相接覆盖全量且无重复(这是「上滚补历史不重复/不丢帧」的后端保证)。
func TestPageEventsTilesWholeLedger(t *testing.T) {
	evs := turnEvents(25) // seq 1..100
	before := uint64(0)
	var pages [][]sdk.SessionEvent
	for i := 0; i < 50; i++ {
		page, hasMore := pageEvents(evs, before, 12)
		if len(page) == 0 {
			t.Fatalf("第 %d 页为空但 hasMore=%v", i, hasMore)
		}
		// 每页都从回合起点开始
		if page[0].Kind != sdk.EventUserMessage {
			t.Fatalf("第 %d 页起点不是回合起点: %s", i, page[0].Kind)
		}
		pages = append(pages, page)
		if !hasMore {
			break
		}
		before = page[0].Seq
	}
	// 分页是「向前」翻的:按 页序倒排 展开 = 前端 prepend 后的真实顺序
	var seen []uint64
	for i := len(pages) - 1; i >= 0; i-- {
		seen = append(seen, kindsSeq(pages[i])...)
	}
	if len(seen) != len(evs) {
		t.Fatalf("各页应恰好覆盖全量 %d 条,得 %d(重复或漏)", len(evs), len(seen))
	}
	for i, seq := range seen {
		if seq != uint64(i+1) {
			t.Fatalf("第 %d 条应为 seq %d,得 %d(乱序或重复)", i, i+1, seq)
		}
	}
}

// 全量装得下时 hasMore=false(前端不显示上滚入口);空账本给空页不 panic。
func TestPageEventsFitsAndEmpty(t *testing.T) {
	evs := turnEvents(3)
	page, hasMore := pageEvents(evs, 0, 100)
	if hasMore || len(page) != len(evs) {
		t.Fatalf("装得下应全给且 hasMore=false: %d 条 hasMore=%v", len(page), hasMore)
	}
	empty, hasMore := pageEvents(nil, 0, 10)
	if len(empty) != 0 || hasMore {
		t.Fatalf("空账本: %d 条 hasMore=%v", len(empty), hasMore)
	}
	// limit<=0 回落默认窗口
	if def, _ := pageEvents(evs, 0, 0); len(def) != len(evs) {
		t.Fatalf("limit=0 应回落默认窗口: %d", len(def))
	}
	// before 早于全部事件 → 空页(前端上滚到底后的正常终止态)
	if p, m := pageEvents(evs, 1, 10); len(p) != 0 || m {
		t.Fatalf("before=1 应为空页: %d 条 hasMore=%v", len(p), m)
	}
}

// 回合对齐回扩有上限:没有 user/message 的长链(单回合几千条 tool 事件)不能把窗口撑爆。
func TestPageEventsExtendBounded(t *testing.T) {
	evs := make([]sdk.SessionEvent, 0, 1000)
	for i := 1; i <= 1000; i++ {
		evs = append(evs, sdk.SessionEvent{Kind: sdk.EventToolResult, Seq: uint64(i)})
	}
	page, hasMore := pageEvents(evs, 0, 100)
	if !hasMore {
		t.Fatal("前面还有大量事件,hasMore 应为 true")
	}
	if len(page) > 100+sessionPageExtendMax {
		t.Fatalf("回扩应有上限: %d > %d", len(page), 100+sessionPageExtendMax)
	}
	if page[len(page)-1].Seq != 1000 {
		t.Fatalf("尾部必须是最新事件: %d", page[len(page)-1].Seq)
	}
}

// turn/start 紧邻 user/message:纳入窗口(保持「回合以 turn/start 起头」,与实时帧同形)。
func TestPageEventsIncludesTurnStart(t *testing.T) {
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventTurnStart, Seq: 1},
		{Kind: sdk.EventUserMessage, Seq: 2},
		{Kind: sdk.EventAssistantMessage, Seq: 3},
		{Kind: sdk.EventTurnStart, Seq: 4},
		{Kind: sdk.EventUserMessage, Seq: 5},
		{Kind: sdk.EventAssistantMessage, Seq: 6},
	}
	page, hasMore := pageEvents(evs, 0, 2) // 尾部 2 条 = 5,6 → 扩到 5 → 前一条是 turn/start(4)→ 一并纳入
	if !hasMore {
		t.Fatal("前面还有 seq1-3,hasMore 应为 true")
	}
	if page[0].Seq != 4 || page[0].Kind != sdk.EventTurnStart {
		t.Fatalf("应从 turn/start 起头: %v", kindsSeq(page))
	}
}

// ReplayTail:帧与基线同源(ID/Replay 标记 + 边界字段),SSE 与 WS 共用同一份。
func TestReplayTailFramesAndBaseline(t *testing.T) {
	hub := NewHub()
	log := &memLog{evs: turnEvents(5)} // 20 条
	frames, base := hub.ReplayTail(log)
	if len(frames) != 20 || base.Count != 20 || base.HasMore {
		t.Fatalf("窗口应覆盖全部 20 条: frames=%d base=%+v", len(frames), base)
	}
	if base.From != 1 || base.To != 20 {
		t.Fatalf("基线边界: %+v", base)
	}
	for i, f := range frames {
		if !f.Replay || f.Type != FrameSession || f.ID != uint64(i+1) {
			t.Fatalf("第 %d 帧: type=%s id=%d replay=%v", i, f.Type, f.ID, f.Replay)
		}
	}
	// 窗口只取尾部时基线要如实标注 hasMore 与起点
	frames, base = hub.ReplayTail(&memLog{evs: turnEvents(200)}) // 800 条 > 窗口
	if !base.HasMore || base.Count > SessionTailEvents+sessionPageExtendMax || len(frames) != base.Count {
		t.Fatalf("尾窗基线: base=%+v frames=%d", base, len(frames))
	}
}

// 分页端点:before/limit 语义 + 越界 limit 收敛 + 空页归一。
func TestHandleSessionEventsEndpoint(t *testing.T) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), nil)
	s.sessions = &memLog{evs: turnEvents(10)} // seq 1..40
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	get := func(q string) sessionPage {
		t.Helper()
		resp, err := http.Get(hs.URL + "/api/session/events" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 应 200,得 %d", q, resp.StatusCode)
		}
		var out sessionPage
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 无参 = 尾部窗口(与首连基线同口径)
	if p := get(""); p.Count != 40 || p.HasMore || p.From != 1 || p.To != 40 {
		t.Fatalf("尾部窗口: %+v", p)
	}
	// limit 生效且回合对齐(起点落在 user/message)
	p := get("?limit=6")
	if p.HasMore != true || p.Count < 6 || p.Events[0].Kind != sdk.EventUserMessage {
		t.Fatalf("limit 窗口: count=%d hasMore=%v 起点=%s", p.Count, p.HasMore, p.Events[0].Kind)
	}
	// before 游标:只回 seq < before 的内容
	pb := get("?before=9&limit=4")
	for _, e := range pb.Events {
		if e.Seq >= 9 {
			t.Fatalf("before=9 不应含 seq %d", e.Seq)
		}
	}
	if pb.To >= 9 {
		t.Fatalf("before=9 的页尾: %d", pb.To)
	}
	// limit 越界收敛到上限(不报错但也不能服千万条)
	if big := get("?limit=999999"); big.Count != 40 {
		t.Fatalf("越界 limit 应收敛(不超全量): %d", big.Count)
	}
	// 坏 limit 显式 400(不静默当默认值)
	resp, err := http.Get(hs.URL + "/api/session/events?limit=-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("负 limit 应 400,得 %d", resp.StatusCode)
	}
}
