// 角色包端点单测(第九十三批):导出下载 / 导入(含覆盖与拒绝)。
//
// 与 roles_test.go 同一套路:装配**真实**角色链走懒解析,不写服务替身 ——
// 这里要证的正是"导出的包换台机器导得回来",替身会与真实落盘行为漂移。
package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// packBody 组一个 multipart 上传体(字段名固定 "file")。
func packBody(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "gah-role-x.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// doPack 发一次 multipart 请求。
func doPack(t *testing.T, s *Server, path string, data []byte) (int, string) {
	t.Helper()
	body, ct := packBody(t, data)
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// makeShareRole 造一个"该有的都有"的角色:定义 + 规则 + 两个私有技能。
func makeShareRole(t *testing.T, s *Server) {
	t.Helper()
	if code, body := do(t, s, http.MethodPost, "/api/roles",
		`{"id":"finance","name":"财务","identity":"你是财务","model":"role-model","approval":"strict"}`); code != 200 {
		t.Fatalf("建角色 = %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPut, "/api/roles/finance/agents", `{"agents":"财务规则正文"}`); code != 200 {
		t.Fatalf("写规则 = %d %s", code, body)
	}
	for _, name := range []string{"tax", "audit"} {
		if code, body := do(t, s, http.MethodPost, "/api/skills",
			`{"role":"finance","name":"`+name+`","description":"`+name+`","body":"正文"}`); code != 200 {
			t.Fatalf("建私有技能 = %d %s", code, body)
		}
	}
}

// TestRolePackRoundTrip 下载 → 删角色 → 上传导回:定义/规则/私有技能全回来。
func TestRolePackRoundTrip(t *testing.T) {
	s, _ := rolesServer(t, true)
	makeShareRole(t, s)

	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/rolepack/finance", nil))
	if w.Code != 200 {
		t.Fatalf("下载角色包 = %d %s", w.Code, w.Body.String())
	}
	pack := w.Body.Bytes()
	if !bytes.HasPrefix(pack, []byte("PK")) {
		t.Fatalf("不是 zip:%q", pack[:8])
	}
	// 下载头:文件名与格式(前端 <a download> 直接吃)
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "gah-role-finance.zip") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %q", ct)
	}
	// 删掉原角色(模拟"另一台机器上导入")
	if code, body := do(t, s, http.MethodDelete, "/api/roles/finance", ""); code != 200 {
		t.Fatalf("删角色 = %d %s", code, body)
	}
	code, body := doPack(t, s, "/api/rolepack", pack)
	if code != 200 || !strings.Contains(body, `"id":"finance"`) {
		t.Fatalf("导入 = %d %s", code, body)
	}
	// 角色索引已重载:面板能立刻看到,且内容完整
	code, body = do(t, s, http.MethodGet, "/api/roles/finance", "")
	if code != 200 {
		t.Fatalf("导入后读角色 = %d %s", code, body)
	}
	for _, want := range []string{"财务", "role-model", `"approval":"strict"`, "财务规则正文", "tax", "audit"} {
		if !strings.Contains(body, want) {
			t.Fatalf("导入后角色缺 %q:\n%s", want, body)
		}
	}
	if code, body := do(t, s, http.MethodGet, "/api/roles", ""); code != 200 || !strings.Contains(body, "finance") {
		t.Fatalf("列表里应有新角色 = %d %s", code, body)
	}
}

// TestRolePackOverwrite 同名目标:默认拒绝;overwrite=1 覆盖且旧份进回收站;as 另起 ID。
func TestRolePackOverwrite(t *testing.T) {
	s, _ := rolesServer(t, true)
	makeShareRole(t, s)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/rolepack/finance", nil))
	pack := w.Body.Bytes()

	if code, body := doPack(t, s, "/api/rolepack", pack); code != 400 || !strings.Contains(body, "已存在") {
		t.Fatalf("同名目标应拒绝 = %d %s", code, body)
	}
	// as:并存一份
	code, body := doPack(t, s, "/api/rolepack?as=finance-copy", pack)
	if code != 200 || !strings.Contains(body, `"id":"finance-copy"`) {
		t.Fatalf("导入为 = %d %s", code, body)
	}
	// overwrite:旧的进回收站
	code, body = doPack(t, s, "/api/rolepack?overwrite=1", pack)
	if code != 200 || !strings.Contains(body, `"replaced":true`) || !strings.Contains(body, "backup_name") {
		t.Fatalf("覆盖 = %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodGet, "/api/trash", ""); code != 200 || !strings.Contains(body, "finance") {
		t.Fatalf("回收站应有被覆盖那份 = %d %s", code, body)
	}
}

// TestRolePackUnavailableAndBadRequests 未装配 → 503;坏请求 → 400/404(错误文案给人看)。
func TestRolePackUnavailableAndBadRequests(t *testing.T) {
	s, _ := rolesServer(t, false)
	if code, _ := do(t, s, http.MethodGet, "/api/rolepack/any", ""); code != 503 {
		t.Fatalf("未装配应 503,got %d", code)
	}
	if code, _ := doPack(t, s, "/api/rolepack", []byte("x")); code != 503 {
		t.Fatalf("未装配导入应 503,got %d", code)
	}

	s2, _ := rolesServer(t, true)
	makeShareRole(t, s2)
	if code, body := do(t, s2, http.MethodGet, "/api/rolepack/ghost", ""); code != 404 || !strings.Contains(body, "角色不存在") {
		t.Fatalf("不存在的角色 = %d %s", code, body)
	}
	// 非 multipart
	if code, body := do(t, s2, http.MethodPost, "/api/rolepack", `{"a":1}`); code != 400 || !strings.Contains(body, "multipart") {
		t.Fatalf("非 multipart = %d %s", code, body)
	}
	// multipart 但没有 file 字段
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("other", "x")
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/rolepack", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s2.handler().ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "没有 file 字段") {
		t.Fatalf("缺 file 字段 = %d %s", w.Code, w.Body.String())
	}
	// 垃圾内容(不是 zip)→ 400,且不该留下任何角色
	if code, body := doPack(t, s2, "/api/rolepack?as=junk", []byte("not a zip")); code != 400 || !strings.Contains(body, "不是有效的角色包") {
		t.Fatalf("垃圾包 = %d %s", code, body)
	}
	if code, _ := do(t, s2, http.MethodGet, "/api/roles/junk", ""); code == 200 {
		t.Fatal("失败的导入不该落地角色")
	}
	// 非法 as(坏 ID)→ 400
	wp := httptest.NewRecorder()
	s2.handler().ServeHTTP(wp, httptest.NewRequest(http.MethodGet, "/api/rolepack/finance", nil))
	if code, body := doPack(t, s2, "/api/rolepack?as=Bad%20ID", wp.Body.Bytes()); code != 400 || !strings.Contains(body, "角色 ID") {
		t.Fatalf("非法 as = %d %s", code, body)
	}
}
