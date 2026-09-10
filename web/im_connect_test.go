// IM 连接端点单测(E0):spec/start/submit/state + 未装配 503 + 密钥不回显 + 外链白名单 +
// 旧端点兼容 + im/connect → SSE 帧。
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubIMConn IMConnectService 桩。
type stubIMConn struct {
	spec      sdk.IMConnectSpec
	status    sdk.IMConnectStatus
	submitted map[string]string
	startErr  error
	subErr    error
}

func (s *stubIMConn) ConnectSpec() sdk.IMConnectSpec { return s.spec }
func (s *stubIMConn) StartConnect(context.Context) (sdk.IMConnectStatus, error) {
	return s.status, s.startErr
}
func (s *stubIMConn) SubmitConfig(_ context.Context, v map[string]string) (sdk.IMConnectStatus, error) {
	s.submitted = v
	return s.status, s.subErr
}
func (s *stubIMConn) ConnectStatus() sdk.IMConnectStatus { return s.status }

func imTestServer(svc sdk.IMConnectService) (*Server, *httptest.Server) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	s.imConn = svc
	return s, httptest.NewServer(s.handler())
}

func TestIMConnectUnavailable(t *testing.T) {
	_, hs := imTestServer(nil)
	defer hs.Close()
	for _, ep := range []string{"/api/im/connect/spec", "/api/im/connect/state"} {
		resp, err := http.Get(hs.URL + ep)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s 未装配应 503,得 %d", ep, resp.StatusCode)
		}
	}
}

func TestIMConnectSpecAndState(t *testing.T) {
	svc := &stubIMConn{
		spec: sdk.IMConnectSpec{Channel: "qq", Kind: sdk.IMConnectForm, Action: "保存并校验",
			LoginURL: "https://q.qq.com/qqbot/#/developer/sandbox",
			DocsURL:  "https://evil.example/x", // 白名单外 → 应被清空
			Fields: []sdk.IMConnectField{
				{Key: "app_id", Label: "AppID", Required: true, Configured: true, Mask: "…1234"},
				{Key: "app_secret", Label: "AppSecret", Secret: true, Configured: true, Mask: "已配置"},
			}},
		status: sdk.IMConnectStatus{Channel: "qq", Phase: sdk.IMPhaseIdle, Detail: "未配置"},
	}
	_, hs := imTestServer(svc)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/im/connect/spec")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var spec sdk.IMConnectSpec
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Kind != sdk.IMConnectForm || spec.LoginURL == "" {
		t.Fatalf("spec 异常: %+v", spec)
	}
	if spec.DocsURL != "" {
		t.Fatalf("白名单外链接应被清空: %q", spec.DocsURL)
	}
	// 密钥字段:只有掩码,绝不含明文值
	blob, _ := json.Marshal(spec)
	if strings.Contains(string(blob), "supersecret") {
		t.Fatalf("密钥不应出现在 spec: %s", blob)
	}
	for _, f := range spec.Fields {
		if f.Secret && f.Mask == "" {
			t.Fatalf("Secret 字段应给掩码: %+v", f)
		}
	}

	resp2, err := http.Get(hs.URL + "/api/im/connect/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var st sdk.IMConnectStatus
	if err := json.NewDecoder(resp2.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Phase != sdk.IMPhaseIdle || st.Channel != "qq" {
		t.Fatalf("状态异常: %+v", st)
	}
}

func TestIMConnectStartQR(t *testing.T) {
	svc := &stubIMConn{
		spec:   sdk.IMConnectSpec{Channel: "wechat", Kind: sdk.IMConnectQR, Action: "扫码登录"},
		status: sdk.IMConnectStatus{Channel: "wechat", Phase: sdk.IMPhaseWaitingScan, QRContent: "https://wx.example/qr"},
	}
	_, hs := imTestServer(svc)
	defer hs.Close()
	resp, err := http.Post(hs.URL+"/api/im/connect/start", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var st sdk.IMConnectStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.QRContent == "" || st.QRPNG == "" || !strings.HasPrefix(st.QRPNG, "data:image/png;base64,") {
		t.Fatalf("应补 PNG data URI: %+v", st)
	}
	// 连接进行中(StartConnect 报错)→ 409 且仍回结构化状态
	svc.startErr = fmt.Errorf("登录进行中")
	resp2, err := http.Post(hs.URL+"/api/im/connect/start", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("进行中应 409,得 %d", resp2.StatusCode)
	}
}

func TestIMConnectSubmitForm(t *testing.T) {
	svc := &stubIMConn{
		spec:   sdk.IMConnectSpec{Channel: "qq", Kind: sdk.IMConnectForm},
		status: sdk.IMConnectStatus{Channel: "qq", Phase: sdk.IMPhaseDone, Detail: "凭证已保存并校验通过", Account: "…1234"},
	}
	_, hs := imTestServer(svc)
	defer hs.Close()
	body := `{"values":{"app_id":"1020","app_secret":"__keep__","env":"sandbox"}}`
	resp, err := http.Post(hs.URL+"/api/im/connect/submit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if svc.submitted["app_secret"] != "__keep__" || svc.submitted["env"] != "sandbox" {
		t.Fatalf("表单值未送达: %+v", svc.submitted)
	}
	// 校验失败 → 422 + 结构化错误
	svc.subErr = fmt.Errorf("AppID 与 AppSecret 均不能为空")
	svc.status = sdk.IMConnectStatus{Channel: "qq", Phase: sdk.IMPhaseFailed, Error: "AppID 与 AppSecret 均不能为空"}
	resp2, err := http.Post(hs.URL+"/api/im/connect/submit", "application/json", strings.NewReader(`{"values":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("校验失败应 422,得 %d", resp2.StatusCode)
	}
	var st sdk.IMConnectStatus
	_ = json.NewDecoder(resp2.Body).Decode(&st)
	if st.Phase != sdk.IMPhaseFailed || st.Error == "" {
		t.Fatalf("应回结构化失败: %+v", st)
	}
	// qr 渠道提交表单 → 400
	svc2 := &stubIMConn{spec: sdk.IMConnectSpec{Channel: "wechat", Kind: sdk.IMConnectQR}}
	_, hs2 := imTestServer(svc2)
	defer hs2.Close()
	resp3, err := http.Post(hs2.URL+"/api/im/connect/submit", "application/json", strings.NewReader(`{"values":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("qr 渠道提交应 400,得 %d", resp3.StatusCode)
	}
}

// im/connect 事件 → SSE FrameIMConnect(事件化替代轮询)。
func TestIMConnectEventFrame(t *testing.T) {
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
	c.fire(sdk.EventIMConnect, sdk.IMConnectStatus{Channel: "wechat", Phase: sdk.IMPhaseScanned, Detail: "已扫码"})
	select {
	case f := <-ch:
		if f.Type != FrameIMConnect {
			t.Fatalf("帧类型应为 %q,得 %q", FrameIMConnect, f.Type)
		}
		st, ok := f.Payload.(sdk.IMConnectStatus)
		if !ok || st.Phase != sdk.IMPhaseScanned {
			t.Fatalf("载荷异常: %#v", f.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 FrameIMConnect")
	}
}

// imLinkAllowed 白名单判定。
func TestIMLinkAllowed(t *testing.T) {
	for _, u := range []string{"https://q.qq.com/qqbot/#/developer/sandbox", "https://bot.q.qq.com/wiki/", "https://github.com/x/y"} {
		if !imLinkAllowed(u) {
			t.Fatalf("应放行: %s", u)
		}
	}
	for _, u := range []string{"https://evil.example/x", "javascript:alert(1)", "https://q.qq.com.evil.io/x"} {
		if imLinkAllowed(u) {
			t.Fatalf("应拒绝: %s", u)
		}
	}
}
