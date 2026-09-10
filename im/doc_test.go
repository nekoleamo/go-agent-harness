// IM 侧文档预览文本降级单测(D5):最近活跃会话定位 / 摘要内容 / 失败与离线静默 /
// ctx.doc 未装配时静默跳过。
package im

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corectx "github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-docview"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestDocDegradeText(t *testing.T) {
	tx := &sdk.DocText{
		Path:       "/ws/docs/报告.md",
		Format:     sdk.DocFormatMarkdown,
		TotalLines: 120,
		Lines: []sdk.DocLine{
			{Number: 1, Text: "# 标题"},
			{Number: 2, Text: "正文一段"},
		},
		PDF:      &sdk.DocPDFFacts{PageCount: 3, Kind: "text", PagesNeedingOCR: []int{2}},
		Warnings: []string{"有页眉未解析"},
	}
	got := docDegradeText(tx, sdk.DocOpenEvent{Path: tx.Path})
	for _, want := range []string{"[文档预览] 报告.md", "markdown", "120 行", "PDF:3 页,类型 text", "1 页需 OCR", "# 标题", "正文一段", "完整内容请在 Web 端", "有页眉未解析"} {
		if !strings.Contains(got, want) {
			t.Fatalf("摘要缺少 %q:\n%s", want, got)
		}
	}
	// 页/表定位提示
	got2 := docDegradeText(tx, sdk.DocOpenEvent{Path: tx.Path, Page: 2, Sheet: 1})
	if !strings.Contains(got2, "起自第 2 页") || !strings.Contains(got2, "工作表 #1") {
		t.Fatalf("定位提示缺失:\n%s", got2)
	}
}

// 无活跃会话 → 静默跳过(不打扰)。
func TestDocOpenNoActiveSession(t *testing.T) {
	b, _, tr, _ := buildTestBridge(t, Options{Mode: AccessAllowlist})
	b.HandleDocOpen(context.Background(), sdk.DocOpenEvent{Path: "a.md"})
	if n := len(tr.sent()); n != 0 {
		t.Fatalf("无活跃会话应静默,发送了 %d 条: %+v", n, tr.sent())
	}
}

// buildDocBridge 带真实宿主上下文的桥(可注入 ctx.doc;buildTestBridge 的 ctx 为 nil)。
func buildDocBridge(t *testing.T) (*Bridge, *stubTransport) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := corectx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	tr := &stubTransport{}
	b := New(c, &stubLoop{}, sessions, tr, Options{Mode: AccessAllowlist, Allow: []string{"mock\x00owner"}})
	return b, tr
}

// 有活跃会话 + ctx.doc 装配 → 文本降级回推到该会话。
func TestDocOpenDegradesToActiveChat(t *testing.T) {
	b, tr := buildDocBridge(t)
	// 触发一次入站(allowlist 放行)以记录活跃会话;stubLoop 不产生输出 → 只更新 lastRoute
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.LastRoute(); !ok {
		t.Fatal("应记录最近活跃会话")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("# 标题\n\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := hostdocview.New(hostdocview.Options{})
	if err := b.c.Provide("ctx.doc", doc); err != nil {
		t.Fatal(err)
	}
	before := len(tr.sent())
	b.HandleDocOpen(context.Background(), sdk.DocOpenEvent{Path: p})
	sent := tr.sent()
	if len(sent) != before+1 {
		t.Fatalf("应回推一条摘要,得 %d(共 %d)", len(sent), before)
	}
	last := sent[len(sent)-1]
	if !strings.Contains(last, "note.md") || !strings.Contains(last, "# 标题") {
		t.Fatalf("摘要内容异常:\n%s", last)
	}
}

// 读取失败 → 回推结构化错误文本(不静默)。
func TestDocOpenReadFailure(t *testing.T) {
	b, tr := buildDocBridge(t)
	if err := b.HandleInbound(context.Background(), Inbound{Route: mkRoute("owner"), MsgID: "1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	doc := hostdocview.New(hostdocview.Options{})
	if err := b.c.Provide("ctx.doc", doc); err != nil {
		t.Fatal(err)
	}
	before := len(tr.sent())
	b.HandleDocOpen(context.Background(), sdk.DocOpenEvent{Path: filepath.Join(t.TempDir(), "missing.md")})
	sent := tr.sent()
	if len(sent) != before+1 || !strings.Contains(sent[len(sent)-1], "文档预览失败") {
		t.Fatalf("失败应回推错误文本: %+v", sent)
	}
}
