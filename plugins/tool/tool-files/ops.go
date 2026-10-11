// 本文件:F1 文件操作工具(M1)——列举/通配/内容搜索/建目录/移动/复制/重命名/删除(回收站)/恢复。
//
// 三条贯穿全文件的硬纪律:
//
//  1. **写目标一律经 resolve(ctx, …, true)**(与 file_write 同一条裁决链):漏一个就是越界写。
//     读动作经 resolve(…, false);回收站路径是工具内部派生,**不经**裁决(它不是用户给的写目标)。
//  2. **跨卷必须 copy+delete,不能只 rename**(D3):`/sdcard` → `$GAH_HOME`、macOS 外置卷、
//     Windows 跨盘都会让 os.Rename 报 EXDEV。失败时先 rename,失败再 copy+fsync+校验大小+删源,
//     copy 失败**保留源文件**(绝不两头都没)。
//  3. **默认不覆盖**:dst 已存在 ⇒ 报错;`overwrite: true` 才覆盖(D2)。
//     写路径一律「同目录临时文件 + rename」落盘(原子),失败清理临时文件,不留半截。
package toolfiles

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 上限与默认值(D2 定稿):超限**如实说**(还有更多/请收窄),不静默截断。
const (
	listDefaultLimit = 200
	listMaxLimit     = 1000
	globDefaultLimit = 1000
	globMaxLimit     = 5000
	grepDefaultLimit = 100
	grepMaxLimit     = 500
	// grepMaxLine 单行最长返回字符数(超长行截断并标 …):一行几 MB 的 minified 文件会撑爆上下文。
	grepMaxLine = 500
	// grepSkipFileSize 单文件超过此大小直接跳过(二进制/大日志);跳过的数量如实报出。
	grepSkipFileSize = 2 << 20
	// trashKeep / trashMaxBytes 回收站保留份数与总量上限(对齐 roles/skills 的 maxTrashKeep=20)。
	trashKeep = 20
	// trashMetaSuffix 回收站条目的元数据文件后缀(条目名 + 它 = 原路径/删除时间)。
	trashMetaSuffix = ".meta.json"
	trashMaxBytes   = 2 << 30
)

// opsNames F1 新增动作清单(注册与分派共用一份,避免两处漂移)。
var opsNames = []string{
	"file_list", "file_glob", "file_grep", "file_mkdir",
	"file_move", "file_copy", "file_rename", "file_delete",
	"file_trash_list", "file_restore",
}

// isOpsTool 该名是否属于 F1 新增动作(与之相对的是 file_read/write/append/edit 四件套)。
func isOpsTool(name string) bool {
	for _, n := range opsNames {
		if n == name {
			return true
		}
	}
	return false
}

// opsArgs F1 动作的统一入参(各动作只取自己那几个字段)。
// 分开解析而非各写各的:少一次 json.Unmarshal 的样板,字段口径也只有一处。
type opsArgs struct {
	Path       string   `json:"path,omitempty"`
	From       string   `json:"from,omitempty"`
	To         string   `json:"to,omitempty"`
	Name       string   `json:"name,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Pattern    string   `json:"pattern,omitempty"`
	Glob       string   `json:"glob,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Cursor     string   `json:"cursor,omitempty"`
	Entry      string   `json:"entry,omitempty"`
	IgnoreCase bool     `json:"ignore_case,omitempty"`
	Overwrite  bool     `json:"overwrite,omitempty"`
	Permanent  bool     `json:"permanent,omitempty"`
	// Parents *bool 三态:nil = 缺省(连父目录一起建),false = 只建自己(父目录不存在则报错)。
	Parents *bool `json:"parents,omitempty"`
}

// opsSchema 各动作的入参 schema。
func opsSchema(name string) map[string]any {
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	num := map[string]any{"type": "integer"}
	obj := func(required []any, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": props}
	}
	switch name {
	case "file_list":
		return obj([]any{"path"}, map[string]any{
			"path": str, "limit": num, "cursor": str})
	case "file_glob":
		return obj([]any{"path", "pattern"}, map[string]any{
			"path": str, "pattern": str, "limit": num})
	case "file_grep":
		return obj([]any{"path", "pattern"}, map[string]any{
			"path": str, "pattern": str, "glob": str, "ignore_case": boolean, "limit": num})
	case "file_mkdir":
		return obj([]any{"path"}, map[string]any{"path": str, "parents": boolean}) // 缺省=连父目录一起建
	case "file_move", "file_copy":
		return obj([]any{"from", "to"}, map[string]any{
			"from": str, "to": str, "overwrite": boolean})
	case "file_rename":
		return obj([]any{"path", "name"}, map[string]any{
			"path": str, "name": str, "overwrite": boolean})
	case "file_delete":
		return obj([]any{"paths"}, map[string]any{
			"paths": map[string]any{"type": "array", "items": str}, "permanent": boolean})
	case "file_trash_list":
		return obj(nil, map[string]any{"path": str})
	case "file_restore":
		return obj([]any{"entry"}, map[string]any{
			"entry": str, "to": str, "overwrite": boolean})
	}
	return obj(nil, map[string]any{})
}

// opsDesc 工具描述。**上限与破坏性语义必须写在描述里**(D2:模型据此决定下一步),
// 尤其是「默认不覆盖」与「删除进回收站可恢复」——这两条错了就是用户丢文件。
func opsDesc(name string) string {
	switch name {
	case "file_list":
		return "列目录内容:{path}(目录必填),返回 name/type/size/mod_time。" +
			fmt.Sprintf("默认 %d 条/页,limit ≤ %d,cursor 续页;截断时返回 next_cursor。", listDefaultLimit, listMaxLimit)
	case "file_glob":
		return "按通配符递归找文件:{path=根目录, pattern=如 **/*.go 或 *.md}。" +
			fmt.Sprintf("默认最多 %d 条;想收窄就换 pattern。", globDefaultLimit)
	case "file_grep":
		return "在目录里搜文件内容(Go 正则):{path, pattern},可选 glob(只搜匹配文件)、ignore_case、limit。" +
			fmt.Sprintf("默认 %d 条命中,单行截断到 %d 字符,大于 %d KiB 的文件会跳过(跳过数量在结果里报)。",
				grepDefaultLimit, grepMaxLine, grepSkipFileSize>>10)
	case "file_mkdir":
		return "建目录:{path};缺省会连父目录一起建,parents=false 只建自己(父目录不存在会报错)。目录已存在会报错。"
	case "file_move":
		return "移动文件/目录:{from, to}。**默认不覆盖**:to 已存在会报错,确实要覆盖才传 overwrite: true。" +
			"跨设备(如 /sdcard → 数据根)自动改用复制+删除,不会丢文件。"
	case "file_copy":
		return "复制文件/目录:{from, to}。**默认不覆盖**:to 已存在会报错,确实要覆盖才传 overwrite: true。"
	case "file_rename":
		return "重命名(只改名字,同目录内):{path, name}。name 只能是单个文件名,不能带路径分隔符。" +
			"**默认不覆盖**:同名已存在会报错。"
	case "file_delete":
		return "删除文件/目录:{paths}(字符串数组)。默认移入回收站(可用 file_trash_list 查、file_restore 恢复);" +
			"permanent: true 才是真删,不可恢复 —— 除非用户明确要求,否则别用。"
	case "file_trash_list":
		return "列回收站条目:{path 可选=只列某个工作区目录的]};返回原路径/删除时间/大小。" +
			"删除文件后想找回,先看这里。"
	case "file_restore":
		return "从回收站恢复:{entry=条目名};默认恢复到删除时的原路径,to 可指定别处(目录需已存在)。" +
			"目标已存在会报错,确实要覆盖才传 overwrite: true。"
	}
	return name
}

// opsPathParams 路径参数能力声明(宿主 pre-execute 路径沙箱据此裁决;**声明优先**,不许绕开)。
// 读写分开声明:移动/复制的源是读、目标是写 —— 只声明写目标会让源侧少一次裁决。
func opsPathParams(name string) []sdk.PathParam {
	switch name {
	case "file_list", "file_glob", "file_grep":
		return []sdk.PathParam{{Arg: "path", Access: sdk.PathRead, Optional: true}}
	case "file_mkdir":
		return []sdk.PathParam{{Arg: "path", Access: sdk.PathWrite}}
	case "file_move", "file_copy":
		return []sdk.PathParam{{Arg: "from", Access: sdk.PathRead}, {Arg: "to", Access: sdk.PathWrite}}
	case "file_rename":
		return []sdk.PathParam{{Arg: "path", Access: sdk.PathWrite}, {Arg: "name", Access: sdk.PathRead, Optional: true}}
	case "file_delete":
		return []sdk.PathParam{{Arg: "paths", Access: sdk.PathWrite, Many: true}}
	case "file_trash_list":
		return []sdk.PathParam{{Arg: "path", Access: sdk.PathRead, Optional: true}}
	case "file_restore":
		return []sdk.PathParam{{Arg: "to", Access: sdk.PathWrite, Optional: true}}
	}
	return nil
}

// ── 分发与入参校验 ────────────────────────────────────────────────

// execOps F1 动作统一分发。错误一律以 {"error": …} 返回(与四件套同口径):
// 模型看得见文案才改得了下一步,工具层不该把「目标已存在」变成一次内部异常。
func (f *FilesTool) execOps(ctx context.Context, name, raw string) (any, error) {
	var a opsArgs
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return opErr("%s: args: %v", name, err), nil
		}
	}
	switch name {
	case "file_list":
		return f.opList(ctx, name, a)
	case "file_glob":
		return f.opGlob(ctx, name, a)
	case "file_grep":
		return f.opGrep(ctx, name, a)
	case "file_mkdir":
		return f.opMkdir(ctx, name, a)
	case "file_move":
		return f.opMove(ctx, name, a)
	case "file_copy":
		return f.opCopy(ctx, name, a)
	case "file_rename":
		return f.opRename(ctx, name, a)
	case "file_delete":
		return f.opDelete(ctx, name, a)
	case "file_trash_list":
		return f.opTrashList(ctx, name, a)
	case "file_restore":
		return f.opRestore(ctx, name, a)
	}
	return opErr("未知文件操作 %s", name), nil
}

// opErr 统一错误出口(与四件套的 map[string]any{"error": …} 同形)。
func opErr(format string, args ...any) map[string]any {
	return map[string]any{"error": fmt.Sprintf(format, args...)}
}

// opFail 动作失败出口:补上「为什么」与可行动的出路。
func opFail(action, p string, err error) map[string]any {
	return map[string]any{"error": fmt.Sprintf("%s %s: %v", action, p, err)}
}

// parseLimit 取页长:0/缺省 → 默认;超上限 → 夹到上限(并在结果里如实说明,见 note)。
func parseLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// parseCursor 游标 = 已列出的条目数(目录读取按文件名排序,顺序稳定)。
func parseCursor(s string, total int) (int, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("cursor 只能是 0..%d 的整数,得 %q", total, s)
	}
	if n > total {
		return 0, fmt.Errorf("cursor=%d 越界(当前共 %d 条,用 0 重新开始)", n, total)
	}
	return n, nil
}

// entryKind 条目类型(目录判定按 lstat 所得的链接本身,避免符号链接指向目录被误当目录)。
func entryKind(d fs.DirEntry) string {
	switch {
	case d.IsDir():
		return "dir"
	case d.Type()&fs.ModeSymlink != 0:
		return "symlink"
	default:
		return "file"
	}
}

// statFields 大小/修改时间(取不到就省掉,不让一次 stat 失败毁掉整条结果)。
func statFields(d fs.DirEntry) (size int64, mod string) {
	info, err := d.Info()
	if err != nil {
		return 0, ""
	}
	return info.Size(), info.ModTime().Format(time.RFC3339)
}

// ── file_list ────────────────────────────────────────────────────

func (f *FilesTool) opList(ctx context.Context, tool string, a opsArgs) (any, error) {
	dir, err := f.resolve(ctx, orDot(a.Path), false)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	entries, err := os.ReadDir(dir) // 已按文件名排序 ⇒ 游标语义稳定
	if err != nil {
		return opFail("列目录", dir, err), nil
	}
	start, cerr := parseCursor(a.Cursor, len(entries))
	if cerr != nil {
		return map[string]any{"error": cerr.Error()}, nil
	}
	limit := parseLimit(a.Limit, listDefaultLimit, listMaxLimit)
	end := min(start+limit, len(entries))
	items := make([]any, 0, end-start)
	for _, d := range entries[start:end] {
		size, mod := statFields(d)
		it := map[string]any{
			"name": d.Name(), "type": entryKind(d), "path": filepath.Join(dir, d.Name()),
		}
		if d.Type().IsRegular() {
			it["size"] = size
		}
		if mod != "" {
			it["mod_time"] = mod
		}
		items = append(items, it)
	}
	res := map[string]any{"path": dir, "count": len(items), "entries": items}
	if end < len(entries) {
		res["next_cursor"] = strconv.Itoa(end)
		res["note"] = fmt.Sprintf("还有 %d 条未列出:传 cursor=%q 继续,或把 limit 调大(上限 %d)",
			len(entries)-end, strconv.Itoa(end), listMaxLimit)
	}
	return res, nil
}

func orDot(p string) string {
	if strings.TrimSpace(p) == "" {
		return "."
	}
	return p
}

// expandGlob 把常见 shell 风格写法展开成 path.Match 能用的候选集。
//
// 为什么必须有这一步:工具描述里推荐的 `**/*.go`,在 Go 的 `path.Match` 里**永远不中**
// (`*` 不跨 `/`,而 `**` 也不是它的特殊语法)。模型照着描述写,拿到的是空结果 ——
// 它会由此认为"这个目录里没有这类文件",而不是"我的写法不被支持"。
// 展开规则:原样 → 去掉 `**/` 前缀 → 把 `/**/` 换成 `/` → 去掉 `./` 前缀。
// 效果上 `**` 一律解释成"任意深度"(与 shell 的 globstar 同义);候选集里同时留着原样写法,
// 于是"恰好一层"的写法也不会被误伤。
func expandGlob(pattern string) []string {
	out := []string{pattern}
	q := pattern
	if strings.HasPrefix(q, "**/") {
		q = strings.TrimPrefix(q, "**/")
		out = append(out, q)
	}
	if strings.Contains(q, "/**/") {
		out = append(out, strings.ReplaceAll(q, "/**/", "/"))
	}
	if strings.HasPrefix(q, "./") {
		out = append(out, strings.TrimPrefix(q, "./"))
	}
	return out
}

// matchGlob 通配匹配:**相对路径**或**基名**任一命中即算命中。
//
// 基名这一条是为了 `*.go` 这种"我只要这类文件"的直觉写法(`a/b.go` 对 `*.go`
// 在 path.Match 下不命中,那是 Go 的语义而不是用户的意图)。
func matchGlob(pattern, rel, base string) bool {
	for _, p := range expandGlob(pattern) {
		if ok, err := path.Match(p, rel); err == nil && ok {
			return true
		}
		if ok, err := path.Match(p, base); err == nil && ok {
			return true
		}
	}
	return false
}

// ── file_glob ────────────────────────────────────────────────────

func (f *FilesTool) opGlob(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.Pattern) == "" {
		return opErr("file_glob: 需要 pattern(如 **/*.go)"), nil
	}
	if _, err := path.Match(a.Pattern, "probe"); err != nil {
		return opErr("file_glob: pattern 不是有效通配符 %q: %v", a.Pattern, err), nil
	}
	root, err := f.resolve(ctx, orDot(a.Path), false)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	limit := parseLimit(a.Limit, globDefaultLimit, globMaxLimit)
	matches := make([]string, 0, 16)
	truncated := false
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 不可读的子目录不该让整次搜索失败(手机/容器目录里太常见):跳过并继续。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchGlob(a.Pattern, rel, d.Name()) {
			if len(matches) >= limit {
				truncated = true
				return filepath.SkipAll
			}
			matches = append(matches, p)
		}
		return nil
	})
	if walkErr != nil {
		return opFail("遍历", root, walkErr), nil
	}
	res := map[string]any{"root": root, "pattern": a.Pattern, "count": len(matches), "matches": matches}
	if truncated {
		res["truncated"] = true
		res["note"] = fmt.Sprintf("命中超过 %d 条,已截断:收窄 pattern 或分目录找", limit)
	}
	return res, nil
}

// ── file_grep ────────────────────────────────────────────────────

// isBinaryText 快速二进制判定(前 8 KiB 内出现 NUL 即判二进制;与 sdk.IsBinaryText 同思路,
// 这里自带一份是为了不把整文件读进内存)。
func isBinaryHead(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// clipLine 单行截断到 grepMaxLine 个字符(按 rune 切,不切碎多字节字符)。
//
// 用 range 的字节下标切而不是 []rune 转换:后者会把整行再复制一份 —— 一行几 MB 的
// minified 文件只为了截到 500 字符而复制全文,不值得。
func clipLine(s string) (string, bool) {
	n := 0
	for j := range s {
		if n == grepMaxLine {
			return s[:j] + "…", true
		}
		n++
	}
	return s, false
}

func (f *FilesTool) opGrep(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.Pattern) == "" {
		return opErr("file_grep: 需要 pattern(Go 正则,如 func\\\\s+main)"), nil
	}
	expr := a.Pattern
	if a.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return opErr("file_grep: pattern 不是有效正则 %q: %v", a.Pattern, err), nil
	}
	if a.Glob != "" {
		if _, err := path.Match(a.Glob, "probe"); err != nil {
			return opErr("file_grep: glob 不是有效通配符 %q: %v", a.Glob, err), nil
		}
	}
	root, err := f.resolve(ctx, orDot(a.Path), false)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	limit := parseLimit(a.Limit, grepDefaultLimit, grepMaxLimit)
	type hit struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	hits := make([]hit, 0, 16)
	scanned, skipLarge, skipBinary, clipped := 0, 0, 0, 0
	truncated := false
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(hits) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		if a.Glob != "" {
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			if !matchGlob(a.Glob, filepath.ToSlash(rel), d.Name()) {
				return nil
			}
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > grepSkipFileSize {
			skipLarge++
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if isBinaryHead(b) {
			skipBinary++
			return nil
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			if len(hits) >= limit {
				truncated = true
				return filepath.SkipAll
			}
			line = strings.TrimSuffix(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			text, wasClipped := clipLine(line)
			if wasClipped {
				clipped++
			}
			hits = append(hits, hit{Path: p, Line: i + 1, Text: text})
		}
		return nil
	})
	if walkErr != nil {
		return opFail("遍历", root, walkErr), nil
	}
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{"path": h.Path, "line": h.Line, "text": h.Text})
	}
	res := map[string]any{
		"root": root, "pattern": a.Pattern, "count": len(out), "matches": out,
		"files_scanned": scanned, "skipped_large": skipLarge, "skipped_binary": skipBinary,
	}
	if clipped > 0 {
		res["clipped_lines"] = clipped
		res["line_max_chars"] = grepMaxLine
	}
	if truncated {
		res["truncated"] = true
		res["note"] = fmt.Sprintf("命中超过 %d 条,已截断:收窄 pattern/glob,或提高 limit(上限 %d)", limit, grepMaxLimit)
	}
	return res, nil
}

// ── file_mkdir ───────────────────────────────────────────────────

func (f *FilesTool) opMkdir(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.Path) == "" {
		return opErr("file_mkdir: 需要 path"), nil
	}
	p, err := f.resolve(ctx, a.Path, true)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if _, err := os.Lstat(p); err == nil {
		return opErr("目标已存在: %s(要新建请换个名字;只想确保父目录存在的话用 mkdir 的 parents)", p), nil
	} else if !os.IsNotExist(err) {
		return opFail("检查", p, err), nil
	}
	if a.Parents == nil || *a.Parents {
		err = os.MkdirAll(p, 0o755)
	} else {
		err = os.Mkdir(p, 0o755)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			return opErr("父目录不存在: %s(要么先建,要么传 parents: true)", filepath.Dir(p)), nil
		}
	}
	if err != nil {
		return opFail("建目录", p, err), nil
	}
	f.recordRaw(p, "mkdir", tool, true)
	return map[string]any{"path": p, "created": true}, nil
}

// ── file_move / file_copy / file_rename ──────────────────────────

// srcFor 取并校验源:不存在/是坏链接都要有可行动的文案。
func srcFor(from string) (fs.FileInfo, error) {
	fi, err := os.Lstat(from)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("源不存在: %s", from)
		}
		return nil, err
	}
	return fi, nil
}

// checkDst 目标占位检查(D2「默认不覆盖」的唯一落点):
//   - 不存在 → 放行;
//   - 存在且未 overwrite → 拒绝,并告诉模型怎么改;
//   - 存在且 overwrite=true → 放行,但**此函数不删它**:删除推迟到落盘提交那一刻
//     (见 copyFile/copyTree 的 commit)。提前删会让"复制失败"把旧目标也赔进去 ——
//     一次失败的操作不该毁掉用户原本就在的文件。
func checkDst(to string, overwrite bool) error {
	_, err := os.Lstat(to)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !overwrite {
		return fmt.Errorf("目标已存在: %s(确认要覆盖就传 overwrite: true,或换一个目标名)", to)
	}
	return nil
}

// commitDst 提交替换:先删旧目标再 rename 临时产物。
//
// 只在**内容已经完整落好**(大小校验过、fsync 过)之后才走到这一步,所以"删旧"这一步
// 失败或崩掉,最坏结果是新文件没就位、旧文件还在 —— 而不是两头都没。
// Windows 上 rename 不能覆盖已存在的文件,故删这一步不是"可选优化"而是必需。
func commitDst(tmp, to string) error {
	if _, err := os.Lstat(to); err == nil {
		if err := sdk.RemoveTree(to); err != nil {
			return fmt.Errorf("提交替换失败(删不掉旧目标 %s): %w", to, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, to)
}

// renameHook = os.Rename 的注入点(单测用它模拟跨卷 EXDEV;生产恒为 os.Rename)。
var renameHook = os.Rename

// moveAny 移动:**先试 rename(同卷最快、原子),失败则 copy+校验+提交+删源**。
//
// 为什么必须兜底 copy(D3):`/sdcard` → `$GAH_HOME`、macOS 外置卷、Windows 跨盘一律 EXDEV,
// 只 rename 的实现 100% 会在这些场景失败或丢文件。copy 失败**保留源文件**,绝不两头都没。
// 返回值 crossDevice=true 表示走的是复制路径(工具结果里如实告诉模型"这是跨设备移动")。
func moveAny(from, to string) (crossDevice bool, err error) {
	if err := renameHook(from, to); err == nil {
		return false, nil
	}
	renameErr := err
	if err := copyAny(from, to); err != nil {
		return true, fmt.Errorf("跨设备移动失败(rename: %v): %w;**源文件仍在原处未删**", renameErr, err)
	}
	if err := sdk.RemoveTree(from); err != nil {
		return true, fmt.Errorf("已复制到 %s,但删源失败(两边都有,请手工确认): %w", to, err)
	}
	return true, nil
}

// copyAny 复制(文件/目录)。dst 的存在性由调用方 checkDst 处理掉。
func copyAny(from, to string) error {
	fi, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if fi.Mode().IsRegular() {
		return copyFile(from, to, fi)
	}
	if fi.IsDir() {
		return copyTree(from, to, fi)
	}
	// 符号链接与特殊文件(fifo/socket/设备)**不猜**。猜错的后果不对称:
	//  · 对符号链接 os.Open 会跟到目标,把目标内容复制成普通文件(模型看到的是"复制成功了",
	//    而链接关系已经没了)—— 更糟的是随后的体积校验必然失败,报一句与事实无关的错;
	//  · 对 fifo,os.Open 会**永久阻塞**(没有写端),整个回合卡死。
	// 移动这两类东西不需要复制,rename 本身就行,所以出路是明确的。
	return fmt.Errorf("%s 不是普通文件或目录(符号链接/设备/fifo/socket);"+
		"复制这类目标请改用 file_move(同一设备内),或先让模型说明要的是链接还是内容",
		filepath.Base(from))
}

// copyFile 文件复制:**同目录临时文件 → fsync → 校验大小 → 提交替换**。
// 中途任何失败都清掉临时文件,绝不把半截文件留在目标位置(D2「写一律原子」)。
func copyFile(from, to string, fi fs.FileInfo) error {
	tmp, err := os.CreateTemp(filepath.Dir(to), "."+filepath.Base(to)+".gah-tmp-*")
	if err != nil {
		return fmt.Errorf("在目标目录建临时文件失败: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if name != "" {
			_ = os.Remove(name)
		}
	}()
	src, err := os.Open(from)
	if err != nil {
		tmp.Close()
		return err
	}
	_, cerr := io.Copy(tmp, src)
	src.Close()
	if cerr == nil {
		cerr = tmp.Sync()
	}
	if cerr2 := tmp.Close(); cerr == nil {
		cerr = cerr2
	}
	if cerr != nil {
		return fmt.Errorf("复制 %s → %s: %w", from, to, cerr)
	}
	// 校验大小:不校验的话,「复制成功但文件被截断」这类问题要等到用户打开文件才发现。
	got, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("复制校验(读临时文件大小)失败: %w", err)
	}
	if got.Size() != fi.Size() {
		return fmt.Errorf("复制校验失败:源 %d 字节,落地 %d 字节(未替换目标,源文件未动)", fi.Size(), got.Size())
	}
	// 权限与时间尽力保留(失败不致命:内容已经对了)。
	if err := os.Chmod(name, fi.Mode().Perm()); err != nil {
		fmt.Fprintf(os.Stderr, "tool-files: 保留权限位失败 %s: %v\n", to, err)
	}
	if err := os.Chtimes(name, fi.ModTime(), fi.ModTime()); err != nil {
		fmt.Fprintf(os.Stderr, "tool-files: 保留修改时间失败 %s: %v\n", to, err)
	}
	if err := commitDst(name, to); err != nil {
		return err
	}
	name = "" // 已落位:不再清理
	return nil
}

// copyTree 目录复制:**先落到同级临时目录,再整体换名**(与文件复制同一个提交口径)。
//
// 为什么不逐项写进目标目录:那会让"覆盖一个已存在的目录"变成**合并** —— 目标里源没有的
// 旧文件会原封不动留下,而调用方(模型)以为自己拿到的是源的一份拷贝。先在临时目录里拼出
// 完整的一棵,再一次性换名,失败/中断最多影响临时目录。
func copyTree(from, to string, fi fs.FileInfo) error {
	parent := filepath.Dir(to)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(to)+".gah-tmp-*")
	if err != nil {
		return fmt.Errorf("在目标目录建临时目录失败: %w", err)
	}
	defer func() { _ = sdk.RemoveTree(staging) }() // 换名成功后 RemoveTree 是空操作
	if err := fillTree(from, staging); err != nil {
		return err
	}
	// 目录的权限(含 setgid 之类)搬过来;失败只提示:内容已经对了。
	if err := os.Chmod(staging, fi.Mode().Perm()); err != nil {
		fmt.Fprintf(os.Stderr, "tool-files: 保留目录权限位失败 %s: %v\n", to, err)
	}
	return commitDst(staging, to)
}

// fillTree 把 from 的内容逐项填进已存在的 dst 目录(不碰 dst 自身)。
func fillTree(from, dst string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src := filepath.Join(from, e.Name())
		dst := filepath.Join(dst, e.Name())
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsRegular():
			if err := copyFile(src, dst, info); err != nil {
				return err
			}
		case info.IsDir():
			if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
				return err
			}
			if err := fillTree(src, dst); err != nil {
				return err
			}
		default:
			// 与 copyAny 同一口径:符号链接/特殊文件不猜(见那里的理由)
			return fmt.Errorf("%s 不是普通文件或目录(符号链接/设备/fifo/socket);"+
				"目录复制不处理这类条目(要整体搬动请用 file_move)", src)
		}
	}
	return nil
}

func (f *FilesTool) opMove(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.From) == "" || strings.TrimSpace(a.To) == "" {
		return opErr("file_move: 需要 from 与 to"), nil
	}
	// 源侧按**写**裁决:移动会删掉源(只按读裁决会让 read-only 档下移动"看起来合法")。
	from, err := f.resolve(ctx, a.From, true)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	to, err := f.resolve(ctx, a.To, true)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if from == to {
		return opErr("源与目标是同一路径,无需移动: %s", from), nil
	}
	if _, err := srcFor(from); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if err := checkDst(to, a.Overwrite); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	cross, err := moveAny(from, to)
	if err != nil {
		return opFail("移动", from+" → "+to, err), nil
	}
	f.recordRaw(to, "move", tool, true)
	f.recordRaw(from, "move", tool, false)
	res := map[string]any{"from": from, "to": to, "cross_device": cross}
	if cross {
		res["note"] = "跨设备移动:走的是复制+删除(源与目标不在同一文件系统)"
	}
	return res, nil
}

func (f *FilesTool) opCopy(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.From) == "" || strings.TrimSpace(a.To) == "" {
		return opErr("file_copy: 需要 from 与 to"), nil
	}
	from, err := f.resolve(ctx, a.From, false)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	to, err := f.resolve(ctx, a.To, true)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if from == to {
		return opErr("源与目标是同一路径: %s", from), nil
	}
	fi, err := srcFor(from)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if err := checkDst(to, a.Overwrite); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if err := copyAny(from, to); err != nil {
		return opFail("复制", from+" → "+to, err), nil
	}
	f.recordRaw(to, "copy", tool, true)
	return map[string]any{"from": from, "to": to, "bytes": fi.Size()}, nil
}

// validBaseName 重命名的新名:单个文件名,不带任何分隔符。
func validBaseName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("name 不能为空")
	case name == "." || name == "..":
		return fmt.Errorf("name 不能是 %s", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("name 只是**文件名**,不能带路径分隔符(要换目录请用 file_move): %s", name)
	case strings.ContainsRune(name, 0):
		return fmt.Errorf("name 含非法字符")
	case len(name) > 255:
		return fmt.Errorf("name 太长(%d 字节)", len(name))
	}
	return nil
}

func (f *FilesTool) opRename(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.Path) == "" {
		return opErr("file_rename: 需要 path"), nil
	}
	if err := validBaseName(a.Name); err != nil {
		return opErr("file_rename: %v", err), nil
	}
	p, err := f.resolve(ctx, a.Path, true)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if _, err := srcFor(p); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	to := filepath.Join(filepath.Dir(p), a.Name)
	if to == p {
		return opErr("新名与原名相同: %s", a.Name), nil
	}
	if err := checkDst(to, a.Overwrite); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if err := renameHook(p, to); err != nil {
		// 同目录改名不该 EXDEV;真出现了也走复制路径,别把"改个名"变成数据丢失。
		if cross, cerr := moveAny(p, to); cerr != nil {
			return opFail("重命名", p+" → "+to, cerr), nil
		} else if cross {
			return opErr("重命名 %s → %s 走了跨设备复制路径(异常:同目录改名本不该跨设备)", p, to), nil
		}
	}
	f.recordRaw(to, "rename", tool, true)
	f.recordRaw(p, "rename", tool, false)
	return map[string]any{"from": p, "to": to}, nil
}

// ── file_delete / 回收站 ──────────────────────────────────────────

// trashMeta 回收站条目的原路径信息(删掉条目目录后元数据就没了,恢复要靠它)。
type trashMeta struct {
	Original  string    `json:"original"`
	DeletedAt time.Time `json:"deleted_at"`
	Size      int64     `json:"size"`
	SizeKnown bool      `json:"size_known"`
}

// trashProjectKey 回收站分区键:按**本次工作根**分目录,避免不同项目的删除物混在一处。
func (f *FilesTool) trashProjectKey(ctx context.Context) string {
	base := ""
	if h, ok := sdk.SandboxHintOf(ctx); ok {
		base = h.Root
	}
	if base == "" && f.sb != nil {
		base = f.sb.Root()
	}
	if base == "" {
		home := sdk.Home()
		if home == "" {
			home = "gah"
		}
		base = home
	}
	return trashKeyFor(base)
}

// trashKeyFor 给定一个根算分区键(名 = 净化后的基名 + 短哈希,哈希取自完整路径)。
//
// **刻意与 trashProjectKey 分开**:按路径筛选回收站时要用"那个路径自己的 key",
// 而不是"当前工作根的 key" —— 两者混用会让筛选恒为空(模型看到"回收站里什么都没有",
// 而条目其实就在里面)。这是上一版的真缺陷。
func trashKeyFor(base string) string {
	base = filepath.Clean(base)
	sum := sha256.Sum256([]byte(base))
	safe := sanitizeName(filepath.Base(base))
	if safe == "" {
		safe = "root"
	}
	return safe + "-" + hex.EncodeToString(sum[:3])
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// trashEntryName 回收站条目名:时间戳前缀让"按名字倒序 = 按时间倒序"成立(与 roles/skills 同口径)。
func trashEntryName(orig string) string {
	stem := sanitizeName(filepath.Base(orig))
	if stem == "" {
		stem = "entry"
	}
	rnd := make([]byte, 2)
	if _, err := rand.Read(rnd); err != nil {
		rnd = []byte{0, 0}
	}
	prefix := time.Now().UTC().Format("20060102T150405Z")
	return prefix + "-" + hex.EncodeToString(rnd) + "-" + stem
}

func (f *FilesTool) trashDirFor(ctx context.Context) string {
	return filepath.Join(sdk.TrashDir(), f.trashProjectKey(ctx))
}

// trashPut 移入回收站:落点 $GAH_HOME/trash/<project-key>/<时间戳>-<原名>,并写元数据。
// 跨卷同样安全(移动到数据根多为跨设备):走 moveAny 的 copy+delete。
func (f *FilesTool) trashPut(ctx context.Context, p string) (entry string, abs string, err error) {
	dir := f.trashDirFor(ctx)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("建回收站目录 %s 失败: %w", dir, err)
	}
	for i := 0; i < 8; i++ { // 同毫秒 + 同名(目录)时靠随机后缀区分
		entry = trashEntryName(p)
		abs = filepath.Join(dir, entry)
		if _, err := os.Lstat(abs); os.IsNotExist(err) {
			break
		}
		entry, abs = "", ""
	}
	if entry == "" {
		return "", "", fmt.Errorf("回收站里找不到可用条目名(目录 %s)", dir)
	}
	if _, err := moveAny(p, abs); err != nil {
		return "", "", err
	}
	var size int64
	known := false
	if fi, err := os.Lstat(abs); err == nil && fi.Mode().IsRegular() {
		size, known = fi.Size(), true
	}
	meta := trashMeta{Original: p, DeletedAt: time.Now(), Size: size, SizeKnown: known}
	if b, err := json.Marshal(meta); err == nil {
		if err := writeFileAtomic(filepath.Join(dir, entry+trashMetaSuffix), b); err != nil {
			fmt.Fprintf(os.Stderr, "tool-files: 回收站元数据写入失败(%s 的原路径将无法自动还原): %v\n", p, err)
		}
	}
	pruneTrash(dir)
	return entry, abs, nil
}

// writeFileAtomic 同目录临时文件 + rename(D2:写一律原子)。
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".gah-tmp"
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(b); err != nil {
		fh.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// pruneTrash 只保留最近 trashKeep 份(名字倒序 = 时间倒序;对齐 roles/skills 的 maxTrashKeep)。
// 条目**可能是文件也可能是目录**(删单个文件最常见),两者都算一份;元数据文件不单独计份。
// 淘汰失败只提示不阻断:删除本身已经成功,回收站轮转不该被当成删除失败。
func pruneTrash(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), trashMetaSuffix) {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) <= trashKeep {
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[trashKeep:] {
		if err := sdk.RemoveTree(filepath.Join(dir, n)); err != nil {
			fmt.Fprintf(os.Stderr, "tool-files: 回收站轮转删除 %s 失败: %v\n", n, err)
		}
		_ = os.Remove(filepath.Join(dir, n+trashMetaSuffix))
	}
}

// trashDirs 列全部回收站分区(恢复时按条目名跨分区找)。
func trashDirs() ([]string, error) {
	roots, err := os.ReadDir(sdk.TrashDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(roots))
	for _, e := range roots {
		if e.IsDir() {
			out = append(out, filepath.Join(sdk.TrashDir(), e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *FilesTool) opDelete(ctx context.Context, tool string, a opsArgs) (any, error) {
	if len(a.Paths) == 0 {
		return opErr("file_delete: 需要 paths(字符串数组,可多条)"), nil
	}
	results := make([]any, 0, len(a.Paths))
	failed := 0
	for _, raw := range a.Paths {
		if strings.TrimSpace(raw) == "" {
			results = append(results, map[string]any{"path": raw, "error": "空路径"})
			failed++
			continue
		}
		p, err := f.resolve(ctx, raw, true)
		if err != nil {
			results = append(results, map[string]any{"path": raw, "error": err.Error()})
			failed++
			continue
		}
		if _, err := srcFor(p); err != nil {
			results = append(results, map[string]any{"path": p, "error": err.Error()})
			failed++
			continue
		}
		if a.Permanent {
			if err := sdk.RemoveTree(p); err != nil {
				results = append(results, map[string]any{"path": p, "error": fmt.Sprintf("永久删除失败: %v", err)})
				failed++
				continue
			}
			f.recordRaw(p, "delete", tool, false)
			results = append(results, map[string]any{"path": p, "deleted": "permanent"})
			continue
		}
		entry, abs, err := f.trashPut(ctx, p)
		if err != nil {
			results = append(results, map[string]any{"path": p, "error": fmt.Sprintf("移入回收站失败(**文件仍在原处**): %v", err)})
			failed++
			continue
		}
		f.recordRaw(p, "delete", tool, false)
		results = append(results, map[string]any{"path": p, "deleted": "trash", "entry": entry, "trash_path": abs})
	}
	res := map[string]any{"count": len(results), "permanent": a.Permanent, "results": results}
	if failed > 0 {
		res["failed"] = failed
		res["note"] = "部分条目失败:逐条 error 字段说明了原因,成功的条目已生效"
	} else if !a.Permanent {
		res["note"] = "已移入回收站:用 file_trash_list 查、file_restore 恢复(" +
			fmt.Sprintf("每分区保留最近 %d 份)", trashKeep)
	}
	return res, nil
}

func (f *FilesTool) opTrashList(ctx context.Context, tool string, a opsArgs) (any, error) {
	dirs, err := trashDirs()
	if err != nil {
		return opFail("读回收站", sdk.TrashDir(), err), nil
	}
	if strings.TrimSpace(a.Path) != "" {
		// 指定了路径 = 只看那个目录自己的分区。key 必须**按解析后的路径**算,
		// 不能用当前工作根的 key —— 否则筛选几乎总是空的。
		p, err := f.resolve(ctx, a.Path, false)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		dirs = filterTrashDirs(dirs, trashKeyFor(p))
	}
	entries := make([]any, 0, 16)
	var totalBytes int64
	for _, dir := range dirs {
		items, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range items {
			if strings.HasSuffix(e.Name(), trashMetaSuffix) { // 元数据文件本身不是条目
				continue
			}
			abs := filepath.Join(dir, e.Name())
			it := map[string]any{"entry": e.Name(), "trash_path": abs, "project": filepath.Base(dir)}
			var meta trashMeta
			if b, err := os.ReadFile(filepath.Join(dir, e.Name()+trashMetaSuffix)); err == nil {
				if json.Unmarshal(b, &meta) == nil {
					it["original"] = meta.Original
					it["deleted_at"] = meta.DeletedAt.Format(time.RFC3339)
					if meta.SizeKnown {
						it["size"] = meta.Size
						totalBytes += meta.Size
					}
				}
			}
			if fi, err := os.Lstat(abs); err == nil {
				if fi.IsDir() {
					it["type"] = "dir"
				} else {
					it["type"] = "file"
				}
			}
			entries = append(entries, it)
		}
	}
	res := map[string]any{"count": len(entries), "entries": entries, "trash_dir": sdk.TrashDir()}
	if totalBytes > trashMaxBytes {
		res["note"] = fmt.Sprintf("回收站已知大小 %.1f MiB,超过 %d GiB 软上限:请用 file_restore 取回不用的,或直接清理 %s",
			float64(totalBytes)/(1<<20), trashMaxBytes>>30, sdk.TrashDir())
	}
	return res, nil
}

// filterTrashDirs 保留指定分区(分区名精确匹配)。
func filterTrashDirs(dirs []string, key string) []string {
	out := dirs[:0:0]
	for _, d := range dirs {
		if filepath.Base(d) == key {
			out = append(out, d)
		}
	}
	return out
}

func (f *FilesTool) opRestore(ctx context.Context, tool string, a opsArgs) (any, error) {
	if strings.TrimSpace(a.Entry) == "" {
		return opErr("file_restore: 需要 entry(条目名,用 file_trash_list 查)"), nil
	}
	dirs, err := trashDirs()
	if err != nil {
		return opFail("读回收站", sdk.TrashDir(), err), nil
	}
	// 条目名必须是**回收站内的单个名字**:带分隔符/.. 的一律拒。
	// 上一版接受"回收站内的路径"看着方便,实际给了模型一条"用 ../ 拼出任意路径再被移动"
	// 的口子 —— 而这个动作根本不经过沙箱裁决(回收站是工具内部区域)。
	name := strings.TrimSpace(a.Entry)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return opErr("file_restore: entry 只是回收站里的**条目名**(不含路径分隔符):%q;"+
			"条目名见 file_trash_list 的 entry 字段", a.Entry), nil
	}
	var found []string
	for _, dir := range dirs {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			found = append(found, filepath.Join(dir, name))
		}
	}
	switch len(found) {
	case 0:
		return opErr("回收站里没有条目 %q:用 file_trash_list 看现有条目(超龄条目按最近 %d 份轮转淘汰)", name, trashKeep), nil
	case 1:
	default:
		return opErr("回收站里有多个同名条目 %q,无法判断恢复哪一个(同名条目分属不同工作区)", name), nil
	}
	src := found[0]
	dir := filepath.Dir(src)
	meta := trashMeta{}
	if b, err := os.ReadFile(filepath.Join(dir, filepath.Base(src)+trashMetaSuffix)); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	dst, err := f.restoreTarget(ctx, a, meta)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if err := checkDst(dst, a.Overwrite); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	cross, err := moveAny(src, dst)
	if err != nil {
		return opFail("恢复", src+" → "+dst, err), nil
	}
	_ = os.Remove(filepath.Join(dir, filepath.Base(src)+trashMetaSuffix))
	f.recordRaw(dst, "restore", tool, true)
	res := map[string]any{"entry": filepath.Base(src), "from": src, "to": dst, "cross_device": cross}
	if meta.Original != "" {
		res["original"] = meta.Original
	}
	return res, nil
}

// restoreTarget 恢复目标:显式 to > 元数据里的原路径(经写裁决);两者都没有 ⇒ 报错(不许瞎猜位置)。
func (f *FilesTool) restoreTarget(ctx context.Context, a opsArgs, meta trashMeta) (string, error) {
	if strings.TrimSpace(a.To) != "" {
		p, err := f.resolve(ctx, a.To, true)
		if err != nil {
			return "", err
		}
		// to 指向已存在的目录 ⇒ 落成 <目录>/<原名>;否则视为目标全路径。
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			base := filepath.Base(meta.Original)
			if base == "" || base == "." || base == string(filepath.Separator) {
				base = "restored"
			}
			return filepath.Join(p, base), nil
		}
		return p, nil
	}
	if strings.TrimSpace(meta.Original) == "" {
		return "", fmt.Errorf("该回收站条目没有原路径记录(元数据缺失):请用 to 指定恢复位置")
	}
	p, err := f.resolve(ctx, meta.Original, true)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil {
		return "", fmt.Errorf("原位置的父目录已不在(%s):恢复不了到原处,请用 to 指定新位置", filepath.Dir(p))
	} else if !fi.IsDir() {
		return "", fmt.Errorf("原位置的父路径已变成文件(%s):请用 to 指定新位置", filepath.Dir(p))
	}
	return p, nil
}

// recordRaw 记一条**不含内容差异**的变更(建目录/移动/复制/删除/恢复)。
//
// 为什么不为这类操作读文件内容:一次 200 MB 的复制会把内容读进内存只为生成一条 diff,
// 而 BuildFileChange 对空 before/after 的处理正好就是「无内容差异」—— op 与 path 已经说清发生了什么。
func (f *FilesTool) recordRaw(path, op, tool string, created bool) {
	ev := sdk.BuildFileChange(path, f.relPath(path), op, tool, created, "", "")
	f.emitChange(ev, path)
}
