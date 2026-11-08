// failures.go:重复失败的记账与触发判定(2026-11-08,C 批借鉴)。
//
// 为什么需要它:记忆里已经有 `pitfall` 这一类,但**触发源只有"用户嘴上说了什么"** ——
// 而真实世界里最有价值的踩坑,是同一个错误在同一个项目里反复发生。这件事在宿主里
// 零成本可得(工具结果就在手上),此前完全没被利用。
//
// 借鉴来源:Routed CL(arXiv 2610.38796)的 ReasoningBank 一支 —— 每条轨迹后判定成功/失败,
// 抽出可复用的经验。本文件只取"失败要被看见"这一半,**不引入任何模型调用**。
//
// 三条边界(写在代码里免得后来放宽):
//   - **只产候选,不直写**。pitfall 永远走候选池等人工转正(`AutoWriteKinds` 不含它);
//     自动写一条"这个坑"等于模型替人判断该记什么,那是静默提权。
//   - **签名不取参数**:`tools/post-execute` 的载荷只有 *ToolResult,没有 args;而
//     `tools/pre-execute` 手里的 id 是另一套(sdk/tools.go 已记这件事),配对不可靠。
//     错误首行本身就带足了区分度(工具名 + 首行 = 这个签名)。
//   - **错误文本可能带凭据**:首行只留 120 rune,且过 `HasSecret` 闸(命中则整段换成
//     一句占位) —— 记的是"这个错误重复了",不是错误的原文。
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxFailures 失败账上限。上限的理由与候选池同源:它是**信号**不是档案,
	// 攒几千条既没人看,也让每次失败都变成一次全文件重写。
	MaxFailures = 200
	// FailureHeadRunes 参与签名的错误首行上限(rune,不是字节 —— 中文错误文本很常见)。
	FailureHeadRunes = 120
	// FailureSampleRunes 进候选正文时的首行上限(比签名更短,候选是给人看的一句话)。
	FailureSampleRunes = 80
)

// FailureEntry 一个失败签名的累计。
type FailureEntry struct {
	Sig     string `json:"sig"`               // 工具 + 归一化首行的哈希键
	Tool    string `json:"tool"`              // 工具名(展示与分组)
	Head    string `json:"head"`              // 首行样本(已截断/已脱敏)
	Count   int    `json:"count"`             // 累计发生次数
	Fired   int    `json:"fired"`             // 已提过几次候选(阈值翻倍用它)
	First   string `json:"first"`             // 首次 RFC3339
	Last    string `json:"last"`              // 最近 RFC3339
	Project string `json:"project,omitempty"` // 项目桶(空 = 无项目上下文)
	Session string `json:"session,omitempty"` // 最近一次发生所在的会话(来源可追溯)
}

// FailureState 失败账本(落 $GAH_HOME/memory/failures.json)。
type FailureState struct {
	Items []FailureEntry `json:"items"`
}

// FailuresPath 账本路径。
func FailuresPath() string { return filepath.Join(Dir(), "failures.json") }

// LoadFailures 读账本(不存在 = 空,不是错误;坏文件按空继续并返回 error)。
func LoadFailures() (FailureState, error) {
	var st FailureState
	raw, err := os.ReadFile(FailuresPath())
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return FailureState{}, fmt.Errorf("memory: 失败账本损坏(按空账本继续): %w", err)
	}
	return st, nil
}

// Save 落盘(原子写)。
func (s FailureState) Save() error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(FailuresPath(), append(raw, '\n'))
}

// FailureHit 一次记录的结果(调用方据此决定要不要提候选与怎么打日志)。
type FailureHit struct {
	Sig   string
	Tool  string
	Head  string // 可进候选正文的样本(已脱敏、已截断)
	Count int    // 该签名累计次数
	Fire  bool   // 本次是否到达阈值(该提一条候选)
}

// Record 记一次失败,返回是否到达触发阈值。
//
// 阈值**翻倍**:第 1 次触发在 threshold,之后 2×、4×…(`fired` 记录已触发次数)。
// 为什么翻倍:同一签名重复 100 次失败时,每一步都提一条候选会把候选池灌满
// (池子上限 12 条 / 日 5 条),而"又失败了 3 次"对人没有任何新信息。
func (s FailureState) Record(tool, errText, project, session string, threshold int, now time.Time) (FailureState, FailureHit) {
	if threshold < 2 {
		threshold = 2
	}
	if now.IsZero() {
		now = time.Now()
	}
	head := firstLineRunes(errText, FailureHeadRunes)
	shown := head
	if HasSecret(shown) {
		// 不记原文:记的是"这个错误重复了",不是错误里的凭据。
		// 签名也必须用**脱敏后**的文本 —— 否则凭据会经签名原样落到账本里
		// (signature 是落盘字段,这一处曾真的漏过)。
		shown = "(错误文本疑似含凭据,已省略)"
	}
	sig := failureSig(tool, shown)
	at := now.Format(time.RFC3339)

	for i := range s.Items {
		e := &s.Items[i]
		if e.Sig != sig || e.Project != project {
			continue
		}
		e.Count++
		e.Last = at
		e.Head = shown
		if session != "" {
			e.Session = session
		}
		fire := e.Count >= threshold<<e.Fired
		if fire {
			e.Fired++
		}
		return s, FailureHit{Sig: sig, Tool: tool, Head: shown, Count: e.Count, Fire: fire}
	}
	e := FailureEntry{Sig: sig, Tool: tool, Head: shown, Count: 1, First: at, Last: at, Project: project, Session: session}
	s.Items = append(s.Items, e)
	s.evict()
	fire := 1 >= threshold<<e.Fired // threshold>=2 ⇒ 首次出现不触发
	if fire {
		s.Items[len(s.Items)-1].Fired++
	}
	return s, FailureHit{Sig: sig, Tool: tool, Head: shown, Count: 1, Fire: fire}
}

// evict 超上限时丢最久没发生的那条(时间相同按签名稳定排序,便于测试)。
func (s *FailureState) evict() {
	for len(s.Items) > MaxFailures {
		oldest := 0
		for i := 1; i < len(s.Items); i++ {
			if s.Items[i].Last < s.Items[oldest].Last ||
				(s.Items[i].Last == s.Items[oldest].Last && s.Items[i].Sig < s.Items[oldest].Sig) {
				oldest = i
			}
		}
		s.Items = append(s.Items[:oldest], s.Items[oldest+1:]...)
	}
}

// CandidateText 到达阈值时给人的那条候选正文。
//
// 只陈述事实 + 下一步要人判断的方向,**不编造原因**(模型编的原因会变成长期生效的错记忆)。
func (s FailureEntry) CandidateText() string {
	head := truncateRunes(s.Head, FailureSampleRunes)
	return fmt.Sprintf("重复失败:工具 %s 已 %d 次报同一错误「%s」——待确认是用法问题还是环境问题", s.Tool, s.Count, head)
}

// failureSig 签名:工具 + 归一化首行(小写)。
func failureSig(tool, head string) string {
	return strings.ToLower(strings.TrimSpace(tool) + "|" + normalizeErrHead(head))
}

var (
	// idRun 任何**含数字的 token**(临时路径、哈希、对象 id、行号、端口):
	// 同一个错误在两次运行里通常只有它不同。整词抹掉而不是只抹数字,
	// 因为 "abc123" 与 "def456" 抹成 "abc<n>" / "def<n>" 仍然不同。
	idRun = regexp.MustCompile(`\S*\d\S*`)
	// spaceRun 连续空白。
	spaceRun = regexp.MustCompile(`\s+`)
)

// normalizeErrHead 把错误首行归一成"同一个错误的稳定指纹"。
//
// 只抹掉真正会变的部分(哈希/数字/空白),**保留文字与标点** —— 抹太多会让
// "文件不存在"和"目录不存在"撞成一个签名,那比不归一更糟。
func normalizeErrHead(head string) string {
	h := strings.ToLower(strings.TrimSpace(head))
	h = idRun.ReplaceAllString(h, "<id>")
	h = spaceRun.ReplaceAllString(h, " ")
	return h
}

// firstLineRunes 取首行并截断到 n 个 rune(错误文本常常是多行的堆栈)。
func firstLineRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return truncateRunes(strings.TrimSpace(s), n)
}

// FailureSignature 曝光给测试与诊断:返回 (签名, 归一化首行)。
func FailureSignature(tool, errText string) (sig, head string) {
	h := firstLineRunes(errText, FailureHeadRunes)
	return failureSig(tool, h), h
}
