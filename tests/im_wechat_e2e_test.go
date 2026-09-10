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
	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// wechatMock iLink 服务器:getupdates 可编程(首条入站);sendmessage/sendtyping 收集。
type wechatMock struct {
	mu          sync.Mutex
	updates     []map[string]any
	sendMsgs    []map[string]any
	typingShows int           // sendtyping status=1(show)次数
	notify      chan struct{} // sendmessage 到达通知
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
			var bd struct {
				Status int `json:"status"`
			}
			json.NewDecoder(r.Body).Decode(&bd)
			if bd.Status == 1 {
				m.mu.Lock()
				m.typingShows++
				m.mu.Unlock()
			}
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
	// typing 指示:回合期间应至少一次 status=1(show)(长回合可感知工作/断联)
	time.Sleep(400 * time.Millisecond)
	m.mu.Lock()
	shows := m.typingShows
	m.mu.Unlock()
	if shows < 1 {
		t.Fatalf("回合应发送 typing show,got %d", shows)
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

// TestImWechatAuthPersist 授权变化持久化:配对/allow 后写回凭证 store,重启恢复(模拟重载)。
func TestImWechatAuthPersist(t *testing.T) {
	m, hs := newWechatMock(t)
	c, home := buildWechatEnv(t, hs.URL, "allowlist", true)
	store := ilink.NewStore(filepath.Join(home, "config", "ilink-wechat.yaml"))
	var cs sdk.ConfirmService
	if err := c.Inject("ctx.confirm", &cs); err != nil {
		t.Fatal(err)
	}
	bridge := cs.(*im.Bridge)

	// 模拟主机 /im pair 批准新用户 → onChange → 落盘
	bridge.Access().Allow("wechat\x00u2")
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Allow) != 2 {
		t.Fatalf("批准后 Allow 应含 2 项并落盘,got %v", persisted.Allow)
	}
	// 模拟撤销
	bridge.Access().Revoke("wechat\x00user1")
	persisted, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Allow) != 1 || persisted.Allow[0] != "wechat\x00u2" {
		t.Fatalf("撤销应落盘,got %v", persisted.Allow)
	}
	_ = m
}

// TestIMCommandsSubLevels 通道命令二级选项契约(P:输入 /wechat 回车应有二级选择,
// 而非直接执行)——命令注册须声明 Args 参数级;/im 同。
func TestIMCommandsSubLevels(t *testing.T) {
	c, _ := buildWechatEnv(t, "", "allowlist", false)
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	// /wechat:二级枚举含 status/login
	wc, ok := cmds.Get("wechat")
	if !ok {
		t.Fatal("/wechat 未注册")
	}
	if len(wc.Args) == 0 {
		t.Fatal("/wechat 应声明 Args 二级选项(否则回车直接执行)")
	}
	vals := map[string]bool{}
	for _, o := range wc.Args[0].Options(nil) {
		vals[o.Value] = true
	}
	if !vals["status"] || !vals["login"] {
		t.Fatalf("/wechat 二级选项应含 status/login: %+v", vals)
	}
	// /im:二级枚举含 status/list/pair;pair 路径再要求配对码(自由参数)
	imCmd, ok := cmds.Get("im")
	if !ok {
		t.Fatal("/im 未注册")
	}
	if len(imCmd.Args) < 2 {
		t.Fatal("/im 应声明两级 Args")
	}
	imVals := map[string]bool{}
	for _, o := range imCmd.Args[0].Options(nil) {
		imVals[o.Value] = true
	}
	if !imVals["status"] || !imVals["list"] || !imVals["pair"] {
		t.Fatalf("/im 二级选项应含 status/list/pair: %+v", imVals)
	}
	// picked 语义 = [命令名, 第一级值, ...](回归:曾误用 picked[0])
	if got := imCmd.Args[1].FreeArgs([]string{"im", "pair"}); len(got) != 1 || got[0] != "配对码" {
		t.Fatalf("pair 应要求配对码自由参数: %+v", got)
	}
	if got := imCmd.Args[1].FreeArgs([]string{"im", "status"}); got != nil {
		t.Fatalf("status 路径不应有额外参数: %+v", got)
	}
	if got := imCmd.Args[1].FreeArgs([]string{"im", "list"}); got != nil {
		t.Fatalf("list 路径不应有额外参数: %+v", got)
	}
}

// TestImWechatSessionExpiredE2E 会话过期(ret=-14):清理失效凭证(token/游标)并停轮询,
// 诊断提示可见(避免重启后带失效凭证反复失败;auto_relogin 默认开时会再发起扫码)。
func TestImWechatSessionExpiredE2E(t *testing.T) {
	m, hs := newWechatMock(t)
	m.mu.Lock()
	m.updates = []map[string]any{{"ret": -14, "get_updates_buf": "", "msgs": []any{}}}
	m.mu.Unlock()
	_, home := buildWechatEnv(t, hs.URL, "allowlist", true)

	// auto_relogin 默认开:过期 → 清理失效凭证 → 自动重新扫码(mock 完成确认)→ 新凭证落盘。
	// 断言旧 token 被替换(mock 的自动登录凭证为 tk-auto),证明清理+重登闭环生效。
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		store := ilink.NewStore(filepath.Join(home, "config", "ilink-wechat.yaml"))
		if c, err := store.Load(); err == nil {
			last = c.Token
			if c.Token == "tk-auto" { // 自动重登成功:失效 token 已被替换
				if c.SyncBuf != "buf-live" && c.SyncBuf != "" {
					// 游标随新登录重置(具体值由 mock 决定,不做强断言)
					t.Logf("新登录后游标: %q", c.SyncBuf)
				}
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("会话过期后应自动重新登录(期望 tk-auto),当前 %q", last)
}
