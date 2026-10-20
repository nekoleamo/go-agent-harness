// read.go:file_read 的分段读取(分页 + 上限)。
//
// 为什么需要它(2026-10-20 优化批次 A1):原实现是 `os.ReadFile` 全文直出 ——
// 一个 10K 行文件 ≈ 100KB ≈ 30–40K token,**一次调用吃掉半个上下文窗口**;
// 而它是历史里最沉、最难压的那块(折叠不得越过最后一轮 ⇒ 只能靠
// clipOldToolResults 从 1200/300 字符硬剪,而"剪"本身又改写前缀中段 ⇒ 缓存失效)。
//
// 行业标配(pi / Codex / claude 的 read)都有 offset+limit;gah 此前没有。
//
// 三条纪律:
//   - **如实说**:截断必须显式告诉模型(truncated + next_offset),不许静默少给;
//   - **默认给得够多**:默认 2000 行(与 pi 一致,模型对它熟),避免"一次读不全"变成常态;
//   - **上限有硬顶**:limit 超上限按上限给并说明,避免模型一次要 100 万行。
package toolfiles

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// 分页默认值与上限。
const (
	// readDefaultLimit 默认返回行数(与 pi 的 read 默认一致:2000 行)。
	readDefaultLimit = 2000
	// readMaxLimit 单次返回行数上限(模型要更多就分段读;硬顶防"一次读整个仓库")。
	readMaxLimit = 20000
	// readMaxBytes 单次返回字节上限(行数够但单行极长/无换行的文件仍要封顶)。
	readMaxBytes = 1 << 20 // 1 MiB
)

// readSegment 读文件的一段(第 offset 行起、最多 limit 行),返回可直接给模型的结果。
//
// 结果形状(给模型的 JSON):
//
//	{"path":…, "content":…, "start_line":…, "end_line":…, "lines":…,
//	 "truncated":bool, "next_offset":int(仅 truncated 时有), "note":…}
//
// offset/limit 语义:offset 为 1 起的行号(≤0 视为 1);limit ≤0 用默认值。
// limit 超上限按上限给(并说明),不报错 —— 报错会让模型放弃读文件。
func readSegment(path string, offset, limit int) (map[string]any, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	if offset < 1 {
		offset = 1
	}
	clamped := false
	if limit <= 0 {
		limit = readDefaultLimit
	}
	if limit > readMaxLimit {
		limit = readMaxLimit
		clamped = true
	}

	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), readMaxBytes) // 单行超长也要读得进来(否则 Scanner 直接报错)
	var (
		lines   []string
		lineNo  int
		bytes   int
		more    bool
		readErr error
	)
	for sc.Scan() {
		lineNo++
		if lineNo < offset {
			continue // 跳过的行照样计入行号(与"起始行"语义一致)
		}
		if len(lines) >= limit {
			more = true // 还有内容没读
			break
		}
		s := sc.Text()
		if bytes+len(s)+1 > readMaxBytes && len(lines) > 0 {
			more = true
			break
		}
		bytes += len(s) + 1
		lines = append(lines, s)
	}
	if err := sc.Err(); err != nil {
		readErr = err
	}

	out := map[string]any{
		"path":       path,
		"content":    strings.Join(lines, "\n"),
		"start_line": offset,
		"end_line":   offset + len(lines) - 1,
		"lines":      len(lines),
		"truncated":  more,
	}
	if more {
		out["next_offset"] = offset + len(lines)
	}
	var notes []string
	if more {
		notes = append(notes, fmt.Sprintf("内容较长,已截断:还有更多,请用 offset=%d 继续读", offset+len(lines)))
	}
	if clamped {
		notes = append(notes, fmt.Sprintf("limit 超过上限,已按 %d 行给", readMaxLimit))
	}
	if len(notes) > 0 {
		out["note"] = strings.Join(notes, ";")
	}
	if readErr != nil {
		// 读出错(比如单行超长):**已读到的部分照给**,并说明出错 —— 全丢等于让模型重读一遍。
		out["error"] = readErr.Error()
	}
	return out, nil
}
