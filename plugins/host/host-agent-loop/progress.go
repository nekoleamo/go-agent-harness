package hostagentloop

import (
	"strings"
	"sync"
	"time"
)

// progressWindow 进度合并窗口(100ms)。
//
// 为什么需要合并:工具可能每秒吐几百块(例如 `yes | head -n 100000`、
// 高频日志的 tail -f)。每块都发一个事件 ⇒ 事件总线与界面被刷爆
// (界面每帧重排、终端掉帧),而用户其实看不清每秒 10 块以上的增量。
//
// 为什么不能丢:进度是用户判断「在动 / 卡住了 / 要等多久」的唯一依据,
// 丢块等于骗人。所以这里是**延迟合并**而不是采样:窗口内的块先攒着,
// 到点(或工具结束)再一次性发出 —— 文本逐字不丢,帧率有界。
const progressWindow = 100 * time.Millisecond

// progressThrottle 工具进度的节流合并器(每次工具调用一个)。
//
// 语义不变量:
//  1. 不丢文本:窗口内累积的块一定会在 flush 或下一个窗口到期时发出;
//  2. 有序:发出的顺序 = 收到的顺序(单锁保证);
//  3. 不阻塞工具太久:add 只在窗口到期时同步发一次,其余时间只是拼字符串。
type progressThrottle struct {
	mu   sync.Mutex
	pend strings.Builder
	tool string    // 最近一次上报的工具名(外部进程回传的块可能与首个块同名)
	last time.Time // 上次发出时刻
	emit func(text string)
}

// add 收到一段增量。
//
// 首个块立即发出(不等窗口):用户要尽快看到「在动」;之后的块按 100ms 窗口合并。
func (p *progressThrottle) add(tool, chunk string) {
	if chunk == "" {
		return
	}
	p.mu.Lock()
	p.pend.WriteString(chunk)
	if tool != "" {
		p.tool = tool
	}
	now := time.Now()
	due := p.last.IsZero() || now.Sub(p.last) >= progressWindow
	var text string
	if due {
		text = p.pend.String()
		p.pend.Reset()
		p.last = now
	}
	p.mu.Unlock()
	if text != "" && p.emit != nil {
		p.emit(text)
	}
}

// flush 发出窗口内剩余的增量(工具结束时调用)。
func (p *progressThrottle) flush() {
	p.mu.Lock()
	text := p.pend.String()
	p.pend.Reset()
	if text != "" {
		p.last = time.Now()
	}
	p.mu.Unlock()
	if text != "" && p.emit != nil {
		p.emit(text)
	}
}
