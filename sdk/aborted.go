// 回合中止(用户按「停止」)的裁决语义。
//
// 为何需要一个哨兵而不是各自判断 err 文本:「用户主动停止」在多处都会以错误形态冒头 ——
// 等待审批的确认被取消、web_fetch 的域名确认被取消、回合 Run 返回 context.Canceled。
// 每一处单独处理会漏,漏了的表现是**噪音**:红色错误块/blocked 前缀弹给用户,
// 而用户明明是自己点的停止(2026-10-03 实机反馈:「webfetch 一直运行中,停止后报错
// 审批未等到应答(context canceled)」「搜索报错 blocked: 策略 guard: 网页抓取确认失败」)。
//
// 约定:被中止**不是策略拒绝**。blocked(被策略拦下)要告诉模型「换个写法」,
// 中止只需要告诉模型「这轮结束了」—— 两者的重试语义相反,文案不能混。
package sdk

import (
	"errors"
	"fmt"
)

// ErrAborted 「本回合已被用户中止」的哨兵错误。
//
// 用法:裁决层遇到 ctx 取消时返回 fmt.Errorf("…: %w", ErrAborted);
// 展示层与工具结果层经 IsAborted 判别并改用中性文案。
var ErrAborted = errors.New("回合已中止(用户按了停止)")

// AbortedError 包装一条说明性文案并挂上中止哨兵。
func AbortedError(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrAborted)...)
}

// IsAborted 该错误(或其错误链)是否表示回合被用户中止。
func IsAborted(err error) bool { return errors.Is(err, ErrAborted) }
