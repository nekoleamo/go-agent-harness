// 面板扫码登录单测:QR PNG data URI 生成 + 端点(未装配 503 / 装配发起与状态轮询)。
package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubIMLogin 面板登录 stub。
type stubIMLogin struct {
	state sdk.IMLoginState
}

func (s *stubIMLogin) StartLogin(context.Context) (sdk.IMLoginQR, error) {
	return sdk.IMLoginQR{Channel: "wechat", Content: "https://ilink.example/login?ticket=abc", ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
}
func (s *stubIMLogin) LoginState() sdk.IMLoginState { return s.state }

// TestQRPNGDataURI 二维码 PNG 生成:data URI 前缀 + PNG 魔数。
func TestQRPNGDataURI(t *testing.T) {
	uri, err := qrPNGDataURI("https://ilink.example/login?ticket=xyz", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("应为 PNG data URI: %.40s", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 8 || string(raw[1:4]) != "PNG" {
		t.Fatalf("PNG 魔数不符: % x", raw[:8])
	}
}

// TestIMLoginEndpoints 未装配 503;装配后发起返回二维码与 PNG,状态可轮询。
func TestIMLoginEndpoints(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	resp, err := http.Post(hs.URL+"/api/im/login", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
	hs.Close()

	stub := &stubIMLogin{state: sdk.IMLoginState{Phase: "pending", Detail: "等待扫码"}}
	s.imLogin = stub
	hs2 := httptest.NewServer(s.handler())
	defer hs2.Close()
	resp2, err := http.Post(hs2.URL+"/api/im/login", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != 200 || out["channel"] != "wechat" {
		t.Fatalf("发起登录响应不符: %v %v", resp2.StatusCode, out)
	}
	if png, _ := out["png"].(string); !strings.HasPrefix(png, "data:image/png;base64,") {
		t.Fatalf("应含二维码 PNG: %.40v", out["png"])
	}
	// 状态轮询
	resp3, err := http.Get(hs2.URL + "/api/im/login/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var st map[string]any
	if err := json.NewDecoder(resp3.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st["phase"] != "pending" || st["detail"] != "等待扫码" {
		t.Fatalf("状态不符: %v", st)
	}
}
