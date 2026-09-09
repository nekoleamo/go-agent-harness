// Package im 提供 IM 远程控制共享运行时(IM 远程控制线 P0-2a;规划见 docs/IM_REMOTE.md)。
// 职责:把微信/QQ 等 IM 入站消息接到宿主 agent 能力面,并把回合输出回推 IM——
//
//	入站管线(gate 访问控制 → 去重 → 交互归属判定 → 命令/回合)、
//	IM ConfirmService(ctx.confirm,供 policy-guard 审批)、
//	输出聚合(回合内最后一条 assistant 文本回推)、/stop 回合取消。
//
// 本包是根下非插件运行时包(web/ 先例):只 import sdk;传输通道(ilink/qqbot/mock)注入
// Transport;真实插件壳(plugins/ui/im-*)在 P0-2b 落位(薄壳装配 + Provide ctx.confirm)。
// 安全基线:访问控制默认 disabled(静默丢弃);allowlist/pairing 二选一放行;命令权限 = 渠道访问策略。
package im

import (
	"context"
	"fmt"
	"time"
)

// Route 一条 IM 会话位置:渠道 + 发送方 + 聊天。
// 私聊 ChatID 通常等于 UserID;群聊留 ChatID 区分(群消息 P0-2b 启用)。
// 回复投递目标 = (Channel, ChatID);访问控制基于 UserID。
type Route struct {
	Channel string // 渠道名(wechat/qq/mock;诊断用)
	UserID  string // 发送方用户 id(渠道内唯一)
	ChatID  string // 回复目标聊天 id(空 = UserID)
}

// Key 会话路由稳定标识(去重/会话绑定的键;与显示名无关)。
func (r Route) Key() string { return r.Channel + "\x00" + r.ChatID }

// SenderKey 发送方标识(访问控制键)。
func (r Route) SenderKey() string { return r.Channel + "\x00" + r.UserID }

// Inbound 一条已解析的 IM 入站消息(transport 层解码后交给 Bridge)。
type Inbound struct {
	Route Route
	MsgID string // 通道消息 id(去重用;空 = 不去重)
	Text  string // 文本内容(媒体留 P0-2b)
}

// Transport 传输抽象:桥只通过它回推文本。入站方向由 transport 自行接收,
// 逐条调用 Bridge.HandleInbound(可并发;桥内部串行化回合)。
type Transport interface {
	// Name 渠道名(wechat/qq/mock)。
	Name() string
	// SendText 向会话位置发送一条文本。分块/平台格式由通道实现负责。
	SendText(ctx context.Context, to Route, text string) error
}

// TypingAware 可选能力:回合运行期间显示/取消平台"正在输入"指示(Transport 可选实现,
// best-effort——未实现则静默跳过)。用于长回合可感知(真机反馈:无法判断工作/断联)。
type TypingAware interface {
	// ShowTyping 开始显示输入状态(实现应持续至 StopTyping,平台若自动消失需周期刷新)。
	ShowTyping(ctx context.Context, route Route) error
	// StopTyping 取消输入状态(回合结束/错误路径均调用)。
	StopTyping(ctx context.Context, route Route) error
}

// Options 桥配置(data 透传 + 默认值)。
type Options struct {
	// Mode 访问模式(默认 disabled = 静默丢弃一切未授权消息)。
	Mode AccessMode
	// Allow 初始 allowlist(授权用户 SenderKey)。
	Allow []string
	// PairingTTL 配对码有效期(默认 1h)。
	PairingTTL time.Duration
	// BusyReply 回合进行中收到普通消息且队列已满时的提示(空 = 默认文案)。
	BusyReply string
	// SessionBindPath chat↔宿主会话绑定映射持久化路径(P1;空 = 仅内存不落盘)。
	SessionBindPath string
	// UnauthorizedReply pairing 模式向陌生用户回配对提示(allowlist/disabled 模式静默)。
	PairingReply func(code string) string
}

func defaultOptions() Options {
	return Options{
		Mode:       AccessDisabled,
		PairingTTL: time.Hour,
		BusyReply:  "⏳ 正在处理上一条消息,请稍候(可用 /stop 取消)。",
		PairingReply: func(code string) string {
			return fmt.Sprintf("⚠️ 未授权。配对码: %s —— 请在主机执行 /im pair %s 授权后重试。", code, code)
		},
	}
}
