package sdk

import (
	"path/filepath"
	"sort"
	"strings"
)

// 本文件是**路径参数判据的单一事实源**(2026-09-27 安全审计 F2 修复)。
//
// 为什么需要它:工具的路径参数声明(sdk.ToolDefinition.PathParams)是**自愿**的,而未声明的工具
// 在宿主 pre-execute 里此前直接放行(fail-open)—— 第三方插件工具与 MCP 工具(mcp-bridge 造的
// ToolDefinition 不带 PathParams)因此完全不受路径沙箱约束:`mcp_<server>_read_file{path:"~/.ssh/id_rsa"}`
// 能读凭据(内容进模型上下文),`save_note{target:"/etc/hosts"}` 能越界写(写侧内核层只包 shell 子进程,
// 不管插件进程自己写)。这里给出"未声明时怎么办"的判据,供两处共用:
//
//   - policy-guard 裁决:推断(`InferPathParams`)→ 值级兜底(`LooksLikePathValue`),见 sandbox.go;
//   - host-tools 注册期提示:让作者知道自己的工具进了"推断档"。
//
// 三条判据都刻意**保守**:宁可把不是路径的值误判成路径(可被作者一条声明解除,错误文案里写明怎么解除),
// 也不放过一眼就是路径的值。与 sdk/credentialpath.go 同思路:同一套判据被多处使用就必须只有一份。
//
// 注意本文件只提供**判据**:裁决顺序、错误措辞、放行与否仍由消费方决定。

// pathArgWords 参数名里出现即视为"承载路径"的词(按分隔符切词后**整词**匹配,
// 故 `input_file`/`file_path`/`target_dir` 命中,`profile`/`settings` 不命中)。
//
// 保守到不含 from/to/input/output/name 这类泛词;`file_id` 这类会误命中 —— 作者显式声明
// `PathParams` 或 `PathParamsDeclared: true` 即可解除。
var pathArgWords = map[string]bool{
	"path": true, "paths": true,
	"file": true, "files": true, "filepath": true, "filename": true,
	"dir": true, "dirs": true, "directory": true, "directories": true, "folder": true, "folders": true,
	"root": true, "cwd": true, "workdir": true,
	"target": true, "targets": true, "dest": true, "destination": true,
	"source": true, "sources": true, "src": true, "srcs": true,
}

// writeVerbs 工具名里的写类动词(切词后整词匹配)。派生自各工具命名惯例,不求穷尽:
// 漏掉的动词 → 落到 InferAccess 的"推断不明 = write"兜底,仍从严。
var writeVerbs = map[string]bool{
	"write": true, "save": true, "create": true, "append": true, "edit": true, "patch": true,
	"delete": true, "remove": true, "rm": true, "move": true, "mv": true, "copy": true, "cp": true,
	"mkdir": true, "rename": true, "touch": true, "import": true, "upload": true, "export": true,
	"download": true, "dump": true, "put": true, "install": true,
}

// readVerbs 工具名里的读类动词。
var readVerbs = map[string]bool{
	"read": true, "load": true, "open": true, "list": true, "ls": true, "stat": true,
	"cat": true, "head": true, "tail": true, "grep": true, "find": true, "search": true,
	"glob": true, "tree": true, "scan": true, "inspect": true, "show": true, "get": true,
	"view": true, "diff": true, "parse": true,
}

// InferPathParams 从**未声明**的工具定义推断路径参数(调用方须先确认 def.PathParams 为空)。
//
// 判据取输入 schema 的 **顶层** `properties`,并**递归一层**到嵌套容器(2026-09-27 加):
//   - 顶层参数名切词后命中 pathArgWords → 生成一条声明(`Optional` 恒 true);
//   - `type: array` 且 `items.properties` 里有路径词字段(`files:[{path:…}]`)→ 按
//     `Nested: [name,"*",子字段]` 生成(逐元素裁决,不再因“含非字符串元素”跳过);
//   - `type: object` 且 `properties` 里有路径词字段(`{"params":{"path":…}}`)→ `Nested: [name,子字段]`。
//
// 三层保守选择:
//   - Optional 一律 true:推断是启发式,不该凭空把调用打成“缺少参数”失败;
//   - 嵌套只**一层**(再深不推):深了误判面陡增,留给作者显式声明;
//   - 数组 items 非 string 且推不出嵌套路径 → 跳过(硬判会因“非字符串元素”把合法调用打成失败)。
func InferPathParams(def ToolDefinition) []PathParam {
	if def.PathParamsDeclared {
		return nil // 作者已显式确认"本工具没有承载路径的参数"
	}
	props, _ := def.InputSchema["properties"].(map[string]any)
	access := InferAccess(def.Name)
	var out []PathParam
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		typ, _ := prop["type"].(string)
		switch typ {
		case "array", "object":
			// 先看嵌套:外层名是不是路径词都不影响子字段命中
			if subs := nestedPathFields(prop); len(subs) > 0 {
				for _, s := range subs {
					p := PathParam{Arg: name, Access: access, Optional: true}
					if typ == "array" {
						p.Nested = []string{name, "*", s}
					} else {
						p.Nested = []string{name, s}
					}
					out = append(out, p)
				}
				continue
			}
			if typ == "object" {
				continue // 对象但推不出路径字段:不做平铺声明(会把合法调用判成“应为字符串”)
			}
			if !hasPathWord(name) {
				continue
			}
			// 字符串数组(items 非 string 的已在上面被嵌套/跳过分支处理)
			if items, ok := prop["items"].(map[string]any); ok {
				if it, _ := items["type"].(string); it != "" && it != "string" {
					continue
				}
			}
			out = append(out, PathParam{Arg: name, Access: access, Many: true, Optional: true})
		default:
			if !hasPathWord(name) {
				continue
			}
			out = append(out, PathParam{Arg: name, Access: access, Optional: true})
		}
	}
	// map 遍历无序 → 定序输出(错误文案与日志要稳定可断言)
	sort.Slice(out, func(i, j int) bool { return pathParamKey(out[i]) < pathParamKey(out[j]) })
	return out
}

// nestedPathFields 取嵌套容器的路径词字段名(array 看 items.properties,object 看 properties)。
func nestedPathFields(prop map[string]any) []string {
	src, _ := prop["properties"].(map[string]any)
	if src == nil {
		if items, ok := prop["items"].(map[string]any); ok {
			src, _ = items["properties"].(map[string]any)
		}
	}
	var out []string
	for n := range src {
		if hasPathWord(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// LookupArgPath 按路径段从参数对象取值(Nested 声明的执行侧,供裁决方复用)。
//
// 段语义:map 按字段名进出;遇数组**一律展开**(段名 `*` 只是书写惯例,展开只会多裁决不会漏);
// 命中点不是字符串时原样返回(是否“应为字符串”由裁决方决定,不在这里报错)。
// 任一段取不到 → 返回 nil(调用方可据此走 Optional 跳过或报缺参)。
func LookupArgPath(m map[string]any, path []string) []any {
	if len(path) == 0 {
		return nil
	}
	v, ok := m[path[0]]
	if !ok {
		return nil
	}
	return lookupPathRest(v, path[1:])
}

func lookupPathRest(v any, rest []string) []any {
	if len(rest) == 0 {
		return []any{v}
	}
	switch t := v.(type) {
	case map[string]any:
		nv, ok := t[rest[0]]
		if !ok {
			return nil
		}
		return lookupPathRest(nv, rest[1:])
	case []any:
		r := rest
		if r[0] == "*" {
			r = rest[1:]
		}
		var out []any
		for _, item := range t {
			out = append(out, lookupPathRest(item, r)...)
		}
		return out
	}
	return nil
}

// pathParamKey 声明排序/去重键(Nested 不同但 Arg 相同的两条不能混序)。
func pathParamKey(p PathParam) string {
	return p.Arg + "|" + strings.Join(p.Nested, ".")
}

// InferAccess 工具名 → 读写意图。写词优先;**推断不明 = write**(更严的一侧)。
//
// 为什么不明时取 write:猜成 read 会让写类工具在 read-only 档下把文件写进工作区(档位契约被打破),
// 猜成 write 只是拒掉一次"可解除"的调用(作者声明 Access 或改名即可)。立场:猜错宁可拒。
func InferAccess(toolName string) PathAccess {
	words := splitWords(toolName)
	for _, w := range words {
		if writeVerbs[w] {
			return PathWrite
		}
	}
	for _, w := range words {
		if readVerbs[w] {
			return PathRead
		}
	}
	return PathWrite
}

// LooksLikePathValue 值级判据:一眼能看出是路径的形态。
//
// 与 sdk.LooksLikeURLPath 互补(URL 先排除):模型把 URL 交给写工具是另一类真机事故,
// 不该在这里被当成路径二次裁决。
//
// 只认"越界/绝对"形态 —— 相对普通名(`notes/todo.md`)不判:它经工具的 cwd 解析仍在工作区内,
// 误判的代价(打断合法调用)大于收益。
func LooksLikePathValue(s string) bool {
	v := strings.TrimSpace(s)
	if v == "" || LooksLikeURLPath(v) {
		return false
	}
	if v == "~" || strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		return true
	}
	if filepath.IsAbs(v) { // /etc/hosts、/c/tmp(MSYS);Windows 盘符形态见 Note
		return true
	}
	if strings.HasPrefix(v, "./") || strings.HasPrefix(v, `.\`) || strings.HasPrefix(v, "../") || strings.HasPrefix(v, `..\`) {
		return true
	}
	// `..` 必须**按段**判:否则 `a..b` 这种普通值会被误判成路径
	for _, seg := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// DescribePathParams 把声明渲染成一行(注册期日志/错误文案用)。
func DescribePathParams(params []PathParam) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		s := p.Arg
		if len(p.Nested) > 0 {
			s = strings.Join(p.Nested, ".") // 嵌套声明按实际取值路径展示
		}
		s += "(" + string(p.Access)
		if p.Many {
			s += ",many"
		}
		s += ")"
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// hasPathWord 参数名是否含"路径词"(切词后整词匹配)。
func hasPathWord(name string) bool {
	for _, w := range splitWords(name) {
		if pathArgWords[w] {
			return true
		}
	}
	return false
}

// splitWords 工具/参数名 → 小写词序列。按非字母数字切分,并在小驼峰边界插分隔符
// (MCP server 的工具名常见 `readFile`),避免用 strings.Contains 造成 `readme` 命中 `read`。
func splitWords(s string) []string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.FieldsFunc(strings.ToLower(b.String()), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}
