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

// IMSendTarget 可投递目标(G-E5-3;D1 口径:**仅已授权者**,由实现方从访问账本枚举)。
// Key 为投递标识(用户=裸 UserID;群=裸 group openid),与 /im allowg 的参数口径一致。
type IMSendTarget struct {
	Key     string `json:"key"`
	Channel string `json:"channel,omitempty"`
	Label   string `json:"label,omitempty"` // 展示名(如 "用户 u1"/"群 g1")
	Group   bool   `json:"group,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
}

// IMControlStatus 出站控制面只读状态(im_status 工具/诊断;不含凭证)。
type IMControlStatus struct {
	Channel    string         `json:"channel"`
	Connected  bool           `json:"connected"`
	Phase      string         `json:"phase,omitempty"`
	Model      string         `json:"model,omitempty"`
	Session    string         `json:"session,omitempty"` // 当前(绑定)会话 id
	Busy       bool           `json:"busy"`
	Authorized int            `json:"authorized_users"`
	Groups     int            `json:"authorized_groups"`
	Artifacts  int            `json:"pending_artifacts,omitempty"` // 已登记待投产物数(MED-1)
	Targets    []IMSendTarget `json:"targets,omitempty"`
}

// IMControlService 受控出站面(G-E5-3;实现方 = im.Bridge,由 ui-im-* Provide "ctx.imControl")。
//
// 纪律(与「不做隐式回落」一致):
//   - **仅已授权目标**:SendText 的 target 必须 ∈ Targets(),否则显式报错(不回落 LastRoute);
//   - 出站仍走通道既有预算层/主动配额/delivery ledger(不新增旁路);
//   - 调用与结果进会话日志(模型可见即已记录);
//   - 工具对模型的暴露由插件配置决定(默认不注册)。
type IMControlService interface {
	// Status 只读状态(连接/模型/会话/忙闲/授权计数/可投目标)。
	Status() IMControlStatus
	// Targets 可投递目标(仅已授权用户 ∪ 已授权群;稳定顺序)。
	Targets() []IMSendTarget
	// SendText 向已授权目标投递文本(未授权/空文本/未装配 → 显式错误)。
	SendText(ctx context.Context, target, text string) error
}

// IMArtifact 已登记产物(MED-1;D2 口径:仅工作区内、大小受限的常规文件可登记)。
type IMArtifact struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"` // image | video | voice | file
	Mime      string    `json:"mime,omitempty"`
	Bytes     int64     `json:"bytes"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Scope     string    `json:"scope,omitempty"` // 限定的投递目标(空 = 任意已授权目标)
}

// IMAttachmentService 受控出站媒体面(MED-1;实现方 = im.Bridge,Provide "ctx.imControl" 的同一实例)。
// 纪律:① **先登记后投递**(通道只接受登记 id,模型不得凭路径外发任意文件);
// ② 登记仅限当前工作区内的常规文件 + 大小/类型白名单;③ 单次可用 + TTL;
// ④ 投递目标仍受 IMControlService 的已授权口径约束;⑤ 失败原样返回(登记条目可重试)。
type IMAttachmentService interface {
	// RegisterArtifact 登记一个待发送产物(工作区内、大小受限;幂等:同文件同版本返回同 id)。
	RegisterArtifact(ctx context.Context, path string) (IMArtifact, error)
	// SendArtifact 向已授权目标投递已登记产物(未登记/已用过/过期 → 显式错误)。
	SendArtifact(ctx context.Context, target, artifactID string) error
}

// ApprovalToolGate 可选能力(实现方 = policy-guard):该工具是否需要逐次审批。
// 供副作用工具在启动时自检"是否已接入审批链"(未接入 → 记警告,不静默假设安全)。
type ApprovalToolGate interface {
	RequiresToolApproval(name string) bool
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
