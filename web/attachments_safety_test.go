// 附件端点的类型安全单测(安全审计 C2,2026-09-27):
// `/attachments/` 不得把可执行文档类型(html/svg/xml/js/未知二进制)在同源内联呈现 ——
// 否则等于存储型 XSS:脚本拿 UI 的 cookie 调 /api/*(读会话、注入提示词、触发工具与审批)。
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttachmentsDangerousTypesNotInline(t *testing.T) {
	s, _ := newTestServer()
	s.cfg.AttachmentsDir = t.TempDir()
	hs := httptest.NewServer(s.Handler()) // 生产护栏栈:同时验证 nosniff 等全局头
	defer hs.Close()

	// 上传 → 取预览 URL 与响应头
	upload := func(ct, name, body string) http.Header {
		t.Helper()
		buf, ctype := multipartBody(t, ct, name, body)
		resp, err := http.Post(hs.URL+"/api/attachments", ctype, buf)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var up struct {
			Attachments []AttachmentView `json:"attachments"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
			t.Fatalf("%s: 上传响应解析失败: %v", name, err)
		}
		if len(up.Attachments) != 1 {
			t.Fatalf("%s: 应返回 1 个附件,得 %+v", name, up.Attachments)
		}
		gr, err := http.Get(hs.URL + up.Attachments[0].URL)
		if err != nil {
			t.Fatal(err)
		}
		defer gr.Body.Close()
		if gr.StatusCode != http.StatusOK {
			t.Fatalf("%s: 预览应 200,得 %d", name, gr.StatusCode)
		}
		if got := gr.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: 应有 nosniff,得 %q", name, got)
		}
		return gr.Header
	}

	// ① 可执行文档类型(含**无扩展名但内容被嗅探成 HTML** 的形态):必须下载 + 禁脚本
	dangerous := []struct{ ct, name, body string }{
		{"text/html", "evil.html", "<script>fetch('/api/state')</script>"},
		{"image/svg+xml", "evil.svg", `<svg onload="alert(1)"></svg>`},
		{"text/plain", "sniff.html", "<html><script>alert(1)</script></html>"},
		{"application/json", "blob", "<html><script>alert(1)</script></html>"},
	}
	for _, c := range dangerous {
		h := upload(c.ct, c.name, c.body)
		if cd := h.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
			t.Errorf("%s: 可执行类型必须强制下载,得 Content-Disposition=%q", c.name, cd)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
			t.Errorf("%s: 可执行类型应有 'default-src none; sandbox' CSP,得 %q", c.name, csp)
		}
	}

	// ② 图片与 PDF 保持内联(前端只用 <img> 预览图片;PDF 交给浏览器原生查看器,无同源脚本面)
	inline := []struct{ ct, name, body string }{
		{"image/png", "photo.png", "\x89PNG\r\n\x1a\nxx"},
		{"application/pdf", "doc.pdf", "%PDF-1.4 x"},
	}
	for _, c := range inline {
		h := upload(c.ct, c.name, c.body)
		if cd := h.Get("Content-Disposition"); strings.Contains(cd, "attachment") {
			t.Errorf("%s: 应保持内联,得 Content-Disposition=%q", c.name, cd)
		}
		if csp := h.Get("Content-Security-Policy"); strings.Contains(csp, "sandbox") {
			t.Errorf("%s: 非可执行类型不应加 sandbox CSP,得 %q", c.name, csp)
		}
	}
}
