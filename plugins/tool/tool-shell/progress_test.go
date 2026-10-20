package toolshell

// 增量输出采集器(C1)的用例。
//
// 要钉的是三条不变量:
//  1. **不改变结果**:采集器收集到的全文 == 旧 CombinedOutput 的输出(逐字);
//  2. **不丢文本**:合并/节流只推迟发送,收尾时零头必须发出去(最后一行不能丢);
//  3. **有序**:块按产生顺序送达(乱序的日志比没有日志更糟)。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// chunkSink 记录收到的进度块(顺序敏感)。
type chunkSink struct {
	mu     sync.Mutex
	chunks []string
}

func (s *chunkSink) add(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, c)
}

func (s *chunkSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.chunks...)
}

func (s *chunkSink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.chunks, "")
}

// ctxWithSink 造一个带进度上报出口的 ctx。
func ctxWithSink(sink *chunkSink) context.Context {
	return sdk.WithToolProgressReporter(context.Background(), func(_, chunk string) error {
		sink.add(chunk)
		return nil
	})
}

// TestProgressWriterKeepsFullOutput 采集器收集到的输出必须与子进程实际输出逐字相同。
func TestProgressWriterKeepsFullOutput(t *testing.T) {
	sink := &chunkSink{}
	w := newProgressWriter(ctxWithSink(sink), "shell")
	if _, err := w.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("world\n")); err != nil {
		t.Fatal(err)
	}
	w.close()
	if got := w.String(); got != "hello world\n" {
		t.Fatalf("采集到的输出应逐字不变,got %q", got)
	}
}

// TestProgressWriterDeliversAllTextInOrder 所有文本都要送到,且顺序不乱。
func TestProgressWriterDeliversAllTextInOrder(t *testing.T) {
	sink := &chunkSink{}
	w := newProgressWriter(ctxWithSink(sink), "shell")
	for _, s := range []string{"一", "二", "三", "四"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	w.close()
	if got := sink.text(); got != "一二三四" {
		t.Fatalf("进度文本应完整且有序,got %q", got)
	}
}

// TestProgressWriterMergesSmallWrites 小块写入要合并(否则事件总线被刷爆):
// 200 次 1 字节写入不能变成 200 帧。
func TestProgressWriterMergesSmallWrites(t *testing.T) {
	sink := &chunkSink{}
	w := newProgressWriter(ctxWithSink(sink), "shell")
	for i := 0; i < 200; i++ {
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	w.close()
	chunks := sink.all()
	if len(chunks) == 0 {
		t.Fatal("应有进度块发出")
	}
	if len(chunks) > 20 {
		t.Fatalf("小块写入应合并成少数几帧,got %d 帧", len(chunks))
	}
	if got := sink.text(); got != strings.Repeat("x", 200) {
		t.Fatalf("合并后文本仍应完整,got %d 字节", len(got))
	}
}

// TestProgressWriterCloseFlushesRemainder 收尾必须把零头发出去(最后一行不能丢)。
func TestProgressWriterCloseFlushesRemainder(t *testing.T) {
	sink := &chunkSink{}
	w := newProgressWriter(ctxWithSink(sink), "shell")
	// 第一块立即发(让用户尽快看到"在动"),第二块落在合并窗口内 ⇒ 收尾才发。
	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	w.close()
	if got := sink.text(); got != "firstsecond" {
		t.Fatalf("收尾应把零头也发出去,got %q", got)
	}
}

// TestProgressWriterWithoutReporter 没有上报出口时(旧宿主/直连测试)照常工作。
func TestProgressWriterWithoutReporter(t *testing.T) {
	w := newProgressWriter(context.Background(), "shell")
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	w.close()
	if got := w.String(); got != "x" {
		t.Fatalf("输出仍应被收集,got %q", got)
	}
}

// TestProgressWriterFlushesOnInterval 长时间无新写入时,到点也要发一帧
// (命令还活着但没输出 ⇒ 界面不该以为它死了)。
func TestProgressWriterFlushesOnInterval(t *testing.T) {
	sink := &chunkSink{}
	w := newProgressWriter(ctxWithSink(sink), "shell")
	if _, err := w.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	// 模拟"过了合并窗口":把上次发送时刻往回拨,下一次写必须触发发送。
	w.mu.Lock()
	w.last = time.Now().Add(-2 * progressFlushInterval)
	w.mu.Unlock()
	if _, err := w.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	w.close()
	if got := sink.text(); got != "ab" {
		t.Fatalf("到窗口应发一帧,got %q", got)
	}
}
