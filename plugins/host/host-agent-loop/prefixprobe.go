// prefixprobe.go:请求前缀指纹探针(host-agent-loop 内,只读诊断)。
//
// 为什么需要它:提示缓存按「与上一次请求完全相同的最长前缀」给折扣,命中率低要么是
// **前缀被改写**(改代码能修),要么是**前缀太短/厂商侧过期**(改代码没用)。累计命中率
// 只是两个数字相除,分不出这两者。本探针每次组装请求时记一条指纹样本,并在指纹变化时
// 指出「第几条消息开始不一样」—— 跑一轮真实会话就能定性,不必猜。
//
// 实现上的克制(它挂在每轮请求的关键路径上):
//   - system 段进哈希时用全文(只取长度+头部会漏掉「长度相近、中间被改写」这类失配 ——
//     实测第一版就是这么漏的),但**不留正文副本**:哈希算完即弃;
//   - 非 system 消息仍只哈希「role + 长度 + 开头 48 字节」,比对够用且省;
//   - 样本环形上限 prefixProbeCap 条,不随会话增长。
package hostagentloop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// prefixProbeCap 保留的样本数(诊断只看最近若干条;再多只是占内存)。
const prefixProbeCap = 20

// prefixHeadBytes 每条消息取开头多少字节参与指纹与比对。
const prefixHeadBytes = 48

type prefixProbe struct {
	mu       sync.Mutex
	seq      int
	samples  []sdk.PrefixSample
	last     []string // 上一条样本的消息摘要(role|长度|前若干字节)
	lastHash string
	// 上一条样本的三段指纹(逐段对比才能说清「哪一段在变」)
	lastSysHash, lastToolsHash, lastHistHash string
}

// messageKey 一条消息参与指纹的摘要。
func messageKey(m sdk.LLMMessage) string {
	head := m.Content
	if len(head) > prefixHeadBytes {
		head = head[:prefixHeadBytes]
	}
	return fmt.Sprintf("%s|%d|%s", m.Role, len(m.Content), head)
}

// record 在一次请求组装完成后取样。
//
// **分三段分别取指纹**(system / tools / 历史),而不是把整个 messages 揉成一个哈希。
// 实测教训(2026-10-06 第一次跑 /cache):单一哈希只能回答「变了」,回答不了「哪一段变了」,
// 而答案恰恰在三段里 —— 定位不到就只剩猜。分段之后,“工具定义每轮在动”与“系统提示里
// 有一段每轮重算”是两行字的差别。
//
// tools 单独计入:模型侧把工具 schema 拼进提示的一部分,工具变了同样让前缀失配
// (只看消息会把这一类原因整个漏掉)。
//
// 三种结论必须分清(第一版混为一谈,把正常追加报成故障,把人带偏了):
//
//	appended  前缀一个字节没动,只是尾巴变长 ⇒ **这是缓存该命中的正常情况**;
//	changed   已有位置的内容被改写 ⇒ 真正的失配;
//	same      完全没变。
func (p *prefixProbe) record(msgs []sdk.LLMMessage, tools []sdk.ToolDefinition) {
	if p == nil {
		return
	}
	keys := make([]string, 0, len(msgs))
	sys, toolsH, hist := sha256.New(), sha256.New(), sha256.New()
	for _, t := range tools {
		fmt.Fprintf(toolsH, "t\x00%s\x00%s\x00", t.Name, t.Description)
	}
	sysChars, sysCount := 0, 0
	for i, m := range msgs {
		k := messageKey(m)
		keys = append(keys, k)
		// system 段**可能不止一条**(滚动摘要触发时会在第 2 位再插一条 system),
		// 所以按 role 归类而不是只认第 1 条 —— 只认首条会漏掉摘要那次插入。
		if m.Role == sdk.RoleSystem {
			sysCount++
			sysChars += len(m.Content)
			// system 用**全文**入指纹:只取长度+头几字节的话,「长度相近、中间被改写」
			// 这种最常见的失配会整个漏掉(实测第一版就是这么漏的)。
			fmt.Fprintf(sys, "%d\x00%s\x00", len(m.Content), m.Content)
			continue
		}
		fmt.Fprintf(hist, "%d\x00%s\x00", len(m.Content), k)
		_ = i
	}
	sHash, tHash, hHash := digest(sys), digest(toolsH), digest(hist)
	hash := joinHash(sHash, tHash, hHash)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	s := sdk.PrefixSample{
		Seq: p.seq, At: time.Now().Format("15:04:05"),
		Hash: hash, SysChars: sysChars, SysCount: sysCount, MsgCount: len(msgs),
		ToolCount: len(tools),
		SysHash:   sHash, ToolsHash: tHash, HistHash: hHash,
		DiffToPrev: "first",
	}
	switch {
	case p.lastHash == "":
		s.DiffToPrev = "first"
	case p.lastHash == hash:
		s.DiffToPrev = "same"
	case len(keys) > len(p.last) && keysEqualPrefix(p.last, keys):
		s.DiffToPrev = "appended"
		s.DiffWhere = fmt.Sprintf("尾部追加 %d 条(前缀未变)", len(keys)-len(p.last))
	default:
		s.DiffToPrev = "changed"
		s.DiffWhere = p.explainChange(keys, sHash, tHash, hHash)
	}
	p.samples = append(p.samples, s)
	if len(p.samples) > prefixProbeCap {
		p.samples = p.samples[len(p.samples)-prefixProbeCap:]
	}
	p.last, p.lastHash = keys, hash
	p.lastSysHash, p.lastToolsHash, p.lastHistHash = sHash, tHash, hHash
}

// explainChange 逐段说清「哪一段变了」。
//
// 为什么要分段而不是只报第一条不同的消息:三段的含义完全不同(工具 schema 在动 /
// 系统提示某段每轮重算 / 历史被改写或重排),混成一句「第 N 条变了」会把人引到错的那一段上
// —— 这正是第一次实测里发生的事:根因在 system 段被插了一条,报出来却像历史变了。
func (p *prefixProbe) explainChange(keys []string, sHash, tHash, hHash string) string {
	var parts []string
	if p.lastSysHash != "" && p.lastSysHash != sHash {
		parts = append(parts, "system 变了(提示被重算或被插入/替换)")
	}
	if p.lastToolsHash != "" && p.lastToolsHash != tHash {
		parts = append(parts, "工具定义变了")
	}
	if p.lastHistHash != "" && p.lastHistHash != hHash {
		parts = append(parts, "历史变了")
	}
	if where := whereChanged(p.last, keys); where != "" {
		parts = append(parts, "消息级:"+where)
	}
	if len(parts) == 0 {
		return "段指纹一致但总指纹不同(不应发生;请把这条当成探针自己的问题报上来)"
	}
	return strings.Join(parts, "; ")
}

// digest 一个哈希的短指纹。
func digest(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil))[:8] }

// joinHash 三段合成总指纹。
func joinHash(parts ...string) string {
	h := sha256.New()
	for _, x := range parts {
		fmt.Fprintf(h, "%s\x00", x)
	}
	return digest(h)
}

// keysEqualPrefix 旧序列是否完全是新序列的前缀(⇒ 只是尾部追加)。
func keysEqualPrefix(prev, now []string) bool {
	if len(now) < len(prev) {
		return false
	}
	for i := range prev {
		if prev[i] != now[i] {
			return false
		}
	}
	return true
}

// whereChanged 定位第一条不同的消息(指纹变了但没告诉我们是谁在变,诊断就等于没有)。
func whereChanged(prev, now []string) string {
	n := len(prev)
	if len(now) < n {
		n = len(now)
	}
	for i := 0; i < n; i++ {
		if prev[i] != now[i] {
			return fmt.Sprintf("第 %d 条(%s)%s", i+1, roleOf(now[i]), headOf(now[i]))
		}
	}
	switch {
	case len(now) > len(prev):
		return fmt.Sprintf("多出 %d 条消息(从第 %d 条起)", len(now)-len(prev), len(prev)+1)
	case len(now) < len(prev):
		return fmt.Sprintf("少了 %d 条消息(原第 %d 条之后没有了)", len(prev)-len(now), len(now)+1)
	default:
		return "消息条数与摘要都没变(内容在摘要窗口之外变化)"
	}
}

func roleOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i]
		}
	}
	return "?"
}

func headOf(key string) string {
	// key = role|len|head —— 取第三段开头 24 字节,够认出是「哪段内容」变了
	parts := splitN(key, '|', 3)
	if len(parts) < 3 {
		return ""
	}
	head := parts[2]
	if len(head) > 24 {
		head = head[:24]
	}
	return ": " + head + "…"
}

func splitN(s string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// PrefixSamples 返回最近的样本(时间正序)。
func (p *prefixProbe) PrefixSamples(limit int) []sdk.PrefixSample {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if limit <= 0 || limit > len(p.samples) {
		limit = len(p.samples)
	}
	return append([]sdk.PrefixSample(nil), p.samples[len(p.samples)-limit:]...)
}

// PrefixTurns 本会话累计请求数。
func (p *prefixProbe) PrefixTurns() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seq
}

var _ sdk.PrefixProbe = (*prefixProbe)(nil)
