// scope.go:项目级记忆文件的「本文件收录什么」描述(2026-11-08,B 批借鉴)。
//
// 为什么需要它:项目级记忆是 `projects/<key>.md` 一个文件,而**什么该写进这个文件**
// 目前只存在于人脑里 —— 写偏了不会报错,只会让后来的人(和模型)反复往同一处塞不相干
// 的东西。Routed CL(arXiv 2610.38796)里 cluster 带着一条**不可变的 description**
// 专门回答这件事,路由与写入都以它为准;那边的实验显示状态共享边界比"用什么机制记"
// 更影响结果,而边界恰恰是这条描述表达的东西。
//
// 形态:文件**首行**一条 HTML 注释
//
//	<!-- scope: 本项目只收编译/测试/发布相关的约定,不收个人偏好 -->
//
// 为什么是注释而不是新字段:① ParseLine 本来就允许文件里夹注释与空行(见 memory.go),
// 加它**不用改解析器**,旧文件零影响;② 人读 markdown 时看不见它,不会打断正文;
// ③ 记忆按"一行一条"存,给条目加状态字段会让解析/正文/注入三处都开始处理"这行是不是
// 生效的",而这里要表达的只是文件级的元信息。
//
// 边界:**不自动生成**。模型写范围 = 又一个静默提权的入口,而这条描述的价值全在
// "人拍板的口径"。空 = 不渲染,与没有这项功能完全一致。
package memory

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// MaxScopeBytes 收录范围的字数上限(200 字节 ≈ 66 个汉字)。
//
// 为什么要上限:它会进系统提示的稳定块,占的是**记忆注入预算**;写成长篇会让
// 真正的记忆条目被挤出去,那正是这一层要防的事。
const MaxScopeBytes = 200

// scopeRe 首行描述行的格式。宽松:允许前后空白与不同间距(人手改文件是常态)。
var scopeRe = regexp.MustCompile(`^\s*<!--\s*scope\s*:\s*(.*?)\s*-->\s*$`)

// ScopeLine 读某个记忆文件首行的收录范围(不存在 / 没有该行 ⇒ 空串,不是错误)。
func ScopeLine(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memory: 读收录范围 %s: %w", path, err)
	}
	line := string(raw)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if m := scopeRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[1]), nil
	}
	return "", nil
}

// SetScope 设置/清除某项目记忆文件的收录范围(text 空 = 清除该行)。
//
// 只动首行,其余内容**逐字节保留** —— 这个文件是人的手写本,任何"顺手重排"
// 都会让人失去改它的信心。文件不存在时新建(只有一行范围,没有记忆条目也是正常状态)。
func SetScope(projectKey, text string) error {
	key := strings.TrimSpace(projectKey)
	if key == "" {
		return fmt.Errorf("memory: 没有当前项目,无法设置收录范围")
	}
	text = strings.TrimSpace(text)
	if strings.ContainsAny(text, "\r\n") {
		return fmt.Errorf("memory: 收录范围只能是**一行**(描述这个文件收什么,别写成正文)")
	}
	if len(text) > MaxScopeBytes {
		return fmt.Errorf("memory: 收录范围 %d 字节,超过上限 %d;写一句就够(它会占记忆注入预算)", len(text), MaxScopeBytes)
	}
	path := ProjectPath(key)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: 读 %s: %w", path, err)
	}
	lines := splitLines(string(raw))
	rest := lines
	if len(lines) > 0 && scopeRe.MatchString(lines[0]) {
		rest = lines[1:]
	}
	var sb strings.Builder
	if text != "" {
		sb.WriteString("<!-- scope: " + text + " -->\n")
	}
	for _, l := range rest {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return writeFileAtomic(path, []byte(sb.String()))
}

// ReadWithScope 一次读盘同时取条目与首行收录范围(自动调取每步都走这条路)。
//
// 为什么要有它:`LoadSnapshot` 的注释写着"一次读盘",而范围行与条目在**同一个文件**里 ——
// 各读一次不但多一次 IO,也让"条目的来源"与"范围行的来源"不再是同一份字节。
func ReadWithScope(path string) (entries []Entry, scope string, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("memory: 读 %s: %w", path, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		// 不单独 TrimSuffix("\r"):ParseLine 与 scopeRe 的 `\s*$` 本来就吃 CRLF
		//(用例 TestReadWithScopeSinglePass 钉着"正文里不残留 \r"这条性质)。
		if i == 0 {
			if m := scopeRe.FindStringSubmatch(line); m != nil {
				scope = strings.TrimSpace(m[1])
				continue
			}
		}
		if e, ok := ParseLine(line); ok {
			entries = append(entries, e)
		}
	}
	return entries, scope, nil
}

// splitLines 按行切分并丢掉末尾换行造成的空串(不改变其余行内容)。
func splitLines(raw string) []string {
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// renderScope 注入用的一行(含换行;空 ⇒ 空串)。
//
// 前缀刻意不是 "  · "(条目行用那个):范围是**小节的说明**,不是一条记忆,
// 用同一个符号会让模型把它当成可引用的事实。
func renderScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return ""
	}
	if len(scope) > MaxScopeBytes {
		// 手改文件写超长时不把注入顶爆(按 rune 边界截,不留半个汉字)。
		scope = truncateRunes(scope, MaxScopeBytes)
	}
	if scope == "" {
		return ""
	}
	return "  [收录范围] " + scope + "\n"
}

// truncateRunes 按字节上限截断但只切在 rune 边界。
func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

// utf8Start 该字节是否是 UTF-8 字符的起始字节。
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
