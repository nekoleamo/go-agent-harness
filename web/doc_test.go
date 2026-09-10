// 文档端点单测(D1):状态码映射 / Range / MIME 白名单 / CSP / 渲染 / 未装配 503 / 鉴权。
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubDoc 文档服务桩(仅实现被测通路;未覆盖方法返回零值)。
type stubDoc struct {
	previewErr error
	view       *sdk.DocView
	raw        []byte
	rawMime    string
	assetMime  string
	assetData  []byte
	tree       *sdk.DocTree
	rendered   *sdk.DocView
	lastReq    sdk.DocRequest
	lastDepth  int
	lastText   string
}

func (d *stubDoc) Detect(context.Context, sdk.DocRequest) (sdk.DocFormat, error) {
	return sdk.DocFormatMarkdown, nil
}

func (d *stubDoc) Preview(_ context.Context, req sdk.DocRequest) (*sdk.DocView, error) {
	d.lastReq = req
	if d.previewErr != nil {
		return nil, d.previewErr
	}
	return d.view, nil
}

func (d *stubDoc) Text(context.Context, sdk.DocRequest) (*sdk.DocText, error) {
	return &sdk.DocText{}, nil
}

func (d *stubDoc) Asset(_ context.Context, _ sdk.DocRequest, _ string) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(d.assetData)), d.assetMime, nil
}

func (d *stubDoc) Raw(context.Context, sdk.DocRequest) (io.ReadSeekCloser, string, error) {
	return nopSeekCloser{bytes.NewReader(d.raw)}, d.rawMime, nil
}

func (d *stubDoc) List(_ context.Context, req sdk.DocRequest, depth int) (*sdk.DocTree, error) {
	d.lastReq = req
	d.lastDepth = depth
	if d.tree == nil {
		return &sdk.DocTree{}, nil
	}
	return d.tree, nil
}

func (d *stubDoc) Render(_ context.Context, text string, _ int) (*sdk.DocView, error) {
	d.lastText = text
	if d.rendered != nil {
		return d.rendered, nil
	}
	return &sdk.DocView{Format: sdk.DocFormatMarkdown}, nil
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func docTestServer(doc sdk.DocService) (*Server, *httptest.Server) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	s.doc = doc
	if doc == nil {
		s.doc = nil
	}
	return s, httptest.NewServer(s.handler())
}

func TestDocUnavailableReturns503(t *testing.T) {
	s, hs := docTestServer(nil)
	defer hs.Close()
	_ = s
	resp, err := http.Get(hs.URL + "/api/doc/preview?path=a.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
	// 懒解析路径:Ctx 未注入 doc 时同样 503(已由上面覆盖);注入后经 handler 生效
}

func TestDocPreviewAndRawURL(t *testing.T) {
	d := &stubDoc{view: &sdk.DocView{Name: "a.md", Format: sdk.DocFormatMarkdown, Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockHeading, Level: 1, Text: "T"}}}}
	_, hs := docTestServer(d)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/preview?path=sub/a.md&max=1024")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var v sdk.DocView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Blocks[0].Text != "T" || !strings.HasPrefix(v.RawURL, "/api/doc/raw?path=") {
		t.Fatalf("视图异常: %+v", v)
	}
	if d.lastReq.Path != "sub/a.md" || d.lastReq.MaxBytes != 1024 || !d.lastReq.Strict {
		t.Fatalf("请求参数异常: %+v(Web 端必须 strict)", d.lastReq)
	}
}

func TestDocPreviewErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmtErr(sdk.ErrDocDenied), http.StatusForbidden},
		{fmtErr(sdk.ErrDocNotFound), http.StatusNotFound},
		{fmtErr(sdk.ErrDocTooLarge), http.StatusRequestEntityTooLarge},
		{fmtErr(sdk.ErrDocParse), http.StatusUnprocessableEntity},
		{fmtErr(sdk.ErrDocUnsupported), http.StatusUnsupportedMediaType},
	}
	for _, c := range cases {
		d := &stubDoc{previewErr: c.err}
		_, hs := docTestServer(d)
		resp, err := http.Get(hs.URL + "/api/doc/preview?path=x")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		hs.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("%v 应映射 %d,得 %d(%s)", c.err, c.want, resp.StatusCode, body)
		}
	}
}

func TestDocRawRangeAndHeaders(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefghij"), 10) // 100 字节
	d := &stubDoc{raw: payload, rawMime: "application/pdf"}
	_, hs := docTestServer(d)
	defer hs.Close()

	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/doc/raw?path=a.pdf", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range 应 206,得 %d", resp.StatusCode)
	}
	if string(body) != "abcdefghij" {
		t.Fatalf("Range 内容异常: %q", body)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("缺少 nosniff")
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("PDF 应 inline(浏览器原生查看器): %q", resp.Header.Get("Content-Disposition"))
	}
}

// text/html 永不 inline(防同源 XSS 注入面);dl=1 一律 attachment。
func TestDocRawHTMLNeverInline(t *testing.T) {
	d := &stubDoc{raw: []byte("<h1>x</h1>"), rawMime: "text/html; charset=utf-8"}
	_, hs := docTestServer(d)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/raw?path=a.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("HTML 应 attachment,得 %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestDocRawDownloadFlag(t *testing.T) {
	d := &stubDoc{raw: []byte("x"), rawMime: "application/pdf"}
	_, hs := docTestServer(d)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/raw?path=a.pdf&dl=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("dl=1 应 attachment,得 %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestDocAssetMimeWhitelist(t *testing.T) {
	d := &stubDoc{assetData: []byte("img"), assetMime: "image/png"}
	_, hs := docTestServer(d)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/asset?path=a.docx&id=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("图片资产应 200/image-png,得 %d/%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") == "" {
		t.Fatal("资产应带 Cache-Control")
	}

	d2 := &stubDoc{assetData: []byte("%PDF"), assetMime: "application/pdf"}
	_, hs2 := docTestServer(d2)
	defer hs2.Close()
	resp2, err := http.Get(hs2.URL + "/api/doc/asset?path=a.docx&id=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("非图片资产应 415,得 %d", resp2.StatusCode)
	}
}

func TestDocTreeEndpoint(t *testing.T) {
	d := &stubDoc{tree: &sdk.DocTree{Name: "ws", Entries: []sdk.DocEntry{{Name: "a.md", Path: "a.md", Previewable: true}}}}
	_, hs := docTestServer(d)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/tree?path=.&depth=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var tree sdk.DocTree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != "a.md" {
		t.Fatalf("树内容异常: %+v", tree)
	}
	if d.lastDepth != 3 {
		t.Fatalf("depth 未传递: %d", d.lastDepth)
	}
}

func TestDocHTMLEndpointCSP(t *testing.T) {
	d := &stubDoc{raw: []byte("<h1>hi</h1>"), rawMime: "text/html"}
	_, hs := docTestServer(d)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/html?path=a.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<h1>hi</h1>") {
		t.Fatalf("HTML 预览异常: %d %q", resp.StatusCode, body)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("HTML 预览必须禁一切外联/脚本,得 CSP=%q", csp)
	}
}

func TestDocRenderEndpoint(t *testing.T) {
	d := &stubDoc{rendered: &sdk.DocView{Format: sdk.DocFormatMarkdown, Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockCode, Lang: "go", Text: "x"}}}}
	_, hs := docTestServer(d)
	defer hs.Close()
	payload, _ := json.Marshal(map[string]string{"text": "# T\n\n```go\nx\n```"})
	resp, err := http.Post(hs.URL+"/api/doc/render", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var v sdk.DocView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Blocks) != 1 || v.Blocks[0].Kind != sdk.DocBlockCode {
		t.Fatalf("渲染结果异常: %+v", v.Blocks)
	}
	if !strings.Contains(d.lastText, "# T") {
		t.Fatalf("文本未传达到服务: %q", d.lastText)
	}
	// 空文本 → 空视图(200)
	resp2, err := http.Post(hs.URL+"/api/doc/render", "application/json", strings.NewReader(`{"text":"  "}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("空文本应 200,得 %d", resp2.StatusCode)
	}
}

func TestDocRenderBodyLimit(t *testing.T) {
	d := &stubDoc{}
	_, hs := docTestServer(d)
	defer hs.Close()
	big := strings.Repeat("x", docMaxRenderBytes+1024)
	resp, err := http.Post(hs.URL+"/api/doc/render", "application/json", strings.NewReader(`{"text":"`+big+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应 413,得 %d", resp.StatusCode)
	}
}

func TestDocAuthRequired(t *testing.T) {
	hub := NewHub()
	s := New(Config{AuthToken: "secret"}, hub, NewConfirm(hub), slog.Default())
	s.doc = &stubDoc{view: &sdk.DocView{}}
	hs := httptest.NewServer(s.authMiddleware(s.handler()))
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/doc/preview?path=a.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("缺 token 应 401,得 %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/doc/preview?path=a.md&token=secret", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("带 token 应 200,得 %d", resp2.StatusCode)
	}
}

func TestDocMissingPathParam(t *testing.T) {
	_, hs := docTestServer(&stubDoc{view: &sdk.DocView{}})
	defer hs.Close()
	for _, ep := range []string{"/api/doc/preview", "/api/doc/raw", "/api/doc/html"} {
		resp, err := http.Get(hs.URL + ep)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 缺 path 应 400,得 %d", ep, resp.StatusCode)
		}
	}
}

// fmtErr 包装哨兵错误(模拟真实错误文本)。
func fmtErr(sentinel error) error { return fmt.Errorf("docview: %w", sentinel) }

// doc/open 事件 → SSE FrameDoc(D5 三端联动:模型 doc_open / `/preview` 命令触发)。
func TestDocOpenBroadcastsFrame(t *testing.T) {
	hub := NewHub()
	c := newTestCtx()
	log := &memLog{}
	dis, err := hub.Subscribe(c, log)
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	ch, release := hub.Stream()
	defer release()

	c.fire(sdk.EventDocOpen, sdk.DocOpenEvent{Path: "docs/a.md", Page: 2})
	select {
	case f := <-ch:
		if f.Type != FrameDoc {
			t.Fatalf("帧类型应为 %q,得 %q", FrameDoc, f.Type)
		}
		ev, ok := f.Payload.(sdk.DocOpenEvent)
		if !ok || ev.Path != "docs/a.md" || ev.Page != 2 {
			t.Fatalf("载荷异常: %#v", f.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 FrameDoc 帧")
	}
}
