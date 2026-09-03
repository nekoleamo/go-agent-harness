// 审批确认服务(ctx.confirm):危险操作的用户确认通道(对齐设计 §8 审批策略)。
// 交互实现由 UI(ui-tui-app)提供;无实现时策略插件安全拒绝(不静默放行)。
package sdk

import "context"

// ConfirmService 确认服务:prompt 呈现给用户,返回用户决定。
// 实现必须尊重 ctx 取消;超时/取消按拒绝处理(安全默认)。
type ConfirmService interface {
	Confirm(ctx context.Context, prompt string) (bool, error)
}
