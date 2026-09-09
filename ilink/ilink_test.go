// ilink 协议单测:mock iLink server 验证端点/鉴权头/业务错误/长轮询空转/QR 登录/凭证存取。
package ilink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockILink 极简 iLink 服务器:记录 sendmessage 请求;可编程 getupdates/qrcode 响应。
type mockILink struct {
	mu         sync.Mutex
	updates    []map[string]any // 依次返回的 getupdates 响应
	sendBodies []map[string]any // sendmessage body 记录
	qrStatuses []map[string]any // 依次返回的 qrcode_status 响应
	authHeader string
}

func (m *mockILink) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.authHeader = r.Header.Get("Authorization")
		if m.authHeader == "" {
			w.WriteHeader(401)
			return
		}
		if len(m.updates) == 0 {
			w.Write([]byte(`{"ret":0,"msgs":[],"get_updates_buf":"b0"}`))
			return
		}
		resp := m.updates[0]
		m.updates = m.updates[1:]
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/ilink/bot/sendmessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.sendBodies = append(m.sendBodies, body)
		m.mu.Unlock()
		w.Write([]byte(`{"ret":0}`))
	})
	mux.HandleFunc("/ilink/bot/getconfig", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ret":0,"ilink_user_id":"u","typing_ticket":"tkt-1"}`))
	})
	mux.HandleFunc("/ilink/bot/sendtyping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ret":0}`))
	})
	mux.HandleFunc("/ilink/bot/get_bot_qrcode", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"qrcode":"qr-1","qrcode_img_content":"https://wx.qq.com/q/scan-1"}`))
	})
	mux.HandleFunc("/ilink/bot/get_qrcode_status", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.qrStatuses) == 0 {
			w.Write([]byte(`{"status":"wait"}`))
			return
		}
		resp := m.qrStatuses[0]
		m.qrStatuses = m.qrStatuses[1:]
		json.NewEncoder(w).Encode(resp)
	})
	return mux
}

func newMock(t *testing.T) (*mockILink, *httptest.Server) {
	t.Helper()
	m := &mockILink{}
	hs := httptest.NewServer(m.handler())
	t.Cleanup(hs.Close)
	return m, hs
}

func msg(text string) map[string]any {
	return map[string]any{"message_type": 1, "from_user_id": "user1", "to_user_id": "bot",
		"context_token": "ct-1", "create_time_ms": int64(1000),
		"item_list": []map[string]any{{"type": 1, "text_item": map[string]any{"text": text}}}}
}

// TestGetUpdatesAndSendMessage 长轮询收消息(ExtractText)+ sendmessage body 与鉴权头。
func TestGetUpdatesAndSendMessage(t *testing.T) {
	m, hs := newMock(t)
	m.updates = []map[string]any{{"ret": 0, "get_updates_buf": "b1",
		"msgs": []map[string]any{msg("你好"), {
			"message_type": 1, "from_user_id": "user2", "to_user_id": "bot",
			"context_token": "ct-2", "create_time_ms": int64(2000),
			"item_list": []map[string]any{{"type": 2, "image_item": map[string]any{}}}}}}}
	cli := New(hs.URL, "tok-secret")
	ctx := context.Background()

	resp, err := cli.GetUpdates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetUpdatesBuf != "b1" || len(resp.Msgs) != 2 {
		t.Fatalf("getupdates 解析不符: %+v", resp)
	}
	if got := resp.Msgs[0].ExtractText(); got != "你好" {
		t.Fatalf("文本提取 = %q", got)
	}
	if got := resp.Msgs[1].ExtractText(); got == "" || !containsAny(got, "图片") {
		t.Fatalf("图片占位缺失: %q", got)
	}
	// 发送
	if err := cli.SendMessage(ctx, "user1", "回复你", "ct-1", "cli-1"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	bodies := m.sendBodies
	m.mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("应收到 1 次 sendmessage,got %d", len(bodies))
	}
	req := bodies[0]
	msgv := req["msg"].(map[string]any)
	if msgv["to_user_id"] != "user1" || msgv["context_token"] != "ct-1" || msgv["client_id"] != "cli-1" {
		t.Fatalf("sendmessage msg 不符: %+v", msgv)
	}
	if msgv["message_type"] != float64(2) || msgv["message_state"] != float64(2) {
		t.Fatalf("sendmessage 类型不符: %+v", msgv)
	}
	if got := m.authHeader; got != "Bearer tok-secret" {
		t.Fatalf("鉴权头不符: %q", got)
	}
}

// TestBusinessError 业务错误(ret 非 0)显式返回(含会话过期识别)。
func TestBusinessError(t *testing.T) {
	m, hs := newMock(t)
	m.updates = []map[string]any{{"ret": -14, "errmsg": "session expired"}}
	cli := New(hs.URL, "tok")
	_, err := cli.GetUpdates(context.Background(), "")
	if err == nil {
		t.Fatal("ret=-14 应报错")
	}
	if !SessionExpired(err) {
		t.Fatalf("应识别会话过期: %v", err)
	}
	// HTTP 层错误
	hs2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer hs2.Close()
	if err := New(hs2.URL, "t").SendMessage(context.Background(), "u", "x", "c", ""); err == nil {
		t.Fatal("500 应报错")
	}
}

// TestLongPollTimeoutEmpty 长轮询超时 = 正常空转(空列表不报错)。
func TestLongPollTimeoutEmpty(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // 挂起直至客户端取消;兜底 3s 退出(防 Close 卡连接)
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer hs.Close()
	cli := New(hs.URL, "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	resp, err := cli.GetUpdates(ctx, "b0")
	if err != nil {
		t.Fatalf("长轮询超时应视为空转: %v", err)
	}
	if len(resp.Msgs) != 0 {
		t.Fatalf("应无消息: %+v", resp)
	}
}

// TestQRLogin 扫码登录轮询:wait → confirmed 得到凭证。
func TestQRLogin(t *testing.T) {
	m, hs := newMock(t)
	m.qrStatuses = []map[string]any{
		{"status": "wait"},
		{"status": "scaned"},
		{"status": "confirmed", "bot_token": "bt-1", "baseurl": hs.URL, "ilink_bot_id": "bot1", "ilink_user_id": "u9"},
	}
	creds, err := LoginQR(context.Background(), hs.URL, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Token != "bt-1" || creds.AccountID != "bot1" || creds.UserID != "u9" {
		t.Fatalf("登录凭证不符: %+v", creds)
	}
}

// TestStoreRoundtrip 凭证 yaml 存取与 0600。
func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "config", "ilink-wechat.yaml"))
	c, err := s.Load()
	if err != nil || c.Token != "" {
		t.Fatalf("缺失文件应返回空凭证: %+v %v", c, err)
	}
	in := &Credentials{Token: "t1", BaseURL: "https://x/", AccountID: "a", UserID: "u", SyncBuf: "b", Allow: []string{"wechat\x00u"}}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "t1" || got.SyncBuf != "b" || len(got.Allow) != 1 || got.Allow[0] != "wechat\x00u" {
		t.Fatalf("往返不符: %+v", got)
	}
	fi, _ := os.Stat(s.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("凭证文件应 0600,got %v", fi.Mode().Perm())
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
