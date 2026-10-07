package web

// 非会话帧的**会话归属**测试(confirm / question / command)。
//
// 为什么要有归属:一次请求/一次回合之后,推给浏览器的并不只有会话事件 ——
// 还有「这个操作要你点头」的审批弹层、要你选的结构化提问、斜杠命令的输出。
// 多会话并行(桌面多窗口已是多会话,页签更是)时,这些帧若不带归属,就会弹在**别的**
// 会话视图上;更糟的是用户能在那里按"同意",替另一个会话做了决定。
//
// 因此:回合入口注入会话 id(sdk.WithSessionContext),web 的呈现服务把它填进帧的 Session,
// 前端据此决定这个待办归谁(消费在页签那一批)。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// waitFrame 收第一帧指定类型(跳过 baseline 等噪声)。
func waitFrame(t *testing.T, ch <-chan Frame, typ string) Frame {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case f := <-ch:
			if f.Type == typ {
				return f
			}
		case <-deadline:
			t.Fatalf("没等到 %s 帧", typ)
		}
	}
}

func TestConfirmFrameCarriesSession(t *testing.T) {
	s, _ := newTestServer()
	hub := NewHub()
	cs := NewConfirm(hub)
	_ = s
	frames, release := hub.Stream("") // want 空 = 全量
	defer release()

	ctx := sdk.WithSessionContext(context.Background(), "sA")
	go func() { _, _ = cs.Confirm(ctx, "危险操作?") }()

	f := waitFrame(t, frames, FrameConfirm)
	if f.Session != "sA" {
		t.Fatalf("审批帧应带归属会话 sA,得 %q", f.Session)
	}
	// 未注入会话(旧调用方/单测)⇒ 空 = 主会话,不是报错
	ctx2 := context.Background()
	go func() { _, _ = cs.Confirm(ctx2, "别的?") }()
	f2 := waitFrame(t, frames, FrameConfirm)
	if f2.Session != "" {
		t.Fatalf("未注入会话时应为空(主会话),得 %q", f2.Session)
	}
}

// TestPendingFramesKeepSession 断线重放的弹层也必须带归属:否则刷新后补推的弹层
// 会被当前视图认领 —— 那正是"别的会话的审批弹在我这儿"的另一种形态。
func TestPendingFramesKeepSession(t *testing.T) {
	s, _ := newTestServer()
	ctx := sdk.WithSessionContext(context.Background(), "sB")
	go func() { _, _ = s.confirm.Confirm(ctx, "待决?") }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(s.confirm.Pending()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(s.confirm.Pending()) != 1 {
		t.Fatalf("应有 1 条未决,得 %d", len(s.confirm.Pending()))
	}
	frames := s.confirm.PendingFrames()
	if len(frames) != 1 || frames[0].Session != "sB" || frames[0].Type != FrameConfirm {
		t.Fatalf("补推帧应保留归属 sB,得 %#v", frames)
	}
	// 载荷不能被塞进归属信息(ConfirmRequest 是前端载荷,Session 是帧的投递元信息)。
	if req, ok := frames[0].Payload.(*ConfirmRequest); !ok || req.Prompt != "待决?" {
		t.Fatalf("补推载荷不对:%#v", frames[0].Payload)
	}
}

func TestQuestionFrameCarriesSession(t *testing.T) {
	hub := NewHub()
	qs := NewQuestionService(hub)
	frames, release := hub.Stream("")
	defer release()

	ctx := sdk.WithSessionContext(context.Background(), "sQ")
	_, cancel, err := qs.PresentQuestion(ctx, sdk.Question{ID: "q1", Prompt: "选哪个?"})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	f := waitFrame(t, frames, FrameQuestion)
	if f.Session != "sQ" {
		t.Fatalf("提问帧应带归属会话 sQ,得 %q", f.Session)
	}
}

func TestCommandFrameCarriesSession(t *testing.T) {
	s, _ := newTestServer()
	cmds := newStubCmds()
	_, _ = cmds.Register(sdk.CommandSpec{
		Name: "echo", Usage: "/echo <词>", Desc: "回声",
		Run: func(args []string) (string, error) { return strings.Join(args, " "), nil },
	})
	s.cmds = cmds
	frames, release := s.hub.Stream("")
	defer release()

	rec := httptest.NewRecorder()
	s.runCommand("/echo 甲", "sC", rec)
	f := waitFrame(t, frames, FrameCommand)
	if f.Session != "sC" {
		t.Fatalf("命令帧应带归属会话 sC,得 %q", f.Session)
	}
}

// TestControlAndRoleUseAreSessionScoped 会话级偏好经端点落地:带 session 写会话,
// 不带写全局(与从前一致);模型**不**写 provider.yaml(那是 provider 的默认模型)。
func TestControlAndRoleUseAreSessionScoped(t *testing.T) {
	s, _ := newTestServer()
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "A"}, {ID: "B"}, {ID: ""}}}
	cs.prefs = &prefsStub{byID: map[string]sdk.SessionPrefs{}}
	s.cs = cs
	// 审批/沙箱服务:测试 Server 没装配,给最小替身(control 端点要它们才能通过校验)
	s.llm = &stubLLM{}
	s.ap = &stubAPRaw{mode: sdk.ApprovalSmart}
	s.sb = &stubSB{mode: sdk.SandboxWorkspace}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	post := func(path, body string) string {
		t.Helper()
		resp, err := http.Post(hs.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s: %d %s", path, resp.StatusCode, string(b))
		}
		return string(b)
	}

	// 角色:写会话 A(端点链路需要真角色服务;这里直接打存储面 —— 端点本身在 host-roles 侧验)
	if err := s.setSessionPref("A", func(p *sdk.SessionPrefs) { p.Role = "finance" }); err != nil {
		t.Fatal(err)
	}
	if got := cs.prefs.byID["A"].Role; got != "finance" {
		t.Fatalf("角色应写进会话 A,得 %q", got)
	}
	// 模型/思考/沙箱/审批:写会话 A
	post("/api/control", `{"session":"A","model":"m-A","thinking":"high","sandbox":"read-only","approval":"strict"}`)
	got := cs.prefs.byID["A"]
	if got.Model != "m-A" || got.Thinking != "high" || got.Sandbox != "read-only" || got.Approval != "strict" {
		t.Fatalf("会话 A 的四项偏好应落库,得 %+v", got)
	}
	// 不带 session ⇒ 不碰会话偏好(全局路径)
	post("/api/control", `{"model":"m-global"}`)
	if len(cs.prefs.byID) != 1 {
		t.Fatalf("不带 session 不该写会话偏好,得 %+v", cs.prefs.byID)
	}

	// state 按会话给生效值 + 来源
	resp, err := http.Get(hs.URL + "/api/state?session=A")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v StateView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Model != "m-A" || v.ModelFrom != "session" {
		t.Fatalf("会话 A 的生效模型应是 m-A(session 来源),得 model=%q from=%q", v.Model, v.ModelFrom)
	}
	if v.Sandbox != "read-only" || v.Approval != "strict" {
		t.Fatalf("会话 A 的沙箱/审批应取会话值,得 %q/%q", v.Sandbox, v.Approval)
	}
	// 另一个会话(B,没设)⇒ 跟随全局
	resp2, err := http.Get(hs.URL + "/api/state?session=B")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var v2 StateView
	if err := json.NewDecoder(resp2.Body).Decode(&v2); err != nil {
		t.Fatal(err)
	}
	if v2.Model == "m-A" || v2.Sandbox == "read-only" || v2.Role == "finance" {
		t.Fatalf("没设过的会话应跟随全局(不该看到 A 的值):model=%q sandbox=%q role=%q", v2.Model, v2.Sandbox, v2.Role)
	}
}
