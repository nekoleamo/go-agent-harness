// Package ilink 提供腾讯 iLink Bot API(微信个人号 Bot 协议,IM 远程控制线 P0-2b)客户端。
// 协议基线:官方 iLink SDK v2.2.0 wire(对齐 omp-wechat/hermes 微信 adapter 同源协议):
//   - QR 登录:GET ilink/bot/get_bot_qrcode?bot_type=3 + GET ilink/bot/get_qrcode_status?qrcode=<t>
//   - 收:POST ilink/bot/getupdates(HTTP 长轮询 ~35s,带 get_updates_buf 断点续传)
//   - 发:POST ilink/bot/sendmessage(纯文本 item;context_token 回显;client_id 幂等去重)
//   - typing:POST ilink/bot/getconfig(取 typing_ticket)+ POST ilink/bot/sendtyping
//
// 鉴权头:AuthorizationType: ilink_bot_token + Authorization: Bearer <token> + X-WECHAT-UIN(base64 uint32)。
// 业务错误以 HTTP 200 + ret/errcode 非 0 返回(必须显式校验,防静默失败)。
// 本包只做协议与凭证存取;消息路由/回合驱动/审批在 im.Bridge,传输适配在 plugins/ui/ui-im-wechat。
package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChannelVersion 声明给 iLink 服务器的通道协议版本(跟随官方 SDK 而非本包版本;
// 过低版本(0.1.0)文字可收但媒体消息服务端拒绝)。
const ChannelVersion = "2.2.0"

// BotAgent UA 式客户端标识(base_info.bot_agent;微信端可能展示该操作者名,保持与产品名一致)。
const BotAgent = "GoAgentHarness/0.1.0"

// DefaultBaseURL iLink API 默认端点。
const DefaultBaseURL = "https://ilinkai.weixin.qq.com/"

// ErrNotLoggedIn 未登录(缺 token)。
var ErrNotLoggedIn = errors.New("ilink: 未登录,请先执行 /im login 扫码")

// Credentials iLink 登录凭证 + 通道状态(便携纪律:入 $GAH_HOME/config/,随目录迁移;0600)。
type Credentials struct {
	Token     string   `yaml:"token"`
	BaseURL   string   `yaml:"base_url"`
	AccountID string   `yaml:"account_id"`
	UserID    string   `yaml:"user_id"`            // 扫码者 ilink_user_id(登录成功后自动授权)
	SyncBuf   string   `yaml:"sync_buf,omitempty"` // getupdates 断点游标
	Allow     []string `yaml:"allow,omitempty"`     // 已授权 SenderKey(channel\0user)持久化(当前配对等变化在 P1 闭环)
	Groups    []string `yaml:"groups,omitempty"`    // 已授权群 chatKey(channel\0chatID;群维度授权)
}

// Client iLink HTTP 客户端(无共享可变状态,可并发)。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client // 零超时;每请求经 ctx 控制(长轮询需 ~35s)
}

// New 构造客户端。token 为空 = 未登录。
func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{}}
}

// baseInfo 每请求公共字段。
func baseInfo() map[string]string {
	return map[string]string{"channel_version": ChannelVersion, "bot_agent": BotAgent}
}

// post 公共 POST:拼 URL、鉴权头、JSON body;业务错误(ret/errcode != 0)显式返回。
// timeout 为本次请求超时(长轮询 getupdates 用 ~35s;普通请求 15s)。
func (c *Client) post(ctx context.Context, path string, body any, timeout time.Duration) ([]byte, error) {
	base := c.BaseURL
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-WECHAT-UIN", randomUIN())
	req.Header.Set("Content-Length", fmt.Sprint(len(payload)))
	if timeout > 0 {
		ctx2, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req = req.WithContext(ctx2)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ilink: %s http %d: %s", path, resp.StatusCode, truncate(string(raw), 200))
	}
	var biz struct {
		Ret     *int   `json:"ret"`
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &biz); err == nil {
		code := 0
		if biz.Ret != nil {
			code = *biz.Ret
		} else if biz.ErrCode != nil {
			code = *biz.ErrCode
		}
		if code != 0 {
			return nil, fmt.Errorf("ilink: %s error ret=%d errcode=%v errmsg=%q", path, code, biz.ErrCode, truncate(biz.ErrMsg, 200))
		}
	}
	return raw, nil
}

// get GET(QR 登录端点;无鉴权头)。
func get(ctx context.Context, baseURL, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ilink: %s http %d: %s", path, resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// SessionExpired 是否会话过期错误(errcode=-14;需重新扫码登录)。
func SessionExpired(err error) bool {
	s := err.Error()
	return strings.Contains(s, "=-14") || strings.Contains(s, "ret=-14") || strings.Contains(s, "errcode=-14")
}

// randomUIN 每次请求随机 X-WECHAT-UIN(对齐官方 SDK 行为)。
func randomUIN() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// GetUpdatesResponse getupdates 响应。
type GetUpdatesResponse struct {
	Ret           int              `json:"ret"`
	GetUpdatesBuf string           `json:"get_updates_buf"`
	Msgs          []InboundMessage `json:"msgs"`
}

// InboundMessage 一条入站消息(字段取文本闭环所需子集)。
type InboundMessage struct {
	MessageType  int    `json:"message_type"` // 1 = 用户消息;2 = bot 自己(BOT)
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	ContextToken string `json:"context_token"`
	CreateTimeMs int64  `json:"create_time_ms"`
	ItemList     []Item `json:"item_list"`
}

// Item 消息内容项(type:1 文本/2 图片/3 语音/4 文件/5 视频)。
type Item struct {
	Type      int        `json:"type"`
	TextItem  *TextItem  `json:"text_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
	VoiceItem *VoiceItem `json:"voice_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
}

// TextItem 文本项。
type TextItem struct {
	Text string `json:"text"`
}

// ImageItem 图片项(CDN 下载 P0-2c;本阶段仅占位)。
type ImageItem struct {
	AesKey string `json:"aeskey"`
	Media  *Media `json:"media"`
	URL    string `json:"url"`
}

// VoiceItem 语音项(优先用服务端 ASR 转写文本)。
type VoiceItem struct {
	Text     string `json:"text"`
	PlayTime int    `json:"playtime"`
}

// FileItem 文件项。
type FileItem struct {
	FileName string `json:"file_name"`
	Len      string `json:"len"`
	Media    *Media `json:"media"`
}

// Media CDN 媒体信息(下载解密留 P0-2c)。
type Media struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AesKey            string `json:"aes_key"`
	EncryptType       int    `json:"encrypt_type"`
	FullURL           string `json:"full_url"`
}

// ExtractText 提取消息文本(媒体项转占位文本,免下载)。
func (m *InboundMessage) ExtractText() string {
	var parts []string
	imgs := 0
	for _, it := range m.ItemList {
		switch it.Type {
		case 1:
			if it.TextItem != nil && it.TextItem.Text != "" {
				parts = append(parts, it.TextItem.Text)
			}
		case 2:
			imgs++
		case 3:
			if it.VoiceItem != nil && it.VoiceItem.Text != "" {
				parts = append(parts, it.VoiceItem.Text) // 服务端 ASR 转写优先
			} else {
				parts = append(parts, "(语音)")
			}
		case 4:
			if it.FileItem != nil {
				name := it.FileItem.FileName
				if name == "" {
					name = "文件"
				}
				parts = append(parts, "(文件: "+name+")")
			}
		case 5:
			parts = append(parts, "(视频)")
		}
	}
	if imgs > 0 {
		parts = append(parts, fmt.Sprintf("(+%d 图片)", imgs))
	}
	return strings.Join(parts, "\n")
}

// GetUpdates 长轮询取消息;超时(长轮询正常空转)返回空列表不报错。
func (c *Client) GetUpdates(ctx context.Context, syncBuf string) (*GetUpdatesResponse, error) {
	raw, err := c.post(ctx, "ilink/bot/getupdates", map[string]any{
		"get_updates_buf": syncBuf,
		"base_info":       baseInfo(),
	}, 35*time.Second)
	if err != nil {
		// 长轮询服务端保持连接至超时:context 超时 = 正常空转
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &GetUpdatesResponse{Ret: 0, GetUpdatesBuf: syncBuf}, nil
		}
		return nil, err
	}
	var out GetUpdatesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ilink: getupdates 解码失败: %w", err)
	}
	return &out, nil
}

// SendMessage 发送纯文本消息(context_token 回显;clientID 跨重试复用供服务端去重)。
func (c *Client) SendMessage(ctx context.Context, to, text, contextToken, clientID string) error {
	if c.Token == "" {
		return ErrNotLoggedIn
	}
	if clientID == "" {
		clientID = "gah-ilink-" + fmt.Sprint(time.Now().UnixNano())
	}
	_, err := c.post(ctx, "ilink/bot/sendmessage", map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    to,
			"client_id":     clientID,
			"message_type":  2, // BOT
			"message_state": 2, // FINISH
			"item_list":     []map[string]any{{"type": 1, "text_item": map[string]any{"text": text}}},
			"context_token": contextToken,
		},
		"base_info": baseInfo(),
	}, 15*time.Second)
	return err
}

// TypingTicket getconfig 返回的 typing 票据。
type TypingTicket struct {
	ILinkUserID string `json:"ilink_user_id"`
	Ticket      string `json:"typing_ticket"`
}

// GetTypingTicket 取 typing 票据(缓存由调用方决定)。
func (c *Client) GetTypingTicket(ctx context.Context, userID string) (string, error) {
	raw, err := c.post(ctx, "ilink/bot/getconfig", map[string]any{
		"ilink_user_id": userID,
		"base_info":     baseInfo(),
	}, 15*time.Second)
	if err != nil {
		return "", err
	}
	var out TypingTicket
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Ticket, nil
}

// SendTyping 发送输入状态:show=true 显示"正在输入",false 取消。
func (c *Client) SendTyping(ctx context.Context, userID, ticket string, show bool) error {
	status := 2
	if show {
		status = 1
	}
	_, err := c.post(ctx, "ilink/bot/sendtyping", map[string]any{
		"ilink_user_id": userID,
		"typing_ticket": ticket,
		"status":        status,
		"base_info":     baseInfo(),
	}, 10*time.Second)
	return err
}
