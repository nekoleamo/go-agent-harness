// ui-im-qq 端到端(P0-2b-QQ,T2 文本闭环):base + im-qq bundle 真实装配 + mock QQ 服务器
// (WS gateway + REST OpenAPI)——已配置凭证(store 预置 AppID/AppSecret)→ gateway 上线(READY)→
// 入站 C2C → im.Bridge 回合(mock llm)→ REST 被动回复(msg_id/msg_seq 断言);
// 群 @ 消息 → 群回复;非 @ 机器人(mentions 不含 bot)丢弃;未授权用户静默;凭证/授权持久。
package tests

import (
	"context"
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

	"github.com/gorilla/websocket"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	confirmfusionb "github.com/nekoleamo/go-agent-harness/bundles/confirm-fusion"
	imqqb "github.com/nekoleamo/go-agent-harness/bundles/im-qq"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/qqbot"
	"github.com/nekoleamo/go-agent-harness/web"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// qqMock QQ 全栈服务器:REST(token 换取/发消息)+ WS gateway(/gateway/bot 分发 wss)。
// 事件在 Identify 后按队列逐条推送(间隔短,便于观察独立消息处理)。
type qqMock struct {
	mu        sync.Mutex
	events    []map[string]any // identify 后待推送的 Dispatch 事件(预置)
	sends     []sendRec        // 已收 REST 发送(单聊+群)
	sendsFreq int              // 前 N 次发送返回频控错误(40034100)
	notify    chan struct{}
	sendQ     chan map[string]any // 待推送事件(预置 + 测试动态 pushEvent)
	ackCh     chan struct{}       // 心跳 ack 转交写侧
	wsURL     string              // /gateway/bot 返回(服务地址已知后回填)
}

type sendRec struct {
	path string
	auth string
	body map[string]any
}

// bodyText 发送正文统一提取(content 或 markdown.content;typing/无正文消息返回空)。
func (s sendRec) bodyText() string {
	switch int(s.body["msg_type"].(float64)) {
	case qqbot.MsgTypeMarkdown:
		if md, ok := s.body["markdown"].(map[string]any); ok {
			if c, ok := md["content"].(string); ok {
				return c
			}
		}
	default:
		if c, ok := s.body["content"].(string); ok {
			return c
		}
	}
	return ""
}

func newQQMock(t *testing.T) (*qqMock, *httptest.Server) {
	t.Helper()
	m := &qqMock{notify: make(chan struct{}, 128), sendQ: make(chan map[string]any, 256), ackCh: make(chan struct{}, 16)}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tk-e2e", "expires_in": 7200})
		case "/gateway/bot":
			json.NewEncoder(w).Encode(map[string]any{"url": "ws://" + r.Host + "/ws", "shards": 1})
		case "/ws":
			conn, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			m.serveWS(conn)
		default:
			if strings.HasPrefix(r.URL.Path, "/v2/") {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				m.mu.Lock()
				m.sends = append(m.sends, sendRec{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
				isText := int(body["msg_type"].(float64)) != qqbot.MsgTypeInput // typing 不占正文频控
				freq := 0
				if isText && m.sendsFreq > 0 {
					m.sendsFreq--
					freq = 1
				}
				m.mu.Unlock()
				select {
				case m.notify <- struct{}{}:
				default:
				}
				if freq > 0 { // 频控:业务错误 40034100(主动超频),记录但拒绝
					json.NewEncoder(w).Encode(map[string]any{"code": 40034100, "message": "主动消息发送超过频控限制"})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "send-" + fmt.Sprint(len(m.sends)), "timestamp": time.Now().Format(time.RFC3339)})
				return
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(hs.Close)
	return m, hs
}

// serveWS 协议循环:Hello → Identify(断言后回 READY,user.id=BOTOPENID)→ 推事件队列 → 心跳 ACK。
func (m *qqMock) serveWS(conn *websocket.Conn) {
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 200}})
	var f map[string]any
	if err := conn.ReadJSON(&f); err != nil {
		return
	}
	if int(f["op"].(float64)) != 2 {
		return
	}
	d := f["d"].(map[string]any)
	if d["token"] != "QQBot tk-e2e" {
		return // 鉴权 token 不符:拒绝(防误连)
	}
	_ = conn.WriteJSON(map[string]any{"op": 0, "t": qqbot.EventReady, "s": 1, "d": map[string]any{
		"session_id": "sess-e2e", "user": map[string]any{"id": "BOTOPENID", "username": "gah-bot"}}})
	// 预置事件入队:由写侧单 goroutine 统一发送(心跳 ack 亦走写侧,避免并发写)
	m.mu.Lock()
	for _, e := range m.events {
		m.sendQ <- e
	}
	m.events = nil
	m.mu.Unlock()

	closed := make(chan struct{})
	defer close(closed)
	go func() { // 写侧:事件 + 心跳 ACK
		for {
			select {
			case e := <-m.sendQ:
				_ = conn.WriteJSON(e)
				time.Sleep(50 * time.Millisecond) // 事件间隔:防冲刷、便于观察逐条
			case <-m.ackCh:
				_ = conn.WriteJSON(map[string]any{"op": 11})
			case <-closed:
				return
			}
		}
	}()
	// 读侧:纯收;心跳转写侧应答
	for {
		if err := conn.ReadJSON(&f); err != nil {
			return
		}
		if int(f["op"].(float64)) == 1 {
			select {
			case m.ackCh <- struct{}{}:
			default:
			}
		}
	}
}

// pushEvent 动态追加一条入站事件(测试时序可控:确认推送后再投用户 y/n)。
func (m *qqMock) pushEvent(e map[string]any) { m.sendQ <- e }

func (m *qqMock) sentContents() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sends {
		switch int(s.body["msg_type"].(float64)) {
		case qqbot.MsgTypeText:
			if c, ok := s.body["content"].(string); ok && c != "" {
				out = append(out, c)
			}
		case qqbot.MsgTypeMarkdown: // 富文本正文在 markdown.content
			if md, ok := s.body["markdown"].(map[string]any); ok {
				if c, ok := md["content"].(string); ok && c != "" {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

func (m *qqMock) findSend(pred func(s sendRec) bool) (sendRec, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sends {
		if pred(s) {
			return s, true
		}
	}
	return sendRec{}, false
}

// waitSend 轮询直至收到正文(文本/markdown)含 wantSubstr 的出站记录。
func (m *qqMock) waitSend(t *testing.T, wantSubstr string, timeout time.Duration) sendRec {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rec, ok := m.findSend(func(s sendRec) bool { return strings.Contains(s.bodyText(), wantSubstr) }); ok {
			return rec
		}
		select {
		case <-m.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("超时未收到含 %q 的出站(已收: %v)", wantSubstr, m.sentContents())
	return sendRec{}
}

func (m *qqMock) sendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sends)
}

// textSendCount 仅统计文本类出站(排除 msg_type=6 输入状态;未授权静默断言用)。
func (m *qqMock) textSendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sends {
		if int(s.body["msg_type"].(float64)) != qqbot.MsgTypeInput {
			n++
		}
	}
	return n
}

// qqScript mock llm 脚本:一步文本收尾(回合闭环)。
const qqScript = `[{"text":"QQ 回推:远程命令已执行","finish":"stop"}]`

// c2cEvent 构造 C2C_MESSAGE_CREATE Dispatch 帧。
func c2cEvent(s int, id, openid, content string) map[string]any {
	return map[string]any{"op": 0, "t": qqbot.EventC2CMessage, "s": s, "d": map[string]any{
		"id": id, "author": map[string]any{"user_openid": openid},
		"content": content, "timestamp": "2026-10-01T00:00:00+08:00"}}
}

// TestImQQE2E 全链路:mock QQ WS 入站 C2C → agent 回合 → REST 被动回复(msg_id/msg_seq/鉴权头);
// 未授权用户(hacker)消息静默丢弃;凭证/授权持久不被打扰。
func TestImQQE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{
		c2cEvent(2, "qqmsg-1", "OPENID1", "远程帮我执行"),
		c2cEvent(3, "qqmsg-2", "hacker", "rm -rf /"),
	}
	home := buildQQEnv(t, hs.URL, "allowlist", qqScript)

	// 授权用户消息 → 回合 → 被动回复(带 msg_id=qqmsg-1;auth QQBot;单聊路径)
	rec := m.waitSend(t, "远程命令已执行", 20*time.Second)
	if rec.path != "/v2/users/OPENID1/messages" {
		t.Fatalf("应发单聊消息,path=%q", rec.path)
	}
	if rec.auth != "QQBot tk-e2e" {
		t.Fatalf("鉴权头不符: %q", rec.auth)
	}
	b := rec.body
	if b["msg_id"] != "qqmsg-1" {
		t.Fatalf("被动回复应带入站 msg_id,got %v", b["msg_id"])
	}
	if int(b["msg_type"].(float64)) != qqbot.MsgTypeText {
		t.Fatalf("纯文本回复应为 msg_type=0,got %v", b["msg_type"])
	}
	if seq, ok := b["msg_seq"].(float64); !ok || seq < 1 {
		t.Fatalf("msg_seq 应为 >=1 自增序号,got %v", b["msg_seq"])
	}
	// 未授权用户(hacker)消息:静默丢弃 —— 等待后断言文本类出站不再增长(仅 1 条)
	time.Sleep(1200 * time.Millisecond)
	if n := m.textSendCount(); n != 1 {
		t.Fatalf("未授权消息应被静默丢弃,文本出站 %d 条: %v", n, m.sentContents())
	}
	// 授权持久态未被破坏
	persisted, err := qqbot.NewStore(filepath.Join(home, "config", "qqbot.yaml")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Allow) != 1 || persisted.Allow[0] != "qq\x00OPENID1" {
		t.Fatalf("授权持久化缺失: %+v", persisted.Allow)
	}
}

// TestImQQGroupAtE2E 群 @:仅 @ 机器人(mentions 含 BOTOPENID)消息触发 → 群被动回复;
// 非 @ 机器人消息丢弃;群回复走群消息端点。
func TestImQQGroupAtE2E(t *testing.T) {
	m, hs := newQQMock(t)
	atBot := map[string]any{"op": 0, "t": qqbot.EventGroupAtMsg, "s": 2, "d": map[string]any{
		"id": "grpmsg-1", "author": map[string]any{"member_openid": "MEMBER9"},
		"group_openid": "GRP1", "content": "@gah 群命令", "timestamp": "2026-10-01T00:00:00+08:00",
		"mentions": []map[string]any{{"id": "BOTOPENID", "member_openid": "BOTMEMBER"}}}}
	notBot := map[string]any{"op": 0, "t": qqbot.EventGroupAtMsg, "s": 3, "d": map[string]any{
		"id": "grpmsg-2", "author": map[string]any{"member_openid": "MEMBER9"},
		"group_openid": "GRP1", "content": "你好", "timestamp": "2026-10-01T00:00:01+08:00",
		"mentions": []map[string]any{{"id": "OTHERUSER", "member_openid": "OTHER"}}}}
	m.events = []map[string]any{atBot, notBot}
	buildQQEnv(t, hs.URL, "allowlist-grp", qqScript)

	// @ 机器人 → 群回复(群端点 + 群 msg_id)
	rec := m.waitSend(t, "远程命令已执行", 20*time.Second)
	if rec.path != "/v2/groups/GRP1/messages" {
		t.Fatalf("应发群消息,path=%q", rec.path)
	}
	if rec.body["msg_id"] != "grpmsg-1" {
		t.Fatalf("群被动回复应带群事件 msg_id,got %v", rec.body["msg_id"])
	}
	// 非 @ 机器人消息:丢弃(文本出站仍 1 条)
	time.Sleep(1200 * time.Millisecond)
	if n := m.textSendCount(); n != 1 {
		t.Fatalf("非 @ 消息应被丢弃,文本出站 %d 条: %v", n, m.sentContents())
	}
}

// buildQQEnv 装配 base+im-qq(mock QQ;预置凭证=已配置;llm-mock 脚本可指定)。
// mode:"allowlist" → 放行 OPENID1(单聊);"allowlist-grp" → 放行 MEMBER9(群)。
func buildQQEnv(t *testing.T, baseURL, mode string, script any) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	allow := []string{}
	groups := []string{}
	switch mode {
	case "allowlist":
		allow = []string{"qq\x00OPENID1"}
	case "group-grant":
		// 群维度授权:整群放行,成员未单独授权(GRP1 内任意成员免配对)
		groups = []string{"qq\x00GRP1"}
	default: // allowlist-grp:成员级授权
		allow = []string{"qq\x00MEMBER9"}
	}
	store := qqbot.NewStore(filepath.Join(home, "config", "qqbot.yaml"))
	if err := store.Save(&qqbot.Credentials{AppID: "app-e2e", AppSecret: "sec-e2e", Allow: allow, Groups: groups}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-cwd-sessions"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": script}},
		{ID: "host-agent-loop"},
		{ID: "ui-im-qq", Data: map[string]any{
			"mode":      "allowlist",
			"base_url":  baseURL,
			"token_url": baseURL + "/app/getAppAccessToken",
		}},
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
	if err := imqqb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return home
}

// qqMarkdownScript mock llm 富文本回复(列表 + 代码块;验证 markdown 渲染决策)。
// 用 YAML 列表形态(parseScript 支持 []any),避免 JSON 字符串内的三重反引号转义地狱。
var qqMarkdownScript = []any{
	map[string]any{"text": "# 执行报告\n\n- 步骤一:完成\n- 步骤二:完成\n\n```json\n{\"status\":\"ok\",\"cost\":0}\n```", "finish": "stop"},
}

// TestImQQMarkdownE2E(T3 呈现):富文本回复 → markdown 单条(msg_type=2 + markdown.content 保整
// 代码块),不发纯文本;纯文本由 TestImQQE2E 已断言 msg_type=0。
func TestImQQMarkdownE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-md", "OPENID1", "给我一份报告")}
	buildQQEnv(t, hs.URL, "allowlist", qqMarkdownScript)

	rec := m.waitSend(t, "# 执行报告", 20*time.Second)
	if int(rec.body["msg_type"].(float64)) != qqbot.MsgTypeMarkdown {
		t.Fatalf("富文本应发 markdown(msg_type=2),got %v", rec.body["msg_type"])
	}
	md, ok := rec.body["markdown"].(map[string]any)
	if !ok {
		t.Fatalf("markdown 载荷缺失: %+v", rec.body)
	}
	content, _ := md["content"].(string)
	if !strings.Contains(content, "```json") || !strings.Contains(content, "\"status\":\"ok\"") ||
		!strings.Contains(content, "- 步骤一") {
		t.Fatalf("markdown 正文保整缺失: %q", content)
	}
	if _, hasContent := rec.body["content"]; hasContent {
		t.Fatalf("markdown 消息不应带 content 字段(官方互斥)")
	}
	// 不应同时发纯文本副本
	if len(m.sentContents()) != 1 {
		t.Fatalf("富文本只应发 1 条,got %v", m.sentContents())
	}
}

// TestImQQTypingE2E(T3 typing):回合期间发 input_notify(msg_type=6, input_type=1 正在输入);
// 回合结束发 input_type=0(取消)。best-effort,断言 show 至少一次(mock 同步首发保证必见)。
func TestImQQTypingE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-ty", "OPENID1", "执行一个长任务")}
	buildQQEnv(t, hs.URL, "allowlist", qqScript)

	// 等回合文本回推(此时 typing show/stop 已发出)
	if _, err := func() (sendRec, error) { return m.waitSend(t, "远程命令已执行", 20*time.Second), nil }(); err != nil {
		t.Fatal(err)
	}
	// 断言存在 msg_type=6 input_type=1(正在输入)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rec, ok := m.findSend(func(s sendRec) bool {
			if int(s.body["msg_type"].(float64)) != qqbot.MsgTypeInput {
				return false
			}
			ni, ok := s.body["input_notify"].(map[string]any)
			return ok && int(ni["input_type"].(float64)) == 1
		}); ok {
			if rec.path != "/v2/users/OPENID1/messages" {
				t.Fatalf("typing 应走单聊端点,path=%q", rec.path)
			}
			return
		}
		select {
		case <-m.notify:
		case <-time.After(30 * time.Millisecond):
		}
	}
	t.Fatalf("回合期间应发送 input_notify(show);已收: %+v", m.sends)
}

// TestImQQLongTextE2E(T3 分块):>4000 字回复降级纯文本分块(不切裂 markdown 语法),
// 每块 ≤4000、msg_seq 递增、块数 = ⌈len/4000⌉;不含富文本单条(markdown 只单条发)。
func TestImQQLongTextE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-long", "OPENID1", "给我长报告")}
	long := strings.Repeat("很长的报告内容", 3000) // 15000 字 → 4 块
	buildQQEnv(t, hs.URL, "allowlist", []any{map[string]any{"text": long, "finish": "stop"}})

	deadline := time.Now().Add(20 * time.Second)
	const limit = 4000 // QQ 单条文本上限(与 ui-im-qq qqChunkLimit 同值;测试包不导内部常量)
	wantBlocks := (len([]rune(long)) + limit - 1) / limit
	var blocks []sendRec
	lastIdx := 0 // 增量收集:同一条只计一次(轮询多轮不重复 append)
	for time.Now().Before(deadline) && len(blocks) < wantBlocks {
		m.mu.Lock()
		for _, s := range m.sends[lastIdx:] {
			if int(s.body["msg_type"].(float64)) != qqbot.MsgTypeText {
				continue
			}
			if c, ok := s.body["content"].(string); ok && c != "" {
				blocks = append(blocks, s)
			}
		}
		lastIdx = len(m.sends)
		m.mu.Unlock()
		if len(blocks) < wantBlocks {
			select {
			case <-m.notify:
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	if len(blocks) != wantBlocks {
		t.Fatalf("长回复应分 %d 块,got %d", wantBlocks, len(blocks))
	}
	var got int
	for i, b := range blocks {
		c, _ := b.body["content"].(string)
		if n := len([]rune(c)); n > limit {
			t.Fatalf("块 %d 超限 %d", i, n)
		}
		if seq, ok := b.body["msg_seq"].(float64); !ok || int(seq) != i+1 {
			t.Fatalf("块 %d msg_seq 应为 %d,got %v", i, i+1, b.body["msg_seq"])
		}
		got += len([]rune(c))
	}
	// 拼接后与原文一致(分块保内容完整)
	if got != len([]rune(long)) {
		t.Fatalf("分块拼接长度不符: %d vs %d", got, len([]rune(long)))
	}
}

// fakeQQShell 进程内假 shell 工具(触发 policy 危险命令审批;不真实执行,复用 im e2e 手法)。
type fakeQQShell struct{}

func (f *fakeQQShell) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "shell", Description: "执行命令(fake,e2e)", InputSchema: map[string]any{"type": "object"}}
}
func (f *fakeQQShell) Execute(_ context.Context, _ string) (any, error) {
	return map[string]any{"ok": true, "out": "(fake)"}, nil
}

// qqDangerScript smart 审批触发:危险命令(rm -rf)→ 确认 → 工具执行 → 文本收尾。
var qqDangerScript = []any{
	map[string]any{"tool": map[string]any{"name": "shell", "args": "rm -rf /tmp/qq-im-e2e-x"}},
	map[string]any{"text": "已清理临时文件", "finish": "stop"},
}

// qqDangerDenyScript 拒绝分支:确认被拒后模型收尾文案(不执行工具)。
var qqDangerDenyScript = []any{
	map[string]any{"tool": map[string]any{"name": "shell", "args": "rm -rf /tmp/qq-im-e2e-x"}},
	map[string]any{"text": "已取消,未执行任何操作", "finish": "stop"},
}

// buildQQApproveEnv 审批装配 = buildQQEnv 集 + policy-guard(smart)+ 假 shell(触发危险确认)。
func buildQQApproveEnv(t *testing.T, baseURL string, script any) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	store := qqbot.NewStore(filepath.Join(home, "config", "qqbot.yaml"))
	if err := store.Save(&qqbot.Credentials{AppID: "app-e2e", AppSecret: "sec-e2e", Allow: []string{"qq\x00OPENID1"}}); err != nil {
		t.Fatal(err)
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
		{ID: "llm-mock", Data: map[string]any{"script": script}},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "host-agent-loop"},
		{ID: "ui-im-qq", Data: map[string]any{
			"mode":      "allowlist",
			"base_url":  baseURL,
			"token_url": baseURL + "/app/getAppAccessToken",
		}},
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
	if err := imqqb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	// 假 shell 注册在宿主启动后(host-tools 经 StartSubset Provide ctx.tools)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(&fakeQQShell{}) // 假 shell 注册(触发 policy 危险确认)
	t.Cleanup(func() { reg.DisposeAll() })
}

// TestImQQApproveFlowE2E(T4 交互-批准):QQ 消息触发危险命令 → confirm 文本推送 → 用户 QQ 回 y
// → 工具执行 → 回合完成回推。交互全程经 QQ 通道(真实 ui-im-qq + mock server)。
func TestImQQApproveFlowE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-a1", "OPENID1", "帮我清理临时文件")}
	buildQQApproveEnv(t, hs.URL, qqDangerScript)

	// 1. 审批确认消息经 QQ 推送(含文字 y/n 提示)
	rec := m.waitSend(t, "需要确认", 20*time.Second)
	if !strings.Contains(rec.bodyText(), "回复 y 批准 / n 拒绝") {
		t.Fatalf("确认提示应含 y/n 指引: %q", rec.bodyText())
	}
	// 2. 用户经 QQ 回复 y → 批准 → 工具执行
	m.pushEvent(c2cEvent(3, "qqmsg-a2", "OPENID1", "y"))
	// 3. 回合完成回推最终文本
	got := m.waitSend(t, "已清理临时文件", 20*time.Second)
	if !strings.Contains(got.bodyText(), "已清理临时文件") {
		t.Fatalf("批准后应回推完成文本: %q", got.bodyText())
	}
}

// TestImQQDenyFlowE2E(T4 交互-拒绝):QQ 回 n → 工具被 veto(拒绝回执推送)→ 回合继续收尾。
func TestImQQDenyFlowE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-d1", "OPENID1", "帮我清理临时文件")}
	buildQQApproveEnv(t, hs.URL, qqDangerDenyScript)

	// 1. 确认推送
	if rec := m.waitSend(t, "需要确认", 20*time.Second); !strings.Contains(rec.bodyText(), "回复 y 批准 / n 拒绝") {
		t.Fatalf("确认提示缺 y/n: %q", rec.bodyText())
	}
	// 2. 用户 QQ 回 n → 拒绝回执推送(操作未执行)
	m.pushEvent(c2cEvent(3, "qqmsg-d2", "OPENID1", "n"))
	if rec := m.waitSend(t, "❌ 已拒绝", 20*time.Second); !strings.Contains(rec.bodyText(), "操作未执行") {
		t.Fatalf("拒绝回执不符: %q", rec.bodyText())
	}
	// 3. 回合继续,模型收尾(被拒信息回传模型后正常结束,不发"已清理")
	got := m.waitSend(t, "已取消", 20*time.Second)
	if strings.Contains(got.bodyText(), "已清理") {
		t.Fatalf("拒绝后不应回推执行成功文案: %q", got.bodyText())
	}
}

// TestImQQRateLimitStashE2E(T5 频控/滞留):发送遇频控(40034100)→ 不重试轰炸(仅 1 次尝试);
// 内容滞留 outbox;mock 恢复后用户再来消息 → 滞留内容先被动补发,随后正常回合回复。
func TestImQQRateLimitStashE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.sendsFreq = 1 // 首次发送返回频控
	m.events = []map[string]any{c2cEvent(2, "qqmsg-f1", "OPENID1", "帮我查个事")}
	buildQQEnv(t, hs.URL, "allowlist", qqScript)

	// 1. 频控:正文一次尝试即停(等尝试发生)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && m.textSendCount() == 0 {
		select {
		case <-m.notify:
		case <-time.After(30 * time.Millisecond):
		}
	}
	if m.textSendCount() == 0 {
		t.Fatal("频控尝试应发生")
	}
	time.Sleep(600 * time.Millisecond) // 观察窗口:不得轰炸重试
	if n := m.textSendCount(); n != 1 {
		t.Fatalf("频控后不应重试轰炸,正文尝试 %d 次", n)
	}
	// 2. 恢复后用户再来消息:滞留内容先补发(旧回复),再正常回应当前消息
	m.pushEvent(c2cEvent(3, "qqmsg-f2", "OPENID1", "还在吗"))
	// 补发滞留 + 新回合回复:最终至少 3 次发送(频控尝试 + 补发 + 新回复)
	pollQQSends(t, m, 3, 15*time.Second)
	texts := m.sentContents()
	if len(texts) < 2 { // 尝试那条也记录了(含文本)
		t.Fatalf("应含补发与新回复文本: %v", texts)
	}
	// P2 二期:补发内容带"可能重复"提示(此前投递失败过,可能已送达)
	dup := false
	for _, tx := range texts {
		if strings.Contains(tx, "♻️ 可能重复") {
			dup = true
		}
	}
	if !dup {
		t.Fatalf("补发应带 ♻️ 可能重复 前缀: %v", texts)
	}
}

func pollQQSends(t *testing.T, m *qqMock, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.textSendCount() >= want {
			return
		}
		select {
		case <-m.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("超时未达 %d 次正文发送,当前 %d", want, m.textSendCount())
}

// TestImQQSessionCommandsE2E(P1 会话绑定命令面):真实装配(含 host-cwd-sessions)全链路——
// QQ /new 新建并绑定会话 → 普通消息回合(落绑定会话)→ /history 回读绑定会话内容。
// 命令经 QQ 通道执行;回合前按绑定切会话(host 启动会话被覆盖)。
func TestImQQSessionCommandsE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-b1", "OPENID1", "/new")}
	buildQQEnv(t, hs.URL, "allowlist", qqScript)

	// 1. /new → 回复含新会话 id(纯文本 msg_type=0)
	rec := m.waitSend(t, "已新建会话并绑定", 20*time.Second)
	if int(rec.body["msg_type"].(float64)) != qqbot.MsgTypeText {
		t.Fatalf("/new 回复应为纯文本: %+v", rec.body)
	}
	// 2. 普通文本消息 → 回合(写入绑定会话)→ QQ 回推脚本文本
	m.pushEvent(c2cEvent(3, "qqmsg-b2", "OPENID1", "你好 QQ"))
	if got := m.waitSend(t, "QQ 回推:远程命令已执行", 20*time.Second); int(got.body["msg_type"].(float64)) != qqbot.MsgTypeText {
		t.Fatalf("回合回复应为纯文本: %+v", got.body)
	}
	// 3. /history → 回读绑定会话(user 事件 + assistant 事件)
	m.pushEvent(c2cEvent(4, "qqmsg-b3", "OPENID1", "/history"))
	h := m.waitSend(t, "❯ 你好 QQ", 20*time.Second)
	if !strings.Contains(h.bodyText(), "🤖 QQ 回推:远程命令已执行") {
		t.Fatalf("/history 应回读绑定会话的问答: %q", h.bodyText())
	}
}

// TestImQQWebFusionE2E(P3 三端融合):base+confirm-fusion+im-qq 真实装配(ui-im-qq 插件
// 检测 ctx.confirmFusion → 注册呈现者不再 Provide)+ 手动装配 web 呈现者 → ctx.confirm
// 单实例(fusion)。QQ 危险命令 → policy smart → fusion 广播双渠道(web+im)——
// web 侧不答、IM 回 y → 批准执行;验证:双 Provide 不冲突 + 首答生效。
func TestImQQWebFusionE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-f1", "OPENID1", "帮我清理临时文件")}
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	store := qqbot.NewStore(filepath.Join(home, "config", "qqbot.yaml"))
	if err := store.Save(&qqbot.Credentials{AppID: "app-e2e", AppSecret: "sec-e2e", Allow: []string{"qq\x00OPENID1"}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-cwd-sessions"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": qqDangerScript}},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "host-agent-loop"},
		{ID: "host-confirm-fusion"},
		{ID: "ui-im-qq", Data: map[string]any{
			"mode":      "allowlist",
			"base_url":  hs.URL,
			"token_url": hs.URL + "/app/getAppAccessToken",
		}},
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
	if err := imqqb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := confirmfusionb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	// StartSubset 后:注入 fusion/tools;手动装配 web 呈现者(等价 ui-web-app 融合
	// 分支:注入 fusion 注册,不 Provide ctx.confirm——双 Provide 冲突即此防住)
	var fusion sdk.ConfirmFusion
	if err := c.Inject("ctx.confirmFusion", &fusion); err != nil {
		t.Fatalf("fusion 未装配: %v", err)
	}
	webConfirm := web.NewConfirm(web.NewHub())
	fusion.Register("web", webConfirm)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(&fakeQQShell{})

	// 1. QQ 危险命令 → 确认推送(经 fusion 广播 → im 呈现者文本 y/n)
	rec := m.waitSend(t, "需要确认", 20*time.Second)
	if !strings.Contains(rec.bodyText(), "回复 y 批准 / n 拒绝") {
		t.Fatalf("IM 呈现应带 y/n: %q", rec.bodyText())
	}
	// 2. 双端同卡:web 呈现者也被调用(Present 收到同一 prompt → 弹层帧推送 hub)
	if !webConfirmHasPending(t, webConfirm, 3*time.Second) {
		t.Fatal("web 呈现者应收到确认(双端同卡)")
	}
	// 3. web 不答,IM 回 y → 批准执行(首答生效)
	m.pushEvent(c2cEvent(3, "qqmsg-f2", "OPENID1", "y"))
	got := m.waitSend(t, "已清理临时文件", 20*time.Second)
	if !strings.Contains(got.bodyText(), "已清理临时文件") {
		t.Fatalf("批准后应执行并回推: %q", got.bodyText())
	}
	// 4. ctx.confirm 单实例(未 Provide 冲突;policy-guard 经 fusion 注入)
	var cs sdk.ConfirmService
	if err := c.Inject("ctx.confirm", &cs); err != nil {
		t.Fatalf("ctx.confirm 应可用: %v", err)
	}
}

// webConfirmHasPending 断言 web ConfirmService 有未决弹层(经 Present 推送后 pending 非空)。
func webConfirmHasPending(t *testing.T, s *web.ConfirmService, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.PendingCount() > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestImQQKeyboardConfirmE2E(QQ 键盘 confirm):危险命令 → 确认文本后附键盘消息
// (msg_type=1,含 批准/拒绝 按钮,action.type=2 reply)→ 用户"点击"(回文本"批准")
// → 批准执行(按钮文本落入文字 y/n 管线,零新增回调协议)。
func TestImQQKeyboardConfirmE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-k1", "OPENID1", "帮我清理临时文件")}
	buildQQApproveEnv(t, hs.URL, qqDangerScript)

	// 1. 确认文本(y/n 指引)
	rec := m.waitSend(t, "需要确认", 20*time.Second)
	if !strings.Contains(rec.bodyText(), "回复 y 批准 / n 拒绝") {
		t.Fatalf("确认说明缺 y/n: %q", rec.bodyText())
	}
	// 2. 键盘消息伴随(msg_type=1 + keyboard 按钮批准/拒绝)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if kb, ok := m.findSend(func(s sendRec) bool {
			if int(s.body["msg_type"].(float64)) != qqbot.MsgTypeKeyboard {
				return false
			}
			kd, ok := s.body["keyboard"].(map[string]any)
			return ok && kd != nil
		}); ok {
			_ = kb
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("确认应附键盘消息: %v", m.sentContents())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 3. 点击"批准"= 回文本"批准"(reply 按钮)→ 现有 y/n 管线批准 → 执行回推
	m.pushEvent(c2cEvent(3, "qqmsg-k2", "OPENID1", "批准"))
	got := m.waitSend(t, "已清理临时文件", 20*time.Second)
	if !strings.Contains(got.bodyText(), "已清理临时文件") {
		t.Fatalf("键盘批准后应执行: %q", got.bodyText())
	}
}

// TestIMQQCommandSubLevels /qq 二级选项契约:status/login 枚举;login 路径再要求
// AppID/AppSecret 两个自由参数(选择器逐级输入)。
func TestIMQQCommandSubLevels(t *testing.T) {
	m, hs := newQQMock(t)
	_ = m // 仅需 mock 端点地址装配通道;命令契约断言不跑回合
	c := mustQQEnvCtx(t, hs.URL)
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	qc, ok := cmds.Get("qq")
	if !ok {
		t.Fatal("/qq 未注册")
	}
	if len(qc.Args) < 2 {
		t.Fatal("/qq 应声明两级 Args(枚举 + 登录凭据)")
	}
	vals := map[string]bool{}
	for _, o := range qc.Args[0].Options(nil) {
		vals[o.Value] = true
	}
	if !vals["status"] || !vals["login"] {
		t.Fatalf("/qq 二级选项应含 status/login: %+v", vals)
	}
	// picked 语义 = [命令名, 第一级值, ...](回归:曾误用 picked[0] 致二级永不出现)
	got := qc.Args[1].FreeArgs([]string{"qq", "login"})
	if len(got) != 2 || got[0] != "AppID" || got[1] != "AppSecret" {
		t.Fatalf("login 路径应要求 AppID/AppSecret: %+v", got)
	}
	if got := qc.Args[1].FreeArgs([]string{"qq", "status"}); got != nil {
		t.Fatalf("status 路径不应有额外参数: %+v", got)
	}
	if got := qc.Args[1].FreeArgs([]string{"qq"}); got != nil {
		t.Fatalf("仅命令名(未选级)不应返回参数: %+v", got)
	}
}

// mustQQEnvCtx 装配 base+im-qq 并返回 Ctx(命令契约断言用;不参与回合,无需 mock 事件)。
func mustQQEnvCtx(t *testing.T, baseURL string) sdk.Ctx {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	store := qqbot.NewStore(filepath.Join(home, "config", "qqbot.yaml"))
	if err := store.Save(&qqbot.Credentials{AppID: "app-e2e", AppSecret: "sec-e2e", Allow: []string{"qq\x00OPENID1"}}); err != nil {
		t.Fatal(err)
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
		{ID: "llm-mock", Data: map[string]any{"script": qqScript}},
		{ID: "host-agent-loop"},
		{ID: "ui-im-qq", Data: map[string]any{
			"mode": "allowlist", "base_url": baseURL, "token_url": baseURL + "/app/getAppAccessToken",
		}},
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
	if err := imqqb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// qqAskScript 语义交互脚本:模型先结构化提问,收到作答后收尾。
var qqAskScript = []any{
	map[string]any{"tool": map[string]any{
		"name": "ask_user_question",
		"args": `{"prompt":"部署到哪个环境?","options":[{"value":"dev","desc":"开发"},{"value":"prod","desc":"生产"}]}`,
	}},
	map[string]any{"text": "已选生产环境,开始部署", "finish": "stop"},
}

// buildQQAskEnv 语义交互装配 = base + confirm-fusion + im-qq + tool-ask(mock QQ)。
func buildQQAskEnv(t *testing.T, baseURL string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	store := qqbot.NewStore(filepath.Join(home, "config", "qqbot.yaml"))
	if err := store.Save(&qqbot.Credentials{AppID: "app-e2e", AppSecret: "sec-e2e", Allow: []string{"qq\x00OPENID1"}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-cwd-sessions"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": qqAskScript}},
		{ID: "host-agent-loop"},
		{ID: "host-confirm-fusion"},
		{ID: "tool-ask"},
		{ID: "ui-im-qq", Data: map[string]any{
			"mode": "allowlist", "base_url": baseURL, "token_url": baseURL + "/app/getAppAccessToken",
		}},
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
	if err := imqqb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := confirmfusionb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
}

// TestImQQAskQuestionE2E(P3 语义交互):模型调 ask_user_question → QQ 收到编号选项 →
// 用户回 "2" → 作答经问答管道回填工具 → 回合收尾(全程零额外回调协议)。
func TestImQQAskQuestionE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{c2cEvent(2, "qqmsg-q1", "OPENID1", "帮我部署")}
	buildQQAskEnv(t, hs.URL)

	// 1. 结构化提问推送(编号选项)
	rec := m.waitSend(t, "部署到哪个环境?", 20*time.Second)
	body := rec.bodyText()
	if !strings.Contains(body, "1) 开发") || !strings.Contains(body, "2) 生产") || !strings.Contains(body, "回复编号") {
		t.Fatalf("提问应含编号选项与作答指引: %q", body)
	}
	// 2. 用户回编号 2 → 作答回填
	m.pushEvent(c2cEvent(3, "qqmsg-q2", "OPENID1", "2"))
	// 3. 工具返回作答 → 模型收尾文本
	got := m.waitSend(t, "已选生产环境", 20*time.Second)
	if !strings.Contains(got.bodyText(), "开始部署") {
		t.Fatalf("收尾文本不符: %q", got.bodyText())
	}
}

// TestImQQGroupGrantE2E 群维度授权:凭证 Groups 预置整群 → 群内成员(未单独 pair)@机器人即放行。
func TestImQQGroupGrantE2E(t *testing.T) {
	m, hs := newQQMock(t)
	m.events = []map[string]any{{"op": 0, "t": qqbot.EventGroupAtMsg, "s": 2, "d": map[string]any{
		"id": "grpmsg-g1", "author": map[string]any{"member_openid": "STRANGER7"},
		"group_openid": "GRP1", "content": "@gah 群命令", "timestamp": "2026-10-01T00:00:00+08:00",
		"mentions": []map[string]any{{"id": "BOTOPENID", "member_openid": "BOTMEMBER"}}}}}
	buildQQEnv(t, hs.URL, "group-grant", qqScript)

	// 群授权放行 → 群回复(成员 STRANGER7 无单独授权)
	rec := m.waitSend(t, "远程命令已执行", 20*time.Second)
	if rec.path != "/v2/groups/GRP1/messages" {
		t.Fatalf("群内成员应被群授权放行: path=%q", rec.path)
	}
}
