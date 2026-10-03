// 插件安装 HTTP 面的单测(2026-10-03)。
//
// 钉的是**安全面**而不是成功路径:未经确认必须拒(服务端没有确认服务,不能替用户点)、
// 审批档 strict 必须拒、本地目录来源的预览必须读得出真实事实。
package web

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newPluginServer(t *testing.T, mode sdk.ApprovalMode) *Server {
	t.Helper()
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	if mode != "" {
		// eff 也设成同一个档:EffectiveApproval 被实现时取的是它,只设 Mode 会测不到联动。
		s.ap = &fakeApproval{mode: mode, eff: mode}
	}
	return s
}

func postJSON(t *testing.T, s *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec
}

func TestPluginInstallRequiresConfirmed(t *testing.T) {
	s := newPluginServer(t, sdk.ApprovalSmart)
	rec := postJSON(t, s, "/api/plugins/install", installSpecReq{Spec: "/tmp/nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未经确认应 400,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "未经确认") {
		t.Fatalf("错误应说清是未确认: %s", rec.Body.String())
	}
}

func TestPluginInstallStrictApprovalRejected(t *testing.T) {
	s := newPluginServer(t, sdk.ApprovalStrict)
	// 连 preview 都不给:strict 档下"看看会装什么"也没意义 —— 一律先拒。
	rec := postJSON(t, s, "/api/plugins/install", installSpecReq{Spec: "/tmp/nope", Preview: true})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("strict 档应 403,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "严格") {
		t.Fatalf("文案应说明是审批档: %s", rec.Body.String())
	}
}

func TestPluginInstallPreviewReadsLocalFacts(t *testing.T) {
	s := newPluginServer(t, sdk.ApprovalSmart)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"),
		[]byte("id: demo\nprotocol: bridge\nbinary: tool-demo\nbuild: make -C .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, s, "/api/plugins/install", installSpecReq{Spec: dir, Preview: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("预览应 200,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo", "make -C .", dir} {
		if !strings.Contains(out.Prompt, want) {
			t.Fatalf("确认文案缺 %q:\n%s", want, out.Prompt)
		}
	}
}

func TestPluginUninstallRequiresConfirmed(t *testing.T) {
	s := newPluginServer(t, "")
	rec := postJSON(t, s, "/api/plugins/uninstall", installNameReq{ID: "demo"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("卸载未经确认应 400,实际 %d", rec.Code)
	}
}

func TestPluginTrustRequiresConfirmed(t *testing.T) {
	s := newPluginServer(t, "")
	for _, p := range []string{"/api/plugins/trust", "/api/plugins/untrust"} {
		rec := postJSON(t, s, p, installNameReq{ID: "tool-demo"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 未经确认应 400,实际 %d", p, rec.Code)
		}
	}
}

// fakeApproval 只实现 Mode 的最小审批服务。
type fakeApproval struct {
	mode sdk.ApprovalMode
	eff  sdk.ApprovalMode
}

func (f *fakeApproval) Mode() sdk.ApprovalMode          { return f.mode }
func (f *fakeApproval) SetMode(sdk.ApprovalMode)        {}
func (f *fakeApproval) EffectiveMode() sdk.ApprovalMode { return f.eff }
func (f *fakeApproval) EffectiveFrom() string           { return "" }

// TestPluginInstallListReportsTrustState 清单要报出「白名单里有没有它」与登记来源。
//
// 这是面板表格的数据源:没有它,用户只能看到"插件被拒了"却不知道为什么、
// 也不知道自己上次是什么时候从哪装的。
func TestPluginInstallListReportsTrustState(t *testing.T) {
	s := newPluginServer(t, "")
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	// 造一个「已装且已登记」的插件目录:手工放,不走完整安装(那是 install 包的职责)
	dir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tool-demo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nv1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 先登记 → 期望 trusted=true 且带审计来源
	if err := writeTrust(t, home, "tool-demo", bin); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/install", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("清单应 200,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	var out []installView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("应列出 1 条,实际 %d: %s", len(out), rec.Body.String())
	}
	v := out[0]
	if !v.Trusted || !v.Enforced || !v.Loadable {
		t.Fatalf("信任状态不对: %+v", v)
	}
	if v.Binary != "tool-demo" || v.Hash == "" {
		t.Fatalf("应报出二进制名与哈希: %+v", v)
	}
	if v.AuditSource == "" {
		t.Fatalf("应报出登记来源: %+v", v)
	}

	// 改内容 ⇒ 哈希不再匹配 ⇒ trusted 变 false(但仍列出来,面板据此显示"未登记")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nv2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	s.handler().ServeHTTP(rec2, req)
	var out2 []installView
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatal(err)
	}
	if len(out2) != 1 || out2[0].Trusted {
		t.Fatalf("哈希不符后不该再算已登记: %+v", out2)
	}
}

// writeTrust 借用 install 包登记(测试里不重复实现白名单写入)。
func writeTrust(t *testing.T, home, name, bin string) error {
	t.Helper()
	sum, err := plugintrust.HashFile(bin)
	if err != nil {
		return err
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		return err
	}
	return list.RecordWithAudit(name, sum, "trust:manual")
}
