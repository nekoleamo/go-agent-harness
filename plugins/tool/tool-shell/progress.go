// progress.go:工具执行期间的**增量输出**采集(2026-10-20 C1)。
//
// 为什么需要:一个跑 3 分钟的构建,界面上只有一个转圈 —— 用户不知道它在动、在等什么、
// 是不是卡住了。而"取消"按钮在那 3 分钟里看上去也像失灵。增量输出把这三件事变成立即可见的。
//
// 三条纪律:
//   - **不改变结果**:完整输出仍照旧进结果 JSON(模型看到的与从前逐字一致);
//     增量只是**额外**的一条通道,给界面用。
//   - **有界**:按 4KB / 200ms 合并后再发 —— 否则一个 10MB 的构建日志会把事件总线打爆
//     (每秒几千帧 ⇒ UI 掉帧、内存涨)。
//   - **尽力而为**:sink 出错/没有 sink ⇒ 静默跳过,绝不影响工具本身的成败。
//   - **有序**:块必须**按产生顺序**送达(输出文本乱序 = 界面显示的日志对不上)。
//     为此用一个专职发送协程 + 有序队列,而不是在每次 flush 里各起一个 goroutine
//     (那样两块可能颠倒,终端里"编译错误"会出现在"开始编译"之前)。
package toolshell

import (
	"context"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// progressFlushBytes 累计多少字节就发一次(约 4KB:一行典型 80B ⇒ 50 行一帧,不至于刷屏)。
const progressFlushBytes = 4 << 10

// progressFlushInterval 距上次发送超过这么久就发一次(长时间无输出的命令也要让界面知道"还活着")。
const progressFlushInterval = 200 * time.Millisecond

// progressQueueDepth 待发送块的上限(64 块 × 4KB ≈ 256KB 积压上限)。
// 满了就**阻塞**发送方(背压回压到命令的输出管道):宁可让命令慢一点,也不丢输出文本。
const progressQueueDepth = 64

// progressWriter 把子进程输出**同时**写进结果缓冲与进度通道。
//
// 用法:cmd.Stdout = pw; cmd.Stderr = pw —— 两个字段指向**同一个** writer 时,
// os/exec 用同一管道读(与 CombinedOutput 的合并语义一致),所以最终 output
// 与从前逐字相同;而 pw 额外把增量喂给 sink(界面用)。
type progressWriter struct {
	ctx  context.Context // 进度通道从这里取(ReportToolProgress)
	tool string          // 工具名(进度事件里带)

	mu   sync.Mutex
	buf  *byteBuffer
	pend []byte // 未满一帧的零头(下次写时先补发)
	last time.Time

	snd       chan string   // 有序发送队列
	sndDone   chan struct{} // 发送协程退出信号
	closeOnce sync.Once
}

// byteBuffer 极简字节缓冲(避免为一个用途引入 bytes.Buffer 的逃逸分析噪声)。
type byteBuffer struct {
	b []byte
}

func (b *byteBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *byteBuffer) String() string { return string(b.b) }

// newProgressWriter 构造采集器(ctx 里无进度通道 ⇒ 只收集不发送)。
//
// ctx 必须**存进 writer**:进度上报要从 ctx 取 reporter(见 flushLocked)——
// 丢了 ctx 就等于进度永远发不出去(工具照常工作,但界面看不到过程)。
func newProgressWriter(ctx context.Context, tool string) *progressWriter {
	w := &progressWriter{
		ctx: ctx, tool: tool, buf: &byteBuffer{},
		snd: make(chan string, progressQueueDepth), sndDone: make(chan struct{}),
	}
	go w.sendLoop()
	return w
}

// sendLoop 专职发送协程:按队列顺序把块交给 sink。
//
// 为何不"每块一个 goroutine":那样两块可能颠倒(输出乱序比没有输出更糟)。
// 为何不"发送方直接调 sink":sink 慢(跨进程回传是 RPC)会卡住命令的输出管道。
func (w *progressWriter) sendLoop() {
	defer close(w.sndDone)
	for chunk := range w.snd {
		sdk.ReportToolProgress(w.ctx, w.tool, chunk)
	}
}

// Write 子进程输出写入:先进缓冲,再按阈值/时间合并成帧发出。
func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.buf.Write(p)
	w.pend = append(w.pend, p...)
	now := time.Now()
	if len(w.pend) >= progressFlushBytes || w.last.IsZero() ||
		(w.last != (time.Time{}) && now.Sub(w.last) >= progressFlushInterval) {
		w.flushLocked(now)
	}
	return len(p), nil
}

// flushLocked 发一帧(调用方持锁)。
func (w *progressWriter) flushLocked(now time.Time) {
	if len(w.pend) == 0 {
		w.last = now
		return
	}
	chunk := string(w.pend)
	w.pend = w.pend[:0]
	w.last = now
	// 队列满 = 发送侧跟不上 ⇒ 阻塞(背压),不丢文本。
	w.snd <- chunk
}

// flush 收尾:把零头发完(命令刚结束时可能还有半行)。
func (w *progressWriter) flush() {
	w.mu.Lock()
	w.flushLocked(time.Now())
	w.mu.Unlock()
}

// close 关闭发送通道并等发送协程排空(收尾块必须送达:否则最后一行输出会丢)。
func (w *progressWriter) close() {
	w.closeOnce.Do(func() {
		w.flush()
		close(w.snd)
		<-w.sndDone
	})
}

// String 返回完整输出(与 CombinedOutput 语义一致:stdout+stderr 合并)。
func (w *progressWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
