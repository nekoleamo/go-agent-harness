// 跨会话检索引擎(S-P2-5):线性扫描 + 预算,零依赖、零索引文件。
//
// 数据源 = 会话账本本身($GAH_HOME/sessions/*.jsonl),不另建索引:
//   - 布局(host-cwd-sessions 的单一事实源):<key>.jsonl(主会话)/ <key>-<id>.jsonl(切换会话);
//     陪读索引 workspaces.json(key→真实目录)、meta.json(显示名 / F3 概述);
//   - 事件线形状:sdk.SessionEvent 无 json tag → 顶层 PascalCase(Kind/Seq/Payload/TS),
//     载荷亦是 PascalCase({"Content":…})。故此处**按 Go 结构读**,不臆造小写键
//     (S-P1-1 教训:跨语言/跨进程读载荷必须对齐线上形状,且要有走真实产物的用例)。
//
// 刻意不做的事(与 DESIGN S-P2-5 的取舍):
//   - 不做持久索引(FTS5 需依赖评审;朴素倒排要面对失效/体积/并发写)。会话文件量级
//     在 MiB 级、单次扫描毫秒级,线性扫描 + 硬预算足够;真到瓶颈再加缓存层。
//   - 不做词干/分词器(中文按子串命中即可;英文按整词子串,`solved` 命中不了 `solve`)。
//   - 不读 assistant/chunk(与 assistant/message 内容重复,避免同一句被计两次分)。
package toolsessionsearch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Scope 检索范围。
type Scope string

const (
	// ScopeWorkspace 只在当前工作区的会话里检索(默认;跨项目内容不进模型上下文)。
	ScopeWorkspace Scope = "workspace"
	// ScopeAll 全部工作区(需调用方显式选择)。
	ScopeAll Scope = "all"
)

// 预算默认值(Options 同名字段非零则覆盖)。
const (
	defaultMaxFiles      = 200
	defaultMaxFileBytes  = int64(8 << 20)
	defaultMaxTotalBytes = int64(32 << 20)
	defaultMaxEvents     = 200000
	defaultTimeBudget    = 8 * time.Second
)

// 输出粒度上限。
const (
	maxMatchesPerSession = 3       // 每个会话最多保留几条命中片段(仍统计全部命中次数)
	snippetBefore        = 60      // 片段取命中点前的 rune 数
	snippetAfter         = 140     // 命中点后的 rune 数
	snippetWindowBytes   = 4 << 10 // 片段先按字节开窗的半径(见 snippet 注释)
	previewRunes         = 160     // 会话首条用户消息省略版长度
	maxCountsPerField    = 5       // 单字段计入分数的命中次数上限(防一条超长工具输出刷爆分数)
)

// indexedKind 参与检索的事件类型 + 权重。
// 权重表达「谁更像用户想找的东西」:用户说的最准,助手次之,工具是线索。
type indexedKind struct {
	role   string
	weight float64
	fields []string // 载荷里承载文本的字段(线上 PascalCase)
}

var indexedKinds = map[string]indexedKind{
	sdk.EventUserMessage:      {role: "user", weight: 3, fields: []string{"Content"}},
	sdk.EventAssistantMessage: {role: "assistant", weight: 2, fields: []string{"Content"}},
	sdk.EventToolCall:         {role: "tool", weight: 1, fields: []string{"Name", "Arguments"}},
	sdk.EventToolResult:       {role: "tool", weight: 1, fields: []string{"Name", "Content", "Error"}},
}

// Options 检索参数。
type Options struct {
	Root      string // 会话目录(空 = $GAH_HOME/sessions)
	Query     string // 查询词(空白分隔;空 = 最近会话模式)
	Limit     int    // 返回条数(0 = 默认 5;上限 maxLimit)
	Scope     Scope  // "" = workspace
	Workspace string // 当前工作区绝对路径(空 = os.Getwd())

	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxEvents     int
	TimeBudget    time.Duration
}

// Match 一条命中片段(同一事件多个字段命中只出一条,取最相关字段做片段)。
type Match struct {
	Kind  string   `json:"kind"`         // 事件类型(如 user/message)
	Role  string   `json:"role"`         // user|assistant|tool
	Seq   uint64   `json:"seq"`          // 会话内事件序号
	TS    string   `json:"ts,omitempty"` // 事件时间(RFC3339)
	Terms []string `json:"terms"`        // 该片段命中的查询词
	Text  string   `json:"text"`         // 命中点上下文片段(压平换行)
	Score float64  `json:"score"`
}

// Hit 一个会话的检索结果。
type Hit struct {
	File    string   `json:"session_file"` // 文件名(会话落盘名,可据此回溯)
	ID      string   `json:"session_id"`   // 会话 id(空 = 主会话 <key>.jsonl)
	Name    string   `json:"name,omitempty"`
	Dir     string   `json:"dir"` // 项目真实目录(key 反查 workspaces.json;未知 = 空)
	Key     string   `json:"project_key"`
	Preview string   `json:"preview,omitempty"` // 首条用户消息省略版
	Summary string   `json:"summary,omitempty"` // F3 概述(meta.json 缓存;无 = 空)
	Topics  []string `json:"topics,omitempty"`
	Frames  int      `json:"frames"` // 事件条数
	MTime   int64    `json:"mtime"`  // 文件修改时间(unix 秒)
	LastTS  string   `json:"last_ts,omitempty"`
	Score   float64  `json:"score"` // 会话总分 = Σ 全部命中事件的分数(片段只保留前 maxMatchesPerSession 条)
	Matches []Match  `json:"matches,omitempty"`

	mtimeNS int64 // 排序用(不输出):纳秒级,避免同秒创建的会话退化成目录序
}

// Result 检索结果。
type Result struct {
	Mode      string   `json:"mode"` // match(有查询词)| recent(空查询 = 最近会话)
	Query     string   `json:"query,omitempty"`
	Scope     string   `json:"scope"`
	Workspace string   `json:"workspace"`
	Sessions  int      `json:"sessions_scanned"` // 参与扫描的会话文件数
	Events    int      `json:"events_scanned"`
	Bytes     int64    `json:"bytes_scanned"`
	Truncated bool     `json:"truncated"` // 预算用尽 → 结果可能不完整(必须让调用方看见)
	Notes     []string `json:"notes,omitempty"`
	Hits      []Hit    `json:"hits"`
}

// Search 执行检索。返回 error 仅限参数非法;数据缺失/为空以 Notes 显式说明(不静默)。
func Search(opt Options) (Result, error) {
	if opt.Scope == "" {
		opt.Scope = ScopeWorkspace
	}
	if opt.Scope != ScopeWorkspace && opt.Scope != ScopeAll {
		return Result{}, fmt.Errorf("scope 只能是 workspace|all,收到 %q", opt.Scope)
	}
	if opt.Limit <= 0 {
		opt.Limit = defaultLimit
	}
	if opt.Limit > maxLimit {
		opt.Limit = maxLimit
	}
	if opt.MaxFiles <= 0 {
		opt.MaxFiles = defaultMaxFiles
	}
	if opt.MaxFileBytes <= 0 {
		opt.MaxFileBytes = defaultMaxFileBytes
	}
	if opt.MaxTotalBytes <= 0 {
		opt.MaxTotalBytes = defaultMaxTotalBytes
	}
	if opt.MaxEvents <= 0 {
		opt.MaxEvents = defaultMaxEvents
	}
	if opt.TimeBudget <= 0 {
		opt.TimeBudget = defaultTimeBudget
	}

	root := opt.Root
	if root == "" {
		root = filepath.Join(sdk.Home(), "sessions")
	}
	ws := opt.Workspace
	if ws == "" {
		if wd, err := os.Getwd(); err == nil {
			ws = wd
		}
	}
	res := Result{Scope: string(opt.Scope), Workspace: ws, Query: opt.Query, Hits: []Hit{}}
	res.Mode = "match"
	if strings.TrimSpace(opt.Query) == "" {
		res.Mode = "recent"
	}

	keys, dirs := loadWorkspaces(root)
	curKey := currentKey(ws, keys, dirs)

	files, err := sessionFiles(root)
	if err != nil {
		// 目录不存在 = 全新数据根(尚无会话):显式说明,不当错误。
		res.Notes = append(res.Notes, "会话目录不存在或不可读(尚无会话记录): "+root)
		return res, nil
	}
	cand := selectSessions(files, keys, curKey, opt.Scope)
	if len(cand) == 0 {
		res.Notes = append(res.Notes, noSessionNote(opt.Scope, ws))
		return res, nil
	}
	// 新会话优先:预算用尽时保证先扫最近用过的会话。
	sort.SliceStable(cand, func(i, j int) bool { return cand[i].mtimeNS > cand[j].mtimeNS })

	names, metas := loadIndexes(root)
	terms := tokenize(opt.Query)
	deadline := time.Now().Add(opt.TimeBudget)
	dropped := 0
	for _, c := range cand {
		if res.Sessions >= opt.MaxFiles || res.Bytes >= opt.MaxTotalBytes || res.Events >= opt.MaxEvents || time.Now().After(deadline) {
			res.Truncated = true
			dropped = len(cand) - res.Sessions
			break
		}
		h, stats := scanSession(c, opt, terms, names, metas)
		h.Dir = dirs[h.Key] // key → 真实目录(workspaces.json)
		res.Sessions++
		res.Events += stats.events
		res.Bytes += stats.bytes
		if stats.truncated {
			res.Truncated = true
			res.Notes = append(res.Notes, "会话 "+c.name+" 超过单文件扫描上限,只扫了前 "+humanBytes(opt.MaxFileBytes)+"(可能漏掉后半段)")
		}
		if stats.overlong > 0 {
			res.Truncated = true
			res.Notes = append(res.Notes, fmt.Sprintf(
				"会话 %s 有 %d 行超长记录(单行 > %s,通常是工具输出):只检索了这些行的行首部分",
				c.name, stats.overlong, humanBytes(opt.MaxFileBytes)))
		}
		if stats.readErr != nil {
			res.Truncated = true
			res.Notes = append(res.Notes, "会话 "+c.name+" 读取中断(结果可能不完整): "+stats.readErr.Error())
		}
		if res.Mode == "recent" || len(h.Matches) > 0 {
			res.Hits = append(res.Hits, h)
		}
		if time.Now().After(deadline) {
			res.Truncated = true
		}
	}
	if res.Truncated {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"扫描预算用尽(文件 %d / 字节 %s / 事件 %d / 时限 %s),还有约 %d 个会话未扫,结果可能不完整",
			opt.MaxFiles, humanBytes(opt.MaxTotalBytes), opt.MaxEvents, opt.TimeBudget, dropped))
	}
	sortHits(res.Hits, res.Mode)
	if len(res.Hits) > opt.Limit {
		res.Hits = res.Hits[:opt.Limit]
	}
	return res, nil
}

// sessionFile 候选会话文件。
type sessionFile struct {
	name    string // 文件名(含 .jsonl)
	key     string // 归属项目 key(未知 = 空)
	id      string // 会话 id(空 = 主会话)
	path    string
	mtime   int64 // unix 秒(输出用)
	mtimeNS int64 // 纳秒(排序用:同秒内创建的会话不能退化成目录序)
	size    int64
}

// sessionFiles 列出会话目录里的 *.jsonl(workspaces.json / meta.json 等索引不是 .jsonl,天然排除)。
func sessionFiles(root string) ([]sessionFile, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]sessionFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		sf := sessionFile{name: e.Name(), path: filepath.Join(root, e.Name())}
		if fi, err := e.Info(); err == nil {
			sf.mtime = fi.ModTime().Unix()
			sf.mtimeNS = fi.ModTime().UnixNano()
			sf.size = fi.Size()
		}
		out = append(out, sf)
	}
	return out, nil
}

// selectSessions 按范围挑出候选会话,并回填归属 key 与会话 id。
// key 归属用「最长已知 key 优先」判定:key 里的目录分隔符已被打成 `-`,前缀关系存在
// 天然歧义(/a/proj 与 /a/proj-2 → 后者是前者加后缀),故只有最长匹配才算归属,
// 否则会把另一个项目的会话算进来。
func selectSessions(files []sessionFile, keys []string, curKey string, scope Scope) []sessionFile {
	sorted := append([]string(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	var out []sessionFile
	for _, f := range files {
		f.key, f.id = ownerOf(f.name, sorted)
		if scope == ScopeWorkspace {
			if f.key != "" {
				if f.key != curKey {
					continue
				}
			} else if !belongsTo(f.name, curKey) {
				// 索引里没有该 key(手工拷入/历史遗留):退化为前缀判定
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// ownerOf 按「最长已知 key 优先」判定文件归属,返回 (key, 会话 id)。
func ownerOf(name string, keysLongestFirst []string) (string, string) {
	for _, k := range keysLongestFirst {
		if name == k+".jsonl" {
			return k, ""
		}
		if strings.HasPrefix(name, k+"-") {
			return k, strings.TrimSuffix(strings.TrimPrefix(name, k+"-"), ".jsonl")
		}
	}
	return "", ""
}

// belongsTo 未知 key 文件的退化判定(仅用于 workspace 范围)。
func belongsTo(name, key string) bool {
	if key == "" {
		return false
	}
	return name == key+".jsonl" || strings.HasPrefix(name, key+"-")
}

// scanStats 单文件扫描统计。
type scanStats struct {
	events    int
	bytes     int64
	truncated bool
	overlong  int   // 超过单行上限的行数(只检索了行首窗口)
	readErr   error // 读取中断原因(非 EOF)
}

// forEachLine 逐行读文件并回调(fn 返回 false 停止)。
// 为什么不用 bufio.Scanner:默认 token 上限 1 MiB,而会话文件里 tool/result 的单行
// 可达数 MB(工具输出),Scanner 会直接返回 ErrTooLong 结束扫描 —— 若只看 Scan() 的 bool
// 就会「静默丢掉文件后半段」(实测:7.9 MB 主会话文件在中途一条 1.9 MB 记录处整段失联)。
// 这里改为分段读:单行最多保留 maxLine 字节参与检索,超出部分丢弃但继续读完整行,
// 并统计 overlong 与真实错误,由调用方显式记账(绝不静默)。
func forEachLine(fp *os.File, maxLine int, fn func(line []byte) bool) (overlong int, readErr error) {
	if maxLine <= 0 {
		maxLine = 1 << 20
	}
	r := bufio.NewReaderSize(fp, 64*1024)
	buf := make([]byte, 0, 64*1024)
	long := false
	for {
		frag, err := r.ReadSlice('\n')
		if len(frag) > 0 {
			if room := maxLine - len(buf); room > 0 {
				n := len(frag)
				if n > room {
					n = room
					long = true
				}
				buf = append(buf, frag[:n]...)
			} else {
				long = true
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // 行未结束,继续读
		}
		if err == nil || errors.Is(err, io.EOF) {
			if len(buf) > 0 || long {
				if long {
					overlong++
				}
				keep := fn(buf)
				buf = buf[:0]
				long = false
				if !keep {
					return overlong, nil
				}
			}
			if errors.Is(err, io.EOF) {
				return overlong, nil
			}
			continue
		}
		return overlong, err
	}
}

// scanSession 扫一个会话文件:按事件抽取文本、打分、取片段。
func scanSession(f sessionFile, opt Options, terms []string, names map[string]string, metas map[string]sessionMeta) (Hit, scanStats) {
	h := Hit{File: f.name, ID: f.id, Key: f.key, MTime: f.mtime, mtimeNS: f.mtimeNS, Matches: []Match{}}
	if v, ok := names[f.name]; ok {
		h.Name = v
	}
	var st scanStats
	fp, err := os.Open(f.path)
	if err != nil {
		return h, st
	}
	defer fp.Close()
	if f.size > opt.MaxFileBytes {
		st.truncated = true
	}
	// 单行上限 = 单文件上限:内存量级与已接受的「每文件最多读 MaxFileBytes」一致,
	// 不再另设 1 MiB 级的小上限(那会漏掉整条工具输出)。
	overlong, err := forEachLine(fp, int(opt.MaxFileBytes), func(line []byte) bool {
		st.bytes += int64(len(line))
		if st.bytes > opt.MaxFileBytes {
			st.truncated = true
			return false
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return true
		}
		st.events++
		h.Frames = st.events
		var ev struct {
			Kind    string
			Seq     uint64
			TS      time.Time
			Payload json.RawMessage
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return true // 坏行容忍(与 sessionlog 读侧一致)
		}
		if !ev.TS.IsZero() {
			h.LastTS = ev.TS.Local().Format(time.RFC3339)
		}
		ik, ok := indexedKinds[ev.Kind]
		if !ok {
			return true
		}
		texts := fieldTexts(ev.Payload, ik.fields)
		if len(texts) == 0 {
			return true
		}
		if h.Preview == "" && ev.Kind == sdk.EventUserMessage {
			h.Preview = clipRunes(texts[0].text, previewRunes)
		}
		if len(terms) == 0 {
			return true
		}
		if m, ok := scoreEvent(ev.Kind, ik, ev.Seq, ev.TS, texts, terms); ok {
			h.Matches = append(h.Matches, m)
			h.Score += m.Score
		}
		return true
	})
	st.overlong, st.readErr = overlong, err
	if m, ok := metas[f.name]; ok {
		h.Name = firstNonEmpty(m.Name, h.Name)
		if m.Summary != nil {
			h.Summary = m.Summary.Text
			h.Topics = m.Summary.Topics
		}
	}
	if len(h.Matches) > 1 {
		// 片段按分数降序(同一会话里最相关的那句排前)
		sort.SliceStable(h.Matches, func(i, j int) bool { return h.Matches[i].Score > h.Matches[j].Score })
	}
	if len(h.Matches) > maxMatchesPerSession {
		h.Matches = h.Matches[:maxMatchesPerSession]
	}
	return h, st
}

// fieldText 一个字段的文本(已截断到单事件上限)与是否被截断。
type fieldText struct {
	name string
	text string
}

// fieldTexts 从载荷里取指定字段的字符串(线上 PascalCase;缺失/非字符串 = 跳过)。
func fieldTexts(payload json.RawMessage, fields []string) []fieldText {
	if len(payload) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil
	}
	var out []fieldText
	for _, f := range fields {
		s, ok := m[f].(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// 不截断:字段字符串本就已整体在内存里(unmarshal 时已建好),
		// 截断只会「悄悄搜不到」超长工具输出的后半段;片段侧再按命中点开窗,不整体展开。
		out = append(out, fieldText{name: f, text: s})
	}
	return out
}

// scoreEvent 对一条事件打分;无命中返回 ok=false。
// 打分 = Σ(权重 × 该字段命中次数) + 全词命中加成;片段取命中最多(同分取靠前)的字段。
func scoreEvent(kind string, ik indexedKind, seq uint64, ts time.Time, texts []fieldText, terms []string) (Match, bool) {
	best := -1.0
	bestField := ""
	bestLow := ""
	bestPos := -1
	var hitTerms []string
	score := 0.0
	for _, ft := range texts {
		low := strings.ToLower(ft.text)
		fieldScore := 0.0
		hit := 0
		first := -1
		for _, t := range terms {
			n := strings.Count(low, t)
			if n == 0 {
				continue
			}
			hit++
			if n > maxCountsPerField {
				n = maxCountsPerField // 计数封顶:覆盖到了就算命中,不靠重复次数压过别的会话
			}
			fieldScore += ik.weight * float64(n)
			if i := strings.Index(low, t); i >= 0 && (first < 0 || i < first) {
				first = i
			}
		}
		if hit == 0 {
			continue
		}
		if len(terms) > 1 && hit == len(terms) {
			fieldScore *= 1.5 // 多词全命中:整句更可能是要找的那句(尺度无关的加成;单词查询不加)
		}
		score += fieldScore
		if fieldScore > best {
			best, bestField, bestLow, bestPos = fieldScore, ft.text, low, first
		}
	}
	if bestField == "" {
		return Match{}, false
	}
	for _, t := range terms {
		if strings.Contains(bestLow, t) {
			hitTerms = append(hitTerms, t)
		}
	}
	m := Match{Kind: kind, Role: ik.role, Seq: seq, Terms: hitTerms, Score: round2(score), Text: snippet(bestField, terms, bestPos)}
	if !ts.IsZero() {
		m.TS = ts.Local().Format(time.RFC3339)
	}
	return m, true
}

// snippet 命中点上下文片段:先按命中点开一个字节窗口,再压平裁剪(模型友好,两端省略号)。
// 为什么先开窗:单条工具输出可达数 MB(实测 7.9 MB 会话文件里有一条 1.9 MB 的 tool/result),
// 整段压平 + 转 rune 会白烧内存/CPU,而片段最终只输出 snippetBefore+snippetAfter 个字符。
// pos = 命中点在原文里的字节偏移(调用方在已小写的副本上定位;未知传 -1,此时自行兜底定位)。
func snippet(text string, terms []string, pos int) string {
	if pos < 0 {
		low := strings.ToLower(text)
		for _, t := range terms {
			if i := strings.Index(low, t); i >= 0 && (pos < 0 || i < pos) {
				pos = i
			}
		}
	}
	if pos < 0 {
		return clipRunes(flatten(text), snippetBefore+snippetAfter)
	}
	flat := flatten(window(text, pos, snippetWindowBytes))
	low := strings.ToLower(flat)
	fpos := -1
	for _, t := range terms {
		if i := strings.Index(low, t); i >= 0 && (fpos < 0 || i < fpos) {
			fpos = i
		}
	}
	if fpos < 0 {
		return clipRunes(flat, snippetBefore+snippetAfter)
	}
	return clipAround(flat, fpos, snippetBefore, snippetAfter)
}

// window 取 text 中 pos 附近 radius 字节的窗口,并把两端对齐到 UTF-8 字符边界。
func window(text string, pos, radius int) string {
	if len(text) <= 2*radius {
		return text
	}
	lo, hi := pos-radius, pos+radius
	if lo < 0 {
		lo = 0
	}
	if hi > len(text) {
		hi = len(text)
	}
	for lo > 0 && !utf8.RuneStart(text[lo]) {
		lo--
	}
	for hi < len(text) && !utf8.RuneStart(text[hi]) {
		hi++
	}
	return text[lo:hi]
}

// clipAround 取字节偏移 pos 前后各 before/after 个 rune,被裁剪的一侧加省略号。
func clipAround(s string, pos, before, after int) string {
	runes := []rune(s)
	rpos := utf8.RuneCountInString(s[:pos])
	rs, re := rpos-before, rpos+after
	if rs < 0 {
		rs = 0
	}
	if re > len(runes) {
		re = len(runes)
	}
	out := string(runes[rs:re])
	if rs > 0 {
		out = "…" + out
	}
	if re < len(runes) {
		out += "…"
	}
	return out
}

// flatten 压平换行/制表与连续空白(片段里保留可读的单行)。
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sortHits 排序:有查询 → 分数降序(同分取最近修改);最近模式 → 修改时间降序。
func sortHits(hits []Hit, mode string) {
	sort.SliceStable(hits, func(i, j int) bool {
		if mode == "recent" {
			return hits[i].mtimeNS > hits[j].mtimeNS
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].mtimeNS > hits[j].mtimeNS
	})
}

// tokenize 查询词 → 小写词元(空白分隔,空词元丢弃)。
func tokenize(q string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(q)) {
		t = strings.Trim(t, "\"'`.,;:!?()[]{}")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// currentKey 当前工作区对应的项目 key:先按真实目录在 workspaces.json 里查(容忍软链/尾斜杠
// 差异),查不到退回 sdk.ProjectKey(与 host-cwd-sessions 同款派生)。
func currentKey(dir string, keys []string, dirs map[string]string) string {
	if dir != "" {
		for _, k := range keys {
			if sameDir(dirs[k], dir) {
				return k
			}
		}
	}
	return sdk.ProjectKey(dir)
}

// sameDir 目录同一性:clean 相等 → 真;否则 realpath 归一后再比(macOS /tmp → /private/tmp)。
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(filepath.Clean(a))
	rb, eb := filepath.EvalSymlinks(filepath.Clean(b))
	return ea == nil && eb == nil && ra == rb
}

// loadWorkspaces 读 workspaces.json(key→真实目录)与全部已知 key。
// 缺文件/坏 JSON = 空(容忍);workspaces.json 由 host-cwd-sessions 维护(启动即记录当前项目)。
func loadWorkspaces(root string) (keys []string, dirs map[string]string) {
	dirs = map[string]string{}
	b, err := os.ReadFile(filepath.Join(root, "workspaces.json"))
	if err != nil {
		return nil, dirs
	}
	var recs []sdk.ProjectInfo
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, dirs
	}
	for _, r := range recs {
		if r.Key == "" {
			continue
		}
		keys = append(keys, r.Key)
		dirs[r.Key] = r.Dir
	}
	return keys, dirs
}

// sessionMeta 会话元数据(meta.json 条目;镜像 host-cwd-sessions 的落盘形状,只取检索要用到的字段)。
type sessionMeta struct {
	Name    string `json:"name,omitempty"`
	Summary *struct {
		Text   string   `json:"text"`
		Topics []string `json:"topics,omitempty"`
	} `json:"summary,omitempty"`
}

// loadIndexes 读显示名(meta.json 优先,回退旧的 names.json)与元数据。
func loadIndexes(root string) (map[string]string, map[string]sessionMeta) {
	names := map[string]string{}
	metas := map[string]sessionMeta{}
	if b, err := os.ReadFile(filepath.Join(root, "meta.json")); err == nil {
		var m map[string]sessionMeta
		if json.Unmarshal(b, &m) == nil {
			for k, v := range m {
				metas[k] = v
				if v.Name != "" {
					names[k] = v.Name
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "names.json")); err == nil {
		var m map[string]string
		if json.Unmarshal(b, &m) == nil {
			for k, v := range m {
				if _, ok := names[k]; !ok && v != "" {
					names[k] = v
				}
			}
		}
	}
	return names, metas
}

// noSessionNote 无候选会话时的说明(区分「这个工作区还没有会话」与「范围/目录问题」)。
func noSessionNote(scope Scope, ws string) string {
	if scope == ScopeWorkspace {
		return "当前工作区还没有会话记录: " + ws
	}
	return "会话目录里没有任何会话文件"
}

// clipRunes 按 rune 截断(超限加省略号)。
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// round2 分数保留两位小数(输出的可读性,不影响排序)。
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// humanBytes 人类可读字节数(仅用于说明文案)。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// firstNonEmpty 取第一个非空值。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
