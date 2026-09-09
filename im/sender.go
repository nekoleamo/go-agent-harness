// 出站预算层(P1 §6.1):微信/QQ 共用的发送器抽象。统一"分块 ≤MaxChunk 字符、
// 一轮 ≤MaxChunks 块(超限截断+提示)、块间 Gap 间隔、短窗口 Burst 条数记账"——
// 各通道按平台规则填 Budget 生效:微信(单条 ~2000/一轮 ~10 块/块间 300ms)、
// QQ(单条 4000/被动窗口内逐块)。
// 与"呈现层"分工:render(富文本→markdown 等)在 transport 内先行决策,
// 需要分块发送的纯文本统一交给 Sender。分块全程 []rune 空间切(rune 安全,
// 不会从多字节字符中间截断产生非法 UTF-8——微信/QQ 长回复乱码教训)。
package im

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Budget 通道出站预算参数(per-channel;0 = 不限制对应维度)。
type Budget struct {
	MaxChunk   int           // 单条最大字符数
	MaxChunks  int           // 一轮发送最大块数(0 = 不限;超限截断 + TruncHint)
	Gap        time.Duration // 块间间隔(防连发触发短窗口截断)
	Burst      int           // 窗口内最大条数(短窗口条数预算;0 = 不限)
	BurstWin   time.Duration // burst 窗口时长(自首条起滑移)
	TruncHint  string        // 截断提示文案(追加在末块后;空 = 仅截断)
}

// Sender 出站发送器(并发安全;每通道一个实例,回合串行下无竞争)。
type Sender struct {
	b *Budget

	mu         sync.Mutex
	burstStart time.Time
	burstSent  int
}

// NewSender 构造发送器(预算参数固化;b 不可为 nil)。
func NewSender(b *Budget) *Sender {
	return &Sender{b: b}
}

// Send 分块发送文本:整除 rune 安全块 → 逐块调 send(块间 Gap;burst 超限暂停至窗口滑移)。
// 超 MaxChunks 截断并追加 TruncHint(自身也算一条)。send 返回错误即中止。
func (s *Sender) Send(_ context.Context, text string, send func(chunk string) error) error {
	if text == "" {
		return nil
	}
	chunks := SplitText(text, s.b.MaxChunk)
	trunc := s.b.MaxChunks > 0 && len(chunks) > s.b.MaxChunks // MaxChunks<=0 = 不限
	if trunc {
		chunks = chunks[:s.b.MaxChunks]
	}
	for i, ch := range chunks {
		if err := s.awaitBurst(); err != nil {
			return err
		}
		if err := send(ch); err != nil {
			return err
		}
		if i < len(chunks)-1 && s.b.Gap > 0 {
			time.Sleep(s.b.Gap)
		}
	}
	if trunc {
		if err := s.awaitBurst(); err != nil {
			return err
		}
		hint := s.b.TruncHint
		if hint == "" {
			hint = "⚠️ 回复过长已截断;请回复 continue 获取剩余内容"
		}
		return send(hint)
	}
	return nil
}

// awaitBurst burst 条数预算:窗口内已达上限则等待窗口滑移(不丢内容,只延后)。
// 无 burst 限制立即返回。
func (s *Sender) awaitBurst() error {
	if s.b.Burst <= 0 {
		return nil
	}
	for {
		s.mu.Lock()
		now := time.Now()
		if s.burstStart.IsZero() || now.Sub(s.burstStart) >= s.b.BurstWin {
			s.burstStart = now
			s.burstSent = 0
		}
		if s.burstSent < s.b.Burst {
			s.burstSent++
			s.mu.Unlock()
			return nil
		}
		wait := s.b.BurstWin - now.Sub(s.burstStart)
		s.mu.Unlock()
		if wait <= 0 {
			continue
		}
		time.Sleep(wait)
	}
}

// SplitText 按 limit 字符切分(仅 []rune 操作,块均合法 UTF-8):
// 优先段落(空行)→ 行 → 空格 → 硬切;空白块丢弃;空输入返回空。
func SplitText(text string, limit int) []string {
	rs := []rune(text)
	if len(rs) == 0 {
		return nil
	}
	if limit <= 0 || len(rs) <= limit {
		return []string{text}
	}
	var chunks []string
	for start := 0; start < len(rs); {
		end := cutRunes(rs, start, limit)
		if piece := strings.TrimSpace(string(rs[start:end])); piece != "" {
			chunks = append(chunks, piece)
		}
		if end <= start {
			break // 防御:切点不推进则终止(limit 非法/全空白)
		}
		start = end
	}
	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}

// cutRunes 在 rs[start:start+limit] 内找最佳切点([]rune 下标):
// 段落空行 > 换行 > 空格 > 硬切。返回切点下标(终点)。
func cutRunes(rs []rune, start, limit int) int {
	end := start + limit
	if end >= len(rs) {
		return len(rs)
	}
	window := string(rs[start:end])
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i > 0 {
			return start + len([]rune(window[:i+len(sep)]))
		}
	}
	// 硬切:窗口最后是完整 rune(rs slice 天然合法),直接返回
	return start + len([]rune(window))
}

// BudgetDefaults 出站预算默认表(per-channel;通道插件构造时按平台规则覆盖)。
var BudgetDefaults = map[string]Budget{
	"wechat": {MaxChunk: 2000, MaxChunks: 10, Gap: 300 * time.Millisecond},
	"qq":     {MaxChunk: 4000, MaxChunks: 0, Gap: 300 * time.Millisecond},
}

// String 预算摘要(诊断/状态展示)。
func (b *Budget) String() string {
	if b == nil {
		return ""
	}
	return fmt.Sprintf("chunk=%d limits=%d gap=%v burst=%d", b.MaxChunk, b.MaxChunks, b.Gap, b.Burst)
}