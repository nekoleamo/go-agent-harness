// 档位联动开关的 Web 面(R10 ②-2):/api/state 下发当前开关(设置面板要渲染勾选态),
// /api/control 接受切换并落偏好;沙箱不支持该能力时显式 400(不静默忽略请求)。
package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubSBSync 声明档 + 活联动 + 开关(等价 policy-guard:open → full-access)。
type stubSBSync struct {
	stubSB
	sync     bool
	approval sdk.ApprovalMode
}

func (s *stubSBSync) SyncEnabled() bool      { return s.sync }
func (s *stubSBSync) SetSyncEnabled(on bool) { s.sync = on }
func (s *stubSBSync) EffectiveMode() sdk.SandboxMode {
	if !s.sync {
		return s.mode
	}
	if s.approval == sdk.ApprovalOpen {
		return sdk.SandboxFullAccess
	}
	return s.mode
}

var (
	_ sdk.SandboxSync      = (*stubSBSync)(nil)
	_ sdk.EffectiveSandbox = (*stubSBSync)(nil)
)

// syncTestServer 装了联动开关的 server(approval=open,声明 workspace-write)。
func syncTestServer(t *testing.T) (*Server, *stubSBSync) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	s, _ := newTestServer()
	sb := &stubSBSync{stubSB: stubSB{mode: sdk.SandboxWorkspace}, sync: true, approval: sdk.ApprovalOpen}
	s.sb = sb
	return s, sb
}

// TestStateReportsSandboxSync /api/state 带开关状态;未实现能力的沙箱省略该字段。
func TestStateReportsSandboxSync(t *testing.T) {
	s, _ := syncTestServer(t)
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	var v StateView
	resp, err := http.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.SandboxSync == nil || !*v.SandboxSync {
		t.Fatalf("state 应下发联动开关 on: %+v", v.SandboxSync)
	}
	if v.SandboxDerived && v.SandboxEffective != "full-access" {
		t.Fatalf("联动开启时有效档应为 full-access: %q", v.SandboxEffective)
	}
	// 未实现能力的沙箱(默认 stubSB):字段省略,前端不显示该项
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	var v2 StateView
	resp2, err := http.Get(hs2.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if err := json.NewDecoder(resp2.Body).Decode(&v2); err != nil {
		t.Fatal(err)
	}
	if v2.SandboxSync != nil {
		t.Fatalf("无该能力时不该出现 sandbox_sync: %v", *v2.SandboxSync)
	}
}

// TestControlSandboxSync 切换开关:即时生效 + 落偏好 + 有效档随之复位。
func TestControlSandboxSync(t *testing.T) {
	s, sb := syncTestServer(t)
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post(hs.URL+"/api/control", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post(`{"sandbox_sync":false}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("切换应 200,got %d", resp.StatusCode)
	}
	if sb.SyncEnabled() {
		t.Fatal("开关应被关掉")
	}
	if got := sb.EffectiveMode(); got != sdk.SandboxWorkspace {
		t.Fatalf("关掉后有效档应回声明档,got %s", got)
	}
	if v := prefs.Load().SandboxSync; v == nil || *v {
		t.Fatalf("偏好应记为 false,got %v", v)
	}
	// 显式 false 与"没给"必须区分:空 body 不该把开关改回去(指针语义)
	resp = post(`{}`)
	resp.Body.Close()
	if sb.SyncEnabled() {
		t.Fatal("空请求体不得改动开关(需区分没给与显式 false)")
	}
	resp = post(`{"sandbox_sync":true}`)
	resp.Body.Close()
	if !sb.SyncEnabled() {
		t.Fatal("显式 true 应重新开启")
	}
	// 沙箱不支持能力:显式 400(不静默成功)
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err := http.Post(hs2.URL+"/api/control", "application/json", bytes.NewBufferString(`{"sandbox_sync":false}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不支持能力应 400,got %d", resp.StatusCode)
	}
}
