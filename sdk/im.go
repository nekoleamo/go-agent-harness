// IM 远程通道状态服务(ctx.imChannels;P3 三端融合 Web 面板)。
// 由 ui-im-wechat/ui-im-qq 插件壳 Provide:聚合渠道(wechat/qq)登录态/授权/诊断,
// 供 Web/桌面设置面板「IM 通道」区段展示(只读状态;配置/扫码仍走各通道命令)。
package sdk

import (
	"context"
	"time"
)

// IMChannelStatus 一个 IM 渠道的展示状态。
type IMChannelStatus struct {
	Channel    string `json:"channel"`    // wechat / qq
	State      string `json:"state"`      // online / running / configuring / 未配置(展示文案由前端映射)
	Detail     string `json:"detail"`     // 通道 statusText 摘要(模型/网关/授权数等)
	Error      string `json:"error"`      // lastError 脱敏诊断(空 = 无)
	Authorized int    `json:"authorized"` // 已授权 SenderKey 数
}

// IMChannelService 渠道状态查询(web server 可选注入;未装配 = 面板隐藏该区)。
type IMChannelService interface {
	Status() []IMChannelStatus
}

// IMLoginQR 扫码登录二维码(面板展示用;PNG 由 web 层渲染)。
type IMLoginQR struct {
	Channel   string    `json:"channel"`
	Content   string    `json:"content"` // 二维码内容(待编码文本/URL)
	ExpiresAt time.Time `json:"expires_at"`
}

// IMLoginState 登录进度(面板轮询):phase = idle|pending|done|failed。
type IMLoginState struct {
	Phase  string `json:"phase"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// IMGroupEntry 群维度授权条目(G-E5-2:Web 面板「群授权」区段/审计)。
// ChatID 为裸群 openid(不带渠道前缀);LastSeen 零值 = 从未收到该群消息。
type IMGroupEntry struct {
	Channel    string    `json:"channel,omitempty"`
	ChatID     string    `json:"chat_id"`
	Authorized bool      `json:"authorized"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
	Source     string    `json:"source,omitempty"` // both | authorized | seen
	Stale      bool      `json:"stale,omitempty"`  // 已授权但长期无活动(提示可清理,不自动撤销)
}

// IMGroupAccessService 可选能力:群维度授权的列举与授权/撤销(实现方=im.Bridge;G-E5-2)。
// 未实现 = 面板隐藏「群授权」区段(不静默假装支持)。
type IMGroupAccessService interface {
	// Groups 群列表(已授权 ∪ 最近活动;授权群优先,其余按最近活动倒序)。
	Groups() []IMGroupEntry
	// SetGroupAccess 授权(allow=true)/撤销(allow=false)一个群;空键/未知群撤销显式报错。
	SetGroupAccess(chatID string, allow bool) error
}

// IMLoginProvider 可选能力:渠道支持从面板发起扫码登录(当前 ui-im-wechat;
// QQ 用 AppID/AppSecret 配置,无扫码)。ctx.imChannels 实现方按需同时实现本接口,
// web 层经类型断言发现(未实现 = 面板不显示登录入口)。
type IMLoginProvider interface {
	// StartLogin 发起登录并返回二维码(已有进行中登录则返回其错误/状态)。
	StartLogin(ctx context.Context) (IMLoginQR, error)
	// LoginState 当前登录进度。
	LoginState() IMLoginState
}

// IMDisconnectProvider 可选能力:渠道支持断开连接并清理本地凭证(E3-R「/im 退出」)。
// 纪律:
//   - 只清凭证(重新登录方可再用),**不删除已授权名单**(授权是身份语义,与连接分离);
//   - 幂等:未连接时调用不得报错;
//   - 未实现 = 桥层不提供 /im logout(显式提示「该渠道不支持」,不静默假装成功)。
type IMDisconnectProvider interface {
	// Disconnect 断开当前连接并清理本地凭证(幂等)。
	Disconnect(ctx context.Context) error
}
