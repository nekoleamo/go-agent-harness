// 审批确认服务(ctx.confirm):危险操作的用户确认通道(对齐设计 §8 审批策略)。
// 交互实现由 UI(ui-tui-app)提供;无实现时策略插件安全拒绝(不静默放行)。
package sdk

import "context"

// ConfirmService 确认服务:prompt 呈现给用户,返回用户决定。
// 实现必须尊重 ctx 取消;超时/取消按拒绝处理(安全默认)。
type ConfirmService interface {
	Confirm(ctx context.Context, prompt string) (bool, error)
}

// ConfirmPresenter 确认呈现者(P3 三端融合):单个 UI 渠道(web/tui)向用户呈现
// 一次确认并回传其应答通道。Fusion 广播给所有已注册 presenter,任一应答即生效
// (双端同卡同决策;无原生控件渠道降级文字作答由 presenter 自行处理)。
type ConfirmPresenter interface {
	// Present 呈现确认并返回应答通道(ok=true 批准)。调用方 select ch 或 ctx.Done;
	// 无论结果,结束前必须调 cancel() 清理该次呈现(超时/放弃/已应答均幂等安全)。
	Present(ctx context.Context, prompt string) (answer <-chan bool, cancel func(), err error)
}

// ConfirmFusion 融合仲裁服务(P3;Provide ctx.confirmFusion,由 host-confirm-fusion 提供)。
// UI 插件(web/tui)注册 Presenter;policy-guard 经 ctx.confirm(Fusion 本身)确认。
// 装配了 Fusion 时 UI 不再 Provide ctx.confirm(由 Fusion 统一提供),同进程并存不再冲突。
type ConfirmFusion interface {
	// Register 注册渠道呈现者;返回 Disposer 随插件卸载撤销。
	Register(channel string, p ConfirmPresenter) Disposer
}

// ApprovalMode 审批档位枚举(对齐 SandboxMode 三档先例)。
type ApprovalMode string

const (
	ApprovalOpen   ApprovalMode = "open"   // 开放:危险操作直接放行,不弹确认
	ApprovalSmart  ApprovalMode = "smart"  // 智能:命中危险模式弹确认(默认,现状行为)
	ApprovalStrict ApprovalMode = "strict" // 严格:危险操作直接拒绝,不弹窗
)

// ApprovalService 审批服务:档位查询/切换(由 policy-guard 实现,Provide ctx.approval)。
type ApprovalService interface {
	Mode() ApprovalMode
	SetMode(m ApprovalMode)
}
