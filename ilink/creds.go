// 凭证存取 + QR 登录流程(iLink Bot API;微信扫码登录个人号 Bot)。
// 便携纪律:凭证入 $GAH_HOME/config/ilink-wechat.yaml(0600,随目录迁移);QR 登录失败/过期显式报错。
package ilink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Store 凭证/状态持久化(yaml,0600)。
type Store struct {
	Path string
}

// NewStore 构造凭证存储(路径由插件壳按 $GAH_HOME/config 派生)。
func NewStore(path string) *Store { return &Store{Path: path} }

// Load 读取凭证;文件缺失/空 = 未登录(不报错)。
func (s *Store) Load() (*Credentials, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Credentials{}, nil
		}
		return nil, fmt.Errorf("ilink: 读凭证失败: %w", err)
	}
	var c Credentials
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("ilink: 凭证解析失败: %w", err)
	}
	return &c, nil
}

// Save 落盘(0600)。
func (s *Store) Save(c *Credentials) error {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// QRResponse 二维码获取响应。
type QRResponse struct {
	QRCode    string `json:"qrcode"`             // 轮询 token
	QRCodeImg string `json:"qrcode_img_content"` // 二维码内容(URL;终端无法渲染二维码时打开此链接)
	QrStatus  string `json:"qr_status,omitempty"`
}

// QRStatus 轮询状态响应。
type QRStatus struct {
	Status      string `json:"status"` // wait | scaned | expired | confirmed
	BotToken    string `json:"bot_token"`
	BaseURL     string `json:"baseurl"`
	ILinkBotID  string `json:"ilink_bot_id"`
	ILinkUserID string `json:"ilink_user_id"`
	ErrMsg      string `json:"errmsg,omitempty"`
}

// FetchQR 获取登录二维码(返回轮询 token + 可打开的二维码内容)。
func FetchQR(ctx context.Context, baseURL string) (*QRResponse, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	raw, err := get(ctx, baseURL, "/ilink/bot/get_bot_qrcode?bot_type=3")
	if err != nil {
		return nil, err
	}
	var out QRResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ilink: 二维码响应解码失败: %w", err)
	}
	if out.QRCode == "" || out.QRCodeImg == "" {
		return nil, fmt.Errorf("ilink: 二维码响应缺字段: %s", truncate(string(raw), 200))
	}
	return &out, nil
}

// PollQR 轮询扫码状态(Wait→Scan 超时在调用方)。
func PollQR(ctx context.Context, baseURL, qrcode string) (*QRStatus, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	raw, err := get(ctx, baseURL, "/ilink/bot/get_qrcode_status?qrcode="+url.QueryEscape(qrcode))
	if err != nil {
		return nil, err
	}
	var out QRStatus
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ilink: 二维码状态解码失败: %w", err)
	}
	return &out, nil
}

// LoginQR 完整扫码登录:取码 → LoginQRFromToken 轮询至 confirmed。
func LoginQR(ctx context.Context, baseURL string, timeout time.Duration) (*Credentials, error) {
	qr, err := FetchQR(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	return LoginQRFromToken(ctx, baseURL, qr.QRCode, timeout)
}

// LoginQRFromToken 轮询已有二维码直至 confirmed/expired/超时(默认 5min)。
// 调用方负责 Save + 授权扫码者。
func LoginQRFromToken(ctx context.Context, baseURL, qrcode string, timeout time.Duration) (*Credentials, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("ilink: 登录超时(%v),请重试", timeout)
		}
		pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		st, err := PollQR(pollCtx, baseURL, qrcode)
		cancel()
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		switch st.Status {
		case "wait", "scaned":
			time.Sleep(3 * time.Second)
		case "expired":
			return nil, errors.New("ilink: 二维码已过期,请重试 /im login")
		case "confirmed":
			if st.BotToken == "" {
				return nil, errors.New("ilink: 登录确认但缺 bot_token")
			}
			base := st.BaseURL
			if base == "" {
				base = baseURL
			}
			return &Credentials{Token: st.BotToken, BaseURL: base, AccountID: st.ILinkBotID, UserID: st.ILinkUserID}, nil
		default:
			return nil, fmt.Errorf("ilink: 未知二维码状态 %q", st.Status)
		}
	}
}

// QRProgress 扫码进度回调(相位 + 人类可读说明)。
// 相位:waiting_scan(待扫)/ scanned(已扫待确认)/ expired_refresh(过期重取中)。
type QRProgress func(phase, detail string)

// LoginQRWithProgress 完整扫码登录(面板/TUI 用):
//   - 相位经 onPhase 回调(waiting_scan → scanned → expired_refresh → …);
//   - **过期自动重取二维码**(协议不下发有效期,只能轮询发现 expired)→ onQR 回调新码;
//   - onQR/onPhase 可为 nil(纯静默调用)。
//
// 轮询窗口 35s(长轮询;本地超时视为继续等待,与 LoginQRFromToken 同纪律)。
func LoginQRWithProgress(ctx context.Context, baseURL string, deadline time.Duration, onQR func(*QRResponse), onPhase QRProgress) (*Credentials, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if deadline <= 0 {
		deadline = 10 * time.Minute
	}
	end := time.Now().Add(deadline)
	qr, err := FetchQR(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	if onQR != nil {
		onQR(qr)
	}
	refreshes := 0
	const maxRefresh = 5 // 防极端情况无限取码
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(end) {
			return nil, fmt.Errorf("ilink: 登录超时(%v),请重试", deadline)
		}
		pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		st, err := PollQR(pollCtx, baseURL, qr.QRCode)
		cancel()
		if err != nil {
			// 长轮询本地超时/网络抖动 = 继续等待(不判定失败)
			time.Sleep(2 * time.Second)
			continue
		}
		switch st.Status {
		case "wait":
			if onPhase != nil {
				onPhase("waiting_scan", "等待微信扫码")
			}
			time.Sleep(2 * time.Second)
		case "scaned":
			if onPhase != nil {
				onPhase("scanned", "已扫码,请在手机上确认")
			}
			time.Sleep(2 * time.Second)
		case "expired":
			refreshes++
			if refreshes > maxRefresh {
				return nil, errors.New("ilink: 二维码多次过期,请稍后重试")
			}
			if onPhase != nil {
				onPhase("expired_refresh", fmt.Sprintf("二维码过期,正在自动重取(第 %d 次)", refreshes))
			}
			nq, ferr := FetchQR(ctx, baseURL)
			if ferr != nil {
				time.Sleep(3 * time.Second)
				continue
			}
			qr = nq
			if onQR != nil {
				onQR(qr)
			}
		case "confirmed":
			if st.BotToken == "" {
				return nil, errors.New("ilink: 登录确认但缺 bot_token")
			}
			if onPhase != nil {
				onPhase("validating", "已确认,正在保存凭证")
			}
			base := st.BaseURL
			if base == "" {
				base = baseURL
			}
			return &Credentials{Token: st.BotToken, BaseURL: base, AccountID: st.ILinkBotID, UserID: st.ILinkUserID}, nil
		default:
			return nil, fmt.Errorf("ilink: 未知二维码状态 %q", st.Status)
		}
	}
}
