// shell 命令路径裁决(R10 ①):workspace-write 档下约束 shell 命令的**显式写目标**。
//
// 背景:路径沙箱此前只裁决 file_* 类工具的参数(见 sandbox.go CheckToolCall),
// shell 命令文本可写任意路径,只能靠危险模式 + 审批档兜底。shell 没有结构化 AST,
// 本文件用保守的**显式目标扫描**把"能可靠指认的写"纳入沙箱:
//
//   - 重定向目标(`>` `>>` `&>` `<>`、fd 前缀 `2>`);
//   - 写命令的路径操作数(rm/rmdir/unlink/mv/mkdir/touch/truncate/shred/chmod/
//     chown/chgrp/tee;cp/install/ln/rsync 的末位目标;sed -i;dd of=;
//     tar 的 -C 与创建态 -f;unzip -d);
//   - 一层嵌套(bash -c "..." / sh -c / zsh -c / dash -c / ksh -c / eval "...")。
//
// 无法裁决的写形态(变量/命令替换、cd 到工作区之外后的相对路径)显式拒绝,不做乐观放行;
// 通配符按"首个通配段之前的前缀"裁决(前缀是目标的祖先目录,裁决安全)。
//
// 明确边界(不做过度宣称):
//   - 间接写入不在覆盖内:编译器缓存与产物旗标(go build -o、gcc -o)、包管理器下载目录、
//     工具自建临时目录、git clone 目标等 —— 这一层仍由审批档(危险模式 + 工具级名单)兜底;
//   - 读路径只做凭据类判定(denyPath / 字面量段),**不做** workspace 归属限制:
//     否则 `cat /etc/hosts`、编译器读 /usr/include、`ls /tmp` 之类常规操作会被大面积误拦;
//   - 命令文本经变量间接构造(如 `CMD='rm -rf /tmp/x'; $CMD`)不在覆盖内:文本级危险模式审批仍有兜底;
//   - 非 shell 执行器(run_code / lisp_eval)不经本路径(命令体不是 shell 文本)。
package policyguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// shellPath 命令行中识别出的一个路径操作数。
// Unresolvable 表示无法可靠解析:含变量/命令替换,或相对路径但当前目录已不可确定
// (前置 cd 到工作区之外)——写语义下必须显式拒绝;读语义下退化到字面量段凭据判定。
type shellPath struct {
	Path         string
	Write        bool
	Unresolvable bool
}

// shellCmdPaths 扫描 shell 命令行,返回识别出的路径操作数(按出现顺序去重)。
// 工作区根取进程当前目录(纯扫描/单测入口);真实裁决请走 CheckShellCommand(带沙箱 root)。
func shellCmdPaths(cmd string) []shellPath {
	return shellCmdPathsRoot(cmd, 0, "")
}

// shellCmdPathsRoot root = 工作区根(root 为空 = 以进程 cwd 为根,仅用于纯扫描);
// depth = 嵌套展开深度(只展开一层,防病态输入无限递归)。
func shellCmdPathsRoot(cmd string, depth int, root string) []shellPath {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	var (
		out     []shellPath
		words   []string
		wantTgt bool
		wantWr  bool
		heredoc bool
		cwdOK   = true // 相对路径是否仍可定位在工作区内(初始 cwd = 工作区根)
		parens  []bool // 子 shell 深度栈:`)` 时恢复
	)
	flush := func() {
		if len(words) == 0 {
			return
		}
		next, paths := classifyShellCommand(words, root, cwdOK, depth)
		cwdOK = next
		out = append(out, paths...)
		words = words[:0]
	}
	for _, tk := range scanShell(cmd) {
		switch tk.kind {
		case tokHeredoc:
			heredoc = true // 下一个词是定界符,不是路径
		case tokWriteRedir:
			wantTgt, wantWr = true, true
		case tokReadRedir:
			wantTgt, wantWr = true, false
		case tokFdDup:
			// fd 复制(2>&1 等):无路径目标
		case tokSep, tokPipe:
			if tk.kind == tokSep {
				switch tk.text {
				case "(":
					flush()
					parens = append(parens, cwdOK)
				case ")":
					flush() // 先判定子 shell 内最后一个命令,再恢复 cwd 判定
					if n := len(parens); n > 0 {
						cwdOK = parens[n-1]
						parens = parens[:n-1]
					}
				default:
					flush()
				}
				continue
			}
			flush()
		case tokWord:
			if heredoc {
				heredoc = false
				continue
			}
			if wantTgt {
				wantTgt = false
				if pth, ok := makeShellPath(tk.text, wantWr, cwdOK, root); ok {
					out = append(out, pth)
				}
				continue
			}
			words = append(words, tk.text)
		}
	}
	flush()
	return dedupeShellPaths(out)
}

// makeShellPath 把原始 token 转成待裁决的路径;ok=false 表示该 token 无需裁决
// (空、`-`、伪设备)。write 语义下做归一(通配前缀 / `~` 展开 / 相对根);读语义下
// 保留字面量(凭据判定按字面量段进行,故变量不阻断判定)。
func makeShellPath(raw string, write, cwdOK bool, root string) (shellPath, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return shellPath{}, false
	}
	if isDevicePath(raw) {
		return shellPath{}, false // /dev/null 等:写它不构成文件系统变更
	}
	if !write {
		return shellPath{Path: raw, Unresolvable: hasShellExpansion(raw)}, true
	}
	if hasShellExpansion(raw) || strings.ContainsAny(raw, "{}") {
		// 变量/命令替换/占位符(xargs -I{} 的 {}、brace expansion):落点不可静态确定
		return shellPath{Path: raw, Write: true, Unresolvable: true}, true
	}
	if head, isGlob := globHead(raw); isGlob {
		raw = head
		if raw == "" {
			raw = "." // 纯通配(如 *.log):裁决当前目录
		}
	}
	if strings.HasPrefix(raw, "~") {
		exp, ok := expandTilde(raw)
		if !ok {
			return shellPath{Path: raw, Write: true, Unresolvable: true}, true
		}
		raw = exp
	}
	if !filepath.IsAbs(raw) && !cwdOK {
		// 前置 cd 到工作区之外:相对路径落点未知 → 不乐观放行
		return shellPath{Path: raw, Write: true, Unresolvable: true}, true
	}
	return shellPath{Path: raw, Write: true}, true
}

// dedupeShellPaths 去重(Path+Write 相同视为同一目标;保留首次出现的判定)。
func dedupeShellPaths(in []shellPath) []shellPath {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]shellPath, 0, len(in))
	for _, p := range in {
		key := p.Path
		if p.Write {
			key = "w:" + key
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// ---------- 命令级分类 ----------

// classifyShellCommand 判定单条命令的路径操作数,并返回其后的相对路径可定位性(nextCwdOK)。
func classifyShellCommand(words []string, root string, cwdOK bool, depth int) (bool, []shellPath) {
	bin, args := stripShellPrefixes(words[0], words[1:])
	if bin == "" {
		return cwdOK, nil
	}
	name := filepath.Base(bin)
	if depth == 0 {
		if inner, ok := nestedShellCommand(name, args); ok {
			return cwdOK, shellCmdPathsRoot(inner, depth+1, root)
		}
	}
	if name == "cd" {
		return cdTargetInside(args, root, cwdOK), nil
	}
	switch name {
	case "dd":
		return cwdOK, ddPaths(args, root, cwdOK)
	case "tar":
		return cwdOK, tarPaths(args, root, cwdOK)
	case "unzip":
		return cwdOK, unzipPaths(args, root, cwdOK)
	case "sed":
		return cwdOK, sedPaths(args, root, cwdOK)
	case "cp", "install", "ln", "rsync":
		return cwdOK, lastWritePaths(args, root, cwdOK)
	case "rm", "rmdir", "unlink", "mkdir", "touch", "truncate", "shred",
		"chmod", "chown", "chgrp", "tee", "mv":
		return cwdOK, writePaths(operandWords(args), root, cwdOK)
	}
	// 其余命令(含 go/npm/git/grep/python3 等):路径操作数按读语义判定。
	// 这些命令自身的写入(构建产物/缓存)属"间接写入",见文件头边界说明。
	return cwdOK, readPaths(operandWords(args))
}

// prefixValueFlags 前缀命令的取值型 flag(取值要紧随其后,剥离时须一并跳过)。
var prefixValueFlags = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-p": true, "-C": true, "-h": true, "-R": true, "-T": true, "-U": true},
	"doas":    {},
	"command": {},
	"env":     {"-u": true, "-C": true, "-S": true},
	"nice":    {"-n": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"xargs":   {"-I": true, "-i": true, "-n": true, "-P": true, "-s": true, "-L": true, "-E": true, "-d": true, "-a": true},
	"time":    {"-o": true, "-f": true},
	"nohup":   {},
	"exec":    {},
	"setsid":  {},
	"builtin": {},
	"timeout": {"-s": true, "-k": true},
}

// prefixSkipOperand 首位操作数是前缀自身取值(而非命令名)的前缀命令(如 `timeout 5 <cmd>`)。
var prefixSkipOperand = map[string]bool{"timeout": true}

// stripShellPrefixes 剥离 sudo/env/nohup/time/xargs 等前缀,取真实命令名。
// 不剥离的话 `sudo rm -rf /tmp/x` 会被当成未知命令按读语义放行(写目标漏判)。
func stripShellPrefixes(bin string, args []string) (string, []string) {
	for i := 0; i < 4 && bin != ""; i++ {
		vals, ok := prefixValueFlags[filepath.Base(bin)]
		if !ok {
			return bin, args
		}
		j := 0
		for j < len(args) {
			w := args[j]
			if w == "--" {
				j++
				break
			}
			if isAssignment(w) { // env FOO=1 cmd ...
				j++
				continue
			}
			if !strings.HasPrefix(w, "-") {
				break
			}
			if strings.Contains(w, "=") { // --foo=bar 自带取值
				j++
				continue
			}
			base := w
			j++
			if vals[base] {
				j++ // 该 flag 带独立取值(如 sudo -u root)
			}
		}
		if name := filepath.Base(bin); prefixSkipOperand[name] && j < len(args) && !strings.HasPrefix(args[j], "-") {
			j++ // timeout 5 <cmd>:跳过时长操作数
		}
		if j >= len(args) {
			return "", nil
		}
		bin, args = args[j], args[j+1:]
	}
	return bin, args
}

// nestedShellCommand 一层嵌套:bash -c "..." / eval "..."。
func nestedShellCommand(name string, args []string) (string, bool) {
	switch name {
	case "bash", "sh", "zsh", "dash", "ksh":
		for i, w := range args {
			if w == "-c" || w == "--command" {
				if i+1 < len(args) {
					return args[i+1], true
				}
				return "", true
			}
		}
		return "", false
	case "eval":
		inner := strings.Join(operandWords(args), " ")
		return inner, inner != ""
	}
	return "", false
}

// cdTargetInside 判定 cd 之后相对路径是否仍可定位在工作区内:
//   - 裸 cd(→ HOME)/ 绝对路径 / `~` / 含变量:按 root 归属判定(不可知则保守 false);
//   - 相对路径不含 `..`:不越出当前目录树,沿用当前判定;
//   - 相对路径含 `..`:可能越界,保守 false。
func cdTargetInside(args []string, root string, cwdOK bool) bool {
	target := ""
	for _, w := range args {
		if strings.HasPrefix(w, "-") {
			continue // cd -P / cd - 等
		}
		target = w
		break
	}
	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" || root == "" {
			return false
		}
		return pathWithin(root, home)
	}
	if hasShellExpansion(target) {
		return false
	}
	if head, isGlob := globHead(target); isGlob {
		target = head
		if target == "" {
			target = "."
		}
	}
	if strings.HasPrefix(target, "~") {
		exp, ok := expandTilde(target)
		if !ok {
			return false
		}
		target = exp
	}
	if filepath.IsAbs(target) {
		if root == "" {
			return false
		}
		return pathWithin(root, target)
	}
	if strings.Contains(target, "..") {
		return false
	}
	return cwdOK
}

// ---------- 各命令的路径操作数 ----------

// operandWords 取非 flag、非赋值形态的操作数(flag 取值不特判:取值作为词出现时按相对路径落在工作区内,不产生误拦)。
func operandWords(args []string) []string {
	out := make([]string, 0, len(args))
	for _, w := range args {
		if w == "" || w == "--" || strings.HasPrefix(w, "-") || isAssignment(w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// isAssignment 判定 shell 赋值形态词(env FOO=1 / FOO=bar cmd)。
func isAssignment(w string) bool {
	k := strings.IndexByte(w, '=')
	if k <= 0 {
		return false
	}
	for i := 0; i < k; i++ {
		c := w[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

func writePaths(ops []string, root string, cwdOK bool) []shellPath {
	var out []shellPath
	for _, w := range ops {
		if pth, ok := makeShellPath(w, true, cwdOK, root); ok {
			out = append(out, pth)
		}
	}
	return out
}

func readPaths(ops []string) []shellPath {
	var out []shellPath
	for _, w := range ops {
		if pth, ok := makeShellPath(w, false, true, ""); ok {
			out = append(out, pth)
		}
	}
	return out
}

// lastWritePaths cp/install/ln/rsync:末位操作数是写目标,其余按读语义。
func lastWritePaths(args []string, root string, cwdOK bool) []shellPath {
	ops := operandWords(args)
	if len(ops) == 0 {
		return nil
	}
	out := readPaths(ops[:len(ops)-1])
	if pth, ok := makeShellPath(ops[len(ops)-1], true, cwdOK, root); ok {
		out = append(out, pth)
	}
	return out
}

// sedPaths 仅 `-i/--in-place` 态产生写;首个非 flag 操作数是脚本,不是路径。
func sedPaths(args []string, root string, cwdOK bool) []shellPath {
	inPlace := false
	for _, w := range args {
		if w == "-i" || w == "--in-place" || strings.HasPrefix(w, "-i.") || strings.HasPrefix(w, "--in-place=") {
			inPlace = true
		}
	}
	if !inPlace {
		return readPaths(operandWords(args))
	}
	ops := operandWords(args)
	if len(ops) > 0 {
		ops = ops[1:] // 跳过 sed 脚本
	}
	return writePaths(ops, root, cwdOK)
}

// ddPaths 只认 of=(写)与 if=(读);bs=/count= 等非路径。
func ddPaths(args []string, root string, cwdOK bool) []shellPath {
	var out []shellPath
	for _, w := range args {
		switch {
		case strings.HasPrefix(w, "of="):
			if pth, ok := makeShellPath(strings.TrimPrefix(w, "of="), true, cwdOK, root); ok {
				out = append(out, pth)
			}
		case strings.HasPrefix(w, "if="):
			if pth, ok := makeShellPath(strings.TrimPrefix(w, "if="), false, true, ""); ok {
				out = append(out, pth)
			}
		}
	}
	return out
}

// tarPaths `-C/--directory` 是写目标;`-f/--file` 在创建态(-c/--create)是写,解包态是读。
func tarPaths(args []string, root string, cwdOK bool) []shellPath {
	create := false
	for _, w := range args {
		if w == "--create" {
			create = true
			continue
		}
		if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.ContainsAny(w[1:], "c") {
			create = true
		}
	}
	var out []shellPath
	add := func(v string, write bool) {
		if pth, ok := makeShellPath(v, write, cwdOK, root); ok {
			out = append(out, pth)
		}
	}
	for i := 0; i < len(args); i++ {
		w := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case w == "-C" || w == "--directory":
			if v := next(); v != "" {
				add(v, true)
			}
		case strings.HasPrefix(w, "--directory="):
			add(strings.TrimPrefix(w, "--directory="), true)
		case strings.HasPrefix(w, "-C"):
			add(strings.TrimPrefix(w, "-C"), true)
		case w == "-f" || w == "--file":
			if v := next(); v != "" {
				add(v, create)
			}
		case strings.HasPrefix(w, "--file="):
			add(strings.TrimPrefix(w, "--file="), create)
		case strings.HasPrefix(w, "--"):
			// 其它长 flag:不进 operands,已由上面两个分支处理取值形态
		case strings.HasPrefix(w, "-") && len(w) > 1:
			body := w[1:]
			for k := 0; k < len(body); k++ {
				switch body[k] {
				case 'C':
					if rest := body[k+1:]; rest != "" {
						add(rest, true)
					} else if v := next(); v != "" {
						add(v, true)
					}
					k = len(body)
				case 'f':
					if rest := body[k+1:]; rest != "" {
						add(rest, create)
					} else if v := next(); v != "" {
						add(v, create)
					}
					k = len(body)
				}
			}
		}
	}
	// 归档名/成员名等非 flag 词按读语义(解包态读归档;-C 已单独判定)
	return append(out, readPaths(operandWords(args))...)
}

// unzipPaths `-d` 目标目录是写;归档本身是读。
func unzipPaths(args []string, root string, cwdOK bool) []shellPath {
	var out []shellPath
	for i := 0; i < len(args); i++ {
		w := args[i]
		switch {
		case w == "-d":
			if i+1 < len(args) {
				i++
				if pth, ok := makeShellPath(args[i], true, cwdOK, root); ok {
					out = append(out, pth)
				}
			}
		case strings.HasPrefix(w, "-d") && len(w) > 2:
			if pth, ok := makeShellPath(w[2:], true, cwdOK, root); ok {
				out = append(out, pth)
			}
		}
	}
	return append(out, readPaths(operandWords(args))...)
}

// ---------- 词法/形态工具 ----------

// hasShellExpansion 变量或命令替换:落点不可静态确定。
func hasShellExpansion(s string) bool {
	return strings.ContainsAny(s, "$`")
}

// isDevicePath 伪设备(/dev/null、/dev/stdout、/dev/fd/N 等):写它不构成文件系统变更。
func isDevicePath(p string) bool {
	if !strings.HasPrefix(p, "/dev/") {
		return false
	}
	rest := strings.TrimPrefix(p, "/dev/")
	if strings.HasPrefix(rest, "fd/") || strings.HasPrefix(rest, "stdin") ||
		strings.HasPrefix(rest, "stdout") || strings.HasPrefix(rest, "stderr") {
		return true
	}
	switch rest {
	case "null", "zero", "full", "random", "urandom", "tty", "console":
		return true
	}
	return false
}

// globHead 通配符前缀:返回首个含通配符的段之前的路径前缀;无通配符时 isGlob=false。
// 前缀是目标的祖先目录,对前缀做归属裁决是安全方向(不会放过越界写)。
func globHead(p string) (string, bool) {
	segs := strings.Split(p, "/")
	head := ""
	for i, seg := range segs {
		if strings.ContainsAny(seg, "*?[") {
			return head, true
		}
		if i == 0 {
			head = seg // 绝对路径首段为空、`./x` 为 `.`、`a/b` 为 `a`
			continue
		}
		head = strings.TrimSuffix(head, "/") + "/" + seg
	}
	return head, false
}

// expandTilde 展开 `~` 与 `~/...`;`~user` 无法可靠定位(isGlob 语义的 ok=false)。
func expandTilde(p string) (string, bool) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	if p == "~" {
		return home, true
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~/")), true
}

// ---------- 词法扫描 ----------

type shellTokKind int

const (
	tokWord       shellTokKind = iota
	tokSep                     // ; && || & ( ) 换行:命令边界
	tokPipe                    // |
	tokWriteRedir              // > >> &> <> 及其 fd 前缀形态:下一词是写目标
	tokReadRedir               // <:下一词是读目标
	tokFdDup                   // 2>&1 / >&2 等:无路径目标
	tokHeredoc                 // << / <<-:下一词是定界符(非路径),正文整体跳过
)

type shellTok struct {
	kind shellTokKind
	text string
}

// scanShell 极简 shell 词法:引号/转义归一,识别运算符、重定向、heredoc 正文。
// 不做任何展开(变量/命令替换保留字面量),顺序与 shell 一致。
func scanShell(cmd string) []shellTok {
	var (
		toks     []shellTok
		heredocs []string
	)
	i, n := 0, len(cmd)
	for i < n {
		c := cmd[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\n':
			i++
			toks = append(toks, shellTok{kind: tokSep, text: "\n"})
			// 换行后是 heredoc 正文:按定界符逐行跳过(正文里的 `>` 等不是命令)
			for len(heredocs) > 0 {
				delim := heredocs[0]
				heredocs = heredocs[1:]
				j, ok := skipHeredocBody(cmd, i, delim)
				if !ok {
					return toks // 未闭合:其余文本按正文处理,停止扫描(不误判为命令)
				}
				i = j
			}
		case c == '#':
			for i < n && cmd[i] != '\n' {
				i++
			}
		case c == '\\' && i+1 < n && cmd[i+1] == '\n':
			i += 2 // 行连接
		case isOpStart(c):
			tok, j := scanOp(cmd, i)
			if tok.kind == tokHeredoc {
				heredocs = append(heredocs, "") // 占位,定界符在下一个词
			}
			toks = append(toks, tok)
			i = j
		default:
			word, j := scanWord(cmd, i)
			if j == i { // 未预期的单字符:跳过,防死循环
				i++
				continue
			}
			if isAllDigits(word) && j < n && (cmd[j] == '>' || cmd[j] == '<') {
				i = j // fd 前缀(2>file):丢弃,重定向语义由运算符分支处理
				continue
			}
			if len(heredocs) > 0 {
				for k := range heredocs {
					if heredocs[k] == "" {
						heredocs[k] = unquoteRaw(word) // 记定界符(按队列顺序)
						break
					}
				}
			}
			toks = append(toks, shellTok{kind: tokWord, text: word})
			i = j
		}
	}
	return toks
}

// scanWord 扫描一个词(引号内容去掉引号,转义归一,变量/命令替换保留字面量);
// 遇到未加引号的运算符字符或空白即结束。
func scanWord(cmd string, i int) (string, int) {
	var b strings.Builder
	n := len(cmd)
	for i < n {
		c := cmd[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			return b.String(), i
		case c == '\'':
			j := i + 1
			for j < n && cmd[j] != '\'' {
				j++
			}
			b.WriteString(cmd[i+1 : min(j, n)])
			if j < n {
				j++
			}
			i = j
		case c == '"':
			j := i + 1
			for j < n {
				if cmd[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				if cmd[j] == '"' {
					break
				}
				j++
			}
			inner := cmd[i+1 : min(j, n)]
			b.WriteString(strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`).Replace(inner))
			if j < n {
				j++
			}
			i = j
		case c == '\\':
			if i+1 < n {
				if cmd[i+1] == '\n' {
					i += 2
					continue
				}
				b.WriteByte(cmd[i+1])
				i += 2
				continue
			}
			i++
		case c == '$' && i+1 < n && cmd[i+1] == '(':
			j := skipDollarParen(cmd, i)
			b.WriteString(cmd[i:j])
			i = j
		case c == '`':
			j := i + 1
			for j < n && cmd[j] != '`' {
				j++
			}
			if j < n {
				j++
			}
			b.WriteString(cmd[i:j])
			i = j
		case isOpStart(c):
			return b.String(), i
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

// unquoteRaw 去引号(heredoc 定界符判定用)。
func unquoteRaw(w string) string {
	return strings.NewReplacer(`"`, "", `'`, "").Replace(w)
}

func isOpStart(c byte) bool {
	switch c {
	case ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// scanOp 扫描运算符(fd 前缀已由调用方丢弃)。
func scanOp(cmd string, i int) (shellTok, int) {
	rest := cmd[i:]
	switch {
	case strings.HasPrefix(rest, "<<-"):
		return shellTok{kind: tokHeredoc, text: "<<"}, i + 3
	case strings.HasPrefix(rest, "<<"):
		return shellTok{kind: tokHeredoc, text: "<<"}, i + 2
	case strings.HasPrefix(rest, "&&"):
		return shellTok{kind: tokSep, text: "&&"}, i + 2
	case strings.HasPrefix(rest, "||"):
		return shellTok{kind: tokSep, text: "||"}, i + 2
	case strings.HasPrefix(rest, "&>>"):
		return shellTok{kind: tokWriteRedir, text: "&>>"}, i + 3
	case strings.HasPrefix(rest, "&>"):
		return shellTok{kind: tokWriteRedir, text: "&>"}, i + 2
	case strings.HasPrefix(rest, ">>"):
		return shellTok{kind: tokWriteRedir, text: ">>"}, i + 2
	case strings.HasPrefix(rest, "<>"):
		return shellTok{kind: tokWriteRedir, text: "<>"}, i + 2
	case strings.HasPrefix(rest, ">&"), strings.HasPrefix(rest, "&>&"):
		j := i + 2
		if strings.HasPrefix(rest, "&>&") {
			j = i + 3
		}
		for j < len(cmd) && (cmd[j] == '-' || (cmd[j] >= '0' && cmd[j] <= '9')) {
			j++
		}
		return shellTok{kind: tokFdDup, text: ">&"}, j
	case rest[0] == '>':
		return shellTok{kind: tokWriteRedir, text: ">"}, i + 1
	case rest[0] == '<':
		return shellTok{kind: tokReadRedir, text: "<"}, i + 1
	case rest[0] == '|':
		return shellTok{kind: tokPipe, text: "|"}, i + 1
	}
	return shellTok{kind: tokSep, text: string(rest[0])}, i + 1
}

// skipDollarParen 跳过 `$( ... )`(含嵌套与引号),返回结束位置(越过 `)`)。
func skipDollarParen(cmd string, i int) int {
	n, depth := len(cmd), 0
	for j := i; j < n; j++ {
		switch cmd[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j + 1
			}
		case '\'', '"':
			q := cmd[j]
			for j+1 < n && cmd[j+1] != q {
				j++
			}
		}
	}
	return n
}

// skipHeredocBody 从 idx 起逐行跳过 heredoc 正文,直到仅含定界符的行(含末行结束)。
// 未找到闭合定界符 → ok=false(调用方停止扫描,其余文本按正文处理)。
func skipHeredocBody(cmd string, idx int, delim string) (int, bool) {
	if delim == "" {
		return idx, false // 定界符不可知(如 <<$VAR):停止扫描更安全
	}
	for idx <= len(cmd) {
		end := strings.IndexByte(cmd[idx:], '\n')
		var line string
		if end < 0 {
			line = cmd[idx:]
			idx = len(cmd) + 1
		} else {
			line = cmd[idx : idx+end]
			idx += end + 1
		}
		if strings.TrimSpace(line) == delim || strings.TrimLeft(line, "\t") == delim {
			return idx, true
		}
		if end < 0 {
			break
		}
	}
	return idx, false
}

// ---------- 读路径凭据判定 ----------

// checkShellReadToken 读语义判定:仅凭据类(denyPath / 字面量段),**不做** workspace 归属限制
// —— 否则 shell 里 `cat /etc/hosts`、编译器读 /usr/include 等常规读会被大面积误拦。
func checkShellReadToken(root, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return nil
	}
	if hasShellExpansion(raw) {
		// 落点不可知,但字面量段仍可判定(如 $HOME/.ssh/id_rsa)
		return denyShellSegments(raw)
	}
	if !isFileNameShaped(raw) {
		return nil // 裸词(如 `grep -r credentials .` 的搜索词):不当路径判,避免把普通词误判成凭据
	}
	p := raw
	if strings.HasPrefix(p, "~") {
		exp, ok := expandTilde(p)
		if !ok {
			return denyShellSegments(raw)
		}
		p = exp
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return denyPath(filepath.Clean(p))
}

// isFileNameShaped 判定读操作数是否具备"文件名形态":含路径分隔或扩展名。
// 裸词在 shell 里既可能是路径也可能是普通参数/搜索模式(`grep -rn id_rsa .` 的搜索词、
// `echo credentials` 的文本),故不按路径判 —— 否则普通词会被当成凭据路径误拦。
// 已知残留面(可接受):工作区内无扩展名的密钥副本(`cat id_rsa`)不做判定;
// 带路径(含 `~/.ssh/...`、`$HOME/.ssh/...`、`.env`)一律判定。
func isFileNameShaped(tok string) bool {
	return strings.ContainsRune(tok, '/') || strings.ContainsRune(tok, '.')
}

// denyShellSegments 按 `/` 段做凭据名判定(变量段本身跳过,其后字面量段仍判定)。
func denyShellSegments(raw string) error {
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		if strings.HasPrefix(seg, "$") || strings.HasPrefix(seg, "`") {
			continue
		}
		if !isFileNameShaped(seg) {
			continue
		}
		s := strings.ToLower(seg)
		for _, d := range denyBase {
			if s == d {
				return fmt.Errorf("sandbox: 拒绝读取凭据类文件 %s", seg)
			}
		}
		for _, g := range denyGlob {
			if ok, _ := filepath.Match(g, s); ok {
				return fmt.Errorf("sandbox: 拒绝读取凭据类文件 %s", seg)
			}
		}
		switch s {
		case ".ssh", ".gnupg", ".aws":
			return fmt.Errorf("sandbox: 拒绝访问用户密钥目录 %s", s)
		}
	}
	return nil
}
