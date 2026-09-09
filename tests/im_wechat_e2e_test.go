// ui-im-wechat 端到端(P0-2b):base + im-wechat bundle 真实装配 + mock iLink 服务器——
// 已登录(store 预置凭证)→ poll loop 收入站消息 → im.Bridge 回合(mock llm)→
// sendmessage 回推(mock 收集断言);未授权用户消息被静默丢弃。
package tests

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	imwechatb "github.com/nekoleamo/go-agent-harness/bundles/im-wechat"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/ilink"
)

// wechatMock iLink 服务器:getupdates 可编程(首条入站);sendmessage 收集。
type wechatMock struct {
	mu       sync.Mutex
	updates  []map[string]any
	sendMsgs []map[string]any
	notify   chan struct{} // sendmessage 到达通知
}

func newWechatMock(t *testing.T) (*wechatMock, *httptest.Server) {
	t.Helper()
	m := &wechatMock{notify: make(chan struct{}, 64)}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getupdates":
			m.mu.Lock()
			var resp map[string]any
			if len(m.updates) > 0 {
				resp = m.updates[0]
				m.updates = m.updates[1:]
			} else {
				resp = map[string]any{"ret": 0, "get_updates_buf": "b-live", "msgs": []any{}}
			}
			m.mu.Unlock()
			json.NewEncoder(w).Encode(resp)
		case "/ilink/bot/sendmessage":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.sendMsgs = append(m.sendMsgs, body)
			m.mu.Unlock()
			select {
			case m.notify <- struct{}{}:
			default:
			}
			w.Write([]byte(`{"ret":0}`))
		case "/ilink/bot/getconfig":
			w.Write([]byte(`{"ret":0,"ilink_user_id":"u","typing_ticket":"tkt-e2e"}`))
		case "/ilink/bot/sendtyping":
			w.Write([]byte(`{"ret":0}`))
		case "/ilink/bot/get_bot_qrcode":
			w.Write([]byte(`{"qrcode":"qr-auto","qrcode_img_content":"https://wx.example/qr-auto"}`))
		case "/ilink/bot/get_qrcode_status":
			w.Write([]byte(`{"status":"confirmed","bot_token":"tk-auto","ilink_bot_id":"bot-auto","ilink_user_id":"uAuto"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(hs.Close)
	return m, hs
}

func (m *wechatMock) sentTexts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, b := range m.sendMsgs {
		if msgv, ok := b["msg"].(map[string]any); ok {
			if items, ok := msgv["item_list"].([]any); ok && len(items) > 0 {
				if it, ok := items[0].(map[string]any); ok {
					if ti, ok := it["text_item"].(map[string]any); ok {
						out = append(out, fmt.Sprint(ti["text"]))
					}
				}
			}
		}
	}
	return out
}

func (m *wechatMock) waitSend(t *testing.T, wantSubstr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range m.sentTexts() {
			if strings.Contains(s, wantSubstr) {
				return s
			}
		}
		select {
		case <-m.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("超时未收到含 %q 的出站消息(已收: %v)", wantSubstr, m.sentTexts())
	return ""
}

const wechatScript = `[
  {"text":"收到: 远程命令已执行","finish":"stop"}
]`

// TestImWechatE2E 全链路:mock iLink 入站 → agent 回合 → sendmessage 回推;未授权用户静默。
func TestImWechatE2E(t *testing.T) {
	m, hs := newWechatMock(t)
	// 首条 getupdates 返回已授权用户入站;第二条起为未授权用户(应被静默丢弃)
	m.updates = []map[string]any{
		{"ret": 0, "get_updates_buf": "b1", "msgs": []map[string]any{{
			"message_type": 1, "from_user_id": "user1", "to_user_id": "bot",
			"context_token": "ct-1", "create_time_ms": int64(111),
			"item_list": []map[string]any{{"type": 1, "text_item": map[string]any{"text": "远程帮我执行"}}},
		}}},
		{"ret": 0, "get_updates_buf": "b2", "msgs": []map[string]any{{
			"message_type": 1, "from_user_id": "hacker", "to_user_id": "bot",
			"context_token": "ct-2", "create_time_ms": int64(222),
			"item_list": []map[string]any{{"type": 1, "text_item": map[string]any{"text": "rm -rf /"}}},
		}}},
	}

	// 装配(base+im-wechat,预置凭证=已登录;allowlist 只放行 user1)
	_, home := buildWechatEnv(t, hs.URL, "allowlist", true)
	store := ilink.NewStore(filepath.Join(home, "config", "ilink-wechat.yaml"))

	// 已登录 → poll loop 自动收第一条(授权用户)→ 回合 → sendmessage 回推
	got := m.waitSend(t, "远程命令已执行", 15*time.Second)
	if !strings.Contains(got, "远程命令已执行") {
		t.Fatalf("回推不符: %q", got)
	}
	// 未授权用户(hacker)消息:静默丢弃 —— 轮询若干窗口断言无新增出站
	time.Sleep(800 * time.Millisecond)
	if n := len(m.sentTexts()); n != 1 {
		t.Fatalf("未授权消息应被静默丢弃,出站 %d 条: %v", n, m.sentTexts())
	}
	// 凭证/allowlist 持久态仍在(未被打扰)
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Allow) != 1 || persisted.Allow[0] != "wechat\x00user1" || persisted.Token != "tk-e2e" {
		t.Fatalf("凭证/授权持久化缺失: %+v", persisted)
	}
}

// buildWechatEnv 装配 base+im-wechat(mock iLink;可带预置凭证文件)。
func buildWechatEnv(t *testing.T, baseURL, mode string, withCreds bool) (*ctx.Ctx, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if withCreds {
		store := ilink.NewStore(filepath.Join(home, "config", "ilink-wechat.yaml"))
		if err := store.Save(&ilink.Credentials{Token: "tk-e2e", BaseURL: baseURL + "/",
			AccountID: "bot", UserID: "u0", Allow: []string{"wechat\x00user1"}}); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": wechatScript}},
		{ID: "host-agent-loop"},
		{ID: "ui-im-wechat", Data: map[string]any{"base_url": baseURL + "/", "mode": mode}},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := imwechatb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, home
}

// TestImWechatAutoLogin 无凭证启动:auto_login 自动扫码(QR mock 立即确认)→ 存凭证/授权 →
// poll 收消息 → 回合回推(mock 断言)。
func TestImWechatAutoLogin(t *testing.T) {
	m, hs := newWechatMock(t)
	m.updates = []map[string]any{
		{"ret": 0, "get_updates_buf": "b1", "msgs": []map[string]any{{
			"message_type": 1, "from_user_id": "uAuto", "to_user_id": "bot",
			"context_token": "ct-auto", "create_time_ms": int64(333),
			"item_list": []map[string]any{{"type": 1, "text_item": map[string]any{"text": "你好"}}},
		}}},
	}
	_, home := buildWechatEnv(t, hs.URL, "allowlist", false)

	// auto_login 自动完成 → 凭证落盘(tk-auto)+ 扫码者授权 → 入站被处理并回推
	deadline := time.Now().Add(10 * time.Second)
	var creds *ilink.Credentials
	for time.Now().Before(deadline) {
		var err error
		creds, err = ilink.NewStore(filepath.Join(home, "config", "ilink-wechat.yaml")).Load()
		if err == nil && creds.Token == "tk-auto" && len(creds.Allow) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if creds == nil || creds.Token != "tk-auto" {
		t.Fatalf("auto_login 应落盘 tk-auto 凭证: %+v", creds)
	}
	if len(creds.Allow) != 1 || creds.Allow[0] != "wechat\x00uAuto" {
		t.Fatalf("扫码者应被自动授权: %+v", creds.Allow)
	}
	got := m.waitSend(t, "远程命令已执行", 15*time.Second)
	if !strings.Contains(got, "远程命令已执行") {
		t.Fatalf("回推不符: %q", got)
	}
}
