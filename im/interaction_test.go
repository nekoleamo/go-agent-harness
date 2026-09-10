// G-E5-4 交互事件观察面单测:其它渠道已处理的提问/审批 → 回推提示;本渠道/空渠道/无活跃会话静默。
package im

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildInteractionBridge 构造可观察出站的桥(真 ctx + sessionlog stub 底座)。
func buildInteractionBridge(t *testing.T) (*Bridge, sdk.Ctx, *stubTransport) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	tr := &stubTransport{}
	b := New(c, &stubLoop{}, sessions, tr, Options{
		Mode: AccessAllowlist, Allow: []string{"mock\x00owner"},
	})
	return b, c, tr
}

func TestWatchInteractionNotifiesForeignResolution(t *testing.T) {
	b, c, tr := buildInteractionBridge(t)
	dis := b.WatchInteraction(c, "im-qq")
	defer dis()

	// 无活跃会话 → 静默跳过(不 panic、不出站)
	c.Emit(context.Background(), sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: sdk.Question{ID: "q1"}, Answer: sdk.QuestionAnswer{Values: []string{"a"}},
		Resolved: true, Channel: "web",
	}, sdk.Emit)
	if got := tr.sent(); len(got) != 0 {
		t.Fatalf("无活跃会话应静默: %+v", got)
	}

	// 有活跃会话:其它渠道作答 → 回推提示(带答案摘要)
	b.setLastRoute(Route{Channel: "mock", UserID: "owner", ChatID: "owner"})
	c.Emit(context.Background(), sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: sdk.Question{ID: "q1"}, Answer: sdk.QuestionAnswer{Values: []string{"prod"}, Text: "备注"},
		Resolved: true, Channel: "web",
	}, sdk.Emit)
	sent := tr.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "其它渠道(web)") || !strings.Contains(sent[0], "prod , 备注") {
		t.Fatalf("应回推外部作答提示: %+v", sent)
	}
	// 无作答 + 错误 → 取消/失败文案
	c.Emit(context.Background(), sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: sdk.Question{ID: "q2"}, Resolved: true, Channel: "tui", Err: "context canceled",
	}, sdk.Emit)
	sent = tr.sent()
	if len(sent) != 2 || !strings.Contains(sent[1], "已取消/失败") {
		t.Fatalf("取消文案异常: %+v", sent)
	}

	// 本渠道结论 / 空渠道 → 静默(自己答的不用通知自己)
	c.Emit(context.Background(), sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: sdk.Question{ID: "q3"}, Answer: sdk.QuestionAnswer{Text: "x"}, Resolved: true, Channel: "im-qq",
	}, sdk.Emit)
	c.Emit(context.Background(), sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: sdk.Question{ID: "q4"}, Resolved: true,
	}, sdk.Emit)
	c.Emit(context.Background(), sdk.EventQuestionResolved, "无关载荷", sdk.Emit)
	if got := tr.sent(); len(got) != 2 {
		t.Fatalf("本渠道/空渠道应静默: %+v", got)
	}
}

func TestWatchInteractionConfirmResolution(t *testing.T) {
	b, c, tr := buildInteractionBridge(t)
	dis := b.WatchInteraction(c, "im-wechat")
	defer dis()
	b.setLastRoute(Route{Channel: "mock", UserID: "owner", ChatID: "owner"})

	c.Emit(context.Background(), sdk.EventConfirmResolved, sdk.ConfirmEvent{Prompt: "rm -rf?", OK: true, Resolved: true, Channel: "tui"}, sdk.Emit)
	c.Emit(context.Background(), sdk.EventConfirmResolved, &sdk.ConfirmEvent{Prompt: "git push?", OK: false, Resolved: true, Channel: "web"}, sdk.Emit)
	// 本渠道 + 空渠道 → 静默
	c.Emit(context.Background(), sdk.EventConfirmResolved, sdk.ConfirmEvent{Prompt: "x", OK: true, Resolved: true, Channel: "im-wechat"}, sdk.Emit)
	c.Emit(context.Background(), sdk.EventConfirmResolved, sdk.ConfirmEvent{Prompt: "y", OK: true, Resolved: true}, sdk.Emit)
	sent := tr.sent()
	if len(sent) != 2 {
		t.Fatalf("应只回推 2 条外部裁决: %+v", sent)
	}
	if !strings.Contains(sent[0], "已批准") || !strings.Contains(sent[0], "其他渠道") && !strings.Contains(sent[0], "其它渠道(tui)") {
		t.Fatalf("批准文案异常: %q", sent[0])
	}
	if !strings.Contains(sent[1], "已拒绝") || !strings.Contains(sent[1], "其它渠道(web)") {
		t.Fatalf("拒绝文案异常: %q", sent[1])
	}
	// 撤销后不再回推
	dis()
	c.Emit(context.Background(), sdk.EventConfirmResolved, sdk.ConfirmEvent{Prompt: "z", OK: true, Resolved: true, Channel: "tui"}, sdk.Emit)
	if got := tr.sent(); len(got) != 2 {
		t.Fatalf("撤销后不应再回推: %+v", got)
	}
}

func TestNotifyInteractionEdgeCases(t *testing.T) {
	b, _, tr := buildInteractionBridge(t)
	if b.NotifyInteraction("") {
		t.Fatal("空文本应返回 false")
	}
	if b.NotifyInteraction("hello") {
		t.Fatal("无活跃会话应返回 false")
	}
	b.setLastRoute(Route{Channel: "mock", UserID: "owner", ChatID: "owner"})
	if !b.NotifyInteraction("hello") {
		t.Fatal("有活跃会话应推送成功")
	}
	if got := tr.sent(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("出站异常: %+v", got)
	}
}
