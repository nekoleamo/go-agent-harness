package web

// /api/state 的 session_prefs 字段护栏(第一百三十四批)。
//
// 为什么要有这组测试:这个字段是**为一个真 bug 补的**。前端最初拿
// `model_from === "session"` 当「这个会话有没有自己压过全局」的判据,而
// sdk.EffectiveModel 只要没有角色声明就**恒返回** "session" —— 于是每个会话都被
// 判成独立,页签条挂满方块。补 session_prefs 就是为了让「跟随 vs 独立」有唯一口径。
//
// 所以这组测试要钉的不是「字段在不在」,而是**它与 *_from 的区别**:
// model_from 恒为 session 不代表会话压过全局 —— 这是整个修正的根据。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// withSessionPrefs 给 Server 装一个按会话 id 存偏好的 stubCS。
func withSessionPrefs(t *testing.T, s *Server, byID map[string]sdk.SessionPrefs) {
	t.Helper()
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Name: "主"}}, curKey: "demo", prefs: &prefsStub{byID: byID}}
	s.cs = cs
}

// statePrefsOf 取 /api/state?session=... 的 session_prefs(未下发时 = nil = 全跟随)。
func statePrefsOf(t *testing.T, s *Server, session string) map[string]bool {
	t.Helper()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/state?session=" + session)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	out, ok := raw["session_prefs"].(map[string]any)
	if !ok {
		return nil
	}
	got := map[string]bool{}
	for k, v := range out {
		if b, ok := v.(bool); ok {
			got[k] = b
		}
	}
	return got
}

// 跟随全局:整个字段省略(而不是下发一张全 false 的表)。
func TestStateSessionPrefsOmittedWhenAllFollowGlobal(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{})
	if got := statePrefsOf(t, s, ""); got != nil {
		t.Fatalf("全跟随时不该下发 session_prefs(前端会读成 nil = 全跟随): %v", got)
	}
}

// 会话自己设过的项才进表,且只进 true 的键。
func TestStateSessionPrefsReportsOnlySetFields(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{
		"": {Model: "own-model", Approval: string(sdk.ApprovalStrict)},
	})
	got := statePrefsOf(t, s, "")
	if !got["model"] {
		t.Fatalf("会话自设的模型应进表: %v", got)
	}
	if !got["approval"] {
		t.Fatalf("会话自设的审批应进表: %v", got)
	}
	if got["thinking"] || got["sandbox"] {
		t.Fatalf("没设过的项不该进表(只带 true 的键): %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("只应有两项,实际 %d: %v", len(got), got)
	}
}

// 另一个会话的独立设置不该串到当前会话上(多页签并行时的基本正确性)。
func TestStateSessionPrefsScopedPerSession(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{
		"other": {Model: "other-model"},
	})
	if got := statePrefsOf(t, s, ""); got != nil {
		t.Fatalf("别的会话设了不该影响到本会话: %v", got)
	}
	if got := statePrefsOf(t, s, "other"); !got["model"] {
		t.Fatalf("该会话自己的独立设置应可见: %v", got)
	}
}

// 与 model_from 的区别(**这个 bug 的根据**,改动前最该有的一条):
// 没有角色声明时 model_from 恒为 "session",但这只说明「生效值不是角色给的」,
// 与「会话有没有压过全局」无关 —— 两者必须各说各的。
func TestStateSessionPrefsIndependentOfModelFrom(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{}) // 全部跟随

	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if raw["model_from"] != "session" {
		t.Fatalf("前提变了:无角色时 model_from 应恒为 session,实际 %v", raw["model_from"])
	}
	if raw["session_prefs"] != nil {
		t.Fatalf("跟随时不该有 session_prefs —— 正是它替前端挡住了「model_from=session ⇒ 独立」的错判: %v", raw)
	}
}
