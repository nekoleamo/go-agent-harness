// IM 连接契约(E 组 E0):统一「扫码 / 表单 / 仅状态」三种连接方式,替代只能表达扫码的
// IMLoginProvider(后者保留一个版本周期兼容)。
//
// 背景事实(见 docs/IM_CONNECT_PLAN.md §0/§2):
//   - 微信 iLink:**有**扫码换凭证(协议不下发二维码有效期,过期只能靠轮询发现 → 自动重取);
//   - QQ 官方 Bot:**无扫码鉴权途径**(鉴权恒为 AppID+clientSecret → getAppAccessToken),
//     故面板只能给「表单 + 即时校验 + 平台外链引导」。
//
// 契约纪律:
//   - 不回显密钥:Secret 字段的 spec 不带值,status 只回「已配置(尾号 4 位)」;
//   - 事件化:相位变化 emit EventIMConnect(载荷 IMConnectStatus),HTTP 轮询仅作兜底;
//   - 能力缺失显式:未装配 IMConnectService → 端点 503 / 面板隐藏入口(不静默降级);
//   - 载荷不含凭证(token/secret 一律不出现在 IMConnectStatus)。
package sdk

import (
	"context"
	"time"
)

// EventIMConnect IM 连接相位变化事件(载荷 IMConnectStatus)。
// 主路径:各端订阅本事件更新连接卡;POST/GET /api/im/connect/* 仅作兜底与兼容。
const EventIMConnect = "im/connect"

// IM 连接方式。
const (
	IMConnectQR   = "qr"   // 扫码(微信)
	IMConnectForm = "form" // 表单填凭证(QQ)
	IMConnectNone = "none" // 仅状态(渠道无连接能力)
)

// IM 连接相位(前端按相位渲染文案/按钮;qr 渠道另有扫码子相位)。
const (
	IMPhaseIdle           = "idle"            // 未连接
	IMPhaseWaitingScan    = "waiting_scan"    // 待扫码
	IMPhaseScanned        = "scanned"         // 已扫码待确认
	IMPhaseExpiredRefresh = "expired_refresh" // 二维码过期,正在自动重取
	IMPhaseValidating     = "validating"      // 表单凭证校验中
	IMPhaseDone           = "done"            // 已连接
	IMPhaseFailed         = "failed"          // 失败(见 Error)
)

// IMConnectOption 枚举型字段选项(如环境 official|sandbox)。
type IMConnectOption struct {
	Value string `json:"value"`
	Desc  string `json:"desc,omitempty"`
}

// IMConnectField 表单字段声明。Secret 字段不回显原值。
type IMConnectField struct {
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Secret      bool              `json:"secret,omitempty"`
	Placeholder string            `json:"placeholder,omitempty"`
	Help        string            `json:"help,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Options     []IMConnectOption `json:"options,omitempty"`    // 非空 = 枚举单选
	Configured  bool              `json:"configured,omitempty"` // 已配置(前端显示「重新填写」)
	Mask        string            `json:"mask,omitempty"`       // 已配置时的脱敏展示(尾号 4 位)
}

// IMConnectSpec 渠道连接方式声明(面板据此渲染二维码卡 / 表单 / 只读状态)。
type IMConnectSpec struct {
	Channel  string           `json:"channel"`
	Kind     string           `json:"kind"` // qr | form | none
	Fields   []IMConnectField `json:"fields,omitempty"`
	LoginURL string           `json:"login_url,omitempty"` // 平台侧创建/配置入口(白名单域)
	DocsURL  string           `json:"docs_url,omitempty"`
	Hint     string           `json:"hint,omitempty"`   // 渠道特性提示(如「官方无扫码鉴权」)
	Action   string           `json:"action,omitempty"` // 主按钮文案(扫码登录 / 保存并校验)
}

// IMConnectStatus 连接进度(事件与轮询共用同一载荷;不含凭证)。
type IMConnectStatus struct {
	Channel   string    `json:"channel"`
	Phase     string    `json:"phase"` // IMPhase*
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"` // 脱敏错误
	QRContent string    `json:"qr_content,omitempty"`
	QRPNG     string    `json:"qr_png,omitempty"` // 服务端渲染的 data URI(web 层填充)
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Account   string    `json:"account,omitempty"` // 账号摘要(尾号)
	Env       string    `json:"env,omitempty"`     // 表单渠道当前环境(如 sandbox)
}

// IMConnectService 渠道连接服务(ui-im-* 实现;web/tui 经 ctx.imChannels 类型断言发现)。
type IMConnectService interface {
	// ConnectSpec 连接方式声明(前端渲染依据)。
	ConnectSpec() IMConnectSpec
	// StartConnect 发起连接(qr:取码;form:进入 validating,空实现即可)。
	StartConnect(ctx context.Context) (IMConnectStatus, error)
	// SubmitConfig 提交表单(仅 form 渠道;values 键见 ConnectSpec.Fields)。
	SubmitConfig(ctx context.Context, values map[string]string) (IMConnectStatus, error)
	// ConnectStatus 当前连接状态(兜底轮询读同一状态机)。
	ConnectStatus() IMConnectStatus
}
