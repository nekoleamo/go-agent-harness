package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeDir 实现 sdk.SessionDir(测试用):非当前会话 → 一个独立 memLog。
type fakeDir struct {
	main   sdk.SessionLog
	cur    string
	byID   map[string]*memLog
	spawnN int
}

func newFakeDir(main sdk.SessionLog, cur string) *fakeDir {
	return &fakeDir{main: main, cur: cur, byID: map[string]*memLog{}}
}

func (d *fakeDir) Acquire(id string) (sdk.SessionLog, error) {
	if id == "" || id == d.cur {
		return d.main, nil
	}
	if l, ok := d.byID[id]; ok {
		return l, nil
	}
	l := &memLog{}
	d.byID[id] = l
	return l, nil
}
func (d *fakeDir) Release(id string) {}
func (d *fakeDir) Active() []string {
	out := make([]string, 0, len(d.byID))
	for id := range d.byID {
		out = append(out, id)
	}
	return out
}
func (d *fakeDir) Spawn() (string, error) {
	d.spawnN++
	id := "spawned-1"
	d.byID[id] = &memLog{}
	return id, nil
}

type fakeCS struct {
	cur string
}

func (c *fakeCS) Current() string                             { return "key" }
func (c *fakeCS) Path() string                                { return "" }
func (c *fakeCS) List() []string                              { return nil }
func (c *fakeCS) Sessions() []sdk.SessionInfo                 { return nil }
func (c *fakeCS) CurrentSession() string                      { return c.cur }
func (c *fakeCS) Open(string) error                           { return nil }
func (c *fakeCS) New() (string, error)                        { return "", nil }
func (c *fakeCS) Delete(string) error                         { return nil }
func (c *fakeCS) RecentProjects() []sdk.ProjectInfo           { return nil }
func (c *fakeCS) SetName(string, string) error                { return nil }
func (c *fakeCS) SetSummary(string, sdk.SessionSummary) error { return nil }
func (c *fakeCS) SetPinned(string, bool) error                { return nil }
func (c *fakeCS) Rename(string) error                         { return nil }
func (c *fakeCS) UnrecordProject(string) error                { return nil }
func (c *fakeCS) SessionName() string                         { return "" }
func (c *fakeCS) SwitchProject(string) (string, error)        { return "", nil }
func (c *fakeCS) SwitchDir(string) (string, error)            { return "", nil }

// TestSessionEventsScopedToOtherSession 读侧:?session= 读的是那个会话自己的历史。
func TestSessionEventsScopedToOtherSession(t *testing.T) {
	s, main := newTestServer()
	other := &memLog{}
	_ = other.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 1})
	s.sdir = newFakeDir(main, "cur-1")
	s.sdir.(*fakeDir).byID["other-9"] = other

	req := httptest.NewRequest(http.MethodGet, "/api/session/events?session=other-9", nil)
	rec := httptest.NewRecorder()
	s.handleSessionEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Events []sdk.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("应只拿到该会话的 1 条,got %d", len(page.Events))
	}
	if len(main.Replay()) != 0 {
		t.Fatal("不应污染主会话")
	}
}

// TestSessionEventsUnscopedIsMainSession 未带 session 时行为不变(读主单例)。
func TestSessionEventsUnscopedIsMainSession(t *testing.T) {
	s, main := newTestServer()
	_ = main.Append(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Seq: 1})
	req := httptest.NewRequest(http.MethodGet, "/api/session/events", nil)
	rec := httptest.NewRecorder()
	s.handleSessionEvents(rec, req)
	if !strings.Contains(rec.Body.String(), "turn/end") {
		t.Fatalf("未带 session 应读主会话,got %s", rec.Body.String())
	}
}

// TestInputToOtherSessionRejected 写侧闸门:agent-loop 未支持 per-session 时必须 409。
// 这是防「静默写进另一个会话」的关键用例。
func TestInputToOtherSessionRejected(t *testing.T) {
	s, main := newTestServer()
	s.cs = &fakeCS{cur: "cur-1"}
	s.sdir = newFakeDir(main, "cur-1")

	req := httptest.NewRequest(http.MethodPost, "/api/input", strings.NewReader(`{"content":"hi","session":"other-9"}`))
	rec := httptest.NewRecorder()
	s.handleInput(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("应 409,got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "尚未支持") {
		t.Fatalf("文案应说明原因,got %s", rec.Body.String())
	}
	if len(main.Replay()) != 0 {
		t.Fatal("被拒的提交不得落进主会话")
	}
}

// TestInputToCurrentSessionAllowed 未带 session(以及带当前会话 id)照旧受理。
func TestInputToCurrentSessionAllowed(t *testing.T) {
	s, main := newTestServer()
	s.cs = &fakeCS{cur: "cur-1"}
	s.sdir = newFakeDir(main, "cur-1")

	rec := httptest.NewRecorder()
	s.handleInput(rec, httptest.NewRequest(http.MethodPost, "/api/input", strings.NewReader(`{"content":"hi","session":"cur-1"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("当前会话应受理,got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestConfirmFromOtherSessionRejected 跨会话应答审批必须拒(防一个窗口替另一个应答)。
func TestConfirmFromOtherSessionRejected(t *testing.T) {
	s, main := newTestServer()
	s.cs = &fakeCS{cur: "cur-1"}
	s.sdir = newFakeDir(main, "cur-1")
	rec := httptest.NewRecorder()
	s.handleConfirm(rec, httptest.NewRequest(http.MethodPost, "/api/confirm", strings.NewReader(`{"id":"x","ok":true,"session":"other-9"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("应 409,got %d", rec.Code)
	}
}

// TestStateRunningScoped / ActiveSessions 快照按会话报 running,并列出活跃会话。
func TestStateRunningScopedAndActive(t *testing.T) {
	s, main := newTestServer()
	s.cs = &fakeCS{cur: "cur-1"}
	d := newFakeDir(main, "cur-1")
	d.byID["other-9"] = &memLog{}
	s.sdir = d
	s.running.Store(true)

	rec := httptest.NewRecorder()
	s.handleState(rec, httptest.NewRequest(http.MethodGet, "/api/state?session=other-9", nil))
	var v StateView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if v.Running {
		t.Fatal("非主会话在并行回合落地前不应报 running(不能谎报)")
	}
	if v.SessionID != "other-9" {
		t.Fatalf("应回显所查会话,got %q", v.SessionID)
	}
	if len(v.ActiveSessions) != 1 || v.ActiveSessions[0] != "other-9" {
		t.Fatalf("应列出活跃会话,got %v", v.ActiveSessions)
	}

	rec2 := httptest.NewRecorder()
	s.handleState(rec2, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var v2 StateView
	_ = json.Unmarshal(rec2.Body.Bytes(), &v2)
	if !v2.Running {
		t.Fatal("主会话回合中应报 running")
	}
}

// TestSpawnSessionDoesNotSwitchCurrent spawn 建独立会话且不动当前。
func TestSpawnSessionDoesNotSwitch(t *testing.T) {
	s, main := newTestServer()
	s.cs = &fakeCS{cur: "cur-1"}
	s.sdir = newFakeDir(main, "cur-1")
	rec := httptest.NewRecorder()
	s.handleSessions(rec, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"action":"spawn"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("spawn 失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if out.ID == "" {
		t.Fatal("应返回新会话 id")
	}
	if s.cs.(*fakeCS).cur != "cur-1" {
		t.Fatal("spawn 不得切换当前会话")
	}
}

// TestSpawnWithoutSessionDir501 未装配目录时显式 501(不假装成功)。
func TestSpawnWithoutSessionDir501(t *testing.T) {
	s, _ := newTestServer()
	s.cs = &fakeCS{cur: "cur-1"}
	rec := httptest.NewRecorder()
	s.handleSessions(rec, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"action":"spawn"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("应 501,got %d: %s", rec.Code, rec.Body.String())
	}
}
