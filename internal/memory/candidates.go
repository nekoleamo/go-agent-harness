// 候选池(记忆层 M2 的**前置件**之一,2026-10-02)。
//
// 为什么单独一个文件而不是给记忆条目加状态字段:记忆是「一行一条、注入按时间倒序」的
// 极简格式,它的全部价值是**人能直接改**。给条目加 status 列会让解析、正文、注入三处
// 都要开始处理「这一行是不是还没生效」—— 而候选池的语义恰恰是「**还没生效**」。
// 两者物理分开,「候选绝不会进上下文」就是构造上的事实,不需要靠过滤维持。
//
// 限流(前置件之二)在这里,不在调用方:候选是**未来唯一可能由模型写入**的东西
// (M2 自动提取)。如果限流散落在各个产出方,「每会话/每天最多几条」这条约束迟早被
// 某个新产出方绕过 —— 约束必须在**唯一的写入口**上。
//
// 明确不做(边界,别误以为已做):**没有任何自动产出候选的路径**。本文件只提供
// 「人手动提候选 → 批量确认 → 进记忆」这条链。自动提取是 M2 本体,须等观察到
// 「记忆确实有用」之后再开,且必须再过审批面(自动写记忆 = 静默提权)。
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 候选池的量级上限。取值理由:
//   - 候选是**待办**,不是资料 —— 一次攒 50 条没人会看,攒 12 条还能一次确认完;
//   - 上限本身也是防「某个失控的产出方一次灌满」的兜底(限流的第一道)。
const (
	// MaxCandidates 候选池总量上限。
	MaxCandidates = 12
	// MaxCandidatesPerDay 每天最多新增几条候选(按条目日期计数)。
	MaxCandidatesPerDay = 5
)

// CandidatesPath 候选池文件(与记忆同目录、同行格式 ⇒ 人可直接编辑)。
func CandidatesPath() string { return filepath.Join(Dir(), "candidates.md") }

// Propose 提一条候选(**不进上下文**)。source 空 = 手工提,无来源可追溯。
//
// 限流在这里生效(唯一写入口):已超总量/当日额度 ⇒ 显式报错并说清是哪一条顶住了,
// 不静默丢弃 —— 静默丢弃会让人以为「提了但没生效」。
func Propose(content, source string) error {
	cur, err := Read(CandidatesPath())
	if err != nil {
		return err
	}
	if len(cur) >= MaxCandidates {
		return fmt.Errorf("memory: 候选池已满(%d 条);先确认或驳回几条再提(候选不是资料,攒太多没人会看)", MaxCandidates)
	}
	today := time.Now().Format("2006-01-02")
	n := 0
	for _, e := range cur {
		if e.Date == today {
			n++
		}
	}
	if n >= MaxCandidatesPerDay {
		return fmt.Errorf("memory: 今天已提 %d 条候选(上限 %d);明天再提,或先确认/驳回已有的", n, MaxCandidatesPerDay)
	}
	return Append(CandidatesPath(), content, source)
}

// ListCandidates 候选展示行(新的在前;与 List 同一种行格式与排序口径)。
func ListCandidates() []string {
	es, err := Read(CandidatesPath())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(es))
	for i, e := range SortedForDisplay(es) {
		line := fmt.Sprintf("%d. [%s] %s", i+1, e.Date, e.Content)
		if e.Source != "" {
			line += " (来源: 会话 " + e.Source + ")"
		}
		out = append(out, line)
	}
	return out
}

// Accept 把第 index 条候选(展示序号,新的在前)转正为记忆,返回被转正的内容。
//
// 顺序刻意是「**先写记忆、再删候选**」:反过来的话,写记忆失败就等于候选丢了(人要重提);
// 而这个顺序的失败态是「记忆已写、候选还在」—— 再点一次会撞 Append 的同日去重,
// 不会写两条,且提示语会让人知道发生了什么。
func Accept(index int) (string, error) {
	cur, err := Read(CandidatesPath())
	if err != nil {
		return "", err
	}
	if index < 1 || index > len(cur) {
		return "", fmt.Errorf("memory: 候选序号 %d 越界(共 %d 条;用 /memory candidates 看编号)", index, len(cur))
	}
	victim := SortedForDisplay(cur)[index-1]
	if err := Append(UserPath(), victim.Content, victim.Source); err != nil {
		// 已经进记忆了(同日同内容)⇒ 当作成功,并把候选清掉,别让用户以为没生效。
		if strings.Contains(err.Error(), "已经有同一条记忆") {
			if _, rerr := Remove(CandidatesPath(), index); rerr != nil {
				return victim.Content, fmt.Errorf("候选已在记忆中,但清理候选失败:%w", rerr)
			}
			return victim.Content + "(此前已记过,已从候选池移除)", nil
		}
		return "", err
	}
	if _, err := Remove(CandidatesPath(), index); err != nil {
		return victim.Content, fmt.Errorf("已写入记忆,但清理候选失败:%w", err)
	}
	return victim.Content, nil
}

// AcceptAll 把候选池里**全部**候选一次性转正(批量确认的主路径),返回转正了几条。
// 任一条失败即停并如实报已转正几条 —— 半截状态必须说清楚,不能报「都好了」。
func AcceptAll() (int, error) {
	n := 0
	for {
		es, err := Read(CandidatesPath())
		if err != nil {
			return n, err
		}
		if len(es) == 0 {
			return n, nil
		}
		// 每次都转**最新**那条:序号会随删除而变,固定用序号 1 会在多次删除后指错条目。
		if _, err := Accept(1); err != nil {
			return n, err
		}
		n++
		if n > MaxCandidates+1 { // 兜底:不该发生(池子上限 12),但绝不允许死循环
			return n, fmt.Errorf("memory: 批量确认超过 %d 次仍未清空(池子状态异常,已停止)", MaxCandidates)
		}
	}
}

// Reject 驳回第 index 条候选(丢弃,不进记忆),返回被丢的内容(用于回执)。
func Reject(index int) (string, error) {
	e, err := Remove(CandidatesPath(), index)
	if err != nil {
		return "", err
	}
	return e.Content, nil
}

// RejectAll 清空候选池,返回丢了几条。
func RejectAll() (int, error) {
	path := CandidatesPath()
	es, err := Read(path)
	if err != nil {
		return 0, err
	}
	n := len(es)
	if n == 0 {
		return 0, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return n, nil
}

// CandidateQuota 候选池用量(用于回显「还能提几条」):已用、总量上限、今日已提、今日上限。
func CandidateQuota() (used, limit, today, todayLimit int) {
	es, err := Read(CandidatesPath())
	if err != nil {
		return 0, MaxCandidates, 0, MaxCandidatesPerDay
	}
	d := time.Now().Format("2006-01-02")
	for _, e := range es {
		if e.Date == d {
			today++
		}
	}
	return len(es), MaxCandidates, today, MaxCandidatesPerDay
}
