// host-session-log 测试:投影/持久化/历史注入 + 压缩器注入整合(token 压缩算法本身在 token-compress)。
package sessionlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// captureCtx 最小 sdk.Ctx:捕获 session 事件载荷(验证广播回填 Seq/TS,不依赖 core)。
type captureCtx struct{ got []sdk.SessionEvent }

func (c *captureCtx) Provide(string, any) error                      { return nil }
func (c *captureCtx) Inject(string, any) error                       { return nil }
func (c *captureCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }
func (c *captureCtx) Emit(_ context.Context, name string, payload any, _ sdk.DispatchMode) (any, error) {
	if name == sdk.EventSession {
		if ev, ok := payload.(*sdk.SessionEvent); ok {
			c.got = append(c.got, *ev)
		}
	}
	return nil, nil
}
func (c *captureCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubCompressor 测试压缩器:一次把水位后所有事件折叠为一条摘要(最简单折叠)。
// 用于验证 host-session-log 的注入整合(水位推进/摘要置顶/完整日志留盘)。
type stubCompressor struct {
	calls int
}

func (s *stubCompressor) Fold(evs []sdk.SessionEvent, watermark int, budget int, summary func(string)) int {
	s.calls++
	// 折叠水位后事件至最后一个用户轮之前(保留最新轮完整)
	start := watermark + 1
	end := lastUserIndexStub(evs) - 1
	if end < start {
		return watermark
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		switch p := evs[i].Payload.(type) {
		case sdk.UserMessage:
			b.WriteString("用户: " + p.Content[:min(20, len(p.Content))] + ";")
		case sdk.AssistantMessage:
			b.WriteString("助手;")
		case sdk.ToolResultEvent:
			b.WriteString("工具;")
		}
	}
	summary("stub:" + b.String())
	return end
}

func lastUserIndexStub(evs []sdk.SessionEvent) int {
	last := -1
	for i, ev := range evs {
		if ev.Kind == sdk.EventUserMessage {
			last = i
		}
	}
	return last
}

// appendTurn 一轮会话:用户问 + 助手答 + 工具结果。
func appendTurn(l *Log, round int) {
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{
		Content: "用户问题 第" + strings.Repeat("行", 40) + itoa(round)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{
		Content: "助手回答 第" + strings.Repeat("果", 40) + itoa(round)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c" + itoa(round), Name: "shell", Content: "输出" + strings.Repeat("o", 30)}})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestDeriveAttachments 附件随 UserMessage 传递到 LLMMessage(附件一期)。
func TestDeriveAttachments(t *testing.T) {
	l := newLog("")
	atts := []sdk.Attachment{{Kind: sdk.AttachmentImage, Name: "a.png", MimeType: "image/png", Rel: "20260906-000000/a.png"}}
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "看图", Attachments: atts}}); err != nil {
		t.Fatal(err)
	}
	msgs := l.DeriveMessages()
	if len(msgs) != 1 || msgs[0].Role != sdk.RoleUser {
		t.Fatalf("投影异常: %+v", msgs)
	}
	if len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].Rel != atts[0].Rel {
		t.Fatalf("附件未传递: %+v", msgs[0].Attachments)
	}
}

// TestFullProjectionNoCompressor 未注册压缩器:全量投影,行为与旧版一致。
func TestFullProjectionNoCompressor(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 5; i++ {
		appendTurn(l, i)
	}
	msgs := l.DeriveMessages()
	if len(msgs) != 15 { // 5 轮 × 3 条
		t.Fatalf("应全量投影: %d", len(msgs))
	}
}

// TestCompressorInjectIntegrate 压缩器注入整合:超预算触发 Fold,摘要置顶、压缩块跳过、完整日志留盘。
func TestCompressorInjectIntegrate(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 6; i++ {
		appendTurn(l, i)
	}
	stub := &stubCompressor{}
	l.RegisterCompressor(200, stub)
	msgs := l.DeriveMessages()
	if stub.calls == 0 {
		t.Fatal("超预算应触发 Fold")
	}
	// 摘要置顶(system)
	if len(msgs) == 0 || msgs[0].Role != sdk.RoleSystem || !strings.Contains(msgs[0].Content, "stub:") {
		t.Fatalf("摘要应置顶: %+v", msgs)
	}
	// 最新轮保留(末尾为第 6 轮工具结果)
	last := msgs[len(msgs)-1]
	if last.Role != sdk.RoleTool || last.ToolCallID != "c6" {
		t.Fatalf("最新轮应保留: %+v", msgs)
	}
	// 完整日志留盘:全部用户事件 + 摘要事件
	events := l.Replay()
	var userCount, summaryCount int
	for _, ev := range events {
		switch ev.Kind {
		case sdk.EventUserMessage:
			userCount++
		case sdk.EventSummary:
			summaryCount++
		}
	}
	if userCount != 6 {
		t.Fatalf("完整日志应保留 6 轮用户事件: %d", userCount)
	}
	if summaryCount == 0 {
		t.Fatal("应有摘要事件落盘")
	}
}

// TestCompressorWithinBudget 预算充足:不触发折叠,无摘要。
func TestCompressorWithinBudget(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 3; i++ {
		appendTurn(l, i)
	}
	stub := &stubCompressor{}
	l.RegisterCompressor(1000000, stub)
	msgs := l.DeriveMessages()
	if stub.calls != 0 {
		t.Fatal("预算充足不应触发 Fold")
	}
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem {
			t.Fatal("预算充足不应出现摘要")
		}
	}
}

// TestHistoryInjection 历史注入:-1 禁止 / N 最近 N 条消息(与压缩器无关的基础语义)。
func TestHistoryInjection(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 3; i++ {
		appendTurn(l, i)
	}
	l.SetHistory(2)
	msgs := l.DeriveMessages()
	if len(msgs) != 2 { // 最近 2 条消息
		t.Fatalf("应保留最近 2 条消息: %d", len(msgs))
	}
	l.SetHistory(-1)
	if msgs := l.DeriveMessages(); msgs != nil {
		t.Fatalf("-1 应禁止注入: %+v", msgs)
	}
}

// TestSummaryEventProjection 摘要事件消费:水位内摘要恒置顶(令牌压缩后多次投影稳定)。
func TestSummaryEventProjection(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 4; i++ {
		appendTurn(l, i)
	}
	stub := &stubCompressor{}
	l.RegisterCompressor(150, stub)
	first := l.DeriveMessages()
	second := l.DeriveMessages()
	// 已折叠完成,再次投影不应重复折叠(水位推进后预算内)
	if stub.calls > 2 {
		t.Fatalf("重复投影不应反复折叠: %d 次", stub.calls)
	}
	if len(first) != len(second) {
		t.Fatalf("折叠完成后投影应稳定: 前 %d 后 %d", len(first), len(second))
	}
	if first[0].Role != sdk.RoleSystem || second[0].Role != sdk.RoleSystem {
		t.Fatalf("摘要应持续置顶: %+v / %+v", first, second)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestLoadRestoresHistory Load 恢复:jsonl 事件读入、seq 从历史顶续接(新事件不复用序号)。
func TestLoadRestoresHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	l := newLog(path)
	// 先写两轮事件(seq 1,2,3:用户/助手/轮末)
	appendTurn(l, 1)
	l.Flush()
	l.Close()

	// 新 Log(模拟重启)→ Load 恢复
	l2 := newLog("")
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	ev := l2.Replay()
	if len(ev) < 2 {
		t.Fatalf("应恢复历史事件: %d", len(ev))
	}
	if ev[0].Kind != sdk.EventUserMessage {
		t.Fatalf("首事件应为用户消息: %+v", ev[0])
	}
	// seq 接续:追加新事件 Seq 应大于历史最大值
	l2.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "继续"}})
	last := l2.Replay()
	if last[len(last)-1].Seq <= ev[len(ev)-1].Seq {
		t.Fatalf("seq 应接续: 历史 max=%d,新=%d", ev[len(ev)-1].Seq, last[len(last)-1].Seq)
	}
}

// TestLoadMissingFile 文件不存在 = 新会话(幂等,不报错)。
func TestLoadMissingFile(t *testing.T) {
	l := newLog("")
	path := filepath.Join(t.TempDir(), "none.jsonl")
	if err := l.Load(path); err != nil {
		t.Fatal(err)
	}
	if ev := l.Replay(); len(ev) != 0 {
		t.Fatalf("新会话应无历史: %+v", ev)
	}
}

// TestLoadToleratesBadLines 坏行容忍:非法 json 行跳过,不中断恢复。
func TestLoadToleratesBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	content := "{not-json}\n" +
		"{\"Kind\":\"user/message\",\"Seq\":2,\"Payload\":{\"Content\":\"hi\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newLog("")
	if err := l.Load(path); err != nil {
		t.Fatal(err)
	}
	ev := l.Replay()
	if len(ev) != 1 || ev[0].Seq != 2 {
		t.Fatalf("坏行应跳过,好行恢复: %+v", ev)
	}
}

// TestLoadLongLineRestores 超长有效事件行(web_fetch 正文 JSON 转义膨胀,实测 ~1.9MB):
// 旧上限 1MB 会 token too long 拖垮 boot——新上限 16MB 应正常恢复。
func TestLoadLongLineRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	ev := sdk.SessionEvent{Kind: sdk.EventToolResult, Seq: 2,
		Payload: sdk.ToolResultEvent{CallID: "c1", Name: "web_fetch", Content: strings.Repeat("x", 2<<20)}} // 2MB 内容
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	content := string(line) + "\n" + "{\"Kind\":\"user/message\",\"Seq\":3,\"Payload\":{\"Content\":\"hi\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newLog("")
	if err := l.Load(path); err != nil {
		t.Fatalf("超长有效行应恢复: %v", err)
	}
	evts := l.Replay()
	if len(evts) != 2 || evts[1].Seq != 3 {
		t.Fatalf("应恢复超长行与后续行: %v", len(evts))
	}
	// normalize 后 Payload 应为具体类型(ToolResultEvent)且内容长度保留
	if p, ok := evts[0].Payload.(sdk.ToolResultEvent); ok {
		if len(p.Content) != 2<<20 {
			t.Fatal("超长行内容应完整")
		}
	} else {
		t.Fatalf("Payload 应还原为 ToolResultEvent,实际 %T", evts[0].Payload)
	}
}

// TestLoadOversizeLineTolerated 极端超限单行(>16MB):坏行容忍——不报错,恢复已读部分。
func TestLoadOversizeLineTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	content := "{\"Kind\":\"user/message\",\"Seq\":1,\"Payload\":{\"Content\":\"hi\"}}\n" +
		strings.Repeat("a", 17<<20) + "\n" + // 17MB 超限行
		"{\"Kind\":\"user/message\",\"Seq\":2,\"Payload\":{\"Content\":\"hi2\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newLog("")
	if err := l.Load(path); err != nil {
		t.Fatalf("超限行应容忍不报错: %v", err)
	}
	evts := l.Replay()
	if len(evts) != 1 || evts[0].Seq != 1 {
		t.Fatalf("应恢复超限行前的部分: %+v", evts)
	}
}

// TestHistoryPersistence SetHistory 持久化 sidecar:重启(新 Log Load)后恢复。
func TestHistoryPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	l := newLog(path)
	l.SetHistory(5) // 落盘 sidecar
	// 模拟重启:新 Log Load → historyLimit 恢复为 5
	l2 := newLog("")
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	l2.mu.Lock()
	got := l2.historyLimit
	l2.mu.Unlock()
	if got != 5 {
		t.Fatalf("重启后 historyLimit 应恢复 5,got %d", got)
	}
	// 再次修改 → 覆盖持久化;Load 反映新值
	l2.SetHistory(2)
	l3 := newLog("")
	if err := l3.Load(path); err != nil {
		t.Fatal(err)
	}
	l3.mu.Lock()
	got = l3.historyLimit
	l3.mu.Unlock()
	if got != 2 {
		t.Fatalf("覆盖后应恢复 2,got %d", got)
	}
	// 无 sidecar(新会话):默认 0 = 全部
	l4 := newLog("")
	if err := l4.Load(filepath.Join(t.TempDir(), "none.jsonl")); err != nil {
		t.Fatal(err)
	}
	l4.mu.Lock()
	got = l4.historyLimit
	l4.mu.Unlock()
	if got != 0 {
		t.Fatalf("无 sidecar 应 0(全部),got %d", got)
	}
}

// TestHistorySidecarPath sidecar 路径:与主 path 同目录同 basename。
func TestHistorySidecarPath(t *testing.T) {
	if got := historySidecar(""); got != "" {
		t.Fatalf("内存会话无 sidecar: %q", got)
	}
	got := historySidecar("/a/b/sess.jsonl")
	if got != "/a/b/sess.jsonl.history" {
		t.Fatalf("sidecar 路径: %q", got)
	}
}

// TestAppendBroadcastStampsSeqTS 广播载荷必须已回填 Seq/TS(web/events.go 依赖它生成
// 帧 id 与断线续传游标;曾广播未回填副本 → 帧 id/TS 恒 0、重连全量重放)。
func TestAppendBroadcastStampsSeqTS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	l := newLog(dir)
	l.SetPath(path)
	c := &captureCtx{}
	l.ctx = c
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if len(c.got) != 1 {
		t.Fatalf("应广播 1 条 session 事件: %d", len(c.got))
	}
	if c.got[0].Seq == 0 {
		t.Fatal("广播载荷 Seq 必须已回填(>0),否则 Web 帧 id 恒 0")
	}
	if c.got[0].TS.IsZero() {
		t.Fatal("广播载荷 TS 必须已回填,否则前端时间戳为零值")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.SplitN(strings.TrimRight(string(raw), "\n"), "\n", 2)[0]
	var disk sdk.SessionEvent
	if err := json.Unmarshal([]byte(line), &disk); err != nil {
		t.Fatalf("落盘首行必须可解析: %v", err)
	}
	if disk.Seq != c.got[0].Seq {
		t.Fatalf("广播 Seq(%d) 必须与落盘同源(%d)", c.got[0].Seq, disk.Seq)
	}
}

// TestLoadRepairsTrailingPartialLine 尾部残行(断电半写)必须修复,否则下一条 Append
// (O_APPEND)会与残行粘成一行坏行,残行与紧随的新事件双双静默丢失。
func TestLoadRepairsTrailingPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	good, err := json.Marshal(sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 1, TS: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// 完整行 + 半截行(无终止符且 json 不完整)
	if err := os.WriteFile(path, append(append(good, '\n'), []byte(`{"kind":"user/message","seq":2,`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLog(dir)
	if err := l.Load(path); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "b"}}); err != nil {
		t.Fatal(err)
	}
	// 重载:新事件必须仍在(未被残行粘坏丢弃)
	l2 := newLog(dir)
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	evs := l2.Replay()
	if len(evs) != 2 {
		t.Fatalf("残行修复后应保留 2 条事件(1 完整 + 1 新),实得 %d", len(evs))
	}
	if evs[1].Seq <= evs[0].Seq {
		t.Fatalf("seq 必须递增: %d → %d", evs[0].Seq, evs[1].Seq)
	}
}

// TestLoadKeepsCompleteTrailingLine 末行完整但缺终止符(崩溃前未写 \n)时补终止符,不丢事件。
func TestLoadKeepsCompleteTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	good, err := json.Marshal(sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 7, TS: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, good, 0o600); err != nil { // 无终止符
		t.Fatal(err)
	}
	l := newLog(dir)
	if err := l.Load(path); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "next"}}); err != nil {
		t.Fatal(err)
	}
	l2 := newLog(dir)
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	if evs := l2.Replay(); len(evs) != 2 {
		t.Fatalf("补终止符后应保留 2 条事件,实得 %d", len(evs))
	}
}

// TestLoadFailureKeepsState 非 NotExist 的读失败不得改变现状(避免"已切路径 + 空历史"分叉)。
func TestLoadFailureKeepsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	l := newLog(dir)
	l.SetPath(path)
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "keep"}}); err != nil {
		t.Fatal(err)
	}
	// 目标为目录:EISDIR 类失败必须不改动现有历史与路径
	if err := l.Load(dir); err == nil {
		t.Fatal("读目录应显式失败")
	}
	if evs := l.Replay(); len(evs) != 1 {
		t.Fatalf("失败后历史必须保持(%d)", len(evs))
	}
	if l.path != path {
		t.Fatalf("失败后路径必须保持: %s", l.path)
	}
}
