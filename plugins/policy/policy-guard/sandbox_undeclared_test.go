package policyguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 本文件覆盖 2026-09-27 安全审计 F2 的修复:未声明路径参数的工具不再 fail-open。
//
// 修复前:`CheckToolCallAt` 在「工具自述 PathParams 为空 + 工具名不在内置表」时 `return nil`,
// 而 `approval_tools` 默认空 —— 第三方插件工具与 MCP 工具(mcp-bridge 造的 ToolDefinition
// 不带 PathParams)**完全不受路径沙箱约束**:
//   - `mcp_<server>_read_file{path:"~/.ssh/id_rsa"}` → 凭据进模型上下文;
//   - `save_note{target:"/etc/hosts"}` → 越界写(写侧内核层只包 shell 子进程,不管插件进程自己写);
//   - `mcp_call{name:"mcp_srv_write_file",arguments:{path:"…"}}` → 代理调用连内层参数都不解析。
//
// 修复后按四级收敛(声明 → 内置名表 → 推断 → 值级兜底),本文件按"每一级各守一段"组织断言。

// TestCheckToolCallUndeclaredConvergence 未声明工具的收敛语义(单元级,直接打裁决函数)。
func TestCheckToolCallUndeclaredConvergence(t *testing.T) {
	withCaseFold(t, false)
	ws := t.TempDir()
	outside := t.TempDir()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome) // denyDir 按 $HOME 判定(见 pathpolicy.go)
	keyDir := filepath.Join(fakeHome, ".ssh")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "id_rsa"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := DefaultSandbox(ws)

	// ③ 值级兜底:无工具定义(CheckPathArgs 入口)也能拦住"一眼是路径"的越界值。
	// 这条同时是**分层证据**:③ 不依赖 ② 的 schema 推断。
	err := p.CheckPathArgs("mcp_srv_write_file", fmt.Sprintf(`{"target":%q}`, filepath.Join(outside, "x.txt")))
	if err == nil {
		t.Fatal("未声明工具的越界绝对路径写应被值级兜底拒")
	}
	// 文案必须可执行:说清"为何被拒"与"怎么解除"(否则第三方作者无从下手,模型也会换参数重试)
	for _, want := range []string{"未声明路径参数", "PathParamsDeclared"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("兜底拒绝文案应含 %q,got %v", want, err)
		}
	}

	// ③ 凭据目录:未声明工具读 ~/.ssh 下的文件(值级兜底 + denyPath)
	// 用 config(basename 不在凭据文件名表里)→ 命中的是"用户密钥目录"规则,而不是撞名表
	err = p.CheckPathArgs("mcp_srv_read_file", fmt.Sprintf(`{"blob":%q}`, filepath.Join(keyDir, "config")))
	if err == nil || !strings.Contains(err.Error(), "密钥目录") {
		t.Fatalf("未声明工具读凭据目录应被拒且说明是密钥目录: %v", err)
	}
	// 密钥文件本体:撞 basename 名表(两层拒绝都在,哪层先命中不影响结论)
	err = p.CheckPathArgs("mcp_srv_read_file", fmt.Sprintf(`{"blob":%q}`, filepath.Join(keyDir, "id_rsa")))
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") {
		t.Fatalf("未声明工具读私钥应被拒: %v", err)
	}

	// ③ 不误伤:URL 不是路径(模型把网址交给写工具是另一类事故,由 ValidatePath 自己判)
	if err := p.CheckPathArgs("mcp_srv_write_file", `{"target":"https://example.com/a/b"}`); err != nil {
		t.Fatalf("URL 值不应被当路径裁决: %v", err)
	}
	// ③ 不误伤:相对普通名(经工具 cwd 解析仍在工作区内)
	if err := p.CheckPathArgs("mcp_srv_write_file", `{"blob":"notes/todo.md"}`); err != nil {
		t.Fatalf("相对普通名不应被当路径裁决: %v", err)
	}
	// ③ 字符串数组:逐元素判
	if err := p.CheckPathArgs("mcp_srv_write_file", fmt.Sprintf(`{"files":[%q]}`, filepath.Join(outside, "a.txt"))); err == nil {
		t.Fatal("字符串数组里的越界路径应被拒")
	}
	// ③ 无 schema 入口(CheckPathArgs):array of object 的值级兜底仍**不递归** —— 递归会在大 JSON
	// 参数里误判;有 schema 的工具走 ② 推断的 Nested 路径裁决(见 TestCheckToolCallNestedPaths)。
	if err := p.CheckPathArgs("mcp_srv_write_file", fmt.Sprintf(`{"files":[{"path":%q}]}`, filepath.Join(outside, "a.txt"))); err != nil {
		t.Fatalf("无 schema 的 array of object 不做值级递归(登记为保守边界): %v", err)
	}

	// ② 推断:schema 的路径型参数名 + 工具名动词
	def := sdk.ToolDefinition{Name: "mcp_srv_write_file", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
	}}
	if err := p.CheckToolCallAt(ws, def.Name, fmt.Sprintf(`{"path":%q}`, filepath.Join(outside, "a.txt")), def); err == nil {
		t.Fatal("可推断(参数名 path)的越界写应被拒")
	}
	// 灵敏度反证:同一调用 + 作者显式声明"本工具没有路径参数" → 放行。
	// 说明上面的拒绝确实来自推断/兜底,而不是"任何绝对路径都拒"的粗暴规则。
	declaredNone := def
	declaredNone.PathParamsDeclared = true
	if err := p.CheckToolCallAt(ws, def.Name, fmt.Sprintf(`{"path":%q}`, filepath.Join(outside, "a.txt")), declaredNone); err != nil {
		t.Fatalf("PathParamsDeclared 应跳过推断与兜底: %v", err)
	}
	// ② 推断出的路径参数在工作区内仍放行(加严不等于一刀切)
	if err := p.CheckToolCallAt(ws, def.Name, `{"path":"ok.txt"}`, def); err != nil {
		t.Fatalf("工作区内应放行: %v", err)
	}
	// ② 读类工具名 + 工作区外读:按 read 裁决(工作区外读同样拒,不是"看工具名松一档")
	readDef := sdk.ToolDefinition{Name: "mcp_srv_read_file", InputSchema: def.InputSchema}
	if err := p.CheckToolCallAt(ws, readDef.Name, fmt.Sprintf(`{"path":%q}`, filepath.Join(outside, "a.txt")), readDef); err == nil {
		t.Fatal("读类未声明工具的工作区外读应被拒")
	}

	// ④ 四级全空:确实没有路径面的调用照常放行(不能把 F2 修成"未声明工具一律拒")
	if err := p.CheckPathArgs("mcp_srv_search", `{"query":"hello","limit":20}`); err != nil {
		t.Fatalf("无路径面的调用应放行: %v", err)
	}
	// 参数不是 JSON 对象(模型偶发)→ 不做启发式判定,不得因此把调用打成失败
	if err := p.CheckPathArgs("mcp_srv_search", `not-json`); err != nil {
		t.Fatalf("非 JSON 参数不应被启发式拒: %v", err)
	}
}

// TestCheckToolCallNestedPaths 嵌套路径参数裁决(A1,2026-09-27):
// 未声明工具把路径藏在 `files:[{path:…}]` / `params:{target:…}` 里时,按 schema 推断出的
// `Nested` 路径逐点裁决(此前这两类直接绕过 —— 推断对 array/object 一律跳过)。
func TestCheckToolCallNestedPaths(t *testing.T) {
	withCaseFold(t, false)
	ws := t.TempDir()
	outside := t.TempDir()
	p := DefaultSandbox(ws)

	arrayDef := sdk.ToolDefinition{Name: "mcp_srv_write_files", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"files": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
			}},
		},
	}}
	objDef := sdk.ToolDefinition{Name: "mcp_srv_write_batch", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"params": map[string]any{"type": "object", "properties": map[string]any{
				"target": map[string]any{"type": "string"},
			}},
		},
	}}

	// array of object:逐元素裁决,越界即拒,且文案点名嵌套路径
	err := p.CheckToolCallAt(ws, arrayDef.Name, fmt.Sprintf(`{"files":[{"path":"ok.txt"},{"path":%q}]}`, filepath.Join(outside, "a.txt")), arrayDef)
	if err == nil {
		t.Fatal("files[].path 越界写应被拒")
	}
	if !strings.Contains(err.Error(), "files.*.path") {
		t.Fatalf("错误文案应含嵌套路径名 files.*.path,got: %v", err)
	}
	// 全在区内 → 放行(加严不等于一刀切)
	if err := p.CheckToolCallAt(ws, arrayDef.Name, `{"files":[{"path":"a.txt"},{"path":"b/c.txt"}]}`, arrayDef); err != nil {
		t.Fatalf("区内嵌套路径应放行: %v", err)
	}
	// object 嵌套
	if err := p.CheckToolCallAt(ws, objDef.Name, fmt.Sprintf(`{"params":{"target":%q}}`, filepath.Join(outside, "a.txt")), objDef); err == nil {
		t.Fatal("params.target 越界写应被拒")
	}
	// 嵌套取不到值 = 没有路径面,不是“缺参数”失败(推断的 Optional 恒 true)
	if err := p.CheckToolCallAt(ws, arrayDef.Name, `{"files":[]}`, arrayDef); err != nil {
		t.Fatalf("空数组不应报缺参: %v", err)
	}
	if err := p.CheckToolCallAt(ws, objDef.Name, `{"params":{}}`, objDef); err != nil {
		t.Fatalf("对象里没有目标字段不应报缺参: %v", err)
	}
	// 非字符串命中点跳过(不把调用打成失败)
	if err := p.CheckToolCallAt(ws, arrayDef.Name, `{"files":[{"path":123}]}`, arrayDef); err != nil {
		t.Fatalf("非字符串路径值应跳过而非报错: %v", err)
	}
	// 灵敏度反证:显式声明“无路径参数” → 嵌套推断同样被跳过
	declared := arrayDef
	declared.PathParamsDeclared = true
	if err := p.CheckToolCallAt(ws, declared.Name, fmt.Sprintf(`{"files":[{"path":%q}]}`, filepath.Join(outside, "a.txt")), declared); err != nil {
		t.Fatalf("PathParamsDeclared 应跳过嵌套推断: %v", err)
	}
	// 而显式声明嵌套参数(作者手写)同样生效
	byHand := sdk.ToolDefinition{Name: "my_saver", PathParams: []sdk.PathParam{
		{Arg: "files", Nested: []string{"files", "*", "path"}, Access: sdk.PathWrite, Optional: true},
	}}
	if err := p.CheckToolCallAt(ws, byHand.Name, fmt.Sprintf(`{"files":[{"path":%q}]}`, filepath.Join(outside, "a.txt")), byHand); err == nil {
		t.Fatal("手写 Nested 声明应生效")
	}
}

// TestSniffSkipsExecutorsAndContentArgs 值级兜底的假阳性边界(2026-09-27 自查发现并修):
//
// 修前的兜底只看“值像不像路径”,不看**参数承载什么** —— 于是 `shell{command:"/usr/bin/env node"}`
// 这种正常调用(命令体本身是绝对路径)会被当成写路径拒掉,`web_search{query:"/etc/hosts"}`
// 这种搜索词同理。修后:执行器类工具不给兜底(命令面由 shellpaths.go 的写目标扫描负责),
// 内容/指令类参数名(command/query/prompt/…)跳过。
func TestSniffSkipsExecutorsAndContentArgs(t *testing.T) {
	withCaseFold(t, false)
	ws := t.TempDir()
	p := DefaultSandbox(ws)

	// 执行器类:命令体是绝对路径 = 正常写法(MCP/第三方插件里同名的 run_code/bash 同理)
	for _, c := range []struct{ tool, args string }{
		{"shell", `{"command":"/usr/bin/env python3 -c pass"}`},
		{"run_code", `{"code":"/usr/bin/env python3"}`},
		{"lisp_eval", `{"input":"(/usr/bin/ls)"}`},
		{"bash", `{"command":"/opt/homebrew/bin/node x.js"}`},
	} {
		if err := p.CheckPathArgs(c.tool, c.args); err != nil {
			t.Fatalf("执行器类工具的绝对路径命令不应被当写路径拒(%s): %v", c.tool, err)
		}
	}
	// 内容/指令类参数名:值即使长成绝对路径也不是路径用法
	for _, c := range []struct{ tool, args string }{
		{"web_search", `{"query":"/etc/hosts"}`},
		{"mcp_srv_note", `{"prompt":"解释 /etc/hosts"}`},
		{"mcp_srv_fetch", `{"url":"/docs/intro"}`},
		{"mcp_srv_grep", `{"pattern":"/etc/hosts"}`},
	} {
		if err := p.CheckPathArgs(c.tool, c.args); err != nil {
			t.Fatalf("内容类参数值不应被当路径裁决(%s): %v", c.tool, err)
		}
	}
	// 反过来:非内容名的参数值依旧兜底拦住(不能因为上面放宽就整体失效)
	outside := t.TempDir()
	if err := p.CheckPathArgs("mcp_srv_write_file", fmt.Sprintf(`{"target":%q}`, filepath.Join(outside, "x.txt"))); err == nil {
		t.Fatal("非内容名参数的越界路径仍应被兜底拒")
	}
	// A2(2026-09-27)收紧后的边界:**内容参数名不再豁免凭据面** —— 搜索词是常态,
	// 拿凭据路径当内容参数的值不是。下面两条由“登记的 FN”翻转为“应拒”。
	for _, c := range []struct{ tool, args string }{
		{"mcp_srv_fetch", `{"url":"~/.ssh/id_rsa"}`},
		{"mcp_srv_note", `{"prompt":"~/.aws/credentials"}`},
	} {
		if err := p.CheckPathArgs(c.tool, c.args); err == nil {
			t.Fatalf("内容参数名里的凭据路径应被拒(%s): %v", c.tool, err)
		}
	}
	// 对照:非凭据路径仍不因内容参数名被裁决(不把搜索词/URL 误拦)
	for _, c := range []struct{ tool, args string }{
		{"mcp_srv_fetch", `{"url":"https://example.com/etc/hosts"}`},
		{"mcp_srv_note", `{"prompt":"解释 /usr/local/bin 的用途"}`},
		// 诚实边界(登记的 FN,值级判定只看“整个值一眼是不是路径”):
		// 句子里**嵌着**凭据路径的文本不算路径值 → 不裁决(不按子串做正则扫描,否则误拦面失控)
		{"mcp_srv_note", `{"prompt":"帮我读 ~/.ssh/id_rsa 看看"}`},
	} {
		if err := p.CheckPathArgs(c.tool, c.args); err != nil {
			t.Fatalf("非凭据内容值不应被裁决(%s): %v", c.tool, err)
		}
	}
}

// stubSchemaTool 可带 inputSchema 的工具替身(验证 ② 推断;stubPathTool 只带固定空 schema)。
type stubSchemaTool struct {
	name   string
	schema map[string]any
	called bool
}

func (s *stubSchemaTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: s.name, Description: "stub", InputSchema: s.schema}
}

func (s *stubSchemaTool) Execute(context.Context, string) (any, error) {
	s.called = true
	return map[string]any{"ok": true}, nil
}

// stubProxyTool 代理工具替身(mcp_call 形态:真实工具名 + 内层参数对象)。
type stubProxyTool struct {
	name        string
	targetParam string
	argsParam   string
	called      bool
}

func (s *stubProxyTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:                s.name,
		Description:         "stub proxy",
		InputSchema:         map[string]any{"type": "object"},
		ApprovalTargetParam: s.targetParam,
		ProxyArgsParam:      s.argsParam,
	}
}

func (s *stubProxyTool) Execute(context.Context, string) (any, error) {
	s.called = true
	return map[string]any{"ok": true}, nil
}

// TestGuardProxyAdjudicatesInnerArgs 代理工具按**真实目标工具**裁决内层参数
// (检索模式 mcp_call:审批维度已按真实名匹配,路径维度此前完全没解析内层参数)。
func TestGuardProxyAdjudicatesInnerArgs(t *testing.T) {
	withCaseFold(t, false)
	c := buildTools(t, nil, nil)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	target := &stubSchemaTool{name: "mcp_srv_write_file", schema: map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
	}}
	proxy := &stubProxyTool{name: "mcp_call", targetParam: "name", argsParam: "arguments"}
	tools.Register(target)
	tools.Register(proxy)

	outside := filepath.Join(t.TempDir(), "a.txt")
	// 内层越界写 → 按真实目标工具的推断裁决并 veto(代理工具自身不得执行)
	if res := execTool(t, c, "mcp_call", fmt.Sprintf(`{"name":"mcp_srv_write_file","arguments":{"path":%q}}`, outside)); res.Error == "" {
		t.Fatal("代理调用内层越界写应被 veto")
	}
	if proxy.called {
		t.Fatal("被 veto 的代理工具不应真正执行")
	}
	// 内层工作区内写 → 放行
	if res := execTool(t, c, "mcp_call", `{"name":"mcp_srv_write_file","arguments":{"path":"ok.txt"}}`); res.Error != "" {
		t.Fatalf("内层工作区内写应放行: %+v", res)
	}
	if !proxy.called {
		t.Fatal("放行的代理调用应真正执行")
	}
	// 真实名不存在(名字写错/已卸载)→ 不猜声明,只留值级兜底:内层绝对路径仍拒
	if res := execTool(t, c, "mcp_call", fmt.Sprintf(`{"name":"nope","arguments":{"whatever":%q}}`, outside)); res.Error == "" {
		t.Fatal("真实目标取不到时,内层绝对路径应被值级兜底拒")
	}
	// arguments 传 JSON 字符串(模型另一种常见写法)→ 同样解析后裁决
	innerJSON, merr := json.Marshal(map[string]any{"path": outside})
	if merr != nil {
		t.Fatal(merr)
	}
	outerJSON, oerr := json.Marshal(map[string]any{"name": "mcp_srv_write_file", "arguments": string(innerJSON)})
	if oerr != nil {
		t.Fatal(oerr)
	}
	if res := execTool(t, c, "mcp_call", string(outerJSON)); res.Error == "" {
		t.Fatal("arguments 为 JSON 字符串时也应裁决")
	}
	// 没有内层参数对象 → 不构造路径面,不得凭空拒
	if res := execTool(t, c, "mcp_call", `{"name":"mcp_srv_write_file"}`); res.Error != "" {
		t.Fatalf("无内层参数对象应放行: %+v", res)
	}
}
