// 群维度授权端点单测(G-E5-2):GET 列表 / POST 授权与撤销 / 422 / 503 / 参数校验。
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeGroups 群授权能力替身。
type fakeGroups struct {
	entries []sdk.IMGroupEntry
	setErr  error
	calls   []string
}

func (f *fakeGroups) Groups() []sdk.IMGroupEntry { return f.entries }
func (f *fakeGroups) SetGroupAccess(chatID string, allow bool) error {
	f.calls = append(f.calls, chatID)
	if f.setErr != nil {
		return f.setErr
	}
	return nil
}

// fakeChannelWithGroups 同时实现 IMChannelService 与 IMGroupAccessService。
type fakeChannelWithGroups struct {
	*fakeGroups
}

func (f fakeChannelWithGroups) Status() []sdk.IMChannelStatus {
	return []sdk.IMChannelStatus{{Channel: "qq", State: "online"}}
}

func groupsServer(t *testing.T, svc any) (*httptest.Server, *fakeGroups) {
	t.Helper()
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	if svc != nil {
		if err := s.Inject(newTestCtx()); err != nil {
			t.Fatal(err)
		}
		s.imc = svc.(sdk.IMChannelService)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	var fg *fakeGroups
	if svc != nil {
		fg = svc.(fakeChannelWithGroups).fakeGroups
	}
	return hs, fg
}

func TestIMGroupsList(t *testing.T) {
	seen := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	fg := &fakeGroups{entries: []sdk.IMGroupEntry{
		{Channel: "qq", ChatID: "GRP-A", Authorized: true, LastSeen: seen, Source: "both"},
		{Channel: "qq", ChatID: "GRP-B", Source: "seen"},
	}}
	hs, _ := groupsServer(t, fakeChannelWithGroups{fg})
	resp, err := http.Get(hs.URL + "/api/im/groups")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200,得 %d", resp.StatusCode)
	}
	var out groupsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 2 || out.Groups[0].ChatID != "GRP-A" || !out.Groups[0].Authorized {
		t.Fatalf("列表异常: %+v", out.Groups)
	}
}

func TestIMGroupsSet(t *testing.T) {
	fg := &fakeGroups{}
	hs, _ := groupsServer(t, fakeChannelWithGroups{fg})
	post := func(body string) *http.Response {
		resp, err := http.Post(hs.URL+"/api/im/groups", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post(`{"chat_id":"GRP-X","allow":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("授权应 200,得 %d", resp.StatusCode)
	}
	resp2 := post(`{"chat_id":"GRP-X","allow":false}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("撤销应 200,得 %d", resp2.StatusCode)
	}
	if len(fg.calls) != 2 || fg.calls[0] != "GRP-X" {
		t.Fatalf("应转发两次: %+v", fg.calls)
	}
	// 缺 chat_id → 400
	resp3 := post(`{"allow":true}`)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 chat_id 应 400,得 %d", resp3.StatusCode)
	}
	// 非法 JSON → 400
	resp4 := post(`{`)
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400,得 %d", resp4.StatusCode)
	}
	// 撤销未知群(服务报错)→ 422
	fg.setErr = errGroupUnknown
	resp5 := post(`{"chat_id":"GRP-Y","allow":false}`)
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("未知群撤销应 422,得 %d", resp5.StatusCode)
	}
}

func TestIMGroupsUnavailable(t *testing.T) {
	hs, _ := groupsServer(t, nil)
	resp, err := http.Get(hs.URL + "/api/im/groups")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
	resp2, err := http.Post(hs.URL+"/api/im/groups", "application/json", strings.NewReader(`{"chat_id":"x","allow":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配 POST 应 503,得 %d", resp2.StatusCode)
	}
}

var errGroupUnknown = &groupErr{"im: 该群未授权: GRP-Y"}

type groupErr struct{ msg string }

func (e *groupErr) Error() string { return e.msg }
