// tool-im 单测:默认不注册 / 启用后注册两工具 / 目标与文本校验 / 未装配控制面结构化错误。
package toolim

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeControl 受控出站面替身。
type fakeControl struct {
	targets   []sdk.IMSendTarget
	sent      []string
	status    sdk.IMControlStatus
	sendErr   error
	connected bool
}

func (f *fakeControl) Status() sdk.IMControlStatus { return f.status }
func (f *fakeControl) Targets() []sdk.IMSendTarget { return f.targets }
func (f *fakeControl) SendText(_ context.Context, target, text string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, target+"|"+text)
	return nil
}

// fakeGate 审批闸门替身。
type fakeGate struct {
	need map[string]bool
}

func (f fakeGate) Mode() sdk.ApprovalMode             { return sdk.ApprovalSmart }
func (f fakeGate) SetMode(m sdk.ApprovalMode)         {}
func (f fakeGate) RequiresToolApproval(n string) bool { return f.need[n] }

func build(t *testing.T, data map[string]any, svc sdk.IMControlService, ap sdk.ApprovalService) sdk.Ctx {
	t.Helper()
	c := ctx.New(slog.New(slog.DiscardHandler), event.New(slog.New(slog.DiscardHandler)))
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if svc != nil {
		if err := c.Provide("ctx.imControl", svc); err != nil {
			t.Fatal(err)
		}
	}
	if ap != nil {
		if err := c.Provide("ctx.approval", ap); err != nil {
			t.Fatal(err)
		}
	}
	m := &sdk.Manifest{}
	if data != nil {
		m.Data = data
	}
	if _, err := (&Plugin{}).Start(c, m); err != nil {
		t.Fatal(err)
	}
	return c
}

func toolsOf(t *testing.T, c sdk.Ctx) sdk.ToolRegistry {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

// fakeAttach 同时实现 IMAttachmentService(用于 im_send_file)。
type fakeAttach struct {
	fakeControl
	registered []string
	sent       []string
	regErr     error
	sendErr    error
}

func (f *fakeAttach) RegisterArtifact(_ context.Context, path string) (sdk.IMArtifact, error) {
	if f.regErr != nil {
		return sdk.IMArtifact{}, f.regErr
	}
	f.registered = append(f.registered, path)
	return sdk.IMArtifact{ID: "art-x", Name: "chart.png", Kind: "image", Bytes: 7}, nil
}

func (f *fakeAttach) SendArtifact(_ context.Context, target, id string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, target+"|"+id)
	return nil
}

func TestSendFileTool(t *testing.T) {
	att := &fakeAttach{}
	c := build(t, map[string]any{"enabled": true}, att, nil)
	tools := toolsOf(t, c)
	if _, ok := tools.Get(ToolSendFile); !ok {
		t.Fatal("im_send_file 应注册")
	}
	res, err := tools.Execute(context.Background(), ToolSendFile, `{"target":"u1","path":"out/chart.png"}`)
	if err != nil || res.Error != "" || !strings.Contains(res.Content, `"ok":true`) {
		t.Fatalf("投递应成功: err=%v res=%+v", err, res)
	}
	if len(att.registered) != 1 || att.registered[0] != "out/chart.png" {
		t.Fatalf("应先登记路径: %+v", att.registered)
	}
	if len(att.sent) != 1 || att.sent[0] != "u1|art-x" {
		t.Fatalf("应按登记 id 投递: %+v", att.sent)
	}
	// 参数校验 + 失败透传
	for _, args := range []string{`{"path":"a.png"}`, `{"target":"u1"}`, `{}`, `{`} {
		if r, err := tools.Execute(context.Background(), ToolSendFile, args); err == nil && r.Error == "" {
			t.Fatalf("args=%s 不应成功", args)
		}
	}
	att.regErr = context.Canceled
	r2, _ := tools.Execute(context.Background(), ToolSendFile, `{"target":"u1","path":"a.png"}`)
	if r2 == nil || !strings.Contains(r2.Error+r2.Content, ToolSendFile+":") {
		t.Fatalf("登记失败应带工具名前缀: %+v", r2)
	}
	// 渠道不支持出站文件(仅实现 IMControlService)→ 显式错误
	c2 := build(t, map[string]any{"enabled": true}, &fakeControl{}, nil)
	r3, _ := toolsOf(t, c2).Execute(context.Background(), ToolSendFile, `{"target":"u1","path":"a.png"}`)
	if r3 == nil || !strings.Contains(r3.Error+r3.Content, "不支持出站文件") {
		t.Fatalf("未实现附件面应显式报错: %+v", r3)
	}
}

func TestDefaultOffRegistersNothing(t *testing.T) {
	for _, data := range []map[string]any{nil, {}, {"enabled": false}, {"enabled": "false"}} {
		c := build(t, data, &fakeControl{}, nil)
		tools := toolsOf(t, c)
		if _, ok := tools.Get(ToolSend); ok {
			t.Fatalf("默认不应注册 im_send(data=%v)", data)
		}
		if _, ok := tools.Get(ToolStatus); ok {
			t.Fatalf("默认不应注册 im_status(data=%v)", data)
		}
		if _, ok := tools.Get(ToolSendFile); ok {
			t.Fatalf("默认不应注册 im_send_file(data=%v)", data)
		}
	}
}

func TestEnabledRegistersBothTools(t *testing.T) {
	svc := &fakeControl{targets: []sdk.IMSendTarget{{Key: "u1", Channel: "qq", Label: "用户 u1", UserID: "u1"}}}
	svc.status = sdk.IMControlStatus{Channel: "qq", Connected: true, Phase: "done", Targets: svc.targets}
	c := build(t, map[string]any{"enabled": true}, svc, nil)
	tools := toolsOf(t, c)

	// im_status:透传只读状态(结果 JSON 含渠道/连通/目标)
	res, err := tools.Execute(context.Background(), ToolStatus, `{}`)
	if err != nil || res.Error != "" {
		t.Fatalf("im_status 应可用: err=%v res=%+v", err, res)
	}
	for _, want := range []string{`"channel":"qq"`, `"connected":true`, `"key":"u1"`} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("im_status 结果缺 %s: %s", want, res.Content)
		}
	}

	// im_send:校验 + 投递
	res2, err := tools.Execute(context.Background(), ToolSend, `{"target":"u1","text":"部署完成"}`)
	if err != nil || res2.Error != "" || !strings.Contains(res2.Content, `"ok":true`) {
		t.Fatalf("im_send 应成功: err=%v res=%+v", err, res2)
	}
	if len(svc.sent) != 1 || svc.sent[0] != "u1|部署完成" {
		t.Fatalf("投递异常: %+v", svc.sent)
	}
	// 参数校验:缺 target / 缺 text / 过长 / 坏 JSON
	long := strings.Repeat("字", maxSendRunes+1)
	for _, args := range []string{
		`{"text":"x"}`, `{"target":"u1"}`, `{"target":"u1","text":"   "}`,
		`{"target":"u1","text":"` + long + `"}`, `{`, `{"target":"u1","text":"ok"} extra`,
	} {
		res, err := tools.Execute(context.Background(), ToolSend, args)
		if err == nil && (res == nil || res.Error == "") && res != nil && strings.Contains(res.Content, `"ok":true`) {
			t.Fatalf("args=%s 不应成功", args)
		}
	}
	if len(svc.sent) != 1 {
		t.Fatalf("错误路径不应投递: %+v", svc.sent)
	}
}

func TestSendErrorsAreStructured(t *testing.T) {
	// 未装配 ctx.imControl → 明确错误(不静默成功)
	c := build(t, map[string]any{"enabled": "true"}, nil, nil)
	tools := toolsOf(t, c)
	for _, name := range []string{ToolSend, ToolStatus} {
		res, err := tools.Execute(context.Background(), name, `{"target":"u1","text":"x"}`)
		msg := ""
		if err != nil {
			msg = err.Error()
		} else if res != nil {
			msg = res.Error + res.Content
		}
		if !strings.Contains(msg, "未装配") {
			t.Fatalf("%s 未装配应报明确错误: err=%v res=%+v", name, err, res)
		}
	}
	// 底层投递失败(未授权)→ 带工具名前缀透传
	svc := &fakeControl{sendErr: context.Canceled}
	c2 := build(t, map[string]any{"enabled": true}, svc, nil)
	res, err := toolsOf(t, c2).Execute(context.Background(), ToolSend, `{"target":"stranger","text":"x"}`)
	msg := ""
	if err != nil {
		msg = err.Error()
	} else if res != nil {
		msg = res.Error + res.Content
	}
	if !strings.Contains(msg, ToolSend+":") {
		t.Fatalf("底层错误应带工具名前缀: err=%v res=%+v", err, res)
	}
}

func TestApprovalGateWarningPath(t *testing.T) {
	// 启用 + 已接入审批 → 正常注册;启用 + 未接入 → 仍注册(仅记警告,不阻塞)
	svc := &fakeControl{}
	for name, gate := range map[string]sdk.ApprovalService{
		"armed":   fakeGate{need: map[string]bool{ToolSend: true}},
		"unarmed": fakeGate{need: map[string]bool{}},
	} {
		c := build(t, map[string]any{"enabled": true}, svc, gate)
		if _, ok := toolsOf(t, c).Get(ToolSend); !ok {
			t.Fatalf("%s: 启用时应注册工具", name)
		}
	}
	// ctx.approval 未装配(无闸门)→ 不 panic,仍注册
	c := build(t, map[string]any{"enabled": true}, svc, nil)
	if _, ok := toolsOf(t, c).Get(ToolSend); !ok {
		t.Fatal("无审批服务时仍应注册")
	}
}
