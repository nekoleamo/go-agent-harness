// assembly/search 单测:模式门控(全量 vs 代理工具)、命名与兼容、停用/失败跳过、
// 冲突保留先注册、检索打分与截断、mcp_call 路由与错误文案。用替身 Conn,不真起进程。
package mcpbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeConn 替身 MCP 连接(defs 里 Name 已带 mcp_ 前缀,与 *Client 契约一致)。
type fakeConn struct {
	defs   []sdk.ToolDefinition
	calls  []string // 收到的 (raw name + args)
	out    string
	err    error
	closed bool
}

func (f *fakeConn) Definitions() []sdk.ToolDefinition { return f.defs }
func (f *fakeConn) Execute(_ context.Context, name, args string) (string, error) {
	f.calls = append(f.calls, name+" "+args)
	if f.err != nil {
		return "", f.err
	}
	return f.out + "|" + name, nil
}
func (f *fakeConn) Close() { f.closed = true }

// withDial 安装替身连接工厂(按命令名路由到预设连接)。
func withDial(t *testing.T, m map[string]*fakeConn) func() {
	t.Helper()
	old := dialConn
	dialConn = func(command string, _ []string) (Conn, error) {
		if c, ok := m[command]; ok {
			return c, nil
		}
		return nil, errors.New("no such server: " + command)
	}
	return func() { dialConn = old }
}

func def(name, desc string) sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "mcp_" + name, Description: desc, InputSchema: map[string]any{"type": "object"}}
}

func toolNames(tools map[string]sdk.Tool) []string {
	var out []string
	for n := range tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestAssembleDirectModePreservesNaming 默认(direct)= 现状:全量注册 + 命名不变。
func TestAssembleDirectModePreservesNaming(t *testing.T) {
	alpha := &fakeConn{defs: []sdk.ToolDefinition{def("greet", "打招呼"), def("recall", "回忆")}, out: "ok"}
	single := &fakeConn{defs: []sdk.ToolDefinition{def("echo", "回声")}}
	restore := withDial(t, map[string]*fakeConn{"/bin/alpha": alpha, "/bin/single": single})
	defer restore()
	specs := []mcpconfig.Server{
		{Name: "alpha", Command: "/bin/alpha"},
		{Name: "", Command: "/bin/single"}, // 单 server 兼容:不加前缀
	}
	tools, notes, err := Assemble(specs)
	if err != nil {
		t.Fatalf("装配不应失败: %v (notes=%v)", err, notes)
	}
	want := []string{"mcp_alpha_greet", "mcp_alpha_recall", "mcp_echo"}
	if got := toolNames(tools); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("direct 命名应保持现状:\n got %v\nwant %v", got, want)
	}
	// 定义必须原样(名字/描述/schema 都不改)
	d := tools["mcp_alpha_greet"].Definition()
	if d.Name != "mcp_alpha_greet" || d.Description != "打招呼" || d.InputSchema == nil {
		t.Fatalf("定义应原样透出: %+v", d)
	}
	// 调用转发到连接内部名(mcp_greet)
	got, err := tools["mcp_alpha_greet"].Execute(context.Background(), `{"name":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["content"].(string) != "ok|mcp_greet" {
		t.Fatalf("应转发到内部名: %+v", got)
	}
	if len(alpha.calls) != 1 || !strings.Contains(alpha.calls[0], "mcp_greet") {
		t.Fatalf("连接收到的调用: %v", alpha.calls)
	}
}

// TestAssembleSearchModeHidesTools search 模式:工具不进注册表,只暴露两个代理工具。
func TestAssembleSearchModeHidesTools(t *testing.T) {
	big := &fakeConn{defs: []sdk.ToolDefinition{def("greet", "打招呼"), def("recall", "回忆往事")}, out: "ok"}
	restore := withDial(t, map[string]*fakeConn{"/bin/big": big})
	defer restore()
	tools, notes, err := Assemble([]mcpconfig.Server{{Name: "big", Command: "/bin/big", Mode: mcpconfig.ModeSearch}})
	if err != nil {
		t.Fatalf("装配失败: %v (notes=%v)", err, notes)
	}
	got := toolNames(tools)
	if strings.Join(got, ",") != "mcp_call,mcp_search" {
		t.Fatalf("search 模式只应暴露代理工具: %v", got)
	}
	if _, ok := tools["mcp_big_greet"]; ok {
		t.Fatal("search 模式工具不得直接注册")
	}
	// 空 query = 全量清单
	res, err := tools["mcp_search"].Execute(context.Background(), `{"query":""}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["count"].(int) != 2 || m["total"].(int) != 2 {
		t.Fatalf("空查询应返回全部: %+v", m)
	}
	list := m["tools"].([]map[string]any)
	names := []string{list[0]["name"].(string), list[1]["name"].(string)}
	sort.Strings(names)
	if strings.Join(names, ",") != "mcp_big_greet,mcp_big_recall" {
		t.Fatalf("清单名字(search 模式与 direct 同名): %v", names)
	}
	if list[0]["server"] != "big" {
		t.Fatalf("清单应标 server: %+v", list[0])
	}
	// 检索命中后按名调用 → 路由到连接内部名
	hit, _ := tools["mcp_search"].Execute(context.Background(), `{"query":"回忆"}`)
	if hit.(map[string]any)["count"].(int) != 1 {
		t.Fatalf("描述命中: %+v", hit)
	}
	out, _ := tools["mcp_call"].Execute(context.Background(), `{"name":"mcp_big_recall","arguments":{"q":"x"}}`)
	if out.(map[string]any)["content"].(string) != "ok|mcp_recall" {
		t.Fatalf("mcp_call 应路由: %+v", out)
	}
	if len(big.calls) != 1 || big.calls[0] != `mcp_recall {"q":"x"}` {
		t.Fatalf("转发参数: %v", big.calls)
	}
}

// TestAssembleMixedModesAndSkips 混合:direct + search 并存;停用/连接失败跳过且不影响其余。
func TestAssembleMixedModesAndSkips(t *testing.T) {
	off := boolPtr(false)
	d := &fakeConn{defs: []sdk.ToolDefinition{def("one", "直接工具")}}
	s := &fakeConn{defs: []sdk.ToolDefinition{def("two", "搜索工具")}, out: "ok"}
	restore := withDial(t, map[string]*fakeConn{"/bin/d": d, "/bin/s": s})
	defer restore()
	tools, notes, err := Assemble([]mcpconfig.Server{
		{Name: "d", Command: "/bin/d"},
		{Name: "s", Command: "/bin/s", Mode: mcpconfig.ModeSearch},
		{Name: "off", Command: "/bin/off", Enabled: off},
		{Name: "bad", Command: "/bin/bad"},
	})
	if err != nil {
		t.Fatalf("部分失败不应整体失败: %v (notes=%v)", err, notes)
	}
	got := toolNames(tools)
	if strings.Join(got, ",") != "mcp_call,mcp_d_one,mcp_search" {
		t.Fatalf("混合模式: %v", got)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "已停用") || !strings.Contains(joined, "连接失败") {
		t.Fatalf("停用/失败都应有提示: %v", notes)
	}
	// 停用的 server 不得被连接(未 dial)
	if offConn, ok := dialConn("/bin/off", nil); ok == nil {
		_ = offConn
		t.Fatal("停用的 server 不应被连接")
	}
}

// TestAssembleAllUnavailable 全部不可用 = 显式错误(不静默空转)。
func TestAssembleAllUnavailable(t *testing.T) {
	restore := withDial(t, nil)
	defer restore()
	off := boolPtr(false)
	tools, notes, err := Assemble([]mcpconfig.Server{{Name: "x", Command: "/bin/x", Enabled: off}})
	if err == nil || tools != nil {
		t.Fatalf("应显式报错: %v %v", tools, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "已停用") {
		t.Fatalf("错误信息应含停用原因: %v", notes)
	}
	// 连接失败同样报错
	if _, _, err := Assemble([]mcpconfig.Server{{Name: "y", Command: "/bin/y"}}); err == nil {
		t.Fatal("全部连接失败应报错")
	}
}

// TestAssembleClosesConnsOnFailure 装配失败必须关掉已建连接(不留孤儿 MCP 进程)。
func TestAssembleClosesConnsOnFailure(t *testing.T) {
	ok := &fakeConn{defs: []sdk.ToolDefinition{def("a", "x")}}
	empty := &fakeConn{defs: nil} // 连上但零工具
	restore := withDial(t, map[string]*fakeConn{"/bin/ok": ok, "/bin/empty": empty})
	defer restore()

	// 成功路径:连接必须保活(插件生命周期内一直用)
	if _, _, err := Assemble([]mcpconfig.Server{{Name: "ok", Command: "/bin/ok"}}); err != nil {
		t.Fatalf("有可用工具不应失败: %v", err)
	}
	if ok.closed {
		t.Fatal("成功路径不得关闭连接")
	}
	// 失败路径:唯一 server 零工具 → 无可用工具 → 连接须关闭
	tools, _, err := Assemble([]mcpconfig.Server{{Name: "empty", Command: "/bin/empty"}})
	if err == nil || tools != nil {
		t.Fatalf("零工具应报错: %v", err)
	}
	if !empty.closed {
		t.Fatal("失败路径应关闭已建连接")
	}
}

// TestAssembleDuplicateToolNameKeepsFirst 冲突保留先注册并提示(历史行为)。
func TestAssembleDuplicateToolNameKeepsFirst(t *testing.T) {
	// 带 server 名的条目工具名必带前缀,不同 server 不会撞名;冲突只会出现在
	// 无名(单 server 兼容)条目之间 —— 用两条匿名 server 构造。
	a := &fakeConn{defs: []sdk.ToolDefinition{def("dup", "来自 a")}}
	b := &fakeConn{defs: []sdk.ToolDefinition{def("dup", "来自 b")}}
	restore := withDial(t, map[string]*fakeConn{"/bin/a": a, "/bin/b": b})
	defer restore()
	tools, notes, err := Assemble([]mcpconfig.Server{{Name: "", Command: "/bin/a"}, {Name: "", Command: "/bin/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools["mcp_dup"].Definition().Description != "来自 a" {
		t.Fatalf("同名保留先注册: %v", toolNames(tools))
	}
	if !strings.Contains(strings.Join(notes, "\n"), "工具名冲突") {
		t.Fatalf("应有冲突提示: %v", notes)
	}
}

// TestSearchRankingAndLimit 打分:名等值 > 名前缀 > 名包含 > 描述;多词 AND;limit 截断。
func TestSearchRankingAndLimit(t *testing.T) {
	ix := &searchIndex{}
	ix.add(searchEntry{display: "mcp_s_recall", desc: "回忆"})        // 名等值 recall? 否(name 是全名)
	ix.add(searchEntry{display: "mcp_s_recall_all", desc: "全部回忆"})  // 名含 recall
	ix.add(searchEntry{display: "mcp_s_memo", desc: "recall 相关描述"}) // 仅描述命中
	if ix.len() != 3 {
		t.Fatalf("索引长度: %d", ix.len())
	}
	hits, total := ix.search("recall", 10)
	if total != 3 || len(hits) != 3 {
		t.Fatalf("三条都应命中: %v/%d", hits, total)
	}
	if hits[0].display != "mcp_s_recall" || hits[2].display != "mcp_s_memo" {
		t.Fatalf("排序应名优先于描述: %v %v %v", hits[0].display, hits[1].display, hits[2].display)
	}
	// 名等值(带前缀名)最高分
	eq, _ := ix.search("mcp_s_recall", 10)
	if eq[0].display != "mcp_s_recall" {
		t.Fatalf("名等值应最高: %v", eq[0].display)
	}
	// 多词 AND:两词都要命中
	and, n := ix.search("全部 回忆", 10)
	if n != 1 || and[0].display != "mcp_s_recall_all" {
		t.Fatalf("多词 AND(两词都要命中): %v(%d)", and, n)
	}
	// limit 截断(limit=1 → 只回 1 条,total 仍为命中总数)
	lim, n2 := ix.search("recall", 1)
	if len(lim) != 1 || n2 != 3 {
		t.Fatalf("limit 截断: %v(%d)", lim, n2)
	}
	// limit 缺省/超上限/非法
	if _, n3 := ix.search("", 0); n3 != 3 {
		t.Fatalf("空查询应全量: %d", n3)
	}
	if got, _ := ix.search("recall", 10_000); len(got) != 3 {
		t.Fatalf("超上限不报错: %v", got)
	}
	// 未命中
	if hits, n := ix.search("不存在的东西", 5); n != 0 || len(hits) != 0 {
		t.Fatalf("未命中应为空: %v %d", hits, n)
	}
}

// TestCallToolArgumentsForms mcp_call 参数宽容(对象/字符串/缺省/非法)与错误文案。
func TestCallToolArgumentsForms(t *testing.T) {
	c := &fakeConn{defs: []sdk.ToolDefinition{def("go", "跑")}, out: "ok"}
	restore := withDial(t, map[string]*fakeConn{"/bin/c": c})
	defer restore()
	tools, _, err := Assemble([]mcpconfig.Server{{Name: "c", Command: "/bin/c", Mode: mcpconfig.ModeSearch}})
	if err != nil {
		t.Fatal(err)
	}
	call := tools["mcp_call"]
	// 对象参数
	if _, err := call.Execute(context.Background(), `{"name":"mcp_c_go","arguments":{"a":1}}`); err != nil {
		t.Fatal(err)
	}
	// 字符串参数(模型常见写法)
	if _, err := call.Execute(context.Background(), `{"name":"mcp_c_go","arguments":"{\"b\":2}"}`); err != nil {
		t.Fatal(err)
	}
	// 缺省参数 → {}
	res, _ := call.Execute(context.Background(), `{"name":"mcp_c_go"}`)
	if res.(map[string]any)["content"].(string) != "ok|mcp_go" {
		t.Fatalf("缺省参数应可调: %+v", res)
	}
	if len(c.calls) != 3 || c.calls[2] != "mcp_go {}" {
		t.Fatalf("参数形态: %v", c.calls)
	}
	// 未知名:给可操作提示;大小写近似给提示
	bad, _ := call.Execute(context.Background(), `{"name":"mcp_c_gone"}`)
	if !strings.Contains(bad.(map[string]any)["error"].(string), "mcp_search") {
		t.Fatalf("未知名应有引导: %+v", bad)
	}
	caseIssue, _ := call.Execute(context.Background(), `{"name":"MCP_C_GO"}`)
	if !strings.Contains(caseIssue.(map[string]any)["error"].(string), "是 想调") &&
		!strings.Contains(caseIssue.(map[string]any)["error"].(string), "mcp_c_go") {
		t.Fatalf("大小写不同应给近似提示: %+v", caseIssue)
	}
	// 缺 name / 非法 JSON
	noName, _ := call.Execute(context.Background(), `{}`)
	if !strings.Contains(noName.(map[string]any)["error"].(string), "缺少 name") {
		t.Fatalf("缺 name 应报错: %+v", noName)
	}
	badJSON, _ := call.Execute(context.Background(), `not-json`)
	if !strings.Contains(badJSON.(map[string]any)["error"].(string), "解析失败") {
		t.Fatalf("非法 JSON 应报错: %+v", badJSON)
	}
	// 后端错误包装
	c.err = errors.New("server 崩了")
	failed, _ := call.Execute(context.Background(), `{"name":"mcp_c_go"}`)
	if !strings.Contains(failed.(map[string]any)["error"].(string), "MCP 调用失败: server 崩了") {
		t.Fatalf("错误应包装: %+v", failed)
	}
}

// TestSearchToolTolerantArgs mcp_search 容忍非 JSON 参数(裸检索词)。
func TestSearchToolTolerantArgs(t *testing.T) {
	c := &fakeConn{defs: []sdk.ToolDefinition{def("recall", "回忆"), def("memo", "备忘")}}
	restore := withDial(t, map[string]*fakeConn{"/bin/c": c})
	defer restore()
	tools, _, err := Assemble([]mcpconfig.Server{{Name: "c", Command: "/bin/c", Mode: mcpconfig.ModeSearch}})
	if err != nil {
		t.Fatal(err)
	}
	st := tools["mcp_search"]
	// 裸词
	res, err := st.Execute(context.Background(), "recall")
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["count"].(int) != 1 {
		t.Fatalf("裸词应可检索: %+v", res)
	}
	// 空字符串参数
	res2, err := st.Execute(context.Background(), "")
	if err != nil || res2.(map[string]any)["count"].(int) != 2 {
		t.Fatalf("空参数应全量: %+v %v", res2, err)
	}
	// 未命中提示
	res3, _ := st.Execute(context.Background(), `{"query":"zzz"}`)
	if res3.(map[string]any)["total"].(int) != 0 || !strings.Contains(res3.(map[string]any)["hint"].(string), "留空") {
		t.Fatalf("未命中提示: %+v", res3)
	}
	// 截断标记
	res4, _ := st.Execute(context.Background(), `{"query":"","limit":1}`)
	if res4.(map[string]any)["truncated"] != true || res4.(map[string]any)["count"].(int) != 1 {
		t.Fatalf("截断标记: %+v", res4)
	}
}

// TestToolDefinitionsHaveSchema 两个代理工具的 JSON schema 必须完整(前端/模型依赖)。
func TestToolDefinitionsHaveSchema(t *testing.T) {
	ix := &searchIndex{}
	ix.add(searchEntry{display: "mcp_x_y", desc: "d", conn: &fakeConn{}, raw: "mcp_y"})
	for name, tool := range map[string]sdk.Tool{"mcp_search": newSearchTool(ix), "mcp_call": newCallTool(ix)} {
		d := tool.Definition()
		if d.Name != name || d.Description == "" {
			t.Fatalf("%s 定义不全: %+v", name, d)
		}
		raw, err := json.Marshal(d.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Type       string                    `json:"type"`
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" || len(schema.Properties) == 0 || len(schema.Required) == 0 {
			t.Fatalf("%s schema 不完整: %s", name, raw)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
