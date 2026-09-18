// serve_protocol_test.go 外部插件侧协议服务端(serve.go)直调测试:
// Definitions/Definition/Execute/ExecuteNamed/Cancel/Commands/CommandOptions/RunCommand
// 是跨进程契约的「插件一侧」,字段与语义漂移会静默破坏宿主加载(如 PathParams 声明丢失
// → 路径沙箱失效),故以真实结构体往返 + 显式错误分支锚定。
package hostbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// serveStubTool 返回固定结果的工具(记录收到的 ctx,便于断言取消语义)。
type serveStubTool struct {
	name    string
	result  any
	err     error
	timeout int64
	paths   []sdk.PathParam
	appr    string // ApprovalTargetParam(代理工具声明;NOND-M1-3b)
	gotArgs string
	ctxErr  error
}

func (t *serveStubTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: t.name, Description: "测试工具", TimeoutMs: t.timeout, PathParams: t.paths,
		ApprovalTargetParam: t.appr,
		InputSchema:         map[string]any{"type": "object"},
	}
}

func (t *serveStubTool) Execute(ctx context.Context, raw string) (any, error) {
	t.gotArgs = raw
	if t.err != nil {
		return nil, t.err
	}
	if t.timeout > 0 { // 模拟慢工具:等到 ctx 结束或被下发超时打断
		select {
		case <-ctx.Done():
			t.ctxErr = ctx.Err()
			return nil, ctx.Err()
		case <-time.After(time.Duration(t.timeout) * time.Millisecond):
		}
	}
	return t.result, nil
}

// chanTool 结果不可 JSON 序列化(覆盖 marshal 失败分支)。
type chanTool struct{ serveStubTool }

func (t *chanTool) Execute(context.Context, string) (any, error) {
	return make(chan int), nil
}

// 直调服务端统一用生产构造器 newToolServer(serve.go):测试不再另起一份副本,
// 免得「构造点漏初始化 running 表」这类缺陷只能靠宿主 Cancel RPC 在真机上暴露。

// TestToolServerDefinitionsSortedWithCapabilityDefinitions JSON 数组按名排序,
// 且能力声明(PathParams/ApprovalTargetParam/TimeoutMs)必须跨协议传递
// (路径裁决与工具级审批都依赖它)。
func TestToolServerDefinitionsSortedWithCapabilityDefinitions(t *testing.T) {
	paths := []sdk.PathParam{{Arg: "target", Access: sdk.PathWrite, Many: true}}
	srv := newToolServer(map[string]sdk.Tool{
		"zeta": &serveStubTool{name: "zeta"},
		"alpha": &serveStubTool{
			name: "alpha", timeout: 4500, paths: paths, appr: "name", result: map[string]any{"ok": true},
		},
	}, nil)
	var raw string
	if err := srv.Definitions(struct{}{}, &raw); err != nil {
		t.Fatal(err)
	}
	var defs []defDTO
	if err := json.Unmarshal([]byte(raw), &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 || defs[0].Name != "alpha" || defs[1].Name != "zeta" {
		t.Fatalf("定义应按名排序: %+v", defs)
	}
	if defs[0].TimeoutMs != 4500 || len(defs[0].PathParams) != 1 ||
		defs[0].PathParams[0].Arg != "target" || defs[0].PathParams[0].Access != sdk.PathWrite ||
		!defs[0].PathParams[0].Many {
		t.Fatalf("能力声明/超时未原样透传: %+v", defs[0])
	}
	if defs[0].ApprovalTargetParam != "name" {
		t.Fatalf("代理目标声明未透传: %+v", defs[0])
	}
	if defs[0].InputSchema["type"] != "object" {
		t.Fatalf("InputSchema 应透传: %+v", defs[0].InputSchema)
	}
}

// TestToolServerDefinitionAndExecuteSingleToolOnly 旧单工具协议:
// 单工具可用;多工具必须走新协议(显式报错,不猜哪一个)。
func TestToolServerDefinitionAndExecuteSingleToolOnly(t *testing.T) {
	single := newToolServer(map[string]sdk.Tool{"only": &serveStubTool{name: "only", result: "值"}}, nil)
	var defJSON string
	if err := single.Definition(struct{}{}, &defJSON); err != nil {
		t.Fatal(err)
	}
	var def sdk.ToolDefinition
	if err := json.Unmarshal([]byte(defJSON), &def); err != nil {
		t.Fatal(err)
	}
	if def.Name != "only" {
		t.Fatalf("单工具定义不符: %+v", def)
	}
	reply := &ExecReply{}
	if err := single.Execute(&ExecArgs{JSONArgs: `{"a":1}`}, reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || !strings.Contains(reply.Content, "值") {
		t.Fatalf("单工具执行应成功: %+v", reply)
	}

	multi := newToolServer(map[string]sdk.Tool{
		"a": &serveStubTool{name: "a"}, "b": &serveStubTool{name: "b"},
	}, nil)
	if err := multi.Definition(struct{}{}, &defJSON); err == nil || !strings.Contains(err.Error(), "Definitions") {
		t.Fatalf("多工具进程 Definition 应显式报错: %v", err)
	}
	if err := multi.Execute(&ExecArgs{}, reply); err == nil || !strings.Contains(err.Error(), "ExecuteNamed") {
		t.Fatalf("多工具进程 Execute 应显式报错: %v", err)
	}
}

// TestToolServerExecuteNamedCoverage ExecuteNamed 三条路径:
// 未知工具名 → 结构化错误(不返回 Go 错误);工具业务错误 → 带名前缀;
// 结果不可序列化 → 明确报错(不静默丢结果)。
func TestToolServerExecuteNamedCoverage(t *testing.T) {
	srv := newToolServer(map[string]sdk.Tool{
		"ok":      &serveStubTool{name: "ok", result: map[string]any{"n": 1}},
		"plain":   &serveStubTool{name: "plain", result: "纯文本结果"},
		"boom":    &serveStubTool{name: "boom", err: errInjectionFailed},
		"unmarsh": &chanTool{serveStubTool{name: "unmarsh"}},
	}, nil)

	// 未知工具
	reply := &ExecReply{}
	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "nope"}, reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Error, "无此工具") {
		t.Fatalf("未知工具应结构化报错: %+v", reply)
	}
	// 正常:JSON 结果 + 参数透传
	reply = &ExecReply{}
	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "ok", JSONArgs: `{"x":1}`}, reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || !strings.Contains(reply.Content, `"n":1`) {
		t.Fatalf("结果应序列化: %+v", reply)
	}
	if srv.tools["ok"].(*serveStubTool).gotArgs != `{"x":1}` {
		t.Fatal("参数应原样透传给工具")
	}
	// 非结构化结果:JSON 序列化后为字符串字面量(宿主再解码回字符串)
	reply = &ExecReply{}
	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "plain"}, reply); err != nil {
		t.Fatal(err)
	}
	if reply.Content != `"纯文本结果"` {
		t.Fatalf("字符串结果应 JSON 编码: %q", reply.Content)
	}
	// 业务错误:前缀工具名
	reply = &ExecReply{}
	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "boom"}, reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Error, "boom: ") || !strings.Contains(reply.Error, "注入失败") {
		t.Fatalf("业务错误应带工具名前缀: %q", reply.Error)
	}
	// 序列化失败
	reply = &ExecReply{}
	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "unmarsh"}, reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Error, "结果序列化失败") {
		t.Fatalf("不可序列化结果应显式报错: %q", reply.Error)
	}
}

// TestToolServerCancelInputGuards Cancel 的入参边界:reply 为 nil 不 panic;
// 空/未知 CallID 一律写回 false(未命中不等于错误)。
func TestToolServerCancelInputGuards(t *testing.T) {
	srv := newToolServer(map[string]sdk.Tool{"x": &serveStubTool{name: "x"}}, nil)
	if err := srv.Cancel(nil, nil); err != nil {
		t.Fatalf("reply=nil 应安全返回: %v", err)
	}
	var ok bool
	if err := srv.Cancel(nil, &ok); err != nil || ok {
		t.Fatalf("args=nil 应 false: ok=%v err=%v", ok, err)
	}
	if err := srv.Cancel(&CancelArgs{CallID: ""}, &ok); err != nil || ok {
		t.Fatalf("空 CallID 应 false: ok=%v err=%v", ok, err)
	}
	if err := srv.Cancel(&CancelArgs{CallID: "unknown"}, &ok); err != nil || ok {
		t.Fatalf("未知 CallID 应 false: ok=%v err=%v", ok, err)
	}
}

// TestToolServerCommandsAndOptionsDTO 命令声明 DTO:
// 枚举级 → Enum=true(选项运行期求值);自由级 → FreeArgs 名字序列;皆空 → 无定义级;
// CommandOptions 对非枚举级/越界/未知名返回空(选择器退化为直接执行)。
func TestToolServerCommandsAndOptionsDTO(t *testing.T) {
	specs := map[string]sdk.CommandSpec{
		"enum": {
			Name: "enum", Usage: "/enum", Desc: "枚举级", TimeoutMs: 1200,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option { return []sdk.Option{{Value: "a", Desc: "A"}} }},
				{FreeArgs: func([]string) []string { return []string{"文本", "目标?"} }},
				{},
			},
		},
		"bare": {}, // 未声明 Args
	}
	srv := newToolServer(nil, specs)

	var raw string
	if err := srv.Commands(struct{}{}, &raw); err != nil {
		t.Fatal(err)
	}
	var got []CommandDTO
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "bare" || got[1].Name != "enum" {
		t.Fatalf("命令应按名排序: %+v", got)
	}
	if got[0].Name != "bare" || len(got[0].Args) != 0 {
		t.Fatalf("未声明 Args 应为空: %+v", got[0])
	}
	enum := got[1]
	if enum.TimeoutMs != 1200 || len(enum.Args) != 3 {
		t.Fatalf("命令 DTO 不符: %+v", enum)
	}
	if !enum.Args[0].Enum || enum.Args[0].FreeArgs != nil {
		t.Fatalf("枚举级应标 Enum: %+v", enum.Args[0])
	}
	if len(enum.Args[1].FreeArgs) != 2 || enum.Args[1].Enum {
		t.Fatalf("自由级应带参数名序列: %+v", enum.Args[1])
	}
	if enum.Args[2].Enum || enum.Args[2].FreeArgs != nil {
		t.Fatalf("未声明级应两者皆空: %+v", enum.Args[2])
	}
	// Name 兜底:map key 与 spec.Name 不一致时用 key
	srv2 := newToolServer(nil, map[string]sdk.CommandSpec{"alias": {Desc: "无 Name"}})
	if err := srv2.Commands(struct{}{}, &raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"Name":"alias"`) {
		t.Fatalf("Name 为空应回退为 map key: %s", raw)
	}

	// CommandOptions:枚举级求值
	var opts string
	if err := srv.CommandOptions(&CmdOptionsArgs{Name: "enum", Level: 0}, &opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opts, `"a"`) {
		t.Fatalf("枚举级应回选项: %s", opts)
	}
	for _, tc := range []struct {
		name string
		args CmdOptionsArgs
	}{
		{"未知命令", CmdOptionsArgs{Name: "nope", Level: 0}},
		{"负级", CmdOptionsArgs{Name: "enum", Level: -1}},
		{"越界级", CmdOptionsArgs{Name: "enum", Level: 9}},
		{"自由级(非枚举)", CmdOptionsArgs{Name: "enum", Level: 1}},
		{"无声明级", CmdOptionsArgs{Name: "enum", Level: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts = "哨兵"
			if err := srv.CommandOptions(&tc.args, &opts); err != nil {
				t.Fatal(err)
			}
			if opts != "" {
				t.Fatalf("非枚举级应回空(选择器直接执行): %q", opts)
			}
		})
	}
}

// TestToolServerRunCommand RunCommand:未知命令结构化报错;失败时输出仍回传(便于诊断);
// 成功时只填 Content。
func TestToolServerRunCommand(t *testing.T) {
	srv := newToolServer(nil, map[string]sdk.CommandSpec{
		"ok":  {Name: "ok", Run: func(args []string) (string, error) { return "输出:" + strings.Join(args, ","), nil }},
		"bad": {Name: "bad", Run: func([]string) (string, error) { return "半截输出", errInjectionFailed }},
	})
	reply := &ExecReply{}
	if err := srv.RunCommand(&RunCommandArgs{Name: "ok", Args: []string{"a", "b"}}, reply); err != nil {
		t.Fatal(err)
	}
	if reply.Content != "输出:a,b" || reply.Error != "" {
		t.Fatalf("成功命令应答不符: %+v", reply)
	}
	reply = &ExecReply{}
	if err := srv.RunCommand(&RunCommandArgs{Name: "bad"}, reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "注入失败" || reply.Content != "半截输出" {
		t.Fatalf("失败命令应回传错误 + 已有输出: %+v", reply)
	}
	reply = &ExecReply{}
	if err := srv.RunCommand(&RunCommandArgs{Name: "nope"}, reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Error, "无此命令") {
		t.Fatalf("未知命令应结构化报错: %+v", reply)
	}
}

// TestToolServerConstructedWithRunningTable 协议服务端构造入口的边界:
// running 表必须在构造时初始化(否则宿主 Cancel RPC → 写 nil map panic),
// 命令表透传(纯工具插件 = nil 命令表,Commands 回空数组)。
func TestToolServerConstructedWithRunningTable(t *testing.T) {
	ts := newToolServer(map[string]sdk.Tool{"x": &serveStubTool{name: "x"}},
		map[string]sdk.CommandSpec{"c": {Name: "c"}})
	if ts.running == nil {
		t.Fatal("running 表必须初始化(Cancel 会写它)")
	}
	var hit bool
	if err := ts.Cancel(&CancelArgs{CallID: "x"}, &hit); err != nil {
		t.Fatalf("running 表已初始化,Cancel 不应出错: %v", err)
	}
	var raw string
	if err := ts.Commands(struct{}{}, &raw); err != nil || !strings.Contains(raw, "\"c\"") {
		t.Fatalf("命令表应透传: %q/%v", raw, err)
	}
	pure := newToolServer(map[string]sdk.Tool{"x": &serveStubTool{name: "x"}}, nil)
	raw = "unset"
	if err := pure.Commands(struct{}{}, &raw); err != nil {
		t.Fatalf("纯工具插件的命令枚举不应报错: %v", err)
	}
	var cmds []CommandDTO
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cmds); err != nil {
			t.Fatalf("命令枚举应为 JSON 数组/空: %q (%v)", raw, err)
		}
	}
	if len(cmds) != 0 {
		t.Fatalf("纯工具插件不应有命令: %+v", cmds)
	}
}

// TestHandshakeIdentity 握手标识/协议版本是两侧一致性的唯一凭据(漂移 ⇒ 外部插件全部加载失败),
// 且不匹配时必须给**可操作**的诊断(旧版 go-plugin 产物 / 版本不符 / 无法识别)。
func TestHandshakeIdentity(t *testing.T) {
	if HandshakeKey != "GAH_PLUGIN" || HandshakeValue != "gah-external-tool" {
		t.Fatalf("握手标识不符: %s=%s", HandshakeKey, HandshakeValue)
	}
	if protoVersion != 2 {
		t.Fatalf("传输层协议版本应为 2(stdio),得到 %d", protoVersion)
	}
	if got, want := handshakeLine(), "GAH-PLUGIN|2|stdio\n"; got != want {
		t.Fatalf("握手行 = %q,期望 %q", got, want)
	}
	if err := classifyHandshake(handshakeLine()); err != nil {
		t.Fatalf("本版本握手行应通过: %v", err)
	}

	// 旧版 go-plugin 握手行 → 指名道姓的升级指引(而不是笼统的「握手失败」)
	err := classifyHandshake("1|1|tcp|127.0.0.1:53219|grpc\n")
	if err == nil || !strings.Contains(err.Error(), "go-plugin") || !strings.Contains(err.Error(), "重新编译") {
		t.Fatalf("旧版产物应给可操作错误: %v", err)
	}
	// 版本不符 → 报两侧版本
	err = classifyHandshake("GAH-PLUGIN|9|stdio\n")
	if err == nil || !strings.Contains(err.Error(), "9") || !strings.Contains(err.Error(), "协议版本不匹配") {
		t.Fatalf("版本不符应报两侧版本: %v", err)
	}
	// 其它输出(插件往 stdout 打了别的东西)→ 报原文并给期望格式
	err = classifyHandshake("hello world\n")
	if err == nil || !strings.Contains(err.Error(), "hello world") || !strings.Contains(err.Error(), "GAH-PLUGIN|") {
		t.Fatalf("无法识别应报原文 + 期望格式: %v", err)
	}
}
