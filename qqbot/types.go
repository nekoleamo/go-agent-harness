// Package qqbot 提供 QQ 官方 Bot v2(api-v2)客户端,IM 远程控制线 P0-2b-QQ(T1 协议壳)。
// 协议基线:QQ 开放平台官方文档(bot.q.qq.com,api-v2),已按官方 OpenAPI 核实:
//   - 接入:AppID/AppSecret 经 https://bots.qq.com/app/getAppAccessToken 换取 access_token
//     (有效期 7200s;到期前 60s 内重复获取会返回新 token,旧 token 60s 内仍有效)
//   - 收:WS gateway(先 GET /gateway/bot 拿 wss 地址)→ opcode 2 Identify(token="QQBot <access_token>",
//     Intents GROUP_AND_C2C_EVENT=1<<25)→ Hello(op 10,按 d.heartbeat_interval 心跳)→
//     Dispatch(op 0)投递 C2C_MESSAGE_CREATE/GROUP_AT_MESSAGE_CREATE;断线 opcode 6 Resume(带 session_id+seq)
//   - 发:REST POST /v2/users/{user_openid}/messages(单聊)/ /v2/groups/{group_openid}/messages(群);
//     被动回复带 msg_id(事件 d.id,5 分钟内有效)+ msg_seq(与 msg_id 联合幂等,相同重复发送失败)
//   - 业务错误以 HTTP body {code, message} 返回(必须显式校验;40034100 主动频控/40034128 被动超限/
//     40054005 消息去重),T5 配额记账按此分类
//
// 本包只做协议与凭证存取;消息路由/回合驱动/审批在 im.Bridge,传输适配在 plugins/ui/ui-im-qq(T2)。
// 鉴权用 access_token(官方已废弃旧式 "Bot AppID.AppSecret" 直鉴权,仅 WS Identify 兼容保留)。
package qqbot

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DefaultBaseURL OpenAPI 根(官方统一为 api.bot.qq.com;旧域名 api.sgroup.qq.com 等价)。
const DefaultBaseURL = "https://api.bot.qq.com"

// DefaultTokenURL access_token 换取端点(不区分正式/沙箱环境)。
const DefaultTokenURL = "https://bots.qq.com/app/getAppAccessToken"

// ErrNotConfigured 未填 AppID/AppSecret(需先 /qq login 配置)。
var ErrNotConfigured = errors.New("qqbot: 未配置 AppID/AppSecret,请先执行 /qq login")

// IntentsGroupAndC2CEvent 事件订阅位:单聊(C2C)+ 群(@)消息事件(1<<25)。
// 覆盖 C2C_MESSAGE_CREATE / GROUP_AT_MESSAGE_CREATE / FRIEND_ADD 等,私域单聊场景足够。
const IntentsGroupAndC2CEvent = 1 << 25

// OpenAPI 业务错误码(发送消息接口;T5 配额记账/降级按此分类)。
const (
	CodePassiveExpired = 40034128 // 被动回复时间或次数超限
	CodeMsgIDExpired   = 40034005 // 回复消息 msg_id 已过期
	CodeRateLimited    = 40034100 // 主动消息发送超过频控限制
	CodeDeduped        = 40054005 // 消息被去重(相同 msg_id + msg_seq)
)

// APIError REST 业务错误(OpenAPI 以 HTTP body {code, message} 返回错误;HTTP 429 亦按频控归类)。
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("qqbot: API 错误 code=%d message=%q", e.Code, e.Message)
}

// IsPassiveExpired 是否被动回复时效/次数超限(40034128/40034005;不应重试,并入下次被动或主动降级)。
func IsPassiveExpired(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code == CodePassiveExpired || ae.Code == CodeMsgIDExpired
	}
	return false
}

// IsRateLimited 是否发送频控(40034100 或 HTTP 429;应退避/记账,不自动轰炸重试)。
func IsRateLimited(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code == CodeRateLimited
	}
	return err != nil && contains(err.Error(), "429")
}

// IsDeduped 是否 msg_id+msg_seq 重复被服务端去重(40054005;幂等语义,可安全视为"已发送")。
func IsDeduped(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code == CodeDeduped
	}
	return false
}

// Credentials QQ 机器人接入凭证(开放平台管理端获得;openid 由事件可得,不入库)。
// 便携纪律:入 $GAH_HOME/config/qqbot.yaml(0600,随目录迁移;与 ilink.Credentials 同型)。
type Credentials struct {
	AppID     string   `yaml:"app_id"`
	AppSecret string   `yaml:"app_secret"`
	BaseURL   string   `yaml:"base_url,omitempty"` // OpenAPI 根(默认 https://api.bot.qq.com;沙箱联调可指 sandbox.api.sgroup.qq.com)
	Allow     []string `yaml:"allow,omitempty"`    // 已授权 SenderKey(channel\0user),与 ilink 同构(T6 接线)
}

// ---- WS gateway 帧(与官方 opcode 表一致)----

// WS opcode(服务端/客户端双向;仅取本包用到子集)。
const (
	OpDispatch       = 0  // 服务端事件推送
	OpHeartbeat      = 1  // 心跳(携带最后收到的 seq;首连传 null)
	OpIdentify       = 2  // 客户端鉴权
	OpResume         = 6  // 断线恢复
	OpReconnect      = 7  // 服务端要求重连
	OpInvalidSession = 9  // 鉴权/恢复参数错误
	OpHello          = 10 // 连接建立后首条(含心跳周期)
	OpHeartbeatAck   = 11 // 心跳应答
)

// EventType WS Dispatch 事件 t 字段(本通道订阅子集)。
const (
	EventC2CMessage = "C2C_MESSAGE_CREATE"      // 单聊消息
	EventGroupAtMsg = "GROUP_AT_MESSAGE_CREATE" // 群 @ 消息(仅 @ 机器人推送)
	EventReady      = "READY"
	EventResumed    = "RESUMED"
)

// Hello Hello 载荷(heartbeat_interval 毫秒;以服务端下发为准,勿硬编码 41257)。
type Hello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// Ready Ready 载荷(Identify 成功后下发;session_id 供断线 Resume;机器人信息 User 可选)。
type Ready struct {
	SessionID string `json:"session_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// WSFrame 统一帧:下行 Dispatch 带 t(事件类型)/s(seq)/d(载荷)/id(事件 ID);
// 上行 Heartbeat/Identify/Resume 只用到 op 与 d(t/s/id 为空)。
type WSFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"` // 下行 seq(心跳需回显最后收到的 s)
	T  string          `json:"t,omitempty"`
	ID string          `json:"id,omitempty"` // 事件 ID(被动回复可选 event_id)
}

// Event 已解析的 Dispatch 事件(交给 Handler;data 为 d 字段原文,按 Type 断言)。
type Event struct {
	Type string // EventType(READY/RESUMED/C2C_.../GROUP_...)
	ID   string // 事件 ID(最外层 id;被动回复 event_id 用)
	Seq  int64  // 事件序号(downstream s;心跳回显)
	Data json.RawMessage
}

// ---- 入站消息事件 d 字段 ----

// Author 消息作者(单聊取 user_openid;群聊取 member_openid)。
type Author struct {
	UserOpenID   string `json:"user_openid,omitempty"`
	MemberOpenID string `json:"member_openid,omitempty"`
}

// C2CMessage C2C_MESSAGE_CREATE 事件 d 字段(单聊)。
// ID = 消息 ID(<->被动回复 msg_id;5 分钟内有效)。
type C2CMessage struct {
	ID        string `json:"id"`
	Author    Author `json:"author"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// GroupAtMessage GROUP_AT_MESSAGE_CREATE 事件 d 字段(群 @;官方群事件本就只推 @ 机器人)。
type GroupAtMessage struct {
	ID          string    `json:"id"`
	Author      Author    `json:"author"`
	GroupOpenID string    `json:"group_openid"`
	Content     string    `json:"content"`
	Timestamp   string    `json:"timestamp"`
	Mentions    []Mention `json:"mentions,omitempty"`
}

// Mention @ 提及(群消息中 @ 机器人段;含机器人自身与其它 @ 用户)。
type Mention struct {
	ID           string `json:"id"`
	UserOpenID   string `json:"user_openid,omitempty"`
	MemberOpenID string `json:"member_openid,omitempty"`
}

// ---- 出站消息模型(发送消息接口)----

// 消息类型(决定哪个内容字段生效)。
const (
	MsgTypeText     = 0 // 纯文本(content)
	MsgTypeMarkdown = 2 // Markdown(markdown.content)
	MsgTypeImage    = 3 // 图片(url)
	MsgTypeAudio    = 4 // 音频(url)
	MsgTypeInput    = 6 // 输入中状态(input_notify)
	MsgTypeMedia    = 7 // 富媒体(media.file_info,需先上传)
)

// SendMessage 发送消息请求体(单聊/群共用;字段按 msg_type 组合,omitempty 输出)。
// msg_type 必填(官方以此决定内容字段生效),故不 omitempty;0=文本也须显式带上。
type SendMessage struct {
	MsgType     int          `json:"msg_type"`
	Content     string       `json:"content,omitempty"`      // msg_type=0 文本全文(填 markdown 时须为空)
	Markdown    *Markdown    `json:"markdown,omitempty"`     // msg_type=2
	Keyboard    *Keyboard    `json:"keyboard,omitempty"`     // 内嵌键盘(短形式传 id,长形式传 content.rows)
	MsgID       string       `json:"msg_id,omitempty"`       // 被动回复消息 ID(事件 d.id;5min 有效)
	EventID     string       `json:"event_id,omitempty"`     // 被动回复事件 ID(与 msg_id 二选一)
	MsgSeq      uint64       `json:"msg_seq,omitempty"`      // 回复序号(与 msg_id 联合幂等,重复发送失败)
	InputNotify *InputNotify `json:"input_notify,omitempty"` // msg_type=6 输入中状态
	Media       *MediaInfo   `json:"media,omitempty"`        // msg_type=7 富媒体
}

// Markdown Markdown 消息内容(msg_type=2)。
type Markdown struct {
	Content string `json:"content"`
}

// Keyboard 内嵌键盘(确认/选择交互,T4 用;短形式 id 为平台模板,长形式 content 内联)。
type Keyboard struct {
	ID      string           `json:"id,omitempty"`
	Content *KeyboardContent `json:"content,omitempty"`
}

// KeyboardContent 长形式键盘内容:行 × 按钮。
type KeyboardContent struct {
	Rows []KeyboardRow `json:"rows"`
}

// KeyboardRow 一行按钮(每行 ≤5 个,总行 ≤5)。
type KeyboardRow struct {
	Buttons []KeyboardButton `json:"buttons"`
}

// KeyboardButton 按钮(id 为回调数据;T4 按 id 回填确认结果)。
type KeyboardButton struct {
	ID         string       `json:"id"`
	RenderData ButtonRender `json:"render_data"`
	Action     ButtonAction `json:"action"`
}

// ButtonRender 按钮外观。
type ButtonRender struct {
	Label string `json:"label"`           // 展示文字
	Style int    `json:"style,omitempty"` // 1 灰/2 蓝/3 绿/4 红(默认 1)
}

// ButtonAction 按钮行为。
type ButtonAction struct {
	Type       int              `json:"type"` // 2=回调交互
	Permission ButtonPermission `json:"permission,omitempty"`
	Data       string           `json:"data,omitempty"` // 透传数据(与回调事件 data 一致)
}

// ButtonPermission 按钮可点击范围(0 指定用户,1 所有人)。
type ButtonPermission struct {
	Type int `json:"type,omitempty"`
}

// InputNotify 输入中状态(msg_type=6;input_second ≤300)。
type InputNotify struct {
	InputType   int `json:"input_type"`             // 1=正在输入(0 取消,文档示例为 1)
	InputSecond int `json:"input_second,omitempty"` // 持续秒数(默认 60)
}

// MediaInfo 富媒体(file_info 由上传接口返回;单聊与群上传不互通)。
type MediaInfo struct {
	FileInfo string `json:"file_info"`
}

// IsEmpty 是否空消息(调用方防空发)。
func (m SendMessage) IsEmpty() bool {
	return m.MsgType == 0 && m.Content == "" && m.Markdown == nil && m.Media == nil
}
