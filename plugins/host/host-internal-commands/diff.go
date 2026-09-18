package hostintcmd

// diff.go:/diff 变更审查面(S-P1-1,对标 Codex CLI `/diff` + 内联 patch)。
//
// 数据源:会话账本里的 `file/change` 事件(由 tool-files 在写盘前后自取内容算出 unified diff)。
// 三条纪律:
//  1. **不依赖 git** —— 只看自己捕获的写操作,不读 git 工作区状态:工作区无仓库时同样可用,
//     也不会把用户未提交的改动混进「本次会话改了什么」;
//  2. **不猜测** —— 没捕获到改动就说没捕获到,不拿 `git status` 之类的旁证替代;
//  3. patch 由 sdk.UnifiedDiff 统一生成(与工具侧同源),超预算截断并显式标记。
//
// 两条用法:
//   - `/diff`            → 本会话改动清单(逐文件 ± 行数);同时发 diff/open 让 Web 切审查视图
//   - `/diff <路径>`     → 该文件本次会话的全部改动拼成 patch,发 diff/open 让 TUI 弹 pager / Web 定位

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// diffEntry 一个文件在本次会话里的累计改动(按路径聚合多条 file/change)。
type diffEntry struct {
	key     string // 聚合主键(Rel 优先,取不到用绝对 Path)
	path    string // 绝对路径(展示可省)
	added   int
	removed int
	ops     []string       // 出现过的操作(去重保序:write/edit/append)
	events  []changeRecord // 逐次改动(seq + 事件),单文件视图按时间序拼接
	created bool
	binary  bool
	trunc   bool
}

// changeRecord 一次改动的原始事件(带 seq/时间,便于 patch 分段标注)。
type changeRecord struct {
	seq uint64
	ts  time.Time
	ev  sdk.FileChangeEvent
}

// collectFileChanges 从账本回放里收集 file/change 事件并按路径聚合(保持首次出现顺序)。
func collectFileChanges(evs []sdk.SessionEvent) []*diffEntry {
	byKey := map[string]*diffEntry{}
	var order []*diffEntry
	for i := range evs {
		ev := &evs[i]
		if ev.Kind != sdk.EventFileChange {
			continue
		}
		fc, ok := sdk.FileChangeFrom(ev.Payload)
		if !ok {
			continue
		}
		key := fc.Rel
		if key == "" {
			key = fc.Path
		}
		e := byKey[key]
		if e == nil {
			e = &diffEntry{key: key, path: fc.Path}
			byKey[key] = e
			order = append(order, e)
		}
		e.added += fc.Added
		e.removed += fc.Removed
		e.created = e.created || fc.Created
		e.binary = e.binary || fc.Binary
		e.trunc = e.trunc || fc.Truncated
		if !containsStr(e.ops, fc.Op) {
			e.ops = append(e.ops, fc.Op)
		}
		e.events = append(e.events, changeRecord{seq: ev.Seq, ts: ev.TS, ev: fc})
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].key < order[j].key })
	return order
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// matchFileChange 按用户给的路径定位改动条目:
// 依次尝试 相对路径精确 → 绝对路径精确 → 路径后缀(带分隔符,防 `go` 命中 dol.go)→ 文件名。
// 多个候选时返回错误并列出候选,让用户指明(不静默挑第一个)。
func matchFileChange(entries []*diffEntry, arg string) (*diffEntry, error) {
	arg = strings.TrimSpace(filepath.ToSlash(arg))
	if arg == "" {
		return nil, fmt.Errorf("/diff <路径>")
	}
	var exact, suffix, base []*diffEntry
	baseArg := path.Base(arg)
	for _, e := range entries {
		k := filepath.ToSlash(e.key)
		p := filepath.ToSlash(e.path)
		switch {
		case k == arg || p == arg:
			exact = append(exact, e)
		case strings.HasSuffix(k, "/"+arg) || strings.HasSuffix(k, arg):
			suffix = append(suffix, e)
		case path.Base(k) == baseArg:
			base = append(base, e)
		}
	}
	for _, cand := range [][]*diffEntry{exact, suffix, base} {
		if len(cand) == 1 {
			return cand[0], nil
		}
		if len(cand) > 1 {
			return nil, fmt.Errorf("/diff: %q 命中 %d 个文件,请写更完整的路径:%s", arg, len(cand), keyList(cand, 6))
		}
	}
	return nil, fmt.Errorf("/diff: 本会话没有捕获到 %s 的改动(/diff 看清单)", arg)
}

func keyList(entries []*diffEntry, max int) string {
	var parts []string
	for i, e := range entries {
		if i >= max {
			parts = append(parts, fmt.Sprintf("…另 %d 个", len(entries)-max))
			break
		}
		parts = append(parts, e.key)
	}
	return "\n  " + strings.Join(parts, "\n  ")
}

// diffListView 改动清单文本(零 patch,速览用)。
func diffListView(entries []*diffEntry) string {
	if len(entries) == 0 {
		return "本会话还没有捕获到文件改动。\n(仅记录经工具写盘的操作:file_write/file_append/file_edit;不读 git 状态)"
	}
	totalAdd, totalDel := 0, 0
	maxKey := 0
	for _, e := range entries {
		totalAdd += e.added
		totalDel += e.removed
		if n := utf8.RuneCountInString(e.key); n > maxKey {
			maxKey = n
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "本会话改动 %d 个文件:+%d −%d 行\n", len(entries), totalAdd, totalDel)
	for _, e := range entries {
		mark := ""
		switch {
		case e.binary:
			mark = " · 二进制(仅计次)"
		case e.created:
			mark = " · 新建"
		}
		pad := strings.Repeat(" ", maxKey-utf8.RuneCountInString(e.key))
		fmt.Fprintf(&sb, "  %s%s  +%-4d −%-4d  %s×%d%s\n",
			e.key, pad, e.added, e.removed, strings.Join(e.ops, "/"), len(e.events), mark)
	}
	sb.WriteString("提示:/diff <路径> 看逐行 diff;数据来自捕获的写操作,不依赖 git")
	return sb.String()
}

// filePatchView 单文件 patch 文本(逐次改动分段标注 seq/时间/op,便于对着会话流定位)。
func filePatchView(e *diffEntry) (string, bool) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n本次会话 %d 次改动:+%d −%d 行\n", e.key, len(e.events), e.added, e.removed)
	trunc := false
	for _, rec := range e.events {
		op := rec.ev.Op
		if rec.ev.Tool != "" && rec.ev.Tool != "file_"+op {
			op = rec.ev.Tool
		}
		when := ""
		if !rec.ts.IsZero() {
			when = " " + rec.ts.Format("15:04:05")
		}
		fmt.Fprintf(&sb, "\n#%d %s%s  +%d −%d\n", rec.seq, op, when, rec.ev.Added, rec.ev.Removed)
		switch {
		case rec.ev.Binary:
			sb.WriteString("(二进制文件,未生成逐行 diff)\n")
		case rec.ev.Diff == "" && (rec.ev.Added > 0 || rec.ev.Removed > 0):
			sb.WriteString("(文件过大,写盘时未生成逐行 diff;上方为真实增删行数)\n")
		default:
			sb.WriteString(rec.ev.Diff)
		}
		if rec.ev.Truncated {
			trunc = true
			sb.WriteString("(该次改动的 patch 已按预算截断)\n")
		}
	}
	if e.trunc {
		trunc = true
	}
	return sb.String(), trunc
}

// cmdDiff 变更审查:`/diff` 清单 / `/diff <路径>` 单文件 patch。
// 两种用法都发 diff/open 意图事件(各端自行呈现:Web 切审查视图、TUI 弹 pager)。
func (h *Host) cmdDiff(args []string) (string, error) {
	var sess sdk.SessionLog
	if err := h.c.Inject("ctx.sessions", &sess); err != nil {
		return "", errString("ctx.sessions 未装配: " + err.Error())
	}
	entries := collectFileChanges(sess.Replay())
	arg := strings.TrimSpace(strings.Join(args, " "))

	if arg == "" {
		// 清单模式:文本给无 UI 端/TUI,事件让 Web 打开审查视图(Path 空 = 只要清单)
		if _, err := h.c.Emit(context.Background(), sdk.EventDiffOpen, sdk.DiffOpenEvent{}, sdk.Emit); err != nil {
			return "", err
		}
		return diffListView(entries), nil
	}

	e, err := matchFileChange(entries, arg)
	if err != nil {
		return "", err
	}
	patch, trunc := filePatchView(e)
	title := e.key
	if title == "" {
		title = e.path
	}
	if _, err := h.c.Emit(context.Background(), sdk.EventDiffOpen, sdk.DiffOpenEvent{
		Path: e.path, Title: title, Diff: patch,
		Added: e.added, Removed: e.removed, Changes: len(e.events), Truncated: trunc,
	}, sdk.Emit); err != nil {
		return "", err
	}
	return fmt.Sprintf("已打开 %s 的改动(%d 次,+%d −%d;TUI 弹 pager / Web 切变更视图)",
		title, len(e.events), e.added, e.removed), nil
}
