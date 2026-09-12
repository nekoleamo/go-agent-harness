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
//   - 输出型 flag 与安装目标(curl -o/--output、wget -O/--output-document、
//     gcc/clang/cc/c++/g++/ld -o、go build|install -o、pip install -t/--target、
//     npm install --prefix);
//   - git clone 的位置目标(第二操作数;只给 URL 时落 cwd);
//   - 工具专属输出旗标与写操作数(第二轮扩展):sort -o、patch -o/-d、`go test` 的
//     profile/-trace 系列、gcc/clang -MF|-MJ、pip --cache-dir 与 download -d/--dest、
//     npm --cache、cargo --target-dir|--root、cmake -B|--build|--prefix、split 输出前缀、
//     tar 旧式旗标簇(`tar czf <归档>`,无横线形态)、zip/7z 归档路径与 7z -o<dir>、
//     mktemp -p|--tmpdir 与模板、find 的 -exec|-execdir 内部命令与 -delete|-fprint|-fls;
//   - 一层嵌套(bash -c "..." / sh -c / zsh -c / dash -c / ksh -c / eval "...")。
//
// 无法裁决的写形态(变量/命令替换、cd 到工作区之外后的相对路径)显式拒绝,不做乐观放行;
// 通配符按"首个通配段之前的前缀"裁决(前缀是目标的祖先目录,裁决安全)。
//
// 明确边界(不做过度宣称):
//   - 仍不在覆盖内:编译器/包管理器的**缓存根**(由 tool-shell 的环境 jail 收敛到
//     $GAH_HOME/jail,见 plugins/tool/tool-shell/jail.go)、命令包装器(ccache/make 等)、
//     解释器内部写(`python3 -c "open('/x','w')"`)、重定向到 cwd 的简写(`curl -O`)、
//     环境变量指定的落点(GOPATH/CARGO_TARGET_DIR/XDG_* 等,由 jail 收敛)、
//     构建系统自选落点(`make install`、`cmake --install` 不给 --prefix 时的 CMAKE_INSTALL_PREFIX)、
//     find -exec 内再套包装器的落点 —— 这一层仍由审批档(危险模式 + 工具级名单)兜底;
//   - 读路径只做凭据类判定(denyPath / 字面量段),**不做** workspace 归属限制:
//     否则 `cat /etc/hosts`、编译器读 /usr/include、`ls /tmp` 之类常规操作会被大面积误拦;
//   - 命令文本经变量间接构造(如 `CMD='rm -rf /tmp/x'; $CMD`)不在覆盖内:文本级危险模式审批仍有兜底;
//   - 非 shell 执行器(run_code / lisp_eval)不经本路径(命令体不是 shell 文本)。
package policyguard

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// shellWinSemantics 命令文本按 Windows/MSYS 语义解释。变量而非常量:单测需在非 Windows
// 机器上覆盖该分支(沿用 kernel.go 的 sandboxExec 惯例)。
var shellWinSemantics = runtime.GOOS == "windows"

// winRootRelativePath 判定 Windows 上的 **MSYS 根相对路径**(`/c/foo`、`/tmp/x`、`/usr/bin`)。
//
// 为什么必须单独处理:go 的 filepath 在 Windows 上把这些路径当**相对路径**
// (无卷名 → IsAbs=false),`filepath.Join(root, "/tmp/x")` 会算成 `<root>\tmp\x` ——
// 裁决层“以为”落在工作区内,而 MSYS 实际写到 `C:\Users\…\AppData\Local\Temp\x`,
// 即最危险的静默击穿(判为区内 → 放行 → 实际写到区外)。
// 处理:写语义下一律按**不可裁决**拒绝(与变量/命令替换同一处置);读语义不阻断。
// UNC(`\\srv\share\x`)带卷名,不走本分支(真机需单独复核)。
func winRootRelativePath(raw string) bool {
	if !shellWinSemantics || filepath.VolumeName(raw) != "" {
		return false
	}
	return strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\")
}

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
	if winRootRelativePath(raw) {
		// Windows/MSYS 根相对路径:filepath 视为相对,但 MSYS 会解析到别的绝对位置(见函数注释)
		return shellPath{Path: raw, Write: true, Unresolvable: true}, true
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
	case "curl":
		return cwdOK, outputFlagPaths(args, root, cwdOK, "-o", "--output")
	case "wget":
		return cwdOK, outputFlagPaths(args, root, cwdOK, "-O", "--output-document")
	case "sort":
		return cwdOK, outputFlagPaths(args, root, cwdOK, "-o", "--output")
	case "gcc", "cc", "clang", "c++", "g++", "ld":
		return cwdOK, outputFlagPaths(args, root, cwdOK, "-o", "--output", "-MF", "-MJ")
	case "go":
		return cwdOK, goPaths(args, root, cwdOK)
	case "pip", "pip3":
		return cwdOK, pipPaths(args, root, cwdOK)
	case "npm":
		return cwdOK, npmPaths(args, root, cwdOK)
	case "cargo":
		return cwdOK, cargoPaths(args, root, cwdOK)
	case "cmake":
		return cwdOK, cmakePaths(args, root, cwdOK)
	case "git":
		return cwdOK, gitPaths(args, root, cwdOK)
	case "patch":
		return cwdOK, patchPaths(args, root, cwdOK)
	case "split":
		return cwdOK, splitPaths(args, root, cwdOK)
	case "mktemp":
		return cwdOK, mktempPaths(args, root, cwdOK)
	case "find":
		return cwdOK, findPaths(args, root, cwdOK)
	case "zip", "7z", "7za", "7zr":
		return cwdOK, archiverPaths(name, args, root, cwdOK)
	}
	// 其余命令(含 grep/python3 等):路径操作数按读语义判定。
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

// tarPaths `-C/--directory` 是写目标;`-f/--file` 在创建态(-c/--create)是写,解包态是读;
// 旧式**无横线旗标簇**(`tar czf a.tgz .`)同样按旗标处理(tar 的取值与旗标簇分词:
// f=归档、C=目录,取簇后紧随的那个词)。
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
	// 旧式旗标簇:首位是不带横线的全字母词(tar 必须给旗标,故不会把普通文件名误当簇)。
	bundleAt, bundle := -1, ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && isAlphaWord(args[0]) {
		bundleAt, bundle = 0, args[0]
		if strings.ContainsRune(bundle, 'c') {
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
		case i == bundleAt:
			for k := 0; k < len(bundle); k++ {
				switch bundle[k] {
				case 'f':
					if v := next(); v != "" {
						add(v, create)
					}
				case 'C':
					if v := next(); v != "" {
						add(v, true)
					}
				}
			}
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

// ---------- 输出型 flag / 安装目标 / git clone 位置目标 ----------

// flagWritePaths 取输出型 flag 的写入目标:
//   - 分离形态 `-o out` / `--output out`;
//   - 紧贴形态 `-oout`(短 flag)/ `-MF/tmp/d.d`(单横线多字符 flag)/ `--output=out`;
//   - 取值缺失(flag 在末尾)不产生目标,也不 panic。
func flagWritePaths(args []string, root string, cwdOK bool, flags ...string) []shellPath {
	set := make(map[string]bool, len(flags))
	for _, f := range flags {
		set[f] = true
	}
	var out []shellPath
	add := func(v string) {
		if pth, ok := makeShellPath(v, true, cwdOK, root); ok {
			out = append(out, pth)
		}
	}
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w == "--" {
			break // `--` 之后都是位置操作数,不再有 flag 取值
		}
		if long, val, ok := strings.Cut(w, "="); ok && set[long] {
			add(val)
			continue
		}
		if set[w] {
			if i+1 < len(args) {
				i++
				add(args[i])
			}
			continue
		}
		if len(w) > 2 && w[0] == '-' && w[1] != '-' && set[w[:2]] {
			add(w[2:]) // 短 flag 紧贴取值(gcc -oout)
			continue
		}
		if tail, ok := joinedFlagValue(w, set); ok {
			add(tail) // 单横线多字符 flag 紧贴取值(gcc -MF/tmp/d.d)
		}
	}
	return out
}

// joinedFlagValue 单横线多字符 flag 的紧贴取值(`-MF/tmp/d.d`)。
// 取**最长**匹配:否则 `-o` 会把 `-MF/tmp/d.d` 的值抢成 `F/tmp/d.d`(落点就错了)。
func joinedFlagValue(w string, set map[string]bool) (string, bool) {
	if len(w) < 4 || w[0] != '-' || w[1] == '-' {
		return "", false
	}
	best := ""
	for f := range set {
		if len(f) <= 2 || strings.HasPrefix(f, "--") || !strings.HasPrefix(w, f) {
			continue
		}
		if len(f) > len(best) {
			best = f
		}
	}
	if best == "" {
		return "", false
	}
	return w[len(best):], true
}

// outputFlagPaths 输出型 flag 的写目标 + 其余操作数按读语义。
// 只增不减:不因新增写识别而放过原有的凭据读判定。
func outputFlagPaths(args []string, root string, cwdOK bool, flags ...string) []shellPath {
	return append(flagWritePaths(args, root, cwdOK, flags...), readPaths(operandWords(args))...)
}

// subcmdArgs 取子命令与其后的参数(跳过前置全局 flag;valueFlags = 前置取值型 flag)。
func subcmdArgs(args []string, valueFlags map[string]bool) (string, []string) {
	for i := 0; i < len(args); i++ {
		w := args[i]
		if isAssignment(w) {
			continue
		}
		if strings.HasPrefix(w, "-") {
			if valueFlags[w] {
				i++ // 该 flag 的取值不是子命令(如 go -C dir build)
			}
			continue
		}
		return w, args[i+1:]
	}
	return "", nil
}

// flagValue 取前置 flag 的取值(紧贴形态 -C/path 或分离形态 -C /path);遇到首个位置操作数即停。
func flagValue(args []string, flag string) (string, bool) {
	for i := 0; i < len(args); i++ {
		w := args[i]
		if isAssignment(w) {
			continue
		}
		if len(flag) == 2 && strings.HasPrefix(w, flag) && len(w) > len(flag) {
			return strings.TrimPrefix(w, flag), true // 短 flag 紧贴取值(git -C/tmp)
		}
		if w == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if !strings.HasPrefix(w, "-") {
			return "", false
		}
	}
	return "", false
}

// localCwdOK 取前置 `-C <dir>` 之后的相对路径可定位性(`go -C` / `git -C` 等于换 cwd:
// 相对目标会被记到别处,不保守处理就是漏判)。
func localCwdOK(args []string, root string, cwdOK bool) bool {
	v, ok := flagValue(args, "-C")
	if !ok {
		return cwdOK
	}
	return cdTargetInside([]string{v}, root, cwdOK)
}

// goPaths go build/install 的 -o 是产物写目标;`go test` 的 profile/-trace 旗标同理
// (`go test -o` 仍未纳入:其语义是“写到文件”而非产物路径,且既有用例已固定该边界)。
// go 自身缓存写由 tool-shell 的环境 jail 收敛。
func goPaths(args []string, root string, cwdOK bool) []shellPath {
	sub, rest := subcmdArgs(args, map[string]bool{"-C": true})
	cwd := localCwdOK(args, root, cwdOK)
	switch sub {
	case "build", "install":
		return outputFlagPaths(rest, root, cwd, "-o", "--output")
	case "test":
		return outputFlagPaths(rest, root, cwd, goTestProfileFlags...)
	}
	return readPaths(operandWords(args))
}

// goTestProfileFlags `go test` 的**写文件**旗标(flag 包同时接受 `-flag value` 与 `-flag=value`)。
var goTestProfileFlags = []string{
	"-coverprofile", "-cpuprofile", "-memprofile", "-blockprofile", "-mutexprofile", "-trace",
}

// pipInstallSubcmds / npmInstallSubcmds 安装类子命令:只有这些子命令的目标 flag 才有"装到哪"语义。
var (
	pipInstallSubcmds = map[string]bool{"install": true}
	npmInstallSubcmds = map[string]bool{"install": true, "i": true, "ci": true, "add": true}
)

// pipInstallFlags / pipDownloadFlags:`pip install` 的安装目标与缓存根;
// `pip download -d/--dest` 的下载落点。
var (
	pipInstallFlags  = []string{"-t", "--target", "--cache-dir"}
	pipDownloadFlags = []string{"-d", "--dest"}
)

// pipPaths pip 的写目标(按子命令分派;其它子命令保持原读语义)。
func pipPaths(args []string, root string, cwdOK bool) []shellPath {
	sub, _ := subcmdArgs(args, nil)
	switch sub {
	case "install":
		return installFlagPaths(args, pipInstallSubcmds, pipInstallFlags, root, cwdOK)
	case "download":
		return outputFlagPaths(args, root, cwdOK, pipDownloadFlags...)
	}
	return readPaths(operandWords(args))
}

// npmPaths npm 的写目标:`--cache` 是全局缓存根(任意子命令);
// `--prefix` 只对安装类子命令有"装到哪"语义(非安装子命令的 --prefix 不是写目标,保持既有边界)。
func npmPaths(args []string, root string, cwdOK bool) []shellPath {
	out := flagWritePaths(args, root, cwdOK, "--cache")
	return append(out, installFlagPaths(args, npmInstallSubcmds, []string{"--prefix"}, root, cwdOK)...)
}

// installFlagPaths 安装类子命令的目标目录 flag(-t/--target、--prefix)取值为写目标;
// 其它子命令保持原读语义(不新增判定面)。
func installFlagPaths(args []string, subcmds map[string]bool, flags []string, root string, cwdOK bool) []shellPath {
	if sub, _ := subcmdArgs(args, nil); !subcmds[sub] {
		return readPaths(operandWords(args))
	}
	return outputFlagPaths(args, root, cwdOK, flags...)
}

// gitCloneValueFlags `git clone` 的取值型 flag:取值不是位置操作数(否则 --depth 的 1 会被当成目标目录)。
var gitCloneValueFlags = map[string]bool{
	"-b": true, "-o": true, "-j": true, "--branch": true, "--depth": true, "--origin": true,
	"--reference": true, "--reference-if-able": true, "--separate-git-dir": true, "--template": true,
	"--jobs": true, "--upload-pack": true, "--shallow-since": true, "--shallow-exclude": true,
	"--filter": true, "--config": true, "--revision": true, "--server-option": true,
}

// gitPaths git clone 的位置目标是写(第二操作数;只给 URL 时落 cwd);其余子命令按读语义。
func gitPaths(args []string, root string, cwdOK bool) []shellPath {
	sub, rest := subcmdArgs(args, map[string]bool{
		"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
		"--namespace": true, "--config-env": true,
	})
	if sub != "clone" {
		return readPaths(operandWords(args))
	}
	ops := cloneOperands(rest)
	target := "." // 只有 URL:克隆到当前目录(相对目标,属工作区内)
	if len(ops) >= 2 {
		target = ops[len(ops)-1]
	}
	out := readPaths(operandWords(rest))
	if pth, ok := makeShellPath(target, true, localCwdOK(args, root, cwdOK), root); ok {
		out = append(out, pth)
	}
	return out
}

// cloneOperands 取 clone 的位置操作数(排除 flag 及其取值)。
func cloneOperands(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		w := args[i]
		if isAssignment(w) {
			continue
		}
		if strings.HasPrefix(w, "-") {
			if !strings.Contains(w, "=") && gitCloneValueFlags[w] {
				i++
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// ---------- 工具专属输出旗标与写操作数(第二轮扩展) ----------
//
// 共同的判定原那么:能静态指认“这个命令会往哪里写”就必须裁决;指认不了(变量/命令替换)
// 那么拒绝。扩展只增拒绝面,不放松任何既有判定(尤其凭据类读判定)。

// splitValueFlags / archiverValueFlags 取值型 flag 表(取值不是位置操作数)。
var (
	splitValueFlags = map[string]bool{
		"-a": true, "-b": true, "-l": true, "-n": true, "-t": true,
		"--bytes": true, "--lines": true, "--number": true, "--suffix-length": true,
		"--additional-suffix": true, "--separator": true,
	}
	archiverValueFlags = map[string]bool{
		"-x": true, "-i": true, "-P": true,
		"--exclude": true, "--include": true, "--password": true,
	}
)

// operandsSkippingValues 取位置操作数(跳过 flag 及其**取值**)。
// 与 operandWords 的差异:后者不认取值,故 `split -b 1k f /tmp/part` 会把 `1k` 当操作数,
// 输出前缀就错位到 `f` 上。取值型 flag 多的命令用本函数。
func operandsSkippingValues(args []string, valueFlags map[string]bool) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		w := args[i]
		if w == "--" {
			continue
		}
		if strings.HasPrefix(w, "-") {
			if long, _, ok := strings.Cut(w, "="); ok && valueFlags[long] {
				continue // --flag=value 自带取值
			}
			if valueFlags[w] && i+1 < len(args) {
				i++ // 该 flag 带独立取值
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// isAlphaWord 全字母词(tar 旧式旗标簇 `czf` / `xvzf` 的形态)。
func isAlphaWord(w string) bool {
	if w == "" {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// patchPaths patch 的写目标:
//   - `-o/--output <file>`:结果写到该文件(不再就地改);
//   - `-d/--directory <dir>`:patch 先切到该目录,再从 **diff 头**取目标文件名就地改 ——
//     目标名不在命令行上,故把**目录本身**当写目标裁决:目录在工作区内 → 其内写也都在
//     工作区内;目录在外 → 拒绝。(不把 patch 一律拒绝:gnu patch 默认拒绍 diff 里的
//     绝对路径与 `..`,误拦会打断正常的“在工作区内打补丁”工作流。)
func patchPaths(args []string, root string, cwdOK bool) []shellPath {
	out := flagWritePaths(args, root, cwdOK, "-o", "--output")
	out = append(out, flagWritePaths(args, root, cwdOK, "-d", "--directory")...)
	return append(out, readPaths(operandWords(args))...)
}

// splitPaths split 的操作数形态是 [INPUT [PREFIX]]:输入是读,输出前缀(第二个操作数)是写。
// 缺前缀时落当前目录(裸 `split` 从 stdin 读、在当前目录产 xaa)—— cwd 不可定位时即不可裁决。
func splitPaths(args []string, root string, cwdOK bool) []shellPath {
	ops := operandsSkippingValues(args, splitValueFlags)
	out := readPaths(ops)
	prefix := "."
	if len(ops) >= 2 {
		prefix = ops[1]
	}
	if pth, ok := makeShellPath(prefix, true, cwdOK, root); ok {
		out = append(out, pth)
	}
	return out
}

// mktempPaths mktemp 的落点:
//   - `-p/--tmpdir <dir>`:显式临时目录(写目标);
//   - 位置操作数 = 模板(可含目录,如 `mktemp /tmp/x.XXXXXX`),其目录是写目标;
//   - 裸 mktemp 与 `-t` 落 TMPDIR(= tool-shell 环境 jail 的 $GAH_HOME/jail),不判定。
func mktempPaths(args []string, root string, cwdOK bool) []shellPath {
	out := flagWritePaths(args, root, cwdOK, "-p", "--tmpdir")
	for _, w := range operandWords(args) {
		if pth, ok := makeShellPath(w, true, cwdOK, root); ok {
			out = append(out, pth)
		}
	}
	return out
}

// archiverPaths zip/7z 系的归档路径:
//   - `zip [opts] <archive> <files...>`:首个位置操作数是归档(创建/更新 → 写);
//   - `7z <a|u|x|e|...> <archive> ...`:`a`/`u` 归档是写;`x`/`e` 是**解包**
//     (落 `-o<dir>` 紧贴形态,缺省当前目录)。
func archiverPaths(name string, args []string, root string, cwdOK bool) []shellPath {
	ops := operandsSkippingValues(args, archiverValueFlags)
	out := readPaths(ops)
	if name == "zip" {
		if len(ops) > 0 {
			if pth, ok := makeShellPath(ops[0], true, cwdOK, root); ok {
				out = append(out, pth)
			}
		}
		return out
	}
	if len(ops) == 0 {
		return out
	}
	sub, rest := ops[0], ops[1:]
	switch sub {
	case "a", "u":
		if len(rest) > 0 {
			if pth, ok := makeShellPath(rest[0], true, cwdOK, root); ok {
				out = append(out, pth)
			}
		}
	case "x", "e":
		dest := "." // 缺 -o 时解到当前目录
		for _, w := range args {
			if strings.HasPrefix(w, "-o") && len(w) > 2 {
				dest = w[2:]
				break
			}
		}
		if pth, ok := makeShellPath(dest, true, cwdOK, root); ok {
			out = append(out, pth)
		}
	}
	return out
}

// findPaths find 的写语义:
//   - `-exec/-execdir/-ok/-okdir <cmd...> ;|+`:递归分类**内部命令**(含内部重定向与嵌套 sh -c);
//     `{}` 占位符按**搜索根**代入 —— 被找到的文件都在搜索根之下,不代入就会把
//     `find . -exec rm -rf {} ;` 这种最常见写法当成“不可裁决写”而误拦;
//   - `-fprint/-fprint0/-fls/-fprintf <file>`:输出文件是写目标;
//   - `-delete`:删除被找到的文件(均在搜索根之下)→ 搜索根按写目标裁决。
func findPaths(args []string, root string, cwdOK bool) []shellPath {
	search := findSearchRoot(args)
	if search == "" {
		search = "."
	}
	var out []shellPath
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
			var inner []string
			j := i + 1
			for ; j < len(args); j++ {
				if args[j] == ";" || args[j] == "+" {
					break // 参数终止符(";" 常已被词法层切成分隔符,此时自然到末尾)
				}
				inner = append(inner, args[j])
			}
			for k, w := range inner {
				if strings.Contains(w, "{}") {
					inner[k] = strings.ReplaceAll(w, "{}", search)
				}
			}
			if len(inner) > 0 {
				_, paths := classifyShellCommand(inner, root, cwdOK, 0)
				out = append(out, paths...)
			}
			i = j - 1
		case "-fprint", "-fprint0", "-fls", "-fprintf":
			if i+1 < len(args) {
				if pth, ok := makeShellPath(args[i+1], true, cwdOK, root); ok {
					out = append(out, pth)
				}
				i++
			}
		case "-delete":
			if pth, ok := makeShellPath(search, true, cwdOK, root); ok {
				out = append(out, pth)
			}
		}
	}
	return append(out, readPaths(operandWords(args))...)
}

// findSearchRoot 取 find 的搜索根:find 的语法是 `find [起始路径...] [表达式]`,
// 故搜索根是**表达式之前**的前导非 flag 词(取首个;缺省 ".")。
// 不能在整串里找首个非 flag 词:`find -name x -exec rm {} ;` 的 `x` 是谓词取值,不是搜索根。
func findSearchRoot(args []string) string {
	for _, w := range args {
		if w == "-L" || w == "-H" || w == "-P" {
			continue // 前置于路径的符号链接选项
		}
		if w == "" || strings.HasPrefix(w, "-") {
			return "" // 进入表达式:前面没有搜索根
		}
		return w
	}
	return ""
}

// cmakePaths cmake 的写目标:`-B <build>`(创建/写入构建目录)、`--build <dir>`、
// `--prefix <dir>`(安装落点)。`--install <dir>` 的参数是**读**的构建目录(不是落点)。
func cmakePaths(args []string, root string, cwdOK bool) []shellPath {
	return outputFlagPaths(args, root, cwdOK, "-B", "--build", "--prefix")
}

// cargoPaths cargo 的写目标:`--target-dir <dir>`(产物/构建缓存)、`--root <dir>`(install 落点)。
// 环境变量形态(CARGO_TARGET_DIR)不在命令行上,由环境 jail 与应用层约束兜底。
func cargoPaths(args []string, root string, cwdOK bool) []shellPath {
	return outputFlagPaths(args, root, cwdOK, "--target-dir", "--root")
}

// ---------- 词法/形态工具 ----------

// hasShellExpansion 变量或命令替换:落点不可静态确定。
func hasShellExpansion(s string) bool {
	return strings.ContainsAny(s, "$`")
}

// isDevicePath 伪设备(/dev/null、/dev/stdout、/dev/fd/N 等):写它不构成文件系统变更。
func isDevicePath(p string) bool {
	if shellWinSemantics {
		// Windows 保留设备名(NUL 等等价 /dev/null):写它不产生文件系统变更。
		// 只在 Windows 语义下豁免:POSIX 上一个真名叫 `nul` 的文件不应被跳过裁决。
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "nul", "con", "prn", "aux":
			return true
		}
	}
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
