// Package toolsummary 工具结果的**一行人话摘要**。
//
// 为什么需要它(2026-10-20 优化批次 B1):工具结果是一段 JSON,界面此前直接把它
// 截断显示 —— TUI 给前 160 字符、Web 给前 400 字符。于是 file_read 显示成
// `{"content":"package main\n\nimport (` —— 一个字也没告诉人"它读了多少行"。
// 用户要的是**结论**,不是**开销**。
//
// 为什么放在 internal/(而不是 sdk 或各工具插件):
//   - sdk 是对外契约(插件面),摘要不是契约;
//   - 各工具插件自己实现摘要 = N 份实现各自漂移,且外部工具(插件/MCP)无法参与;
//   - 本包是宿主侧**唯一一份**实现,算好后随事件载荷下发,TUI 与 Web 显示同一句话。
//
// 三条纪律:
//   - **不认识就不猜**:解析不出结论时返回空串,调用方回退到旧的截断行为
//     (不阻塞、不编造);
//   - **错误要当错误**:工具把失败写在结果内容里({"error": …})时,摘要要标明
//     这是错误 —— 否则界面会给一次失败打上 ✓(比"没信息"更误导);
//   - **不带路径全名**:路径在调用行里已经出现过,摘要只说"做了什么"。
package toolsummary

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Result 一条工具结果的展示摘要。
type Result struct {
	// Text 一行摘要(空 = 不认识,调用方回退到原截断行为)。
	Text string
	// Err true = 工具把失败写在了结果内容里(而非错误通道)—— 界面须按错误呈现。
	Err bool
}

// Summarize 由工具名与结果内容算一行摘要。
//
// errText 非空时直接以它为准(那是工具的错误通道);否则解析结果 JSON。
func Summarize(tool, content, errText string) Result {
	if strings.TrimSpace(errText) != "" {
		return Result{Text: oneLine(errText), Err: true}
	}
	m, ok := decode(content)
	if !ok {
		return Result{} // 不是 JSON(极少数工具返回纯文本)→ 不猜
	}
	// 工具把错误写在内容里:先认这个,再谈成功摘要。
	if e, ok := m["error"].(string); ok && strings.TrimSpace(e) != "" {
		return Result{Text: oneLine(e), Err: true}
	}
	if fn, ok := summarizers[tool]; ok {
		if s := fn(m); s != "" {
			return Result{Text: s}
		}
	}
	return Result{Text: generic(m)}
}

// summarizers 按工具名的摘要器(表驱动:新增工具加一行即可,不用改流程)。
var summarizers = map[string]func(map[string]any) string{
	"file_read":    summaryFileRead,
	"file_write":   summaryFileWrite,
	"file_append":  summaryFileWrite,
	"file_edit":    summaryFileEdit,
	"file_list":    summaryFileList,
	"file_glob":    summaryFileGlob,
	"file_grep":    summaryFileGrep,
	"file_mkdir":   summaryFileMkdir,
	"file_move":    summaryFileMove,
	"file_copy":    summaryFileCopy,
	"file_rename":  summaryFileRename,
	"file_delete":  summaryFileDelete,
	"file_restore": summaryFileRestore,
	"shell":        summaryShell,
	"web_fetch":    summaryWebFetch,
	"web_search":   summaryWebSearch,
	"todo":         summaryTodo,
	"image_ops":    summaryImage,
}

func summaryFileRead(m map[string]any) string {
	lines := num(m["lines"])
	body, _ := m["content"].(string)
	s := fmt.Sprintf("读 %s 行 · %s", humanNum(lines), humanBytes(len(body)))
	if trunc, _ := m["truncated"].(bool); trunc {
		s += " · 还有更多"
	}
	return s
}

func summaryFileWrite(m map[string]any) string {
	return fmt.Sprintf("写入 %s", humanBytes(num(m["wrote"])))
}

func summaryFileEdit(m map[string]any) string {
	return fmt.Sprintf("替换 %s 处", humanNum(num(m["replaced"])))
}

func summaryFileList(m map[string]any) string {
	return fmt.Sprintf("列出 %s 项", humanNum(num(m["count"])))
}

func summaryFileGlob(m map[string]any) string {
	return fmt.Sprintf("匹配 %s 个", humanNum(num(m["count"])))
}

func summaryFileGrep(m map[string]any) string {
	files := distinctFiles(m["matches"])
	s := fmt.Sprintf("%s 处命中", humanNum(num(m["count"])))
	if files > 0 {
		s += fmt.Sprintf(" · %s 个文件", humanNum(files))
	}
	return s
}

func summaryFileMkdir(m map[string]any) string { return "建目录" }

func summaryFileMove(m map[string]any) string {
	if b := num(m["bytes"]); b > 0 {
		return fmt.Sprintf("移动 %s", humanBytes(b))
	}
	return "移动"
}

func summaryFileCopy(m map[string]any) string   { return "复制" }
func summaryFileRename(m map[string]any) string { return "重命名" }

func summaryFileDelete(m map[string]any) string {
	verb := "移入回收站"
	if perm, _ := m["permanent"].(bool); perm {
		verb = "永久删除"
	}
	return fmt.Sprintf("%s %s 项", verb, humanNum(num(m["count"])))
}

func summaryFileRestore(m map[string]any) string { return "恢复 1 项" }

func summaryShell(m map[string]any) string {
	out, _ := m["output"].(string)
	n := 0
	if strings.TrimSpace(out) != "" {
		n = len(strings.Split(strings.TrimRight(out, "\n"), "\n"))
	}
	return fmt.Sprintf("输出 %s 行", humanNum(n))
}

func summaryWebFetch(m map[string]any) string {
	status := num(m["status"])
	if status == 0 {
		return ""
	}
	s := fmt.Sprintf("HTTP %d", status)
	if body, ok := m["content"].(string); ok {
		s += " · " + humanBytes(len(body))
	}
	return s
}

func summaryWebSearch(m map[string]any) string {
	return fmt.Sprintf("%s 条结果", humanNum(lenOf(m["results"])))
}

func summaryTodo(m map[string]any) string {
	if id, ok := m["id"].(string); ok && id != "" {
		if st, ok := m["status"].(string); ok && st != "" {
			return fmt.Sprintf("任务 %s · %s", id, st)
		}
		return "任务 " + id
	}
	if n := lenOf(m["tasks"]); n > 0 {
		return fmt.Sprintf("%s 项任务", humanNum(n))
	}
	return ""
}

func summaryImage(m map[string]any) string {
	src, dst := num(m["src_bytes"]), num(m["dst_bytes"])
	if src == 0 && dst == 0 {
		return ""
	}
	return fmt.Sprintf("%s → %s", humanBytes(src), humanBytes(dst))
}

// generic 未知工具的兜底:只说结果里**确实有**的量,不猜语义。
func generic(m map[string]any) string {
	if n, ok := m["count"]; ok {
		return fmt.Sprintf("%s 项", humanNum(num(n)))
	}
	if note, ok := m["note"].(string); ok && strings.TrimSpace(note) != "" {
		return oneLine(note)
	}
	return ""
}

// decode 解析结果 JSON(对象);不是对象则失败。
func decode(content string) (map[string]any, bool) {
	c := strings.TrimSpace(content)
	if c == "" || c[0] != '{' {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(c), &m); err != nil {
		return nil, false
	}
	return m, true
}

// distinctFiles grep 结果里去重的文件数(matches[].path)。
func distinctFiles(v any) int {
	arr, ok := v.([]any)
	if !ok {
		return 0
	}
	seen := map[string]bool{}
	for _, one := range arr {
		m, ok := one.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := m["path"].(string); ok {
			seen[p] = true
		}
	}
	return len(seen)
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func lenOf(v any) int {
	arr, ok := v.([]any)
	if !ok {
		return 0
	}
	return len(arr)
}

// humanNum 千分位(1,204 而不是 1204 —— 摘要里数字要一眼读出量级)。
func humanNum(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// humanBytes 字节短格式(B/KB/MB)。
func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// oneLine 压成单行(摘要必须是**一行**)。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	const max = 120
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
