// qqbot REST 单测:mock OpenAPI server 验证 access_token 换取/缓存/提前刷新、
// 发消息路径与 body(单聊/群)、鉴权头、业务错误分类、凭证存取。
package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockOpenAPI 极简 OpenAPI 服务器:token 换取(可按次数编程)+ 单聊/群发消息记录。
type mockOpenAPI struct {
	mu          sync.Mutex
	tokenCalls  int
	accessToken string
	expiresIn   int
	sendBodies  []map[string]any
	sendAuth    []string
	sendPaths   []string
	nextErr     *APIError // 非空则下一次发送返回该业务错误
	sends401    int       // 前 N 次发送直接回 401(access_token 失效兕底重试测试)
}

func (m *mockOpenAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/getAppAccessToken", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.tokenCalls++
		appID, _ := body["appId"].(string)
		secret, _ := body["clientSecret"].(string)
		m.mu.Unlock()
		if appID != "app-1" || secret != "sec-1" {
			json.NewEncoder(w).Encode(map[string]any{"code": 100016, "message": "appId 与 clientSecret 不一致"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": m.accessToken, "expires_in": m.expiresIn})
	})
	handle := func(w http.ResponseWriter, r *http.Request, path string) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.sendBodies = append(m.sendBodies, body)
		m.sendAuth = append(m.sendAuth, r.Header.Get("Authorization"))
		m.sendPaths = append(m.sendPaths, path)
		if m.sends401 > 0 {
			m.sends401--
			m.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		err := m.nextErr
		m.nextErr = nil
		m.mu.Unlock()
		if err != nil {
			w.WriteHeader(http.StatusOK) // 官方业务错误以 200 + body {code,message} 返回
			json.NewEncoder(w).Encode(err)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"id": "send-1", "timestamp": "2026-10-01T00:00:00+08:00"})
	}
	mux.HandleFunc("/v2/users/openid-1/messages", func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, "c2c")
	})
	mux.HandleFunc("/v2/groups/grp-1/messages", func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, "group")
	})
	return mux
}

// newMockAPI 构造 mock server + 已配置好的 token 源/客户端。
func newMockAPI(t *testing.T, token string) (*mockOpenAPI, *httptest.Server, *TokenSource) {
	t.Helper()
	m := &mockOpenAPI{accessToken: token, expiresIn: 7200}
	hs := httptest.NewServer(m.handler())
	t.Cleanup(hs.Close)
	ts := NewTokenSource("app-1", "sec-1").WithHTTP(hs.Client())
	ts.URL = hs.URL + "/app/getAppAccessToken"
	return m, hs, ts
}

// TestAccessTokenFetchAndCache 换取一次后缓存复用;接近过期(提前窗口内)刷新。
func TestAccessTokenFetchAndCache(t *testing.T) {
	m, hs, ts := newMockAPI(t, "tok-1")
	ctx := context.Background()
	got, err := ts.Token(ctx)
	if err != nil || got != "tok-1" {
		t.Fatalf("首次换取 = %q, %v", got, err)
	}
	m.mu.Lock()
	calls := m.tokenCalls
	m.mu.Unlock()
	if calls != 1 {
		t.Fatalf("应换取 1 次,got %d", calls)
	}
	got, err = ts.Token(ctx) // 缓存命中
	if err != nil || got != "tok-1" {
		t.Fatalf("缓存复用 = %q, %v", got, err)
	}
	m.mu.Lock()
	calls = m.tokenCalls
	m.mu.Unlock()
	if calls != 1 {
		t.Fatalf("缓存后不应再换取,got %d", calls)
	}
	// 提前窗口(60s):expiress_in=2s + refreshBefore=1s → 过期后再次取应重新换取
	m.mu.Lock()
	m.expiresIn = 2
	m.accessToken = "tok-2"
	m.mu.Unlock()
	ts2 := NewTokenSource("app-1", "sec-1").WithHTTP(hs.Client())
	ts2.URL = hs.URL + "/app/getAppAccessToken"
	ts2.SetRefreshBefore(1 * time.Second)
	if tok, err := ts2.Token(ctx); err != nil || tok != "tok-2" {
		t.Fatalf("短生命周期首次换取 = %q, %v", tok, err)
	}
	time.Sleep(1500 * time.Millisecond) // 超过(expires_in - refreshBefore)窗口 → 过期
	if tok, err := ts2.Token(ctx); err != nil || tok != "tok-2" {
		t.Fatalf("过期后重新换取 = %q, %v", tok, err)
	}
	m.mu.Lock()
	calls = m.tokenCalls
	m.mu.Unlock()
	if calls != 3 { // 场景 1 换 1 次 + 短生命周期换 2 次
		t.Fatalf("应累计换取 3 次,got %d", calls)
	}
	// 未配置:应报 ErrNotConfigured
	unconf := NewTokenSource("", "")
	if _, err := unconf.Token(ctx); err != ErrNotConfigured {
		t.Fatalf("未配置应报 ErrNotConfigured,got %v", err)
	}
}

// TestSendMessagesAndAuth 单聊/群发消息:路径、鉴权头 QQBot、body(msg_type/content/msg_id/msg_seq)。
func TestSendMessagesAndAuth(t *testing.T) {
	m, hs, ts := newMockAPI(t, "tok-x")
	cli := NewClient(ts).WithBaseURL(hs.URL)
	ctx := context.Background()

	// 单聊被动回复(文本)
	if err := cli.SendText(ctx, "openid-1", "你好,我是 gah 机器人", "msg-c2c-1", 7); err != nil {
		t.Fatal(err)
	}
	// 群被动回复(markdown)
	if err := cli.SendGroupMessage(ctx, "grp-1", SendMessage{
		MsgType: MsgTypeMarkdown, Markdown: &Markdown{Content: "**结果**\n- a\n- b"},
		MsgID: "msg-grp-1", MsgSeq: 3}); err != nil {
		t.Fatal(err)
	}
	// 输入状态(正在输入 type=1)
	if err := cli.SendInputState(ctx, "openid-1", 1, 30, "msg-c2c-1", 8); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sendBodies) != 3 || len(m.sendPaths) != 3 {
		t.Fatalf("应 3 次发送,got %d bodies %d paths", len(m.sendBodies), len(m.sendPaths))
	}
	// 鉴权头统一为 QQBot <access_token>
	for _, a := range m.sendAuth {
		if a != "QQBot tok-x" {
			t.Fatalf("鉴权头不符: %q", a)
		}
	}
	// 单聊文本
	if m.sendPaths[0] != "c2c" {
		t.Fatalf("路径[0] = %q", m.sendPaths[0])
	}
	b0 := m.sendBodies[0]
	if b0["msg_type"] != float64(0) || b0["content"] != "你好,我是 gah 机器人" ||
		b0["msg_id"] != "msg-c2c-1" || b0["msg_seq"] != float64(7) {
		t.Fatalf("单聊 body 不符: %+v", b0)
	}
	// 群 markdown
	if m.sendPaths[1] != "group" {
		t.Fatalf("路径[1] = %q", m.sendPaths[1])
	}
	b1 := m.sendBodies[1]
	if b1["msg_type"] != float64(2) || b1["msg_id"] != "msg-grp-1" || b1["msg_seq"] != float64(3) {
		t.Fatalf("群发 body 不符: %+v", b1)
	}
	md, ok := b1["markdown"].(map[string]any)
	if !ok || md["content"] == "" {
		t.Fatalf("markdown 载荷缺失: %+v", b1)
	}
	// 输入状态
	b2 := m.sendBodies[2]
	if b2["msg_type"] != float64(6) {
		t.Fatalf("input_notify body 不符: %+v", b2)
	}
	ni, ok := b2["input_notify"].(map[string]any)
	if !ok || ni["input_type"] != float64(1) || ni["input_second"] != float64(30) {
		t.Fatalf("input_notify 载荷不符: %+v", b2)
	}
}

// TestAPIErrorClassification 业务错误分类:被动过期/频控/去重;HTTP 429 归类频控。
func TestAPIErrorClassification(t *testing.T) {
	m, hs, ts := newMockAPI(t, "tok-y")
	cli := NewClient(ts).WithBaseURL(hs.URL)
	ctx := context.Background()

	checks := []struct {
		code  int
		msg   string
		check func(error) bool
	}{
		{CodePassiveExpired, "被动回复时间或次数超限", IsPassiveExpired},
		{CodeMsgIDExpired, "msg_id 已过期", IsPassiveExpired},
		{CodeRateLimited, "主动消息发送超过频控限制", IsRateLimited},
		{CodeDeduped, "消息被去重", IsDeduped},
	}
	for _, c := range checks {
		m.mu.Lock()
		m.nextErr = &APIError{Code: c.code, Message: c.msg}
		m.mu.Unlock()
		err := cli.SendText(ctx, "openid-1", "test", "m", 1)
		if err == nil {
			t.Fatalf("code=%d 应报错", c.code)
		}
		if !c.check(err) {
			t.Fatalf("code=%d 分类失败: %v", c.code, err)
		}
	}
	// HTTP 429 → 频控
	hs429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer hs429.Close()
	cli2 := NewClient(ts).WithBaseURL(hs429.URL) // 复用已换取 token 的源,绕过 token 层
	err := cli2.SendText(ctx, "openid-1", "x", "m", 1)
	if !IsRateLimited(err) {
		t.Fatalf("HTTP 429 应归类频控,got %v", err)
	}
}

// TestAccessTokenRetryOn401 发送遇 401(access_token 失效,理论不应出现)→ 清缓存重取新 token 重试一次成功。
func TestAccessTokenRetryOn401(t *testing.T) {
	m, hs, ts := newMockAPI(t, "tok-1")
	// 预热:先缓存 tok-1
	if tok, err := ts.Token(context.Background()); err != nil || tok != "tok-1" {
		t.Fatalf("预热失败: %q %v", tok, err)
	}
	m.mu.Lock()
	m.sends401 = 1
	m.accessToken = "tok-2" // 失效后重取时签发新 token
	m.mu.Unlock()
	cli := NewClient(ts).WithBaseURL(hs.URL)
	if err := cli.SendText(context.Background(), "openid-1", "hi", "m", 1); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sendAuth) != 2 || m.sendAuth[0] != "QQBot tok-1" || m.sendAuth[1] != "QQBot tok-2" {
		t.Fatalf("401 重试鉴权不符: %+v", m.sendAuth)
	}
	if m.tokenCalls != 2 {
		t.Fatalf("应重取 token 一次,tokenCalls=%d", m.tokenCalls)
	}
}

// TestCredentialsStore 凭证 yaml 存取与 0600(同 ilink.Store 型)。
func TestCredentialsStore(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "config", "qqbot.yaml"))
	c, err := s.Load()
	if err != nil || c.Configured() {
		t.Fatalf("缺失文件应返回未配置: %+v %v", c, err)
	}
	in := &Credentials{AppID: "app-1", AppSecret: "sec-1", BaseURL: "https://sandbox.api.sgroup.qq.com",
		Allow: []string{"qq\x00OPENID1"}}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Configured() || got.AppID != "app-1" || got.AppSecret != "sec-1" ||
		got.BaseURL != "https://sandbox.api.sgroup.qq.com" || len(got.Allow) != 1 {
		t.Fatalf("往返不符: %+v", got)
	}
	fi, _ := os.Stat(s.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("凭证文件应 0600,got %v", fi.Mode().Perm())
	}
}

// TestTokenExpiresInFlexible 官方 expires_in 实为字符串("7200",文档示例如此),
// 数字与字符串都必须可解(回归:曾按 int 解析 → 解码失败 → 网关永远连不上)。
func TestTokenExpiresInFlexible(t *testing.T) {
	for _, body := range []string{
		`{"access_token":"tk-1","expires_in":"7200"}`, // 官方实测形态(字符串)
		`{"access_token":"tk-2","expires_in":7200}`,   // 数字形态(文档"类型"栏)
	} {
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		ts := NewTokenSource("app", "sec")
		ts.URL = hs.URL
		tk, err := ts.Token(context.Background())
		hs.Close()
		if err != nil {
			t.Fatalf("响应 %s 应可解析: %v", body, err)
		}
		if tk == "" {
			t.Fatalf("响应 %s 应取到 token", body)
		}
	}
}

// TestTokenBizErrorStringCode code 为字符串时也应正确识别为业务错误。
func TestTokenBizErrorStringCode(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"100007","message":"invalid appid or secret"}`))
	}))
	defer hs.Close()
	ts := NewTokenSource("app", "sec")
	ts.URL = hs.URL
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("业务错误应返回错误")
	} else if !strings.Contains(err.Error(), "100007") {
		t.Fatalf("应带业务错误码: %v", err)
	}
}

// TestFlexInt 直接单测宽容解析。
func TestFlexInt(t *testing.T) {
	var v struct {
		A flexInt `json:"a"`
		B flexInt `json:"b"`
		C flexInt `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":"7200","b":42,"c":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 7200 || v.B != 42 || v.C != 0 {
		t.Fatalf("解析不符: %+v", v)
	}
}

// TestDownloadMediaWithAuth 附件下载:带 QQBot 鉴权头、返回字节与 Content-Type、404/超限报错。
func TestDownloadMediaWithAuth(t *testing.T) {
	var auth string
	tokHS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"tk-1","expires_in":"7200"}`))
	}))
	defer tokHS.Close()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/ok.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("\xff\xd8\xff-fake-jpeg"))
		case "/big":
			w.Write(bytes.Repeat([]byte("b"), 21<<20))
		default:
			http.NotFound(w, r)
		}
	}))
	defer hs.Close()
	ts := NewTokenSource("app-x", "sec-y")
	ts.URL = tokHS.URL
	c := NewClient(ts)
	data, ct, err := c.DownloadMedia(context.Background(), hs.URL+"/ok.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if ct != "image/jpeg" || len(data) != 13 {
		t.Fatalf("下载不符: ct=%q len=%d", ct, len(data))
	}
	if auth != "QQBot tk-1" {
		t.Fatalf("附件下载应带鉴权头(单前缀),得 %q", auth)
	}
	if _, _, err := c.DownloadMedia(context.Background(), hs.URL+"/missing"); err == nil {
		t.Fatal("404 应报错")
	}
	if _, _, err := c.DownloadMedia(context.Background(), hs.URL+"/big"); err == nil {
		t.Fatal("超限应报错")
	}
	if _, _, err := c.DownloadMedia(context.Background(), ""); err == nil {
		t.Fatal("空 URL 应报错")
	}
}
