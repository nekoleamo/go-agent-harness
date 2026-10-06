// prefixprobe.go:请求前缀指纹探针(host-agent-loop 内,只读诊断)。
//
// 为什么需要它:提示缓存按「与上一次请求完全相同的最长前缀」给折扣,命中率低要么是
// **前缀被改写**(改代码能修),要么是**前缀太短/厂商侧过期**(改代码没用)。累计命中率
// 只是两个数字相除,分不出这两者。本探针每次组装请求时记一条指纹样本,并在指纹变化时
// 指出「第几条消息开始不一样」—— 跑一轮真实会话就能定性,不必猜。
//
// 实现上的克制(它挂在每轮请求的关键路径上):
//   - 只哈希「role + 内容长度 + 开头 48 字节」,**不序列化全文、不留正文副本**;
//   - 样本环形上限 prefixProbeCap 条,不随会话增长;
//   - 上一条样本的消息摘要也只留这么多,比对够用。
package hostagentloop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	last     []string // 上一条样本的消息摘要(role|前若干字节)
	lastHash string
}

// messageKey 一条消息参与指纹的摘要。
func messageKey(m sdk.LLMMessage) string {
	head := m.Content
	if len(head) > prefixHeadBytes {
		head = head[:prefixHeadBytes]
	}
	return fmt.Sprintf("%s|%d|%s", m.Role, len(m.Content), head)
}

// record 在一次请求组装完成后取样。tools 参与指纹:工具 schema 变了同样会让前缀失配
// (模型侧把 tools 计入提示的一部分),只看消息会漏掉这一类原因。
func (p *prefixProbe) record(msgs []sdk.LLMMessage, tools []sdk.ToolDefinition) {
	if p == nil {
		return
	}
	keys := make([]string, 0, len(msgs)+len(tools))
	h := sha256.New()
	sysChars := 0
	for i, m := range msgs {
		k := messageKey(m)
		keys = append(keys, k)
		fmt.Fprintf(h, "%d\x00%s\x00", len(m.Content), k)
		if i == 0 && m.Role == sdk.RoleSystem {
			sysChars = len(m.Content)
		}
	}
	for _, t := range tools {
		fmt.Fprintf(h, "t\x00%s\x00%s\x00", t.Name, t.Description)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	hash := sum[:8]

	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	s := sdk.PrefixSample{
		Seq: p.seq, At: time.Now().Format("15:04:05"),
		Hash: hash, SysChars: sysChars, MsgCount: len(msgs),
		DiffToPrev: "first",
	}
	switch {
	case p.lastHash == "":
		// 首条:没有可比对象
	case p.lastHash == hash:
		s.DiffToPrev = "same"
	default:
		s.DiffToPrev = "changed"
		s.DiffWhere = whereChanged(p.last, keys)
	}
	p.samples = append(p.samples, s)
	if len(p.samples) > prefixProbeCap {
		p.samples = p.samples[len(p.samples)-prefixProbeCap:]
	}
	p.last, p.lastHash = keys, hash
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
