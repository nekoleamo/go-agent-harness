package sdk

import (
	"reflect"
	"testing"
)

// pathparam_test.go 覆盖 2026-09-27 审计 F2 的判据(未声明路径参数的工具怎么裁决):
// 推断(InferPathParams)、读写意图(InferAccess)、值级判据(LooksLikePathValue)。
// 每条用例都写明**它守的是哪个边界**,避免判据被后续"优化"成更松的形态。

// schemaOf 造一个只含 properties 的最小 inputSchema。
func schemaOf(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func TestInferPathParams(t *testing.T) {
	strProp := map[string]any{"type": "string"}

	t.Run("参数名命中 path → 生成声明(Optional 恒 true)", func(t *testing.T) {
		def := ToolDefinition{
			Name:        "mcp_srv_read_file",
			InputSchema: schemaOf(map[string]any{"path": strProp, "query": strProp}),
		}
		got := InferPathParams(def)
		want := []PathParam{{Arg: "path", Access: PathRead, Optional: true}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v want %+v", got, want)
		}
		// 即便 schema 把 path 标成 required,推断也不该制造"缺少参数"硬失败(启发式不负责必填)
		def2 := ToolDefinition{Name: "x", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"path": strProp}, "required": []any{"path"},
		}}
		if g := InferPathParams(def2); len(g) != 1 || !g[0].Optional {
			t.Fatalf("required 的推断项也应为 Optional: %+v", g)
		}
	})

	t.Run("字符串数组 → Many", func(t *testing.T) {
		def := ToolDefinition{Name: "mcp_srv_write_files", InputSchema: schemaOf(map[string]any{
			"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		})}
		got := InferPathParams(def)
		if len(got) != 1 || !got[0].Many || got[0].Access != PathWrite {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("array of object 仍跳过", func(t *testing.T) {
		// items 是 object 但**没有 properties** → 推不出路径字段(与 A1 的 Nested 分支互补);
		// 若平铺成 `files` 声明会因“含非字符串元素”把合法调用打成失败
		def := ToolDefinition{Name: "mcp_srv_write_files", InputSchema: schemaOf(map[string]any{
			"files": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})}
		if got := InferPathParams(def); len(got) != 0 {
			t.Fatalf("array of object 不应推断: %+v", got)
		}
	})

	t.Run("array of object 的路径字段 → Nested 逐元素裁决", func(t *testing.T) {
		// A1(2026-09-27):从“跳过”改为递归一层 —— `files:[{path:…}]` 不再绕过
		def := ToolDefinition{Name: "mcp_srv_write_files", InputSchema: schemaOf(map[string]any{
			"files": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{"path": strProp, "size": strProp},
			}},
		})}
		got := InferPathParams(def)
		if len(got) != 1 || got[0].Arg != "files" || got[0].Nested[0] != "files" || got[0].Nested[2] != "path" {
			t.Fatalf("got %+v", got)
		}
		if got[0].Many {
			t.Fatalf("Nested 形态不应再标 Many(值由路径展开收集): %+v", got[0])
		}
	})

	t.Run("object 的路径字段 → Nested", func(t *testing.T) {
		def := ToolDefinition{Name: "mcp_srv_save", InputSchema: schemaOf(map[string]any{
			"params": map[string]any{"type": "object", "properties": map[string]any{"target": strProp}},
		})}
		got := InferPathParams(def)
		if len(got) != 1 || got[0].Arg != "params" || len(got[0].Nested) != 2 || got[0].Nested[1] != "target" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("array of object 无路径词字段 / 纯 object 无路径词 → 跳过", func(t *testing.T) {
		// 不能平铺成“应为字符串”的声明(会把合法调用打成失败)
		def := ToolDefinition{Name: "mcp_srv_write_files", InputSchema: schemaOf(map[string]any{
			"files": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{"content": strProp, "size": strProp},
			}},
			"meta": map[string]any{"type": "object", "properties": map[string]any{"note": strProp}},
		})}
		if got := InferPathParams(def); len(got) != 0 {
			t.Fatalf("无路径词字段不应推断: %+v", got)
		}
	})

	t.Run("嵌套只递一层(深层留给作者声明)", func(t *testing.T) {
		def := ToolDefinition{Name: "mcp_srv_write", InputSchema: schemaOf(map[string]any{
			"params": map[string]any{"type": "object", "properties": map[string]any{
				"inner": map[string]any{"type": "object", "properties": map[string]any{"path": strProp}},
			}},
		})}
		if got := InferPathParams(def); len(got) != 0 {
			t.Fatalf("二层嵌套不应推断(登记边界): %+v", got)
		}
	})

	t.Run("LookupArgPath:通配展开与取不到", func(t *testing.T) {
		m := map[string]any{
			"files": []any{
				map[string]any{"path": "a.txt"},
				map[string]any{"path": "b.txt", "meta": map[string]any{"x": 1}},
			},
			"params": map[string]any{"target": "t.txt"},
		}
		got := LookupArgPath(m, []string{"files", "*", "path"})
		if len(got) != 2 || got[0] != "a.txt" || got[1] != "b.txt" {
			t.Fatalf("通配应逐元素收集: %+v", got)
		}
		// 数组段名不是 `*` 也展开(展开只会多裁决不会漏)
		if got := LookupArgPath(m, []string{"files", "path"}); len(got) != 2 {
			t.Fatalf("数组应自动展开: %+v", got)
		}
		if got := LookupArgPath(m, []string{"params", "target"}); len(got) != 1 || got[0] != "t.txt" {
			t.Fatalf("对象路径应命中: %+v", got)
		}
		if got := LookupArgPath(m, []string{"params", "nope"}); got != nil {
			t.Fatalf("取不到应返回 nil: %+v", got)
		}
		if got := LookupArgPath(m, nil); got != nil {
			t.Fatalf("空路径应返回 nil: %+v", got)
		}
	})

	t.Run("分词命中与不命中", func(t *testing.T) {
		cases := map[string]bool{
			"file_path": true, "input_file": true, "target_dir": true, "dst_file": true,
			"file_id": true, // 已知误判面:作者声明即解除(见文件头)
			"profile": false, "settings": false, "query": false, "limit": false, "id": false,
		}
		for arg, want := range cases {
			got := len(InferPathParams(ToolDefinition{Name: "t", InputSchema: schemaOf(map[string]any{arg: strProp})})) == 1
			if got != want {
				t.Fatalf("参数名 %q: got %v want %v", arg, got, want)
			}
		}
	})

	t.Run("PathParamsDeclared 跳过推断(作者的解除开关)", func(t *testing.T) {
		def := ToolDefinition{Name: "t", PathParamsDeclared: true,
			InputSchema: schemaOf(map[string]any{"path": strProp})}
		if got := InferPathParams(def); got != nil {
			t.Fatalf("显式声明无路径参数时不应推断: %+v", got)
		}
	})

	t.Run("无 schema / 空 properties → nil", func(t *testing.T) {
		if got := InferPathParams(ToolDefinition{Name: "t"}); got != nil {
			t.Fatalf("无 schema 不应推断: %+v", got)
		}
	})

	t.Run("输出定序(错误文案/日志可断言)", func(t *testing.T) {
		def := ToolDefinition{Name: "mcp_x_write", InputSchema: schemaOf(map[string]any{
			"zeta_path": strProp, "alpha_file": strProp,
		})}
		got := InferPathParams(def)
		if len(got) != 2 || got[0].Arg != "alpha_file" || got[1].Arg != "zeta_path" {
			t.Fatalf("应按下标排序: %+v", got)
		}
	})
}

func TestInferAccess(t *testing.T) {
	cases := map[string]PathAccess{
		"mcp_fs_write_file": PathWrite,
		"save_note":         PathWrite,
		"mcp_srv_move":      PathWrite,
		"mcp_fs_read_file":  PathRead,
		"readFile":          PathRead, // 小驼峰:按驼峰边界切词
		"list_dir":          PathRead,
		"mcp_search":        PathRead,
		"settings":          PathWrite, // 整词匹配:`set` 不是动词,不误命中;推断不明 = write
		"unknown_tool":      PathWrite,
		"":                  PathWrite,
	}
	for name, want := range cases {
		if got := InferAccess(name); got != want {
			t.Fatalf("%q: got %v want %v", name, got, want)
		}
	}
	// 写词优先于读词(同名前缀冲突时取更严)
	if got := InferAccess("read_write_file"); got != PathWrite {
		t.Fatalf("写词应优先: %v", got)
	}
}

func TestLooksLikePathValue(t *testing.T) {
	yes := []string{"~/x", "~", "/etc/hosts", "/c/tmp/x", "../../x", "../x", "./x", `..\x`, `.\x`, "a/../b", "/tmp"}
	no := []string{"", "   ", "notes/todo.md", "a..b", "2026/09/27", "https://x/y", "file:///etc/hosts",
		"plain", "1.2.3", "a/b", "x..y/z"}
	for _, s := range yes {
		if !LooksLikePathValue(s) {
			t.Fatalf("%q 应判为路径形态", s)
		}
	}
	for _, s := range no {
		if LooksLikePathValue(s) {
			t.Fatalf("%q 不应判为路径形态", s)
		}
	}
}

// TestPosixRooted Windows 根相对形态(`\foo`、`/etc/hosts`)在两种语义下都要判对 ——
// 开关化是为了能在非 Windows 机器上跑(真机判据 filepath.IsAbs 翻不动,这里判的是自己的壳)。
func TestPosixRooted(t *testing.T) {
	old := winSemantics
	defer func() { winSemantics = old }()

	winSemantics = false // POSIX 值语义:`\foo` 是普通值(反斜杠只是字符),`/foo` 由 IsAbs 负责
	if posixRooted(`\foo`) {
		t.Fatal("POSIX 语义下 `\\foo` 不是根相对路径")
	}
	if !posixRooted("/foo") {
		t.Fatal("`/foo` 是根相对路径")
	}
	winSemantics = true // Windows:`\foo` 是当前盘根下,`/foo` 是 MSYS 根相对
	if !posixRooted(`\foo`) || !posixRooted("/foo") {
		t.Fatal("Windows 语义下两者都是根相对路径")
	}
	if posixRooted("foo\\bar") {
		t.Fatal("前缀不是根的路径不算根相对")
	}
}

func TestDescribePathParams(t *testing.T) {
	got := DescribePathParams([]PathParam{
		{Arg: "path", Access: PathWrite},
		{Arg: "files", Access: PathRead, Many: true},
	})
	if want := "path(write) files(read,many)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
