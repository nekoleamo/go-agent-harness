// Package usageindex 跨会话 token 用量聚合(读侧,**只读**)。
//
// 为什么需要它(2026-10-23):`ctx.usageStats` 是**进程内**累计器 —— 重启即清零,
// 而且只统计本进程看到的会话。所以「这个项目这段时间一共烧了多少 token / 哪几个会话最贵」
// 这个问题,在既有能力里**没有答案**:看板卡片的「用量」是**当前会话累计**,
// `/context` 也是当场的账。
//
// 而数据的**权威副本其实一直在盘上**:每次模型请求都落一条
// `session/usage` 事件(`sdk.UsageEvent`:模型名 + Prompt/Completion/Cached),
// 追加在会话 jsonl 里。所以本包做的事就一件:**把那些事件扫一遍并按会话求和**。
//
// 三条纪律:
//  1. **只读** —— 不写任何东西(没有缓存索引:诊断命令不是热路径,而一个可能过期的
//     索引会让"我到底花了多少"给出错答案,这个代价远大于省下的几十毫秒)。
//  2. **不调模型** —— 纯本地统计。
//  3. **容忍坏行** —— 会话文件可能被截断/手工编辑过;坏行跳过,不因为一行坏掉就整场失败
//     (但会在 Result.Skipped 里如实计数,免得"读不出来"被误当成"没花过")。
package usageindex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// SessionUsage 单个会话的用量小计。
type SessionUsage struct {
	// Session 会话名(= 文件名去 .jsonl;形如 `<项目键>-20261023-101500`)。
	// 不用文件里的 Session 字段:旧日志没有它,而文件名恒有。
	Session string
	Path    string
	// MTime 文件最后修改时间(unix 秒;排序与「最近」判定用)。
	MTime int64
	// Requests 计入的请求条数。
	Requests int
	Prompt   int // 累计输入 token
	Complete int // 累计输出 token
	Cached   int // 其中缓存命中的输入 token
}

// Total 该会话的 (输入+输出) token。
func (s SessionUsage) Total() int { return s.Prompt + s.Complete }

// Result 一次聚合的结果。
type Result struct {
	// Sessions 参与统计的会话(按 Total 降序,同额按 MTime 降序)。
	Sessions []SessionUsage
	// 汇总
	Requests int
	Prompt   int
	Complete int
	Cached   int
	// Files 扫过的文件数;Skipped 读不动/坏行跳过的**行**数(如实计数)。
	Files   int
	Skipped int
	// Since 生效的时间下界(零值 = 不限);按**文件 mtime** 过滤 —— 见 Scan 的说明。
	Since time.Time
}

// Sum 全部会话的 (输入+输出) token。
func (r Result) Sum() int { return r.Prompt + r.Complete }

// Scan 扫 root 下的会话文件并按会话聚合;since 非零时只算 mtime 在该时刻之后的文件。
//
// 为什么按**文件 mtime** 而不是事件 TS 过滤:会话文件是**追加式**的,一个持续使用的会话
// 里既有上个月的请求也有今天的 —— 若按事件 TS 过滤,同一个文件会被扫两遍(一遍为了过滤、
// 一遍为了统计),而且"这段时间某会话花了多少"与"这个会话总共花了多少"两个口径会混在
// 一份输出里。当前口径:一个文件 = 一个会话,时间窗筛的是**会话**,不是单条请求。
// 命令文案里必须说清这一点(否则用户会以为分母只算了近 N 天的请求)。
func Scan(root string, since time.Time) (Result, error) {
	if root == "" {
		root = filepath.Join(sdk.Home(), "sessions")
	}
	res := Result{Since: since}
	forked := forkedBefore(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		// 目录不存在 = 还没跑过 = 没有用量(不是错误)
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(root, e.Name())
		info, ierr := e.Info()
		if ierr != nil {
			res.Skipped++
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		res.Files++
		name := strings.TrimSuffix(e.Name(), ".jsonl")
		su := SessionUsage{
			Session: name,
			Path:    path,
			MTime:   info.ModTime().Unix(),
		}
		// 派生会话(fork/clone)的文件里**复制了父会话的前缀事件**(含 session/usage)——
		// 不跳过就会把父的消耗在每条分支上再计一遍,`/usage` 的总数随派生次数虚高
		// (2026-10-24 review 的 P2)。ParentSeq 之后的才是这条支自己的花费。
		scanFile(path, name, forked, &su, &res)
		if su.Requests > 0 { // 没有任何请求的会话不进榜(空会话/只有设置事件的)
			res.Sessions = append(res.Sessions, su)
			res.Requests += su.Requests
			res.Prompt += su.Prompt
			res.Complete += su.Complete
			res.Cached += su.Cached
		}
	}
	sort.Slice(res.Sessions, func(i, j int) bool {
		if a, b := res.Sessions[i].Total(), res.Sessions[j].Total(); a != b {
			return a > b
		}
		return res.Sessions[i].MTime > res.Sessions[j].MTime
	})
	return res, nil
}

// forkedBefore 会话派生点:filename → 父会话覆盖到的 Seq(只统计 Seq 之后的事件)。
//
// 读 fork-tree.json(与 host-cwd-sessions 同一份记录);读不到就是空表 —— 那是"没有派生"
// 而不是错误,统计退化成"整个文件都算"(与改造前一致)。
func forkedBefore(root string) map[string]uint64 {
	out := map[string]uint64{}
	b, err := os.ReadFile(filepath.Join(root, "fork-tree.json"))
	if err != nil {
		return out
	}
	var m map[string]struct {
		ID        string `json:"ID"`
		Parent    string `json:"Parent"`
		ParentSeq uint64 `json:"ParentSeq"`
	}
	if json.Unmarshal(b, &m) != nil {
		return out
	}
	for id, n := range m {
		if n.ParentSeq > 0 && id != "" {
			out[id] = n.ParentSeq // 键是**裸会话 id**(文件名后缀)
		}
	}
	return out
}

// parentSeqOf 该会话文件名对应的派生点(0 = 不是派生会话)。
// fork-tree 的键是裸 id,文件名是 `<项目键>-<id>` ⇒ 用后缀匹配,不依赖项目键的形状。
func parentSeqOf(name string, forked map[string]uint64) uint64 {
	for id, seq := range forked {
		if name == id || strings.HasSuffix(name, "-"+id) {
			return seq
		}
	}
	return 0
}

// scanFile 累加单个会话文件里的 session/usage 事件(forked 非空时只算 Seq 之后的事件)。
func scanFile(path, name string, forked map[string]uint64, su *SessionUsage, res *Result) {
	skipUntil := parentSeqOf(name, forked)
	f, err := os.Open(path)
	if err != nil {
		res.Skipped++
		return
	}
	defer f.Close()
	// 单行可能很长(工具结果整段进事件),默认 64KB 不够 ⇒ 给 8MB。
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		// 先做一次便宜的预判:绝大多数行不是用量事件,不值得付完整反序列化的钱。
		if !fastHasKind(line, sdk.EventUsage) {
			continue
		}
		var ev struct {
			Seq     uint64          `json:"Seq"`
			Kind    string          `json:"Kind"`
			Payload json.RawMessage `json:"Payload"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			res.Skipped++
			continue
		}
		if ev.Kind != sdk.EventUsage || len(ev.Payload) == 0 {
			continue
		}
		if skipUntil > 0 && ev.Seq <= skipUntil {
			continue // 从父会话复制来的前缀:那笔花费已经记在父会话名下
		}
		var ue sdk.UsageEvent
		if err := json.Unmarshal(ev.Payload, &ue); err != nil {
			res.Skipped++
			continue
		}
		su.Requests++
		su.Prompt += ue.Usage.PromptTokens
		su.Complete += ue.Usage.CompletionTokens
		su.Cached += ue.Usage.CachedTokens
	}
	if err := sc.Err(); err != nil {
		res.Skipped++ // 超长行/读错:如实计数(该文件已统计到的部分保留)
	}
}

// fastHasKind 便宜的字节级预判:该行是否含指定 Kind 字面量。
//
// 为什么可以只看字面量就"可能命中":把匹配交给后面的 json.Unmarshal 定论 ——
// 这里只是把 99% 的非用量行在分配任何结构体之前就排掉(18MB 会话目录实测差一个数量级)。
func fastHasKind(line []byte, kind string) bool {
	return strings.Contains(string(line), `"`+kind+`"`)
}
