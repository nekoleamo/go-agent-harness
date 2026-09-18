// 行级 unified diff(S-P1-1 变更审查面)。
//
// 零依赖纯函数:标准库实现,供工具侧捕获(plugins/tool/tool-files)与命令侧呈现
// (host-internal-commands `/diff`)共用同一份实现 —— 三端不各写一套差异算法。
//
// 纪律:
//   - diff 来自**捕获的写操作**(before/after 由工具在写盘前后取到),不伪造 git HEAD、
//     不读 git 工作区状态 —— 工作区无 git 仓库时同样可用;
//   - 规模有界:超大中间段退回「整段替换」粗粒度输出(标记 Coarse),不用 O(n·m) 撑爆内存;
//   - 输出可被截断(调用方按预算调 TruncateDiff),截断必须显式(Capped/Truncated)。
package sdk

import (
	"fmt"
	"strings"
)

// FileChangeMaxDiffBytes 单条 file/change 事件携带 patch 的字节上限(超出截断并标记)。
// 32 KiB:足以覆盖常规多 hunk 编辑;一次会话几十次改动也不会把会话 jsonl 撑爆。
const FileChangeMaxDiffBytes = 32 << 10

// BuildFileChange 由「写盘前后内容」构造一条 file/change 事件(口径唯一)。
// 两条记录路径共用本函数,避免各写一份统计规则:
//   - 内嵌工具(plugins/tool/tool-files)拿到宿主 Ctx,构造后直接 Append 账本;
//   - 外部进程工具(tool-basic)无宿主 Ctx,构造后经桥回调(host-bridge change.record)交宿主落账。
//
// rel 相对工作区路径:外层工具能取到沙箱根时传入;外部进程取不到传空
// (宿主落账时会按其沙箱根补算,见 Callback 的 change sink)。
// created=目标原不存在(新建);before/after 为写盘前后全文。
func BuildFileChange(path, rel, op, tool string, created bool, before, after string) FileChangeEvent {
	ev := FileChangeEvent{
		Path:    path,
		Rel:     rel,
		Op:      op,
		Tool:    tool,
		Created: created,
		Bytes:   len(after),
	}
	if IsBinaryText(before) || IsBinaryText(after) {
		ev.Binary = true
		ev.Added, ev.Removed = 0, 0 // 二进制不做行计数(避免拿乱码行数当信号)
		return ev
	}
	d := UnifiedDiff(before, after, -1)
	ev.Added, ev.Removed, ev.Coarse = d.Added, d.Removed, d.Coarse
	ev.Diff, ev.Truncated = TruncateDiff(d.Diff, FileChangeMaxDiffBytes)
	return ev
}

// FileChangeRecorder 接收由写盘工具产生的 file/change 事件(构造已完成)。
// 内嵌工具经宿主 Ctx 直记账本,不需要实现本接口;
// 外部进程工具经桥回传时由 host-bridge 的 CbChanges 实现(见 plugins/host/host-bridge)。
// 语义:记录失败不得影响写盘结果(调用方只记日志),故返回错误仅供宿主侧诊断。
type FileChangeRecorder interface {
	RecordChange(ev FileChangeEvent) error
}

// DiffResult 一次行级差异的结果。
type DiffResult struct {
	Diff    string // unified diff 正文(无 ---/+++ 文件头,从首个 @@ 开始)
	Added   int    // 新增行数
	Removed int    // 删除行数
	Coarse  bool   // 差异段过大 → 整段替换(未做逐行对齐;计数仍真实)
}

// diffMaxCells LCS 动态规划单元格上限(超过则转粗粒度,防大文件卡死)。
const diffMaxCells = 4 << 20

// diffMaxInputLines 参与逐行 diff 的单文件行数上限。
// 超过则不生成 patch(只回真实增删计数,调用方据 Diff=="" 明示「未逐行 diff」):
// 大文件的逐行 diff 既无阅读价值,又拖慢写盘路径。
const diffMaxInputLines = 50000

// diffContextLines unified diff 默认上下文行数。
const diffContextLines = 3

// UnifiedDiff 计算 oldText → newText 的行级 unified diff(上下文 3 行,无文件头)。
// ctxLines < 0 时取默认 3;0 表示不带上文(仅变更行)。
// Diff 为空有三种成因:内容相同(Added=Removed=0)、二进制输入(调用方先用 IsBinaryText 自查)、
// 规模超限(Coarse=true 且 Added/Removed 为真实计数)。
func UnifiedDiff(oldText, newText string, ctxLines int) DiffResult {
	if ctxLines < 0 {
		ctxLines = diffContextLines
	}
	if oldText == newText {
		return DiffResult{}
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	// 公共前后缀先剥掉:典型编辑只动几行,剥完中间段骤降(也让 LCS 只需处理真差异区)
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]
	res := DiffResult{Added: len(mb), Removed: len(ma)}
	if len(a) > diffMaxInputLines || len(b) > diffMaxInputLines {
		res.Coarse = true // patch 未生成(见 Diff 字段注释)
		return res
	}
	if len(ma)*len(mb) > diffMaxCells {
		res.Coarse = true // 粗粒度:中间段整段替换(只为有界与可读,不追求最小编辑脚本)
	}

	// 公共前后缀作为上下文行重新纳入 ops:renderHunks 直接在完整序列上切 hunk(渲染层无需跨数组取上下文)。
	// 代价是 O(总行数) 个 op —— 只持有原字符串引用,无内容拷贝。
	ops := make([]diffOp, 0, len(a)+len(b))
	for i := 0; i < p; i++ {
		ops = append(ops, diffOp{kind: ' ', text: a[i]})
	}
	if res.Coarse {
		for _, l := range ma {
			ops = append(ops, diffOp{kind: '-', text: l})
		}
		for _, l := range mb {
			ops = append(ops, diffOp{kind: '+', text: l})
		}
	} else {
		ops = append(ops, lcsOps(ma, mb)...)
	}
	for i := 0; i < s; i++ {
		ops = append(ops, diffOp{kind: ' ', text: a[len(a)-s+i]})
	}
	res.Diff = renderHunks(ops, 0, ctxLines)
	// 计数必须从真实 op 里数:len(ma)/len(mb) 只在「未逐行对齐」的超限分支才等价,
	// 逐行对齐后中间段的许多行是相同的上下文行(按段长计数会把上下文行误算成增删)
	res.Added, res.Removed = 0, 0
	for _, o := range ops {
		switch o.kind {
		case '+':
			res.Added++
		case '-':
			res.Removed++
		}
	}
	return res
}

// splitDiffLines 按行切分:统一换行符;末尾换行不产生空行元素。
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""} // 文件内容为单个空行
	}
	return strings.Split(s, "\n")
}

type diffOp struct {
	kind byte // ' ' 相同 / '-' 删除 / '+' 新增
	text string
}

// lcsOps 以 LCS 动态规划求最小编辑脚本(输入规模已由 diffMaxCells 约束)。
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// dp[i][j] = a[i:] 与 b[j:] 的 LCS 长度
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
				continue
			}
			if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', text: a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', text: b[j]})
	}
	return ops
}

// renderHunks 把操作序列切成带上下文的 hunk(相邻变更间距 <= 2*ctx 时合并为一个 hunk)。
// base = 已剥离的公共前缀行数(行号偏移);ops 只覆盖真正的差异区间。
func renderHunks(ops []diffOp, base, ctx int) string {
	var changeIdx []int
	for i, o := range ops {
		if o.kind != ' ' {
			changeIdx = append(changeIdx, i)
		}
	}
	if len(changeIdx) == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < len(changeIdx); {
		// 本 hunk 覆盖 [start,end]:变更区间向外扩 ctx 行
		last := changeIdx[i]
		j := i
		for j+1 < len(changeIdx) && changeIdx[j+1]-last <= 2*ctx {
			j++
			last = changeIdx[j]
		}
		start, end := changeIdx[i]-ctx, last+ctx
		if start < 0 {
			start = 0
		}
		if end > len(ops)-1 {
			end = len(ops) - 1
		}
		// 行号:start 之前已消费的旧/新行数(旧 = 非 '+' 行;新 = 非 '-' 行)
		oldNo, newNo := base+1, base+1
		for k := 0; k < start; k++ {
			if ops[k].kind != '+' {
				oldNo++
			}
			if ops[k].kind != '-' {
				newNo++
			}
		}
		oldStart, newStart := oldNo, newNo
		oldCount, newCount := 0, 0
		var body strings.Builder
		for k := start; k <= end; k++ {
			o := ops[k]
			body.WriteString(string(o.kind) + o.text + "\n")
			switch o.kind {
			case ' ':
				oldCount++
				newCount++
			case '-':
				oldCount++
			case '+':
				newCount++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		sb.WriteString(body.String())
		i = j + 1
	}
	return sb.String()
}

// TruncateDiff 按字节预算截断 patch(在行边界处切,附加显式提示行)。
// 返回是否发生截断。
func TruncateDiff(diff string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(diff) <= maxBytes {
		return diff, false
	}
	cut := strings.LastIndexByte(diff[:maxBytes], '\n')
	if cut <= 0 {
		cut = maxBytes
	}
	return diff[:cut] + "\n……(diff 超预算已截断)\n", true
}

// IsBinaryText 判断内容是否不适合逐行 diff(前 8000 字节含 NUL)。
func IsBinaryText(s string) bool {
	head := s
	if len(head) > 8000 {
		head = head[:8000]
	}
	return strings.IndexByte(head, 0) >= 0
}
