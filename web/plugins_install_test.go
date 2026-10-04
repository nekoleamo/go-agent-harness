// 插件安装 HTTP 面的单测(2026-10-03)。
//
// 钉的是**安全面**而不是成功路径:未经确认必须拒(服务端没有确认服务,不能替用户点)、
// 审批档 strict 必须拒、本地目录来源的预览必须读得出真实事实。
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
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

// TestPluginInstallListReportsSource 清单要报出「这台机器上的这个插件当初是从哪一份代码装的」。
//
// 为什么不满足于 audit_source:审计行答的是「谁把它登记进白名单」,来源账答的是
// 「它是从哪个仓库哪个 ref 哪次提交装的」—— 后者才是用户问「这插件哪来的」时要的答案。
func TestPluginInstallListReportsSource(t *testing.T) {
	s := newPluginServer(t, "")
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tool-demo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nv1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{
		Repo: "github.com/a/b", Ref: "v1.2.0", Kind: install.KindTag,
		Commit: "9f2c1ab3e5f7aa11bb22cc33dd44ee55ff6607", PluginID: "demo",
		APIVersion: "v1", Origin: install.OriginUser,
	})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/install", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out []installView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("应列出 1 条: %s", rec.Body.String())
	}
	v := out[0]
	if v.SourceRepo != "github.com/a/b" || v.SourceRef != "v1.2.0" || v.SourceKind != install.KindTag {
		t.Errorf("来源三件不符: %+v", v)
	}
	if v.SourceCommit != "9f2c1ab3e5f7" {
		t.Errorf("commit 应短显示 12 位: %q", v.SourceCommit)
	}
	if v.Origin != install.OriginUser || !v.CompatOK {
		t.Errorf("origin/compat 不符: %+v", v)
	}
}

// TestPluginInstallListReportsIncompat 不兼容的插件要在清单里被标出来(前端据此出提示条)。
func TestPluginInstallListReportsIncompat(t *testing.T) {
	s := newPluginServer(t, "")
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "plugins", "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-old"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{
		Repo: "github.com/a/old", Kind: install.KindTag, PluginID: "old", APIVersion: "v0.9",
	})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/install", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out []installView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].CompatOK {
		t.Fatalf("协议版本不认的应 compat_ok=false: %+v", out)
	}
}

// TestPluginInstallListBrokenLedger 坏账要报错,不当空账。
//
// 静默当空账的话,面板会显示「这些插件没有来源记录」,而真实情况是「来源账坏了,
// 我们不敢显示」—— 那会让用户照着一个错误结论做安全判断。
func TestPluginInstallListBrokenLedger(t *testing.T) {
	s := newPluginServer(t, "")
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(install.SourcesPath(home), []byte("sources: [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/install", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("坏账应 500,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "来源账") {
		t.Errorf("错误应指明是来源账: %s", rec.Body.String())
	}
}

// TestPluginUpdateCheck 只问不装:响应里带三态与「不自动装」的提示。
func TestPluginUpdateCheck(t *testing.T) {
	s := newPluginServer(t, "")
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	// 本地目录来源不该出现在检查列表里(没有"更新"这回事)。
	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{PluginID: "loc", Kind: install.KindLocal, Repo: "/src/loc"})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/update-check", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Checks []install.UpdateCheck `json:"checks"`
		Notice string                `json:"notice"`
		Hint   string                `json:"hint"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Checks) != 0 {
		t.Errorf("本地来源不该进检查列表: %+v", out.Checks)
	}
	if !strings.Contains(out.Hint, "不会自动安装") {
		t.Errorf("必须明说不会自动装: %s", out.Hint)
	}
}

// fakeExtPlugins 控制面替身(批二 sdk.ExternalPlugins 多了四个方法)。
type fakeExtPlugins struct {
	disabled map[string]bool
	calls    []string
	fail     bool
}

func (f *fakeExtPlugins) Reload(n string) error { f.calls = append(f.calls, "reload:"+n); return nil }
func (f *fakeExtPlugins) List() []sdk.ExternalPluginInfo {
	return []sdk.ExternalPluginInfo{{Name: "tool-demo", Path: "/x/plugins/demo/tool-demo", Loaded: !f.disabled["tool-demo"], Disabled: f.disabled["tool-demo"]}}
}
func (f *fakeExtPlugins) Enable(n string) error {
	f.calls = append(f.calls, "enable:"+n)
	if f.fail {
		return errors.New("tag 已被改写(测试)")
	}
	delete(f.disabled, n)
	return nil
}
func (f *fakeExtPlugins) Disable(n string) error {
	f.calls = append(f.calls, "disable:"+n)
	f.disabled[n] = true
	return nil
}

func withExtp(t *testing.T, f *fakeExtPlugins) *Server {
	t.Helper()
	s := newPluginServer(t, "")
	s.extp = f
	return s
}

// TestPluginDisableEnable 停用/启用两个出口(批二)。
func TestPluginDisableEnable(t *testing.T) {
	f := &fakeExtPlugins{disabled: map[string]bool{}}
	s := withExtp(t, f)
	rec := postJSON(t, s, "/api/plugins/install/disable", installNameReq{ID: "tool-demo", Confirmed: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("停用应 200,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	if !f.disabled["tool-demo"] {
		t.Error("停用应落到控制面")
	}
	rec = postJSON(t, s, "/api/plugins/install/enable", installNameReq{ID: "tool-demo", Confirmed: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("启用应 200,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	if f.disabled["tool-demo"] {
		t.Error("启用应解开停用")
	}
}

// TestPluginDisableRequiresConfirmed 启停也要二次确认(停用 = 你在决定本机常驻执行什么)。
func TestPluginDisableRequiresConfirmed(t *testing.T) {
	f := &fakeExtPlugins{disabled: map[string]bool{}}
	s := withExtp(t, f)
	for _, p := range []string{"/api/plugins/install/disable", "/api/plugins/install/enable"} {
		rec := postJSON(t, s, p, installNameReq{ID: "tool-demo"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "未经确认") {
			t.Errorf("%s 未经确认应 400,实际 %d(%s)", p, rec.Code, rec.Body.String())
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("未经确认时不应真调控制面: %v", f.calls)
	}
}

// TestPluginEnableRefusalPassesThrough 漂移拒绝要**原样透出**(里面有可执行的路子)。
func TestPluginEnableRefusalPassesThrough(t *testing.T) {
	f := &fakeExtPlugins{disabled: map[string]bool{"tool-demo": true}, fail: true}
	s := withExtp(t, f)
	rec := postJSON(t, s, "/api/plugins/install/enable", installNameReq{ID: "tool-demo", Confirmed: true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("漂移拒绝应 400,实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tag 已被改写") {
		t.Errorf("拒绝原因应原样透出: %s", rec.Body.String())
	}
}

// TestPluginDisableWithoutExtp 未装配 host-bridge 时明确报错(而不是假装成功)。
func TestPluginDisableWithoutExtp(t *testing.T) {
	s := newPluginServer(t, "")
	rec := postJSON(t, s, "/api/plugins/install/disable", installNameReq{ID: "tool-demo", Confirmed: true})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "host-bridge") {
		t.Fatalf("未装配控制面应明确报错,实际 %d(%s)", rec.Code, rec.Body.String())
	}
}

// TestPluginInstallListReportsDisabled 清单要把「已停用」单独标出来(批二三态)。
func TestPluginInstallListReportsDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-demo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTrust(t, home, "tool-demo", filepath.Join(dir, "tool-demo")); err != nil {
		t.Fatal(err)
	}
	f := &fakeExtPlugins{disabled: map[string]bool{"tool-demo": true}}
	s := withExtp(t, f)
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/install", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out []installView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Disabled || out[0].Loaded {
		t.Errorf("清单应报出已停用(而文件仍在、二进制可加载): %+v", out)
	}
	if !out[0].Loadable {
		t.Error("停用不该让二进制变成「缺失」—— 文件还在")
	}
}

// TestPluginInstallPreviewPrebuiltText 预编译的确认文案要自己讲清代价。
//
// 这一段不是"多写一句说明":产物是**下载来的**、来源由**作者声明**、gah **不验签名**
// ⇒ 装的那一刻没有独立校验。用户点确认时不知道这件事,就是在不知情下接受了一个
// 可执行二进制 —— 而这条恰好是整个模型里唯一的诚实缺口。
func TestPluginInstallPreviewPrebuiltText(t *testing.T) {
	s := newPluginServer(t, sdk.ApprovalSmart)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"),
		[]byte("id: demo\nprotocol: bridge\nbinary: tool-demo\nprebuilt:\n  "+testPlat()+": https://example.invalid/tool-demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, s, "/api/plugins/install", installSpecReq{Spec: dir, Preview: true, Prebuilt: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("预览应 200,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"https://example.invalid/tool-demo", "不会执行仓库里的任何构建命令", "不验签名", "没有独立校验"} {
		if !strings.Contains(body, want) {
			t.Errorf("预编译确认文案缺 %q:\n%s", want, body)
		}
	}
}

// TestPluginInstallPreviewWithoutPrebuilt 默认仍是源码构建那套文案。
func TestPluginInstallPreviewWithoutPrebuilt(t *testing.T) {
	s := newPluginServer(t, sdk.ApprovalSmart)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"),
		[]byte("id: demo\nprotocol: bridge\nbinary: tool-demo\nbuild: make -C .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, s, "/api/plugins/install", installSpecReq{Spec: dir, Preview: true})
	if !strings.Contains(rec.Body.String(), "make -C .") {
		t.Errorf("不带 prebuilt 时应是源码构建那套文案:\n%s", rec.Body.String())
	}
}

// TestPluginInstallPrebuiltFlagPassedThrough prebuilt 开关要真的传到内核。
func TestPluginInstallPrebuiltFlagPassedThrough(t *testing.T) {
	s := newPluginServer(t, sdk.ApprovalSmart)
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"),
		[]byte("id: demo\nprotocol: bridge\nbinary: tool-demo\nprebuilt:\n  "+testPlat()+": https://example.invalid/tool-demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 真正装:URL 不可达 ⇒ 内核的下载路径被走到(而不是悄悄退回源码构建)。
	rec := postJSON(t, s, "/api/plugins/install", installSpecReq{Spec: dir, Confirmed: true, Prebuilt: true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应 400,实际 %d(%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "example.invalid") {
		t.Errorf("错误应指向预编译产物 URL(证明走了下载路而非构建路): %s", rec.Body.String())
	}
}

func testPlat() string { return runtime.GOOS + "/" + runtime.GOARCH }

// TestUIScanFiltersDisabled 停用的 UI 插件**不下发**(前端根本不加载)。
//
// 停用要有一个效果,否则它只是个记号。判据选「不下发」而不是「下发但前端藏起来」:
// UI 插件与宿主同源同权限,「已经下载到浏览器内存里再藏」等于没停。
func TestUIScanFiltersDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	uiRoot := filepath.Join(home, "ui-plugins")
	s := newPluginServer(t, "")
	s.cfg.UIPluginsDir = uiRoot
	dir := filepath.Join(uiRoot, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"id":"demo","version":"1","slots":[{"name":"v1:extra-panel","priority":1,"module":"./dist/p.js"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "p.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/ui-plugins", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out []UIPlugin
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// 清单不存在 ⇒ 未启用 ⇒ 放行(这是批四之前的老行为;闸由 ui-web-app 在 Start 建)
	if len(out) != 1 {
		t.Fatalf("闸不存在时应照旧放行(boot 还没建闸): %s", rec.Body.String())
	}
	// 停用 ⇒ 不下发
	prefs.SetUIDisabled("demo", true)
	rec2 := httptest.NewRecorder()
	s.handler().ServeHTTP(rec2, req)
	var out2 []UIPlugin
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatal(err)
	}
	if len(out2) != 0 {
		t.Errorf("停用的 UI 插件不应下发: %s", rec2.Body.String())
	}
	// 启用 ⇒ 回来
	prefs.SetUIDisabled("demo", false)
	rec3 := httptest.NewRecorder()
	s.handler().ServeHTTP(rec3, req)
	var out3 []UIPlugin
	if err := json.Unmarshal(rec3.Body.Bytes(), &out3); err != nil {
		t.Fatal(err)
	}
	if len(out3) != 1 {
		t.Errorf("启用后应回来: %s", rec3.Body.String())
	}
}

// TestUIToggleRequiresConfirmed 启停也要二次确认(UI 插件与宿主同源同权限)。
func TestUIToggleRequiresConfirmed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	s := newPluginServer(t, "")
	for _, p := range []string{"/api/ui-plugins/disable", "/api/ui-plugins/enable"} {
		rec := postJSON(t, s, p, installNameReq{ID: "demo"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "未经确认") {
			t.Errorf("%s 未经确认应 400,实际 %d(%s)", p, rec.Code, rec.Body.String())
		}
		if prefs.IsUIDisabled("demo") {
			t.Errorf("%s 未经确认时不应真停用", p)
		}
	}
	rec := postJSON(t, s, "/api/ui-plugins/disable", installNameReq{ID: "demo", Confirmed: true})
	if rec.Code != http.StatusOK || !prefs.IsUIDisabled("demo") {
		t.Errorf("确认后应停用: %d %s", rec.Code, rec.Body.String())
	}
	rec = postJSON(t, s, "/api/ui-plugins/enable", installNameReq{ID: "demo", Confirmed: true})
	if rec.Code != http.StatusOK || prefs.IsUIDisabled("demo") {
		t.Errorf("确认后应启用: %d %s", rec.Code, rec.Body.String())
	}
}

// TestPluginInstallListOriginOfficial 面板要能把「随 gah 附带的」与「你安装的」分开。
func TestPluginInstallListOriginOfficial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	// 官方件的落位布局是 plugins/<二进制名>/<二进制名>(internal/embed.pluginDst),
	// 所以它的目录名 == 二进制名;用户自己装的走安装器布局 plugins/<id>/tool-<id>。
	// 二进制必须带 tool- 前缀,否则 host-bridge 根本不会加载它。
	for _, c := range []struct{ id, bin string }{{"tool-basic", "tool-basic"}, {"demo", "tool-demo"}} {
		d := filepath.Join(home, "plugins", c.id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, c.bin), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := install.MarkOfficial(home, []string{"tool-basic"}); err != nil {
		t.Fatal(err)
	}
	s := newPluginServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/install", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out []installView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range out {
		got[v.Binary] = v.Origin
	}
	if got["tool-basic"] != install.OriginOfficial {
		t.Errorf("官方件应标 official: %+v", got)
	}
	if got["tool-demo"] != install.OriginUser {
		t.Errorf("没标的一律算「你安装的」(含手工放置): %+v", got)
	}
}

// TestUIPluginStateEndpoint 状态面要给出「闸是否强制」—— 闸挡住时 /api/ui-plugins 是空数组。
func TestUIPluginStateEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	uiRoot := filepath.Join(home, "ui-plugins")
	s := newPluginServer(t, "")
	s.cfg.UIPluginsDir = uiRoot

	get := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/ui-plugins/state", nil)
		rec := httptest.NewRecorder()
		s.handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态面应 200,实际 %d(%s)", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// 闸不存在 ⇒ 不强制(boot 还没建闸;老行为)
	if got := get()["enforced"]; got != false {
		t.Errorf("闸不存在时应为 false,得 %v", got)
	}
	if _, err := install.EnsureUIList(uiRoot); err != nil {
		t.Fatal(err)
	}
	if got := get()["enforced"]; got != true {
		t.Errorf("建闸后应为 true,得 %v", got)
	}
	prefs.SetUIDisabled("demo", true)
	if d, _ := get()["disabled"].([]any); len(d) != 1 || d[0] != "demo" {
		t.Errorf("停用项要能被面板看见: %v", get()["disabled"])
	}
}
