// REST 接口:access_token 换取(内存缓存+提前 60s 刷新)与消息发送(单聊/群被动回复+输入状态)。
// 鉴权头:Authorization: QQBot <access_token>;业务错误以 HTTP body {code, message} 显式返回。
package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenSource AppID/AppSecret → access_token 换取与缓存。
// access_token 有效期 7200s;到期前 60s 内重复获取会签发新 token(旧 token 60s 内仍有效)。
// 线程安全:刷新期间持锁,多调用者串行等待,不并发重复换取。
type TokenSource struct {
	AppID     string
	AppSecret string
	URL       string // 默认 DefaultTokenURL
	HTTP      *http.Client

	mu            sync.Mutex
	token         string
	expiresAt     time.Time
	refreshBefore time.Duration // 提前刷新窗口(默认 60s;测试可缩小)
}

// NewTokenSource 构造 token 源。
func NewTokenSource(appID, appSecret string) *TokenSource {
	return &TokenSource{AppID: appID, AppSecret: appSecret, URL: DefaultTokenURL,
		HTTP: &http.Client{}, refreshBefore: 60 * time.Second}
}

// WithHTTP 注入自定义 HTTP 客户端(测试 mock)。
func (s *TokenSource) WithHTTP(hc *http.Client) *TokenSource { s.HTTP = hc; return s }

// SetRefreshBefore 设置提前刷新窗口(测试用)。
func (s *TokenSource) SetRefreshBefore(d time.Duration) { s.refreshBefore = d }

// Token 取当前有效 access_token(缓存未到期则直返;否则换取新 token)。
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.token != "" && now.Add(s.refreshBefore).Before(s.expiresAt) {
		return s.token, nil
	}
	if s.AppID == "" || s.AppSecret == "" {
		return "", ErrNotConfigured
	}
	return s.fetchLocked(ctx)
}

// Invalidate 清缓存(下次 Token 强制重取;用于 401 失效兜底重试)。
func (s *TokenSource) Invalidate() {
	s.mu.Lock()
	s.token = ""
	s.mu.Unlock()
}

// fetchLocked 换取 access_token(调用方须持锁)。
func (s *TokenSource) fetchLocked(ctx context.Context) (string, error) {
	payload, err := json.Marshal(map[string]string{"appId": s.AppID, "clientSecret": s.AppSecret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qqbot: 换取 access_token http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Code        int    `json:"code"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("qqbot: access_token 响应解码失败: %w", err)
	}
	if out.Code != 0 {
		return "", &APIError{Code: out.Code, Message: out.Message}
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("qqbot: access_token 响应缺字段: %s", truncate(string(raw), 200))
	}
	s.token = out.AccessToken
	exp := time.Duration(out.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = 7200 * time.Second
	}
	s.expiresAt = time.Now().Add(exp)
	return s.token, nil
}

// Client OpenAPI 消息客户端(单聊/群;自动带鉴权头)。
type Client struct {
	BaseURL string
	TS      *TokenSource
	HTTP    *http.Client
}

// NewClient 构造 OpenAPI 客户端(默认根 https://api.bot.qq.com)。
func NewClient(ts *TokenSource) *Client {
	return &Client{BaseURL: DefaultBaseURL, TS: ts, HTTP: ts.HTTP}
}

// WithBaseURL 覆盖 OpenAPI 根(沙箱联调/测试 mock)。
func (c *Client) WithBaseURL(u string) *Client { c.BaseURL = strings.TrimSuffix(u, "/"); return c }

// do 公共 POST:补鉴权头 → 发送 → 解析业务错误({code,message} 非 0 显式返回;HTTP 429 归类频控)。
// 401(access_token 失效,理论不应出现——60s 前已刷新)兜底:清缓存重取一次后重试。
func (c *Client) do(ctx context.Context, path string, body any) error {
	return c.doRetry(ctx, path, body, true)
}

func (c *Client) doRetry(ctx context.Context, path string, body any, allowRetry bool) error {
	token, err := c.TS.Token(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && allowRetry {
		c.TS.Invalidate() // token 失效:清缓存重取一次重试(不再递归)
		return c.doRetry(ctx, path, body, false)
	}
	// 业务错误以 body {code,message} 返回;HTTP 429 视为频控(无 body 也归类)。
	var biz struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &biz); err == nil && biz.Code != 0 {
		return &APIError{Code: biz.Code, Message: biz.Message}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &APIError{Code: CodeRateLimited, Message: "http 429"}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qqbot: %s http %d: %s", path, resp.StatusCode, truncate(string(raw), 200))
	}
	return nil
}

// SendC2CMessage 发送单聊消息(POST /v2/users/{openid}/messages)。
// 被动回复必须带 msg.MsgID(事件 d.id;5 分钟内有效)与递增 MsgSeq(同 msg_id 重复去重)。
func (c *Client) SendC2CMessage(ctx context.Context, openid string, msg SendMessage) error {
	if msg.IsEmpty() {
		return errors.New("qqbot: 空消息拒绝发送")
	}
	return c.do(ctx, "/v2/users/"+openid+"/messages", msg)
}

// SendGroupMessage 发送群消息(POST /v2/groups/{group_openid}/messages;群被动回复同理带 msg_id)。
func (c *Client) SendGroupMessage(ctx context.Context, groupOpenID string, msg SendMessage) error {
	if msg.IsEmpty() {
		return errors.New("qqbot: 空消息拒绝发送")
	}
	return c.do(ctx, "/v2/groups/"+groupOpenID+"/messages", msg)
}

// SendText 便捷:纯文本被动回复(自动填 msg_type=0)。
func (c *Client) SendText(ctx context.Context, openid, text, msgID string, seq uint64) error {
	return c.SendC2CMessage(ctx, openid, SendMessage{MsgType: MsgTypeText, Content: text, MsgID: msgID, MsgSeq: seq})
}

// SendInputState 发送输入状态消息(msg_type=6;inputType 1=正在输入,0=取消;input_second ≤300,
// 默认 60)。ShowTyping/StopTyping 各调一次(transport 周期刷新由调用方负责)。
func (c *Client) SendInputState(ctx context.Context, openid string, inputType, inputSecond int, msgID string, seq uint64) error {
	if inputSecond <= 0 || inputSecond > 300 {
		inputSecond = 60
	}
	return c.do(ctx, "/v2/users/"+openid+"/messages", SendMessage{
		MsgType: MsgTypeInput, InputNotify: &InputNotify{InputType: inputType, InputSecond: inputSecond},
		MsgID: msgID, MsgSeq: seq})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// contains 简易子串判断(IsRateLimited 分类用)。
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
