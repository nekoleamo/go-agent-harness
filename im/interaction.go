// 交互事件观察面(G-E5-4):订阅 confirm/question 的 requested↔resolved 广播,
// 对**非本渠道**处理的交互向最近活跃会话回推提示(多端并存时的同步观察:
// 用户已在 Web/TUI 作答,IM 侧不应再等着一个已作废的提问/审批)。
//
// 纪律:
//   - 只读观察,不参与裁决(呈现管道与首答生效仍归 host-confirm-fusion);
//   - Channel 为空(无呈现渠道/单测裸广播)或等于本渠道 → 静默跳过;
//   - 无活跃会话 → 静默跳过(与 HandleDocOpen 同纪律,不打扰)。
package im

import (
	"context"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// NotifyInteraction 向最近活跃会话推送一条交互通知;无活跃会话/空文本静默跳过。
// 返回是否已推送。出站经 SendText(预算层分块/截断适用)。
func (b *Bridge) NotifyInteraction(text string) bool {
	if text == "" {
		return false
	}
	route, ok := b.LastRoute()
	if !ok {
		b.diagf("interaction: 无活跃会话,跳过通知")
		return false
	}
	if err := b.sendText(context.Background(), route, text); err != nil {
		b.diagf("interaction: 通知推送失败(%v)", err)
		return false
	}
	return true
}

// WatchInteraction 订阅交互解决事件并向最近活跃会话回推「已在其它渠道处理」。
// channel 为本渠道名(如 "im-qq"/"im-wechat")。返回 Disposer(随插件卸载撤销)。
func (b *Bridge) WatchInteraction(c sdk.Ctx, channel string) sdk.Disposer {
	d1 := c.Subscribe(sdk.EventQuestionResolved, func(_ context.Context, ev *sdk.Event) error {
		qe, ok := sdk.QuestionEventOf(ev.Payload)
		if !ok || qe.Channel == "" || qe.Channel == channel {
			return nil
		}
		detail := sdk.AnswerSummary(qe.Answer)
		if qe.Err != "" {
			detail = "已取消/失败(" + qe.Err + ")"
		}
		b.NotifyInteraction("⚠ 该提问已由其它渠道(" + qe.Channel + ")处理: " + detail)
		return nil
	})
	d2 := c.Subscribe(sdk.EventConfirmResolved, func(_ context.Context, ev *sdk.Event) error {
		ce, ok := sdk.ConfirmEventOf(ev.Payload)
		if !ok || ce.Channel == "" || ce.Channel == channel {
			return nil
		}
		verdict := "已拒绝"
		if ce.OK {
			verdict = "已批准"
		}
		if ce.Err != "" && !ce.OK {
			verdict = "已取消/失败(" + ce.Err + ")"
		}
		b.NotifyInteraction("⚠ 该审批已由其它渠道(" + ce.Channel + ")处理: " + verdict)
		return nil
	})
	return func() {
		d1()
		d2()
	}
}
