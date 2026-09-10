// 首启引导端点单测(G-E4-R):列表 / 关闭(幂等)/ 非法 id 400 / 坏 JSON 400。
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guidesServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return hs
}

func TestGuidesDismissFlow(t *testing.T) {
	hs := guidesServer(t)
	resp, err := http.Get(hs.URL + "/api/guides")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200,得 %d", resp.StatusCode)
	}
	var out guidesResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Dismissed) != 0 {
		t.Fatalf("初始应为空: %+v", out.Dismissed)
	}
	post := func(body string) *http.Response {
		r, err := http.Post(hs.URL+"/api/guides", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r2 := post(`{"id":"desktop-im"}`)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("关闭应 200,得 %d", r2.StatusCode)
	}
	var out2 guidesResp
	if err := json.NewDecoder(r2.Body).Decode(&out2); err != nil {
		t.Fatal(err)
	}
	if len(out2.Dismissed) != 1 || out2.Dismissed[0] != "desktop-im" {
		t.Fatalf("应记录关闭: %+v", out2.Dismissed)
	}
	// 幂等 + 重启后仍在(GET 走同一偏好文件)
	r3 := post(`{"id":"desktop-im"}`)
	defer r3.Body.Close()
	var out3 guidesResp
	_ = json.NewDecoder(r3.Body).Decode(&out3)
	if len(out3.Dismissed) != 1 {
		t.Fatalf("幂等异常: %+v", out3.Dismissed)
	}
	// 非法 id / 坏 JSON
	for _, body := range []string{`{"id":""}`, `{"id":"BAD ID"}`, `{"id":"` + strings.Repeat("x", 65) + `"}`, `{`} {
		r := post(body)
		code := r.StatusCode
		r.Body.Close()
		if code != http.StatusBadRequest {
			t.Fatalf("body=%q 应 400,得 %d", body, code)
		}
	}
}
