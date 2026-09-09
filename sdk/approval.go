// 审批确认服务(ctx.confirm):危险操作的用户确认通道(对齐设计 §8 审批策略)。
// 交互实现由 UI(ui-tui-app)提供;无实现时策略插件安全拒绝(不静默放行)。
package sdk

import "context"

// ConfirmService 确认服务:prompt 呈现给用户,返回用户决定。
// 实现必须尊重 ctx 取消;超时/取消按拒绝处理(安全默认)。
type ConfirmService interface {
	Confirm(ctx context.Context, prompt string) (bool, error)
}

// ApprovalMode 审批档位枚举(对齐 SandboxMode 三档先例)。
type ApprovalMode string

const (
	ApprovalOpen   ApprovalMode = "open"   // 开放:危险操作直接放行,不弹确认
	ApprovalSmart  ApprovalMode = "smart"  // 智能:命中危险模式弹确认(默认,现状行为)
	ApprovalStrict ApprovalMode = "strict" // 严格:危险操作直接拒绝,不弹窗
)

// ApprovalService 审批服务:档位查询/切换(由 policy-approval 实现,Provide ctx.approval)。
type ApprovalService interface {
	Mode() ApprovalMode
	SetMode(m ApprovalMode)
}
