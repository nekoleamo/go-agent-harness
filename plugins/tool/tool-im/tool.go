// Package toolim 提供 tool-im 插件(G-E5-3 IM-1):把「向已授权 IM 目标发消息」暴露给模型。
//
// 安全口径(与 DESIGN §14.1 实施方案 D1 一致):
//   - **默认不注册**:`data.enabled: true` 才注册工具——未启用时模型完全看不到 im_send/im_status;
//   - **仅已授权目标**:目标必须 ∈ ctx.imControl.Targets()(未授权显式报错,不隐式回落 LastRoute);
//   - **审批链**:建议同时把 im_send 加入 policy-guard 的 `data.approval_tools`(启用但未接入 → 启动警告);
//   - **出站复用通道预算/配额/ledger**(无旁路);调用与结果进会话日志(模型可见即已记录)。
package toolim

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 工具名(审批名单与文档引用同一常量)。
const (
	ToolSend     = "im_send"
	ToolSendFile = "im_send_file"
	ToolStatus   = "im_status"
)

// maxSendRunes 单次投递文本上限(rune;通道分块由通道预算层负责,此处防滥用/误用)。
const maxSendRunes = 8000

// Plugin 实现 tool-im。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-im" }

// Start 按 data.enabled 决定是否注册工具(默认关;关 = 零副作用)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	if !enabled(m) {
		return func() {}, nil
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	// 审批链自检:启用但没有把副作用工具接入工具级审批 → 记警告(不静默假设安全)
	if gate, ok := approvalGate(c); ok {
		var missing []string
		for _, name := range []string{ToolSend, ToolSendFile} {
			if !gate.RequiresToolApproval(name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			c.Logger().Warn("tool-im: 已启用但未接入工具级审批(远程副作用)",
				"tools", strings.Join(missing, ","), "hint", "policy-guard data.approval_tools")
		}
	}
	d1 := tools.Register(&sendTool{c: c})
	d2 := tools.Register(&statusTool{c: c})
	d3 := tools.Register(&sendFileTool{c: c})
	return func() {
		d1()
		d2()
		d3()
	}, nil
}

// enabled 读 data.enabled(bool 或字符串,默认 false)。
func enabled(m *sdk.Manifest) bool {
	if m == nil || m.Data == nil {
		return false
	}
	switch v := m.Data["enabled"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

// approvalGate 取审批闸门(可选能力:policy-guard 实现 ApprovalToolGate)。
func approvalGate(c sdk.Ctx) (sdk.ApprovalToolGate, bool) {
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil || ap == nil {
		return nil, false
	}
	gate, ok := ap.(sdk.ApprovalToolGate)
	return gate, ok
}

// control 运行期现取 ctx.imControl(未装配 → 明确错误;不在 Start 一次性注入,
// 避免与 ui-im-* 的装配顺序耦合——与 policy-guard/tool-ask 同一时序纪律)。
func control(c sdk.Ctx) (sdk.IMControlService, error) {
	if c == nil {
		return nil, fmt.Errorf("IM 控制面未装配")
	}
	var svc sdk.IMControlService
	if err := c.Inject("ctx.imControl", &svc); err != nil || svc == nil {
		return nil, fmt.Errorf("IM 控制面未装配(该 profile 无 IM 渠道;请用 im-qq/im-wechat 系 profile)")
	}
	return svc, nil
}

// sendTool im_send:向已授权目标投递文本。
type sendTool struct{ c sdk.Ctx }

func (t *sendTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: ToolSend,
		Description: "向**已授权**的 IM 用户或群发送一条消息(远程回执/通知)。" +
			"仅在用户明确要求「发到微信/QQ」「通知某人/某个群」时使用;" +
			"target 必须取自 im_status 返回的可投目标(未授权目标会被拒绝);" +
			"不要用于寒暄或自言自语,也不要把长文整篇转发。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "目标标识(裸 openid / 群 id,取自 im_status 的 targets[].key)",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "消息正文(纯文本;通道会按平台规则分块,单次 ≤8000 字)",
				},
			},
			"required": []string{"target", "text"},
		},
	}
}

func (t *sendTool) Execute(ctx context.Context, argsJSON string) (any, error) {
	var in struct {
		Target string `json:"target"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return nil, fmt.Errorf("%s: 参数解析失败: %w", ToolSend, err)
	}
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return nil, fmt.Errorf("%s: target 不能为空(用 im_status 查看可投目标)", ToolSend)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, fmt.Errorf("%s: text 不能为空", ToolSend)
	}
	if n := len([]rune(text)); n > maxSendRunes {
		return nil, fmt.Errorf("%s: text 过长(%d 字 > %d);请精简或分段", ToolSend, n, maxSendRunes)
	}
	svc, err := control(t.c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolSend, err)
	}
	if err := svc.SendText(ctx, target, text); err != nil {
		return nil, fmt.Errorf("%s: %w", ToolSend, err)
	}
	return map[string]any{
		"ok":     true,
		"target": target,
		"chars":  len([]rune(text)),
		"note":   "已投递(通道分块/配额规则由渠道决定;IM 侧可见完整内容)",
	}, nil
}

// sendFileTool im_send_file:把**工作区内**的文件/图片投递到已授权目标(MED-2)。
// 安全:D2 口径——先登记(RegisterArtifact:工作区 realpath 校验 + 大小上限 + 单次可用),
// 再按登记 id 投递;通道未实现出站媒体 → 显式错误。
type sendFileTool struct{ c sdk.Ctx }

func (t *sendFileTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: ToolSendFile,
		Description: "把一个**工作区内**的文件(图片/文档)发送到已授权的 IM 用户或群。" +
			"仅在用户明确要求「把这个文件发给我/发到群里」时使用;" +
			"target 必须取自 im_status 的 targets[].key;路径必须是当前工作区内的常规文件," +
			"且受大小上限与审批策略约束(未登记/越界/超限会被拒绝)。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "目标标识(取自 im_status 的 targets[].key)",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "待发送文件路径(当前工作区内;可用相对路径)",
				},
			},
			"required": []string{"target", "path"},
		},
	}
}

func (t *sendFileTool) Execute(ctx context.Context, argsJSON string) (any, error) {
	var in struct {
		Target string `json:"target"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return nil, fmt.Errorf("%s: 参数解析失败: %w", ToolSendFile, err)
	}
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return nil, fmt.Errorf("%s: target 不能为空(用 im_status 查看可投目标)", ToolSendFile)
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return nil, fmt.Errorf("%s: path 不能为空", ToolSendFile)
	}
	svc, err := control(t.c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolSendFile, err)
	}
	att, ok := svc.(sdk.IMAttachmentService)
	if !ok {
		return nil, fmt.Errorf("%s: 该渠道不支持出站文件", ToolSendFile)
	}
	art, err := att.RegisterArtifact(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolSendFile, err)
	}
	if err := att.SendArtifact(ctx, target, art.ID); err != nil {
		return nil, fmt.Errorf("%s: %w", ToolSendFile, err)
	}
	return map[string]any{
		"ok":          true,
		"target":      target,
		"name":        art.Name,
		"bytes":       art.Bytes,
		"kind":        art.Kind,
		"artifact_id": art.ID,
		"note":        "已投递(受大小上限与渠道平台限制;失败会如实报错,可重试)",
	}, nil
}

// statusTool im_status:只读状态 + 可投目标。
type statusTool struct{ c sdk.Ctx }

func (t *statusTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: ToolStatus,
		Description: "查看 IM 远程控制状态:连接相位、当前模型与会话、忙闲、已授权用户/群、" +
			"以及 im_send 可用的目标列表。发消息前先调用它取 target。",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (t *statusTool) Execute(_ context.Context, _ string) (any, error) {
	svc, err := control(t.c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolStatus, err)
	}
	return svc.Status(), nil
}
