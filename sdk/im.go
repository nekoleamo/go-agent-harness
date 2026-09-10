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
	Channel  string `json:"channel"`  // wechat / qq
	State    string `json:"state"`    // online / running / configuring / 未配置(展示文案由前端映射)
	Detail   string `json:"detail"`   // 通道 statusText 摘要(模型/网关/授权数等)
	Error    string `json:"error"`    // lastError 脱敏诊断(空 = 无)
	Authorized int  `json:"authorized"` // 已授权 SenderKey 数
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

// IMLoginProvider 可选能力:渠道支持从面板发起扫码登录(当前 ui-im-wechat;
// QQ 用 AppID/AppSecret 配置,无扫码)。ctx.imChannels 实现方按需同时实现本接口,
// web 层经类型断言发现(未实现 = 面板不显示登录入口)。
type IMLoginProvider interface {
	// StartLogin 发起登录并返回二维码(已有进行中登录则返回其错误/状态)。
	StartLogin(ctx context.Context) (IMLoginQR, error)
	// LoginState 当前登录进度。
	LoginState() IMLoginState
}
