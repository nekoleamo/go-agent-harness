// 设置面板端点测试:/api/compact(手动滚动压缩)与 /api/settings/history(历史注入条数)。
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// compactLog memLog + CompactService/SetHistory 记录(供断言)。
type compactLog struct {
	*memLog
	folded  int
	summary string
	history *int // nil = 未调用;否则为最近一次 n
}

func (c *compactLog) Compact(prompt string) (string, int, error) {
	c.summary = "压缩摘要: " + prompt
	return c.summary, c.folded, nil
}
func (c *compactLog) SetHistory(n int) { c.history = &n }

func TestCompactEndpoint(t *testing.T) {
	s, _ := newTestServer()
	cl := &compactLog{memLog: &memLog{}, folded: 7}
	s.sessions = cl
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/compact", "application/json",
		strings.NewReader(`{"prompt":"压缩长会话"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("compact 应 200,得 %d", resp.StatusCode)
	}
	var out struct {
		OK      bool   `json:"ok"`
		Summary string `json:"summary"`
		Folded  int    `json:"folded"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !strings.Contains(out.Summary, "压缩长会话") || out.Folded != 7 {
		t.Fatalf("compact 响应不符: %+v", out)
	}
	if cl.summary != out.Summary {
		t.Fatalf("Compact 应被调用: %q", cl.summary)
	}

	// 未实现 CompactService → 501 显式(不静默)
	s2, _ := newTestServer() // 默认 memLog 不实现 Compact
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp2, _ := http.Post(hs2.URL+"/api/compact", "application/json", strings.NewReader(`{}`))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotImplemented {
		t.Fatalf("无 CompactService 应 501,得 %d", resp2.StatusCode)
	}
}

func TestSettingsHistoryEndpoint(t *testing.T) {
	s, _ := newTestServer()
	cl := &compactLog{memLog: &memLog{}}
	s.sessions = cl
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	for _, n := range []int{0, 10, -1} {
		resp, err := http.Post(hs.URL+"/api/settings/history", "application/json",
			strings.NewReader(`{"n":`+strconv.Itoa(n)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("n=%d 应 200,得 %d", n, resp.StatusCode)
		}
		resp.Body.Close()
		if cl.history == nil || *cl.history != n {
			t.Fatalf("SetHistory 未按 n=%d 调用: %v", n, cl.history)
		}
	}
	// 非法 n < -1 → 400
	resp, err := http.Post(hs.URL+"/api/settings/history", "application/json",
		strings.NewReader(`{"n":-5}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("n<-1 应 400,得 %d", resp.StatusCode)
	}
}

var _ sdk.CompactService = (*compactLog)(nil)

// —— 运行偏好持久化(web-state.json)——
type thinkingLLM struct {
	sdk.LLMService
	setThinking string
	setModel    string
}

func (t *thinkingLLM) SetThinking(l sdk.ThinkingLevel) { t.setThinking = l.String() }
func (t *thinkingLLM) SetModel(m string)               { t.setModel = m }

func TestPrefsPersistAndApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	// 写入偏好(模拟用户退出时设置)
	h := 20
	prefs.Save(prefs.Prefs{Thinking: "high", Sandbox: "full-access", History: &h})
	p := loadPrefs()
	if p.Thinking != "high" || p.Sandbox != "full-access" || p.History == nil || *p.History != 20 {
		t.Fatalf("roundtrip 不符: %+v", p)
	}

	// ApplyPrefs 应用:thinking/sandbox/history
	s, _ := newTestServer()
	tllm := &thinkingLLM{}
	s.llm = tllm
	cl := &compactLog{memLog: &memLog{}}
	s.sessions = cl
	s.sb = &stubSB{mode: sdk.SandboxWorkspace}
	s.ApplyPrefs()
	if tllm.setThinking != "high" {
		t.Fatalf("thinking 应恢复 high,got %q", tllm.setThinking)
	}
	if s.sb.Mode() != sdk.SandboxFullAccess {
		t.Fatalf("sandbox 应恢复 full-access,got %v", s.sb.Mode())
	}
	if cl.history == nil || *cl.history != 20 {
		t.Fatalf("history 应恢复 20,got %v", cl.history)
	}

	// 非法 thinking 值不阻塞(跳过该条)
	prefs.Save(prefs.Prefs{Thinking: "nope"})
	s2, _ := newTestServer()
	s2.llm = &thinkingLLM{}
	s2.ApplyPrefs() // 不应 panic/不应 set
	if llm2 := s2.llm.(*thinkingLLM); llm2.setThinking != "" {
		t.Fatalf("非法 thinking 不应应用,got %q", llm2.setThinking)
	}
}

func TestControlPersistsPrefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	s, _ := newTestServer()
	tllm := &thinkingLLM{}
	s.llm = tllm
	s.sb = &stubSB{mode: sdk.SandboxWorkspace}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/control", "application/json",
		strings.NewReader(`{"thinking":"low","sandbox":"read-only"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("control 应 200,得 %d", resp.StatusCode)
	}
	p := loadPrefs()
	if p.Thinking != "low" || p.Sandbox != "read-only" {
		t.Fatalf("control 后应持久化偏好: %+v", p)
	}
}

// TestCompactEndpointRefusedMidTurn 回合进行中压缩必须 409 + 明说原因(第一百六十七批)。
//
// 与 /compact 同一句话(单一事实源 sdk.MaintenanceBlocked):两条入口口径不一,
// 用户会以为这是两个功能。
func TestCompactEndpointRefusedMidTurn(t *testing.T) {
	s, _ := newTestServer()
	cl := &compactLog{memLog: &memLog{}, folded: 3}
	s.sessions = cl
	s.tc = &runningTurn{running: true}
	s.ctx = &turnCtx{tc: s.tc}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/compact", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("回合进行中应 409,得 %d", resp.StatusCode)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	msg := string(body[:n])
	if !strings.Contains(msg, "回合进行中") || !strings.Contains(msg, "/stop") {
		t.Fatalf("拒绝要说清原因与出口: %q", msg)
	}
	if cl.summary != "" {
		t.Fatal("被拒绝时**不能**已经压过(顺序:先闸门后动手)")
	}
}

// turnCtx 只多提供 ctx.turnControl 的最小 Ctx(其余走 testCtx 的默认实现)。
type turnCtx struct {
	testCtx
	tc sdk.TurnControl
}

func (c *turnCtx) Inject(key string, dst any) error {
	if key != "ctx.turnControl" || c.tc == nil {
		return nil // 与 testCtx 同口径:未装配不报错,MaintenanceBlocked 视为放行
	}
	*(dst.(*sdk.TurnControl)) = c.tc
	return nil
}

// runningTurn 固定 Running 的回合控制替身。
type runningTurn struct{ running bool }

func (r *runningTurn) Running() bool { return r.running }
func (r *runningTurn) Cancel()       {}
