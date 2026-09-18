// traj_test.go S-P0-1 TUI 端轨迹视图:纯聚合口径与 Web 侧 traj.ts 对齐(同源事件、同语义)。
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// trajTS 固定时间基准(测试内只用相对差值,不依赖真实时钟)。
func trajTS(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("时间夹具解析失败 %q: %v", s, err)
	}
	return ts
}

func trajPushEv(t *testing.T, tr *Traj, kind string, seq uint64, ts string, payload any) {
	t.Helper()
	tr.Push(&sdk.SessionEvent{Kind: kind, Seq: seq, Payload: payload, TS: trajTS(t, ts)})
}

// TestTrajTurnBoundaryDerived 回合边界由 user/message 起点与 turn/end 终点派生。
func TestTrajTurnBoundaryDerived(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "第一问"})
	trajPushEv(t, tr, sdk.EventTurnEnd, 2, "2026-09-18T10:00:02Z", "done")
	trajPushEv(t, tr, sdk.EventUserMessage, 3, "2026-09-18T10:01:00Z", sdk.UserMessage{Content: "第二问"})
	trajPushEv(t, tr, sdk.EventTurnEnd, 4, "2026-09-18T10:01:05Z", "done")

	if len(tr.Turns) != 2 {
		t.Fatalf("应派生 2 个回合,得到 %d", len(tr.Turns))
	}
	if tr.Turns[0].Index != 1 || tr.Turns[1].Index != 2 {
		t.Fatalf("回合序号应递增: %d %d", tr.Turns[0].Index, tr.Turns[1].Index)
	}
	if tr.Turns[0].User != "第一问" || tr.Turns[1].User != "第二问" {
		t.Fatalf("用户消息未落到所属回合: %q %q", tr.Turns[0].User, tr.Turns[1].User)
	}
	if ms, ok := tr.Turns[0].TurnMS(); !ok || ms != 2000 {
		t.Fatalf("首个回合时长应为 2000ms: %d/%v", ms, ok)
	}
	if ms, ok := tr.Turns[1].TurnMS(); !ok || ms != 5000 {
		t.Fatalf("次个回合时长应为 5000ms: %d/%v", ms, ok)
	}
	if tr.cur != nil {
		t.Fatalf("回合结束后不应仍有进行中回合")
	}
}

// TestTrajTurnStartFirstThenUserMessage turn/start 先行(新账本顺序)时用户消息回填同一回合。
func TestTrajTurnStartFirstThenUserMessage(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventTurnStart, 1, "2026-09-18T10:00:00Z", nil)
	trajPushEv(t, tr, sdk.EventUserMessage, 2, "2026-09-18T10:00:00.100Z", sdk.UserMessage{Content: "回填"})
	if len(tr.Turns) != 1 {
		t.Fatalf("turn/start 后 user/message 不应另开回合: %d", len(tr.Turns))
	}
	turn := tr.Turns[0]
	if turn.User != "回填" || turn.Seq != 2 {
		t.Fatalf("用户消息应回填同回合并刷新锚点 seq: %q seq=%d", turn.User, turn.Seq)
	}
}

// TestTrajImplicitTurnAndStep 只有过程帧(无 user/message、无 step/start)时补隐式回合与隐式步。
func TestTrajImplicitTurnAndStep(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventToolCall, 1, "2026-09-18T10:00:00Z", sdk.ToolCallEvent{ID: "t1", Name: "read_file", Arguments: `{"path":"a.go"}`})
	if len(tr.Turns) != 1 {
		t.Fatalf("无 user/message 时应开隐式回合: %d", len(tr.Turns))
	}
	turn := tr.Turns[0]
	if turn.User != "" {
		t.Fatalf("隐式回合无用户消息: %q", turn.User)
	}
	if len(turn.Steps) != 1 || len(turn.Steps[0].ToolIDs) != 1 {
		t.Fatalf("无 step 的工具调用应补隐式 step: %+v", turn.Steps)
	}
	if turn.ToolIDs[0] != "t1" || turn.Tools["t1"].Name != "read_file" {
		t.Fatalf("工具归属丢失: %+v", turn.Tools["t1"])
	}
}

// TestTrajToolAttributionAndBackfill 工具归属当前 step,结果回填状态与出参字节。
func TestTrajToolAttributionAndBackfill(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, tr, sdk.EventStepStart, 2, "2026-09-18T10:00:00.100Z", nil)
	trajPushEv(t, tr, sdk.EventToolCall, 3, "2026-09-18T10:00:00.200Z", sdk.ToolCallEvent{ID: "ok1", Name: "file_read", Arguments: "{}"})
	trajPushEv(t, tr, sdk.EventToolResult, 4, "2026-09-18T10:00:01Z", sdk.ToolResultEvent{CallID: "ok1", Name: "file_read", Content: "abcd"})
	trajPushEv(t, tr, sdk.EventToolCall, 5, "2026-09-18T10:00:01.100Z", sdk.ToolCallEvent{ID: "err1", Name: "bash", Arguments: "{}"})
	trajPushEv(t, tr, sdk.EventToolResult, 6, "2026-09-18T10:00:01.500Z", sdk.ToolResultEvent{CallID: "err1", Name: "bash", Error: "ENOENT"})
	trajPushEv(t, tr, sdk.EventStepEnd, 7, "2026-09-18T10:00:02Z", nil)

	turn := tr.Turns[0]
	if len(turn.Steps) != 1 || len(turn.Steps[0].ToolIDs) != 2 {
		t.Fatalf("两个工具应归同一 step: %+v", turn.Steps)
	}
	if got := turn.Tools["ok1"]; got.Status != "ok" || got.OutBytes != 4 {
		t.Fatalf("成功工具应回填 ok/字节数: %+v", got)
	}
	if got := turn.Tools["err1"]; got.Status != "err" || got.Error != "ENOENT" || got.OutBytes != 6 {
		t.Fatalf("失败工具应回填 err/错误文案: %+v", got)
	}
	// 时长只取事件 TS:step 1900ms;工具 800ms/400ms
	if ms, ok := turn.Steps[0].StepMS(); !ok || ms != 1900 {
		t.Fatalf("步时长应为 1900ms: %d/%v", ms, ok)
	}
	if ms, ok := turn.Tools["ok1"].ToolMS(); !ok || ms != 800 {
		t.Fatalf("工具耗时应为 800ms: %d/%v", ms, ok)
	}
	if ms, ok := turn.Tools["err1"].ToolMS(); !ok || ms != 400 {
		t.Fatalf("工具耗时应为 400ms: %d/%v", ms, ok)
	}
}

// TestTrajInProgressNoDuration 进行中的 turn/step/tool 一律不给时长(不拿本地时钟补齐)。
func TestTrajInProgressNoDuration(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, tr, sdk.EventStepStart, 2, "2026-09-18T10:00:00.100Z", nil)
	trajPushEv(t, tr, sdk.EventToolCall, 3, "2026-09-18T10:00:00.200Z", sdk.ToolCallEvent{ID: "t1", Name: "bash"})

	turn := tr.Turns[0]
	if _, ok := turn.TurnMS(); ok {
		t.Fatalf("进行中的回合不应有时长")
	}
	if _, ok := turn.Steps[0].StepMS(); ok {
		t.Fatalf("进行中的步不应有时长")
	}
	if _, ok := turn.Tools["t1"].ToolMS(); ok {
		t.Fatalf("未回填结果的工具不应有时长")
	}
	if over := tr.Overview(); over.HasMS {
		t.Fatalf("有进行中回合时总计时长不应可算: %+v", over)
	}
	if st := turn.Stats(); st.Pending != 1 || st.HasMS {
		t.Fatalf("未回填计数应为 1 且无时长: %+v", st)
	}
}

// TestTrajUsageAggregatedPerTurn 用量按回合累加;回合外 usage 不记账也不崩。
func TestTrajUsageAggregatedPerTurn(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUsage, 1, "2026-09-18T10:00:00Z", sdk.UsageEvent{Model: "deepseek-chat", Usage: sdk.Usage{PromptTokens: 100, CompletionTokens: 20, CachedTokens: 60}})
	if len(tr.Turns) != 0 {
		t.Fatalf("回合外 usage 不应开回合: %d", len(tr.Turns))
	}
	trajPushEv(t, tr, sdk.EventUserMessage, 2, "2026-09-18T10:00:01Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, tr, sdk.EventUsage, 3, "2026-09-18T10:00:02Z", sdk.UsageEvent{Model: "deepseek-chat", Usage: sdk.Usage{PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 600}})
	trajPushEv(t, tr, sdk.EventUsage, 4, "2026-09-18T10:00:03Z", sdk.UsageEvent{Model: "deepseek-reasoner", Usage: sdk.Usage{PromptTokens: 500, CompletionTokens: 100, CachedTokens: 300}})

	u := tr.Turns[0].Usage
	if u.Requests != 2 || u.Prompt != 1500 || u.Completion != 300 || u.Cached != 900 {
		t.Fatalf("用量应按回合累加: %+v", u)
	}
	if u.Model != "deepseek-reasoner" {
		t.Fatalf("模型名应取最后一次: %q", u.Model)
	}
	if st := tr.Turns[0].Stats(); st.Tokens != 1800 {
		t.Fatalf("token 合计应为 prompt+completion: %+v", st)
	}
}

// TestTrajLateToolResultCrossTurn 结果帧晚于回合结束(取消后收尾)时跨回合回溯定位,不新开回合。
func TestTrajLateToolResultCrossTurn(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, tr, sdk.EventStepStart, 2, "2026-09-18T10:00:00.100Z", nil)
	trajPushEv(t, tr, sdk.EventToolCall, 3, "2026-09-18T10:00:00.200Z", sdk.ToolCallEvent{ID: "t1", Name: "bash"})
	trajPushEv(t, tr, sdk.EventTurnEnd, 4, "2026-09-18T10:00:01Z", "cancelled")
	trajPushEv(t, tr, sdk.EventToolResult, 5, "2026-09-18T10:00:01.500Z", sdk.ToolResultEvent{CallID: "t1", Name: "bash", Error: "killed"})

	if len(tr.Turns) != 1 {
		t.Fatalf("晚到结果帧不应新开回合: %d", len(tr.Turns))
	}
	if tool := tr.Turns[0].Tools["t1"]; tool == nil || tool.Status != "err" {
		t.Fatalf("晚到结果应回填到原回合工具: %+v", tool)
	}
	if tr.Turns[0].Reason != "cancelled" {
		t.Fatalf("结束原因应原样记录: %q", tr.Turns[0].Reason)
	}
	// 未知 CallID 静默忽略(不 panic、不建回合)
	trajPushEv(t, tr, sdk.EventToolResult, 6, "2026-09-18T10:00:02Z", sdk.ToolResultEvent{CallID: "missing"})
	if len(tr.Turns) != 1 {
		t.Fatalf("未知工具结果不应建回合: %d", len(tr.Turns))
	}
}

// TestTrajTurnEndReasonVariants 结束原因:cancelled/max_steps 原样;缺载荷记 done。
func TestTrajTurnEndReasonVariants(t *testing.T) {
	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"有载荷", "cancelled", "cancelled"},
		{"步数耗尽", "max_steps", "max_steps"},
		{"空串", "", "done"},
		{"缺载荷", nil, "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTraj()
			trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
			trajPushEv(t, tr, sdk.EventTurnEnd, 2, "2026-09-18T10:00:01Z", tc.payload)
			if got := tr.Turns[0].Reason; got != tc.want {
				t.Fatalf("结束原因 = %q,期望 %q", got, tc.want)
			}
		})
	}
}

// TestTrajDuplicateSeqIgnored 同一事件二次进入(重放路径)按 seq 去重,不重复计数。
func TestTrajDuplicateSeqIgnored(t *testing.T) {
	tr := NewTraj()
	ev := &sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 7, TS: trajTS(t, "2026-09-18T10:00:00Z"), Payload: sdk.UserMessage{Content: "q"}}
	tr.Push(ev)
	tr.Push(ev) // 重放二次进入
	trajPushEv(t, tr, sdk.EventTurnEnd, 8, "2026-09-18T10:00:01Z", "done")
	if len(tr.Turns) != 1 {
		t.Fatalf("重复 seq 应被丢弃: %d", len(tr.Turns))
	}
	if ms, ok := tr.Turns[0].TurnMS(); !ok || ms != 1000 {
		t.Fatalf("去重后时长应正确: %d/%v", ms, ok)
	}
	// 无 seq 的 UI 侧事件不参与去重(同 seq=0 的两帧都应入账)
	tr.Reset()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	tr.Push(&sdk.SessionEvent{Kind: sdk.EventStepStart, TS: trajTS(t, "2026-09-18T10:00:02Z")})
	tr.Push(&sdk.SessionEvent{Kind: sdk.EventStepStart, TS: trajTS(t, "2026-09-18T10:00:03Z")})
	if got := len(tr.Turns[0].Steps); got != 2 {
		t.Fatalf("seq=0 帧应照常入账(两条): %d", got)
	}
}

// TestTrajAssistantAccumulate assistant 文本多步累积(与 Web 同;轨迹仅存不铺正文)。
func TestTrajAssistantAccumulate(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, tr, sdk.EventAssistantMessage, 2, "2026-09-18T10:00:01Z", sdk.AssistantMessage{Content: "第一步"})
	trajPushEv(t, tr, sdk.EventAssistantMessage, 3, "2026-09-18T10:00:02Z", sdk.AssistantMessage{Content: "第二步"})
	if got := tr.Turns[0].Assistant; got != "第一步\n\n第二步" {
		t.Fatalf("assistant 文本应累积: %q", got)
	}
}

// TestTrajReset 会话切换清空(含 seq 去重游标)。
func TestTrajReset(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 5, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	tr.Reset()
	if len(tr.Turns) != 0 || tr.cur != nil {
		t.Fatalf("Reset 应清空轨迹: %+v", tr)
	}
	// 重置后 seq 从新会话起点重新计数(小 seq 不被旧游标吞掉)
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T11:00:00Z", sdk.UserMessage{Content: "新会话"})
	if len(tr.Turns) != 1 || tr.Turns[0].User != "新会话" {
		t.Fatalf("重置后应重新计账: %+v", tr.Turns)
	}
}

// TestTrajOverview 概览:回合数/总时长/累计 token 与缓存;有未结束回合则不给总时长。
func TestTrajOverview(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q1"})
	trajPushEv(t, tr, sdk.EventUsage, 2, "2026-09-18T10:00:00.500Z", sdk.UsageEvent{Usage: sdk.Usage{PromptTokens: 100, CompletionTokens: 50, CachedTokens: 80}})
	trajPushEv(t, tr, sdk.EventTurnEnd, 3, "2026-09-18T10:00:02Z", "done")
	trajPushEv(t, tr, sdk.EventUserMessage, 4, "2026-09-18T10:01:00Z", sdk.UserMessage{Content: "q2"})
	trajPushEv(t, tr, sdk.EventUsage, 5, "2026-09-18T10:01:00.500Z", sdk.UsageEvent{Usage: sdk.Usage{PromptTokens: 200, CompletionTokens: 100, CachedTokens: 20}})
	trajPushEv(t, tr, sdk.EventTurnEnd, 6, "2026-09-18T10:01:03Z", "done")

	ov := tr.Overview()
	if ov.Turns != 2 || ov.Running || !ov.HasMS || ov.MS != 5000 {
		t.Fatalf("概览错: %+v", ov)
	}
	if ov.Tokens != 450 || ov.Cached != 100 {
		t.Fatalf("token/缓存合计错: %+v", ov)
	}
	if pct := trajCachePct(ov); pct != 22 {
		t.Fatalf("缓存占比应为 100/450=22%%: %d", pct)
	}
	// 再来一个回合不结束 → 总时长不可算,running 置位
	trajPushEv(t, tr, sdk.EventUserMessage, 7, "2026-09-18T10:02:00Z", sdk.UserMessage{Content: "q3"})
	ov = tr.Overview()
	if !ov.Running || ov.HasMS {
		t.Fatalf("进行中回合应置 running 且不给总时长: %+v", ov)
	}
	if got := trajTotalDur(ov); !strings.Contains(got, "计算中") {
		t.Fatalf("进行中总时长应显式说明而非编造: %q", got)
	}
	if pct := trajCachePct(TrajOverview{}); pct != 0 {
		t.Fatalf("零 token 时缓存占比应为 0: %d", pct)
	}
}

// TestTrajRenderShape 报告渲染:空态文案 + 关键字段(过程/成本,不含正文)。
func TestTrajRenderShape(t *testing.T) {
	empty := strings.Join(NewTraj().Render(), "\n")
	if !strings.Contains(empty, "暂无轨迹") {
		t.Fatalf("空态应显式说明: %q", empty)
	}

	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "把坞渲染改成两栏布局,顺便看看性能"})
	trajPushEv(t, tr, sdk.EventStepStart, 2, "2026-09-18T10:00:00.100Z", nil)
	trajPushEv(t, tr, sdk.EventToolCall, 3, "2026-09-18T10:00:00.200Z", sdk.ToolCallEvent{ID: "t1", Name: "file_read", Arguments: `{"path":"dock.go"}`})
	trajPushEv(t, tr, sdk.EventToolResult, 4, "2026-09-18T10:00:01Z", sdk.ToolResultEvent{CallID: "t1", Name: "file_read", Content: strings.Repeat("x", 2048)})
	trajPushEv(t, tr, sdk.EventStepEnd, 5, "2026-09-18T10:00:01.100Z", nil)
	trajPushEv(t, tr, sdk.EventUsage, 6, "2026-09-18T10:00:01.500Z", sdk.UsageEvent{Model: "deepseek-chat", Usage: sdk.Usage{PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 600}})
	trajPushEv(t, tr, sdk.EventTurnEnd, 7, "2026-09-18T10:00:02Z", "done")

	out := strings.Join(tr.Render(), "\n")
	for _, want := range []string{
		"概览", "1 回合", "总时长 2.0s", "1.2K tok", "缓存",
		"#1 ", "2.0s (done)", "1 步", "1 工具", "deepseek-chat",
		"提问", "把坞渲染改成两栏布局", "步 1", "1.0s", "file_read", "ok", "2.0KB", `{"path":"dock.go"}`,
		"时长只取事件时间戳",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("报告应含 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, strings.Repeat("x", 40)) {
		t.Fatalf("工具出参正文不应铺进轨迹(只记体积):\n%s", out)
	}
}

// TestTrajRenderRunning 进行中回合/工具在报告里显式标"进行中",不给时长。
func TestTrajRenderRunning(t *testing.T) {
	tr := NewTraj()
	trajPushEv(t, tr, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, tr, sdk.EventStepStart, 2, "2026-09-18T10:00:00.100Z", nil)
	trajPushEv(t, tr, sdk.EventToolCall, 3, "2026-09-18T10:00:00.200Z", sdk.ToolCallEvent{ID: "t1", Name: "bash"})
	out := strings.Join(tr.Render(), "\n")
	for _, want := range []string{"进行中", "计算中", "未回填 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("进行中报告应含 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "… ·") {
		t.Fatalf("进行中工具不应带时长占位: \n%s", out)
	}
}

// TestTrajFormatters 格式化口径(与 Web fmtMs/fmtBytes/clip 同语义,数字口径对齐本端 fmtK)。
func TestTrajFormatters(t *testing.T) {
	if got := trajDur(500); got != "500ms" {
		t.Fatalf("亚秒应显 ms: %q", got)
	}
	if got := trajDur(1500); got != "1.5s" {
		t.Fatalf("秒级沿用 fmtDur: %q", got)
	}
	if got := trajDur(200000); got != "3m20s" {
		t.Fatalf("分钟级应带分: %q", got)
	}
	for in, want := range map[int]string{512: "512B", 2048: "2.0KB", 2 * 1024 * 1024: "2.0MB"} {
		if got := trajBytes(in); got != want {
			t.Fatalf("trajBytes(%d) = %q,期望 %q", in, got, want)
		}
	}
	if got := trajClip("a\nb   c", 40); got != "a b c" {
		t.Fatalf("多行应折叠为单行: %q", got)
	}
	if got := trajClip(strings.Repeat("中", 80), 10); !strings.HasSuffix(got, "…") || len([]rune(got)) != 10 {
		t.Fatalf("超长应截断带省略号: %q", got)
	}
	if got := trajClip("   ", 10); got != "" {
		t.Fatalf("空白应归空: %q", got)
	}
}

// TestStateFeedsTraj 接线:State 的两条事件入口都喂轨迹,含被展示层跳过的 turn/end。
func TestStateFeedsTraj(t *testing.T) {
	s := &State{}
	user := &sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 1, TS: trajTS(t, "2026-09-18T10:00:00Z"), Payload: sdk.UserMessage{Content: "q"}}
	end := &sdk.SessionEvent{Kind: sdk.EventTurnEnd, Seq: 2, TS: trajTS(t, "2026-09-18T10:00:01Z"), Payload: "done"}
	s.ApplySessionEvent(user)
	if got := s.Traj.Overview(); got.Turns != 1 || !got.Running {
		t.Fatalf("ApplySessionEvent 应喂轨迹: %+v", got)
	}
	s.ApplySessionEvent(end)
	if ms, ok := s.Traj.Turns[0].TurnMS(); !ok || ms != 1000 {
		t.Fatalf("实时 turn/end 应结算时长: %d/%v", ms, ok)
	}

	// 重放路径:turn/end 被展示层跳过(不铺分隔行),轨迹仍须结算
	s2 := &State{}
	s2.ApplyReplay(user)
	s2.ApplyReplay(end)
	ov := s2.Traj.Overview()
	if ov.Turns != 1 || ov.Running || !ov.HasMS || ov.MS != 1000 {
		t.Fatalf("重放(含跳过的 turn/end)应结算回合: %+v", ov)
	}
	if len(s2.Lines) != 1 {
		t.Fatalf("重放仍应跳过轮次结束行: %+v", s2.Lines)
	}
	// 同帧二次进入不重复计数
	s2.ApplyReplay(user)
	if got := len(s2.Traj.Turns); got != 1 {
		t.Fatalf("重放重复帧应去重: %d", got)
	}
}

// TestTrajCommandRegistered /traj 已注册进内部命令表,并经注册表执行(与真实分发路径一致)。
func TestTrajCommandRegistered(t *testing.T) {
	a := commandTestApp()
	spec, ok := a.cmds.Get("traj")
	if !ok {
		t.Fatal("/traj 应注册进内部命令表(registerInternalCommands)")
	}
	if spec.Run == nil || !strings.Contains(spec.Usage, "/traj") {
		t.Fatalf("命令声明不完整: %+v", spec)
	}
	out, err := spec.Run(nil)
	if err != nil || !strings.Contains(out, "暂无轨迹") {
		t.Fatalf("空轨迹经注册表执行应给提示: %q/%v", out, err)
	}
	// 已在提示表里(输入 / 可见)
	for _, o := range filterHints(a.cmds.List(), "tra") {
		if o.Value == "traj" {
			return
		}
	}
	t.Fatal("/traj 应出现在 / 提示候选中")
}

// TestTrajCommandThroughDispatch 经真实分发路径(输入 /traj → command())行为一致。
func TestTrajCommandThroughDispatch(t *testing.T) {
	a := commandTestApp()
	if err := a.command("/traj"); err != nil {
		t.Fatalf("空轨迹 /traj 不应报错: %v", err)
	}
	if lines := a.model.state.Lines; len(lines) == 0 || !strings.Contains(lines[len(lines)-1].Text, "暂无轨迹") {
		t.Fatalf("应回显提示行: %+v", a.model.state.Lines)
	}
	trajPushEv(t, &a.model.state.Traj, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, &a.model.state.Traj, sdk.EventStepStart, 2, "2026-09-18T10:00:00.100Z", nil)
	trajPushEv(t, &a.model.state.Traj, sdk.EventToolCall, 3, "2026-09-18T10:00:00.200Z", sdk.ToolCallEvent{ID: "t1", Name: "bash"})
	if err := a.command("/traj"); err != nil {
		t.Fatalf("/traj 不应报错: %v", err)
	}
	doc := a.model.state.Doc
	if doc == nil || len(doc.Lines) < 3 {
		t.Fatalf("应打开轨迹浮层: %+v", doc)
	}
	if !strings.Contains(doc.Lines[0], "轨迹 · 可观测") || doc.Format != "text" {
		t.Fatalf("浮层应为轨迹文本报告: %q format=%q", doc.Lines[0], doc.Format)
	}
}

// TestCmdTrajPager 命令行为:无轨迹给提示不开浮层;有轨迹开文本 pager。
func TestCmdTrajPager(t *testing.T) {
	a := &App{model: &Model{state: &State{}}}
	out, err := a.cmdTraj(nil)
	if err != nil {
		t.Fatalf("空轨迹不应报错: %v", err)
	}
	if !strings.Contains(out, "暂无轨迹") {
		t.Fatalf("空轨迹应给提示: %q", out)
	}
	if a.model.state.Doc != nil {
		t.Fatalf("空轨迹不应开浮层")
	}

	trajPushEv(t, &a.model.state.Traj, sdk.EventUserMessage, 1, "2026-09-18T10:00:00Z", sdk.UserMessage{Content: "q"})
	trajPushEv(t, &a.model.state.Traj, sdk.EventTurnEnd, 2, "2026-09-18T10:00:01Z", "done")
	out, err = a.cmdTraj(nil)
	if err != nil {
		t.Fatalf("开浮层不应报错: %v", err)
	}
	if !strings.Contains(out, "轨迹已打开") {
		t.Fatalf("应有打开提示: %q", out)
	}
	doc := a.model.state.Doc
	if doc == nil || doc.Title != "轨迹 · 可观测" || len(doc.Lines) == 0 {
		t.Fatalf("应构造轨迹 pager: %+v", doc)
	}
	if !strings.Contains(strings.Join(doc.Lines, "\n"), "#1 ") {
		t.Fatalf("pager 内容应为轨迹报告: %+v", doc.Lines)
	}
}
