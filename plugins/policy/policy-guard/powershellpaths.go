package policyguard

// PowerShell 命令的写目标扫描(Windows 侧执行器,NOND-W1b)。
//
// 为什么需要一份**独立**的扫描器而不是复用 `shellCmdPaths`:POSIX 那一套认的是
// `>` / `cp` / `mv` / `tee` / `sed -i` 这些词法;PowerShell 的写动作长成
// `Set-Content C:\x\y` / `Remove-Item` / `New-Item`,再叠一层 `-Path`/`-LiteralPath`
// 参数与 cmdlet 名不区分大小写。拿 POSIX 扫描器去扫 PowerShell,结果是**把裸词认成路径、
// 却判不出它是写**(读侧照样会裁凭据)—— 而那恰恰是最危险的一种失败:看起来在管写,
// 实际写一个都没拦住。
//
// 口径与 POSIX 那一支保持一致(这是刻意的,见下):
//   - **写动词取第一个非旗标参数**(PowerShell 的位置约定);`-Path`/`-LiteralPath`/
//     `-Destination`/`-Filter`/`-Include`/`-Exclude` 这些**不是**目标,旗标也不当路径;
//   - 含 `$` 变量或子表达式的写目标 → `Unresolvable`(与 shell 同一处置:
//     workspace-write/read-only 下直接拒,full-access 下留给派生审批面);
//   - 读动词(Get-*/Test-*/Select-*/Measure-*/Write-Output/…)**不产��**写目标;
//     Read-* 若带路径则作为读目标返回(读侧同样要裁凭据路径,与 shell 一致)。
//
// **边界(诚实登记)**:PowerShell 的写面**远大于**这里的动词表 —— 脚本块、
// .NET 方法调用、`Invoke-Expression`、自定义函数、模块都能写文件,而词法扫描看不见它们。
// 这类命令按 `Unresolvable` 的同一纪律处置:**要求显式 full 档或用户确认**,而不是
// 当成「没有写目标」放过去。内核层是真正的兜底(macOS/Linux 有 seatbelt/Landlock);
// Windows 上**没有**内核层,所以这条纪律是 Windows 侧唯一的一道,不能松。

import (
	"strings"
)

// psWriteVerbs:第一个非旗标参数是**写目标**的 cmdlet。
// 只收「破坏性/落盘」类里高频的那几个 —— 宁少勿滥:多收会把正常命令全推去确认。
var psWriteVerbs = map[string]bool{
	"set-content":       true, // > 与 >> 的等价物(覆盖全文件)
	"add-content":       true, // >> 的等价物
	"out-file":          true,
	"new-item":          true,
	"remove-item":       true,
	"rename-item":       true,
	"clear-content":     true,
	"copy-item":         true,
	"move-item":         true,
	"export-csv":        true,
	"export-clixml":     true,
	"invoke-webrequest": true, // -OutFile 的形态
}

// psReadVerbs:带路径但是**读**。用于凭据路径裁剪(与 shell 的读侧同一处置)。
var psReadVerbs = map[string]bool{
	"get-content": true,
	"get-item":    true,
	"test-path":   true,
	"read-all":    true, // -Raw 变体
}

// 旗标分两类,处理方式各不相同 —— 混在一张表里就会出现「把真正的路径当成旗标的值吃掉」
// 这类错(初版就有:`-Path C:\a\b.txt` 里的目标被当旗标值跳过,写目标整个丢失)。
var (
	// psTargetFlags 是「目标标记」:它后面的 token **就是**路径(或多个路径)。
	psTargetFlags = map[string]bool{
		"-path": true, "-literalpath": true, "-destination": true, "-destpath": true,
	}
	// 「不带值的旗标」**不需要**一张表:两个取值函数里其余旗标一律「只跳过自己」,
	// 这正是无值旗标要的语义(初版为此单列一张表,staticcheck 立刻指出它从未被读 ——
	// 死表比没有表更糟,它看起来像是被照顾到了)。
	// psValueFlags 带值:跳过它**和**它的值(否则值会被当成路径,凭空多出一条裁决)。
	psValueFlags = map[string]bool{
		"-filter": true, "-include": true, "-exclude": true, "-encoding": true,
		"-delimiter": true, "-separator": true, "-type": true, "-value": true,
		"-itemtype": true, "-erroraction": true, "-warningaction": true,
		"-informationaction": true, "-credential": true, "-timeout": true,
		"-port": true, "-index": true, "-count": true, "-start": true,
		"-maximum": true, "-minimum": true, "-replace": true,
	}
)

// powershellCmdPaths 扫描一条 PowerShell 命令,返回写/读路径操作数(按出现顺序去重)。
func powershellCmdPaths(cmd string) []shellPath {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	var out []shellPath
	seen := map[string]bool{}
	add := func(raw string, write bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		key := raw + "|" + map[bool]string{true: "w", false: "r"}[write]
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, shellPath{
			Path:         raw,
			Write:        write,
			Unresolvable: psUnresolvable(raw),
		})
	}
	for _, seg := range psStatements(cmd) {
		// 重定向:`>` `>>` `2>` `3>` `2>&1`(`&1`/`&2` 不是路径,跳过)
		if target, ok := psRedirectTarget(seg); ok {
			add(target, true)
			continue
		}
		words := psTokens(seg)
		if len(words) == 0 {
			continue
		}
		// 跳过前导的 `&` / `.` / `Invoke-Expression` 之类的调用前缀,取第一个像 cmdlet 的词
		verb := ""
		rest := words
		for i, w := range words {
			c := strings.ToLower(strings.TrimPrefix(w, "&"))
			// 模块限定名(`Microsoft.PowerShell.Management\Remove-Item`)取最后一段 ——
			// 真实的 PowerShell 里这是最常见的写法之一,不剥就认不出动词。
			if k := strings.LastIndexAny(c, `\/`); k >= 0 {
				c = c[k+1:]
			}
			if c == "" || c == "." || strings.HasPrefix(c, "-") {
				continue
			}
			verb = c
			rest = words[i+1:]
			break
		}
		if verb == "" {
			continue
		}
		// Invoke-WebRequest 的第一个参数是 **URL**,不是路径:按位置取会得到一个
		// 「写 URL」的假裁决。真正的落盘参数是 -OutFile / -InFile。
		if verb == "invoke-webrequest" {
			if p, ok := psFlagValue(rest, "-outfile", "-infile"); ok {
				add(p, true)
			}
			continue
		}
		switch {
		case psWriteVerbs[verb]:
			// Copy-Item/Move-Item 的**最后一个**路径参数才是写目标(第一个是读源)——
			// 取第一个会把源文件报成写目标,而真正的目标反而漏掉(方向反了:该拒的没拒)。
			if psLastArgIsTarget[verb] {
				if p, ok := psLastPathArg(rest); ok {
					add(p, true)
				}
			} else if p, ok := psFirstPathArg(rest); ok {
				add(p, true)
			}
		case psReadVerbs[verb]:
			if p, ok := psFirstPathArg(rest); ok {
				add(p, false)
			}
		}
	}
	return out
}

// psLastArgIsTarget 这些写动词的**最后一个**路径参数才是目标(源参数是读)。
// rename-item 不在其中:它的参数是「被改名的项(路径)+ 新名字(不是路径)」,
// 取最后一个会把新名字当成写目标。
var psLastArgIsTarget = map[string]bool{
	"copy-item": true, "move-item": true,
}

// psStatements 按 `;` / 换行 / 管道 `|` 粗切语句(管道右侧的 cmdlet 自成一条)。
// 不做引号感知的完整解析:切错了的后果是「多切/少切一段」,而每一段仍要过动词表,
// 少认一个动词的后果是那条写目标漏网 —— 所以宁可多切(重复由 seen 去重)。
func psStatements(cmd string) []string {
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	var out []string
	for _, line := range strings.Split(cmd, "\n") {
		// 管道两侧各自成段:`Get-ChildItem | Out-File C:\list.txt` 的写动作在右侧,
		// 不切的话左边的读动词会把整段判成「读」,写目标整个漏掉。
		for _, byPipe := range strings.Split(line, "|") {
			for _, bySemi := range strings.Split(byPipe, ";") {
				if strings.TrimSpace(bySemi) == "" {
					continue
				}
				out = append(out, bySemi)
			}
		}
	}
	return out
}

// psRedirectTarget 取重定向的目标(`>` `>>` `N>` 之后到行尾)。
func psRedirectTarget(seg string) (string, bool) {
	// 逐个字符找 "N>" / ">>"(跳过 "2>&1" 这类 fd 复制)
	for i := 0; i < len(seg); i++ {
		if seg[i] != '>' {
			continue
		}
		j := i
		// 左边的数字是 fd(1-9),不是目标
		for j > 0 && seg[j-1] >= '0' && seg[j-1] <= '9' {
			j--
		}
		// j 指向第一个 '>'
		rest := seg[i+1:]
		rest = strings.TrimPrefix(rest, ">") // >>
		if rest == "" {
			return "", false
		}
		if strings.HasPrefix(rest, "&") { // 2>&1 / &>1:不是路径
			return "", false
		}
		// 目标可能带引号 —— 由 psTokens 负责(它引号感知,这里再解一次就是重复实现;
		// 初版两处都写,后来把重定向这一处删了,免得两套引号规则漂移)。
		fields := psTokens(rest)
		if len(fields) == 0 {
			return "", false
		}
		return psUnquote(fields[0]), true
	}
	return "", false
}

// psFirstPathArg 取第一个「像路径的参数」。
//
// 三类旗标(见上面的三张表):目标标记后面**就是**目标;带值旗标要连带跳过它的值;
// 无值旗标只跳自己。引号包裹的 token 取引号内内容(`-LiteralPath 'C:\my file.txt'`)。
func psFirstPathArg(words []string) (string, bool) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "" {
			continue
		}
		if strings.HasPrefix(w, "-") {
			lw := strings.ToLower(w)
			if psTargetFlags[lw] {
				// 后面紧跟另一个旗标 = 只给了标记没给值(没有目标);把 `-Force`
				// 当成路径会是**凭空多出一条裁决**(凭空多出来的裁决 = 误拒)。
				if i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
					return psUnquote(words[i+1]), true
				}
				return "", false
			}
			if psValueFlags[lw] {
				if i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
					i++ // 连带跳过它的值
				}
			}
			continue // 其余旗标(含未知旗标):只跳自己
		}
		return psUnquote(w), true
	}
	return "", false
}

// psFlagValue 取指定旗标(不区分大小写)后面的值。
func psFlagValue(words []string, flags ...string) (string, bool) {
	for i := 0; i < len(words); i++ {
		lw := strings.ToLower(words[i])
		for _, f := range flags {
			if lw == f && i+1 < len(words) {
				return psUnquote(words[i+1]), true
			}
		}
	}
	return "", false
}

// psLastPathArg 取最后一个「像路径的参数」(Copy/Move/Rename 的目标是最后一个)。
func psLastPathArg(words []string) (string, bool) {
	found := ""
	ok := false
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "" {
			continue
		}
		if strings.HasPrefix(w, "-") {
			lw := strings.ToLower(w)
			if psTargetFlags[lw] {
				if i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
					found, ok = psUnquote(words[i+1]), true
					i++
				}
				continue
			}
			if psValueFlags[lw] && i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
				i++
			}
			continue
		}
		found, ok = psUnquote(w), true
	}
	return found, ok
}

// psTokens 引号感知分词:引号内的空格**不是**分隔符
// (`-LiteralPath 'C:\my file.txt'` 用 strings.Fields 会被切成 `'C:\my` 与 `file.txt'`)。
func psTokens(seg string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// psUnquote 去掉成对引号(`'…'` / `"…"`);引号没闭合则原样返回(交给上层判成不可裁决)。
func psUnquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// psUnresolvable 目标是否不可靠解析(含 `$` 变量 / 子表达式 / 通配 / 调用表达式)。
func psUnresolvable(raw string) bool {
	// 引号没闭合也要算不可裁决:半截路径当正常路径放过去,等于给了一条绕过口子
	// (`-LiteralPath "C: b\c.txt` 会被切碎,碎片本身不是合法路径)。
	if strings.ContainsAny(raw, `"'`) {
		return true
	}
	return strings.ContainsAny(raw, "$@`") || strings.Contains(raw, "(") ||
		strings.ContainsAny(raw, "*?")
}
