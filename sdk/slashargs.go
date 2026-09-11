package sdk

import "strings"

// SplitArgs 斜杠命令参数分词:空白分隔,支持单/双引号包裹与反斜杠转义。
//
// 为什么不用 strings.Fields:/provider set https://x "sk a b" 这类命令需要保留
// 含空格的参数,Fields 会把引号原样留在参数里并把值切成多段(用户可见的数据损坏)。
// 语义刻意比 shell 简单:不展开变量、不处理重定向,未闭合引号按"到串尾"处理
// (命令参数容错优先,不因少一个引号直接失败);引号内的 \" 与 \\ 为字面量。
// 连续空白不产生空参数;空串与纯空白返回 nil。
func SplitArgs(s string) []string {
	var (
		out     []string
		cur     []rune
		started bool
		quote   rune // 0 = 未在引号内
		esc     bool
	)
	for _, r := range s {
		if esc {
			cur = append(cur, r)
			started = true
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			cur = append(cur, r)
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if started {
				out = append(out, string(cur))
				cur = cur[:0]
				started = false
			}
		default:
			cur = append(cur, r)
			started = true
		}
	}
	if esc {
		cur = append(cur, '\\') // 结尾孤立反斜杠:按字面量
	}
	if started {
		out = append(out, string(cur))
	}
	return out
}

// JoinArgs 是 SplitArgs 的逆操作:把参数列表重建为可被 SplitArgs 还原的命令行
// (含空白/引号/反斜杠的参数自动加双引号并转义;空参数写成 "")。
// 用于补全/逐步向导把参数拼回输入框——直接 strings.Join(…, " ") 会让含空格的
// 参数在下次解析时被拆成两段(用户可见的数据损坏)。
func JoinArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "":
			parts = append(parts, `""`)
		case !strings.ContainsAny(a, " \t\n\r\"'\\"):
			parts = append(parts, a)
		default:
			var b strings.Builder
			b.WriteByte('"')
			for _, r := range a {
				if r == '"' || r == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(r)
			}
			b.WriteByte('"')
			parts = append(parts, b.String())
		}
	}
	return strings.Join(parts, " ")
}
