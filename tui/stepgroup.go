package tui

// 工具分组(C3 · step grouping):一轮里 5 个并行工具 = 5 行调用 + 5 行结果 = 10 行,
// 翻十行看不到自己关心的东西。
//
// 形态:每一步(一次模型请求)一个**头行**「第 N 步 · K 个工具 · Xs」,
// 本步的工具行归为一个组;点头行可**折叠/展开**该组(手动,不像结果行那样默认折)。
//
// 为什么**不默认折叠**(2026-10-21 改):原设计是"步结束自动折叠成一���" ——
// 但真机验收用例(条目 41)当场红了:它要求屏幕上一直看得见工具**调用行**
// (「⚙ shell(echo 甲四十一)」里的命令文本)。
// 「过程可见、结果明确」是这一层的底线,折叠是锦上添花 ⇒ 顺序不能反:
// **默认展开,想收就点一下**。
//
// 为什么不做成"中途插一行头行":会话流的若干状态是**按行下标**索引的
// (结果行展开态 FoldOpen、C1 的工具行索引 toolRow)——中途插入会让那些下标全部错位,
// 展开态张冠李戴、实时输出写到别的工具行上。故头行在**步开始时**就建好(占位),
// 步结束时**回填**内容(工具数/耗时/失败数);没有工具的步则整行不显示
// (渲染时跳过,不影响任何下标)。
//
// 三条不变量:
//  1. 头行永远在步的最前 ⇒ 不动任何已有行的下标;
//  2. 折叠只藏**本步的工具行**(调用+结果),助手正文与工具执行期间的 meta 行永远可见
//     (结论与关键提示不能被藏起来 —— 踩过一次:分组原按"行号区间"收,把
//     「❓ 提问已结束(其它渠道作答或已取消)」一起收了,跨渠道验收用例直接红);
//  3. 折叠态下头行仍报**结论**(工具数/失败数)⇒ 折叠不等于"看不见发生了什么"。

import (
	"fmt"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stepGroup 一步的工具分组(键 = 头行在 Lines 里的下标)。
type stepGroup struct {
	// lines 本步的**工具行**下标(按序;只收工具行)。
	//
	// 为什么记行号而不是记一段区间:区间会把夹在中间的**别的行**也收进去 ——
	// 比如工具执行期间的 meta 行(「提问已由其它渠道处理」)—— 折叠时一起消失,
	// 用户刚看到的关键提示就没了。只收工具行,别的行永远可见。
	lines  []int
	tools  int // 工具调用数
	failed int // 失败的工具数
}

// startStep 步开始:建头行(占位)+ 记分组。
func (s *State) startStep(ev *sdk.SessionEvent) {
	s.stepNo++
	head := len(s.Lines)
	ln := Line{Kind: "meta", Text: fmt.Sprintf("第 %d 步", s.stepNo)}
	// C2a 同款:实时耗时起点(仅实时事件;重放不设)。
	if !s.replaying {
		ln.Since = time.Now()
	}
	s.Lines = append(s.Lines, ln)
	if s.stepGroups == nil {
		s.stepGroups = map[int]*stepGroup{}
	}
	s.stepGroups[head] = &stepGroup{}
	if s.stepFold == nil {
		s.stepFold = map[int]bool{}
	}
	// 默认展开(用户要看着工具跑、看着实时输出;验收用例也要求调用行一直可见)。
	// 想收起点一下头行 —— 手动折叠是锦上添花,自动折叠会藏掉"调用了什么"。
	s.stepFold[head] = true
	s.stepHead = head
	s.stepOpen = true
	s.stepStartTS = ev.TS
	if s.stepStartTS.IsZero() {
		s.stepStartTS = time.Now()
	}
}

// noteStepToolCall 本步新增了一个工具**调用**行 ⇒ 归入分组区间并计数。
func (s *State) noteStepToolCall() {
	if !s.stepOpen {
		return
	}
	g, ok := s.stepGroups[s.stepHead]
	if !ok {
		return
	}
	g.lines = append(g.lines, len(s.Lines)-1)
	g.tools++
}

// noteStepToolLine 本步新增了一个工具**结果**行 ⇒ 归入分组区间(收尾时才知道分组到哪里)。
func (s *State) noteStepToolLine(failed bool) {
	if !s.stepOpen {
		return
	}
	g, ok := s.stepGroups[s.stepHead]
	if !ok {
		return
	}
	g.lines = append(g.lines, len(s.Lines)-1)
	if failed {
		g.failed++
	}
}

// endStep 步结束:回填头行(工具数/耗时/失败数)+ 定折叠态。
func (s *State) endStep(ev *sdk.SessionEvent) {
	if !s.stepOpen {
		return
	}
	head := s.stepHead
	g, ok := s.stepGroups[head]
	s.stepOpen = false
	s.stepHead = 0
	if !ok {
		return
	}
	if g.tools < 2 {
		return // 工具少于 2 个:没有"成组"可言 ⇒ 头行不显示(不添噪声)
	}
	dur := ev.TS.Sub(s.stepStartTS)
	if dur < 0 {
		dur = 0
	}
	text := fmt.Sprintf("第 %d 步 · %d 个工具 · %s", s.stepNo, g.tools, fmtDur(dur))
	if g.failed > 0 {
		text += fmt.Sprintf(" · %d 个失败", g.failed)
	}
	s.Lines[head].Text = text
	s.Lines[head].Since = time.Time{} // 步结束了:不再实时计时
	// 保持展开(默认可见);点头行可手动收起这一组(头行报结论,点开看明细)。
}

// ToggleStep 切换某个头行所在分组的展开/折叠。返回切换是否生效。
func (s *State) ToggleStep(lineIdx int) bool {
	g, ok := s.stepGroups[lineIdx]
	if !ok || g.tools < 2 {
		return false // 不是分组头行(或本步工具不足 2 个 ⇒ 无组)
	}
	if s.stepFold == nil {
		s.stepFold = map[int]bool{}
	}
	s.stepFold[lineIdx] = !s.stepFold[lineIdx]
	return true
}

// stepExpanded 分组是否展开(默认折叠)。
func (s *State) stepExpanded(head int) bool {
	return s.stepFold != nil && s.stepFold[head]
}

// stepHeadOf 找出包含该行的分组头行下标(不在任何分组里 ⇒ -1)。
//
// 用处:搜索命中落在**折叠中的**工具行里时,得先把那一组展开 ——
// 否则「跳到命中处」等于没跳(那一行根本不在屏上)。
func (s *State) stepHeadOf(lineIdx int) int {
	for head, g := range s.stepGroups {
		for _, i := range g.lines {
			if i == lineIdx {
				return head
			}
		}
	}
	return -1
}

// hiddenStepLines 折叠中的分组所藏起来的行号集合(渲染时跳过)。
//
// 返回 nil 表示"没有要藏的"⇒ 渲染热路径不做额外判断。
func (s *State) hiddenStepLines() map[int]bool {
	if len(s.stepGroups) == 0 {
		return nil
	}
	var hidden map[int]bool
	mark := func(i int) {
		if hidden == nil {
			hidden = map[int]bool{}
		}
		hidden[i] = true
	}
	for head, g := range s.stepGroups {
		if g.tools < 2 {
			mark(head) // 工具不足 2 个:头行不显示(不添噪声)
			continue
		}
		if s.stepExpanded(head) {
			continue
		}
		for _, i := range g.lines {
			mark(i) // 折叠:只收工具行(夹在中间的 meta 行永远可见)
		}
	}
	return hidden
}
